package mcpserver

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/run"
)

type cardExecutionReader struct {
	BridgeService
	value       agentexecution.Projection
	session, id string
}

func (r *cardExecutionReader) GetAgentExecutionForSession(_ context.Context, session, id string) (agentexecution.Projection, error) {
	r.session, r.id = session, id
	return r.value, nil
}

func TestRunAndCardCarryUniversalExecutionWithoutAuthorizationAlias(t *testing.T) {
	for _, tc := range []struct {
		kind      agentexecution.Kind
		operation run.OperationKind
		purpose   run.Purpose
	}{
		{agentexecution.KindReview, run.OperationReview, run.PurposeScopeReview},
		{agentexecution.KindDevelopment, run.OperationExecution, run.PurposeVerification},
		{agentexecution.KindInspection, run.OperationInspection, run.PurposeInspection},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			e := agentexecution.Execution{ID: "universal", ProjectID: "project", TaskID: "task", Kind: tc.kind, SemanticOwnerKind: string(tc.operation), SemanticOwnerID: "owner", ProvenanceKind: agentexecution.ProvenanceNative}
			r := run.Run{ID: "run", AgentExecutionID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, OperationKind: tc.operation, OperationID: e.SemanticOwnerID, Purpose: tc.purpose, Status: run.StatusSucceeded, CreatedAt: time.Now()}
			if tc.kind == agentexecution.KindDevelopment {
				e.ExecutionProposalID = e.ID
				r.ExecutionProposalID = e.ID
			}
			p := agentexecution.Reduce(e, agentexecution.Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, DomainStatus: "CONFLICT"})
			output := runOutput(r, false)
			if output.ExecutionID != p.ExecutionID || output.ExecutionProposalID != e.ExecutionProposalID {
				t.Fatalf("run=%+v projection=%+v", output, p)
			}
			card := UIWorkflowStateOutput{}
			applyRunPresentation(&card, r)
			service := &cardExecutionReader{value: p}
			if err := enrichUIAgentExecution(context.Background(), service, "session", &card); err != nil {
				t.Fatal(err)
			}
			if card.ExecutionID != p.ExecutionID || card.CurrentRun.ExecutionID != p.ExecutionID || !reflect.DeepEqual(card.AgentExecution, &p) || service.id != e.ID || service.session != "session" {
				t.Fatalf("card diverged: %+v", card)
			}
		})
	}
}

func TestExecutionIdentityFieldsAreDeclaredInStrictSchemas(t *testing.T) {
	for _, raw := range []string{runOutputSchema, uiWorkflowStateOutputSchema} {
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		if _, ok := schema.Properties["executionId"]; !ok {
			t.Fatal("universal identity absent from schema")
		}
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(uiWorkflowStateOutputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if !reflect.DeepEqual(props["agentExecution"], func() any { var p any; _ = json.Unmarshal([]byte(agentExecutionProjectionSchema), &p); return p }()) {
		t.Fatal("card does not use shared Execution schema")
	}
}
