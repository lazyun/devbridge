package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type sequenceCollector struct {
	mu        sync.Mutex
	snapshots []string
	next      int
}

func awaitPlanSession(t *testing.T, fixture *runtimeFixture) *blockingCodexSession {
	t.Helper()
	select {
	case session := <-fixture.factory.created:
		select {
		case <-session.started:
			return session
		case <-time.After(2 * time.Second):
			t.Fatal("Codex plan turn did not start")
		}
	case <-time.After(2 * time.Second):
		runs, _ := fixture.runs.List(context.Background())
		failures := make([]string, len(runs))
		for i := range runs {
			if runs[i].Failure != nil {
				failures[i] = fmt.Sprintf("%s/%s/%s: %s", runs[i].Failure.Kind, runs[i].Failure.Stage, runs[i].Failure.Code, runs[i].Failure.Message)
			}
		}
		t.Fatalf("Codex plan session was not created; failures=%+v runs=%+v", failures, runs)
	}
	return nil
}

func awaitPlanPrompt(t *testing.T, session *blockingCodexSession) string {
	t.Helper()
	select {
	case prompt := <-session.prompts:
		return prompt
	case <-time.After(2 * time.Second):
		t.Fatal("Codex plan prompt was not recorded")
	}
	return ""
}

func (c *sequenceCollector) CaptureSnapshot(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.next >= len(c.snapshots) {
		return "", fmt.Errorf("unexpected snapshot %d", c.next)
	}
	value := c.snapshots[c.next]
	c.next++
	return value, nil
}

func (c *sequenceCollector) CollectRun(_ context.Context, path, base, start, end string) (evidence.GitEvidence, error) {
	changed := []string(nil)
	if start != end {
		changed = []string{"change.go"}
	}
	return evidence.GitEvidence{BaseCommit: base, HeadCommit: base, StartSnapshot: start, EndSnapshot: end, Dirty: true, ChangedFiles: changed, Diff: path}, nil
}

func planSpecs() (task.WorkSpec, task.VerificationSpec) {
	return task.WorkSpec{Goal: "implement", CodexInstruction: "Make the implementation change exactly as specified.", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, task.VerificationSpec{
		Mode: task.VerificationModeRun, Scope: "Go packages", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}},
		AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "Run the confirmed tests without modifying files.",
	}
}

func TestAuthorizedPlanSerializesImplementationAndVerificationWithContinuity(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	fixture.collector = nil
	fixture.coordinator.Evidence = &sequenceCollector{snapshots: []string{fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2)}}
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		if id == 2 {
			session.message = reportJSON("PASSED", "PASSED")
			session.commands = []ObservedCommand{{Command: "go test ./...", ExitCode: exitCode(0), Output: "ok"}}
			session.usage = &run.TokenUsageObservation{Reliable: true, StartCumulative: run.TokenCounts{TotalTokens: 110}, EndCumulative: run.TokenCounts{InputTokens: 2, OutputTokens: 2, TotalTokens: 130}}
		}
		fixture.factory.created <- session
		return session
	}
	work, verification := planSpecs()
	fixture.authorizePlan(t, "task-a", work, verification, "execution-a")
	completed := make(chan executionproposal.Outcome, 1)
	started, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-a")
		},
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeVerification, "execution-a")
		},
		func(ctx context.Context, outcome executionproposal.Outcome, _, _ string) error {
			completed <- outcome
			// Runtime completion does not accept the Task.
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if started.Purpose != run.PurposeImplementation {
		t.Fatalf("implementation run=%+v", started)
	}
	implementationSession := awaitPlanSession(t, fixture)
	if prompt := awaitPlanPrompt(t, implementationSession); prompt != work.CodexInstruction {
		t.Fatalf("implementation prompt did not preserve instruction: %q", prompt)
	}
	select {
	case extra := <-fixture.factory.created:
		t.Fatalf("verification started before implementation completed: %+v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	close(implementationSession.release)
	verificationSession := awaitPlanSession(t, fixture)
	if prompt := awaitPlanPrompt(t, verificationSession); prompt != verification.CodexInstruction {
		t.Fatalf("verification prompt lost confirmed contract: %q", prompt)
	}
	runs, err := fixture.runs.List(context.Background())
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	implementation, verificationRun := runs[0], runs[1]
	if implementation.Status != run.StatusSucceeded || implementation.Purpose != run.PurposeImplementation || verificationRun.Purpose != run.PurposeVerification || implementation.ID == verificationRun.ID || implementation.TurnID == verificationRun.TurnID || implementation.WorkspaceID != verificationRun.WorkspaceID || implementation.ThreadID != verificationRun.ThreadID {
		t.Fatalf("serial continuity implementation=%+v verification=%+v", implementation, verificationRun)
	}
	if implementation.TokenUsage.Usage == nil || implementation.TokenUsage.Usage.TotalTokens != 10 {
		t.Fatalf("implementation usage=%+v", implementation.TokenUsage)
	}
	close(verificationSession.release)
	select {
	case outcome := <-completed:
		if outcome != executionproposal.OutcomeSucceeded {
			t.Fatalf("outcome=%s", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("plan did not complete")
	}
	runs, _ = fixture.runs.List(context.Background())
	verificationRun = runs[1]
	if verificationRun.Status != run.StatusSucceeded || verificationRun.VerificationReport != nil || verificationRun.ResultProtocol != run.NaturalResultProtocol || verificationRun.TokenUsage.Usage == nil || verificationRun.TokenUsage.Usage.TotalTokens != 20 || verificationRun.TokenUsage.StartCumulative.TotalTokens != 110 {
		t.Fatalf("verification result=%+v", verificationRun)
	}
}

func TestImplementationFailurePreventsVerification(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		session.turnErr = &TaskExecutionError{Err: errors.New("implementation rejected")}
		fixture.factory.created <- session
		return session
	}
	work, verification := planSpecs()
	fixture.authorizePlan(t, "task-a", work, verification, "execution-implementation-failed")
	var verificationReservations atomic.Int32
	completed := make(chan string, 1)
	_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-implementation-failed")
		},
		func(context.Context) (run.Run, error) {
			verificationReservations.Add(1)
			return run.Run{}, errors.New("must not reserve verification")
		},
		func(_ context.Context, outcome executionproposal.Outcome, code, _ string) error {
			completed <- string(outcome) + ":" + code
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	implementation := awaitBlockingSession(t, fixture.factory)
	close(implementation.release)
	select {
	case result := <-completed:
		if result != "FAILED:EXECUTION_FAILED" {
			t.Fatalf("completion=%s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed implementation plan did not complete")
	}
	if verificationReservations.Load() != 0 {
		t.Fatalf("verification reservations=%d", verificationReservations.Load())
	}
	runs, _ := fixture.runs.List(context.Background())
	if len(runs) != 1 || runs[0].Purpose != run.PurposeImplementation || runs[0].Status != run.StatusFailed {
		t.Fatalf("runs=%+v", runs)
	}
}

func TestImplementationDiscoveredPathsReachVerification(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	fixture.worktrees.seedPaths = []string{"allowed", "outside/protected.txt"}
	fixture.coordinator.Evidence = &sequenceCollector{snapshots: []string{fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2)}}
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		fixture.factory.created <- session
		return session
	}
	work, verification := planSpecs()
	work.ExecutionScope = task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"allowed"}}
	fixture.authorizePlan(t, "task-a", work, verification, "execution-out-of-scope")
	var verificationReservations atomic.Int32
	completed := make(chan string, 1)
	_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-out-of-scope")
		},
		func(context.Context) (run.Run, error) {
			verificationReservations.Add(1)
			return run.Run{}, errors.New("synthetic verification reservation reached")
		},
		func(_ context.Context, outcome executionproposal.Outcome, code, message string) error {
			completed <- string(outcome) + ":" + code + ":" + message
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	implementation := awaitBlockingSession(t, fixture.factory)
	close(implementation.release)
	select {
	case result := <-completed:
		if strings.Contains(result, "OUT_OF_SCOPE_CHANGE") || !strings.Contains(result, "synthetic verification reservation reached") {
			t.Fatalf("completion=%s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("implementation with discovered paths did not complete")
	}
	if verificationReservations.Load() != 1 {
		t.Fatalf("verification reservations=%d", verificationReservations.Load())
	}
	runs, _ := fixture.runs.List(context.Background())
	if len(runs) != 1 || runs[0].Status != run.StatusSucceeded || runs[0].Failure != nil {
		t.Fatalf("runs=%+v", runs)
	}
}

func TestVerificationFailureFailsPlanWithoutRepair(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	fixture.coordinator.Evidence = &sequenceCollector{snapshots: []string{fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2)}}
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		if id == 2 {
			session.message = reportJSON("PASSED", "PASSED")
			session.commands = []ObservedCommand{{Command: "go test ./...", ExitCode: exitCode(1), Output: "FAIL"}}
		}
		fixture.factory.created <- session
		return session
	}
	work, verification := planSpecs()
	fixture.authorizePlan(t, "task-a", work, verification, "execution-verification-failed")
	completed := make(chan string, 1)
	_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-verification-failed")
		},
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeVerification, "execution-verification-failed")
		},
		func(_ context.Context, outcome executionproposal.Outcome, code, _ string) error {
			completed <- string(outcome) + ":" + code
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	implementation := awaitBlockingSession(t, fixture.factory)
	close(implementation.release)
	verificationSession := awaitBlockingSession(t, fixture.factory)
	close(verificationSession.release)
	select {
	case result := <-completed:
		if result != "SUCCEEDED:" {
			t.Fatalf("completion=%s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("failed verification plan did not complete")
	}
	select {
	case extra := <-fixture.factory.created:
		t.Fatalf("unexpected automatic repair Run: %+v", extra)
	case <-time.After(30 * time.Millisecond):
	}
	runs, _ := fixture.runs.List(context.Background())
	if len(runs) != 2 || runs[1].Status != run.StatusSucceeded || runs[1].VerificationReport != nil || runs[1].ResultProtocol != run.NaturalResultProtocol {
		t.Fatalf("runs=%+v", runs)
	}

	// A failed observed check is report evidence and deliberately does not
	// rewrite the accepted plan outcome. A runtime turn failure is different:
	// it must fail the plan, still without scheduling an implicit repair Run.
	t.Run("verification runtime failure", func(t *testing.T) {
		fixture := newRuntimeFixture(t, "task-a")
		defer fixture.supervisor.Shutdown()
		fixture.coordinator.Evidence = &sequenceCollector{snapshots: []string{fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2)}}
		fixture.factory.newValue = func(id int64) CodexSession {
			session := newBlockingCodexSession(id)
			if id == 2 {
				session.turnErr = &TaskExecutionError{Err: errors.New("verification runtime rejected")}
			}
			fixture.factory.created <- session
			return session
		}
		work, verification := planSpecs()
		fixture.authorizePlan(t, "task-a", work, verification, "execution-verification-runtime-failed")
		completed := make(chan string, 1)
		_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
			func(ctx context.Context) (run.Run, error) {
				return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-verification-runtime-failed")
			},
			func(ctx context.Context) (run.Run, error) {
				return fixture.reserveExecution(ctx, "task-a", run.PurposeVerification, "execution-verification-runtime-failed")
			},
			func(_ context.Context, outcome executionproposal.Outcome, code, _ string) error {
				completed <- string(outcome) + ":" + code
				return nil
			})
		if err != nil {
			t.Fatal(err)
		}
		implementation := awaitPlanSession(t, fixture)
		close(implementation.release)
		verificationSession := awaitPlanSession(t, fixture)
		close(verificationSession.release)
		select {
		case result := <-completed:
			if result != "FAILED:EXECUTION_FAILED" {
				t.Fatalf("completion=%s", result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("runtime-failed verification plan did not complete")
		}
		select {
		case extra := <-fixture.factory.created:
			t.Fatalf("unexpected automatic repair Run: %+v", extra)
		case <-time.After(30 * time.Millisecond):
		}
		runs, _ := fixture.runs.List(context.Background())
		if len(runs) != 2 || runs[1].Status != run.StatusFailed || runs[1].VerificationReport != nil {
			t.Fatalf("runs=%+v", runs)
		}
	})
}

func TestAuthorizedPlanNoneCreatesNoVerificationRun(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	work, _ := planSpecs()
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User explicitly waived tests"}}
	fixture.authorizePlan(t, "task-a", work, verification, "execution-none")
	completed := make(chan executionproposal.Outcome, 1)
	_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-none")
		},
		func(context.Context) (run.Run, error) {
			return run.Run{}, fmt.Errorf("synthetic verification reservation reached")
		},
		func(ctx context.Context, outcome executionproposal.Outcome, _, _ string) error {
			completed <- outcome
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	session := awaitBlockingSession(t, fixture.factory)
	close(session.release)
	select {
	case outcome := <-completed:
		if outcome != executionproposal.OutcomeSucceeded {
			t.Fatalf("outcome=%s", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waived plan did not complete")
	}
	// Plan completion terminalizes the Execution transport only; Task success
	// is committed exclusively by the acceptance/delivery owner, so the Task
	// keeps its ACTIVE lifecycle here.
	if got, getErr := fixture.tasks.Get(context.Background(), "task-a"); getErr != nil || got.Status != task.StatusActive || got.Phase != task.PhaseImplementing {
		t.Fatalf("completed plan wrote Task lifecycle: task=%+v err=%v", got, getErr)
	}
	runs, _ := fixture.runs.List(context.Background())
	if len(runs) != 1 || runs[0].Purpose != run.PurposeImplementation {
		t.Fatalf("waived verification created runs: %+v", runs)
	}
}

func TestVerificationMutationFailsPlan(t *testing.T) {
	fixture := newRuntimeFixture(t, "task-a")
	defer fixture.supervisor.Shutdown()
	fixture.coordinator.Evidence = &sequenceCollector{snapshots: []string{fmt.Sprintf("%040x", 1), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 2), fmt.Sprintf("%040x", 3)}}
	fixture.factory.newValue = func(id int64) CodexSession {
		session := newBlockingCodexSession(id)
		if id == 2 {
			session.message = "ordinary verification prose"
			session.commands = []ObservedCommand{{Command: "go test ./...", ExitCode: exitCode(0)}}
		}
		fixture.factory.created <- session
		return session
	}
	work, verification := planSpecs()
	fixture.authorizePlan(t, "task-a", work, verification, "execution-mutated")
	completed := make(chan string, 1)
	_, err := fixture.supervisor.StartAuthorizedPlan(context.Background(), "task-a", work, verification,
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeImplementation, "execution-mutated")
		},
		func(ctx context.Context) (run.Run, error) {
			return fixture.reserveExecution(ctx, "task-a", run.PurposeVerification, "execution-mutated")
		},
		func(_ context.Context, outcome executionproposal.Outcome, code, _ string) error {
			completed <- string(outcome) + ":" + code
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	implementation := awaitBlockingSession(t, fixture.factory)
	close(implementation.release)
	verificationSession := awaitBlockingSession(t, fixture.factory)
	close(verificationSession.release)
	select {
	case result := <-completed:
		if result != "FAILED:VERIFICATION_MUTATED_WORKTREE" {
			t.Fatalf("result=%s", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mutation plan did not complete")
	}
	runs, _ := fixture.runs.List(context.Background())
	verificationRun := runs[1]
	if verificationRun.Status != run.StatusInfrastructureFailed || verificationRun.VerificationReport != nil {
		t.Fatalf("verification mutation run=%+v", verificationRun)
	}
}
