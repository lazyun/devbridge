package controlapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// This separately authorized real-model smoke creates only test-owned roots.
// It cannot answer a semantic question: an operator must author the exact
// UserDecisionInput in the printed fixture directory. No default home is read.
func TestRealCodexRecoveryStatus(t *testing.T) {
	if os.Getenv("DEVBRIDGE_REAL_CODEX_RECOVERY_STATUS") != "1" {
		t.Skip("REAL_CODEX_RECOVERY_STATUS_E2E_NOT_RUN")
	}
	native := os.Getenv("DEVBRIDGE_REAL_CODEX")
	files := map[string]string{"config.toml": os.Getenv("DEVBRIDGE_REAL_CODEX_RECOVERY_CONFIG_FILE"), "auth.json": os.Getenv("DEVBRIDGE_REAL_CODEX_RECOVERY_AUTH_FILE")}
	if !filepath.IsAbs(native) {
		t.Fatal("explicit absolute DEVBRIDGE_REAL_CODEX required")
	}
	home := t.TempDir()
	for name, path := range files {
		if !filepath.IsAbs(path) {
			t.Fatal("explicit isolated config/auth fixture required")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			t.Fatal("read explicitly selected fixture", err)
		}
		if err = os.WriteFile(filepath.Join(home, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DEVBRIDGE_USER_DECISION_REAL_FIXTURE", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "real-operator")
	factorySpec := func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: home, Env: []string{"HOME=" + home, "CODEX_HOME=" + home}}
	}
	var config controlplane.Config
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		var question agentruntime.Request
		var source run.Run
		deadline := time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) && question.ID == "" {
			values, e := f.plane.Runs.ListByTask(f.ctx, f.task)
			if e != nil {
				t.Fatal(e)
			}
			for _, v := range values {
				if v.AgentRuntime != nil {
					for _, q := range v.AgentRuntime.Requests {
						if q.Kind == "CODEX_REQUEST" && q.State == "WAITING_FOR_USER" {
							question, source = q, v
						}
					}
				}
			}
			if question.ID == "" {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if question.ID == "" {
			t.Fatal("real fixture did not ask; no answer generated")
		}
		requestPath, answerPath := filepath.Join(f.dataRoot, "recovery-smoke-question.json"), filepath.Join(f.dataRoot, "recovery-smoke-user-decision.json")
		raw, _ := json.MarshalIndent(question, "", "  ")
		if err := os.WriteFile(requestPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		t.Logf("Read %s and explicitly author exact UserDecisionInput at %s (SourceSurface REAL_SMOKE_OPERATOR, nonempty sourceUserEventId). No answer is generated.", requestPath, answerPath)
		answered := false
		deadline = time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			raw, e := os.ReadFile(answerPath)
			if os.IsNotExist(e) {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if e != nil {
				t.Fatal(e)
			}
			var in agentruntime.UserDecisionInput
			if json.Unmarshal(raw, &in) != nil || in.Identity != question.Identity || in.RuntimeRequestID != question.ID || in.Action != "RESPOND" || in.SourceSurface != "REAL_SMOKE_OPERATOR" || in.SourceUserEventID == "" {
				t.Fatal("operator answer identity/provenance mismatch")
			}
			in.PayloadSHA256 = agentruntime.DecisionPayloadSHA256(in)
			if _, e = f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in); e != nil {
				t.Fatal(e)
			}
			answered = true
			break
		}
		if !answered {
			t.Fatal("explicit user answer not supplied")
		}
		deadline = time.Now().Add(5 * time.Minute)
		terminal := false
		for time.Now().Before(deadline) {
			v, e := f.plane.Runs.Get(f.ctx, source.ID)
			if e != nil {
				t.Fatal(e)
			}
			source = v
			if v.AgentRuntime != nil && v.AgentRuntime.TurnStatus == "completed" && agentruntime.HasUnresolvedResponse(*v.AgentRuntime) {
				terminal = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !terminal {
			t.Fatal("source terminal + unknown receipt not observed")
		}
		binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, source.ID)
		if e != nil {
			t.Fatal(e)
		}
		if e = f.plane.Close(); e != nil {
			t.Fatal(e)
		}
		f.plane = nil
		f.plane, e = controlplane.Open(f.ctx, config)
		if e != nil {
			t.Fatal(e)
		}
		f.facade, e = controlapi.NewFromControlPlane(f.plane)
		if e != nil {
			t.Fatal(e)
		}
		deadline = time.Now().Add(time.Minute)
		for time.Now().Before(deadline) {
			source, e = f.plane.Runs.Get(f.ctx, source.ID)
			if e == nil && source.AgentRuntime != nil && source.AgentRuntime.RuntimeEpoch != question.RuntimeEpoch {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if source.RuntimeWorkset == nil || source.AgentRuntime == nil {
			t.Fatal("exact source runtime missing")
		}
		w := source.RuntimeWorkset
		src := recoverystatus.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: executionID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: source.AgentRuntime.AgentConversationID, ThreadID: source.ThreadID, AppServerInstanceID: binding.ID}
		current, e := f.plane.Tasks.Get(f.ctx, source.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		// Authored by this opt-in test caller, not by the Core recovery service.
		instruction := fmt.Sprintf("Report only the status of the completed original operation without editing any file or running repairs. Return the required structured status using sourceOperationKind=%q sourceOperationId=%q sourceRunId=%q sourceTurnId=%q. Use the exact approved goal identity %q, report all original app/sdk runtime members and actual changed paths. Do not answer or repeat any previous question. No commits, merges, new repositories, or scope changes.", src.OperationKind, src.OperationID, src.RunID, src.TurnID, current.WorkSpec.Goal)
		prepared, e := f.facade.PrepareRecoveryStatusForSession(f.ctx, f.session, recoverystatus.Spec{Source: src, RequestID: "real-recovery-smoke", RecoveryReason: "lost durable original receipt/result", Instruction: instruction, OutputSchemaVersion: "v1", Generation: 1})
		if e != nil {
			t.Fatal(e)
		}
		if prepared.StartCommand == nil {
			t.Fatal("start command unavailable")
		}
		result, e := f.facade.StartRecoveryStatusForSession(f.ctx, f.session, *prepared.StartCommand)
		if e != nil {
			t.Fatal(e)
		}
		deadline = time.Now().Add(5 * time.Minute)
		for time.Now().Before(deadline) {
			result, e = f.facade.ReadRecoveryStatusForSession(f.ctx, f.session, result.Record.ID)
			if e != nil {
				t.Fatal(e)
			}
			if result.Record.State == "ASSESSED" || result.Record.State == "FAILED" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if result.Projection.EngineeringDisposition != "RECOVERED_COMPLETE" || result.ResolveCommand == nil {
			t.Fatalf("real recovery not independently complete: %+v", result.Projection)
		}
		result, e = f.facade.ResolveRecoveryStatusForSession(f.ctx, f.session, *result.ResolveCommand)
		if e != nil {
			t.Fatal(e)
		}
		current, e = f.plane.Tasks.Get(f.ctx, source.TaskID)
		if e != nil || current.Phase != task.PhaseVerificationReady || result.Record.Spec.ThreadID != source.ThreadID || result.Record.RecoveryTurnID == source.TurnID || result.Record.SourceResponseDisposition != "UNKNOWN" {
			t.Fatal("canonical real recovery failed", e)
		}
		return false
	}, func(f *upperLifecycleFixture) {
		store := sqlite.NewAgentRuntimeStore(f.plane.DB)
		fault := &loseResolvedReceiptStore{AgentRuntimeStore: store}
		f.plane.Runs.AgentRuntime = agentruntime.NewService(fault)
		f.plane.Coordinator.AppServers = store
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: native, AgentRuntime: f.plane.Runs.AgentRuntime}, ContinuityDirectory: f.dataRoot, AppServers: store, AppServerSpec: factorySpec, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		config = controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: factorySpec}
		t.Cleanup(func() { stopContinuityFixtureServers(f) })
	})
}
