package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Only the HTTP response is lost: the actual Local handler, confirmation owner,
// SQLite admission and runtime handoff finish before the client sees the fault.
// No test substitute supplies an approval, selection or consumed receipt.
type approvedStartLostResponse struct {
	base  http.RoundTripper
	armed atomic.Bool
	lost  chan []byte
}

func (r *approvedStartLostResponse) RoundTrip(req *http.Request) (*http.Response, error) {
	drop := false
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
		var call struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if json.Unmarshal(body, &call) == nil && call.Method == "tools/call" && call.Params.Name == "execution_start" {
			drop = r.armed.CompareAndSwap(true, false)
		}
	}
	response, err := r.base.RoundTrip(req)
	if err != nil || !drop {
		return response, err
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	r.lost <- body
	return nil, errors.New("test: execution_start response lost after real handler completion")
}

type approvedStartLocalClient struct {
	t       *testing.T
	session *sdkmcp.ClientSession
	fault   *approvedStartLostResponse
	close   func()
}

func newApprovedStartLocalClient(t *testing.T, f *upperLifecycleFixture) *approvedStartLocalClient {
	t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "approved-start-local-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := localmcp.New(localmcp.Config{RequestTimeout: 30 * time.Second}, service)
	if err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(adapter.Handler())
	fault := &approvedStartLostResponse{base: host.Client().Transport, lost: make(chan []byte, 1)}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "approved-start-local-client", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &sdkmcp.StreamableClientTransport{Endpoint: host.URL + localmcp.MCPPath, HTTPClient: &http.Client{Transport: fault, Timeout: 35 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		host.Close()
		t.Fatal(err)
	}
	out := &approvedStartLocalClient{t: t, session: session, fault: fault, close: func() { _ = session.Close(); host.Close() }}
	t.Cleanup(out.close)
	return out
}
func (s *approvedStartLocalClient) call(name string, args map[string]any, wantError bool) []byte {
	s.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, err := s.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		s.t.Fatalf("%s transport: %v", name, err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		s.t.Fatal(err)
	}
	if result.IsError != wantError {
		s.t.Fatalf("%s isError=%v want=%v content=%+v structured=%s", name, result.IsError, wantError, result.Content, raw)
	}
	return raw
}
func decodeApprovedLocalStart(t *testing.T, raw []byte) executionproposal.StartResult {
	t.Helper()
	var out struct {
		Result executionproposal.StartResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.Proposal.ID == "" || out.Result.Run.ID == "" {
		t.Fatalf("start omitted durable identities: %s", raw)
	}
	return out.Result
}

// Setup performs an actual Coding Review and NO_START approval. Everything
// under test then enters through Local MCP HTTP, including Session creation,
// Project binding, scoped options/target reads and the explicit start command.
func TestApprovedTaskStartThroughLocalMCP(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, prior review.Review) {
		t := f.t
		alternate, wire := definitionOnlyAlternateBackend(t, f)
		client := newApprovedStartLocalClient(t, f)
		var initialized mcpserver.SessionInitializeOutput
		if err := json.Unmarshal(client.call("session_initialize", map[string]any{"requestId": "local-explicit-start-session"}, false), &initialized); err != nil {
			t.Fatal(err)
		}
		sessionID := initialized.Result.AISessionID
		if sessionID == "" || sessionID == f.session {
			t.Fatal("Local did not create its own real caller-scoped Session")
		}
		client.call("project_open", map[string]any{"aiSessionId": sessionID, "projectId": f.project, "requestId": "local-explicit-start-project"}, false)
		var workspace struct {
			Result struct {
				TaskView struct {
					ExecutionStartTarget *wp.TaskExecutionExpectation `json:"executionStartTarget"`
				} `json:"taskView"`
				RuntimeOptions *struct {
					RuntimeOptions    []codexruntime.RuntimeOption   `json:"runtimeOptions"`
					BackendCandidates []codexconfig.BackendSelection `json:"backendCandidates"`
				} `json:"runtimeOptions"`
			} `json:"result"`
		}
		raw := client.call("workspace_get", map[string]any{"aiSessionId": sessionID, "taskId": current.ID, "include": []string{"TASK", "RUNTIME_CONFIG"}}, false)
		if err := json.Unmarshal(raw, &workspace); err != nil {
			t.Fatal(err)
		}
		target := workspace.Result.TaskView.ExecutionStartTarget
		options := workspace.Result.RuntimeOptions
		if target == nil || target.ExecutionID != "" || target.PrimaryMemberID == "" || target.ApprovedWorksetHash == "" || options == nil {
			t.Fatalf("Local cannot obtain complete approved start facts: %s", raw)
		}
		selected := differentDefinitionRuntime(t, c.Options{RuntimeOptions: options.RuntimeOptions}, prior.RuntimeConfig)
		var backend codexconfig.BackendSelectionInput
		for _, candidate := range options.BackendCandidates {
			if candidate.ConfigID == alternate.ID {
				backend = candidate.Input()
			}
		}
		if backend.ConfigID == "" || backend.ConfigID == prior.BackendSelection.ConfigID {
			t.Fatalf("Local scoped options omitted distinct registered backend: %s", raw)
		}
		command := map[string]any{"aiSessionId": sessionID, "taskId": current.ID, "requestId": "local-explicit-start", "expectedTarget": target, "runtimeConfig": map[string]any{"model": selected.Model, "reasoningEffort": selected.ReasoningEffort, "codexFastMode": selected.CodexFastMode}, "backend": backend}
		before := definitionRuntimeSnapshot(t, f, true)
		client.call("execution_start", map[string]any{"aiSessionId": sessionID, "taskId": current.ID}, true)
		for _, missing := range []string{"runtimeConfig", "backend", "requestId", "expectedTarget"} {
			partial := map[string]any{}
			for key, value := range command {
				if key != missing {
					partial[key] = value
				}
			}
			client.call("execution_start", partial, true)
		}
		wrongPhase := *target
		wrongPhase.Phase = task.PhaseVerificationReady
		wrongPhase.ExecutionID = "not-an-existing-execution"
		invalid := map[string]any{}
		for key, value := range command {
			invalid[key] = value
		}
		invalid["expectedTarget"] = wrongPhase
		client.call("execution_start", invalid, true)
		if after := definitionRuntimeSnapshot(t, f, true); after != before {
			t.Fatalf("rejected Local starts wrote facts:\n%s", definitionRuntimeSnapshotDiff(t, f, before, after))
		}

		client.fault.armed.Store(true)
		result, err := client.session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: command})
		if err == nil {
			t.Fatalf("response loss not observed: %+v", result)
		}
		var lost []byte
		select {
		case lost = <-client.fault.lost:
		case <-time.After(5 * time.Second):
			t.Fatalf("response loss was not at the intended completed handler boundary: %v", err)
		}
		var lostEnvelope struct {
			Result struct {
				IsError           bool            `json:"isError"`
				StructuredContent json.RawMessage `json:"structuredContent"`
			} `json:"result"`
		}
		if err = json.Unmarshal(lost, &lostEnvelope); err != nil || lostEnvelope.Result.IsError {
			t.Fatalf("lost response was not a successful real commit: %s %v", lost, err)
		}
		original := decodeApprovedLocalStart(t, lostEnvelope.Result.StructuredContent)
		started := decodeApprovedLocalStart(t, client.call("execution_start", command, false))
		if started.Proposal.ID != original.Proposal.ID || started.Run.ID != original.Run.ID {
			t.Fatal("response-loss retry changed original identities")
		}
		if started.Proposal.RuntimeConfig != selected || started.Proposal.BackendSelection == nil || started.Proposal.BackendSelection.Input() != backend {
			t.Fatalf("Local ignored explicit selection: %+v", started.Proposal)
		}
		var model, effort, configID, fingerprint string
		if err = f.plane.DB.SQL().QueryRow(`SELECT p.model,p.reasoning_effort,p.backend_identity,p.backend_config_fingerprint FROM runtime_scope_plans p JOIN runtime_preparation_attempts a ON a.plan_id=p.plan_id WHERE a.run_id=?`, started.Run.ID).Scan(&model, &effort, &configID, &fingerprint); err != nil {
			t.Fatal(err)
		}
		if model != selected.Model || effort != selected.ReasoningEffort || configID != backend.ConfigID || fingerprint != backend.ConfigFingerprint {
			t.Fatalf("runtime plan did not preserve Local selection: %s/%s %s/%s", model, effort, configID, fingerprint)
		}
		waitDefinitionRuntimeSession(t, f, started.Run.ID, started.Proposal.ID)
		data, err := os.ReadFile(wire)
		if err != nil {
			t.Fatal(err)
		}
		turns := 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var entry struct {
				Method string `json:"method"`
				Params struct {
					Model  string `json:"model"`
					Effort string `json:"effort"`
				} `json:"params"`
			}
			if err = json.Unmarshal([]byte(line), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.Method == "turn/start" {
				turns++
				if entry.Params.Model != selected.Model || entry.Params.Effort != selected.ReasoningEffort {
					t.Fatalf("runtime wire ignored Local selection: %s", line)
				}
			}
		}
		if turns != 2 {
			t.Fatalf("expected real Implementation and Verification turns, got %d", turns)
		}

		// Administrative inventory removal is intentionally after terminal runtime.
		// It changes availability only; no workflow receipt/authority is fabricated.
		if _, err = f.plane.DB.SQL().Exec(`UPDATE codex_configs SET enabled=0 WHERE id=?`, alternate.ID); err != nil {
			t.Fatal(err)
		}
		for _, phase := range []string{"inventory-removed", "cold-reopen"} {
			if phase == "cold-reopen" {
				client.close()
				reopenDirectDeliveryFixture(t, f)
				f.plane.Retirement.Close()
				client = newApprovedStartLocalClient(t, f)
			}
			before = definitionRuntimeSnapshot(t, f, true)
			rawReplay := client.call("execution_start", command, false)
			replay := decodeApprovedLocalStart(t, rawReplay)
			var envelope struct {
				Context         map[string]any `json:"context"`
				Workflow        map[string]any `json:"workflow"`
				RefreshRequired bool           `json:"refreshRequired"`
			}
			if err = json.Unmarshal(rawReplay, &envelope); err != nil {
				t.Fatal(err)
			}
			// The canonical Task now points at Verification, but replay still
			// belongs to the original Implementation Run. Never attach the
			// newer operation's identity, decisions or actions to this receipt.
			if envelope.Context["executionId"] != started.Proposal.ID || envelope.Context["runId"] != started.Run.ID || envelope.Context["taskDefinitionRevisionId"] != started.Proposal.TaskDefinitionRevisionID {
				t.Errorf("%s replay envelope changed exact original scope: original execution=%s run=%s context=%+v", phase, started.Proposal.ID, started.Run.ID, envelope.Context)
			}
			if !envelope.RefreshRequired || envelope.Workflow["stale"] != true || envelope.Workflow["nextAction"] != nil || envelope.Workflow["pendingDecisions"] != nil || envelope.Context["decisionId"] != nil {
				t.Errorf("%s replay inherited current Verification guidance: workflow=%+v context=%+v", phase, envelope.Workflow, envelope.Context)
			}
			if replay.Proposal.ID != started.Proposal.ID || replay.Run.ID != started.Run.ID || replay.Proposal.RuntimeConfig != selected || !reflect.DeepEqual(replay.Proposal.BackendSelection, started.Proposal.BackendSelection) {
				t.Fatalf("%s replay replaced frozen start: %+v", phase, replay)
			}
			conflict := map[string]any{}
			for key, value := range command {
				conflict[key] = value
			}
			conflict["runtimeConfig"] = map[string]any{"model": prior.RuntimeConfig.Model, "reasoningEffort": prior.RuntimeConfig.ReasoningEffort, "codexFastMode": prior.RuntimeConfig.CodexFastMode}
			client.call("execution_start", conflict, true)
			conflict["runtimeConfig"] = command["runtimeConfig"]
			conflict["backend"] = prior.BackendSelection.Input()
			client.call("execution_start", conflict, true)
			if after := definitionRuntimeSnapshot(t, f, true); after != before {
				t.Fatalf("%s replay/conflict wrote facts:\n%s", phase, definitionRuntimeSnapshotDiff(t, f, before, after))
			}
		}
	})
}
