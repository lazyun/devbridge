package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type exactStartEnvelopeService struct {
	*surfaceCommandSpy
	started           executionproposal.StartResult
	guidance          wp.Guidance
	startErr, readErr error
	scope             wp.ResourceScope
}

func (s *exactStartEnvelopeService) StartApprovedTaskExecution(context.Context, controlapi.ApprovedTaskStartInput) (executionproposal.StartResult, error) {
	return s.started, s.startErr
}
func (s *exactStartEnvelopeService) WorkflowGuidanceForResource(_ context.Context, scope wp.ResourceScope) (wp.Guidance, error) {
	s.scope = scope
	return s.guidance, s.readErr
}

// These are actual MCP tool calls through the shared production middleware.
// The service is an explicit test double; real Core/SQLite replay is covered by
// TestApprovedTaskStartThroughLocalMCP's cold-reopen Verification scenario.
func TestExecutionStartEnvelopeNeverBorrowsSuccessorGuidance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*exactStartEnvelopeService)
		stale  bool
	}{
		{name: "matching-original"},
		{name: "matching-legacy-result", change: func(s *exactStartEnvelopeService) { s.started.Receipt = nil }},
		{name: "later-verification-run", stale: true, change: func(s *exactStartEnvelopeService) { s.guidance.RunID = "successor-run" }},
		{name: "successor-execution", stale: true, change: func(s *exactStartEnvelopeService) { s.guidance.ExecutionID = "successor-execution" }},
		{name: "successor-revision", stale: true, change: func(s *exactStartEnvelopeService) { s.guidance.TaskDefinitionRevisionID = "successor-revision" }},
		{name: "review-replaced-execution", stale: true, change: func(s *exactStartEnvelopeService) {
			s.guidance.ExecutionID = ""
			s.guidance.ReviewID = "successor-review"
			s.guidance.RunID = "successor-review-run"
		}},
		{name: "receipt-only-failure", stale: true, change: func(s *exactStartEnvelopeService) {
			s.started.Proposal = executionproposal.Proposal{}
			s.started.Run = run.Run{}
			s.startErr = errors.New("original child read unavailable")
			s.guidance.RunID = "successor-run"
		}},
		{name: "receipt-before-run-failure", stale: true, change: func(s *exactStartEnvelopeService) {
			s.started.Receipt.OriginalRunID = ""
			s.started.Run = run.Run{}
			s.startErr = errors.New("original start has no confirmed Run")
			s.guidance.RunID = "successor-run"
		}},
		{name: "legacy-result-without-receipt", stale: true, change: func(s *exactStartEnvelopeService) { s.started.Receipt = nil; s.guidance.RunID = "successor-run" }},
		{name: "readback-unavailable", stale: true, change: func(s *exactStartEnvelopeService) { s.readErr = errors.New("readback unavailable") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := &executionproposal.StartReceiptIdentity{ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", OriginalExecutionID: "original-execution", OriginalRunID: "original-run"}
			s := &exactStartEnvelopeService{surfaceCommandSpy: &surfaceCommandSpy{}, started: executionproposal.StartResult{Receipt: original, Proposal: executionproposal.Proposal{ID: "original-execution", ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", RunID: "later-proposal-run"}, Run: run.Run{ID: "original-run", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutionProposalID: "original-execution"}}, guidance: wp.Guidance{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", ExecutionID: "original-execution", RunID: "original-run", DecisionID: "current-decision", RuntimeApprovalID: "current-runtime-approval", NextAction: wp.Action{Code: "RESOLVE_RUNTIME_APPROVAL", Required: true}}}
			if tc.change != nil {
				tc.change(s)
			}
			session, listed := surfaceTestSession(t, s)
			out, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "execution_start", Arguments: approvedStartSchemaInput()})
			if err != nil || out.IsError != (s.startErr != nil) {
				t.Fatalf("command disposition changed: %+v %v", out, err)
			}
			envelope := jsonValue(t, out.StructuredContent)
			contextValue := envelope["context"].(map[string]any)
			workflow := envelope["workflow"].(map[string]any)
			expectedRun := s.started.Run.ID
			if s.started.Receipt != nil {
				expectedRun = s.started.Receipt.OriginalRunID
			}
			if s.scope.ExecutionID != "original-execution" || s.scope.RunID != expectedRun || s.scope.TaskID != "task" || s.scope.ProjectID != "project" || s.scope.AISessionID != "session" {
				t.Errorf("readback not scoped to original receipt: %+v", s.scope)
			}
			if contextValue["executionId"] != "original-execution" || contextValue["taskDefinitionRevisionId"] != "revision" || contextValue["taskId"] != "task" || contextValue["projectId"] != "project" {
				t.Errorf("context changed original identity: %+v", contextValue)
			}
			if expectedRun == "" {
				if contextValue["runId"] != nil {
					t.Errorf("unconfirmed Run borrowed from live state: %+v", contextValue)
				}
			} else if contextValue["runId"] != expectedRun {
				t.Errorf("context changed original Run: %+v", contextValue)
			}
			expected := SurfaceExecutionStartOutput{StartResult: s.started, ReservationCommitted: true}
			if s.startErr != nil {
				expected.DispatchError = publicToolError(s.startErr).Error()
				expected.RefreshRequired = true
			}
			if !reflect.DeepEqual(envelope["result"], jsonValue(t, expected)) {
				t.Errorf("command result rewritten: %+v", envelope["result"])
			}
			hint := envelope["presentationHint"].(map[string]any)
			if tc.stale {
				if envelope["refreshRequired"] != true || workflow["stale"] != true || workflow["readError"] == nil || workflow["readError"] == "" || workflow["nextAction"] != nil || workflow["pendingDecisions"] != nil || workflow["runtimeApprovalId"] != nil || contextValue["decisionId"] != nil || hint["arguments"] != nil {
					t.Errorf("borrowed successor action or hid unavailable readback: %+v", envelope)
				}
			} else if envelope["refreshRequired"] == true || workflow["nextAction"] == nil || contextValue["decisionId"] != "current-decision" {
				t.Errorf("matching original guidance suppressed: %+v", envelope)
			}
			if err := resolveSchema(t, listed["execution_start"].OutputSchema).Validate(envelope); err != nil {
				t.Fatalf("envelope violates published schema: %v", err)
			}
			if len(out.Content) != 1 {
				t.Fatal("duplicate receipt representations")
			}
			var textValue map[string]any
			if err := json.Unmarshal([]byte(out.Content[0].(*sdkmcp.TextContent).Text), &textValue); err != nil || !reflect.DeepEqual(textValue, envelope) {
				t.Fatalf("text differs from structured envelope: %v", err)
			}
		})
	}
}
