package sqlite

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
	"time"
)

func readyExecutionTask(t *testing.T, db *DB, suffix string) (task.Task, aisession.Session) {
	t.Helper()
	current, session := revisionTaskFixture(t, db, suffix)
	current.WorkSpec = task.WorkSpec{Goal: current.Goal, Constraints: current.Constraints, CodexInstruction: "Implement the confirmed continuation verbatim.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	current.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Go packages", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"all tests pass"}, CodexInstruction: "Run the confirmed Go tests without modifying files."}
	return approveFixtureTask(t, db, current, session), session
}
func reserveTaskExecutionFull(t *testing.T, db *DB, sessionID, taskID string) (executionproposal.Proposal, run.Run) {
	t.Helper()
	ctx := context.Background()
	value, reserved, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, sessionID, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status == executionproposal.StatusAdmitted && reserved.ID == "" {
		value, reserved, err = continueAdmittedExecution(ctx, db, value.ID, testNow)
		if err != nil {
			t.Fatal(err)
		}
	}
	return value, reserved
}

func TestExecutionPlanAndVerificationReportSurviveReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	current, session := readyExecutionTask(t, db, "plan-reopen")
	proposals := NewExecutionProposalStore(db)
	value, implementation, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status == executionproposal.StatusAdmitted && implementation.ID == "" {
		value, implementation, err = continueAdmittedExecution(ctx, db, value.ID, testNow.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	source := value
	now := testNow.Add(3 * time.Second)
	runs := run.NewService(NewRunStore(db), nil, func() time.Time {
		now = now.Add(time.Second)
		return now
	})
	if _, err := runs.Transition(ctx, implementation.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentConversationStore(db).BeginRun(ctx, implementation.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusRunning, run.StatusCollecting} {
		if implementation, err = runs.Transition(ctx, implementation.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewAgentConversationStore(db).FinishRun(ctx, implementation.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if implementation, err = runs.Succeed(ctx, implementation.ID, run.Result{AgentClaim: evidence.AgentClaim{Message: "durable run result"}}); err != nil {
		t.Fatal(err)
	}
	_, verification, err := proposals.ReserveVerificationRun(ctx, value.ID, "verification-plan-reopen", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if implementation.AgentExecutionID != value.ID || verification.AgentExecutionID != value.ID {
		t.Fatalf("implementation/verification execution IDs: %q / %q", implementation.AgentExecutionID, verification.AgentExecutionID)
	}

	if _, err := runs.Transition(ctx, verification.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentConversationStore(db).BeginRun(ctx, verification.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusRunning} {
		if verification, err = runs.Transition(ctx, verification.ID, status); err != nil {
			t.Fatal(err)
		}
	}
	result := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "facts recorded", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(task.VerificationCheckTestCommand), Required: true, Status: run.VerificationCheckPassed, Summary: "go test ./... passed", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "reported pass"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: "all tests pass", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "reported pass"}}}}
	if verification, err = runs.AttachVerificationReport(ctx, verification.ID, result); err != nil {
		t.Fatal(err)
	}
	if verification, err = runs.Transition(ctx, verification.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if err := NewAgentConversationStore(db).FinishRun(ctx, verification.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if verification, err = runs.Succeed(ctx, verification.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}

	if _, err := proposals.Complete(ctx, value.ID, executionproposal.OutcomeSucceeded, "", "", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	persisted, err := NewExecutionProposalStore(db).Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Outcome != executionproposal.OutcomeSucceeded || persisted.ImplementationRunID != implementation.ID || persisted.VerificationRunID != verification.ID || persisted.WorkSpec.CodexInstruction != source.WorkSpec.CodexInstruction || persisted.VerificationSpec.CodexInstruction != source.VerificationSpec.CodexInstruction {
		t.Fatalf("persisted execution plan=%+v", persisted)
	}
	persistedVerification, err := NewRunStore(db).Get(ctx, verification.ID)
	if err != nil || persistedVerification.Purpose != run.PurposeVerification || persistedVerification.ExecutionProposalID != value.ID || persistedVerification.VerificationReport == nil || persistedVerification.VerificationReport.Availability != run.VerificationReportAvailable || len(persistedVerification.VerificationReport.Checks) != 1 {
		t.Fatalf("persisted verification=%+v err=%v", persistedVerification, err)
	}
	if _, err = db.sql.ExecContext(ctx, `UPDATE runs SET verification_result_json=NULL WHERE id=?`, verification.ID); err == nil {
		t.Fatal("durable Verification Report remained mutable")
	}
	persistedImplementation, err := NewRunStore(db).Get(ctx, implementation.ID)
	if err != nil || persistedImplementation.Result == nil || persistedImplementation.Result.AgentClaim.Message != "durable run result" {
		t.Fatalf("persisted run result=%+v err=%v", persistedImplementation.Result, err)
	}
	t.Run("immutable history after revision", func(t *testing.T) {
		assertExecutionHistoryAfterRevision(t, db, session, persisted, persistedImplementation, persistedVerification)
	})
	legacy, err := decodeVerificationReportJSON(`{"version":1,"status":"FAILED","checks":[{"index":0,"kind":"CUSTOM","required":true,"status":"SEMANTIC_RESULT_AVAILABLE","evidenceTruncated":false}]}`)
	if err != nil || legacy.Version != 1 || legacy.LegacyResultStatus != run.LegacyVerificationFailed || legacy.Checks[0].Status != run.VerificationCheckUnknown || legacy.Checks[0].Reported.Status != run.VerificationCheckUnknown {
		t.Fatalf("legacy codec=%+v err=%v", legacy, err)
	}
}

func TestRecoveryFailsExecutionPlanOrphanedAtSerialBoundary(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db := openTestDB(t, root)
	defer db.Close()
	current, session := readyExecutionTask(t, db, "orphaned-plan")
	proposals := NewExecutionProposalStore(db)
	value, implementation, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status == executionproposal.StatusAdmitted && implementation.ID == "" {
		value, implementation, err = continueAdmittedExecution(ctx, db, value.ID, testNow.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	proposals = NewExecutionProposalStore(db)
	service := recovery.NewService(NewRecoveryStore(db), func() time.Time { return testNow.Add(3 * time.Second) })
	if err := service.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	persisted, err := proposals.Get(ctx, value.ID)
	if err != nil || persisted.Outcome != executionproposal.OutcomeFailed || persisted.FailureCode != recovery.InterruptedCode {
		t.Fatalf("proposal=%+v err=%v", persisted, err)
	}
	// The orphaned in-flight implementation is at-rest start uncertainty: the
	// boot reconciler moves the Task to RECOVERY_REQUIRED exactly once.
	taskValue, err := NewTaskStore(db).Get(ctx, value.TaskID)
	if err != nil || (taskValue.Status != task.StatusActive || taskValue.Phase != task.PhaseRecoveryRequired) {
		t.Fatalf("task=%+v err=%v", taskValue, err)
	}
	runValue, err := NewRunStore(db).Get(ctx, implementation.ID)
	if err != nil || runValue.Status != run.StatusInfrastructureFailed || runValue.Failure == nil {
		t.Fatalf("interrupted claimed Run should remain auditable: %+v err=%v", runValue, err)
	}
}

func TestTaskOwnedExecutionEventFaultsRollbackAndRetry(t *testing.T) {
	// Admission and Run creation are now separate transactions.  A fault
	// during Run creation (transaction B) must not undo the committed
	// admission (transaction A).  Retry after clearing the fault reuses the
	// same admission and creates exactly one Run.
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	current, session := readyExecutionTask(t, db, "events")
	value, reserved, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != executionproposal.StatusAdmitted || reserved.ID != "" {
		t.Fatal("scalar admission did not separate from Run creation")
	}
	if _, err := db.sql.Exec(`CREATE TRIGGER execution_event_fault BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'injected runtime fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := continueAdmittedExecution(ctx, db, value.ID, testNow); err == nil {
		t.Fatal("fault did not abort Run creation")
	}
	after, _ := NewTaskStore(db).Get(ctx, current.ID)
	if after.CurrentExecutionID == "" || after.Phase != task.PhaseImplementationReady {
		t.Fatal("partial authority committed")
	}
	admitted, err := NewExecutionProposalStore(db).Get(ctx, value.ID)
	if err != nil || admitted.Status != executionproposal.StatusAdmitted {
		t.Fatalf("admission did not survive Run creation fault: %+v %v", admitted, err)
	}
	if _, err := db.sql.Exec(`DROP TRIGGER execution_event_fault`); err != nil {
		t.Fatal(err)
	}
	first, r, err := continueAdmittedExecution(ctx, db, value.ID, testNow)
	if err != nil {
		t.Fatal(err)
	}
	next, replay, err := continueAdmittedExecution(ctx, db, value.ID, testNow)
	if err != nil || first.ID != next.ID || r.ID != replay.ID || r.ID == "" {
		t.Fatal("retry allocated another operation")
	}
}
func TestTaskOwnedExecutionPurposeAndCurrentPointerFence(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	current, session := readyExecutionTask(t, db, "purpose")
	value, r, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status == executionproposal.StatusAdmitted && r.ID == "" {
		value, r, err = continueAdmittedExecution(ctx, db, value.ID, testNow)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Even a different attempt cannot add a second business Run for one purpose.
	if _, err = db.sql.Exec(`INSERT INTO runs(id,task_id,project_id,feature_area_id,operation_kind,operation_id,execution_proposal_id,purpose,attempt,status,runtime_epoch,agent_execution_id,created_at,updated_at) SELECT 'duplicate-purpose',task_id,project_id,feature_area_id,operation_kind,operation_id,execution_proposal_id,purpose,attempt+1,status,runtime_epoch,agent_execution_id,created_at,updated_at FROM runs WHERE id=?`, r.ID); err == nil {
		t.Fatal("duplicate implementation purpose accepted")
	}
	if _, err = db.sql.Exec(`UPDATE tasks SET current_execution_id=NULL WHERE id=?`, current.ID); err == nil {
		t.Fatal("current fence cleared")
	}
	duplicate := value
	duplicate.ID = "unauthorized-candidate"
	duplicate.Status = executionproposal.StatusPendingConfirmation
	duplicate.RunID = ""
	duplicate.ImplementationRunID = ""
	if err = NewExecutionProposalStore(db).Create(ctx, duplicate); err == nil {
		t.Fatal("second authorization path accepted")
	}
}

// TestTerminalTasksCannotConsumeExecutionStartAuthority proves the durable
// start boundary rejects SUCCEEDED/FAILED/CANCELED Tasks before any side
// effect: no candidate, no Run, no consumed authority, no ledger fact, and a
// byte-equivalent Task row.
func TestTerminalTasksCannotConsumeExecutionStartAuthority(t *testing.T) {
	ctx := context.Background()
	for _, terminal := range []task.Status{task.StatusSucceeded, task.StatusFailed, task.StatusCanceled} {
		t.Run(string(terminal), func(t *testing.T) {
			db := openTestDB(t, t.TempDir())
			defer db.Close()
			current, session := readyExecutionTask(t, db, "terminal-"+string(terminal))
			if _, err := db.sql.ExecContext(ctx, `UPDATE tasks SET status=? WHERE id=?`, string(terminal), current.ID); err != nil {
				t.Fatal(err)
			}
			type counts struct{ proposals, runs, events int }
			readCounts := func() counts {
				var value counts
				if err := db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM execution_proposals WHERE task_id=?),(SELECT count(*) FROM runs WHERE task_id=?),(SELECT count(*) FROM events WHERE task_id=?)`, current.ID, current.ID, current.ID).Scan(&value.proposals, &value.runs, &value.events); err != nil {
					t.Fatal(err)
				}
				return value
			}
			type taskRow struct {
				status, phase, currentExecutionID, updatedAfter string
			}
			readTask := func() taskRow {
				var value taskRow
				if err := db.sql.QueryRowContext(ctx, `SELECT status,phase,current_execution_id,updated_at FROM tasks WHERE id=?`, current.ID).Scan(&value.status, &value.phase, &value.currentExecutionID, &value.updatedAfter); err != nil {
					t.Fatal(err)
				}
				return value
			}
			beforeCounts, beforeTask := readCounts(), readTask()
			if _, _, err := NewWorkflowProgressionStore(db).ReserveTaskExecution(ctx, session.ID, current.ID); !errors.Is(err, executionproposal.ErrConflict) {
				t.Fatalf("terminal %s start: err=%v", terminal, err)
			}
			if after := readCounts(); after != beforeCounts {
				t.Fatalf("terminal %s start produced side effects: before=%+v after=%+v", terminal, beforeCounts, after)
			}
			if after := readTask(); after != beforeTask {
				t.Fatalf("terminal %s start mutated the Task row: before=%+v after=%+v", terminal, beforeTask, after)
			}
		})
	}
}
