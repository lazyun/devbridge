package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

func TestWorkflowApprovalMissingSessionAndFailRunCannotWriteTerminal(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Execution, agentconversation.Review} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			f := newAgentStorageFixture(t, "approval-missing-"+string(kind))
			r, _ := beginRecoveryRun(t, f, kind, "missing-"+string(kind), true)
			store := NewApprovalStore(f.db)
			pending := pendingApproval("selected", r.ID, approval.KindCommandExecution)
			pending.ThreadID, pending.TurnID = "thread-missing-"+string(kind), "turn-missing-"+string(kind)
			a, err := store.CreatePending(ctx, pending, testNow)
			if err != nil {
				t.Fatal(err)
			}
			pending.ID = "sibling"
			if _, err = store.CreatePending(ctx, pending, testNow); err != nil {
				t.Fatal(err)
			}
			before, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeTask, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil {
				t.Fatal(err)
			}
			service := approval.NewService(store, nil)
			got, err := service.Decide(ctx, a.ID, approval.Decision{Type: approval.DecisionDecline})
			if !errors.Is(err, approval.ErrSessionNotLive) || got.Status != approval.StatusDecided || got.DeliveredAt != nil {
				t.Fatalf("decision=%+v err=%v", got, err)
			}
			if err = store.FailRun(ctx, r.ID, "approval_delivery_failed", "bypass attempt", testNow); !errors.Is(err, approval.ErrWorkflowFinalizationRequired) {
				t.Fatalf("direct FailRun=%v", err)
			}
			after, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("Run changed: before=%+v after=%+v err=%v", before, after, err)
			}
			afterTask, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil || !reflect.DeepEqual(beforeTask, afterTask) {
				t.Fatalf("Task changed: %+v err=%v", afterTask, err)
			}
			sibling, err := store.Get(ctx, "sibling")
			if err != nil || sibling.Status != approval.StatusPending {
				t.Fatalf("sibling=%+v err=%v", sibling, err)
			}
			if _, err = service.Decide(ctx, a.ID, approval.Decision{Type: approval.DecisionDecline}); !errors.Is(err, approval.ErrAlreadyResolved) {
				t.Fatalf("retry=%v", err)
			}
			assertApprovalClosureUnfinalized(t, f, r.ID)
			oldEpoch := f.db.RuntimeEpoch()
			reopenAgentFixtureDB(t, &f)
			if err = NewRecoveryStore(f.db).RecoverAgentOperations(ctx, testNow.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, r.ID)
			if err != nil || boundary.Advance != agentconversation.Unknown {
				t.Fatalf("restart boundary=%+v err=%v", boundary, err)
			}
			if _, err = NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID); err != nil {
				t.Fatal(err)
			}
			if err = NewApprovalStore(f.db).FailRun(runtimeepoch.WithFence(ctx, oldEpoch, r.ID), r.ID, "late", "late callback", testNow); !errors.Is(err, runtimeepoch.ErrFenced) {
				t.Fatalf("stale failure=%v", err)
			}
		})
	}
}

// Real SQLite stores and the production Coordinator own all state transitions.
// Gates hold external delivery and transport join so assertions inspect the
// intermediate state deterministically, including an already completed Turn.
func TestApprovalDeliveryCoordinatorFinalizationClosure(t *testing.T) {
	for _, mode := range []string{"delivery", "persistence-known", "caller-canceled", "close-failed", "intent-failed", "terminal-rollback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newAgentStorageFixture(t, "approval-"+mode)
			store := NewApprovalStore(f.db)
			service := approval.NewService(store, nil)
			session := &approvalClosureSession{requests: make(chan codex.ApprovalRequest, 2), deliveryEntered: make(chan struct{}), deliveryRelease: make(chan struct{}), closeEntered: make(chan struct{}), closeRelease: make(chan struct{}), knownTerminal: mode == "persistence-known"}
			if mode == "close-failed" {
				session.closeErr = errors.New("transport join failed")
			}
			defer session.releaseClose()
			defer session.releaseDelivery()
			c, prepared := prepareApprovalClosureCoordinator(t, f, service, session)
			runID := prepared.Run.ID
			if mode == "persistence-known" {
				if _, err := f.db.SQL().Exec(`CREATE TRIGGER fail_delivery BEFORE UPDATE OF status ON approvals WHEN NEW.status='DELIVERED' BEGIN SELECT RAISE(ABORT,'injected delivery persistence failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "intent-failed" {
				if _, err := f.db.SQL().Exec(`CREATE TRIGGER fail_intent BEFORE INSERT ON agent_finalization_intents BEGIN SELECT RAISE(ABORT,'injected intent failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "terminal-rollback" {
				if _, err := f.db.SQL().Exec(`CREATE TRIGGER fail_apply BEFORE UPDATE ON agent_finalization_intents WHEN NEW.applied_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected finalization apply failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			type outcome struct {
				r   run.Run
				err error
			}
			finished := make(chan outcome, 1)
			executeCtx, cancelExecution := context.WithCancel(ctx)
			defer cancelExecution()
			go func() { r, err := c.ExecuteRun(executeCtx, prepared, nil); finished <- outcome{r, err} }()
			waitApprovalClosurePending(t, store, "selected")
			waitApprovalClosurePending(t, store, "sibling")
			decisionDone := make(chan error, 1)
			go func() {
				_, err := service.Decide(ctx, "selected", approval.Decision{Type: approval.DecisionApproveOnce})
				decisionDone <- err
			}()
			waitApprovalClosureSignal(t, session.deliveryEntered)
			assertApprovalClosureUnfinalized(t, f, runID)
			if mode == "caller-canceled" {
				cancelExecution()
			}
			session.releaseDelivery()
			var delivery *approval.DeliveryError
			select {
			case err := <-decisionDone:
				if !errors.As(err, &delivery) {
					t.Fatalf("delivery error=%v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("decision did not finish")
			}
			waitApprovalClosureSignal(t, session.closeEntered)
			// This is the original P1 window: delivery has failed, local turn
			// cancellation has fired, but transport quiescence is not established.
			assertApprovalClosureUnfinalized(t, f, runID)
			currentTask, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil || currentTask.Status != task.StatusActive {
				t.Fatalf("Task before close=%+v err=%v", currentTask, err)
			}
			selected, err := store.Get(ctx, "selected")
			if err != nil || selected.Status != approval.StatusDecided || selected.DeliveredAt != nil {
				t.Fatalf("selected=%+v err=%v", selected, err)
			}
			sibling, err := store.Get(ctx, "sibling")
			if err != nil || sibling.Status != approval.StatusPending {
				t.Fatalf("sibling=%+v err=%v", sibling, err)
			}
			if _, err = service.Decide(ctx, "selected", approval.Decision{Type: approval.DecisionApproveOnce}); !errors.Is(err, approval.ErrAlreadyResolved) {
				t.Fatalf("duplicate=%v", err)
			}
			session.releaseClose()
			var result outcome
			select {
			case result = <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("Coordinator did not finish")
			}
			if result.err == nil {
				t.Fatal("delivery failure was lost")
			}
			current, err := NewRunStore(f.db).Get(ctx, runID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "intent-failed" || mode == "terminal-rollback" {
				if current.Status != run.StatusWaitingApproval || current.FinishedAt != nil || current.TokenUsage.Status != run.TokenUsagePending {
					t.Fatalf("terminal escaped failed finalization: %+v", current)
				}
				currentTask, err = NewTaskStore(f.db).Get(ctx, f.task.ID)
				if err != nil || currentTask.Status != task.StatusActive {
					t.Fatalf("Task escaped rollback: %+v %v", currentTask, err)
				}
				var applied int
				if err = f.db.SQL().QueryRow(`SELECT COUNT(applied_at) FROM agent_finalization_intents WHERE run_id=?`, runID).Scan(&applied); err != nil || applied != 0 {
					t.Fatalf("applied=%d err=%v", applied, err)
				}
				boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, runID)
				if err != nil || boundary.Quiesced || boundary.Advance != agentconversation.Unknown {
					t.Fatalf("rollback boundary=%+v err=%v", boundary, err)
				}
				return
			}
			if current.Status != run.StatusInfrastructureFailed || current.Failure == nil || current.Failure.Code != delivery.Code || current.Failure.Stage != "approval" || current.FinishedAt == nil || current.TokenUsage.Status == run.TokenUsagePending {
				t.Fatalf("final Run=%+v", current)
			}
			currentTask, err = NewTaskStore(f.db).Get(ctx, f.task.ID)
			if err != nil || currentTask.Status != task.StatusActive {
				t.Fatalf("final Task=%+v err=%v", currentTask, err)
			}
			selected, err = store.Get(ctx, "selected")
			if err != nil || selected.Status != approval.StatusCanceled || selected.Decision == nil || selected.Decision.Type != approval.DecisionApproveOnce || selected.DecidedAt == nil || selected.DeliveredAt != nil {
				t.Fatalf("final decision=%+v err=%v", selected, err)
			}
			sibling, err = store.Get(ctx, "sibling")
			if err != nil || sibling.Status != approval.StatusCanceled {
				t.Fatalf("final sibling=%+v err=%v", sibling, err)
			}
			boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, runID)
			if err != nil || boundary.Quiesced != (mode != "close-failed" && boundary.Advance != agentconversation.Unknown) {
				t.Fatalf("boundary=%+v err=%v", boundary, err)
			}
			wantAdvance := agentconversation.Unknown
			if session.knownTerminal {
				wantAdvance = agentconversation.KnownAdvance
			}
			if boundary.Advance != wantAdvance {
				t.Fatalf("boundary=%+v", boundary)
			}
			var intents, applied int
			if err = f.db.SQL().QueryRow(`SELECT COUNT(*),COUNT(applied_at) FROM agent_finalization_intents WHERE run_id=?`, runID).Scan(&intents, &applied); err != nil || intents != 1 || applied != 1 {
				t.Fatalf("intents=%d applied=%d err=%v", intents, applied, err)
			}
			if _, err = NewExecutionProposalStore(f.db).Complete(ctx, current.ExecutionProposalID, executionproposal.OutcomeFailed, "", "", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			_, claimErr := NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID)
			if session.knownTerminal && !errors.Is(claimErr, agentconversation.ErrNotFound) || !session.knownTerminal && claimErr != nil {
				t.Fatalf("claim after product finalization=%v", claimErr)
			}
			if session.deliveries.Load() != 1 {
				t.Fatalf("deliveries=%d", session.deliveries.Load())
			}
			events, err := NewEventStore(f.db).List(ctx, event.Filter{RunID: runID})
			if err != nil {
				t.Fatal(err)
			}
			terminalEvents := 0
			for _, e := range events {
				if e.Type == event.RunStatusChanged && string(e.Payload) != "" {
					var payload struct {
						To run.Status `json:"to"`
					}
					if err := json.Unmarshal(e.Payload, &payload); err != nil {
						t.Fatal(err)
					}
					if payload.To == run.StatusInfrastructureFailed {
						terminalEvents++
					}
				}
			}
			if terminalEvents != 1 {
				t.Fatalf("terminal events=%d", terminalEvents)
			}
		})
	}
}

type approvalClosureReadGate struct {
	*ApprovalStore
	entered, release chan struct{}
	once             sync.Once
}

func (s *approvalClosureReadGate) Get(ctx context.Context, id string) (approval.Approval, error) {
	a, err := s.ApprovalStore.Get(ctx, id)
	s.once.Do(func() { close(s.entered); <-s.release })
	return a, err
}

func TestApprovalFinalizationSealsDecisionAdmission(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "approval-admission")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "admission", true)
	store := NewApprovalStore(f.db)
	pending := pendingApproval("selected", r.ID, approval.KindCommandExecution)
	pending.ThreadID, pending.TurnID = "thread-admission", "turn-admission"
	if _, err := store.CreatePending(ctx, pending, testNow); err != nil {
		t.Fatal(err)
	}
	gate := &approvalClosureReadGate{ApprovalStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	service := approval.NewService(gate, nil)
	deliveries := 0
	unregister, err := service.Register(r.ID, approvalDecisionResolver(func(context.Context, string, codex.ApprovalDecision) error { deliveries++; return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	decided := make(chan error, 1)
	go func() {
		_, err := service.Decide(ctx, pending.ID, approval.Decision{Type: approval.DecisionApproveOnce})
		decided <- err
	}()
	waitApprovalClosureSignal(t, gate.entered)
	if err := service.Wait(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	if err := <-decided; !errors.Is(err, approval.ErrSessionNotLive) {
		t.Fatalf("late decision=%v", err)
	}
	a, err := store.Get(ctx, pending.ID)
	if err != nil || a.Status != approval.StatusPending || deliveries != 0 {
		t.Fatalf("late delivery: Approval=%+v deliveries=%d err=%v", a, deliveries, err)
	}
	assertApprovalClosureUnfinalized(t, f, r.ID)
}

func TestApprovalFinalizationJoinsAllAdmittedDeliveriesAndKeepsFirstFailure(t *testing.T) {
	ctx := context.Background()
	f := newAgentStorageFixture(t, "approval-drain")
	r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "drain", true)
	store := NewApprovalStore(f.db)
	for _, id := range []string{"first", "second", "late"} {
		a := pendingApproval(id, r.ID, approval.KindCommandExecution)
		a.ThreadID, a.TurnID = "thread-drain", "turn-drain"
		if _, err := store.CreatePending(ctx, a, testNow); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan string, 2)
	first, second := make(chan struct{}), make(chan struct{})
	secondCanceled := make(chan struct{})
	service := approval.NewService(store, nil)
	unregister, err := service.Register(r.ID, approvalDecisionResolver(func(ctx context.Context, id string, _ codex.ApprovalDecision) error {
		entered <- id
		if id == "first" {
			<-first
		} else {
			<-ctx.Done()
			close(secondCanceled)
			<-second
		}
		return errors.New(id + " delivery failure")
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	done := make(chan error, 2)
	for _, id := range []string{"first", "second"} {
		go func() {
			_, err := service.Decide(ctx, id, approval.Decision{Type: approval.DecisionApproveOnce})
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("delivery not admitted")
		}
	}
	close(first)
	var original *approval.DeliveryError
	if err := <-done; !errors.As(err, &original) {
		t.Fatalf("first delivery=%v", err)
	}
	waitApprovalClosureSignal(t, secondCanceled)
	if _, err := service.Decide(ctx, "late", approval.Decision{Type: approval.DecisionApproveOnce}); !errors.Is(err, approval.ErrSessionNotLive) {
		t.Fatalf("new admission after failure=%v", err)
	}
	// A canceled waiter proves a second delivery is still in flight; it cannot
	// consume the retained failure or allow the Coordinator to finalize early.
	waitCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := service.Wait(waitCtx, r.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("drain did not wait for second delivery: %v", err)
	}
	close(second)
	if err := <-done; err == nil {
		t.Fatal("second delivery failure missing")
	}
	if err := service.Wait(ctx, r.ID); err != original {
		t.Fatalf("first failure replaced: %v", err)
	}
	if err := service.Wait(ctx, r.ID); err != original {
		t.Fatalf("failure consumed on readback: %v", err)
	}
	assertApprovalClosureUnfinalized(t, f, r.ID)
}

func assertApprovalClosureUnfinalized(t *testing.T, f agentStorageFixture, runID string) {
	t.Helper()
	ctx := context.Background()
	r, err := NewRunStore(f.db).Get(ctx, runID)
	if err != nil || r.Status != run.StatusWaitingApproval || r.FinishedAt != nil || r.Failure != nil || r.TokenUsage.Status != run.TokenUsagePending {
		t.Fatalf("premature terminal: %+v err=%v", r, err)
	}
	var count int
	if err = f.db.SQL().QueryRow(`SELECT COUNT(*) FROM agent_finalization_intents WHERE run_id=?`, runID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("premature finalization count=%d err=%v", count, err)
	}
	boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, runID)
	if err != nil || boundary.Quiesced || boundary.Advance != agentconversation.Unknown {
		t.Fatalf("premature boundary=%+v err=%v", boundary, err)
	}
	claim, err := NewAgentConversationStore(f.db).GetClaim(ctx, f.area.ID)
	if err != nil || claim.RunID != runID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
}

func waitApprovalClosureSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("protocol gate was not reached")
	}
}

func waitApprovalClosurePending(t *testing.T, store *ApprovalStore, id string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		if a, err := store.Get(context.Background(), id); err == nil && a.Status == approval.StatusPending {
			return
		}
		select {
		case <-deadline:
			rows, err := store.db.SQL().Query(`SELECT id,status,COALESCE(failure_code,''),COALESCE(failure_message,'') FROM runs ORDER BY created_at`)
			if err != nil {
				t.Fatalf("Approval was not requested; inspect Runs: %v", err)
			}
			defer rows.Close()
			var runs []string
			for rows.Next() {
				var id, status, code, message string
				if err := rows.Scan(&id, &status, &code, &message); err != nil {
					t.Fatal(err)
				}
				runs = append(runs, fmt.Sprintf("%s:%s:%s:%s", id, status, code, message))
			}
			t.Fatalf("Approval was not requested; Runs=%v", runs)
		case <-time.After(time.Millisecond):
		}
	}
}

type approvalClosureSession struct {
	requests                                                     chan codex.ApprovalRequest
	deliveryEntered, deliveryRelease, closeEntered, closeRelease chan struct{}
	deliveryOnce, closeOnce                                      sync.Once
	deliveries                                                   atomic.Int32
	knownTerminal                                                bool
	closeErr                                                     error
}

func (s *approvalClosureSession) releaseDelivery() {
	s.deliveryOnce.Do(func() { close(s.deliveryRelease) })
}
func (s *approvalClosureSession) releaseClose() { s.closeOnce.Do(func() { close(s.closeRelease) }) }
func (s *approvalClosureSession) StartThread(context.Context, string) (string, error) {
	return "thread-closure", nil
}
func (s *approvalClosureSession) StartTurn(context.Context, string, string) (execution.CodexTurnResult, error) {
	return execution.CodexTurnResult{}, errors.New("started callback required")
}
func (s *approvalClosureSession) StartTurnWithStarted(ctx context.Context, thread, _ string, started func(string) error) (execution.CodexTurnResult, error) {
	if err := started("turn-closure"); err != nil {
		return execution.CodexTurnResult{}, err
	}
	for _, id := range []string{"selected", "sibling"} {
		s.requests <- codex.ApprovalRequest{ID: id, ThreadID: thread, TurnID: "turn-closure", Kind: codex.ApprovalKindCommandExecution, RequestPayload: []byte(`{"command":"true"}`), AllowedDecisions: []codex.DecisionType{codex.DecisionApproveOnce, codex.DecisionDecline}}
	}
	if s.knownTerminal {
		<-s.deliveryEntered
		return execution.CodexTurnResult{TurnID: "turn-closure", Message: "terminal observed"}, nil
	}
	<-ctx.Done()
	return execution.CodexTurnResult{TurnID: "turn-closure"}, ctx.Err()
}
func (s *approvalClosureSession) NextApproval(ctx context.Context) (codex.ApprovalRequest, error) {
	select {
	case a := <-s.requests:
		return a, nil
	case <-ctx.Done():
		return codex.ApprovalRequest{}, ctx.Err()
	}
}
func (s *approvalClosureSession) ResolveApproval(context.Context, string, codex.ApprovalDecision) error {
	s.deliveries.Add(1)
	close(s.deliveryEntered)
	<-s.deliveryRelease
	if s.knownTerminal {
		return nil
	}
	return errors.New("injected backend delivery failure")
}
func (s *approvalClosureSession) Close() error {
	close(s.closeEntered)
	<-s.closeRelease
	return s.closeErr
}

type approvalClosureFactory struct{ session *approvalClosureSession }

func (f approvalClosureFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return f.session, nil
}

type approvalClosureTree struct{ root string }

func (f approvalClosureTree) Create(_ context.Context, in worktree.CreateInput) (worktree.Worktree, error) {
	if err := os.MkdirAll(f.root, 0700); err != nil {
		return worktree.Worktree{}, err
	}
	return worktree.Worktree{ID: "tree-closure", RepositoryID: in.RepositoryID, RunID: in.RunID, Path: f.root, Branch: "test/closure", BaseCommit: in.BaseCommit, Status: worktree.StatusReady}, nil
}

type approvalClosureEvidence struct{}

func (approvalClosureEvidence) CaptureSnapshot(context.Context, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (approvalClosureEvidence) CollectRun(context.Context, string, string, string, string) (evidence.GitEvidence, error) {
	return evidence.GitEvidence{}, nil
}

func prepareApprovalClosureCoordinator(t *testing.T, f agentStorageFixture, approvals *approval.Service, session *approvalClosureSession) (*execution.Coordinator, execution.PreparedRun) {
	t.Helper()
	ctx := context.Background()
	id := seedExecutionOwner(t, f.db, f.task, f.session, "execution-closure")
	proposals := NewExecutionProposalStore(f.db)
	_, reserved, err := proposals.ConsumeAndCreateRun(ctx, id, "run-closure", testNow)
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	ids := func() (string, error) { seq++; return fmt.Sprintf("closure-%d", seq), nil }
	c := &execution.Coordinator{Repositories: NewRepositoryStore(f.db), CodexConfigs: NewCodexConfigStore(f.db), RuntimeConfigProvider: codexruntime.Default, Tasks: task.NewService(NewTaskStore(f.db), nil, nil, nil, nil, nil), Runs: run.NewService(NewRunStore(f.db), ids, nil), Workspaces: workspace.NewService(NewWorkspaceStore(f.db), ids, nil), Worktrees: &worktree.DurableManager{External: approvalClosureTree{filepath.Join(f.root, "worktree")}, Store: NewWorktreeStore(f.db)}, Codex: approvalClosureFactory{session}, Evidence: approvalClosureEvidence{}, Approvals: approvals, Conversations: NewAgentConversationStore(f.db), Executions: &taskgoal.ExecutionAdapter{Proposals: proposals}}
	prepared, err := c.PrepareReservedRun(ctx, reserved.ID, execution.Instruction{CodexInstruction: f.task.WorkSpec.CodexInstruction, ExecutionScope: f.task.WorkSpec.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	return c, prepared
}

func (s *approvalClosureSession) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.StartTurnWithStarted(ctx, thread, prompt, started)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
