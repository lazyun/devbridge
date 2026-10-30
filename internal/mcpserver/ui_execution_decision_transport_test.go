package mcpserver_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type pendingExecutionDecisionService struct {
	*workflowToolService
	legacyCancelCalls atomic.Int32
	rejectCalls       atomic.Int32
}

func (s *pendingExecutionDecisionService) GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	status := executionproposal.StatusPendingConfirmation
	if s.rejectCalls.Load() != 0 {
		status = executionproposal.StatusRejected
	}
	return s.proposal(status), nil
}

func (s *pendingExecutionDecisionService) CancelRunForSession(context.Context, string, string) (run.Run, error) {
	s.legacyCancelCalls.Add(1)
	return s.run(run.StatusCanceled), nil
}

func (s *pendingExecutionDecisionService) RejectExecutionProposal(ctx context.Context, sessionID, proposalID string) (executionproposal.Proposal, error) {
	s.rejectCalls.Add(1)
	return s.workflowToolService.RejectExecutionProposal(ctx, sessionID, proposalID)
}

func TestObsoleteDecisionToolsCannotMutateThroughFreshMCP(t *testing.T) {
	static, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service := &pendingExecutionDecisionService{workflowToolService: &workflowToolService{StaticService: static, now: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	freshSession := func(t *testing.T) *sdkmcp.ClientSession {
		t.Helper()
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "pending-execution-card", Version: "1"}, nil)
		session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	t.Run("obsolete decision tools are absent", func(t *testing.T) {
		listed, e := freshSession(t).ListTools(ctx, nil)
		if e != nil {
			t.Fatal(e)
		}
		for _, tool := range listed.Tools {
			if tool.Name == "ui_execution_decide" || tool.Name == "ui_task_decide" || tool.Name == "ui_decision_get" {
				t.Fatalf("obsolete tool: %s", tool.Name)
			}
		}
	})
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"legacy run only", map[string]any{"aiSessionId": "session-a", "runId": "run-a", "decision": "CANCEL"}},
		{"legacy run with proposal", map[string]any{"aiSessionId": "session-a", "runId": "run-a", "proposalId": "execution-1", "decision": "CANCEL"}},
		{"missing proposal", map[string]any{"aiSessionId": "session-a", "decision": "CANCEL"}},
		{"empty proposal", map[string]any{"aiSessionId": "session-a", "proposalId": "", "decision": "CANCEL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := freshSession(t).CallTool(ctx, &sdkmcp.CallToolParams{Name: "ui_execution_decide", Arguments: tc.args})
			if err == nil && (result == nil || !result.IsError) {
				t.Fatalf("stale active cancellation request was accepted: %+v", result)
			}
			if service.legacyCancelCalls.Load() != 0 || service.rejectCalls.Load() != 0 || service.executionStarts != 0 {
				t.Fatalf("rejected request mutated workflow: legacyCancel=%d reject=%d starts=%d", service.legacyCancelCalls.Load(), service.rejectCalls.Load(), service.executionStarts)
			}
		})
	}
}
