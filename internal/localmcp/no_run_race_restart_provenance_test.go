package localmcp_test

import (
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type heldPreparation struct{ entered, release chan struct{} }

func newHeldPreparation() *heldPreparation {
	return &heldPreparation{make(chan struct{}), make(chan struct{})}
}

// This wrapper controls entry only, then returns the real canonical result.
func (b *heldPreparation) call(f *integrationFixture, sid string, p executionproposal.Proposal) (run.Run, error) {
	close(b.entered)
	<-b.release
	_, r, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p)
	return r, err
}
func factRows(t *testing.T, facts map[string]any, key string) []map[string]any {
	t.Helper()
	rows, ok := facts[key].([]map[string]any)
	if !ok {
		t.Fatalf("missing facts %s", key)
	}
	return rows
}
func exactExecutionRuns(t *testing.T, facts map[string]any, id string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, r := range factRows(t, facts, "runs") {
		if r["execution_proposal_id"] == id {
			out = append(out, r)
		}
	}
	return out
}
func assertStableCanceledNoRun(t *testing.T, f *integrationFixture, p executionproposal.Proposal, in executioncancel.Input) map[string]any {
	t.Helper()
	facts := restartDurableFacts(t, f, p, in)
	tasks := factRows(t, facts, "task")
	e := factRows(t, facts, "execution")
	c := factRows(t, facts, "cancellations")
	all := factRows(t, facts, "allTaskExecutions")
	if len(all) != 1 || all[0]["id"] != p.ID {
		t.Fatalf("unexpected Task execution proposals: %+v", all)
	}
	eligibility := factRows(t, facts, "eligibilities")
	if len(eligibility) != 1 || eligibility[0]["predecessor_execution_id"] != p.ID || eligibility[0]["task_id"] != p.TaskID || eligibility[0]["consumed_successor_execution_id"] != nil {
		t.Fatalf("unexpected eligibility: %+v", eligibility)
	}
	if len(tasks) != 1 || tasks[0]["status"] != "ACTIVE" || tasks[0]["phase"] != "NO_RUN_SUCCESSOR_REQUIRED" || tasks[0]["current_execution_id"] != p.ID || tasks[0]["current_definition_revision_id"] != p.TaskDefinitionRevisionID {
		t.Fatalf("unstable Task: %+v", tasks)
	}
	if len(e) != 1 || e[0]["outcome"] != "CANCELED" || e[0]["run_id"] != nil {
		t.Fatalf("predecessor: %+v", e)
	}
	if len(c) != 1 || c[0]["state"] != "CANCELED" || c[0]["backend_disposition"] != "NO_BACKEND" || c[0]["interrupt_state"] != "NOT_SENT" || c[0]["run_id"] != nil {
		t.Fatalf("cancellation facts: %+v", c)
	}
	receipt, err := f.plane.ExecutionCancellations.Get(f.ctx, f.projectID, in.RequestID)
	if err != nil || receipt.RecoveryRequired {
		t.Fatalf("receipt recovery: %+v %v", receipt, err)
	}
	if len(exactExecutionRuns(t, facts, p.ID)) != 0 {
		t.Fatal("canceled predecessor has Run")
	}
	for _, key := range []string{"attempts", "recoveries", "effects", "receipts", "startAuthorities", "bindings"} {
		if len(factRows(t, facts, key)) != 0 {
			t.Fatalf("unexpected %s", key)
		}
	}
	return facts
}
func TestAdmittedNoRunCancelWinsBeforePreparationFirstWrite(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	defer deferNoRunFailureFacts(t, f, p, in)()
	recordRestartFacts(t, "cancel-wins.before", restartDurableFacts(t, f, p, in))
	b := newHeldPreparation()
	done := make(chan error, 1)
	go func() { _, err := b.call(f, sid, p); done <- err }()
	<-b.entered
	receipt, err := f.control.CancelExecutionForSession(f.ctx, in)
	// Always release the owned goroutine, even if an assertion fails below.
	before := restartDurableFacts(t, f, p, in)
	recordRestartFacts(t, "cancel-wins.after-cancel", before)
	close(b.release)
	prepErr := <-done
	if err != nil {
		t.Fatal(err)
	}
	assertAdmissionCancelReceipt(t, p, receipt)
	if !errors.Is(prepErr, sqlite.ErrConflict) {
		t.Fatalf("preparation fence: %v", prepErr)
	}
	after := assertStableCanceledNoRun(t, f, p, in)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("losing preparation wrote durable facts")
	}
	recordRestartFacts(t, "cancel-wins.after-preparation-release", after)
}
func TestPreparationRunWinsBeforeAdmittedNoRunCancellation(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	defer deferNoRunFailureFacts(t, f, p, in)()
	recordRestartFacts(t, "preparation-wins.before", restartDurableFacts(t, f, p, in))
	_, prepared, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := f.plane.Runs.Get(f.ctx, prepared.ID)
	if err != nil {
		t.Fatal(err)
	}
	if durable.ID == "" || durable.RuntimeEpoch != f.plane.DB.RuntimeEpoch() || durable.OperationID != p.ID || durable.OperationKind != run.OperationExecution || durable.ExecutionProposalID != p.ID {
		t.Fatalf("exact Run identity: %+v", durable)
	}
	before := restartDurableFacts(t, f, p, in)
	recordRestartFacts(t, "preparation-wins.after-run-commit", before)
	_, err = f.control.CancelExecutionForSession(f.ctx, in)
	if err == nil || !strings.Contains(err.Error(), "RUN_NOT_ACTIVE") {
		t.Fatalf("empty Run guard: %v", err)
	}
	after := restartDurableFacts(t, f, p, in)
	recordRestartFacts(t, "preparation-wins.after-empty-run-cancel", after)
	if len(factRows(t, after, "eligibilities")) != 0 || len(factRows(t, after, "allTaskExecutions")) != 1 {
		t.Fatal("no-Run rejection created successor/eligibility")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected no-Run cancellation wrote facts")
	}
	if len(exactExecutionRuns(t, after, p.ID)) != 1 || len(factRows(t, after, "attempts")) != 1 || len(factRows(t, after, "cancellations")) != 0 || len(factRows(t, after, "recoveries")) != 0 || len(factRows(t, after, "effects")) != 0 {
		t.Fatal("mixed preparation-wins state")
	}
	// Explicitly start the real supervisor with the existing controlled backend.
	// The backend's entered channel signals that durable exact Turn identity exists.
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{})}
	f.plane.Coordinator.Codex = backend
	consumed, err := f.plane.ExecutionProposals.Get(f.ctx, sid, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	started, err := f.plane.ExecutionProposals.StartAuthorizedTurn(f.ctx, sid, consumed, durable)
	if err != nil {
		t.Fatal(err)
	}
	<-backend.entered
	live, ok := f.plane.Supervisor.GetSession(started.Run.ID)
	if !ok {
		t.Fatal("missing live supervisor")
	}
	active, err := f.plane.Runs.Get(f.ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID != durable.ID || active.RuntimeEpoch != durable.RuntimeEpoch || active.OperationID != p.ID || active.ThreadID == "" || active.TurnID == "" {
		t.Fatal("live exact identity missing")
	}
	activeBefore := restartDurableFacts(t, f, p, in)
	_, err = f.control.CancelExecutionForSession(f.ctx, in)
	if err == nil || !strings.Contains(err.Error(), "RUN_NOT_ACTIVE") {
		t.Fatalf("active empty Run guard: %v", err)
	}
	if !reflect.DeepEqual(activeBefore, restartDurableFacts(t, f, p, in)) {
		t.Fatal("active empty Run rejection wrote facts")
	}
	exact := in
	exact.RunID = active.ID
	exact.RequestID = "exact-runtime-cancel"
	accepted, err := f.control.CancelExecutionForSession(runtimeepoch.WithFence(f.ctx, active.RuntimeEpoch, active.ID), exact)
	if err != nil || accepted.RunID != active.ID || accepted.OwnerID != active.OperationID || accepted.BackendDisposition == "NO_BACKEND" {
		t.Fatalf("exact runtime cancellation: %+v %v", accepted, err)
	}
	_, _ = live.Wait(f.ctx)
	terminal, err := sqlite.NewExecutionCancellationStore(f.plane.DB).Get(f.ctx, f.projectID, exact.RequestID)
	if err != nil || terminal.State != "CANCELED" || terminal.BackendDisposition != "TERMINATED" || terminal.QuiescenceState != "QUIESCED" || backend.calls.Load() != 1 {
		t.Fatalf("runtime terminal: %+v %v calls=%d", terminal, err, backend.calls.Load())
	}
	recordRestartFacts(t, "preparation-wins.after-exact-run-cancel", restartDurableFacts(t, f, p, exact))
	recordRestartFacts(t, "preparation-wins-final", after)
}
func TestAdmittedNoRunCancellationAndPreparationHaveExactlyOneWinner(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	defer deferNoRunFailureFacts(t, f, p, in)()
	ready := make(chan struct{}, 2)
	start := make(chan struct{})
	cancelDone := make(chan error, 1)
	prepDone := make(chan error, 1)
	go func() {
		ready <- struct{}{}
		<-start
		_, err := f.control.CancelExecutionForSession(f.ctx, in)
		cancelDone <- err
	}()
	go func() {
		ready <- struct{}{}
		<-start
		_, _, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p)
		prepDone <- err
	}()
	<-ready
	<-ready
	close(start)
	cancelErr, prepErr := <-cancelDone, <-prepDone
	facts := restartDurableFacts(t, f, p, in)
	if cancelErr == nil {
		if prepErr == nil {
			t.Fatal("both won")
		}
		assertStableCanceledNoRun(t, f, p, in)
	} else {
		if prepErr != nil || !strings.Contains(cancelErr.Error(), "RUN_NOT_ACTIVE") {
			t.Fatalf("no complete winner: cancel=%v prep=%v", cancelErr, prepErr)
		}
		runs := exactExecutionRuns(t, facts, p.ID)
		if len(runs) != 1 || len(factRows(t, facts, "attempts")) != 1 || len(factRows(t, facts, "cancellations")) != 0 || len(factRows(t, facts, "recoveries")) != 0 || len(factRows(t, facts, "effects")) != 0 || len(factRows(t, facts, "receipts")) != 0 {
			t.Fatal("mixed state")
		}
		taskRow := factRows(t, facts, "task")[0]
		executionRow := factRows(t, facts, "execution")[0]
		if taskRow["phase"] == "NO_RUN_SUCCESSOR_REQUIRED" || taskRow["current_execution_id"] != p.ID || executionRow["run_id"] != runs[0]["id"] || executionRow["status"] != "CONSUMED" {
			t.Fatal("prepared pointer/status mismatch")
		}
		if factRows(t, facts, "attempts")[0]["run_id"] != runs[0]["id"] {
			t.Fatal("partial attempt")
		}
	}
	if len(factRows(t, facts, "allTaskExecutions")) != 1 {
		t.Fatal("race created successor Execution")
	}
	if cancelErr != nil && len(factRows(t, facts, "eligibilities")) != 0 {
		t.Fatal("preparation winner created no-Run eligibility")
	}
	recordRestartFacts(t, "simultaneous-final", facts)
}
func reopenSameNoRunFixture(t *testing.T, f *integrationFixture) {
	t.Helper()
	root := f.plane.DataPaths.Root
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	opened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	f.plane = opened
	t.Cleanup(func() { _ = opened.Close() })
	fresh, err := controlapi.NewFromControlPlane(opened)
	if err != nil {
		t.Fatal(err)
	}
	f.control = fresh
}
func assertSameRestartDomain(t *testing.T, before, after map[string]any) {
	t.Helper()
	for key, value := range before {
		if key == "runtimeEpoch" {
			continue
		} // sole allowed boot bookkeeping: new epoch
		if !reflect.DeepEqual(value, after[key]) {
			t.Fatalf("restart durable delta %s: before=%+v after=%+v", key, value, after[key])
		}
	}
}
func stableNoRunRestart(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	defer deferNoRunFailureFacts(t, f, p, in)()
	first, err := f.control.CancelExecutionForSession(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before := assertStableCanceledNoRun(t, f, p, in)
	recordRestartFacts(t, "restart-before-close", before)
	for i, name := range []string{"restart-after-open-1", "restart-after-open-2"} {
		reopenSameNoRunFixture(t, f)
		after := assertStableCanceledNoRun(t, f, p, in)
		assertSameRestartDomain(t, before, after)
		recordRestartFacts(t, name, after)
		replay, err := f.control.CancelExecutionForSession(f.ctx, in)
		if err != nil || !reflect.DeepEqual(first, replay) {
			t.Fatalf("restart %d exact replay: %+v %v", i, replay, err)
		}
		assertSameRestartDomain(t, after, restartDurableFacts(t, f, p, in))
	}
}
func TestNoRunSuccessorRequiredStableAcrossRepeatedRestart(t *testing.T) { stableNoRunRestart(t) }
func TestRestartFixtureDoesNotStartPreparationBeforeStableSnapshot(t *testing.T) {
	TestAdmittedNoRunFixtureHasNoAutonomousPreparation(t)
}
func TestNoRunCancellationFixtureDoesNotWaitOnSchedulerLuck(t *testing.T) {
	TestAdmittedNoRunCancelWinsBeforePreparationFirstWrite(t)
}
func TestPreparationFixtureRequiresExplicitStart(t *testing.T) {
	f, _, p := localMCPScalarAdmission(t)
	assertScalarLayer(t, f, p.ID, 0, 0)
}
func TestStartAuthorizedAloneDoesNotAssertRunAbsent(t *testing.T) {
	TestAdmittedNoRunFixturePreparationWinsWitness(t)
}
func TestEmptyRunIDCancellationCannotTargetExistingRun(t *testing.T) {
	TestPreparationRunWinsBeforeAdmittedNoRunCancellation(t)
}

// Reuses the already-existing Core successor command scenario solely to expose
// full persisted rows across exact replay and actual same-root restart.
func TestCompleteExistingSuccessorRowsAcrossReplayAndRestart(t *testing.T) {
	f, sid, p := localMCPQualifiedAdmission(t)
	cancel := admissionCancelInput(f, sid, p)
	if _, err := f.control.CancelExecutionForSession(f.ctx, cancel); err != nil {
		t.Fatal(err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, p.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "complete-row-successor", PayloadSHA256: strings.Repeat("a", 64), TaskID: owner.ID, PredecessorExecutionID: p.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: p.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID}
	created, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, in)
	if err != nil || created.Outcome != wp.NoRunSuccessorCreated {
		t.Fatalf("existing Core successor scenario: %+v %v", created, err)
	}
	before := restartDurableFacts(t, f, p, cancel)
	recordRestartFacts(t, "successor-created.after-commit", before)
	if len(factRows(t, before, "allTaskExecutions")) != 2 || len(factRows(t, before, "eligibilities")) != 1 || factRows(t, before, "eligibilities")[0]["consumed_successor_execution_id"] != created.SuccessorExecutionID {
		t.Fatal("full created successor/eligibility identity mismatch")
	}
	replay, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, in)
	if err != nil || replay != created {
		t.Fatalf("exact successor replay: %+v %v", replay, err)
	}
	after := restartDurableFacts(t, f, p, cancel)
	recordRestartFacts(t, "successor-created.after-replay", after)
	if !reflect.DeepEqual(before["completeEntities"], after["completeEntities"]) {
		t.Fatal("replay changed complete successor/eligibility rows")
	}
	reopenSameNoRunFixture(t, f)
	reopened := restartDurableFacts(t, f, p, cancel)
	recordRestartFacts(t, "successor-created.after-restart", reopened)
	if !reflect.DeepEqual(before["completeEntities"], reopened["completeEntities"]) {
		t.Fatal("restart changed complete successor/eligibility rows")
	}
}
