package sqlite

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	"sync"
	"testing"
	"time"
)

type continuationPlans struct{ store *TaskPlanStore }

func (p continuationPlans) Get(ctx context.Context, session, id string) (taskplan.Plan, error) {
	return p.store.Get(ctx, id)
}

type continuationRuntime struct {
	mu         sync.Mutex
	started    []run.Run
	failBefore int
}

func (r *continuationRuntime) StartAuthorizedRun(ctx context.Context, _ string, _ string, _ []string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failBefore > 0 {
		r.failBefore--
		return run.Run{}, errors.New("temporary runtime admission failure")
	}
	v, e := reserve(ctx)
	if e == nil {
		r.started = append(r.started, v)
	}
	return v, e
}
func (r *continuationRuntime) StartAuthorizedPlan(ctx context.Context, id string, _ task.WorkSpec, _ task.VerificationSpec, reserve, verification func(context.Context) (run.Run, error), complete func(context.Context, executionproposal.Outcome, string, string) error) (run.Run, error) {
	return r.StartAuthorizedRun(ctx, id, "", nil, reserve)
}
func approvedExecutionFixture(t *testing.T) (*reviewApprovalFixture, string) {
	t.Helper()
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	out, e := f.store.Resolve(context.Background(), f.input(a, "APPROVE"))
	if e != nil {
		t.Fatal(e)
	}
	candidate, e := f.store.AuthorizeTaskExecution(context.Background(), f.session, out.Task.ID)
	if e != nil {
		t.Fatal(e)
	}
	return f, candidate.ID
}
func continuationService(t *testing.T, db *DB, runtime *continuationRuntime) *executionproposal.Service {
	t.Helper()
	s, e := executionproposal.NewService(NewExecutionProposalStore(db), NewAISessionStore(db), continuationPlans{NewTaskPlanStore(db)}, NewTaskStore(db), runtime, nil, nil, time.Now)
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestApprovedExecutionContinuationRestartAndIdempotence(t *testing.T) {
	f, id := approvedExecutionFixture(t)
	ctx := context.Background()
	before, e := NewExecutionProposalStore(f.db).Get(ctx, id)
	if e != nil {
		t.Fatal(e)
	}
	if before.Status != executionproposal.StatusConfirmed || before.RunID != "" || before.TaskDefinitionRevisionID == "" {
		t.Fatalf("not atomic: %+v", before)
	}
	owner, e := NewAgentExecutionStore(f.db).Get(ctx, id)
	if e != nil || owner.Kind != "DEVELOPMENT" {
		t.Fatalf("owner=%+v %v", owner, e)
	}
	root := f.db.StateRoot()
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db = openTestDB(t, root)
	defer f.db.Close()
	runtime := &continuationRuntime{}
	service := continuationService(t, f.db, runtime)
	// A new process has no in-memory wake or cursor from the approving process.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sweep := service.ApprovedContinuation()
			for j := 0; j < 3; j++ {
				sweep(ctx)
			}
		}()
	}
	wg.Wait()
	// Approval materializes an immutable candidate only.  Restart recovery may
	// discover it, but cannot manufacture the separately confirmed start effect.
	if len(runtime.started) != 0 {
		t.Fatalf("implicit restart start=%d", len(runtime.started))
	}
	var n int
	if e = f.db.SQL().QueryRow(`SELECT count(*) FROM runs WHERE agent_execution_id=?`, id).Scan(&n); e != nil || n != 0 {
		t.Fatalf("implicit Run: %d %v", n, e)
	}
	for _, name := range []string{"task_preparations", "task_preparation_access_evidence"} {
		if e = f.db.SQL().QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?`, name).Scan(&n); e != nil || n != 0 {
			t.Fatal("retired entity remains", name, e)
		}
	}
	if e = f.db.SQL().QueryRow(`SELECT count(*) FROM pragma_table_info('runs') WHERE name='task_preparation_id'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("retired Run column", e)
	}
}

func TestApprovedExecutionContinuationCancellationAndRetirement(t *testing.T) {
	for _, mode := range []string{"cancel-before", "cancel-after", "retiring"} {
		t.Run(mode, func(t *testing.T) {
			f, id := approvedExecutionFixture(t)
			ctx := context.Background()
			runtime := &continuationRuntime{}
			sweep := continuationService(t, f.db, runtime).ApprovedContinuation()
			if mode == "cancel-after" {
				sweep(ctx)
			}
			switch mode {
			case "cancel-before", "cancel-after":
				e, _ := NewExecutionProposalStore(f.db).Get(ctx, id)
				_, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: id, RunID: e.RunID, RequestID: "cancel-auto", Reason: "stop"}, mode == "cancel-after")
				if err != nil {
					t.Fatal(err)
				}
			case "retiring":
				if _, err := NewFeatureAreaStore(f.db).beginRetirement(ctx, featurearea.LifecycleMutation{ProjectID: f.project, AISessionID: f.session, FeatureAreaID: f.area, RequestID: "retire-auto", PayloadSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", At: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 3; i++ {
				sweep(ctx)
			}
			if len(runtime.started) != 0 {
				t.Fatalf("invalid automatic start: %d", len(runtime.started))
			}
		})
	}
}

// An unsafe persisted conversation is a workflow recovery boundary even when
// an approved Execution has not reserved its own Run yet.
func TestApprovedExecutionContinuationRecoveryBlocksStart(t *testing.T) {
	f, id := approvedExecutionFixture(t)
	ctx := context.Background()
	_, err := f.db.SQL().Exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,backend,host_tool_contract_version,continuation_safe,created_at) VALUES('unsafe-auto',?,?,'CODEX',1,0,?)`, f.project, f.area, formatTime(f.now))
	if err != nil {
		t.Fatal(err)
	}
	runtime := &continuationRuntime{}
	sweep := continuationService(t, f.db, runtime).ApprovedContinuation()
	for i := 0; i < 3; i++ {
		sweep(ctx)
	}
	e, err := NewExecutionProposalStore(f.db).Get(ctx, id)
	if err != nil || e.RunID != "" || e.Status != executionproposal.StatusConfirmed || len(runtime.started) != 0 {
		t.Fatalf("recovery admitted work: %+v %v", e, err)
	}
}

func TestApprovedExecutionContinuationCancellationRace(t *testing.T) {
	for iteration := 0; iteration < 12; iteration++ {
		f, id := approvedExecutionFixture(t)
		ctx := context.Background()
		runtime := &continuationRuntime{}
		sweep := continuationService(t, f.db, runtime).ApprovedContinuation()
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; sweep(ctx) }()
		go func() {
			defer wg.Done()
			<-start
			_, _, _ = NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: id, RequestID: "race-before"}, false)
		}()
		close(start)
		wg.Wait()
		e, err := NewExecutionProposalStore(f.db).Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// If reservation won, cancellation must use the observed exact Run. A
		// no-Run request that lost the race cannot silently cancel another attempt.
		if e.RunID != "" {
			_, _, err = NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "EXECUTION", OwnerID: id, RunID: e.RunID, RequestID: "race-observed"}, true)
			if err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 3; i++ {
			sweep(ctx)
		}
		if len(runtime.started) > 1 {
			t.Fatal("duplicate start after cancellation race")
		}
		var n int
		if err = f.db.SQL().QueryRow(`SELECT count(*) FROM runs WHERE agent_execution_id=?`, id).Scan(&n); err != nil || n != len(runtime.started) {
			t.Fatalf("reservation cardinality=%d starts=%d %v", n, len(runtime.started), err)
		}
	}
}

func TestApprovedExecutionContinuationSameFeatureAreaBusyAndUnknown(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "busy", true: "UNKNOWN"}[unknown], func(t *testing.T) {
			f, id := approvedExecutionFixture(t)
			ctx := context.Background()
			runtime := &continuationRuntime{}
			service := continuationService(t, f.db, runtime)
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); service.ApprovedContinuation()(ctx) }()
			}
			wg.Wait()
			after, err := NewExecutionProposalStore(f.db).Get(ctx, id)
			if err != nil || after.RunID != "" || after.Status != executionproposal.StatusConfirmed || len(runtime.started) != 0 {
				t.Fatalf("implicit continuation admitted work %+v %v", after, err)
			}
		})
	}
}

func TestApprovedExecutionContinuationRetriesOnlyUnreservedWork(t *testing.T) {
	f, id := approvedExecutionFixture(t)
	ctx := context.Background()
	runtime := &continuationRuntime{failBefore: 1}
	sweep := continuationService(t, f.db, runtime).ApprovedContinuation()
	sweep(ctx)
	before, err := NewExecutionProposalStore(f.db).Get(ctx, id)
	if err != nil || before.RunID != "" || before.Status != executionproposal.StatusConfirmed {
		t.Fatalf("failed admission consumed approval: %+v %v", before, err)
	}
	sweep(ctx)
	sweep(ctx)
	if len(runtime.started) != 0 {
		t.Fatalf("retry starts=%d", len(runtime.started))
	}
}

func TestExecutionIdentityFailureRollsBackStartWithoutUndoingApproval(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err != nil {
		t.Fatal(err)
	}
	// Scalar admission and Run creation are now separate transactions.
	// First, admit the execution (no Run created yet).
	value, reserved, err := f.store.ReserveTaskExecution(ctx, f.session, f.candidate.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != executionproposal.StatusAdmitted || reserved.ID != "" {
		t.Fatal("scalar admission did not separate from Run creation")
	}
	// Fail the Run insertion to prove the admission transaction leaves the
	// approved identity intact and the Run creation can be retried.
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER fail_development_identity BEFORE INSERT ON runs BEGIN SELECT RAISE(ABORT,'injected runtime start failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := continueAdmittedExecution(ctx, f.db, value.ID, testNow); err == nil {
		t.Fatal("partial start accepted")
	}
	if ownedTask(t, f).Phase != task.PhaseImplementationReady || ownedTask(t, f).CurrentExecutionID == "" || ownedCount(t, f, "execution_proposals") != 1 || ownedCount(t, f, "task_definition_revisions") != 2 {
		t.Fatal("start rollback changed prior approval")
	}
	if _, err := f.db.SQL().Exec(`DROP TRIGGER fail_development_identity`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := continueAdmittedExecution(ctx, f.db, value.ID, testNow); err != nil {
		t.Fatal(err)
	}
}
