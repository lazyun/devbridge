package controlapi_test

import (
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func requireUnlinkBlocker(t *testing.T, f *upperLifecycleFixture, repositoryID, entityType string) {
	t.Helper()
	eligibility, err := sqlite.NewRepositoryUnlinkStore(f.plane.DB).Eligibility(f.ctx, f.project, repositoryID)
	if err != nil {
		t.Fatal(err)
	}
	for _, blocker := range eligibility.Blockers {
		if blocker.EntityType == entityType {
			if eligibility.CanUnlink {
				t.Fatalf("%s blocker reported with canUnlink=true: %+v", entityType, eligibility)
			}
			prepared := eligibility
			_, commitErr := sqlite.NewRepositoryUnlinkStore(f.plane.DB).Commit(f.ctx, repositoryunlink.CommitInput{RequestID: "blocked-" + entityType, ProjectID: f.project, RepositoryID: repositoryID, ExpectedGeneration: prepared.RegistrationGeneration, ExpectedTopologyRevision: prepared.ProjectTopologyRevision, Reason: "must remain blocked"})
			if !errors.Is(commitErr, repositoryunlink.ErrBlocked) {
				t.Fatalf("%s commit err=%v, want blocked", entityType, commitErr)
			}
			return
		}
	}
	t.Fatalf("missing %s blocker: %+v", entityType, eligibility.Blockers)
}

func startAndSubmitDirectResult(t *testing.T, f *upperLifecycleFixture, current task.Task) (directfile.State, directfile.ImplementationResult) {
	t.Helper()
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state: %+v %v", state, err)
	}
	for _, target := range current.RepositoryTargets {
		in := directInput(f, state, target)
		read, readErr := f.facade.FileRead(f.ctx, in)
		if readErr != nil {
			t.Fatal(readErr)
		}
		in.RequestID = "unlink-replace-" + target.RepositoryID
		in.ExpectedDigest = read.Digest
		in.Content = "unlink authority evidence\n"
		if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "unlink-submit-files", Summary: "Exact members updated", VerificationNotes: "Fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	return state, result
}

func TestRepositoryUnlinkBlocksDirectFileEffectBeforeReceiptAfterGrantClosed(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state: %+v %v", state, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER unlink_direct_receipt_fault BEFORE UPDATE OF state ON direct_file_operation_receipts WHEN NEW.state='COMPLETED' BEGIN SELECT RAISE(ABORT,'unlink direct receipt fault'); END`); err != nil {
		t.Fatal(err)
	}
	target := current.RepositoryTargets[0]
	in := directInput(f, state, target)
	read, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "unlink-effect-before-receipt"
	in.ExpectedDigest = read.Digest
	in.Content = "effect persisted before receipt\n"
	if _, err = f.facade.FileReplace(f.ctx, in); err == nil {
		t.Fatal("receipt fault did not interrupt finalization")
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER unlink_direct_receipt_fault`); err != nil {
		t.Fatal(err)
	}
	var receiptState string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&receiptState); err != nil || receiptState != "PENDING" {
		t.Fatalf("receipt state=%q err=%v", receiptState, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE direct_file_task_authorizations SET state='CLOSED' WHERE workflow_id=?`, state.WorkflowID); err != nil {
		t.Fatal(err)
	}
	requireUnlinkBlocker(t, f, target.RepositoryID, "direct_file_operation_receipt")
}

func TestRepositoryUnlinkBlocksV2TaskAndFeatureAreaDeliveryMembers(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := startAndSubmitDirectResult(t, f, current)
	progression := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	progression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "TASK_COMMIT_AFTER_GIT" {
			return errors.New("task delivery crash")
		}
		return nil
	})
	accepted, err := progression.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "unlink-accept-files", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted || accepted.CommitError == "" {
		t.Fatalf("accept=%+v err=%v", accepted, err)
	}
	requireUnlinkBlocker(t, f, current.RepositoryTargets[0].RepositoryID, "task_commit_member")

	progression.SetDeliveryFaultHookForTest(nil)
	if _, err = progression.RetryTaskCommit(f.ctx, f.project, f.task); err != nil {
		t.Fatal(err)
	}
	plan, err := progression.PrepareFeatureAreaDeliveryPlan(f.ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	progression.SetDeliveryFaultHookForTest(func(stage, _ string) error {
		if stage == "AREA_MERGE_AFTER_GIT" {
			return errors.New("area delivery crash")
		}
		return nil
	})
	if _, err = progression.CommitFeatureAreaPlan(f.ctx, f.project, f.area, "unlink-area-delivery", plan); err == nil {
		t.Fatal("area delivery fault did not interrupt finalization")
	}
	requireUnlinkBlocker(t, f, current.RepositoryTargets[0].RepositoryID, "feature_area_commit_member")
}
