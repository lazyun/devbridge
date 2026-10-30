package controlapi_test

import (
	"strings"
	"testing"

	"errors"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestFormalApprovalBoundPreRunCancellationCreatesExactSuccessor(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	receipt, err := store.CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := selections.MaterializeBinding(f.ctx, rs.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: ref.LineageVersion})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(binding.Values.RuntimeAdapterIdentity, "adapterImplementationDigest") || binding.Values.BackendKind != "CODEX" {
		t.Fatal("qualified source fell back to legacy constants")
	}
	cancellation := sqlite.NewExecutionCancellationStore(f.plane.DB)
	cancel, _, err := cancellation.Begin(f.ctx, executioncancel.Input{AISessionID: f.session, ProjectID: source.Owner.ProjectID, ExecutionID: receipt.T2.ExecutionID, OwnerType: "EXECUTION", OwnerID: receipt.T2.ExecutionID, RequestID: "formal-no-run-cancel", Reason: "cancel admitted execution before any Run"}, false)
	if err != nil || cancel.State != "CANCELED" || cancel.RunID != "" {
		t.Fatalf("cancel %+v %v", cancel, err)
	}
	phase, err := cancellation.ProjectCanceledAdmittedExecutionToSuccessorRequired(f.ctx, f.task, receipt.T2.ExecutionID)
	if err != nil || phase != task.PhaseNoRunSuccessorRequired {
		t.Fatalf("project canceled phase=%s %v", phase, err)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "formal-no-run-successor", ProjectID: source.Owner.ProjectID, TaskID: f.task, PredecessorExecutionID: receipt.T2.ExecutionID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: receipt.T2.ExecutionID, ExpectedDefinitionRevisionID: source.Owner.TaskDefinitionRevisionID, ActorAuthority: f.session}
	in.PayloadSHA256 = rs.HashValue(in)
	successor, err := store.PrepareSuccessorExecution(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if successor.Outcome != wp.NoRunSuccessorCreated || successor.SuccessorExecutionID == receipt.T2.ExecutionID {
		t.Fatal(successor)
	}
	var predecessorHash, verifiedHash, successorHash, kind, adapter, backend string
	err = f.plane.DB.SQL().QueryRow(`SELECT a.predecessor_authority_snapshot_hash,a.verified_current_authority_snapshot_hash,p.authority_snapshot_hash,p.owner_kind,p.runtime_adapter_identity,p.backend_kind FROM no_run_successor_effect_authorities a JOIN runtime_scope_plans p ON p.plan_id=a.runtime_scope_plan_id WHERE a.predecessor_execution_id=?`, receipt.T2.ExecutionID).Scan(&predecessorHash, &verifiedHash, &successorHash, &kind, &adapter, &backend)
	if err != nil || predecessorHash != binding.AuthoritySnapshotHash || verifiedHash != binding.AuthoritySnapshotHash || successorHash != binding.AuthoritySnapshotHash || kind != "EXECUTION" || adapter != binding.Values.RuntimeAdapterIdentity || backend != "CODEX" {
		t.Fatalf("successor snapshot %s/%s/%s kind=%s backend=%s err=%v", predecessorHash, verifiedHash, successorHash, kind, backend, err)
	}
	for _, table := range []string{"runs", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities"} {
		var count int
		query := `SELECT COUNT(*) FROM ` + table + ` WHERE plan_id=?`
		arg := successor.RuntimeScopePlanID
		if table == "runs" {
			query = `SELECT COUNT(*) FROM runs WHERE execution_proposal_id=?`
			arg = successor.SuccessorExecutionID
		}
		if err = f.plane.DB.SQL().QueryRow(query, arg).Scan(&count); err != nil || count != 0 {
			t.Fatalf("successor wrote %s: %d %v", table, count, err)
		}
	}
	scope, _ := aisession.CallerScopeFromContext(f.ctx)
	second, err := f.plane.AIContext.InitializeSession(f.ctx, aiprojectselection.ClientBindingReference{CallerScope: scope}, "no-run-second-actor-init", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.AIContext.BindProject(f.ctx, aiprojectselection.ClientBindingReference{CallerScope: scope}, second.AISessionID, f.project, "no-run-second-actor-bind"); err != nil {
		t.Fatal(err)
	}
	tables := append(formalExecutionTables(), "no_run_successor_command_receipts", "no_run_successor_command_identities", "no_run_successor_effect_authorities", "no_run_successor_eligibilities")
	before := definitionFormalSnapshot(t, f, tables)
	fenced := runtimeepoch.WithFence(f.ctx, "retired-successor-writer", "")
	replay, err := store.PrepareSuccessorExecution(fenced, in)
	if err != nil || replay != successor {
		t.Fatalf("stale epoch original replay %+v %v", replay, err)
	}
	different := in
	different.RequestID = "fresh-ack-under-old-epoch"
	if _, err = store.PrepareSuccessorExecution(fenced, different); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("fresh stale writer: %v", err)
	}
	foreignActor := in
	foreignActor.ActorAuthority = second.AISessionID
	if got, e := store.PrepareSuccessorExecution(fenced, foreignActor); e == nil || !strings.Contains(e.Error(), "REQUEST_IDENTITY_CONFLICT") || got.RequestID != "" {
		t.Fatalf("another authorized actor borrowed original %+v %v", got, e)
	}
	foreignProject := in
	foreignProject.ProjectID = "different-project"
	if got, e := store.PrepareSuccessorExecution(fenced, foreignProject); e == nil || !strings.Contains(e.Error(), "PROJECT_MISMATCH") || got.RequestID != "" {
		t.Fatalf("another project borrowed original %+v %v", got, e)
	}
	changedTarget := in
	changedTarget.ExpectedDefinitionRevisionID = "other-revision"
	if got, e := store.PrepareSuccessorExecution(fenced, changedTarget); e == nil || !strings.Contains(e.Error(), "REQUEST_IDENTITY_CONFLICT") || got.RequestID != "" {
		t.Fatalf("other target borrowed original %+v %v", got, e)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "pure original replay and identity rejection")
	replay, err = store.PrepareSuccessorExecution(f.ctx, in)
	if err != nil || replay != successor {
		t.Fatalf("exact successor replay %+v %v", replay, err)
	}
	// An upgrade-shaped historical receipt has no original identity row. Never
	// backfill it from this caller or the current candidate/Task provenance.
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER no_run_successor_command_identity_durable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DELETE FROM no_run_successor_command_identities WHERE request_id=?`, in.RequestID); err != nil {
		t.Fatal(err)
	}
	historical := definitionFormalSnapshot(t, f, tables)
	if got, e := store.PrepareSuccessorExecution(fenced, in); e == nil || !strings.Contains(e.Error(), "ORIGINAL_COMMAND_IDENTITY_UNAVAILABLE") || got.RequestID != "" {
		t.Fatalf("historical missing actor inferred %+v %v", got, e)
	}
	assertDefinitionFormalSnapshot(t, f, tables, historical, "legacy original identity unavailable")

}
