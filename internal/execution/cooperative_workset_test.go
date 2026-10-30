package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"github.com/lazyun/devbridge/internal/workset"
)

func cooperativeWorksetFixture(t *testing.T) workset.RuntimeWorkset {
	t.Helper()
	root := t.TempDir()
	commit := strings.Repeat("a", 40)
	w := workset.RuntimeWorkset{AgentConfigFingerprint: strings.Repeat("a", 64), ProtocolVersion: 1, AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1, ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", RunID: "run", WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: "repository", RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "tree", CanonicalRoot: root, RootFileIdentity: "root-id", OriginCommit: commit, StartHEAD: commit, StartSnapshot: commit, Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}, RuntimeRootIdentity: "root-id"}}, BackendConfigID: "backend", BackendConfigFingerprint: "fingerprint", Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: agentpolicy.CooperativeAdapterIdentity, RuntimeProtocolIdentity: agentpolicy.CooperativeProtocolIdentity, CreatedAt: time.Unix(1800000000, 0).UTC()}
	w, err := w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	return w
}

type cooperativeReplayRuns struct {
	RunService
	value workset.RuntimeWorkset
	err   error
}

func (r cooperativeReplayRuns) RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error) {
	return r.value, r.err
}

func TestCooperativePreparationDoesNotCreateNativeSandbox(t *testing.T) {
	for _, verification := range []bool{false, true} {
		w := cooperativeWorksetFixture(t)
		c := &Coordinator{Codex: DefaultCodexFactory{}}
		got, err := c.prepareTaskPermissions(context.Background(), w, verification)
		if err != nil || got.TaskPermissions != nil || got.ContentHash != w.ContentHash || got.AgentID != "pi" || got.ExecutionPolicy != agentpolicy.PolicyCooperativeTrustedV1 {
			t.Fatalf("cooperative authority changed: %+v %v", got, err)
		}
		base := filepath.Join(filepath.Dir(w.Members[0].CanonicalRoot), ".task-runtime", "run-"+runtimepermission.EnvironmentID(w.RunID))
		if _, err := os.Stat(base); !os.IsNotExist(err) {
			t.Fatalf("cooperative preparation created native environment: %v", err)
		}
	}
}

func TestCooperativePreparationRejectsNativeManifestWithoutReadingOrReplacingIt(t *testing.T) {
	w := cooperativeWorksetFixture(t)
	base := filepath.Join(filepath.Dir(w.Members[0].CanonicalRoot), ".task-runtime", "run-"+runtimepermission.EnvironmentID(w.RunID))
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(base, "permissions.json")
	original := []byte("native-or-incomplete-manifest")
	if err := os.WriteFile(manifest, original, 0600); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{Codex: DefaultCodexFactory{}}
	if _, err := c.prepareTaskPermissions(context.Background(), w, false); err == nil || !strings.Contains(err.Error(), "NATIVE_MANIFEST_CONFLICT") {
		t.Fatalf("native artifact accepted: %v", err)
	}
	if after, err := os.ReadFile(manifest); err != nil || string(after) != string(original) {
		t.Fatalf("existing artifact changed: %q %v", after, err)
	}
}

func TestCooperativePreparationReplaysOnlyExactPolicy(t *testing.T) {
	w := cooperativeWorksetFixture(t)
	c := &Coordinator{Runs: cooperativeReplayRuns{value: w}}
	if got, err := c.prepareTaskPermissions(context.Background(), w, false); err != nil || got.ContentHash != w.ContentHash {
		t.Fatalf("exact replay failed: %v", err)
	}
	legacy := w
	legacy.AgentID, legacy.ExecutionPolicy, legacy.AgentConfigFingerprint = "", "", ""
	legacy.RuntimeAdapterIdentity, legacy.RuntimeProtocolIdentity = "codex-app-server-v1", "codex-app-server-0.149.0"
	legacy, err := legacy.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	c.Runs = cooperativeReplayRuns{value: legacy}
	if _, err := c.prepareTaskPermissions(context.Background(), w, false); err == nil {
		t.Fatal("native historical workset silently adopted cooperative mode")
	}
	lookupErr := errors.New("authority read unavailable")
	c.Runs = cooperativeReplayRuns{err: lookupErr}
	if _, err := c.prepareTaskPermissions(context.Background(), w, false); !errors.Is(err, lookupErr) {
		t.Fatalf("authority read failure hidden: %v", err)
	}
	c.Runs = cooperativeReplayRuns{err: run.ErrNotFound}
	if _, err := c.prepareTaskPermissions(context.Background(), w, false); err != nil {
		t.Fatalf("new prospective run refused: %v", err)
	}
}
