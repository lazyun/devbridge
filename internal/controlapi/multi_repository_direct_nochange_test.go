package controlapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
)

func TestMultiRepositoryDirectG5ReadOnlyAndNoChangeAreCompleteWithoutFakeCommits(t *testing.T) {
	f, current := approveDirectFixture(t, true, true)
	heads := map[string]string{}
	roots := map[string]string{}
	for _, target := range current.RepositoryTargets {
		var root string
		if err := f.plane.DB.SQL().QueryRow(`SELECT wt.path FROM workspace_members m JOIN worktrees wt ON wt.id=m.worktree_id WHERE m.id=?`, target.MemberID).Scan(&root); err != nil {
			t.Fatal(err)
		}
		roots[target.RepositoryID] = root
		heads[target.RepositoryID] = gitOutput(t, root, "rev-parse", "HEAD")
		if target.Write.Kind != "NONE" {
			if err := os.WriteFile(filepath.Join(root, "unrelated-dirty.txt"), []byte("preexisting bytes must stay\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start %v", err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range current.RepositoryTargets {
		if target.Write.Kind == "NONE" {
			continue
		}
		in := directInput(f, state, target)
		read, e := f.facade.FileRead(f.ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		in.RequestID = "no-op-replace"
		in.Content = read.Content
		in.ExpectedDigest = read.Digest
		if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "no-change-result", Summary: "No content change and read-only evidence", VerificationNotes: "explicit fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range result.Members {
		if len(m.ChangedPaths) != 0 {
			t.Fatalf("no-op invented changes %+v", m)
		}
	}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "accept-no-change", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept %+v %v", accepted, err)
	}
	var count int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_commit_members WHERE task_id=? AND state='NO_CHANGE' AND resulting_commit=''`, f.task).Scan(&count); err != nil || count != 2 {
		t.Fatalf("no-change receipts %d %v", count, err)
	}
	for repo, root := range roots {
		if got := gitOutput(t, root, "rev-parse", "HEAD"); got != heads[repo] {
			t.Fatal("no-change created fake commit")
		}
	}
}
