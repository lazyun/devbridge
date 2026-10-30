package execution

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/workset"
)

func multiRuntimeTestInput() MultiRuntimePrepareInput {
	commit := strings.Repeat("a", 40)
	return MultiRuntimePrepareInput{
		Owner:       MultiRuntimeOwner{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", RunID: "run"},
		WorkspaceID: "workspace", PrimaryMemberID: "member-app",
		Targets: []workset.Target{{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: commit, Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"internal"}}}},
		Runtime: MultiRuntimeIdentity{BackendConfigID: "config", BackendConfigFingerprint: "fingerprint", Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol", CreatedAt: time.Now()},
	}
}

func TestMultiRuntimeInputRequiresExclusiveCompleteOwner(t *testing.T) {
	in := multiRuntimeTestInput()
	if err := validateMultiRuntimeInput(in); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	in.Owner.ReviewID = "review"
	if err := validateMultiRuntimeInput(in); err == nil {
		t.Fatal("task/review dual owner accepted")
	}
	in = multiRuntimeTestInput()
	in.PrimaryMemberID = ""
	if err := validateMultiRuntimeInput(in); err == nil {
		t.Fatal("missing supplied primary accepted")
	}
}

func TestMultiRuntimePathsAllowedHonorsExactScope(t *testing.T) {
	scope := workset.WriteScope{Kind: "PATHS", Paths: []string{"internal", "cmd/tool"}}
	if !multiRuntimePathsAllowed(scope, []string{"internal/a.go", "cmd/tool/main.go"}) {
		t.Fatal("approved paths rejected")
	}
	for _, changed := range [][]string{{"other/a.go"}, {"../escape"}, {"/absolute"}} {
		if multiRuntimePathsAllowed(scope, changed) {
			t.Fatalf("out-of-scope paths accepted: %#v", changed)
		}
	}
	if multiRuntimePathsAllowed(workset.WriteScope{Kind: "NONE"}, []string{"internal/a.go"}) {
		t.Fatal("readonly member accepted changes")
	}
}

func TestMultiRuntimeRootIdentitySurvivesNormalFileEdit(t *testing.T) {
	root := t.TempDir()
	canonical, before, err := multiRuntimeRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "edited.txt"), []byte("ordinary edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, after, err := multiRuntimeRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != again || before != after {
		t.Fatalf("normal file edit changed root identity: %q/%q != %q/%q", canonical, before, again, after)
	}
}

func TestMultiRuntimeClientBindingRejectsUnboundPrimaryAndWrites(t *testing.T) {
	valid := codex.RuntimeBinding{RuntimeWorkspaceRoots: []string{"/managed/app", "/managed/sdk"}, WritableRoots: []string{"/managed/app"}}
	if err := validateMultiRuntimeClientBinding("/managed/app", valid); err != nil {
		t.Fatalf("valid binding: %v", err)
	}
	if err := validateMultiRuntimeClientBinding("/outside", valid); err == nil {
		t.Fatal("outside primary accepted")
	}
	valid.WritableRoots = []string{"/outside"}
	if err := validateMultiRuntimeClientBinding("/managed/app", valid); err == nil {
		t.Fatal("outside write root accepted")
	}
}

func TestMultiRuntimeMemberObservationClassifiesFrozenBoundary(t *testing.T) {
	root := t.TempDir()
	canonical, identity, err := multiRuntimeRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("a", 40)
	snapshot := strings.Repeat("1", 40)
	base := workset.RuntimeMemberBinding{RepositoryID: "repository", MemberID: "member", MemberVersion: 1, CanonicalRoot: canonical, RootFileIdentity: identity, RuntimeRootIdentity: identity, OriginCommit: commit, StartHEAD: commit, StartSnapshot: snapshot, Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"allowed"}}}
	for _, tc := range []struct {
		name     string
		member   workset.RuntimeMemberBinding
		readOnly bool
		evidence evidence.GitEvidence
		want     string
		wantErr  bool
	}{
		{name: "no-change", member: base, evidence: evidence.GitEvidence{HeadCommit: commit}, want: "NO_CHANGE"},
		{name: "allowed-change", member: base, evidence: evidence.GitEvidence{HeadCommit: commit, ChangedFiles: []string{"allowed/file.go"}, Diff: "allowed"}, want: "OBSERVED"},
		{name: "read-only-mutation", member: base, readOnly: true, evidence: evidence.GitEvidence{HeadCommit: commit, ChangedFiles: []string{"allowed/file.go"}, Diff: "mutation"}, want: "SCOPE_CONFLICT", wantErr: true},
		{name: "outside-path", member: base, evidence: evidence.GitEvidence{HeadCommit: commit, ChangedFiles: []string{"outside/file.go"}, Diff: "outside"}, want: "SCOPE_CONFLICT", wantErr: true},
		{name: "head-change", member: base, evidence: evidence.GitEvidence{HeadCommit: strings.Repeat("b", 40)}, want: "SCOPE_CONFLICT", wantErr: true},
		{name: "root-substitution", member: func() workset.RuntimeMemberBinding {
			value := base
			value.RootFileIdentity = strings.Repeat("f", 64)
			return value
		}(), evidence: evidence.GitEvidence{HeadCommit: commit}, want: "SCOPE_CONFLICT", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := &fakeCollector{value: tc.evidence, stable: true}
			coordinator := &Coordinator{Evidence: collector}
			observed, state, observeErr := coordinator.observeMultiRuntimeMember(t.Context(), workset.RuntimeWorkset{RunID: "run"}, tc.member, tc.readOnly)
			if state != tc.want || (observeErr != nil) != tc.wantErr || observed.ObservationState != tc.want || observed.EndSnapshot != snapshot {
				t.Fatalf("observation=%+v state=%s err=%v", observed, state, observeErr)
			}
		})
	}
}
