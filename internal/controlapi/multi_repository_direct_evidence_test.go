package controlapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
)

func TestMultiRepositoryDirectG4UncontrolledBytesAndHeadCannotBecomeResult(t *testing.T) {
	for _, kind := range []string{"unreceipted", "after-replace", "head-drift"} {
		t.Run(kind, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
				t.Fatal(err)
			}
			state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			target := current.RepositoryTargets[0]
			in := directInput(f, state, target)
			var root string
			if err = f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND repository_id=?`, state.WorkflowID, in.RepositoryID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			if kind != "unreceipted" {
				r, e := f.facade.FileRead(f.ctx, in)
				if e != nil {
					t.Fatal(e)
				}
				in.RequestID = "controlled-replace"
				in.ExpectedDigest = r.Digest
				in.Content = "controlled bytes\n"
				if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "head-drift" {
				gitOutput(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "external head drift")
			} else {
				if err = os.WriteFile(filepath.Join(root, "same.txt"), []byte("uncontrolled external bytes\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, err = f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "must-reject-result", Summary: "not controlled evidence", VerificationNotes: "waived", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
			if err == nil {
				t.Fatalf("%s became formal result", kind)
			}
			var n int
			if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_implementation_results WHERE task_id=?`, f.task).Scan(&n); e != nil || n != 0 {
				t.Fatalf("bad result persisted %d %v", n, e)
			}
		})
	}
}
func TestMultiRepositoryDirectG3FailedPreconditionHasNoUnresolvedEffect(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	in := directInput(f, state, current.RepositoryTargets[0])
	in.RequestID = "stale-cas"
	in.ExpectedDigest = "not-current"
	in.Content = "must not happen"
	if _, err = f.facade.FileReplace(f.ctx, in); err == nil {
		t.Fatal("stale CAS accepted")
	}
	var pending int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=? AND state IN ('PENDING','UNKNOWN')`, state.WorkflowID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("precondition failure invented unresolved effect %d %v", pending, err)
	}
	in = directInput(f, state, current.RepositoryTargets[0])
	r, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "valid-after-cas"
	in.ExpectedDigest = r.Digest
	in.Content = "valid bytes"
	if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state)
	if err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("safe failure poisoned cancel %+v %v", canceled.Steps, err)
	}
}
func TestMultiRepositoryDirectG7IncompletePreparationRequiresExplicitRecovery(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	target := current.RepositoryTargets[1]
	var root string
	if err := f.plane.DB.SQL().QueryRow(`SELECT wt.path FROM workspace_members m JOIN worktrees wt ON wt.id=m.worktree_id WHERE m.id=?`, target.MemberID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-unavailable"); err != nil {
		t.Fatal(err)
	}
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err == nil && started.State == "COMPLETED" {
		t.Fatal("incomplete preparation started")
	}
	if err = os.Rename(root+"-unavailable", root); err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State == "ACTIVE" || state.WorkflowID == "" {
		t.Fatalf("missing reserved preparation %+v %v", state, err)
	}
	if _, err = f.facade.FileRead(f.ctx, directInput(f, state, current.RepositoryTargets[0])); err == nil {
		t.Fatal("partial preparation granted file access")
	}
	recovered, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state)
	if err != nil || recovered.State != "COMPLETED" {
		t.Fatalf("explicit preparation recovery %+v %v", recovered.Steps, err)
	}
	var release string
	if e := f.plane.DB.SQL().QueryRow(`SELECT reason FROM task_action_releases WHERE intent_id=?`, started.IntentID).Scan(&release); e != nil || release != "FILE_PREPARATION_REQUIRES_RECOVERY" {
		t.Fatalf("missing exact release: %s %v", release, e)
	}
	original, e := f.facade.ReadConfirmation(f.ctx, nodeRead(started))
	if e != nil || original.Commit == nil || original.State == "COMPLETED" {
		t.Fatalf("original partial result overwritten: %s %v", original.State, e)
	}
	next, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || next.State != "ACTIVE" || next.WorkspaceID != state.WorkspaceID || next.WorkflowID != state.WorkflowID {
		t.Fatalf("preparation substituted owner %+v %v", next, err)
	}
}

func TestMultiRepositoryDirectG4EachMutationHasFormalFinalEvidence(t *testing.T) {
	for _, kind := range []string{"create", "replace", "delete", "move"} {
		t.Run(kind, func(t *testing.T) {
			f, current := approveDirectFixture(t, true, true)
			if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
				t.Fatal(err)
			}
			state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			target := current.RepositoryTargets[0]
			for _, candidate := range current.RepositoryTargets {
				if candidate.Write.Kind != "NONE" {
					target = candidate
				}
			}
			in := directInput(f, state, target)
			read, err := f.facade.FileRead(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			in.RequestID = "mutation-" + kind
			in.ExpectedDigest = read.Digest
			in.Content = "final implementation bytes\n"
			in.Destination = "new.txt"
			switch kind {
			case "create":
				in.Path = "new.txt"
				in.ExpectedDigest = ""
				_, err = f.facade.FileCreate(f.ctx, in)
			case "replace":
				_, err = f.facade.FileReplace(f.ctx, in)
			case "delete":
				_, err = f.facade.FileDelete(f.ctx, in)
			case "move":
				_, err = f.facade.FileMove(f.ctx, in)
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "result-" + kind, Summary: "Exact mutation evidence", VerificationNotes: "explicit fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "accept-" + kind, ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
			if err != nil || !accepted.Accepted || accepted.CommitError != "" {
				t.Fatalf("mutation delivery %+v %v", accepted, err)
			}
			var committed, noChange int
			if err = f.plane.DB.SQL().QueryRow(`SELECT sum(state='COMPLETED'),sum(state='NO_CHANGE') FROM task_commit_members WHERE task_id=?`, f.task).Scan(&committed, &noChange); err != nil || committed != 1 || noChange != 1 {
				t.Fatalf("exact member outcomes %d %d %v", committed, noChange, err)
			}
		})
	}
}

func TestMultiRepositoryDirectG7FailedMemberPreparationCanRecover(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	target := current.RepositoryTargets[1]
	// Inject the durable failed-preparation boundary in this test-owned DB;
	// Task/revision/approval and worktree identities remain production-created.
	if _, err := f.plane.DB.SQL().Exec(`UPDATE workspace_members SET preparation='FAILED',failure='injected member unavailable' WHERE id=?`, target.MemberID); err != nil {
		t.Fatal(err)
	}
	if started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err == nil && started.State == "COMPLETED" {
		t.Fatal("failed member started grant")
	}
	var prep string
	if err := f.plane.DB.SQL().QueryRow(`SELECT preparation FROM direct_file_task_members WHERE member_id=?`, target.MemberID).Scan(&prep); err != nil || prep != "FAILED" {
		t.Fatalf("failed member receipt %s %v", prep, err)
	}
	if _, err := f.plane.DB.SQL().Exec(`UPDATE workspace_members SET preparation='READY',failure='' WHERE id=?`, target.MemberID); err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state)
	if err != nil || recovered.State != "COMPLETED" {
		t.Fatalf("failed-member recovery %+v %v", recovered.Steps, err)
	}
}
