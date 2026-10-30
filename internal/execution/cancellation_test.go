package execution_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// Every race uses explicit protocol/finalizer gates, never elapsed sleeps.
type cancellationSession struct {
	id                                   int
	started, interrupted, finish, closed chan struct{}
	status                               string
	interruptErr                         error
	closeErr                             error
	onInterrupt                          func(string, string) error
	onClose                              func()
	calls                                atomic.Int32
	closeOnce                            sync.Once
}

func newCancellationSession(id int) *cancellationSession {
	return &cancellationSession{id: id, started: make(chan struct{}), interrupted: make(chan struct{}), finish: make(chan struct{}), closed: make(chan struct{}), status: "interrupted"}
}
func (s *cancellationSession) StartThread(context.Context, string) (string, error) {
	return "thread-cancel", nil
}
func (s *cancellationSession) ResumeThread(_ context.Context, id, _ string, _ *run.TokenCounts) (string, error) {
	return id, nil
}
func (s *cancellationSession) StartTurn(ctx context.Context, thread, prompt string) (exec.CodexTurnResult, error) {
	return s.StartTurnWithStarted(ctx, thread, prompt, nil)
}
func (s *cancellationSession) StartTurnWithStarted(ctx context.Context, thread, _ string, started func(string) error) (exec.CodexTurnResult, error) {
	turn := fmt.Sprintf("turn-cancel-%d", s.id)
	if started != nil {
		if err := started(turn); err != nil {
			return exec.CodexTurnResult{}, err
		}
	}
	close(s.started)
	select {
	case <-s.finish:
		r := exec.CodexTurnResult{TurnID: turn, Message: "partial result preserved"}
		if s.status == "completed" {
			return r, nil
		}
		return r, &codex.TurnError{ThreadID: thread, TurnID: turn, Status: s.status}
	case <-ctx.Done():
		return exec.CodexTurnResult{TurnID: turn}, ctx.Err()
	}
}
func (s *cancellationSession) InterruptTurn(_ context.Context, thread, turn string) error {
	if s.calls.Add(1) != 1 {
		return errors.New("duplicate interrupt")
	}
	if s.onInterrupt != nil {
		if err := s.onInterrupt(thread, turn); err != nil {
			return err
		}
	}
	close(s.interrupted)
	return s.interruptErr
}
func (s *cancellationSession) Close() error {
	s.closeOnce.Do(func() {
		if s.onClose != nil {
			s.onClose()
		}
		close(s.closed)
	})
	return s.closeErr
}

type cancellationFactory struct {
	sessions []*cancellationSession
	next     int
}

func (f *cancellationFactory) Start(context.Context, codexconfig.Config) (exec.CodexSession, error) {
	if f.next >= len(f.sessions) {
		return nil, errors.New("unexpected successor session")
	}
	s := f.sessions[f.next]
	f.next++
	return s, nil
}

func cancellationFixture(t *testing.T, sessions ...*cancellationSession) (*authorizedPlanPanicFixture, *sqlite.ExecutionCancellationStore) {
	t.Helper()
	f := newAuthorizedPlanPanicFixture(t)
	store := sqlite.NewExecutionCancellationStore(f.db)
	f.coordinator.Cancellations = store
	f.coordinator.Codex = &cancellationFactory{sessions: sessions}
	t.Cleanup(func() { _ = f.supervisor.Shutdown(); _ = f.db.Close() })
	return f, store
}
func startCancellationFixture(t *testing.T, f *authorizedPlanPanicFixture) run.Run {
	return f.start(t, func(ctx context.Context, outcome executionproposal.Outcome, code, message string) error {
		_, err := f.proposals.Complete(ctx, f.proposalValue.ID, outcome, code, message, time.Now().UTC())
		return err
	})
}
func cancellationInput(f *authorizedPlanPanicFixture, r run.Run) executioncancel.Input {
	return executioncancel.Input{ProjectID: f.taskValue.ProjectID, AISessionID: f.proposalValue.AISessionID, ExecutionID: f.proposalValue.ID, RunID: r.ID, RequestID: "cancel-exact", Reason: "authorized cancellation"}
}
func awaitCancellationGate(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation synchronization gate did not open")
	}
}
func awaitCancellationSession(t *testing.T, live *exec.RunSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := live.Wait(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestExecutionCancellationImplementationAndVerification(t *testing.T) {
	for _, verification := range []bool{false, true} {
		t.Run(fmt.Sprintf("verification=%v", verification), func(t *testing.T) {
			impl, verify := newCancellationSession(1), newCancellationSession(2)
			f, store := cancellationFixture(t, impl, verify)
			r := startCancellationFixture(t, f)
			select {
			case <-impl.started:
			case <-time.After(5 * time.Second):
				current, getErr := f.runs.Get(context.Background(), r.ID)
				t.Fatalf("implementation start gate did not open: run=%+v err=%v", current, getErr)
			}
			session := impl
			if verification {
				impl.status = "completed"
				close(impl.finish)
				awaitCancellationGate(t, verify.started)
				var err error
				r, err = f.runs.Get(context.Background(), "run-panic-verification")
				if err != nil {
					t.Fatal(err)
				}
				session = verify
			}
			live, ok := f.supervisor.GetSession(r.ID)
			if !ok {
				t.Fatal("missing live session")
			}
			current, _ := f.runs.Get(context.Background(), r.ID)
			if current.ThreadID == "" || current.TurnID == "" || current.Status != run.StatusRunning {
				t.Fatalf("cancellation fixture did not reach an interruptible Run: %+v", current)
			}
			ws, err := sqlite.NewWorkspaceStore(f.db).Get(context.Background(), current.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			tree, err := sqlite.NewWorktreeStore(f.db).Get(context.Background(), current.WorktreeID)
			if err != nil {
				t.Fatal(err)
			}
			partial := filepath.Join(tree.Path, "preserved-partial.txt")
			if err = os.WriteFile(partial, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			in := cancellationInput(f, r)
			session.onInterrupt = func(thread, turn string) error {
				receipt, err := store.Get(context.Background(), in.ProjectID, in.RequestID)
				if err != nil || receipt.BackendInterruptState != "REQUESTED" {
					return fmt.Errorf("interrupt preceded durable intent: %+v %v", receipt, err)
				}
				if thread != current.ThreadID || turn != current.TurnID {
					return errors.New("wrong backend target")
				}
				return nil
			}
			first, err := f.supervisor.CancelExecution(context.Background(), in)
			if err != nil || first.State != "CANCEL_REQUESTED" {
				t.Fatalf("receipt=%+v err=%v", first, err)
			}
			awaitCancellationGate(t, session.interrupted)
			claim, err := sqlite.NewAgentConversationStore(f.db).GetClaim(context.Background(), f.areaID)
			if err != nil || claim.RunID != r.ID {
				t.Fatalf("ACK released claim: %+v %v", claim, err)
			}
			for i := 0; i < 3; i++ {
				if _, err = f.supervisor.CancelExecution(context.Background(), in); err != nil {
					t.Fatal(err)
				}
			}
			// Closing the transport is independently required after terminal event.
			closeEntered, closeRelease := make(chan struct{}), make(chan struct{})
			session.onClose = func() { close(closeEntered); <-closeRelease }
			close(session.finish)
			awaitCancellationGate(t, closeEntered)
			if _, err = sqlite.NewAgentConversationStore(f.db).GetClaim(context.Background(), f.areaID); err != nil {
				t.Fatal("claim released before transport joined")
			}
			close(closeRelease)
			awaitCancellationSession(t, live)
			receipt, err := store.Get(context.Background(), in.ProjectID, in.RequestID)
			if err != nil || receipt.State != "CANCELED" || receipt.RunStatus != run.StatusCanceled || receipt.ExecutionStatus != "CANCELED" || receipt.QuiescenceState != "QUIESCED" || receipt.RecoveryRequired || receipt.ResolvedAt == nil {
				t.Fatalf("receipt=%+v err=%v", receipt, err)
			}
			if session.calls.Load() != 1 {
				t.Fatal("replay dispatched twice")
			}
			if _, err = sqlite.NewAgentConversationStore(f.db).GetClaim(context.Background(), f.areaID); !errors.Is(err, agentconversation.ErrNotFound) {
				t.Fatalf("claim remains: %v", err)
			}
			if _, err = os.Stat(partial); err != nil {
				t.Fatal("workspace files removed")
			}
			after, err := sqlite.NewWorkspaceStore(f.db).Get(context.Background(), ws.ID)
			if err != nil || after.RuntimeQuarantined {
				t.Fatalf("safe cancellation quarantined workspace: %+v %v", after, err)
			}
			var conversations int
			_ = f.db.SQL().QueryRow(`SELECT count(*) FROM agent_conversations WHERE feature_area_id=?`, f.areaID).Scan(&conversations)
			if conversations != 1 {
				t.Fatal("cancel rotated conversation")
			}
			if _, err = f.runs.Succeed(runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID), r.ID, run.Result{}); err == nil {
				t.Fatal("late callback rewrote cancellation")
			}
			if _, err = f.runs.AttachEndSnapshot(runtimeepoch.WithFence(context.Background(), r.RuntimeEpoch, r.ID), r.ID, current.EndSnapshot); !errors.Is(err, runtimeepoch.ErrFenced) {
				t.Fatalf("late accounting callback not fenced: %v", err)
			}
			all, _ := f.runs.List(context.Background())
			want := 2 // the Task also retains its pre-execution Review Run
			if verification {
				want = 3
			}
			if len(all) != want {
				t.Fatalf("successor runs: %d", len(all))
			}
		})
	}
}

func TestExecutionCancellationUnknownInterruptOrphansWithoutQuiescence(t *testing.T) {
	session := newCancellationSession(1)
	session.interruptErr = errors.New("response/outcome lost")
	f, store := cancellationFixture(t, session)
	r := startCancellationFixture(t, f)
	awaitCancellationGate(t, session.started)
	live, _ := f.supervisor.GetSession(r.ID)
	in := cancellationInput(f, r)
	if _, err := f.supervisor.CancelExecution(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	awaitCancellationGate(t, session.interrupted)
	awaitCancellationSession(t, live)
	receipt, err := store.Get(context.Background(), in.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "UNKNOWN_ORPHANED" || receipt.QuiescenceState != "UNKNOWN" || receipt.RecoveryRequired || receipt.ExecutionStatus != "CANCELED" || receipt.ResolvedAt == nil {
		t.Fatalf("unknown receipt=%+v %v", receipt, err)
	}
	state, err := sqlite.NewAgentConversationStore(f.db).State(context.Background(), f.areaID)
	if err != nil || state.Status != "READY" || state.Claim != nil {
		t.Fatalf("state=%+v %v", state, err)
	}
	if _, err = f.supervisor.CancelExecution(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if session.calls.Load() != 1 {
		t.Fatal("unknown replay resent interrupt")
	}
}

func TestExecutionCancellationTransportJoinFailureNeverPublishesSafeCancellation(t *testing.T) {
	session := newCancellationSession(1)
	session.closeErr = errors.New("owned transport could not be joined")
	f, store := cancellationFixture(t, session)
	r := startCancellationFixture(t, f)
	awaitCancellationGate(t, session.started)
	live, _ := f.supervisor.GetSession(r.ID)
	in := cancellationInput(f, r)
	if _, err := f.supervisor.CancelExecution(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	awaitCancellationGate(t, session.interrupted)
	close(session.finish) // Exact interrupted terminal, followed by failed join.
	awaitCancellationSession(t, live)
	receipt, err := store.Get(context.Background(), in.ProjectID, in.RequestID)
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "UNKNOWN_ORPHANED" || receipt.QuiescenceState != "UNKNOWN" || receipt.RecoveryRequired || receipt.ExecutionStatus != "CANCELED" || receipt.ResolvedAt == nil {
		t.Fatalf("failed join receipt=%+v err=%v", receipt, err)
	}
	state, err := sqlite.NewAgentConversationStore(f.db).State(context.Background(), f.areaID)
	if err != nil || state.Status != "READY" || state.Claim != nil {
		t.Fatalf("failed join lost exact recovery claim: %+v err=%v", state, err)
	}
	if _, err := f.supervisor.CancelExecution(context.Background(), in); err != nil || session.calls.Load() != 1 {
		t.Fatalf("failed join replay sent another interrupt: calls=%d err=%v", session.calls.Load(), err)
	}
}

func TestExecutionCancellationLegacyRuntimeFacadeCannotCancelWorkflowRun(t *testing.T) {
	session := newCancellationSession(1)
	f, _ := cancellationFixture(t, session)
	r := startCancellationFixture(t, f)
	awaitCancellationGate(t, session.started)
	if _, err := f.supervisor.CancelRun(context.Background(), r.ID); err == nil {
		t.Fatal("legacy runtime facade accepted workflow-owned Run")
	}
	current, err := f.runs.Get(context.Background(), r.ID)
	if err != nil || current.Status != run.StatusRunning || session.calls.Load() != 0 {
		t.Fatalf("legacy facade mutated Run=%+v calls=%d err=%v", current, session.calls.Load(), err)
	}
	select {
	case <-session.closed:
		t.Fatal("legacy facade closed workflow transport")
	default:
	}
}

func TestExecutionCancellationNormalCompletionWinsBeforeAndAfterIntent(t *testing.T) {
	for _, intentFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("intentFirst=%v", intentFirst), func(t *testing.T) {
			session := newCancellationSession(1)
			session.status = "completed"
			f, store := cancellationFixture(t, session)
			f.verification.Mode = "NONE"
			f.verification.Scope = ""
			f.verification.Waiver = &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Deterministic cancellation race fixture"}
			f.verification.Checks = nil
			f.verification.AcceptanceCriteria = nil
			f.verification.CodexInstruction = ""
			r := startCancellationFixture(t, f)
			awaitCancellationGate(t, session.started)
			live, _ := f.supervisor.GetSession(r.ID)
			in := cancellationInput(f, r)
			if intentFirst {
				if _, err := f.supervisor.CancelExecution(context.Background(), in); err != nil {
					t.Fatal(err)
				}
				awaitCancellationGate(t, session.interrupted)
			}
			close(session.finish)
			awaitCancellationSession(t, live)
			if !intentFirst {
				receipt, err := f.supervisor.CancelExecution(context.Background(), in)
				if err != nil || receipt.State != "ALREADY_TERMINAL" {
					t.Fatalf("late=%+v %v", receipt, err)
				}
			}
			receipt, err := store.Get(context.Background(), in.ProjectID, in.RequestID)
			want := "ALREADY_TERMINAL"
			if intentFirst {
				want = "CANCEL_NOT_APPLIED"
			}
			if err != nil || receipt.State != want || receipt.RunStatus != run.StatusSucceeded || receipt.ExecutionStatus != "SUCCEEDED" {
				t.Fatalf("receipt=%+v %v", receipt, err)
			}
		})
	}
}

func (s *cancellationSession) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (exec.CodexTurnResult, error) {
	result, err := s.StartTurnWithStarted(ctx, thread, prompt, started)
	if string(schema) == exec.ImplementationReportJSONSchema {
		raw, _ := json.Marshal(exec.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
