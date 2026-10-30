package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestApprovalProjectionAndSeparateDeliveryLifecycle(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "projection")
	store := NewApprovalStore(db)
	first := pendingApproval("approval-first", f.run.ID, approval.KindCommandExecution)
	second := pendingApproval("approval-second", f.run.ID, approval.KindFileChange)
	if _, err := store.CreatePending(ctx, first, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertWaitingRun(t, db, f.run.ID, f.task.ID)
	if _, err := store.CreatePending(ctx, second, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	decided, err := store.Decide(ctx, first.ID, approval.Decision{Type: approval.DecisionApproveOnce}, testNow.Add(3*time.Minute))
	if err != nil || decided.Status != approval.StatusDecided || decided.DecidedAt == nil || decided.DeliveredAt != nil {
		t.Fatalf("decided=%+v err=%v", decided, err)
	}
	if _, err := store.MarkDelivered(ctx, first.ID, testNow.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertWaitingRun(t, db, f.run.ID, f.task.ID)
	if _, err := store.Decide(ctx, second.ID, approval.Decision{Type: approval.DecisionDecline}, testNow.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDelivered(ctx, second.ID, testNow.Add(6*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, err := NewRunStore(db).Get(ctx, f.run.ID)
	if err != nil || got.Status != run.StatusRunning || got.FinishedAt != nil || got.TokenUsage.Status != run.TokenUsagePending {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	values, err := store.ListByRun(ctx, f.run.ID)
	if err != nil || len(values) != 2 || values[0].Status != approval.StatusDelivered || values[1].Status != approval.StatusDelivered {
		t.Fatalf("approvals=%+v err=%v", values, err)
	}
	events, err := NewEventStore(db).List(ctx, event.Filter{RunID: f.run.ID})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, value := range events {
		counts[value.Type]++
	}
	if counts[event.ApprovalRequested] != 2 || counts[event.ApprovalDecided] != 2 || counts[event.ApprovalDelivered] != 2 {
		t.Fatalf("event counts=%v", counts)
	}
}

func TestApprovalPermissionDecisionLegalityAndExactGrant(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "permissions")
	value := pendingApproval("approval-permissions", f.run.ID, approval.KindPermissions)
	value.RequestPayload = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","permissions":{"fileSystem":{"write":["/exact"]},"network":{"enabled":true}}}`)
	value.AllowedDecisions = []approval.DecisionType{approval.DecisionGrantRequestedPermissions, approval.DecisionDecline, approval.DecisionCancel}
	store := NewApprovalStore(db)
	if _, err := store.CreatePending(ctx, value, testNow); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionApproveOnce}, testNow.Add(time.Minute)); !errors.Is(err, approval.ErrInvalidDecision) {
		t.Fatalf("illegal decision err=%v", err)
	}
	broader := json.RawMessage(`{"fileSystem":{"write":["/"]},"network":{"enabled":true}}`)
	if _, err := store.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionGrantRequestedPermissions, Permissions: broader}, testNow.Add(time.Minute)); !errors.Is(err, approval.ErrInvalidDecision) {
		t.Fatalf("broader grant err=%v", err)
	}
	decided, err := store.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionGrantRequestedPermissions}, testNow.Add(time.Minute))
	if err != nil || decided.Decision == nil || len(decided.Decision.Permissions) == 0 {
		t.Fatalf("decided=%+v err=%v", decided, err)
	}
	if _, err := store.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionDecline}, testNow.Add(2*time.Minute)); !errors.Is(err, approval.ErrAlreadyResolved) {
		t.Fatalf("duplicate decision err=%v", err)
	}
}

func TestWorkflowApprovalCancelDoesNotTerminateRunOrTask(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "cancel")
	store := NewApprovalStore(db)
	first := pendingApproval("approval-cancel", f.run.ID, approval.KindCommandExecution)
	second := pendingApproval("approval-other", f.run.ID, approval.KindFileChange)
	_, _ = store.CreatePending(ctx, first, testNow)
	_, _ = store.CreatePending(ctx, second, testNow.Add(time.Second))
	decided, err := store.Decide(ctx, first.ID, approval.Decision{Type: approval.DecisionCancel}, testNow.Add(time.Minute))
	if err != nil || decided.Status != approval.StatusDecided {
		t.Fatalf("decided=%+v err=%v", decided, err)
	}
	runValue, _ := NewRunStore(db).Get(ctx, f.run.ID)
	taskValue, _ := NewTaskStore(db).Get(ctx, f.task.ID)
	other, _ := store.Get(ctx, second.ID)
	if !decided.WorkflowOwnedRun || runValue.Status != run.StatusWaitingApproval || runValue.FinishedAt != nil || runValue.Failure != nil {
		t.Fatalf("run=%+v", runValue)
	}
	if taskValue.Status != task.StatusActive || other.Status != approval.StatusPending {
		t.Fatalf("task=%s other=%s", taskValue.Status, other.Status)
	}
	if _, err := store.MarkDelivered(ctx, first.ID, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	runValue, _ = NewRunStore(db).Get(ctx, f.run.ID)
	if runValue.Status != run.StatusWaitingApproval {
		t.Fatalf("delivery ignored sibling approval: %s", runValue.Status)
	}
}

type approvalDecisionResolver func(context.Context, string, codex.ApprovalDecision) error

func (resolve approvalDecisionResolver) ResolveApproval(ctx context.Context, id string, decision codex.ApprovalDecision) error {
	return resolve(ctx, id, decision)
}

func TestWorkflowApprovalCancelDeclinesOnlyActionAndNeverRedelivers(t *testing.T) {
	for _, kind := range []approval.Kind{approval.KindCommandExecution, approval.KindFileChange, approval.KindPermissions, approval.KindLegacyCommand, approval.KindLegacyFileChange} {
		t.Run(string(kind), func(t *testing.T) {
			ctx := context.Background()
			f, r, _, _ := cancellationStoreFixture(t, "approval-action-"+string(kind))
			store := NewApprovalStore(f.db)
			first := pendingApproval("approval-action", r.ID, kind)
			first.ThreadID, first.TurnID = r.ThreadID, r.TurnID
			second := first
			second.ID = "approval-sibling"
			for _, value := range []approval.Approval{first, second} {
				if _, err := store.CreatePending(ctx, value, testNow); err != nil {
					t.Fatal(err)
				}
			}
			service := approval.NewService(store, nil)
			cancelCalls, deliveries := 0, 0
			unregister, err := service.Register(r.ID, approvalDecisionResolver(func(_ context.Context, id string, decision codex.ApprovalDecision) error {
				deliveries++
				if decision.Type != codex.DecisionDecline {
					t.Fatalf("approval %s received %s, want action DECLINE", id, decision.Type)
				}
				assertWaitingRun(t, f.db, r.ID, f.task.ID)
				return nil
			}), func() { cancelCalls++ })
			if err != nil {
				t.Fatal(err)
			}
			defer unregister()
			delivered, err := service.Decide(ctx, first.ID, approval.Decision{Type: approval.DecisionCancel})
			if err != nil || delivered.Status != approval.StatusDelivered || !delivered.WorkflowOwnedRun || delivered.Decision == nil || delivered.Decision.Type != approval.DecisionCancel {
				t.Fatalf("delivered=%+v err=%v", delivered, err)
			}
			// Losing the response does not repeat delivery or turn/Run cancellation.
			if _, err := service.Decide(ctx, first.ID, approval.Decision{Type: approval.DecisionCancel}); !errors.Is(err, approval.ErrAlreadyResolved) {
				t.Fatalf("replay err=%v", err)
			}
			fresh, err := NewApprovalStore(f.db).Get(ctx, first.ID)
			if err != nil || !fresh.WorkflowOwnedRun || fresh.DecidedAt == nil || !fresh.DecidedAt.Equal(*delivered.DecidedAt) {
				t.Fatalf("fresh readback=%+v err=%v", fresh, err)
			}
			sibling, err := store.Get(ctx, second.ID)
			if err != nil || sibling.Status != approval.StatusPending || cancelCalls != 0 || deliveries != 1 {
				t.Fatalf("sibling=%+v cancelCalls=%d deliveries=%d err=%v", sibling, cancelCalls, deliveries, err)
			}
			assertWaitingRun(t, f.db, r.ID, f.task.ID)
			if _, err := service.Decide(ctx, second.ID, approval.Decision{Type: approval.DecisionDecline}); err != nil {
				t.Fatal(err)
			}
			current, err := NewRunStore(f.db).Get(ctx, r.ID)
			if err != nil || current.Status != run.StatusRunning || current.FinishedAt != nil || cancelCalls != 0 || deliveries != 2 {
				t.Fatalf("current=%+v cancelCalls=%d deliveries=%d err=%v", current, cancelCalls, deliveries, err)
			}
			var cancellationCount int
			if err := f.db.SQL().QueryRow(`SELECT COUNT(*) FROM execution_cancellations WHERE run_id=?`, r.ID).Scan(&cancellationCount); err != nil || cancellationCount != 0 {
				t.Fatalf("Approval invented cancellation intent: count=%d err=%v", cancellationCount, err)
			}
			assertCancellationClaim(t, f, r, true)
		})
	}
}

func TestNonApprovalRunTerminationCancelsOpenApprovalsAtomically(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "terminal-cleanup")
	store := NewApprovalStore(db)
	value := pendingApproval("approval-terminal-cleanup", f.run.ID, approval.KindCommandExecution)
	if _, err := store.CreatePending(ctx, value, testNow); err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow.Add(time.Minute) })
	if _, err := runs.Fail(ctx, f.run.ID, run.Failure{Kind: run.FailureInfrastructure, Code: "runtime_interrupted"}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.Get(ctx, value.ID)
	if err != nil || stored.Status != approval.StatusCanceled {
		t.Fatalf("approval=%+v err=%v", stored, err)
	}
	events, err := NewEventStore(db).List(ctx, event.Filter{RunID: f.run.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range events {
		found = found || value.Type == event.ApprovalCanceled
	}
	if !found {
		t.Fatal("terminal Run update omitted approval.canceled event")
	}
}

func TestWorkflowApprovalDeliveryFailureKeepsDecisionForCoordinator(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "delivery-fail")
	store := NewApprovalStore(db)
	value := pendingApproval("approval-delivery-fail", f.run.ID, approval.KindCommandExecution)
	if _, err := store.CreatePending(ctx, value, testNow); err != nil {
		t.Fatal(err)
	}
	service := approval.NewService(store, func() time.Time { return testNow.Add(time.Minute) })
	resolver := failingResolver{err: errors.New("stdin closed")}
	unregister, err := service.Register(f.run.ID, resolver, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	decided, err := service.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionApproveOnce})
	if err == nil || decided.Status != approval.StatusDecided {
		t.Fatalf("decided=%+v err=%v", decided, err)
	}
	stored, _ := store.Get(ctx, value.ID)
	runValue, _ := NewRunStore(db).Get(ctx, f.run.ID)
	if stored.Status != approval.StatusDecided || stored.DeliveredAt != nil {
		t.Fatalf("approval=%+v", stored)
	}
	if runValue.Status != run.StatusWaitingApproval || runValue.Failure != nil || runValue.FinishedAt != nil {
		t.Fatalf("run=%+v", runValue)
	}
	var delivery *approval.DeliveryError
	if err := service.Wait(ctx, f.run.ID); !errors.As(err, &delivery) || delivery.Code != "approval_delivery_failed" {
		t.Fatalf("Coordinator delivery observation=%v", err)
	}
}

func TestApprovalServiceExposesDecidedBeforeExternalDeliveryCompletes(t *testing.T) {
	db := openTestDB(t, t.TempDir())
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "delivery-order")
	store := NewApprovalStore(db)
	value := pendingApproval("approval-delivery-order", f.run.ID, approval.KindCommandExecution)
	if _, err := store.CreatePending(ctx, value, testNow); err != nil {
		t.Fatal(err)
	}
	resolver := &blockingResolver{entered: make(chan struct{}), release: make(chan struct{})}
	service := approval.NewService(store, func() time.Time { return testNow.Add(time.Minute) })
	unregister, err := service.Register(f.run.ID, resolver, func() {})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	result := make(chan struct {
		value approval.Approval
		err   error
	}, 1)
	go func() {
		decided, err := service.Decide(ctx, value.ID, approval.Decision{Type: approval.DecisionApproveOnce})
		result <- struct {
			value approval.Approval
			err   error
		}{decided, err}
	}()
	select {
	case <-resolver.entered:
	case <-time.After(time.Second):
		t.Fatal("resolver was not called")
	}
	stored, err := store.Get(ctx, value.ID)
	if err != nil || stored.Status != approval.StatusDecided || stored.DecidedAt == nil || stored.DeliveredAt != nil {
		t.Fatalf("during delivery=%+v err=%v", stored, err)
	}
	close(resolver.release)
	got := <-result
	if got.err != nil || got.value.Status != approval.StatusDelivered || got.value.DeliveredAt == nil {
		t.Fatalf("after delivery=%+v err=%v", got.value, got.err)
	}
}

func TestApprovalWaitingRecoveryExpiresOpenApprovalsIdempotently(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	defer db.Close()
	ctx := context.Background()
	f := runningApprovalFixture(t, db, "recovery")
	store := NewApprovalStore(db)
	pending := pendingApproval("approval-pending", f.run.ID, approval.KindCommandExecution)
	decided := pendingApproval("approval-decided", f.run.ID, approval.KindFileChange)
	delivered := pendingApproval("approval-delivered", f.run.ID, approval.KindLegacyCommand)
	for index, value := range []approval.Approval{pending, decided, delivered} {
		if _, err := store.CreatePending(ctx, value, testNow.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Decide(ctx, decided.ID, approval.Decision{Type: approval.DecisionApproveOnce}, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Decide(ctx, delivered.ID, approval.Decision{Type: approval.DecisionDecline}, testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkDelivered(ctx, delivered.ID, testNow.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Recovery is a successor-process action. Reopen so it owns a distinct
	// runtime epoch instead of simulating recovery inside the old writer.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	store = NewApprovalStore(db)
	recoveryStore := NewRecoveryStore(db)
	finished := testNow.Add(3 * time.Minute)
	err := recoveryStore.RecoverAgentOperations(ctx, finished)
	if err != nil {
		t.Fatal(err)
	}
	pendingGot, _ := store.Get(ctx, pending.ID)
	decidedGot, _ := store.Get(ctx, decided.ID)
	deliveredGot, _ := store.Get(ctx, delivered.ID)
	if pendingGot.Status != approval.StatusExpired || decidedGot.Status != approval.StatusExpired || decidedGot.Decision == nil || deliveredGot.Status != approval.StatusDelivered {
		t.Fatalf("pending=%+v decided=%+v delivered=%+v", pendingGot, decidedGot, deliveredGot)
	}
	runValue, _ := NewRunStore(db).Get(ctx, f.run.ID)
	if runValue.Status != run.StatusInfrastructureFailed || runValue.FinishedAt == nil || !runValue.FinishedAt.Equal(finished) || runValue.Failure == nil || runValue.Failure.Code != recovery.InterruptedCode {
		t.Fatalf("run=%+v", runValue)
	}
	err = recoveryStore.RecoverAgentOperations(ctx, finished.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	runValue, _ = NewRunStore(db).Get(ctx, f.run.ID)
	if !runValue.FinishedAt.Equal(finished) {
		t.Fatalf("FinishedAt changed: %v", runValue.FinishedAt)
	}
}

func runningApprovalFixture(t *testing.T, db *DB, suffix string) fixture {
	t.Helper()
	ctx := context.Background()
	f := seed(t, db, suffix)
	runs := run.NewService(NewRunStore(db), nil, func() time.Time { return testNow })
	if _, err := runs.Transition(ctx, f.run.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err := NewAgentConversationStore(db).BeginRun(ctx, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.AttachThread(ctx, f.run.ID, "thread-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Transition(ctx, f.run.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	return f
}

func pendingApproval(id, runID string, kind approval.Kind) approval.Approval {
	allowed := []approval.DecisionType{approval.DecisionApproveOnce, approval.DecisionApproveForSession, approval.DecisionDecline, approval.DecisionCancel}
	return approval.Approval{ID: id, RunID: runID, ThreadID: "thread-1", TurnID: "turn-1", ItemID: id + "-item", Kind: kind, RequestPayload: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`), AllowedDecisions: allowed}
}

func assertWaitingRun(t *testing.T, db *DB, runID, taskID string) {
	t.Helper()
	runValue, err := NewRunStore(db).Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	taskValue, err := NewTaskStore(db).Get(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	if runValue.Status != run.StatusWaitingApproval || runValue.FinishedAt != nil || runValue.TokenUsage.Status != run.TokenUsagePending || taskValue.Status != task.StatusActive {
		t.Fatalf("run=%+v task=%+v", runValue, taskValue)
	}
}

type failingResolver struct{ err error }

func (r failingResolver) ResolveApproval(context.Context, string, codex.ApprovalDecision) error {
	return r.err
}

type blockingResolver struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockingResolver) ResolveApproval(context.Context, string, codex.ApprovalDecision) error {
	close(r.entered)
	<-r.release
	return nil
}
