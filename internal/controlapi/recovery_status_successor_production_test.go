package controlapi_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestRecoveryStatusOfflineCompleteThenVerificationSameServer(t *testing.T) {
	runRecoveryStatusOffline(t, "complete-then-verification")
}

func TestRecoveryStatusOfflinePartialThenVerificationSameServer(t *testing.T) {
	runRecoveryStatusOffline(t, "partial-then-verification")
}

func TestRecoveryStatusOfflineReviewThenExecutionSameServer(t *testing.T) {
	runRecoveryStatusOffline(t, "review-then-execution")
}

func approveRecoveredReview(t *testing.T, f *upperLifecycleFixture, report *review.ReviewReport) {
	t.Helper()
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "recover-review-approve", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, ReviewID: current.CurrentReviewID, DecisionID: wp.DecisionID(current.CurrentReviewID)}, Variants: []c.Variant{{ID: "approve", Summary: "Formally approve recovered Review without implicit start", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: report.RepositoryTargets, PrimaryMemberID: report.PrimaryMemberID, WorkSpec: report.WorkSpec, VerificationSpec: report.VerificationSpec}}})
	if err != nil {
		t.Fatalf("prepare recovered Review approval: %v cause=%v", err, errors.Unwrap(err))
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "recover-review-approve-commit", ContentHash: prepared.ContentHash, VariantID: "approve"}); err != nil {
		t.Fatalf("commit recovered Review approval: %v cause=%v", err, errors.Unwrap(err))
	}
}

func verifyRecoveredAutomaticSuccessor(t *testing.T, f *upperLifecycleFixture, threadID string, original appserver.Binding, calls *atomic.Int64, implementationID string, expectedCalls int64) {
	t.Helper()
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if implementationID == "" {
		prepared, e := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "recovered-review-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, ReviewID: current.CurrentReviewID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: &wp.TaskExecutionExpectation{FeatureAreaID: current.FeatureAreaID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, Phase: current.Phase}}, Variants: []c.Variant{{ID: "start", Summary: "Start formally approved recovered Review", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
		if e != nil {
			t.Fatalf("prepare recovered Review start: %v cause=%v", e, errors.Unwrap(e))
		}
		options, e := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if e != nil || len(options.BackendCandidates) == 0 {
			t.Fatalf("start backend options: %v", e)
		}
		backend := options.BackendCandidates[0]
		started, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "recovered-review-start-commit", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
		if e != nil || len(started.Steps) != 1 {
			t.Fatalf("start approved recovered Review: %v cause=%v", e, errors.Unwrap(e))
		}
		implementationID = started.Steps[0].RunID
	}
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		impl, e := f.plane.Runs.Get(f.ctx, implementationID)
		if e != nil {
			t.Fatal(e)
		}
		if impl.Failure != nil {
			t.Fatalf("review successor implementation failed: %+v", impl.Failure)
		}
		current, err = f.plane.Tasks.Get(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if current.Phase == task.PhaseAcceptancePending {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, current.CurrentExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != task.PhaseAcceptancePending || proposal.VerificationRunID == "" || calls.Load() != expectedCalls {
		t.Fatalf("review successor phase=%s verification=%s calls=%d", current.Phase, proposal.VerificationRunID, calls.Load())
	}
	for _, id := range []string{implementationID, proposal.VerificationRunID} {
		value, e := f.plane.Runs.Get(f.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		if value.Status != run.StatusSucceeded || value.ThreadID != threadID || binding.ID != original.ID || binding.PID != original.PID {
			t.Fatalf("review successor identity/state run=%s thread=%s state=%s server=%s", id, value.ThreadID, value.Status, binding.ID)
		}
		if value.Purpose == run.PurposeVerification {
			if value.VerificationReport == nil {
				t.Fatal("missing Verification facts")
			}
			for _, check := range value.VerificationReport.Checks {
				if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 {
					t.Fatalf("missing required check %s", check.ID)
				}
			}
		}
	}
}

func recoveredVerificationInputs(t *testing.T, f *upperLifecycleFixture, sourceRunID string) ([]map[string]any, string) {
	t.Helper()
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.plane.Runs.RuntimeWorkset(f.ctx, sourceRunID)
	if err != nil {
		t.Fatal(err)
	}
	roots := map[string]string{}
	for _, member := range work.Members {
		roots[member.RepositoryID] = member.CanonicalRoot
	}
	checks := []map[string]any{}
	results := []map[string]any{}
	criteria := []map[string]any{}
	for i, check := range current.VerificationSpec.Checks {
		checks = append(checks, map[string]any{"cmd": check.Command, "workdir": filepath.Join(roots[check.RepositoryID], check.WorkingDirectory), "max_output_tokens": 1000})
		results = append(results, map[string]any{"index": i, "status": "PASSED", "summary": "Exact native command passed", "evidence": "Authoritative command exited zero", "reason": ""})
	}
	for i := range current.VerificationSpec.AcceptanceCriteria {
		criteria = append(criteria, map[string]any{"index": i, "status": "PASSED", "summary": "Approved checks passed", "evidence": "Actual native observations", "reason": ""})
	}
	encoded, err := json.Marshal(map[string]any{"version": 2, "summary": "Approved verification checks passed", "checks": results, "acceptanceCriteria": criteria})
	if err != nil {
		t.Fatal(err)
	}
	return checks, string(encoded)
}

func verifyRecoveredImplementationSuccessor(t *testing.T, f *upperLifecycleFixture, sourceRunID, sourceThreadID string, original appserver.Binding, calls *atomic.Int64) {
	t.Helper()
	expectedCalls := calls.Load() + 4
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if current.Phase != task.PhaseVerificationReady {
		t.Fatalf("recovered implementation phase=%s", current.Phase)
	}
	before, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, sourceRunID)
	if err != nil {
		t.Fatal(err)
	}
	var originalDecision []byte
	requestID := ""
	for _, request := range before.Requests {
		if agentruntime.ResponseDisposition(request) == "UNKNOWN" {
			requestID = request.ID
			originalDecision, _ = json.Marshal(request.UserDecision)
			break
		}
	}
	if requestID == "" {
		t.Fatal("source UNKNOWN request missing before successor")
	}
	proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, current.CurrentExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := f.facade.StartTaskExecutionExpectedCommand(f.ctx, f.session, current.ID, "START_VERIFICATION", "recovered-complete-verification", wp.TaskExecutionExpectation{FeatureAreaID: current.FeatureAreaID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID, Phase: current.Phase, PrimaryMemberID: current.PrimaryMemberID, ApprovedWorksetHash: proposal.ApprovedWorksetHash})
	if err != nil {
		t.Fatalf("start verification: %v cause=%v", err, errors.Unwrap(err))
	}
	var verified run.Run
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		verified, err = f.plane.Runs.Get(f.ctx, started.Run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Failure != nil {
			t.Fatalf("successor verification failed stage=%s cause=%s", verified.Failure.Stage, verified.Failure.Message)
		}
		if verified.Status == run.StatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	binding, err := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, verified.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.ID == sourceRunID || verified.Purpose != run.PurposeVerification || verified.ThreadID != sourceThreadID || binding.ID != original.ID || binding.PID != original.PID || verified.Status != run.StatusSucceeded || calls.Load() != expectedCalls {
		t.Fatalf("successor identity/state run=%s purpose=%s thread=%s server=%s status=%s calls=%d", verified.ID, verified.Purpose, verified.ThreadID, binding.ID, verified.Status, calls.Load())
	}
	if verified.VerificationReport == nil {
		t.Fatal("successor omitted verification evidence")
	}
	for _, check := range verified.VerificationReport.Checks {
		if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 {
			t.Fatalf("missing actual check observation: %s", check.ID)
		}
	}
	current, err = f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseAcceptancePending {
		t.Fatalf("verification did not reach acceptance pending: %s %v", current.Phase, err)
	}
	bindings := sqlite.NewAgentRuntimeStore(f.plane.DB)
	if keep, err := bindings.RecoveryAppServerNeedsContinuity(f.ctx, original.ID); err != nil || keep {
		t.Fatalf("finished owner retained idle process: keep=%v err=%v", keep, err)
	}
	if keep, err := bindings.RecoveryAppServerNeedsContinuity(f.ctx, "unrelated-test-instance"); err != nil || keep {
		t.Fatalf("unrelated process retained: keep=%v err=%v", keep, err)
	}
	if _, err := bindings.RecoveryContinuationBinding(f.ctx, verified.ID); !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, recoverystatus.ErrConflict) {
		t.Fatalf("terminal successor retained dispatch authority: %v", err)
	}
	if _, err := bindings.RecoveryContinuationBinding(f.ctx, sourceRunID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("original source gained successor authority: %v", err)
	}
	after, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, sourceRunID)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range after.Requests {
		if request.ID == requestID {
			decision, _ := json.Marshal(request.UserDecision)
			if agentruntime.ResponseDisposition(request) != "UNKNOWN" || string(decision) != string(originalDecision) {
				t.Fatal("successor rewrote original answer disposition")
			}
			return
		}
	}
	t.Fatal("successor removed original UNKNOWN request")
}
