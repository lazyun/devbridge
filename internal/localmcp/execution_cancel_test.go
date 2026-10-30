package localmcp_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This service isolates adapter parity. Durable cancellation and backend race
// behavior are exercised by the store/runtime fixtures, not reimplemented here.
type cancellationTransportService struct {
	*mcpserver.StaticService
	mu      sync.Mutex
	inputs  map[string]executioncancel.Input
	receipt executioncancel.Receipt
}

// InitializeSession supplies the current Session-first admission fact for this
// transport-only fixture. The cancellation behavior remains in the explicit
// fake methods below; no production Session or Project store is substituted.
func (s *cancellationTransportService) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	projectID := "project-a"
	return aicontext.InitializationResult{
		AISessionID:               id,
		State:                     "PROJECT_BOUND",
		BoundProjectID:            &projectID,
		Project:                   &aicontext.ProjectIdentity{ID: projectID, Name: "Cancellation fixture"},
		AvailableProjects:         []aicontext.ProjectIdentity{},
		AvailableProjectsStatus:   "NOT_APPLICABLE",
		AvailableProjectsComplete: true,
	}, nil
}

func (s *cancellationTransportService) CancelExecutionForSession(_ context.Context, input executioncancel.Input) (executioncancel.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prior, ok := s.inputs[input.RequestID]; ok {
		if !reflect.DeepEqual(prior, input) {
			return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRequestConflict)
		}
		return s.receipt, nil
	}
	if input.ExecutionID != "execution-a" || input.RunID != "run-a" {
		return executioncancel.Receipt{}, controlapi.MapError(executioncancel.ErrRunNotActive)
	}
	s.inputs[input.RequestID] = input
	now := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	s.receipt = executioncancel.Receipt{RequestID: input.RequestID, ProjectID: "project-a", ExecutionID: input.ExecutionID, RunID: input.RunID, RequestedByAISessionID: input.AISessionID, RequestedAt: now, Reason: input.Reason, State: "CANCELED", BackendInterruptState: "ACKNOWLEDGED", QuiescenceState: "QUIESCED", RunStatus: run.StatusCanceled, ExecutionStatus: "CANCELED", ResolvedAt: &now}
	return s.receipt, nil
}
func (s *cancellationTransportService) ListExecutionCancellationsForSession(context.Context, string, string) ([]executioncancel.Receipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []executioncancel.Receipt{s.receipt}, nil
}
func (s *cancellationTransportService) GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	return executionproposal.Proposal{ID: "execution-a", ProjectID: "project-a", TaskID: "task-a", TaskPlanID: "plan-a", Status: executionproposal.StatusConsumed, Outcome: executionproposal.OutcomeCanceled}, nil
}
func (s *cancellationTransportService) GetTaskPlan(context.Context, string, string) (taskplan.Plan, error) {
	return taskplan.Plan{ID: "plan-a", TaskID: "task-a"}, nil
}
func (s *cancellationTransportService) LatestRunForTask(context.Context, string, string, run.Purpose, string) (run.Run, error) {
	return run.Run{}, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "no stage"}
}
func (s *cancellationTransportService) ExecutionGoalProgress(context.Context, string, string) ([]taskgoal.Progress, error) {
	return []taskgoal.Progress{}, nil
}

func TestExecutionCancellationFreshTransportParityAndReplay(t *testing.T) {
	service := &cancellationTransportService{StaticService: readyService(t), inputs: map[string]executioncancel.Input{}}
	_, _, gptURL := startGPTTestServer(t, service)
	localURL := startLocalMCPTestServer(t, service)
	gpt := connectGPT(t, gptURL)
	local := connectLocalMCP(t, localURL)
	var schema []byte
	for _, session := range []*sdkmcp.ClientSession{gpt, local} {
		listed, err := session.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, tool := range listed.Tools {
			if tool.Name != "operation_cancel" {
				continue
			}
			found = true
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			if schema == nil {
				schema = encoded
			} else if string(schema) != string(encoded) {
				t.Fatalf("adapter schemas differ: %s != %s", schema, encoded)
			}
			if tool.Annotations == nil || tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint {
				t.Fatalf("cancellation control semantics=%+v meta=%+v", tool.Annotations, tool.Meta)
			}
			if session == gpt {
				ui, _ := tool.Meta["ui"].(map[string]any)
				if ui["resourceUri"] != nil || tool.Meta["openai/outputTemplate"] != nil {
					t.Fatalf("GPT cancellation emitted a Card before exact readback: %v", tool.Meta)
				}
			} else if tool.Meta["ui"] != nil {
				t.Fatalf("Local MCP gained a ChatGPT resource: %v", tool.Meta)
			}
			var parsed map[string]any
			_ = json.Unmarshal(encoded, &parsed)
			for _, raw := range parsed["oneOf"].([]any) {
				branch := raw.(map[string]any)
				for _, name := range branch["required"].([]any) {
					if name == "runId" {
						t.Fatal("pre-Run cancellation must allow omitted runId")
					}
				}
			}

		}
		if !found {
			t.Fatal("operation_cancel absent")
		}
	}
	args := map[string]any{"aiSessionId": "session-a", "executionId": "execution-a", "runId": "run-a", "requestId": "cancel-a", "reason": "stop exact run"}
	first := callToolMap(t, context.Background(), gpt, "operation_cancel", args)
	// Losing the first response and retrying via the other adapter preserves the
	// same logical request and receipt; neither transport asks for a Decision.
	replay := callToolMap(t, context.Background(), local, "operation_cancel", args)
	if !reflect.DeepEqual(first, replay) || first["state"] != "CANCELED" {
		t.Fatalf("first=%+v replay=%+v", first, replay)
	}
	service.mu.Lock()
	count := len(service.inputs)
	service.mu.Unlock()
	if count != 1 {
		t.Fatalf("logical cancellation count=%d", count)
	}
	conflict := map[string]any{"aiSessionId": "session-a", "executionId": "execution-a", "runId": "run-a", "requestId": "cancel-a", "reason": "changed"}
	for _, session := range []*sdkmcp.ClientSession{gpt, local} {
		if got := callToolErrorText(t, context.Background(), session, "operation_cancel", conflict); !strings.Contains(got, "REQUEST_CONFLICT") {
			t.Fatalf("conflict=%s", got)
		}
		missing := map[string]any{"aiSessionId": "session-a", "executionId": "execution-a", "requestId": "missing"}
		if got := callToolErrorText(t, context.Background(), session, "operation_cancel", missing); !strings.Contains(got, "RUN_NOT_ACTIVE") {
			t.Fatalf("required target validation=%s", got)
		}
	}
	fresh := connectLocalMCP(t, localURL)
	detail := callToolMap(t, context.Background(), fresh, "operation_get", map[string]any{"aiSessionId": "fresh", "target": map[string]any{"kind": "EXECUTION", "operationId": "execution-a", "ownerTaskId": "task-a"}, "include": []string{"RESULT"}})
	cancellation := detail["result"].(map[string]any)["cancellation"].(map[string]any)
	receipts := cancellation["receipts"].([]any)
	if cancellation["state"] != "CANCELED" || cancellation["available"] != false || len(receipts) != 1 || receipts[0].(map[string]any)["requestId"] != "cancel-a" {
		t.Fatalf("fresh cancellation read=%+v", cancellation)
	}
}

func (s *cancellationTransportService) GetAgentExecutionForSession(context.Context, string, string) (agentexecution.Projection, error) {
	return agentexecution.Reduce(agentexecution.Execution{ID: "execution-a", ProjectID: "project-a", TaskID: "task-a", Kind: agentexecution.KindDevelopment, SemanticOwnerKind: "EXECUTION", SemanticOwnerID: "execution-a", ExecutionProposalID: "execution-a", ProvenanceKind: agentexecution.ProvenanceNative}, agentexecution.Facts{DevelopmentOutcome: "CANCELED"}), nil
}

func (s *cancellationTransportService) GetSession(_ context.Context, id string) (aicontext.SessionContext, error) {
	return aicontext.SessionContext{Session: aisession.Session{ID: id, ProjectID: "project-a"}, Project: project.Project{ID: "project-a"}}, nil
}
func (s *cancellationTransportService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-a", ProjectID: "project-a", FeatureAreaID: "area-a"}, nil
}
