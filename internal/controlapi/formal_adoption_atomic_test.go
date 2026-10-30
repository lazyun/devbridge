package controlapi_test

import (
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func lockedFormalAdoptionFixture(t *testing.T) (*upperLifecycleFixture, fe.Receipt, fe.ReadIdentity, c.CommitInput) {
	t.Helper()
	f, priorID, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	prior, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, priorID, ref)
	if err != nil {
		t.Fatal(err)
	}
	parent := prior.Archive.Commit
	parent.BackendSelection, parent.OriginalRequest, parent.SubmittedHash = nil, nil, ""
	if out, e := f.facade.CommitConfirmation(f.ctx, parent); e != nil || out.State != "COMPLETED" {
		t.Fatalf("finish exact parent before next proposal: %+v %v", out, e)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "locked-atomic-prepare", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: expected.ExecutionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Summary: "Confirm exact prior admission", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	backend, runtime := prior.Archive.Backend, prior.Archive.Runtime
	wire := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "locked-atomic-commit", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
	commit := wire
	commit.SubmittedHash = c.ValueHash(wire)
	commit.BackendSelection = &backend
	step := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "START_IMPLEMENTATION"})[:32], State: "UNKNOWN"}
	if _, _, err = sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step}); err != nil {
		t.Fatal(err)
	}
	id, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).FormalExecutionIdentity(f.ctx, f.session, prepared.IntentID, wire.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	return f, prior, id, wire
}

func TestFormalLockedConfirmationAtomicConcurrentColdReplay(t *testing.T) {
	f, prior, id, wire := lockedFormalAdoptionFixture(t)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	for _, fault := range []struct{ table, operation, condition string }{{"formal_adoption_receipts", "INSERT", ""}, {"runtime_selection_sources", "INSERT", ""}, {"confirmation_intents", "UPDATE OF intent_json", " WHEN NEW.intent_id='" + id.IntentID + "' AND json_extract(NEW.intent_json,'$.state')='COMPLETED'"}} {
		statement := `CREATE TRIGGER locked_atomic_fault BEFORE ` + fault.operation + ` ON ` + fault.table + fault.condition + ` BEGIN SELECT RAISE(ABORT,'locked-atomic-fault'); END`
		if _, err := f.plane.DB.SQL().Exec(statement); err != nil {
			t.Fatal(err)
		}
		if _, err := store.CommitAdoption(f.ctx, id, rs.PlatformReference{}); err == nil || !strings.Contains(err.Error(), "locked-atomic-fault") {
			t.Fatalf("%s fault: %v", fault.table, err)
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "locked atomic rollback at "+fault.table)
		if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER locked_atomic_fault`); err != nil {
			t.Fatal(err)
		}
	}
	oldCounts := formalExecutionCounts(t, f)
	var wg sync.WaitGroup
	receipts := make([]fe.AdoptionReceipt, 2)
	failures := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipts[i], failures[i] = store.CommitAdoption(f.ctx, id, rs.PlatformReference{})
		}(i)
	}
	wg.Wait()
	for _, err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	receipt := receipts[0]
	if !reflect.DeepEqual(receipt, receipts[1]) || receipt.Archive.PriorRuntimeApproval == nil || !reflect.DeepEqual(*receipt.Archive.PriorRuntimeApproval, prior) || receipt.Archive.ExecutionID != prior.T2.ExecutionID || !reflect.DeepEqual(receipt.Archive.Plan, prior.Plan) {
		t.Fatal("locked confirmation did not preserve exact prior admission")
	}
	afterCounts := formalExecutionCounts(t, f)
	for _, table := range []string{"execution_proposals", "execution_proposal_admissions", "runtime_scope_plans", "runtime_preparation_authorities", "execution_start_effects", "runs", "runtime_selection_bindings", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts"} {
		if oldCounts[table] != afterCounts[table] {
			t.Fatalf("confirmation changed %s: %d -> %d", table, oldCounts[table], afterCounts[table])
		}
	}
	source, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadSource(f.ctx, receipt.SourceID())
	if err != nil {
		t.Fatal(err)
	}
	if source.Entry != rs.EntryRuntimeAuthorized || source.OriginalAction != "START_APPROVED" || source.NormalizedIssuanceClass != "RUNTIME_AUTHORIZED_EXECUTION_CONFIRMATION" || source.SourceReceiptIdentity == prior.ReceiptIdentity || source.PriorApprovalReceiptIdentity != prior.ReceiptIdentity {
		t.Fatalf("wrong independent locked source: %+v", source)
	}
	platform := sqlite.NewRuntimePlatformStore(f.plane.DB)
	ref := prior.Archive.PlatformReference
	if _, err = platform.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: ref.QualificationID, Generation: ref.QualificationGeneration, RequestID: "locked-invalidate-after-complete", Reason: "test replay precedes current qualification", ExpectedLineageVersion: ref.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	before = definitionFormalSnapshot(t, f, tables)
	replay, err := f.facade.CommitConfirmation(f.ctx, wire)
	if err != nil || replay.State != "COMPLETED" || len(replay.Steps) != 1 || replay.Steps[0].RunID != "" || replay.Steps[0].Effect != "PREPARATION_READY" || replay.Steps[0].ExecutionID != prior.T2.ExecutionID {
		t.Fatalf("cold locked replay: %+v %v", replay, err)
	}
	got, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitAdoption(f.ctx, id, rs.PlatformReference{})
	if err != nil || !reflect.DeepEqual(got, receipt) {
		t.Fatalf("cold immutable archive: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "cold locked replay before invalid qualification")
	changed := id
	changed.RequestID = "different-current-command"
	if _, err = sqlite.NewWorkflowProgressionStore(f.plane.DB).ReadAdoption(f.ctx, changed); !errors.Is(err, fe.ErrMissing) {
		t.Fatalf("different request borrowed receipt: %v", err)
	}
}

func TestFormalLockedConfirmationRejectsCommittedPrefix(t *testing.T) {
	f, _, id, wire := lockedFormalAdoptionFixture(t)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	receipt, err := store.CommitAdoption(f.ctx, id, rs.PlatformReference{})
	if err != nil {
		t.Fatal(err)
	}
	// Model an old interrupted writer. Only tests can remove append-only rows;
	// normal producer failures are covered above and cannot leave this prefix.
	for _, table := range []string{"runtime_selection_sources", "formal_adoption_receipts"} {
		var triggerName, triggerSQL string
		if err = f.plane.DB.SQL().QueryRow(`SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? AND sql LIKE '%BEFORE DELETE%'`, table).Scan(&triggerName, &triggerSQL); err != nil {
			t.Fatal(err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER ` + triggerName); err != nil {
			t.Fatal(err)
		}
		if _, err = f.plane.DB.SQL().Exec(`DELETE FROM `+table+` WHERE ai_session_id=? AND request_id=?`, id.AISessionID, id.RequestID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.plane.DB.SQL().Exec(triggerSQL); err != nil {
			t.Fatal(err)
		}
	}
	reopenDirectDeliveryFixture(t, f)
	f.plane.Retirement.Close()
	tables := definitionFormalEveryTable(t, f)
	before := definitionFormalSnapshot(t, f, tables)
	if _, err = sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitAdoption(f.ctx, id, receipt.Archive.PlatformReference); !errors.Is(err, fe.ErrCorrupt) {
		t.Fatalf("prefix backfilled final receipt: %v", err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, wire); err == nil {
		t.Fatal("prefix became successful current confirmation")
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "reject historical prefix without mutation")
}
