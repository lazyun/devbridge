package controlapi_test

import (
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunRevisionV2NoStartThenExplicitStart(t *testing.T) {
	t.Run("after-run-before-handoff", func(t *testing.T) { noRunRevisionV2StartRecovery(t, false) })
	t.Run("before-run-creation", func(t *testing.T) { noRunRevisionV2StartRecovery(t, true) })
}

func noRunRevisionV2StartRecovery(t *testing.T, beforeRun bool) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	installProtocolFixturePlatform(t, f)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	// Both revisions enter through the public formal producers. The explicit
	// test-only platform port preserves protocol coverage without treating the
	// app-server harness as independently measured production compatibility.
	approveRevision := func(action, requestID string, runtime *codexruntime.NextRunRuntimeConfig) task.Task {
		targets, work, verification, _ := submitLifecycleApprovalReady(t, f, requestID+"-result")
		parent, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: requestID, Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: parent.CurrentDefinitionRevisionID, ExecutionID: parent.CurrentExecutionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve exact reviewed revision", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
		if err != nil {
			t.Fatal(err)
		}
		commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: requestID + "-commit", ContentHash: prepared.ContentHash, VariantID: "approve"}
		if runtime != nil {
			opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
			if err != nil || len(opts.BackendCandidates) == 0 {
				t.Fatalf("approval options: %+v %v", opts, err)
			}
			backend := opts.BackendCandidates[0]
			commit.Selection = c.Selection{RuntimeConfig: runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}
		}
		approved, err := f.facade.CommitConfirmation(f.ctx, commit)
		if err != nil || approved.State != "COMPLETED" {
			t.Fatalf("formal %s: %+v %v", action, approved, err)
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	original := approveRevision("APPROVE_AND_START", "no-run-original-approve", &runtime)
	// T2 already durably admits the qualified owner without a Run. Do not
	// introduce a separate raw start receipt before the deliberate cancellation.
	pending, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, original.CurrentExecutionID)
	if err != nil || pending.Status != executionproposal.StatusAdmitted || pending.RunID != "" {
		t.Fatalf("formal pre-Run admission: %+v %v", pending, err)
	}
	cancel := executioncancel.Input{AISessionID: f.session, ProjectID: f.project, ExecutionID: pending.ID, OwnerType: "EXECUTION", OwnerID: pending.ID, RequestID: "no-run-v2-cancel"}
	if _, _, err = sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(f.ctx, cancel, false); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.NewExecutionCancellationStore(f.plane.DB).ProjectCanceledAdmittedExecutionToSuccessorRequired(f.ctx, f.task, pending.ID); err != nil {
		t.Fatal(err)
	}
	predecessor, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StopAndReviseNoRunTask(f.ctx, wp.StopAndReviseNoRunTask{ProjectID: f.project, TaskID: f.task, PredecessorExecutionID: pending.ID, ExpectedDefinitionRevisionID: original.CurrentDefinitionRevisionID, ActorAuthority: f.session}); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "no-run-new-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: f.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "Review revised intent", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Re-review both exact files"}}})
	if err != nil {
		t.Fatal(err)
	}
	started, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "no-run-new-review-commit", ContentHash: prepared.ContentHash, VariantID: "upper"})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("new Review: %+v %v", started, err)
	}
	f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil {
		t.Fatal(err)
	}
	current := approveRevision("APPROVE", "no-run-new-approve", nil)
	if current.CurrentExecutionID != pending.ID || current.CurrentDefinitionRevisionID == original.CurrentDefinitionRevisionID {
		t.Fatalf("NO_START identity: %+v", current)
	}
	if _, err = sqlite.NewExecutionCancellationStore(f.plane.DB).ProjectCanceledAdmittedExecutionToSuccessorRequired(f.ctx, f.task, pending.ID); err != nil {
		t.Fatalf("old cancellation replay after new approval: %v", err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	start, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "no-run-revision-start", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, ExecutionID: current.CurrentExecutionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Summary: "Start revised definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash}})
	if err != nil || len(opts.BackendCandidates) == 0 || opts.LockedRuntimeConfig != nil || opts.LockedBackend != nil {
		t.Fatalf("start options: %+v %v", opts, err)
	}
	newRuntime := runtime
	for _, option := range opts.RuntimeOptions {
		for _, effort := range option.SupportedReasoningEfforts {
			if option.ModelID != runtime.Model || effort != runtime.ReasoningEffort {
				newRuntime = codexruntime.NextRunRuntimeConfig{Model: option.ModelID, ReasoningEffort: effort}
				break
			}
		}
		if newRuntime != runtime {
			break
		}
	}
	if newRuntime == runtime {
		t.Fatal("fixture offers no distinct runtime selection")
	}
	selected := opts.BackendCandidates[0]
	commit := c.CommitInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, Action: start.Action, RequestID: "no-run-revision-start-commit", ContentHash: start.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &newRuntime, Backend: &codexconfig.BackendSelectionInput{ConfigID: selected.ConfigID, ConfigFingerprint: selected.ConfigFingerprint}}}
	committed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || committed.State != "COMPLETED" || len(committed.Steps) != 1 || committed.Steps[0].Effect != "PREPARATION_READY" || committed.Steps[0].ExecutionID == pending.ID || committed.Steps[0].ExecutionID == "" || committed.Steps[0].RunID != "" {
		t.Fatalf("revision admission: %+v %v", committed, err)
	}
	// The admission retains its original predecessor expectation. A caller may
	// only launch after refreshing the target to the newly admitted Execution.
	observed, observedRun, found, err := store.ReadTaskExecutionReservationCommand(f.ctx, f.session, f.task, "START_EXECUTION", committed.Steps[0].RequestID, *start.Content.Execution.ExpectedTarget)
	if err != nil || !found || observed.ID != committed.Steps[0].ExecutionID || observedRun.ID != "" {
		t.Fatalf("pre-Run admission readback: %+v %+v %v %v", observed, observedRun, found, err)
	}
	for _, field := range []string{"area", "revision", "execution", "phase", "primary", "hash"} {
		changed := *start.Content.Execution.ExpectedTarget
		switch field {
		case "area":
			changed.FeatureAreaID = "other-area"
		case "revision":
			changed.TaskDefinitionRevisionID = original.CurrentDefinitionRevisionID
		case "execution":
			changed.ExecutionID = observed.ID
		case "phase":
			changed.Phase = task.PhaseVerificationReady
		case "primary":
			changed.PrimaryMemberID = "other-primary"
		case "hash":
			changed.ApprovedWorksetHash = "different-hash"
		}
		if _, _, found, readErr := store.ReadTaskExecutionReservationCommand(f.ctx, f.session, f.task, "START_EXECUTION", committed.Steps[0].RequestID, changed); found || readErr == nil {
			t.Fatalf("original admission accepted changed %s expectation: found=%v err=%v", field, found, readErr)
		}
	}
	expected, err = f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil || expected.ExecutionID != observed.ID {
		t.Fatalf("refresh admitted target: %+v %v", expected, err)
	}
	launch := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "no-run-revision-launch", ExpectedTarget: expected, RuntimeConfig: newRuntime, Backend: selected.Input()}
	fault := `CREATE TRIGGER no_run_launch_fault BEFORE UPDATE OF state ON runtime_handoffs WHEN NEW.state='PREPARING' BEGIN SELECT RAISE(ABORT,'injected explicit launch failure'); END`
	if beforeRun {
		fault = `CREATE TRIGGER no_run_launch_fault BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected explicit launch failure'); END`
	}
	if _, err = f.plane.DB.SQL().Exec(fault); err != nil {
		t.Fatal(err)
	}
	failed, err := f.facade.StartApprovedTaskExecution(f.ctx, launch)
	if err == nil || failed.Proposal.ID != observed.ID || beforeRun && failed.Run.ID != "" || !beforeRun && failed.Run.ID == "" {
		t.Fatalf("explicit launch fault result=%+v err=%v", failed, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER no_run_launch_fault`); err != nil {
		t.Fatal(err)
	}
	if beforeRun {
		owner, readErr := f.facade.GetTask(f.ctx, f.task)
		if readErr != nil || owner.Phase != task.PhaseImplementationReady || owner.CurrentExecutionID != observed.ID || failed.Proposal.Status != executionproposal.StatusAdmitted {
			t.Fatalf("before-Run failure did not retain READY/ADMITTED: %+v %+v %v", owner, failed, readErr)
		}
	}
	confirmedAfterFailure, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: start.IntentID, Version: start.Version, Purpose: start.Purpose, ContentHash: start.ContentHash})
	if err != nil || confirmedAfterFailure.State != "COMPLETED" || !reflect.DeepEqual(confirmedAfterFailure.Steps, committed.Steps) {
		t.Fatalf("launch failure altered completed confirmation: %+v %v", confirmedAfterFailure, err)
	}
	before := definitionRuntimeSnapshot(t, f, true)
	changed := launch
	changed.ExpectedTarget = *start.Content.Execution.ExpectedTarget
	if _, err = f.facade.StartApprovedTaskExecution(f.ctx, changed); err == nil {
		t.Fatal("launch request accepted the old predecessor target")
	}
	if after := definitionRuntimeSnapshot(t, f, true); after != before {
		t.Fatalf("rejected launch changed facts: %s", definitionRuntimeSnapshotDiff(t, f, before, after))
	}
	resumed, err := f.facade.StartApprovedTaskExecution(f.ctx, launch)
	if err != nil || resumed.Proposal.ID != observed.ID || resumed.Run.ID == "" || !resumed.Handoff.Accepted() || !beforeRun && resumed.Run.ID != failed.Run.ID {
		t.Fatalf("original launch recovery: %+v %v", resumed, err)
	}
	var executions, implementationRuns, commands int
	if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM execution_proposals WHERE task_id=?),(SELECT count(*) FROM runs WHERE execution_proposal_id=? AND purpose='IMPLEMENTATION'),(SELECT count(*) FROM ai_admitted_execution_start_requests WHERE task_id=?)`, f.task, observed.ID, f.task).Scan(&executions, &implementationRuns, &commands); err != nil || executions != 2 || implementationRuns != 1 || commands != 1 {
		t.Fatalf("duplicate launch facts: executions/runs/requests=%d/%d/%d err=%v", executions, implementationRuns, commands, err)
	}
	after, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, pending.ID)
	if err != nil || !reflect.DeepEqual(predecessor, after) {
		t.Fatalf("old predecessor changed: %+v %v", after, err)
	}
	newCandidate, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, committed.Steps[0].ExecutionID)
	if err != nil || newCandidate.RuntimeConfig != newRuntime || newCandidate.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || newCandidate.ReviewApprovalID != current.ReviewApprovalID {
		t.Fatalf("revised candidate selection: %+v %v", newCandidate, err)
	}
	replay, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || replay.State != "COMPLETED" || !reflect.DeepEqual(replay.Steps, committed.Steps) {
		t.Fatalf("explicit START replay: %+v %v", replay, err)
	}
}
