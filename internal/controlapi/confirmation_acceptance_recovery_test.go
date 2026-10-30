package controlapi_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestAcceptanceConfirmationRecoversCommittedDecision(t *testing.T) {
	for _, action := range []string{"REQUEST_REVERIFICATION", "ACCEPT", "REJECT"} {
		t.Run(action, func(t *testing.T) {
			runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
				current := awaitAcceptanceRecoveryFixture(t, f, executionID)
				prepared, input := prepareAcceptanceRecoveryFixture(t, f, current, executionID, action)
				// A different prepare request cannot open a second Task chain.
				otherContent := prepared.Content
				otherContent.RequestID = "other-acceptance-prepare"
				otherContent.SubmittedHash = ""
				_, err := f.facade.PrepareConfirmation(f.ctx, otherContent)
				requireNodeFailure(t, err, "TASK_ACTION_PENDING")
				if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER acceptance_receipt_loss BEFORE UPDATE OF intent_json ON confirmation_intents
 WHEN NEW.intent_id='` + prepared.IntentID + `' AND json_extract(NEW.intent_json,'$.state')='COMPLETED'
 BEGIN SELECT RAISE(ABORT,'injected acceptance receipt loss'); END`); err != nil {
					t.Fatal(err)
				}
				out, err := f.facade.CommitConfirmation(f.ctx, input)
				if err == nil || !acceptanceRecoveryFailureContains(err, "acceptance receipt loss") || out.State == "COMPLETED" {
					t.Fatalf("expected loss after committed decision: state=%s err=%v", out.State, err)
				}
				var resultID, option, actor, request string
				if err = f.plane.DB.SQL().QueryRow(`SELECT result_id,option,actor_id,request_id FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(executionID)).Scan(&resultID, &option, &actor, &request); err != nil || option != action || actor != f.session || request != out.Steps[0].RequestID {
					t.Fatalf("exact business receipt missing: result=%q option=%q actor=%q request=%q err=%v", resultID, option, actor, request, err)
				}
				if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER acceptance_receipt_loss`); err != nil {
					t.Fatal(err)
				}
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				tables := definitionFormalEveryTable(t, f)
				before := definitionFormalSnapshot(t, f, tables)
				read := c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: input.RequestID}
				for range 2 {
					recovered, readErr := f.facade.ReadConfirmation(f.ctx, read)
					assertAcceptanceRecoveryResult(t, recovered, readErr, current.CurrentDefinitionRevisionID, action, resultID)
				}
				replayed, err := f.facade.CommitConfirmation(f.ctx, input)
				assertAcceptanceRecoveryResult(t, replayed, err, current.CurrentDefinitionRevisionID, action, resultID)
				assertDefinitionFormalSnapshot(t, f, tables, before, "acceptance response loss reads and original replay")
				return false
			})
		})
	}
}

func TestAcceptanceConfirmationDoesNotInferMissingReceipt(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		current := awaitAcceptanceRecoveryFixture(t, f, executionID)
		prepared, input := prepareAcceptanceRecoveryFixture(t, f, current, executionID, "REQUEST_REVERIFICATION")
		if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER acceptance_no_receipt BEFORE INSERT ON decision_resolution_receipts
 WHEN NEW.decision_kind='TASK_ACCEPTANCE' BEGIN SELECT RAISE(ABORT,'injected acceptance transaction rollback'); END`); err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CommitConfirmation(f.ctx, input)
		if err == nil || out.State != "UNKNOWN" {
			t.Fatalf("failed business command: %+v %v", out, err)
		}
		var receipts int
		if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.TaskAcceptanceDecisionID(executionID)).Scan(&receipts); err != nil || receipts != 0 {
			t.Fatalf("rollback created decision receipt: %d %v", receipts, err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER acceptance_no_receipt`); err != nil {
			t.Fatal(err)
		}
		reopenDirectDeliveryFixture(t, f)
		f.plane.Retirement.Close()
		tables := definitionFormalEveryTable(t, f)
		before := definitionFormalSnapshot(t, f, tables)
		read := c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: input.RequestID}
		observed, err := f.facade.ReadConfirmation(f.ctx, read)
		if err != nil || observed.State != "UNKNOWN" || observed.CanResume || observed.Steps[0].ExecutionID != "" {
			t.Fatalf("missing receipt became success: %+v %v", observed, err)
		}
		if replay, err := f.facade.CommitConfirmation(f.ctx, input); err != nil || replay.State != "UNKNOWN" || replay.CanResume {
			t.Fatalf("missing receipt allowed redispatch: %+v %v", replay, err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "missing receipt stays unknown without dispatch")
		return false
	})
}

func awaitAcceptanceRecoveryFixture(t *testing.T, f *upperLifecycleFixture, executionID string) task.Task {
	t.Helper()
	f.plane.Retirement.Close()
	deadline := time.Now().Add(15 * time.Second)
	var current task.Task
	for {
		submitCompletedFixtureVerificationAnalysis(t, f, executionID)
		var err error
		current, err = f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == task.PhaseAcceptancePending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real implementation and verification did not finish: %+v", current)
		}
		time.Sleep(20 * time.Millisecond)
	}
	execution, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{execution.RunID, execution.VerificationRunID} {
		if session, ok := f.plane.Supervisor.GetSession(runID); ok {
			select {
			case <-session.Done():
			case <-time.After(8 * time.Second):
				t.Fatal("real runtime pipeline did not finish")
			}
		}
	}
	return current
}

func prepareAcceptanceRecoveryFixture(t *testing.T, f *upperLifecycleFixture, current task.Task, executionID, action string) (c.Intent, c.CommitInput) {
	t.Helper()
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-acceptance-recovery", Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "resolve", Summary: "Resolve the exact verified execution"}}})
	if err != nil {
		t.Fatal(err)
	}
	return prepared, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-acceptance-recovery", ContentHash: prepared.ContentHash, VariantID: "resolve"}
}

func assertAcceptanceRecoveryResult(t *testing.T, got c.Intent, err error, revision, action, resultID string) {
	t.Helper()
	if err != nil || got.State != "COMPLETED" || got.CanResume || len(got.Steps) != 1 || got.Steps[0].State != "COMPLETED" || got.Steps[0].RevisionID != revision || got.Steps[0].Effect != "COMMAND_COMPLETED" || got.Steps[0].Failure != nil {
		t.Fatalf("committed %s receipt not recovered: %+v %v", action, got, err)
	}
	executionID := ""
	if action == "REQUEST_REVERIFICATION" {
		executionID = resultID
	}
	if got.Steps[0].ExecutionID != executionID || got.Steps[0].RunID != "" || got.Submission == nil || got.Submission.Outcome != c.Consumed {
		t.Fatalf("recovery substituted result or invented dispatch: %+v", got)
	}
}

func acceptanceRecoveryFailureContains(err error, message string) bool {
	for err != nil {
		if strings.Contains(err.Error(), message) {
			return true
		}
		err = errors.Unwrap(err)
	}
	return false
}
