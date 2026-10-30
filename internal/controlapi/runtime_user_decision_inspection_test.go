package controlapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestInspectionUserDecisionAppServerHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_INSPECTION_DECISION_HELPER") != "1" {
		return
	}
	write := func(v any) { b, _ := json.Marshal(v); fmt.Fprintln(os.Stdout, string(b)) }
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(81)
		}
		switch req.Method {
		case "config/read":
			write(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": req.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}})
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "inspection decision fixture"}})
		case "initialized":
		case "thread/start":
			write(map[string]any{"id": req.ID, "result": map[string]any{"thread": map[string]any{"id": "thread-coding-v1"}}})
		case "thread/name/set", "thread/inject_items":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "turn/start":
			write(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-inspection", "status": "inProgress", "items": []any{}}}})
			if !codingFixtureAskUser(scanner, "turn-inspection", write, func(string, json.RawMessage) {}) {
				continue
			}
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turnId": "turn-inspection", "item": map[string]any{"id": "inspection-result", "type": "agentMessage", "text": "Inspection completed after explicit user response", "phase": "final_answer"}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-coding-v1", "turn": map[string]any{"id": "turn-inspection", "status": "completed", "items": []any{}}}})
		default:
			os.Exit(82)
		}
	}
	os.Exit(0)
}

type inspectionDecisionFactory struct{ config codex.Config }

func (f inspectionDecisionFactory) Start(ctx context.Context, selected codexconfig.Config) (probeexecution.CodexSession, error) {
	client, err := codex.Start(ctx, f.config)
	if err != nil {
		return nil, err
	}
	return inspectionDecisionSession{client: client}, nil
}

// This transport fixture explicitly selects its model; the legacy probe input
// has no model field and must not weaken the production missing-model guard.
type inspectionDecisionSession struct{ client *codex.Client }

func (s inspectionDecisionSession) StartThread(ctx context.Context, cwd string) (string, error) {
	thread, err := s.client.StartThread(ctx, codex.ThreadOptions{CWD: cwd, Model: "gpt-5.6-terra"})
	return thread.ID, err
}
func (s inspectionDecisionSession) StartTurn(ctx context.Context, thread, prompt string) (probeexecution.CodexTurnResult, error) {
	return s.StartTurnWithStarted(ctx, thread, prompt, nil)
}
func (s inspectionDecisionSession) StartTurnWithStarted(ctx context.Context, thread, prompt string, onStarted func(string) error) (probeexecution.CodexTurnResult, error) {
	fence, _ := runtimeepoch.FromContext(ctx)
	result, err := s.client.StartTurn(ctx, codex.TurnOptions{RunID: fence.RunID, ThreadID: thread, Prompt: prompt, Model: "gpt-5.6-terra", AuthorizationCeiling: codex.AuthorizationReadOnly, OnStarted: func(_ string, turn string) error {
		if onStarted != nil {
			return onStarted(turn)
		}
		return nil
	}})
	return probeexecution.CodexTurnResult{TurnID: result.TurnID, Message: result.FinalMessage}, err
}
func (s inspectionDecisionSession) InterruptTurn(ctx context.Context, thread, turn string) error {
	return s.client.InterruptTurn(ctx, thread, turn)
}
func (s inspectionDecisionSession) Close() error { return s.client.Close() }

func TestRuntimeUserDecisionProductionInspection(t *testing.T) {
	for _, action := range []string{"RESPOND", "CANCEL_OPERATION", "STOP_AND_REVISE"} {
		t.Run(action, func(t *testing.T) {
			f := newUpperLifecycleFixture(t)
			t.Setenv("DEVBRIDGE_INSPECTION_DECISION_HELPER", "1")
			launcher := filepath.Join(t.TempDir(), "fake-inspection")
			executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestInspectionUserDecisionAppServerHelper$\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runner, err := probeexecution.NewRunner(f.plane.ContextProbes, f.plane.Repositories, f.plane.CodexConfigs, inspectionDecisionFactory{config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}})
			if err != nil {
				t.Fatal(err)
			}
			runner.Cancellations = f.plane.ExecutionCancellations
			f.plane.ContextProbeRunner.Shutdown()
			f.plane.ContextProbeRunner = runner
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			probe, err := f.facade.CreateContextProbe(f.ctx, contextprobe.CreateInput{AISessionID: f.session, RepositoryID: f.repos["app"], CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "Read project guidance, ask explicit user before choosing adaptation; never mutate"})
			if err != nil {
				t.Fatal(err)
			}
			probe, err = f.facade.StartContextProbe(f.ctx, f.session, probe.ID)
			if err != nil {
				t.Fatal(err)
			}
			var request agentruntime.Request
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				state, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, probe.RunID)
				if e != nil {
					t.Fatal(e)
				}
				for _, r := range state.Requests {
					if r.Kind == "CODEX_REQUEST" && r.State == "WAITING_FOR_USER" {
						request = r
					}
				}
				if request.ID != "" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if request.ID == "" {
				current, _ := f.facade.GetContextProbeForSession(f.ctx, f.session, probe.ID)
				t.Fatalf("inspection question missing: %+v", current)
			}
			if request.OperationKind != "INSPECTION" || request.OperationID != probe.ID || request.AgentConversationID != "" {
				t.Fatalf("invented owner/conversation %+v", request.Identity)
			}
			time.Sleep(40 * time.Millisecond)
			state, err := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, probe.RunID)
			if err != nil || state.Requests[0].UserDecision != nil {
				t.Fatalf("automatic decision %+v %v", state, err)
			}
			answer := ""
			if action == "RESPOND" {
				answer = "no"
			}
			in := explicitDecision(t, f, request, action, answer)
			result, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
			if err != nil {
				t.Fatalf("submit %+v %v", result, err)
			}
			if result.Decision.ID == "" {
				t.Fatal("missing durable decision")
			}
			deadline = time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				probe, err = f.facade.GetContextProbeForSession(f.ctx, f.session, probe.ID)
				if err != nil {
					t.Fatal(err)
				}
				if probe.Status != contextprobe.StatusRunning {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			want := contextprobe.StatusSucceeded
			if action != "RESPOND" {
				want = contextprobe.StatusCanceled
			}
			if probe.Status != want {
				t.Fatalf("probe %+v want %s", probe, want)
			}
			replay, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, in)
			if err != nil || replay.Decision.ID != result.Decision.ID {
				t.Fatalf("replay %+v %v", replay, err)
			}
			if action != "RESPOND" {
				runtimeState, e := f.facade.AgentRuntimeStateForSession(f.ctx, f.session, probe.RunID)
				if e != nil || len(runtimeState.Requests) != 1 || runtimeState.Requests[0].State != "CANCELED" {
					t.Fatalf("canceled question %+v %v", runtimeState, e)
				}
				receipts, e := f.plane.ExecutionCancellations.List(f.ctx, f.project, probe.ID)
				if e != nil || len(receipts) != 1 || receipts[0].State != "CANCELED" || receipts[0].QuiescenceState != "QUIESCED" {
					t.Fatalf("cancellation %+v %v", receipts, e)
				}
			}
		})
	}
}
