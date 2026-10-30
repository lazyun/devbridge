package execution_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

type scalarCountingEvidence struct {
	exec.EvidenceCollector
	captures int
	fail     bool
}

func (e *scalarCountingEvidence) CaptureSnapshot(ctx context.Context, root string) (string, error) {
	e.captures++
	if e.fail {
		return "", errors.New("lost workspace preparation response")
	}
	return e.EvidenceCollector.CaptureSnapshot(ctx, root)
}

type scalarChangedHead struct{ exec.EvidenceCollector }

func (e scalarChangedHead) CollectRun(ctx context.Context, root, base, start, end string) (evidence.GitEvidence, error) {
	v, err := e.EvidenceCollector.CollectRun(ctx, root, base, start, end)
	v.HeadCommit = strings.Repeat("b", 40)
	return v, err
}

func TestScalarProductionPreparationReplayDoesNotRecaptureOrDuplicate(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	collector := &scalarCountingEvidence{EvidenceCollector: f.coordinator.Evidence}
	f.coordinator.Evidence = collector
	in := exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope}
	first, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if collector.captures != 1 || first.Run.ID != replay.Run.ID || first.Run.RuntimeWorkset.ContentHash != replay.Run.RuntimeWorkset.ContentHash {
		t.Fatalf("replay recaptured or replaced authority: captures=%d", collector.captures)
	}
	for _, table := range []string{"runtime_preparation_attempts", "runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities"} {
		var n int
		if err = f.db.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, first.Run.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s=%d error=%v", table, n, err)
		}
	}
}

func TestScalarProductionPreparationRecoversBindingFailureFromObservations(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	collector := &scalarCountingEvidence{EvidenceCollector: f.coordinator.Evidence}
	f.coordinator.Evidence = collector
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER scalar_binding_fault BEFORE INSERT ON runtime_start_authorities BEGIN SELECT RAISE(ABORT,'injected scalar binding fault'); END`); err != nil {
		t.Fatal(err)
	}
	in := exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope}
	if _, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in); err == nil {
		t.Fatal("binding fault accepted")
	}
	var observations, bindings, authorities int
	for table, ptr := range map[string]*int{"runtime_workspace_observations": &observations, "runtime_workset_bindings": &bindings, "runtime_start_authorities": &authorities} {
		if err := f.db.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, f.implementationRun.ID).Scan(ptr); err != nil {
			t.Fatal(err)
		}
	}
	if observations != 1 || bindings != 0 || authorities != 0 {
		t.Fatalf("partial binding commit: %d/%d/%d", observations, bindings, authorities)
	}
	if _, err := f.db.SQL().Exec(`DROP TRIGGER scalar_binding_fault`); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if collector.captures != 1 {
		t.Fatal("binding retry repeated external preparation")
	}
	if err = sqlite.NewRunStore(f.db).ValidatePreparedStart(f.ctx, prepared.Run.ID); err != nil {
		t.Fatal(err)
	}
}

func TestScalarProductionPreparationUnknownDoesNotRepeatExternalOperation(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	collector := &scalarCountingEvidence{EvidenceCollector: f.coordinator.Evidence, fail: true}
	f.coordinator.Evidence = collector
	in := exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope}
	if _, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in); err == nil {
		t.Fatal("lost response accepted")
	}
	collector.fail = false
	if _, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, in); err == nil || !strings.Contains(err.Error(), "RUNTIME_WORKSPACE_PREPARATION_OUTCOME_UNKNOWN") {
		t.Fatalf("unknown replay=%v", err)
	}
	if collector.captures != 1 {
		t.Fatal("unknown external operation repeated")
	}
	assertScalarPreparedFacts(t, f)
}

func TestScalarProductionPreparationRejectsFreshHeadDrift(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	f.coordinator.Evidence = scalarChangedHead{f.coordinator.Evidence}
	if _, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope}); err == nil {
		t.Fatal("wrong fresh HEAD accepted")
	}
	assertScalarPreparedFacts(t, f)
}

// Scope counts to the implementation owner: the prerequisite Review has its
// own Run and completed Turn and must not be confused with execution authority.
func assertScalarPreparedFacts(t *testing.T, f *authorizedPlanPanicFixture) {
	t.Helper()
	db := f.db.SQL()
	id := f.proposalValue.ID
	var plan, authority, admissionRequest, admissionPayload string
	if err := db.QueryRow(`SELECT runtime_scope_plan_id,runtime_preparation_authority_id,admission_request_id,payload_sha256 FROM execution_proposal_admissions WHERE proposal_id=?`, id).Scan(&plan, &authority, &admissionRequest, &admissionPayload); err != nil {
		t.Fatal(err)
	}
	t.Logf("execution=%s plan=%s preparationAuthority=%s run=%s admissionRequest=%s payload=%s", id, plan, authority, f.implementationRun.ID, admissionRequest, admissionPayload)
	for _, c := range []struct {
		name, query, key string
		want             int
	}{
		{"admission", `SELECT count(*) FROM execution_proposal_admissions WHERE proposal_id=?`, id, 1},
		{"plan", `SELECT count(*) FROM runtime_scope_plans WHERE plan_id=?`, plan, 1},
		{"authority", `SELECT count(*) FROM runtime_preparation_authorities WHERE plan_id=?`, plan, 1},
		{"attempt", `SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id=?`, plan, 1},
		{"receipt", `SELECT count(*) FROM runtime_preparation_receipts WHERE plan_id=?`, plan, 1},
		{"run", `SELECT count(*) FROM runs WHERE execution_proposal_id=?`, id, 1},
		{"observation", `SELECT count(*) FROM runtime_workspace_observations WHERE plan_id=?`, plan, 0},
		{"binding", `SELECT count(*) FROM runtime_workset_bindings WHERE plan_id=?`, plan, 0},
		{"startAuthority", `SELECT count(*) FROM runtime_start_authorities WHERE plan_id=?`, plan, 0},
		{"turn", `SELECT count(*) FROM runs WHERE execution_proposal_id=? AND COALESCE(turn_id,'')<>''`, id, 0},
	} {
		var n int
		if err := db.QueryRow(c.query, c.key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s=%d", c.name, n)
		if n != c.want {
			t.Errorf("%s=%d, want %d", c.name, n, c.want)
		}
	}
	var prepRequest, prepPayload string
	if err := db.QueryRow(`SELECT request_id,payload_hash FROM runtime_preparation_attempts WHERE plan_id=?`, plan).Scan(&prepRequest, &prepPayload); err != nil {
		t.Fatal(err)
	}
	t.Logf("preparationRequest=%s preparationPayload=%s bindingRequest=<absent> startRequest=<absent>", prepRequest, prepPayload)
	if f.implementationRun.ID == "" || f.proposalValue.Status != executionproposal.StatusConsumed || f.proposalValue.RunID != f.implementationRun.ID {
		t.Fatal("fixture did not preserve the canonical consumed Run")
	}
}

func TestAuthorizedPlanHelperDoesNotRepeatPreparation(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	assertScalarPreparedFacts(t, f)
}

func TestAuthorizedPlanPanicFixtureUsesExistingRun(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	stored, err := f.runs.Get(f.ctx, f.implementationRun.ID)
	if err != nil || stored.ID != f.proposalValue.RunID || stored.AgentExecutionID != f.proposalValue.ID {
		t.Fatalf("canonical fixture Run=%+v error=%v", stored, err)
	}
	assertScalarPreparedFacts(t, f)
}

func TestScalarProductionPreparationCreatesStartAuthority(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	prepared, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlite.NewRunStore(f.db).ValidatePreparedStart(f.ctx, prepared.Run.ID); err != nil {
		t.Fatalf("production preparation omitted start authority: %v", err)
	}
	for _, table := range []string{"runtime_workspace_observations", "runtime_workset_bindings", "runtime_start_authorities"} {
		var count int
		if err := f.db.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, prepared.Run.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s count=%d error=%v", table, count, err)
		}
	}
	if prepared.Run.RuntimeWorkset == nil || prepared.Run.RuntimeWorkset.Validate() != nil {
		t.Fatal("missing canonical Workset handoff")
	}
	select {
	case <-f.factory.created:
		t.Fatal("preparation dispatched backend")
	default:
	}
}

// This regression records the fail-closed boundary, not successful runtime
// preparation. A Run-only fixture cannot grant permission to dispatch.
func TestScalarPreparedFixtureCannotStartWithoutBinding(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	// Deliberately stop at the Run-only boundary. Do not require the production
	// preparation method to omit binding; it may eventually complete that stage.
	prepared := exec.PreparedRun{Run: f.implementationRun}
	assertScalarPreparedFacts(t, f)
	var authority string
	err := f.db.SQL().QueryRow(`SELECT a.authority_id FROM runs r JOIN runtime_preparation_attempts t ON t.run_id=r.id JOIN runtime_scope_plans p ON p.plan_id=t.plan_id JOIN runtime_workset_bindings b ON b.plan_id=p.plan_id AND b.run_id=r.id JOIN runtime_start_authorities a ON a.plan_id=p.plan_id AND a.run_id=r.id AND a.authority_id=b.start_authority_id WHERE r.id=?`, prepared.Run.ID).Scan(&authority)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("start-authority join error=%v, want sql.ErrNoRows", err)
	}
	t.Logf("start-authority SELECT error=%v; no INSERT or UNIQUE/CHECK/FK/trigger failure", err)
	_, err = f.coordinator.ExecuteRun(f.ctx, prepared, nil)
	var infrastructure *exec.InfrastructureError
	if !errors.As(err, &infrastructure) || infrastructure.Stage != "RuntimePreparation start authority" || !errors.Is(err, sqlite.ErrConflict) {
		t.Fatalf("missing start authority error=%v", err)
	}
	select {
	case <-f.factory.created:
		t.Fatal("Run-only fixture dispatched a runtime")
	default:
	}
}

func TestScalarProductionPreparationRejectsCancellation(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	if _, _, err := sqlite.NewExecutionCancellationStore(f.db).Begin(f.ctx, executioncancel.Input{ProjectID: f.taskValue.ProjectID, AISessionID: f.proposalValue.AISessionID, ExecutionID: f.proposalValue.ID, RunID: f.implementationRun.ID, RequestID: "cancel-before-preparation"}, true); err != nil {
		t.Fatal(err)
	}
	collector := &scalarCountingEvidence{EvidenceCollector: f.coordinator.Evidence}
	f.coordinator.Evidence = collector
	_, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
	if !errors.Is(err, sqlite.ErrConflict) || collector.captures != 0 {
		t.Fatalf("revoked preparation: captures=%d error=%v", collector.captures, err)
	}
	assertScalarPreparedFacts(t, f)
}

func TestScalarProductionBindingReplayAndPayloadConflict(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	prepared, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	var plan, request, payload, authority string
	if err = f.db.SQL().QueryRow(`SELECT plan_id,request_id,payload_hash,start_authority_id FROM runtime_preparation_receipts WHERE run_id=? AND outcome='BINDING_COMMITTED'`, prepared.Run.ID).Scan(&plan, &request, &payload, &authority); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewRunStore(f.db)
	if r, err := store.BindPreparedRuntimeWorkset(f.ctx, plan, request, payload, authority, *prepared.Run.RuntimeWorkset, time.Now().UTC()); err != nil || r.ID != prepared.Run.ID {
		t.Fatalf("exact replay=%s %v", r.ID, err)
	}
	for _, change := range []struct{ request, payload, authority string }{{request, strings.Repeat("0", 64), authority}, {"other-binding", payload, "other-authority"}} {
		if _, err = store.BindPreparedRuntimeWorkset(f.ctx, plan, change.request, change.payload, change.authority, *prepared.Run.RuntimeWorkset, time.Now().UTC()); !errors.Is(err, sqlite.ErrConflict) {
			t.Fatalf("conflict accepted: %v", err)
		}
	}
	for _, table := range []string{"runtime_workset_bindings", "runtime_start_authorities"} {
		var n int
		if err = f.db.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, prepared.Run.ID).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s=%d %v", table, n, err)
		}
	}
}

func TestScalarProductionDispatchRejectsReplacedRoot(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	prepared, err := f.coordinator.PrepareReservedRun(f.ctx, f.implementationRun.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	root := prepared.Run.RuntimeWorkset.Members[0].CanonicalRoot
	if err = os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = f.coordinator.ExecuteRun(f.ctx, prepared, nil); err == nil {
		t.Fatal("replaced physical root dispatched")
	}
	select {
	case <-f.factory.created:
		t.Fatal("replaced root reached backend")
	default:
	}
}
