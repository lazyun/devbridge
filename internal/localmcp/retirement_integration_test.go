package localmcp_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestRetirementAutomaticallyConvergesThroughBothAdapters(t *testing.T) {
	for _, kind := range []string{"REVIEW", "EXECUTION"} {
		for _, unknown := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unknown=%v", kind, unknown), func(t *testing.T) {
				f := newIntegrationFixture(t)
				backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{}), unknown: unknown}
				f.plane.Coordinator.Codex = backend
				gpt, local := connectGPT(t, f.gptURL), connectLocalMCP(t, f.libreURL)
				sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
				area := closureArea(t, f, gpt, sid, "Retire "+kind)
				var taskID, runID string
				if kind == "REVIEW" {
					out := callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
					owner := out["review"].(map[string]any)["id"].(string)
					v, err := f.plane.Reviews.Get(f.ctx, owner)
					if err != nil {
						t.Fatal(err)
					}
					taskID, runID = v.TaskID, v.RunID
				} else {
					callToolMap(t, f.ctx, gpt, "featureArea_activate", map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "activate"})
					out := f.seedTaskExecution(t, validOwnershipTaskArgs(sid, area))
					taskID = out["id"].(string)
					runID = out["runId"].(string)
				}
				select {
				case <-backend.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("launch timeout")
				}
				args := map[string]any{"aiSessionId": sid, "featureAreaId": area, "requestId": "retire-once"}
				out := callToolMap(t, f.ctx, gpt, "featureArea_retire", args)
				if out["status"] != "RETIRING" || out["retirement"] == nil {
					t.Fatalf("intent=%+v", out)
				}
				// Pure direct store observations: no Refresh or second RETIRE drives closure.
				deadline := time.Now().Add(8 * time.Second)
				for {
					a, err := sqlite.NewFeatureAreaStore(f.plane.DB).Get(f.ctx, area)
					if err != nil {
						t.Fatal(err)
					}
					if a.Status == featurearea.StatusRetired {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("stuck=%+v retirement=%+v", a, a.Retirement)
					}
					time.Sleep(10 * time.Millisecond)
				}
				v, err := f.plane.Tasks.Get(f.ctx, taskID)
				if err != nil || string(v.Status) != "CANCELED" {
					t.Fatalf("task=%+v %v", v, err)
				}
				if backend.calls.Load() != 1 {
					t.Fatalf("interrupts=%d", backend.calls.Load())
				}
				disposition := "TERMINATED"
				if unknown {
					disposition = "UNKNOWN_ORPHANED"
				}
				execution, readErr := f.control.GetAgentExecutionForSession(f.ctx, sid, v.Work.ExecutionID)
				if readErr != nil || execution.BackendDisposition != disposition {
					t.Fatalf("work=%+v", v.Work)
				}
				replay := callToolMap(t, f.ctx, local, "featureArea_retire", args)
				if replay["status"] != "RETIRED" {
					t.Fatalf("replay=%+v", replay)
				}
				card := callToolMap(t, f.ctx, gpt, "feature_area_presentation_get", map[string]any{"aiSessionId": sid, "featureAreaId": area})
				if card["workflowStatus"] != "RETIRED" || card["retirement"] == nil {
					t.Fatalf("projection=%+v", card)
				}
				current, err := f.plane.Runs.Get(f.ctx, runID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = sqlite.NewRunStore(f.plane.DB).Update(runtimeepoch.WithFence(f.ctx, current.RuntimeEpoch, runID), runID, func(r *run.Run) error { r.Status = run.StatusSucceeded; return nil }); err == nil {
					t.Fatal("late callback regained authority")
				}
			})
		}
	}
}
