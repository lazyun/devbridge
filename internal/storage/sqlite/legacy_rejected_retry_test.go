package sqlite

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/delivery"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRejectedRetryProjectsExactAuthority(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-scalar-retry-audit")
	execution = terminalExecutionWithReport(t, f, execution)
	_, source, _ := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	path := filepath.Join(source, "task.txt")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f.store.SetDeliveryFaultHookForTest(func(stage, repo string) error {
		if stage == "TASK_COMMIT_BEFORE_GIT" {
			return os.WriteFile(path, []byte("temporary interfering write\n"), 0600)
		}
		return nil
	})
	accepted, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "audit-accept"})
	if err != nil || accepted.CommitError == "" {
		t.Fatalf("fixture: %+v %v", accepted, err)
	}
	f.store.SetDeliveryFaultHookForTest(nil)
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	candidates, err := f.store.TaskDeliveryRetryCandidates(ctx, f.project, owner.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("no exact rejection: %+v %v", candidates, err)
	}
	cap, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("capability=%+v candidate=%+v", cap, candidates[0])
	if !cap.CanRetry || cap.RetryTaskID != owner.ID || cap.RetryOperation == nil || cap.RetryOperation.OperationID != candidates[0].OperationID || cap.RetryOperation.RepositoryID != candidates[0].RepositoryID {
		t.Error("scalar rejected retry omits successor authorization identity")
	}
	if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); !errors.Is(err, delivery.ErrRejected) {
		t.Fatalf("plain retry control: %v", err)
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	cold, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer cold.Close()
	f.db = cold
	f.store = NewWorkflowProgressionStore(cold)
	coldCap, err := f.store.CommitCapability(ctx, f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cold capability=%+v", coldCap)
	if coldCap.RetryOperation == nil {
		t.Error("cold scalar rejection still omits successor authority")
	}
	candidate := *coldCap.RetryOperation
	if _, err = f.store.ReprepareTaskCommit(ctx, f.project, delivery.RetryInput{TaskID: owner.ID, RepositoryID: candidate.RepositoryID, PredecessorOperationID: candidate.OperationID, RequestID: "explicit-retry"}); err != nil {
		t.Fatalf("explicit retry control: %v", err)
	}
	done, err := NewTaskStore(f.db).Get(ctx, owner.ID)
	if err != nil || string(done.Status) != "SUCCEEDED" {
		t.Fatalf("explicit retry did not succeed: %+v %v", done, err)
	}
	t.Log("explicit new-authority retry succeeds after restoring bytes and cold reopen")
}
