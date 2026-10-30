package controlapi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/task"
)

// This test never supplies a semantic answer. The operator must copy the exact
// observed identity and explicitly author each decision in the printed file.
func TestRealCodexRequestUserDecision(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_CODEX_REQUEST") != "1" {
		t.Skip("REAL_CODEX_REQUEST_E2E_NOT_RUN")
	}
	if os.Getenv("DEVBRIDGE_REAL_CODEX") == "" {
		t.Fatal("DEVBRIDGE_REAL_CODEX must name the explicitly selected real executable")
	}
	t.Setenv("DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "real-operator")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		requestPath := filepath.Join(f.dataRoot, "operator-runtime-request.json")
		answerPath := filepath.Join(f.dataRoot, "operator-user-decision.json")
		t.Logf("Isolated managed fixture. Read current raw request at %s. Explicitly author UserDecisionInput at %s; no preselected answer or automatic response is provided.", requestPath, answerPath)
		seen := map[string]bool{}
		answered := false
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			runs, err := f.plane.Runs.ListByTask(f.ctx, f.task)
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range runs {
				if value.AgentRuntime == nil {
					continue
				}
				for _, request := range value.AgentRuntime.Requests {
					if request.Kind != "CODEX_REQUEST" || request.State != "WAITING_FOR_USER" || seen[request.ID] {
						continue
					}
					raw, _ := json.MarshalIndent(request, "", "  ")
					if err = os.WriteFile(requestPath, raw, 0600); err != nil {
						t.Fatal(err)
					}
					data, e := os.ReadFile(answerPath)
					if os.IsNotExist(e) {
						continue
					}
					if e != nil {
						t.Fatal(e)
					}
					var decision agentruntime.UserDecisionInput
					if err = json.Unmarshal(data, &decision); err != nil {
						t.Fatal(err)
					}
					if decision.RuntimeRequestID != request.ID || decision.Identity != request.Identity || decision.SourceSurface != "REAL_SMOKE_OPERATOR" || decision.SourceUserEventID == "" {
						t.Fatal("operator input must reference exact current request and explicit user event")
					}
					decision.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(decision)
					if _, err = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, decision); err != nil {
						t.Fatal(err)
					}
					seen[request.ID] = true
					answered = true
					if err = os.Remove(answerPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			current, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil {
				t.Fatal(err)
			}
			if current.Phase == task.PhaseAcceptancePending {
				if !answered {
					t.Fatal("real backend completed without an operator-answered CODEX_REQUEST; capability not proven")
				}
				return false
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("real request/operator answer/completion not observed within bounded smoke")
		return false
	})
}
