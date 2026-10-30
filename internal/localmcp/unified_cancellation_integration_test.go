package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// Drives both public adapters into the production supervisor and real SQLite /
// Git workspace machinery. The fixture backend controls exact terminal vs lost
// transport. No paid Codex invocation and no physical-death assumption.
type unifiedBackend struct {
	*stubCodexSession
	fixture *integrationFixture
	entered chan struct{}
	finish  chan struct{}
	once    sync.Once
	calls   atomic.Int32
	unknown bool
}

func (s *unifiedBackend) Start(ctx context.Context, _ codexconfig.Config) (execution.CodexSession, error) {
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok {
		return nil, errors.New("launch lacks runtime identity")
	}
	r, err := s.fixture.plane.Runs.Get(ctx, fence.RunID)
	if err != nil {
		return nil, err
	}
	v, err := s.fixture.plane.Tasks.Get(ctx, r.TaskID)
	if err != nil || v.ID == "" || v.Work.RunID != r.ID {
		return nil, fmt.Errorf("launch before visible Task: %+v %v", v, err)
	}
	return s, nil
}
func (s *unifiedBackend) StartTurnWithStarted(ctx context.Context, thread, prompt string, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.stubCodexSession.StartTurn(ctx, thread, prompt)
	if err != nil {
		return result, err
	}
	if err = started(result.TurnID); err != nil {
		return result, err
	}
	close(s.entered)
	select {
	case <-s.finish:
		return result, &codex.TurnError{ThreadID: thread, TurnID: result.TurnID, Status: "interrupted"}
	case <-ctx.Done():
		return result, ctx.Err()
	}
}
func (s *unifiedBackend) InterruptTurn(context.Context, string, string) error {
	if s.calls.Add(1) != 1 {
		return errors.New("duplicate destructive dispatch")
	}
	if s.unknown {
		return errors.New("fixture transport loss")
	}
	s.once.Do(func() { close(s.finish) })
	return nil
}
func (s *unifiedBackend) Close() error { return nil }

func TestUnifiedReviewDevelopmentCancellationThroughAdapters(t *testing.T) {
	for _, kind := range []string{"REVIEW", "EXECUTION"} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", kind, unknown), func(t *testing.T) {
				f := newIntegrationFixture(t)
				backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{}), unknown: unknown}
				f.plane.Coordinator.Codex = backend
				gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
				sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
				area := closureArea(t, f, gpt, sid, "Unified "+kind)
				var taskID, ownerID, runID string
				if kind == "REVIEW" {
					out := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
					ownerID = out["review"].(map[string]any)["id"].(string)
					v, err := f.plane.Reviews.Get(f.ctx, ownerID)
					if err != nil {
						t.Fatal(err)
					}
					taskID, runID = v.TaskID, v.RunID
				} else {
					callToolMap(t, f.ctx, gpt, "featureArea_activate", map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "activate"})
					out := f.seedTaskExecution(t, validOwnershipTaskArgs(sid, area))
					taskID = out["id"].(string)
					ownerID, runID = out["executionId"].(string), out["runId"].(string)
				}
				select {
				case <-backend.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("Agent admission did not finish")
				}
				live, ok := f.plane.Supervisor.GetSession(runID)
				if !ok {
					t.Fatal("missing live owner")
				}
				before, err := f.plane.Tasks.Get(f.ctx, taskID)
				if err != nil || before.Work.Phase != map[string]string{"REVIEW": "REVIEWING", "EXECUTION": "IMPLEMENTING"}[kind] || before.Work.Status != "ACTIVE" {
					t.Fatalf("active work=%+v %v", before, err)
				}
				inventory := callToolMap(t, f.ctx, local, "work_list", map[string]any{"aiSessionId": sid, "kind": "TASK"})
				if len(inventory["result"].(map[string]any)["tasks"].([]any)) != 1 {
					t.Fatalf("work inventory=%+v", inventory)
				}

				web, err := bridgehttp.New(f.plane, false)
				if err != nil {
					t.Fatal(err)
				}
				observed, err := f.plane.Runs.Get(f.ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				accepted := cancelHTTP(t, web.Handler(), f.projectID, observed.AgentExecutionID, runID, "cancel-exact")
				if accepted.OwnerType != kind || accepted.OwnerID != ownerID {
					t.Fatalf("HTTP universal owner mismatch: %+v", accepted)
				}
				waitCtx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
				defer cancel()
				_, waitErr := live.Wait(waitCtx)
				if errors.Is(waitErr, context.DeadlineExceeded) {
					t.Fatal(waitErr)
				}
				first, err := sqlite.NewExecutionCancellationStore(f.plane.DB).Get(f.ctx, f.projectID, accepted.RequestID)
				if err != nil || first.State != "CANCELED" || backend.calls.Load() != 1 {
					t.Fatalf("receipt=%+v err=%v calls=%d", first, err, backend.calls.Load())
				}
				disposition, quiescence := "TERMINATED", "QUIESCED"
				if unknown {
					disposition, quiescence = "UNKNOWN_ORPHANED", "UNKNOWN"
				}
				if first.BackendDisposition != disposition || first.QuiescenceState != quiescence {
					t.Fatalf("boundary=%+v", first)
				}
				after, err := f.plane.Tasks.Get(f.ctx, taskID)
				expectedPhase := before.Phase
				if kind == "REVIEW" {
					expectedPhase = task.PhaseReviewRequired
				} else if unknown {
					// Unknown cancellation outcome is a frozen recovery entry;
					// a known TERMINATED cancellation preserves the active Phase.
					expectedPhase = task.PhaseRecoveryRequired
				}
				if err != nil || after.Status != task.StatusActive || after.Phase != expectedPhase || after.CurrentDefinitionRevisionID != before.CurrentDefinitionRevisionID {
					t.Fatalf("terminal Task=%+v %v", after, err)
				}
				// The HTTP operator request owns the cancellation identity. Read its
				// exact receipt instead of submitting the same user request through a
				// different AI-session principal.
				replay, err := sqlite.NewExecutionCancellationStore(f.plane.DB).Get(f.ctx, f.projectID, accepted.RequestID)
				if err != nil || !reflect.DeepEqual(first, replay) {
					t.Fatalf("exact receipt readback changed: %+v %v", replay, err)
				}
				current, err := f.plane.Runs.Get(f.ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = sqlite.NewRunStore(f.plane.DB).Update(runtimeepoch.WithFence(f.ctx, current.RuntimeEpoch, current.ID), current.ID, func(v *run.Run) error { v.Status = run.StatusSucceeded; return nil }); err == nil {
					t.Fatal("late success regained authority")
				}
			})
		}
	}
}

func (s *unifiedBackend) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.StartTurnWithStarted(ctx, thread, prompt, started)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
