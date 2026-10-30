package execution

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
)

// The supervisor's unit tests use a recording backend and an explicit fake
// validation capability. Production MemoryStore intentionally lacks it.
type runtimeValidatedMemoryStore struct{ *run.MemoryStore }

func (runtimeValidatedMemoryStore) ValidateRunExecution(context.Context, run.Run) error { return nil }

func (runtimeValidatedMemoryStore) ValidateRunExecutionDispatch(context.Context, run.Run) error {
	return nil
}

type unvalidatedRuns struct{ RunService }

type entryValidatedRuns struct{ RunService }

func (entryValidatedRuns) ValidateRunExecution(context.Context, run.Run) error { return nil }

func TestAgentExecutionOwnershipFailsClosedBeforeBackend(t *testing.T) {
	for _, operation := range []struct {
		name    string
		kind    run.OperationKind
		purpose run.Purpose
	}{
		{"Review admission", run.OperationReview, run.PurposeScopeReview},
		{"Implementation", run.OperationExecution, run.PurposeImplementation},
		{"Verification", run.OperationExecution, run.PurposeVerification},
	} {
		for _, reason := range []string{"missing execution", "wrong execution", "wrong task", "wrong owner", "wrong scope", "LEGACY_UNKNOWN", "stale runtime epoch", "UNKNOWN_ORPHANED"} {
			t.Run(operation.name+"/"+reason, func(t *testing.T) {
				c, runs, tasks, _, session, _ := successfulCoordinator()
				prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
				if err != nil {
					t.Fatal(err)
				}
				prepared.Run.OperationKind = operation.kind
				prepared.Run.Purpose = operation.purpose
				runs.validateExecution = func(context.Context, run.Run) error { return errors.New(reason) }
				result, err := c.ExecuteRun(context.Background(), prepared, nil)
				if err == nil || result.Status != prepared.Run.Status || result.Failure != nil {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				stored, getErr := runs.Get(context.Background(), prepared.Run.ID)
				if getErr != nil || stored.Status != prepared.Run.Status || stored.Failure != nil {
					t.Fatalf("unproven ownership mutated durable Run: %+v %v", stored, getErr)
				}
				if session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 {
					t.Fatalf("backend called: %+v", session)
				}
			})
		}
	}
}

func TestAgentExecutionValidatorIsRequired(t *testing.T) {
	c, runs, tasks, _, session, _ := successfulCoordinator()
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Runs = unvalidatedRuns{runs}
	if _, err := executeDirectFeatureAreaRun(context.Background(), c, prepared); err == nil {
		t.Fatal("missing validator authorized backend")
	}
	if session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 {
		t.Fatalf("backend called: %+v", session)
	}
}

func TestCanceledUnprovenExecutionCannotAcquireCleanupAuthority(t *testing.T) {
	c, runs, tasks, _, _, _ := successfulCoordinator()
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	runs.validateExecution = func(context.Context, run.Run) error { return errors.New("LEGACY_UNKNOWN") }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ExecuteRun(ctx, prepared, nil); !errors.Is(err, ErrAgentExecutionUnproven) {
		t.Fatalf("unproven owner was treated as cancelable: %v", err)
	}
	stored, err := runs.Get(context.Background(), prepared.Run.ID)
	if err != nil || stored.Status != run.StatusPreparing || stored.Failure != nil {
		t.Fatalf("canceled unproven owner mutated durable Run: %+v %v", stored, err)
	}
}

func TestAgentExecutionOwnershipRevalidatedAtDispatch(t *testing.T) {
	for _, boundary := range []int{2, 3} {
		t.Run(map[int]string{2: "thread", 3: "turn"}[boundary], func(t *testing.T) {
			c, runs, tasks, _, session, _ := successfulCoordinator()
			prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			runs.validateExecution = func(context.Context, run.Run) error {
				calls++
				if calls == boundary {
					return errors.New("ownership became stale")
				}
				return nil
			}
			if _, err := c.ExecuteRun(context.Background(), prepared, nil); err == nil {
				t.Fatal("stale ownership authorized backend")
			}
			wantStarts := boundary - 2
			if session.startCalls != wantStarts || session.turn != 0 || calls != boundary {
				t.Fatalf("starts=%d turns=%d validations=%d", session.startCalls, session.turn, calls)
			}
		})
	}
}

func TestAgentExecutionDispatchValidatorIsRequired(t *testing.T) {
	c, runs, tasks, _, session, _ := successfulCoordinator()
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Runs = entryValidatedRuns{runs}
	if _, err := executeDirectFeatureAreaRun(context.Background(), c, prepared); err == nil {
		t.Fatal("entry ownership proof authorized dispatch without conversation proof")
	}
	if session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 {
		t.Fatalf("backend called: %+v", session)
	}
}

func TestAgentExecutionOwnershipRevalidatedBeforeResume(t *testing.T) {
	c, runs, tasks, _, session, _ := successfulCoordinator()
	c.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation", BackendThreadID: "existing-thread", ContinuationSafe: true}}
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	runs.validateExecution = func(context.Context, run.Run) error {
		calls++
		if calls == 2 {
			return errors.New("conversation or owner became stale")
		}
		return nil
	}
	if _, err := executeDirectFeatureAreaRun(context.Background(), c, prepared); err == nil {
		t.Fatal("stale ownership authorized resume")
	}
	if session.startCalls != 0 || session.resumeCalls != 0 || session.turn != 0 || calls != 2 {
		t.Fatalf("backend called: %+v, validations=%d", session, calls)
	}
}

func TestSupervisorDoesNotCompleteOrRecoverUnprovenExecution(t *testing.T) {
	f := newRuntimeFixture(t, "task")
	defer f.supervisor.Shutdown()
	f.coordinator.Runs = unvalidatedRuns{f.runs}
	conversation := &ownershipRecoveryRecorder{}
	f.coordinator.Conversations = conversation
	done := make(chan struct{})
	f.supervisor.SetTerminalWake(func() { close(done) })
	taskValue, err := f.tasks.Get(context.Background(), "task")
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	reserved, err := f.supervisor.StartAuthorizedPlan(context.Background(), taskValue.ID, taskValue.WorkSpec, taskValue.VerificationSpec,
		func(ctx context.Context) (run.Run, error) { return f.runs.Create(ctx, taskValue.ID) },
		func(context.Context) (run.Run, error) {
			t.Error("reserved verification for unproven owner")
			return run.Run{}, errors.New("unexpected verification")
		},
		func(context.Context, executionproposal.Outcome, string, string) error { completed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("supervisor did not finish rejection")
	}
	stored, err := f.runs.Get(context.Background(), reserved.ID)
	if err != nil || stored.Status != run.StatusPreparing || stored.Failure != nil || completed || conversation.recoveryCalls != 0 {
		t.Fatalf("unproven owner acquired mutation authority: run=%+v err=%v completed=%t recovery=%d", stored, err, completed, conversation.recoveryCalls)
	}
	select {
	case <-f.factory.created:
		t.Fatal("unproven owner started backend")
	default:
	}
}

type ownershipRecoveryRecorder struct {
	recordingConversationRuntime
	recoveryCalls int
}

func (r *ownershipRecoveryRecorder) RequireRecovery(context.Context, string, string) error {
	r.recoveryCalls++
	return nil
}
