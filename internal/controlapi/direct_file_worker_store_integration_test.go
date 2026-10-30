package controlapi_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func fileWorkerFixture(t *testing.T) (*upperLifecycleFixture, task.Task, directfile.State, directfile.WorkerClaimInput) {
	t.Helper()
	f, current := approveDirectFixture(t, false, true)
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state: %+v %v", state, err)
	}
	in := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: state.TaskDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: state.ProtocolVersion, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "worker-dispatch", WorkerID: "worker-1"}}
	return f, current, state, in
}
func fileWorkerResultInput(f *upperLifecycleFixture, state directfile.State, claim *directfile.WorkerClaimIdentity) directfile.ImplementationResultInput {
	return directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: state.TaskDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "worker-result", Summary: "Inspected authorized members; no file change required", VerificationNotes: "No commands executed", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration, WorkerClaim: claim}
}

func TestFileWorkerStoreDispatchReplayAndAtomicResult(t *testing.T) {
	f, current, state, in := fileWorkerFixture(t)
	store := sqlite.NewDirectFileWorkerStore(f.plane.DB)
	first, err := store.ClaimFileImplementationWorker(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != directfile.WorkerDispatchIntent || first.ExecutionPayload == nil || first.ExecutionPayload.Instruction != current.WorkSpec.CodexInstruction || !reflect.DeepEqual(first.ExecutionPayload.Definition.WorkSpec, current.WorkSpec) || !reflect.DeepEqual(first.ExecutionPayload.Definition.RepositoryTargets, current.RepositoryTargets) || !reflect.DeepEqual(first.ExecutionPayload.Verification, current.VerificationSpec) || len(first.ExecutionPayload.Access.RepositoryTargets) != len(current.RepositoryTargets) {
		t.Fatalf("first: %+v", first)
	}
	replay, err := store.ClaimFileImplementationWorker(f.ctx, in)
	if err != nil || replay.ExecutionPayload != nil || replay.State != directfile.WorkerOutcomeUnknown || !reflect.DeepEqual(replay.Receipt, first.Receipt) {
		t.Fatalf("replay %+v %v", replay, err)
	}
	if _, err = store.GetFileImplementationWorker(context.Background(), in); err == nil {
		t.Fatal("unauthenticated history")
	}
	other := in
	other.WorkerID = "other"
	if _, err = store.ClaimFileImplementationWorker(f.ctx, other); err == nil {
		t.Fatal("reassigned worker")
	}
	resultIn := fileWorkerResultInput(f, state, nil)
	if _, err = f.facade.SubmitFileImplementationResult(f.ctx, resultIn); err == nil {
		t.Fatal("unclaimed result bypass")
	}
	resultIn.WorkerClaim = &in.WorkerClaimIdentity
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER fail_file_worker_completion BEFORE UPDATE ON direct_file_worker_claims BEGIN SELECT RAISE(ABORT,'injected worker completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.SubmitFileImplementationResult(f.ctx, resultIn); err == nil {
		t.Fatal("missing completion rollback")
	}
	var n int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_implementation_results WHERE workflow_id=?`, state.WorkflowID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("result rollback=%d %v", n, err)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER fail_file_worker_completion`); err != nil {
		t.Fatal(err)
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, resultIn)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetFileImplementationWorker(f.ctx, in)
	if err != nil || got.State != directfile.WorkerResultRecorded || got.ResultReceipt == nil || got.ResultReceipt.ID != result.ID || got.ExecutionPayload != nil {
		t.Fatalf("completed=%+v %v", got, err)
	}
	if _, err = f.facade.SubmitFileImplementationResult(aisession.WithCallerScope(context.Background(), "foreign"), resultIn); err == nil {
		t.Fatal("foreign completed replay")
	}
	reopenMutationFixture(t, f)
	stale := runtimeepoch.WithFence(f.ctx, state.RuntimeEpoch, "")
	replayResult, err := f.facade.SubmitFileImplementationResult(stale, resultIn)
	if err != nil || replayResult.ID != result.ID || !replayResult.Replay {
		t.Fatalf("terminal result replay %+v %v", replayResult, err)
	}
	history, err := sqlite.NewDirectFileWorkerStore(f.plane.DB).GetFileImplementationWorker(stale, in)
	if err != nil || history.State != directfile.WorkerResultRecorded {
		t.Fatalf("terminal history %+v %v", history, err)
	}
}
func TestFileWorkerStoreConcurrentDispatchOnce(t *testing.T) {
	f, _, _, in := fileWorkerFixture(t)
	store := sqlite.NewDirectFileWorkerStore(f.plane.DB)
	var wg sync.WaitGroup
	results := make(chan directfile.WorkerClaimResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := store.ClaimFileImplementationWorker(f.ctx, in)
			results <- v
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	count := 0
	for r := range results {
		if r.ExecutionPayload != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("payload count=%d", count)
	}
}
func TestFileWorkerStoreRestartRecoveryRefusedWithoutGrantMutation(t *testing.T) {
	f, current, state, in := fileWorkerFixture(t)
	store := sqlite.NewDirectFileWorkerStore(f.plane.DB)
	if _, err := store.ClaimFileImplementationWorker(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	reopenMutationFixture(t, f)
	store = sqlite.NewDirectFileWorkerStore(f.plane.DB)
	for _, call := range []func(context.Context, directfile.WorkerClaimInput) (directfile.WorkerClaimResult, error){store.GetFileImplementationWorker, store.ClaimFileImplementationWorker} {
		v, e := call(f.ctx, in)
		if e != nil || v.State != directfile.WorkerOutcomeUnknown || v.ExecutionPayload != nil {
			t.Fatalf("historical=%+v %v", v, e)
		}
	}
	before, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || before.FailureCode != directfile.WorkerContinuationUnsupported || before.Enabled {
		t.Fatalf("restart %+v %v", before, err)
	}
	recovered, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state)
	if err == nil && recovered.State == "COMPLETED" {
		t.Fatalf("unsupported recovery %+v", recovered)
	}
	after, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || after.RuntimeEpoch != before.RuntimeEpoch || after.GrantGeneration != before.GrantGeneration {
		t.Fatalf("recovery mutated grant %+v %v", after, err)
	}
	canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state)
	if err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("cancel %+v %v", canceled, err)
	}
	history, err := store.GetFileImplementationWorker(f.ctx, in)
	if err != nil || history.State != directfile.WorkerOutcomeUnknown || history.ExecutionPayload != nil {
		t.Fatalf("canceled history %+v %v", history, err)
	}
	if _, err = f.facade.SubmitFileImplementationResult(f.ctx, fileWorkerResultInput(f, state, &in.WorkerClaimIdentity)); err == nil {
		t.Fatal("late claimed result after cancellation")
	}
}
func TestFileWorkerStoreCannotAdoptPriorMutation(t *testing.T) {
	f, current, state, in := fileWorkerFixture(t)
	op := directInput(f, state, current.RepositoryTargets[0])
	op.Path = "new.txt"
	op.RequestID = "manual"
	op.Content = "manual bytes\n"
	if _, err := f.facade.FileCreate(f.ctx, op); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewDirectFileWorkerStore(f.plane.DB).ClaimFileImplementationWorker(f.ctx, in); err == nil {
		t.Fatal("claimed previously used workflow")
	}
	var n int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_worker_claims`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("intent=%d %v", n, err)
	}
}
func TestFileWorkerAbsentIdentityPreservesLegacyResultPayload(t *testing.T) {
	in := directfile.ImplementationResultInput{AISessionID: "session", TaskID: "task", TaskDefinitionRevisionID: "revision", WorkflowID: "workflow", RequestID: "request", Summary: "summary", VerificationNotes: "none"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"aiSessionId":"session","taskId":"task","taskDefinitionRevisionId":"revision","workflowId":"workflow","requestId":"request","summary":"summary","verificationNotes":"none"}`
	if string(raw) != want {
		t.Fatalf("legacy payload changed: %s", raw)
	}
}

func TestFileWorkerRecoveryPreparationAndClaimRespectPendingAction(t *testing.T) {
	t.Run("active-recovery-refused-before-intent", func(t *testing.T) {
		f, current, _, in := fileWorkerFixture(t)
		if _, err := sqlite.NewDirectFileWorkerStore(f.plane.DB).ClaimFileImplementationWorker(f.ctx, in); err != nil {
			t.Fatal(err)
		}
		state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
		if err != nil {
			t.Fatal(err)
		}
		var before, after int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM confirmation_intents`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err = directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state); err == nil {
			t.Fatal("active recovery prepared")
		}
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM confirmation_intents`).Scan(&after); err != nil || before != after {
			t.Fatalf("recovery created intent %d→%d %v", before, after, err)
		}
	})
	t.Run("pending-cancel-blocks-enrollment", func(t *testing.T) {
		f, current, state, in := fileWorkerFixture(t)
		_, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "pending-cancel", Purpose: c.Execution, Action: "CANCEL_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "CANCEL_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: state.DecisionID, TaskDefinitionRevisionID: state.TaskDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Summary: "Explicit cancellation", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = sqlite.NewDirectFileWorkerStore(f.plane.DB).ClaimFileImplementationWorker(f.ctx, in); err == nil {
			t.Fatal("claim bypassed pending action")
		}
		var count int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_worker_claims`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("worker claim=%d %v", count, err)
		}
	})
}

func TestFileWorkerClaimAndUnclaimedResultSerialize(t *testing.T) {
	f, _, state, in := fileWorkerFixture(t)
	store := sqlite.NewDirectFileWorkerStore(f.plane.DB)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var claim directfile.WorkerClaimResult
	var result directfile.ImplementationResult
	var claimErr, resultErr error
	go func() { defer wg.Done(); <-start; claim, claimErr = store.ClaimFileImplementationWorker(f.ctx, in) }()
	go func() {
		defer wg.Done()
		<-start
		result, resultErr = f.facade.SubmitFileImplementationResult(f.ctx, fileWorkerResultInput(f, state, nil))
	}()
	close(start)
	wg.Wait()
	if (claimErr == nil) == (resultErr == nil) {
		t.Fatalf("claim=%+v err=%v result=%+v err=%v", claim, claimErr, result, resultErr)
	}
	var claims, results int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_worker_claims`).Scan(&claims); err != nil {
		t.Fatal(err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_implementation_results`).Scan(&results); err != nil {
		t.Fatal(err)
	}
	if claims+results != 1 {
		t.Fatalf("committed claims=%d results=%d", claims, results)
	}
}
