package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func taskPermissionFixture(t *testing.T) (*Coordinator, workset.RuntimeWorkset) {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "source")
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%v: %v %s", args, e, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "user.name", "Fixture")
	if err = os.WriteFile(filepath.Join(root, "source.txt"), []byte("committed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("dist/\n.cache/\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "baseline")
	head := git("rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(root, "source.txt"), []byte("actual uncommitted implementation\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "new-test.txt"), []byte("new untracked test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	client := bridgegit.NewClient(bridgegit.Config{})
	c := &Coordinator{Codex: DefaultCodexFactory{}, Evidence: evidence.NewCollector(client)}
	snapshot, err := c.Evidence.CaptureSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: 1, ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "execution", RunID: "verification-run", WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: "repository", RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "tree", CanonicalRoot: root, RootFileIdentity: "source-id", OriginCommit: head, StartHEAD: head, StartSnapshot: snapshot, Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}, RuntimeRootIdentity: "source-id"}}, BackendConfigID: "config", BackendConfigFingerprint: "fingerprint", Model: "model", ReasoningEffort: "low", RuntimeAdapterIdentity: "codex-app-server-v1", RuntimeProtocolIdentity: "codex-app-server-0.149.0", CreatedAt: time.Now().UTC()}
	_, rootID, rootErr := multiRuntimeRootIdentity(root)
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	w.Members[0].RootFileIdentity = rootID
	w.Members[0].RuntimeRootIdentity = rootID
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	return c, w
}

func TestTaskVerificationCopiesUncommittedSourcesAndPreservesAuthority(t *testing.T) {
	c, w := taskPermissionFixture(t)
	ctx := context.Background()
	originalHash := w.ContentHash
	w, err := c.prepareTaskPermissions(ctx, w, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.ContentHash != originalHash || w.TaskPermissions == nil || w.Validate() != nil {
		t.Fatal("engineering authority changed")
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil {
		t.Fatal(err)
	}
	if cwd == w.Members[0].CanonicalRoot || len(binding.WritableRoots) != 2 || binding.TaskPermissions == nil {
		t.Fatal("verification runs in source")
	}
	for _, root := range binding.WritableRoots {
		if root == w.Members[0].CanonicalRoot {
			t.Fatal("source writable")
		}
	}
	b, err := os.ReadFile(filepath.Join(cwd, "source.txt"))
	if err != nil || string(b) != "actual uncommitted implementation\n" {
		t.Fatal("lost uncommitted implementation")
	}
	if _, err = os.Stat(filepath.Join(cwd, "new-test.txt")); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(cwd, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cwd, "dist", "bundle.js"), []byte("build output"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.verifyTaskCopies(ctx, w); err != nil {
		t.Fatalf("ignored build output rejected: %v", err)
	}
	if _, err = os.Stat(filepath.Join(w.Members[0].CanonicalRoot, "dist")); !os.IsNotExist(err) {
		t.Fatal("build affected source")
	}
	// A remount/restart uses the same manifest and roots, never a fresh clone.
	again, err := c.prepareTaskPermissions(ctx, w, true)
	if err != nil || again.TaskPermissions.ContentHash != w.TaskPermissions.ContentHash {
		t.Fatalf("replay: %v", err)
	}
	if err = os.WriteFile(filepath.Join(cwd, "source.txt"), []byte("verification secretly fixed implementation"), 0644); err != nil {
		t.Fatal(err)
	}
	if c.verifyTaskCopies(ctx, w) == nil {
		t.Fatal("verification source mutation accepted")
	}
	source, _ := os.ReadFile(filepath.Join(w.Members[0].CanonicalRoot, "source.txt"))
	if string(source) != "actual uncommitted implementation\n" {
		t.Fatal("source changed")
	}
}

func TestTaskPermissionCopyProvenanceIsNotAnArbitraryCWD(t *testing.T) {
	c, w := taskPermissionFixture(t)
	w, err := c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	copy := w.TaskPermissions.Copies[0]
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app", CodexInstruction: "verify", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true, RepositoryID: "repository", RepositoryIDs: []string{"repository"}, WorkingDirectory: "."}}, AcceptanceCriteria: []string{"passes"}}
	message, _ := json.Marshal(reportedVerificationV2{Version: 2, Summary: "verified", Checks: []reportedVerificationItem{{Index: 0, Status: run.VerificationCheckPassed, Summary: "passed"}}, AcceptanceCriteria: []reportedVerificationItem{{Index: 0, Status: run.VerificationCheckPassed, Summary: "passed"}}})
	commands := []ObservedCommand{{ItemID: "item", Command: "go test ./...", CWD: copy.Root, Status: "completed", ExitCode: exitCode(0)}}
	scope := &verificationScope{workset: w}
	report, err := buildVerificationReportWithScope(spec, string(message), commands, evidence.GitEvidence{}, scope)
	if err != nil || !run.VerificationEvidenceComplete(report, spec, &w) {
		t.Fatalf("copy evidence refused: %v %+v", err, report)
	}
	if report.Checks[0].Observed.Source.CWD != copy.Root || report.Checks[0].Observed.Source.TaskPermissionHash != w.TaskPermissions.ContentHash {
		t.Fatal("actual provenance lost")
	}
	report.Checks[0].Observed.Source.TaskPermissionHash = strings.Repeat("f", 64)
	if run.VerificationEvidenceComplete(report, spec, &w) {
		t.Fatal("forged copy policy accepted")
	}
	commands[0].CWD = filepath.Dir(copy.Root)
	report, err = buildVerificationReportWithScope(spec, string(message), commands, evidence.GitEvidence{}, scope)
	if err != nil || run.VerificationEvidenceComplete(report, spec, &w) {
		t.Fatal("unbound directory accepted")
	}
}

func TestTaskPermissionRejectsSourceDriftAndChangedRoot(t *testing.T) {
	t.Run("source drift", func(t *testing.T) {
		c, w := taskPermissionFixture(t)
		os.WriteFile(filepath.Join(w.Members[0].CanonicalRoot, "source.txt"), []byte("late change"), 0644)
		if _, err := c.prepareTaskPermissions(context.Background(), w, true); err == nil {
			t.Fatal("source drift copied")
		}
	})
	t.Run("external symlink", func(t *testing.T) {
		c, w := taskPermissionFixture(t)
		os.Symlink(t.TempDir(), filepath.Join(w.Members[0].CanonicalRoot, "escape"))
		snapshot, err := c.Evidence.CaptureSnapshot(context.Background(), w.Members[0].CanonicalRoot)
		if err != nil {
			t.Fatal(err)
		}
		w.Members[0].StartSnapshot = snapshot
		if _, err = c.prepareTaskPermissions(context.Background(), w, true); err == nil {
			t.Fatal("external symlink copied")
		}
	})
	t.Run("replaced copy", func(t *testing.T) {
		c, w := taskPermissionFixture(t)
		w, err := c.prepareTaskPermissions(context.Background(), w, true)
		if err != nil {
			t.Fatal(err)
		}
		root := w.TaskPermissions.Copies[0].Root
		if err = os.Rename(root, root+"-old"); err != nil {
			t.Fatal(err)
		}
		os.Mkdir(root, 0700)
		if validateTaskEnvironment(w) == nil {
			t.Fatal("replaced root accepted")
		}
	})
}

func TestTaskImplementationUsesOnlyApprovedRootsAndScratch(t *testing.T) {
	c, w := taskPermissionFixture(t)
	w.RunID = "implementation-run"
	w, err := c.prepareTaskPermissions(context.Background(), w, false)
	if err != nil {
		t.Fatal(err)
	}
	cwd, binding, err := multiRuntimeClientBinding(w)
	if err != nil || cwd != w.Members[0].CanonicalRoot || len(binding.WritableRoots) != 2 || binding.TaskPermissions.Purpose != "IMPLEMENTATION" {
		t.Fatalf("implementation policy: %v %+v", err, binding)
	}
	forged := *w.TaskPermissions
	forged.RunID = "other"
	forged.ContentHash = forged.Hash()
	w.TaskPermissions = &forged
	if w.Validate() == nil {
		t.Fatal("policy borrowed by another Run")
	}
}

func TestTaskScopeReviewHasIndependentCopyAndNoNetwork(t *testing.T) {
	c, w := taskPermissionFixture(t)
	w.TaskID = ""
	w.TaskDefinitionRevisionID = ""
	w.ExecutionID = ""
	w.ReviewID = "review"
	w.RunID = "review-run"
	w.Members[0].Write = workset.WriteScope{Kind: "NONE"}
	var err error
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	w, err = c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	if w.TaskPermissions.Purpose != "SCOPE_REVIEW" || w.TaskPermissions.NetworkAccess || len(w.TaskPermissions.Copies) != 1 {
		t.Fatal("review policy incomplete")
	}
}

func TestTaskPermissionCopiesTrackedDeletions(t *testing.T) {
	c, w := taskPermissionFixture(t)
	root := w.Members[0].CanonicalRoot
	if err := os.Remove(filepath.Join(root, "source.txt")); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.Evidence.CaptureSnapshot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	w.Members[0].StartSnapshot = snapshot
	w, err = c.prepareTaskPermissions(context.Background(), w, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.verifyTaskCopies(context.Background(), w); err != nil {
		t.Fatal(err)
	}
}

func TestTaskPermissionReadsNetworkFromFrozenDefinition(t *testing.T) {
	for _, grant := range []bool{false, true} {
		t.Run(fmt.Sprint(grant), func(t *testing.T) {
			c, w := taskPermissionFixture(t)
			c.Tasks = &fakeTasks{value: task.Task{ID: w.TaskID, WorkSpec: task.WorkSpec{CommandPermissions: &task.CommandPermissions{NetworkAccess: !grant}}}, revision: &task.DefinitionRevision{ID: w.TaskDefinitionRevisionID, TaskID: w.TaskID, Definition: task.Definition{WorkSpec: task.WorkSpec{CommandPermissions: &task.CommandPermissions{NetworkAccess: grant}}}}}
			w, err := c.prepareTaskPermissions(context.Background(), w, true)
			if err != nil || w.TaskPermissions.NetworkAccess != grant {
				t.Fatalf("live task overrode frozen grant: %v", err)
			}
		})
	}
}

func TestTaskPermissionRejectsScopedWriteSymlink(t *testing.T) {
	c, w := taskPermissionFixture(t)
	root := w.Members[0].CanonicalRoot
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	w.Members[0].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"linked/file.txt"}}
	var err error
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.prepareTaskPermissions(context.Background(), w, false); err == nil {
		t.Fatal("symlink expanded source grant")
	}
}
