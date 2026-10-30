package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/delivery"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type mergeRetryContractSpy struct {
	surfaceCommandSpy
	input           delivery.MergeRetryInput
	err             error
	reads, commands int
}

func (s *mergeRetryContractSpy) ReprepareFeatureAreaMerge(_ context.Context, session string, in delivery.MergeRetryInput) (delivery.MergeRetryReceipt, error) {
	s.commands++
	s.input = in
	if in.ReleasePlan {
		return delivery.MergeRetryReceipt{ProjectID: "project", MergeRetryInput: in, PlanReleased: true}, s.err
	}
	return delivery.MergeRetryReceipt{ProjectID: "project", MergeRetryInput: in, OperationID: "retry/new-operation"}, s.err
}
func (s *mergeRetryContractSpy) FeatureAreaMergeRetryCandidates(context.Context, string, string) ([]delivery.MergeRetryCandidate, error) {
	s.reads++
	return []delivery.MergeRetryCandidate{{MergeRequestID: "merge", RetryCandidate: delivery.RetryCandidate{RepositoryID: "repo", OperationID: "initial/predecessor", Error: "WORKSPACE_DIRTY"}}}, nil
}

func TestMergeV119MCPRetryCatalogAndClosedReceipts(t *testing.T) {
	spy := &mergeRetryContractSpy{}
	session, listed := surfaceTestSession(t, spy)
	read, write := listed["feature_area_merge_retry_candidates"], listed["feature_area_merge_reprepare"]
	if read == nil || write == nil || read.Annotations == nil || !read.Annotations.ReadOnlyHint || write.Annotations == nil || write.Annotations.ReadOnlyHint {
		t.Fatal("delivery retry catalog or command classification missing")
	}
	readResult, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: read.Name, Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area"}})
	if err != nil || readResult.IsError || spy.reads != 1 || spy.commands != 0 {
		t.Fatalf("read created authority: %+v %v", readResult, err)
	}
	if err = resolveSchema(t, read.OutputSchema).Validate(jsonValue(t, readResult.StructuredContent)); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"aiSessionId": "session", "featureAreaId": "area", "mergeRequestId": "merge", "repositoryId": "repo", "predecessorOperationId": "initial/predecessor", "requestId": "new-request"}
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
	if !reflect.DeepEqual(spy.input, delivery.MergeRetryInput{FeatureAreaID: "area", MergeRequestID: "merge", RepositoryID: "repo", PredecessorOperationID: "initial/predecessor", RequestID: "new-request"}) {
		t.Fatalf("target stripped: %+v", spy.input)
	}
	for _, key := range []string{"mergeRequestId", "repositoryId", "predecessorOperationId", "requestId"} {
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
	if err = resolveSchema(t, json.RawMessage(mergeCandidatesSchema)).Validate(badRead); err == nil {
		t.Fatal("open candidate schema")
	}
}

func TestMergePlanReleaseMCPExplicitFlag(t *testing.T) {
	spy := &mergeRetryContractSpy{}
	session, listed := surfaceTestSession(t, spy)
	result, err := session.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "feature_area_merge_reprepare", Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area", "mergeRequestId": "parent", "repositoryId": "repo", "predecessorOperationId": "rejected", "requestId": "release-request", "releasePlan": true}})
	if err != nil || result.IsError || !spy.input.ReleasePlan {
		t.Fatalf("release: %+v %v", result, err)
	}
	value := jsonValue(t, result.StructuredContent)
	if err = resolveSchema(t, listed["feature_area_merge_reprepare"].OutputSchema).Validate(value); err != nil {
		t.Fatal(err)
	}
	wire := value["result"].(map[string]any)
	if wire["planReleased"] != true || wire["operationId"] != "" {
		t.Fatalf("release fabricated retry: %+v", wire)
	}
}
