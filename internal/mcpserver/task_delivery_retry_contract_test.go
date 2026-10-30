package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type deliveryRetryContractSpy struct {
	surfaceCommandSpy
	input           delivery.RetryInput
	err             error
	reads, commands int
}

func (s *deliveryRetryContractSpy) ReprepareAcceptedTaskCommit(_ context.Context, session string, in delivery.RetryInput) (delivery.RetryReceipt, error) {
	s.commands++
	s.input = in
	return delivery.RetryReceipt{ProjectID: "project", TaskID: in.TaskID, RepositoryID: in.RepositoryID, PredecessorOperationID: in.PredecessorOperationID, OperationID: "retry/new-operation", RequestID: in.RequestID}, s.err
}
func (s *deliveryRetryContractSpy) TaskDeliveryRetryCandidates(context.Context, string, string) ([]delivery.RetryCandidate, error) {
	s.reads++
	return []delivery.RetryCandidate{{RepositoryID: "repo", OperationID: "initial/predecessor", Error: "TASK_COMMIT_SNAPSHOT_DRIFT"}}, nil
}

func TestDeliveryV118MCPRetryCatalogAndClosedReceipts(t *testing.T) {
	spy := &deliveryRetryContractSpy{}
	session, listed := surfaceTestSession(t, spy)
	read, write := listed["task_commit_retry_candidates"], listed["task_commit_reprepare"]
	if read == nil || write == nil || read.Annotations == nil || !read.Annotations.ReadOnlyHint || write.Annotations == nil || write.Annotations.ReadOnlyHint {
		t.Fatal("delivery retry catalog or command classification missing")
	}
	readResult, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: read.Name, Arguments: map[string]any{"aiSessionId": "session", "taskId": "task"}})
	if err != nil || readResult.IsError || spy.reads != 1 || spy.commands != 0 {
		t.Fatalf("read created authority: %+v %v", readResult, err)
	}
	if err = resolveSchema(t, read.OutputSchema).Validate(jsonValue(t, readResult.StructuredContent)); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"aiSessionId": "session", "taskId": "task", "repositoryId": "repo", "predecessorOperationId": "initial/predecessor", "requestId": "new-request"}
	for _, partial := range []bool{false, true} {
		if partial {
			spy.err = errors.New("unknown after dispatch")
		}
		result, callErr := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: write.Name, Arguments: args})
		if callErr != nil || result.IsError != partial {
			t.Fatalf("retry %+v %v", result, callErr)
		}
		value := jsonValue(t, result.StructuredContent)
		if err = resolveSchema(t, write.OutputSchema).Validate(value); err != nil {
			t.Fatal(err)
		}
		wire := value["result"].(map[string]any)
		if partial && (wire["dispatchError"] != "unknown after dispatch" || wire["refreshRequired"] != true) {
			t.Fatalf("lost partial failure: %#v", wire)
		}
		if wire["operationId"] != "retry/new-operation" || wire["predecessorOperationId"] != args["predecessorOperationId"] || wire["requestId"] != args["requestId"] {
			t.Fatalf("lost durable receipt: %#v", wire)
		}
		wire["inventedAuthority"] = true
		if err = resolveSchema(t, write.OutputSchema).Validate(value); err == nil {
			t.Fatal("open receipt schema")
		}
	}
	if !reflect.DeepEqual(spy.input, delivery.RetryInput{TaskID: "task", RepositoryID: "repo", PredecessorOperationID: "initial/predecessor", RequestID: "new-request"}) {
		t.Fatalf("target stripped: %+v", spy.input)
	}
	for _, key := range []string{"repositoryId", "predecessorOperationId", "requestId"} {
		bad := map[string]any{}
		for k, v := range args {
			if k != key {
				bad[k] = v
			}
		}
		if err = resolveSchema(t, write.InputSchema).Validate(bad); err == nil {
			t.Fatalf("missing %s accepted", key)
		}
	}
	badRead := map[string]any{"candidates": []any{map[string]any{"repositoryId": "repo", "operationId": "op", "error": "reason", "approved": true}}}
	if err = resolveSchema(t, json.RawMessage(deliveryRetryCandidatesSchema)).Validate(badRead); err == nil {
		t.Fatal("open candidate schema")
	}
}

func TestDeliveryV118ExactTargetSurvivesClosedSurfaceSchema(t *testing.T) {
	target := workflowpresentation.ActionTarget{TaskID: "task", FeatureAreaID: "area", RepositoryID: "repo", OperationID: "initial/exact-operation"}
	wire := jsonValue(t, target)
	if err := resolveSchema(t, json.RawMessage(surfaceActionTargetSchema)).Validate(wire); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var actual workflowpresentation.ActionTarget
	if err = json.Unmarshal(encoded, &actual); err != nil || !reflect.DeepEqual(target, actual) {
		t.Fatalf("target stripped %+v %v", actual, err)
	}
}
