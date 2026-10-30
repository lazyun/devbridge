package controlapi_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func definitionAdoptionStartFixture(t *testing.T) (*upperLifecycleFixture, c.Intent, c.CommitInput, controlapi.ApprovedTaskStartInput) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	installProtocolFixturePlatform(t, f)
	f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
	_, approve := prepareDefinitionFormalApproval(t, f, "admitted-start-definition")
	approved, err := f.facade.CommitConfirmation(f.ctx, approve)
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("definition approval: %+v %v", approved, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, options := definitionOnlyStart(t, f, current)
	runtime := differentDefinitionRuntime(t, options, codexruntime.NextRunRuntimeConfig{})
	backend := options.BackendCandidates[0].Input()
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "admitted-start-confirm", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &backend}}
	return f, prepared, commit, controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "admitted-start-launch", ExpectedTarget: *prepared.Content.Execution.ExpectedTarget, RuntimeConfig: runtime, Backend: backend}
}

func completedAdoptionStartFixture(t *testing.T) (*upperLifecycleFixture, c.Intent, c.CommitInput, controlapi.ApprovedTaskStartInput) {
	t.Helper()
	f, _, commit, input := definitionAdoptionStartFixture(t)
	confirmed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || confirmed.State != "COMPLETED" || len(confirmed.Steps) != 1 || confirmed.Steps[0].Effect != "PREPARATION_READY" || confirmed.Steps[0].ExecutionID == "" || confirmed.Steps[0].RunID != "" {
		t.Fatalf("first confirmation: %+v %v", confirmed, err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil || expected.ExecutionID != confirmed.Steps[0].ExecutionID {
		t.Fatalf("current admission: %+v %v", expected, err)
	}
	var runs int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, expected.ExecutionID).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("confirmation created runtime work: %d %v", runs, err)
	}
	input.ExpectedTarget = expected
	return f, confirmed, commit, input
}

func TestApprovedTaskStartComposesFreshAdoption(t *testing.T) {
	f, proposal, _, input := definitionAdoptionStartFixture(t)
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(proposal)); err != nil {
		t.Fatal(err)
	}
	started, err := f.facade.StartApprovedTaskExecution(f.ctx, input)
	if err != nil || started.Proposal.ID == "" || started.Run.ID == "" || !started.Handoff.Accepted() {
		t.Fatalf("fresh explicit start: %+v %v", started, err)
	}
	key := c.ValueHash([]string{"devbridge.approved-task-start.v1", input.AISessionID, input.RequestID})
	confirmed, err := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(input))
	if err != nil || confirmed.State != "COMPLETED" || len(confirmed.Steps) != 1 || confirmed.Steps[0].Effect != "PREPARATION_READY" || confirmed.Steps[0].ExecutionID != started.Proposal.ID || confirmed.Steps[0].RunID != "" {
		t.Fatalf("fresh composed admission: %+v %v", confirmed, err)
	}
	var consumers int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM ai_admitted_execution_start_requests`).Scan(&consumers); err != nil || consumers != 0 {
		t.Fatalf("fresh confirmation was confused with a prior admission: %d %v", consumers, err)
	}
}

func TestApprovedTaskStartAuthorizesOriginalFormalAdmissionAfterRestart(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	receipt, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	commit := receipt.Archive.Commit
	commit.BackendSelection, commit.OriginalRequest, commit.SubmittedHash = nil, nil, ""
	if confirmed, e := f.facade.CommitConfirmation(f.ctx, commit); e != nil || confirmed.State != "COMPLETED" {
		t.Fatalf("original formal confirmation: %+v %v", confirmed, e)
	}
	// PREPARATION_READY alone cannot authorize a Run, including after restart.
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	f.plane.Retirement.Close()
	store := sqlite.NewExecutionProposalStore(f.plane.DB)
	if _, _, err = store.PrepareAdmittedExecution(f.ctx, receipt.T2.ExecutionID, time.Now().UTC()); err == nil {
		t.Fatal("formal approval alone authorized a Run")
	}
	var effects, attempts int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_start_effects WHERE execution_id=?`, receipt.T2.ExecutionID).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("approval created start effects: %d %v", effects, err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	in := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "formal-admission-explicit-consumer", ExpectedTarget: expected, RuntimeConfig: receipt.Archive.Runtime, Backend: receipt.Archive.Backend.Input()}
	before := formalExecutionCounts(t, f)
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER formal_admission_run_fault BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'stop after explicit start authority'); END`); err != nil {
		t.Fatal(err)
	}
	started, err := f.facade.StartApprovedTaskExecution(f.ctx, in)
	if err == nil || started.Proposal.ID != receipt.T2.ExecutionID || started.Run.ID != "" {
		t.Fatalf("explicit start before Run fault: %+v %v", started, err)
	}
	key := c.ValueHash([]string{"devbridge.approved-task-start.v1", in.AISessionID, in.RequestID})
	intent, err := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(in))
	if err != nil || intent.State != "COMPLETED" || len(intent.Steps) != 1 || intent.Steps[0].Effect != "PREPARATION_READY" {
		t.Fatalf("exact composed confirmation: %+v %v", intent, err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_start_effects WHERE execution_id=? AND request_id=? AND actor_id=? AND effect='START_AUTHORIZED' AND expected_phase='IMPLEMENTATION_READY'`, receipt.T2.ExecutionID, intent.Steps[0].RequestID, f.session).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("explicit consumer did not register its exact start: %d %v", effects, err)
	}
	after := formalExecutionCounts(t, f)
	for _, table := range []string{"execution_proposals", "execution_proposal_admissions", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_selection_bindings", "runs"} {
		if after[table] != before[table] {
			t.Fatalf("explicit consumer replaced original %s", table)
		}
	}
	// A response lost between the explicit effect and Run creation reuses
	// that same command after restart, without selecting another admission.
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	f.plane.Retirement.Close()
	store = sqlite.NewExecutionProposalStore(f.plane.DB)
	started, err = f.facade.StartApprovedTaskExecution(f.ctx, in)
	if err == nil || started.Proposal.ID != receipt.T2.ExecutionID || started.Run.ID != "" {
		t.Fatalf("cold explicit start before Run fault: %+v %v", started, err)
	}
	changed := in
	changed.RuntimeConfig.Model += "-changed"
	if _, err = f.facade.StartApprovedTaskExecution(f.ctx, changed); err == nil {
		t.Fatal("same explicit request accepted a changed runtime")
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_start_effects WHERE execution_id=?`, receipt.T2.ExecutionID).Scan(&effects); err != nil || effects != 1 {
		t.Fatalf("cold replay duplicated explicit start: %d %v", effects, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER formal_admission_run_fault`); err != nil {
		t.Fatal(err)
	}
	proposal, reserved, err := store.PrepareAdmittedExecution(f.ctx, receipt.T2.ExecutionID, time.Now().UTC())
	if err != nil || proposal.ID != receipt.T2.ExecutionID || reserved.ID == "" || reserved.Purpose != "IMPLEMENTATION" {
		t.Fatalf("original admission after explicit authorization: %+v %+v %v", proposal, reserved, err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id=? AND request_id=? AND payload_hash=? AND run_id=?`, receipt.Plan.ID, receipt.Plan.PreparationRequestID, receipt.Plan.PreparationPayloadHash, reserved.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("explicit start did not consume the original plan: %d %v", attempts, err)
	}
}

func TestApprovedTaskStartDoesNotBorrowLaterLockedAdoptions(t *testing.T) {
	f, prior, id, wire := lockedFormalAdoptionFixture(t)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	approval := prior.Archive.Commit
	approval.BackendSelection, approval.OriginalRequest, approval.SubmittedHash = nil, nil, ""
	if completed, err := f.facade.CommitConfirmation(f.ctx, approval); err != nil || completed.State != "COMPLETED" {
		t.Fatalf("materialize original runtime-authorized binding: %+v %v", completed, err)
	}
	if _, err := store.CommitAdoption(f.ctx, id, prior.Archive.PlatformReference); err != nil {
		t.Fatal(err)
	}
	original, err := f.facade.CommitConfirmation(f.ctx, wire)
	if err != nil || original.State != "COMPLETED" {
		t.Fatalf("first locked confirmation: %+v %v", original, err)
	}
	confirmAgain := func(request string) {
		t.Helper()
		input := original.Content
		input.RequestID, input.SubmittedHash = request, ""
		prepared, err := f.facade.PrepareConfirmation(f.ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		commit := wire
		commit.IntentID, commit.Version, commit.ContentHash, commit.RequestID = prepared.IntentID, prepared.Version, prepared.ContentHash, request+"-commit"
		if confirmed, err := f.facade.CommitConfirmation(f.ctx, commit); err != nil || confirmed.State != "COMPLETED" || confirmed.Steps[0].ExecutionID != prior.T2.ExecutionID {
			t.Fatalf("additional locked confirmation: %+v %v", confirmed, err)
		}
	}
	confirmAgain("another-locked-adoption")
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	in := fe.AdoptionStartRequest{AISessionID: f.session, TaskID: f.task, RequestID: "multiple-adoptions-consumer", ExpectedTarget: expected, RuntimeConfig: prior.Archive.Runtime, Backend: prior.Archive.Backend.Input()}
	bound, found, err := store.BindAdoptionStart(f.ctx, in)
	if err != nil || found || bound.IntentID != "" {
		t.Fatalf("borrowed a later locked archive: %+v %v %v", bound, found, err)
	}
	confirmAgain("later-locked-adoption")
	replayed, found, err := store.ReplayAdoptionStart(f.ctx, in)
	if err != nil || found || replayed.IntentID != "" {
		t.Fatalf("later confirmation became the admission source: %+v %v %v", replayed, found, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER locked_adoption_run_fault BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'stop composed start after original authority'); END`); err != nil {
		t.Fatal(err)
	}
	input := controlapi.ApprovedTaskStartInput(in)
	started, err := f.facade.StartApprovedTaskExecution(f.ctx, input)
	if err == nil || started.Proposal.ID != expected.ExecutionID || started.Run.ID != "" {
		t.Fatalf("original runtime-authorized compose path: %+v %v", started, err)
	}
	key := c.ValueHash([]string{"devbridge.approved-task-start.v1", input.AISessionID, input.RequestID})
	composed, err := f.plane.Confirmations.ReplayPrepare(f.ctx, f.session, "approved_task_start_prepare_"+key, c.ValueHash(input))
	if err != nil || composed.State != "COMPLETED" || composed.Steps[0].ExecutionID != expected.ExecutionID || composed.Steps[0].Effect != "PREPARATION_READY" {
		t.Fatalf("runtime-authorized start did not compose its own confirmation: %+v %v", composed, err)
	}
}

func TestApprovedTaskStartConsumesCompletedAdoption(t *testing.T) {
	for _, beforeRunFailure := range []bool{false, true} {
		name := "response-loss-cold-replay"
		if beforeRunFailure {
			name = "before-run-failure-cold-retry"
		}
		t.Run(name, func(t *testing.T) {
			f, confirmed, commit, input := completedAdoptionStartFixture(t)
			before := formalExecutionCounts(t, f)
			changed := input
			changed.RuntimeConfig.Model = "different-model"
			if out, err := f.facade.StartApprovedTaskExecution(f.ctx, changed); err == nil || out.Proposal.ID != "" || out.Run.ID != "" {
				t.Fatalf("new consumer changed locked runtime: %+v %v", out, err)
			}
			if beforeRunFailure {
				if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER admitted_start_run_fault BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'admitted start Run fault'); END`); err != nil {
					t.Fatal(err)
				}
			}
			started, err := f.facade.StartApprovedTaskExecution(f.ctx, input)
			if beforeRunFailure {
				if err == nil || started.Proposal.ID != confirmed.Steps[0].ExecutionID || started.Run.ID != "" {
					t.Fatalf("before-Run failure: %+v %v", started, err)
				}
				if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER admitted_start_run_fault`); err != nil {
					t.Fatal(err)
				}
			} else if err != nil || started.Proposal.ID != confirmed.Steps[0].ExecutionID || started.Run.ID == "" || !started.Handoff.Accepted() {
				t.Fatalf("launch after completed public confirmation: %+v %v", started, err)
			}
			// The consumer owns only its submitted payload and original intent.
			// Neither changed input nor an UPDATE may replace that immutable link.
			for _, field := range []string{"runtime", "backend", "target", "task"} {
				changed := input
				switch field {
				case "runtime":
					changed.RuntimeConfig.ReasoningEffort = "different-effort"
				case "backend":
					changed.Backend.ConfigFingerprint = strings.Repeat("a", 64)
				case "target":
					changed.ExpectedTarget.ExecutionID = "later-execution"
				case "task":
					changed.TaskID = "later-task"
				}
				if out, err := f.facade.StartApprovedTaskExecution(f.ctx, changed); err == nil || out.Proposal.ID != "" || out.Run.ID != "" || !strings.Contains(err.Error(), "CONFIRMATION_REQUEST_CONFLICT") {
					t.Fatalf("request accepted changed %s: %+v %v", field, out, err)
				}
			}
			if _, err = f.plane.DB.SQL().Exec(`UPDATE ai_admitted_execution_start_requests SET request_id=request_id`); err == nil {
				t.Fatal("consumer request was mutable")
			}
			reopenDirectDeliveryFixture(t, f)
			f.plane.Retirement.Close()
			installProtocolFixturePlatform(t, f)
			f.plane.Coordinator.Codex = preparationOnlyCodexFactory{}
			replayed, err := f.facade.StartApprovedTaskExecution(f.ctx, input)
			if err != nil || replayed.Proposal.ID != confirmed.Steps[0].ExecutionID || replayed.Run.ID == "" || !beforeRunFailure && replayed.Run.ID != started.Run.ID || replayed.Receipt == nil || replayed.Receipt.OriginalRunID != replayed.Run.ID {
				t.Fatalf("cold original consumer: %+v %v", replayed, err)
			}
			after := formalExecutionCounts(t, f)
			for table, count := range before {
				if table == "runtime_selection_bindings" {
					// The original confirmation seals its source; the explicit
					// launch installs that exact source's first revision binding.
					count++
				}
				if table != "runs" && after[table] != count {
					t.Fatalf("consumer minted %s: before=%d after=%d", table, count, after[table])
				}
			}
			var exactBinding int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_selection_bindings b JOIN runtime_selection_sources s ON s.source_id=b.source_id WHERE s.ai_session_id=? AND s.request_id=? AND s.adoption_request_id=? AND b.task_id=? AND b.revision_id=?`, f.session, commit.RequestID, commit.RequestID, f.task, input.ExpectedTarget.TaskDefinitionRevisionID).Scan(&exactBinding); err != nil || exactBinding != 1 {
				t.Fatalf("launch did not bind the original confirmed source: count=%d err=%v", exactBinding, err)
			}
			var requests, implementations int
			if err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM ai_admitted_execution_start_requests),(SELECT count(*) FROM runs WHERE execution_proposal_id=? AND purpose='IMPLEMENTATION')`, input.ExpectedTarget.ExecutionID).Scan(&requests, &implementations); err != nil || requests != 1 || implementations != 1 {
				t.Fatalf("consumer request/Run=%d/%d err=%v", requests, implementations, err)
			}
			read, err := f.facade.CommitConfirmation(f.ctx, commit)
			if err != nil || read.State != "COMPLETED" || !reflect.DeepEqual(read.Steps, confirmed.Steps) {
				t.Fatalf("launch rewrote confirmation completion: %+v %v", read, err)
			}
		})
	}
}
