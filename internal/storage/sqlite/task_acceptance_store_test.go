package sqlite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func acceptedExecutionFixture(t *testing.T, suffix string) (*reviewApprovalFixture, task.Task, executionproposal.Proposal) {
	t.Helper()
	f := newReviewApprovalFixture(t)
	f.candidate.EngineeringGoal += suffix
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	approved, err := f.store.Resolve(context.Background(), f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, approved.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, *approved.Task, proposal
}

func acceptanceTestClock(start time.Time) func() time.Time {
	var mu sync.Mutex
	current := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		return current
	}
}

type taskAcceptanceHistory struct {
	Task       task.Task
	Executions []executionproposal.Proposal
	Runs       []run.Run
	Receipts   []decisionresolution.Receipt
}

func loadTaskAcceptanceHistory(t *testing.T, f *reviewApprovalFixture, taskID string) taskAcceptanceHistory {
	t.Helper()
	ctx := context.Background()
	owner, err := NewTaskStore(f.db).Get(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	executions, err := NewExecutionProposalStore(f.db).ListByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := NewRunStore(f.db).ListByTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := NewDecisionResolutionStore(f.db).ListTaskAcceptance(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	return taskAcceptanceHistory{Task: owner, Executions: executions, Runs: runs, Receipts: receipts}
}

func runFromHistory(t *testing.T, history taskAcceptanceHistory, id string) run.Run {
	t.Helper()
	for _, value := range history.Runs {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("run %s absent from Task-keyed history", id)
	return run.Run{}
}

func executionFromHistory(t *testing.T, history taskAcceptanceHistory, id string) executionproposal.Proposal {
	t.Helper()
	for _, value := range history.Executions {
		if value.ID == id {
			return value
		}
	}
	t.Fatalf("execution %s absent from Task-keyed history", id)
	return executionproposal.Proposal{}
}

func receiptFromHistory(t *testing.T, history taskAcceptanceHistory, executionID string) decisionresolution.Receipt {
	t.Helper()
	for _, value := range history.Receipts {
		if value.SubjectID == executionID {
			return value
		}
	}
	t.Fatalf("acceptance receipt for %s absent from Task-keyed history", executionID)
	return decisionresolution.Receipt{}
}

func terminalExecutionWithReport(t *testing.T, f *reviewApprovalFixture, proposal executionproposal.Proposal) executionproposal.Proposal {
	return terminalExecutionEvidence(t, f, proposal, run.VerificationCheckFailed, "")
}

func terminalExecutionEvidence(t *testing.T, f *reviewApprovalFixture, proposal executionproposal.Proposal, fact run.VerificationCheckStatus, failureCode string, completedCriteria ...bool) executionproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	proposals := NewExecutionProposalStore(f.db)
	clock := acceptanceTestClock(proposal.CreatedAt.Add(time.Minute))
	runStore := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-run-id", nil }, clock)
	_, implementation, err := proposals.ConsumeAndCreateRun(ctx, proposal.ID, "impl-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if _, err = runStore.Transition(ctx, implementation.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == run.StatusPreparing {
			if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = NewAgentConversationStore(f.db).FinishRun(ctx, implementation.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = runStore.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	_, verification, err := proposals.ReserveVerificationRun(ctx, proposal.ID, "verify-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning} {
		if _, err = runStore.Transition(ctx, verification.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == run.StatusPreparing {
			if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, verification.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	declared := proposal.VerificationSpec.Checks[0]
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "facts", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(declared.Kind), Required: declared.Required, DeclaredCommand: declared.Command, Status: run.VerificationCheckFailed, Observed: &run.VerificationObservedFact{Status: run.VerificationCheckFailed, ExitCode: intPointer(1), Summary: "observed command exited nonzero"}, Reported: run.VerificationReportedFact{Status: run.VerificationCheckFailed, Summary: "failed"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: proposal.VerificationSpec.AcceptanceCriteria[0], Reported: run.VerificationReportedFact{Status: run.VerificationCheckUnknown, Summary: "unknown"}}}}
	report.Checks[0].Status, report.Checks[0].Observed.Status, report.Checks[0].Reported.Status = fact, fact, fact
	if len(completedCriteria) > 0 && completedCriteria[0] {
		report.Checks[0].Observed.ExitCode = intPointer(0)
		report.AcceptanceCriteria[0].Reported.Status = run.VerificationCheckPassed
	}
	if failureCode == "" {
		if _, err = runStore.AttachVerificationReport(ctx, verification.ID, report); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = runStore.Transition(ctx, verification.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if err = NewAgentConversationStore(f.db).FinishRun(ctx, verification.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	outcome := executionproposal.OutcomeSucceeded
	if failureCode != "" {
		outcome = executionproposal.OutcomeFailed
		_, err = runStore.Fail(ctx, verification.ID, run.Failure{Kind: run.FailureInfrastructure, Code: failureCode, Stage: "verification mutation policy", Message: "Verification modified the managed worktree"})
	} else {
		_, err = runStore.Succeed(ctx, verification.ID, run.Result{})
	}
	if err != nil {
		t.Fatal(err)
	}
	completed, err := proposals.Complete(ctx, proposal.ID, outcome, failureCode, "", clock())
	if err != nil {
		t.Fatal(err)
	}
	f.store.clock = clock
	return completed
}

func intPointer(v int) *int { return &v }

func terminalExecutionWithoutVerification(t *testing.T, f *reviewApprovalFixture, proposal executionproposal.Proposal) executionproposal.Proposal {
	t.Helper()
	ctx := context.Background()
	proposals := NewExecutionProposalStore(f.db)
	clock := acceptanceTestClock(proposal.CreatedAt.Add(time.Minute))
	runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-run-id", nil }, clock)
	_, implementation, err := proposals.ConsumeAndCreateRun(ctx, proposal.ID, "impl-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if _, err = runs.Transition(ctx, implementation.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == run.StatusPreparing {
			if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = NewAgentConversationStore(f.db).FinishRun(ctx, implementation.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	completed, err := proposals.Complete(ctx, proposal.ID, executionproposal.OutcomeSucceeded, "", "", clock())
	if err != nil {
		t.Fatal(err)
	}
	f.store.clock = clock
	return completed
}

func TestTaskAcceptanceAcceptIgnoresFailedReportFacts(t *testing.T) {
	f, taskValue, proposal := acceptedExecutionFixture(t, "-accept")
	proposal = terminalExecutionWithReport(t, f, proposal)
	attachCommitWorkspace(t, f, taskValue.ID, proposal.VerificationRunID)
	out, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceAccept, RequestID: "accept-1"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Task.Status != task.StatusSucceeded || out.Receipt.ResultID != taskValue.ID {
		t.Fatalf("resolution=%+v", out)
	}
}

func TestAuthoritativeVerificationPassesRequiresObservedChecksAndPassedCriteria(t *testing.T) {
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckCustom, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"all checks pass"}}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "verified", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(task.VerificationCheckCustom), Required: true, DeclaredCommand: "go test ./...", Observed: &run.VerificationObservedFact{Status: run.VerificationCheckPassed, Summary: "exit 0"}, Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "passed"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: "all checks pass", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "met"}}}}
	if !authoritativeVerificationPasses(report, spec) {
		t.Fatal("complete bridge-observed PASS was rejected")
	}
	report.Checks[0].Observed.Status = run.VerificationCheckFailed
	if authoritativeVerificationPasses(report, spec) {
		t.Fatal("failed bridge-observed required check accepted")
	}
	report.Checks[0].Observed.Status = run.VerificationCheckPassed
	report.AcceptanceCriteria[0].Reported.Status = run.VerificationCheckFailed
	if authoritativeVerificationPasses(report, spec) {
		t.Fatal("failed acceptance criterion accepted")
	}
}

func TestCustomAssessmentCannotResolveRecoveryAsAuthoritativeProof(t *testing.T) {
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckCustom, Instruction: "Exercise real Codex isolation", Required: true}}, AcceptanceCriteria: []string{"complete"}}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "GPT assessment", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: "CUSTOM", Required: true, Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "assessed"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: "complete", Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "met"}}}}
	if !run.VerificationAssessmentPassed(report, spec) || authoritativeVerificationPasses(report, spec) {
		t.Fatal("explicit completion assessment was confused with authoritative recovery evidence")
	}
}

func TestAvailableReportMustMatchExactExecutionVerificationSpec(t *testing.T) {
	spec := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"criterion"}, CodexInstruction: "verify"}
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "facts", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(spec.Checks[0].Kind), Required: true, DeclaredCommand: spec.Checks[0].Command, Reported: run.VerificationReportedFact{Status: run.VerificationCheckUnknown, Summary: "reported"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: spec.AcceptanceCriteria[0], Reported: run.VerificationReportedFact{Status: run.VerificationCheckUnknown, Summary: "reported"}}}}
	if !reportMatchesVerificationSpec(report, spec) {
		t.Fatal("exact report binding rejected")
	}
	report.AcceptanceCriteria[0].Criterion = "different criterion"
	if reportMatchesVerificationSpec(report, spec) {
		t.Fatal("AVAILABLE report bound to a different criterion was accepted")
	}
}

func TestTaskAcceptanceAcceptsExactUserWaiverWithoutVerificationRun(t *testing.T) {
	f, _, first := acceptedExecutionFixture(t, "waiver")
	first = terminalExecutionWithReport(t, f, first)
	work := task.CloneWorkSpec(first.WorkSpec)
	waiver := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User explicitly waived a Verification Run"}}
	outChange, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestChanges, RequestID: "request-user-waiver", NextWorkSpec: &work, NextVerificationSpec: &waiver})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewExecutionProposalStore(f.db).Get(context.Background(), outChange.SuccessorExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	proposal = terminalExecutionWithoutVerification(t, f, proposal)
	attachCommitWorkspace(t, f, proposal.TaskID, proposal.ImplementationRunID)
	out, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceAccept, RequestID: "accept-waiver"})
	if err != nil || out.Task.Status != task.StatusSucceeded {
		t.Fatalf("waiver acceptance=%+v err=%v", out, err)
	}
}

func TestTaskAcceptanceChangesCreatesSameTaskSuccessorAndRejectsStaleCallback(t *testing.T) {
	f, taskValue, first := acceptedExecutionFixture(t, "-changes")
	first = terminalExecutionWithReport(t, f, first)
	nextWork := task.CloneWorkSpec(first.WorkSpec)
	nextWork.CodexInstruction = "Apply the requested bounded correction."
	nextWork.Constraints = append(nextWork.Constraints, "Preserve the approved architecture")
	nextVerification := task.CloneVerificationSpec(first.VerificationSpec)
	invalidWork := task.CloneWorkSpec(nextWork)
	invalidWork.ExecutionScope.Paths = []string{"../outside-approved-scope"}
	if _, invalidErr := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestChanges, RequestID: "changes-out-of-scope", NextWorkSpec: &invalidWork, NextVerificationSpec: &nextVerification}); invalidErr == nil {
		t.Fatal("REQUEST_CHANGES accepted a non-canonical scope outside the approved repository boundary")
	}
	in := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestChanges, RequestID: "changes-1", NextWorkSpec: &nextWork, NextVerificationSpec: &nextVerification}
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true}
	out, err := f.store.ResolveTaskAcceptance(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Task.ID != taskValue.ID || out.Task.Status != task.StatusActive || out.SuccessorExecutionID == "" {
		t.Fatalf("resolution=%+v", out)
	}
	successor, err := NewExecutionProposalStore(f.db).Get(context.Background(), out.SuccessorExecutionID)
	if err != nil || successor.TaskID != first.TaskID || successor.TaskDefinitionRevisionID == first.TaskDefinitionRevisionID || successor.Status != executionproposal.StatusConfirmed {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
	if got := successor.RuntimeConfig; got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "high" || !got.CodexFastMode || got.ServiceTier != "priority" {
		t.Fatalf("runtime config=%+v", got)
	}
	if _, err = f.db.SQL().Exec(`UPDATE tasks SET goal='unauthorized post-successor mutation' WHERE id=?`, taskValue.ID); err == nil {
		t.Fatal("approved Task semantics remained mutable after successor creation closed the receipt window")
	}
	replayed, err := f.store.ResolveTaskAcceptance(context.Background(), in)
	if err != nil || !replayed.Replay || replayed.SuccessorExecutionID != successor.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changedReplay := task.CloneWorkSpec(nextWork)
	changedReplay.CodexInstruction = "different retry payload"
	in.NextWorkSpec = &changedReplay
	if _, err = f.store.ResolveTaskAcceptance(context.Background(), in); err == nil {
		t.Fatal("response-loss replay accepted a different successor definition hash")
	}
	in.NextWorkSpec = &nextWork
	changedConfig := *in.NextRunRuntimeConfig
	changedConfig.CodexFastMode = false
	in.NextRunRuntimeConfig = &changedConfig
	if _, err = f.store.ResolveTaskAcceptance(context.Background(), in); err == nil {
		t.Fatal("response-loss replay accepted different runtime config")
	}
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true}
	stale := in
	stale.RequestID = "stale-same-option-new-request"
	if _, err = f.store.ResolveTaskAcceptance(context.Background(), stale); err == nil {
		t.Fatal("stale callback with a new requestId converged onto historical receipt")
	}
	if _, err = f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceReject, RequestID: "stale-old"}); err == nil {
		t.Fatal("stale old acceptance callback succeeded")
	}
	second := terminalExecutionWithReport(t, f, successor)
	attachCommitWorkspace(t, f, taskValue.ID, second.VerificationRunID)
	accepted, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(second.ID), ExecutionID: second.ID, Option: wp.AcceptanceAccept, RequestID: "accept-second"})
	if err != nil || accepted.Task.ID != taskValue.ID || accepted.Task.Status != task.StatusSucceeded {
		t.Fatalf("second acceptance=%+v err=%v", accepted, err)
	}
	history := loadTaskAcceptanceHistory(t, f, taskValue.ID)
	if history.Task.Status != task.StatusSucceeded || len(history.Executions) != 2 || len(history.Runs) != 5 || len(history.Receipts) != 2 {
		t.Fatalf("incomplete Task-keyed history after changes: %+v", history)
	}
	durableFirst := executionFromHistory(t, history, first.ID)
	durableSecond := executionFromHistory(t, history, second.ID)
	if durableFirst.Outcome != executionproposal.OutcomeSucceeded || durableSecond.Outcome != executionproposal.OutcomeSucceeded {
		t.Fatalf("Execution outcomes were conflated with Task acceptance: first=%s second=%s", durableFirst.Outcome, durableSecond.Outcome)
	}
	firstVerification := runFromHistory(t, history, first.VerificationRunID)
	secondVerification := runFromHistory(t, history, second.VerificationRunID)
	if firstVerification.Status != run.StatusSucceeded || secondVerification.Status != run.StatusSucceeded || firstVerification.VerificationReport == nil || secondVerification.VerificationReport == nil || firstVerification.VerificationReport.Checks[0].Status != run.VerificationCheckFailed || secondVerification.VerificationReport.Checks[0].Status != run.VerificationCheckFailed {
		t.Fatalf("Run transport and Report facts were not independently retained: first=%+v second=%+v", firstVerification, secondVerification)
	}
	if receiptFromHistory(t, history, first.ID).Option != wp.AcceptanceRequestChanges || receiptFromHistory(t, history, second.ID).Option != wp.AcceptanceAccept {
		t.Fatalf("Execution acceptance history=%+v", history.Receipts)
	}
}

func TestTaskAcceptanceReverificationCreatesVerificationOnlySuccessor(t *testing.T) {
	f, _, first := acceptedExecutionFixture(t, "-reverify")
	first = terminalExecutionWithReport(t, f, first)
	config := &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh", CodexFastMode: false}
	input := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(first.ID), ExecutionID: first.ID, Option: wp.AcceptanceRequestReverification, RequestID: "reverify-1", NextRunRuntimeConfig: config}
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), input); err == nil {
		t.Fatal("reverification must not replace frozen runtime configuration")
	}
	input.NextRunRuntimeConfig = nil
	config = &first.RuntimeConfig
	out, err := f.store.ResolveTaskAcceptance(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	proposals := NewExecutionProposalStore(f.db)
	clock := acceptanceTestClock(first.CreatedAt.Add(2 * time.Minute))
	successor, verification, err := proposals.ConsumeAndCreateVerificationRun(context.Background(), out.SuccessorExecutionID, "reverify-run", clock())
	if err != nil {
		t.Fatal(err)
	}
	if got := successor.RuntimeConfig; got != *config {
		t.Fatalf("runtime=%+v", got)
	}
	if successor.RunID != verification.ID || successor.VerificationRunID != verification.ID || successor.ImplementationRunID != "" || verification.Purpose != run.PurposeVerification {
		t.Fatalf("successor=%+v run=%+v", successor, verification)
	}
	runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-reverification-run-id", nil }, clock)
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning} {
		if _, err = runs.Transition(context.Background(), verification.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == run.StatusPreparing {
			if _, err = NewAgentConversationStore(f.db).BeginRun(context.Background(), verification.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	declared := successor.VerificationSpec.Checks[0]
	report := run.VerificationReport{Version: 2, Availability: run.VerificationReportAvailable, Summary: "reverification facts", Checks: []run.VerificationCheckOutcome{{ID: "check-1", Index: 0, Kind: string(declared.Kind), Required: declared.Required, DeclaredCommand: declared.Command, Status: run.VerificationCheckPassed, Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "reported"}}}, AcceptanceCriteria: []run.VerificationCriterionOutcome{{Index: 0, Criterion: successor.VerificationSpec.AcceptanceCriteria[0], Reported: run.VerificationReportedFact{Status: run.VerificationCheckPassed, Summary: "reported"}}}}
	if _, err = runs.AttachVerificationReport(context.Background(), verification.ID, report); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(context.Background(), verification.ID, run.StatusCollecting); err != nil {
		t.Fatal(err)
	}
	if err = NewAgentConversationStore(f.db).FinishRun(context.Background(), verification.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Succeed(context.Background(), verification.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	if successor, err = proposals.Complete(context.Background(), successor.ID, executionproposal.OutcomeSucceeded, "", "", clock()); err != nil {
		t.Fatal(err)
	}
	f.store.clock = clock
	attachCommitWorkspace(t, f, first.TaskID, verification.ID)
	accepted, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(successor.ID), ExecutionID: successor.ID, Option: wp.AcceptanceAccept, RequestID: "accept-reverified"})
	if err != nil || accepted.Task.Status != task.StatusSucceeded {
		t.Fatalf("reverified acceptance=%+v err=%v", accepted, err)
	}
	history := loadTaskAcceptanceHistory(t, f, first.TaskID)
	if history.Task.Status != task.StatusSucceeded || len(history.Executions) != 2 || len(history.Runs) != 4 || len(history.Receipts) != 2 {
		t.Fatalf("incomplete Task-keyed reverification history: %+v", history)
	}
	reverified := runFromHistory(t, history, verification.ID)
	durableSuccessor := executionFromHistory(t, history, successor.ID)
	if reverified.Status != run.StatusSucceeded || reverified.VerificationReport == nil || reverified.VerificationReport.Checks[0].Status != run.VerificationCheckPassed || durableSuccessor.Outcome != executionproposal.OutcomeSucceeded {
		t.Fatalf("reverification layers transport=%s report=%+v execution=%s", reverified.Status, reverified.VerificationReport, durableSuccessor.Outcome)
	}
	if receiptFromHistory(t, history, first.ID).Option != wp.AcceptanceRequestReverification || receiptFromHistory(t, history, successor.ID).Option != wp.AcceptanceAccept {
		t.Fatalf("reverification acceptance history=%+v", history.Receipts)
	}
}

func TestTaskAcceptanceRejectCancelsTaskOnlyOnExplicitUserDecision(t *testing.T) {
	f, _, proposal := acceptedExecutionFixture(t, "-reject")
	proposal = terminalExecutionWithReport(t, f, proposal)
	out, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceReject, RequestID: "reject-task"})
	if err != nil || out.Task.Status != task.StatusCanceled || out.Receipt.Option != wp.AcceptanceReject {
		t.Fatalf("rejection=%+v err=%v", out, err)
	}
	history := loadTaskAcceptanceHistory(t, f, proposal.TaskID)
	verification := runFromHistory(t, history, proposal.VerificationRunID)
	durableExecution := executionFromHistory(t, history, proposal.ID)
	if history.Task.Status != task.StatusCanceled || len(history.Executions) != 1 || len(history.Runs) != 3 || len(history.Receipts) != 1 || durableExecution.Outcome != executionproposal.OutcomeSucceeded || verification.Status != run.StatusSucceeded || verification.VerificationReport == nil || verification.VerificationReport.Checks[0].Status != run.VerificationCheckFailed || receiptFromHistory(t, history, proposal.ID).Option != wp.AcceptanceReject {
		t.Fatalf("reject did not preserve four independent layers: %+v", history)
	}
}

func TestTaskAcceptanceRecoveryStateBlocksPendingMutation(t *testing.T) {
	f, _, proposal := acceptedExecutionFixture(t, "-recovery")
	proposal = terminalExecutionWithReport(t, f, proposal)
	var epoch string
	if err := f.db.sql.QueryRow(`SELECT epoch_id FROM runtime_authority WHERE singleton=1`).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','test recovery',?)`, proposal.VerificationRunID, epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceReject, RequestID: "reject-during-recovery"}); err == nil {
		t.Fatal("acceptance mutation bypassed recovery")
	}
}

func TestTaskAcceptanceSameFeatureAreaRaceCreatesOneSuccessor(t *testing.T) {
	f, _, proposal := acceptedExecutionFixture(t, "-race")
	proposal = terminalExecutionWithReport(t, f, proposal)
	baseWork := task.CloneWorkSpec(proposal.WorkSpec)
	baseWork.CodexInstruction = "bounded successor A"
	verification := task.CloneVerificationSpec(proposal.VerificationSpec)
	inputs := []wp.TaskAcceptanceInput{
		{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceRequestChanges, RequestID: "race-a", NextWorkSpec: &baseWork, NextVerificationSpec: &verification},
		{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: wp.AcceptanceRequestReverification, RequestID: "race-b"},
	}
	var wait sync.WaitGroup
	errs := make(chan error, len(inputs))
	for _, input := range inputs {
		wait.Add(1)
		go func(in wp.TaskAcceptanceInput) {
			defer wait.Done()
			_, err := f.store.ResolveTaskAcceptance(context.Background(), in)
			errs <- err
		}(input)
	}
	wait.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	var executions int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM execution_proposals WHERE task_id=?`, proposal.TaskID).Scan(&executions); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || executions != 2 {
		t.Fatalf("successes=%d executions=%d", successes, executions)
	}
}

func TestTaskAcceptanceReportFactsNeverDecideTask(t *testing.T) {
	for _, fact := range []run.VerificationCheckStatus{run.VerificationCheckFailed, run.VerificationCheckBlocked, run.VerificationCheckNotRun, run.VerificationCheckUnknown} {
		t.Run(string(fact), func(t *testing.T) {
			f, owner, execution := acceptedExecutionFixture(t, "-facts")
			execution = terminalExecutionEvidence(t, f, execution, fact, "")
			attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
			before := loadTaskAcceptanceHistory(t, f, owner.ID)
			verification := runFromHistory(t, before, execution.VerificationRunID)
			if before.Task.Status != task.StatusActive || len(before.Receipts) != 0 || verification.Status != run.StatusSucceeded || verification.VerificationReport.Checks[0].Reported.Status != fact {
				t.Fatalf("report acquired Task authority: %+v", before)
			}
			input := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "accept-facts"}
			accepted, err := f.store.ResolveTaskAcceptance(context.Background(), input)
			if err != nil || accepted.Task.Status != task.StatusSucceeded {
				t.Fatalf("accept=%+v err=%v", accepted, err)
			}
			replay, err := f.store.ResolveTaskAcceptance(context.Background(), input)
			if err != nil || !replay.Replay || replay.Receipt != accepted.Receipt {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			input.Option = wp.AcceptanceReject
			if _, err = f.store.ResolveTaskAcceptance(context.Background(), input); err == nil {
				t.Fatal("same requestId accepted different payload")
			}
		})
	}
}

func TestTaskAcceptanceVerificationMutationEntersRecoveryAndReleasesClaim(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-mutation")
	execution = terminalExecutionEvidence(t, f, execution, run.VerificationCheckUnknown, "VERIFICATION_MUTATED_WORKTREE")
	history := loadTaskAcceptanceHistory(t, f, owner.ID)
	implementation := runFromHistory(t, history, execution.ImplementationRunID)
	verification := runFromHistory(t, history, execution.VerificationRunID)
	if history.Task.Status != task.StatusActive || implementation.Status != run.StatusSucceeded || verification.Status != run.StatusInfrastructureFailed || verification.Failure == nil || verification.Failure.Code != "VERIFICATION_MUTATED_WORKTREE" || verification.VerificationReport != nil || len(history.Receipts) != 0 {
		t.Fatalf("mutation conflated layers: %+v", history)
	}
	var claims int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, owner.FeatureAreaID).Scan(&claims); err != nil || claims != 0 {
		t.Fatalf("claims=%d err=%v", claims, err)
	}
	input := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "mutation-accept"}
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), input); err == nil {
		t.Fatal("accepted unavailable mutation report")
	}
	input.Option = wp.AcceptanceRequestReverification
	input.RequestID = "mutation-reverify"
	// The frozen recovery registry routes verification mutations into
	// RECOVERY_REQUIRED; ordinary acceptance options (including reverification)
	// may not leave it. Only the formal Resolve transaction exits that Phase.
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), input); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("reverify err=%v want proposal conflict", err)
	}
	if v, err := NewTaskStore(f.db).Get(context.Background(), owner.ID); err != nil || v.Status != task.StatusActive || v.Phase != task.PhaseRecoveryRequired {
		t.Fatalf("mutation task=%+v err=%v", v, err)
	}
}

func TestTaskAcceptanceHistoricalMutationRemainsTerminal(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-historical")
	execution = terminalExecutionEvidence(t, f, execution, run.VerificationCheckUnknown, "VERIFICATION_MUTATED_WORKTREE")
	// Seed the historical terminal written by the former Task reducer.
	if _, err := f.db.sql.Exec(`UPDATE tasks SET status='FAILED' WHERE id=?`, owner.ID); err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestReverification} {
		if _, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: option, RequestID: "historical-" + option}); err == nil {
			t.Fatal("historical Task reopened")
		}
	}
	history := loadTaskAcceptanceHistory(t, f, owner.ID)
	if history.Task.Status != task.StatusFailed || len(history.Receipts) != 0 {
		t.Fatalf("history rewritten: %+v", history)
	}
	legacy, err := decodeVerificationReportJSON(`{"version":1,"status":"VERIFICATION_MUTATED_WORKTREE","checks":[]}`)
	if err != nil || legacy.Availability != run.VerificationReportUnavailable || legacy.LegacyResultStatus != run.LegacyVerificationMutatedWorktree {
		t.Fatalf("legacy=%+v err=%v", legacy, err)
	}
}

func TestTaskAcceptanceRepeatedReverificationKeepsOriginalImplementation(t *testing.T) {
	f, owner, first := acceptedExecutionFixture(t, "-repeat-reverify")
	first = terminalExecutionWithReport(t, f, first)
	current := first
	clock := acceptanceTestClock(first.UpdatedAt.Add(time.Minute))
	f.store.clock = clock
	for _, suffix := range []string{"second", "third"} {
		out, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(current.ID), ExecutionID: current.ID, Option: wp.AcceptanceRequestReverification, RequestID: "repeat-" + suffix})
		if err != nil {
			t.Fatal(err)
		}
		next, verification, err := NewExecutionProposalStore(f.db).ConsumeAndCreateVerificationRun(context.Background(), out.SuccessorExecutionID, "verification-"+suffix, clock())
		if err != nil || next.TaskDefinitionRevisionID != first.TaskDefinitionRevisionID || next.ImplementationRunID != "" {
			t.Fatalf("next=%+v err=%v", next, err)
		}
		runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-id", nil }, clock)
		for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning} {
			if _, err = runs.Transition(context.Background(), verification.ID, status); err != nil {
				t.Fatal(err)
			}
			if status == run.StatusPreparing {
				if _, err = NewAgentConversationStore(f.db).BeginRun(context.Background(), verification.ID); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err = NewAgentConversationStore(f.db).FinishRun(context.Background(), verification.ID, agentconversation.NoAdvance, "", true); err != nil {
			t.Fatal(err)
		}
		// VERIFICATION_MUTATED_WORKTREE is a frozen recovery entry and would
		// block the next reverification; this lineage probe uses an ordinary
		// infrastructure code that never enters RECOVERY_REQUIRED.
		if _, err = runs.Fail(context.Background(), verification.ID, run.Failure{Kind: run.FailureInfrastructure, Code: "VERIFICATION_RUNTIME_UNAVAILABLE", Message: "verification runtime unavailable"}); err != nil {
			t.Fatal(err)
		}
		current, err = NewExecutionProposalStore(f.db).Complete(context.Background(), next.ID, executionproposal.OutcomeFailed, "VERIFICATION_RUNTIME_UNAVAILABLE", "verification runtime unavailable", clock())
		if err != nil {
			t.Fatal(err)
		}
	}
	history := loadTaskAcceptanceHistory(t, f, owner.ID)
	if len(history.Executions) != 3 || len(history.Runs) != 5 || len(history.Receipts) != 2 || history.Task.Status == task.StatusFailed {
		t.Fatalf("lineage=%+v", history)
	}
}

func TestTaskAcceptanceLegacyReportBytesRemainUnchanged(t *testing.T) {
	f, _, execution := acceptedExecutionFixture(t, "-legacy-bytes")
	execution = terminalExecutionEvidence(t, f, execution, run.VerificationCheckUnknown, "VERIFICATION_MUTATED_WORKTREE")
	original := `{ "version":1, "status":"VERIFICATION_MUTATED_WORKTREE", "checks":[] }`
	if _, err := f.db.sql.Exec(`UPDATE runs SET verification_result_json=? WHERE id=?`, original, execution.VerificationRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunStore(f.db).Update(context.Background(), execution.VerificationRunID, func(value *run.Run) error { value.UpdatedAt = value.UpdatedAt.Add(time.Second); return nil }); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := f.db.sql.QueryRow(`SELECT verification_result_json FROM runs WHERE id=?`, execution.VerificationRunID).Scan(&stored); err != nil || stored != original {
		t.Fatalf("legacy bytes rewritten: %q err=%v", stored, err)
	}
}

func TestFinalAcceptanceConcurrentReplayExactlyOneSuccessor(t *testing.T) {
	for _, option := range []string{wp.AcceptanceRequestChanges, wp.AcceptanceRequestReverification} {
		t.Run(option, func(t *testing.T) {
			f, _, e := acceptedExecutionFixture(t, "-same-request")
			e = terminalExecutionWithReport(t, f, e)
			in := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: e.TaskID, ExecutionID: e.ID, TaskDefinitionRevisionID: e.TaskDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), Option: option, RequestID: "concurrent-replay"}
			if option == wp.AcceptanceRequestChanges {
				work, verify := e.WorkSpec, e.VerificationSpec
				in.NextWorkSpec = &work
				in.NextVerificationSpec = &verify
			}
			start := make(chan struct{})
			results := make(chan wp.TaskAcceptanceResolution, 4)
			errs := make(chan error, 4)
			var wait sync.WaitGroup
			for i := 0; i < 4; i++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					out, err := f.store.ResolveTaskAcceptance(context.Background(), in)
					results <- out
					errs <- err
				}()
			}
			close(start)
			wait.Wait()
			close(results)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatal(err)
				}
			}
			successor := ""
			fresh := 0
			for out := range results {
				if !out.Replay {
					fresh++
				}
				if successor == "" {
					successor = out.SuccessorExecutionID
				}
				if successor == "" || successor != out.SuccessorExecutionID {
					t.Fatal("duplicate successor", out)
				}
			}
			var executions, revisions int
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM execution_proposals WHERE task_id=?`, e.TaskID).Scan(&executions); err != nil {
				t.Fatal(err)
			}
			if err := f.db.sql.QueryRow(`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`, e.TaskID).Scan(&revisions); err != nil {
				t.Fatal(err)
			}
			expectedRevisions := 2
			if option == wp.AcceptanceRequestChanges {
				expectedRevisions = 3
			}
			if fresh != 1 || executions != 2 || revisions != expectedRevisions {
				t.Fatalf("fresh=%d executions=%d revisions=%d", fresh, executions, revisions)
			}
		})
	}
}
