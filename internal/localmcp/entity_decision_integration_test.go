package localmcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/run"
)

type responseLossDecisionService struct {
	*mcpserver.StaticService
	authorizations atomic.Int32
	rejections     atomic.Int32
	approvals      atomic.Int32
}

func (s *responseLossDecisionService) AuthorizeAndStartExecution(ctx context.Context, sessionID, id string) (executionproposal.StartResult, error) {
	s.authorizations.Add(1)
	value, err := s.StaticService.AuthorizeAndStartExecution(ctx, sessionID, id)
	if err != nil {
		return value, err
	}
	return executionproposal.StartResult{}, errors.New("simulated lost response after authorization")
}
func (s *responseLossDecisionService) RejectExecutionProposal(ctx context.Context, sessionID, id string) (executionproposal.Proposal, error) {
	s.rejections.Add(1)
	value, err := s.StaticService.RejectExecutionProposal(ctx, sessionID, id)
	if err != nil {
		return value, err
	}
	return executionproposal.Proposal{}, errors.New("simulated lost response after rejection")
}
func (s *responseLossDecisionService) DecideApprovalForSession(ctx context.Context, sessionID, id string, decision approval.Decision) (approval.Approval, error) {
	s.approvals.Add(1)
	value, err := s.StaticService.DecideApprovalForSession(ctx, sessionID, id, decision)
	if err != nil {
		return value, err
	}
	return approval.Approval{}, errors.New("simulated lost response after approval delivery")
}

type decisionApprovalBackend struct {
	started    chan struct{}
	threadID   string
	requests   chan codex.ApprovalRequest
	deliveries atomic.Int32
	decision   atomic.Value
}

func (s *decisionApprovalBackend) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return s, nil
}
func (s *decisionApprovalBackend) StartThread(context.Context, string) (string, error) {
	return "thread-decision", nil
}
func (s *decisionApprovalBackend) ResumeThread(_ context.Context, id, _ string, _ *run.TokenCounts) (string, error) {
	return id, nil
}
func (s *decisionApprovalBackend) StartTurn(ctx context.Context, _, _ string) (execution.CodexTurnResult, error) {
	<-ctx.Done()
	return execution.CodexTurnResult{}, ctx.Err()
}
func (s *decisionApprovalBackend) StartTurnWithStarted(ctx context.Context, threadID, instruction string, started func(string) error) (execution.CodexTurnResult, error) {
	if err := started("turn-decision"); err != nil {
		return execution.CodexTurnResult{}, err
	}
	s.threadID = threadID
	close(s.started)
	return s.StartTurn(ctx, threadID, instruction)
}
func (s *decisionApprovalBackend) NextApproval(ctx context.Context) (codex.ApprovalRequest, error) {
	select {
	case value := <-s.requests:
		return value, nil
	case <-ctx.Done():
		return codex.ApprovalRequest{}, ctx.Err()
	}
}
func (s *decisionApprovalBackend) ResolveApproval(_ context.Context, _ string, decision codex.ApprovalDecision) error {
	s.deliveries.Add(1)
	s.decision.Store(decision.Type)
	return nil
}
func (s *decisionApprovalBackend) Close() error { return nil }

func TestEntityDecisionsFreshTransportResponseLossAndGPTLocalParity(t *testing.T) {
	fixture := newIntegrationFixture(t)
	lost := &responseLossDecisionService{StaticService: fixture.service}
	localURL := startLocalMCPTestServer(t, lost)
	_, _, gptURL := startGPTTestServer(t, lost)
	local := connectLocalMCP(t, localURL)
	gpt := connectGPT(t, gptURL)
	aiSession := callToolMap(t, fixture.ctx, local, "project_open", map[string]any{"projectId": fixture.projectID})["aiSessionId"].(string)
	current, _ := fixture.admitScalarExecution(t, aiSession)
	backend := &decisionApprovalBackend{started: make(chan struct{}), requests: make(chan codex.ApprovalRequest, 1)}
	fixture.plane.Coordinator.Codex = backend
	if _, err := fixture.control.StartTaskExecution(fixture.ctx, aiSession, current.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.started:
	case <-time.After(5 * time.Second):
		t.Fatal("backend did not start")
	}
	fresh := connectLocalMCP(t, localURL)
	freshID := callToolMap(t, fixture.ctx, fresh, "project_open", map[string]any{"projectId": fixture.projectID})["aiSessionId"].(string)
	backend.requests <- codex.ApprovalRequest{ID: "approval-decision", ThreadID: backend.threadID, TurnID: "turn-decision", Kind: codex.ApprovalKind("COMMAND_EXECUTION"), RequestPayload: json.RawMessage(`{"command":"go test ./..."}`), AllowedDecisions: []codex.DecisionType{codex.DecisionCancel, codex.DecisionDecline}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := fixture.plane.Approvals.Get(fixture.ctx, "approval-decision"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("durable Approval not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	localState := callToolMap(t, fixture.ctx, fresh, "workspace_get", map[string]any{"aiSessionId": freshID, "taskId": current.ID})
	gptState := callToolMap(t, fixture.ctx, gpt, "workspace_get", map[string]any{"aiSessionId": freshID, "taskId": current.ID})
	localPending := localState["workflow"].(map[string]any)["pendingDecisions"].([]any)
	gptPending := gptState["workflow"].(map[string]any)["pendingDecisions"].([]any)
	// Adapter presentation hints and owner-specific runtime observations are
	// enrichment. The durable decision identity is the shared contract.
	if len(localPending) == 0 || len(gptPending) == 0 {
		t.Fatalf("Approval decision projection diverged: local=%+v gpt=%+v", localState, gptState)
	}
	decision := localPending[0].(map[string]any)["decision"].(map[string]any)
	if decision["id"] != "RUNTIME_APPROVAL:approval-decision" || decision["cancelEffect"] != "DECLINE_ACTION" {
		t.Fatalf("Approval CANCEL lost exact owner or decline-without-cancellation semantics: %+v", decision)
	}
	approvalFirst := callToolMap(t, fixture.ctx, gpt, "ui_approval_decide", map[string]any{"aiSessionId": aiSession, "approvalId": "approval-decision", "decision": "CANCEL"})
	if approvalFirst["approval"].(map[string]any)["cancelEffect"] != "DECLINE_ACTION" || approvalFirst["entityDecision"].(map[string]any)["transitionError"] == nil {
		t.Fatalf("Approval response loss/ownership missing: %+v", approvalFirst)
	}
	approvalReplay := callToolMap(t, fixture.ctx, fresh, "decision_respond", map[string]any{"aiSessionId": freshID, "decisionId": "RUNTIME_APPROVAL:approval-decision", "option": "CANCEL"})
	assertEntityContract(t, approvalReplay)
	gptApprovalReplay := callToolMap(t, fixture.ctx, connectGPT(t, gptURL), "ui_approval_decide", map[string]any{"aiSessionId": freshID, "approvalId": "approval-decision", "decision": "CANCEL"})
	if !reflect.DeepEqual(approvalReplay["entityDecision"], gptApprovalReplay["entityDecision"]) || backend.deliveries.Load() != 1 || lost.approvals.Load() != 1 || backend.decision.Load() != codex.DecisionDecline {
		t.Fatalf("Approval retry redelivered or changed semantics: local=%+v gpt=%+v deliveries=%d calls=%d", approvalReplay, gptApprovalReplay, backend.deliveries.Load(), lost.approvals.Load())
	}
	approvalConflict := callToolMap(t, fixture.ctx, fresh, "decision_respond", map[string]any{"aiSessionId": freshID, "decisionId": "RUNTIME_APPROVAL:approval-decision", "option": "DECLINE"})
	if approvalConflict["entityDecision"].(map[string]any)["conflict"] != true || lost.approvals.Load() != 1 {
		t.Fatalf("changed Approval payload mutated state: %+v", approvalConflict)
	}
	if _, err := fixture.control.GetDecisionResolution(fixture.ctx, freshID, "RUNTIME_APPROVAL:approval-decision"); !errors.Is(err, decisionresolution.ErrNotFound) {
		t.Fatalf("Approval gained an exact request receipt: %v", err)
	}
	// Direct adapter calls enforce the same no-requestId boundary as fresh schemas.
	if _, err := localmcp.DecisionRespondCore(fixture.ctx, fixture.service, localmcp.DecisionRespondInput{AISessionID: freshID, DecisionID: "RUNTIME_APPROVAL:approval-decision", Option: "CANCEL", RequestID: "pretend-receipt"}); err == nil {
		t.Fatal("core accepted non-Task request identity")
	}
}

func assertEntityContract(t *testing.T, out map[string]any) {
	t.Helper()
	if out["guarantee"] != "AUTHORITATIVE_ENTITY_STATE" || out["entityDecision"] == nil {
		t.Fatal(out)
	}
	for _, key := range []string{"requestId", "resolutionReceipt", "accepted", "replay"} {
		if out[key] != nil {
			t.Fatalf("runtime Approval invented request receipt: %+v", out)
		}
	}
}

func (s *decisionApprovalBackend) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.StartTurnWithStarted(ctx, thread, prompt, started)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
