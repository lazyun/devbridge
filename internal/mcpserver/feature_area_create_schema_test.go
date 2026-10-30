package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFeatureAreaCreateSchemaSeparatesCreationModes(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(mcpserver.ExternalTopicCreateInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := []string{
		`{"aiSessionId":"session","title":"Area","problemStatement":"Record this work."}`,
		`{"aiSessionId":"session","title":"Area","problemStatement":"Record this work.","mode":"CREATE_ONLY","requestId":"receipt-1"}`,
		`{"aiSessionId":"session","title":"Area","problemStatement":"Investigate this work.","mode":"CREATE_AND_REVIEW","requestId":"receipt-2","initialTask":{"title":"Investigate","problemStatement":"Inspect the implementation.","constraints":["Read only"],"repositoryId":"repository"},"initialReview":{"reviewDepth":"DEEP","reviewPlan":{"mode":"FOCUSED","selectedModules":[{"key":"api","role":"PRIMARY"}]},"reviewInstruction":"Inspect exact implementation without changing it.","model":"gpt-5.6-terra","reasoningEffort":"xhigh"}}`,
	}
	invalid := []string{
		`{"aiSessionId":"session","title":"Area","problemStatement":"Record this work.","initialTask":{"title":"Task","problemStatement":"Work","repositoryId":"repository"}}`,
		`{"aiSessionId":"session","title":"Area","problemStatement":"Record this work.","mode":"CREATE_ONLY","initialReview":{"reviewDepth":"FAST","reviewPlan":{"mode":"REPOSITORY"},"reviewInstruction":"Inspect.","model":"gpt-5.6-terra"}}`,
		`{"aiSessionId":"session","title":"Area","problemStatement":"Investigate.","mode":"CREATE_AND_REVIEW","initialTask":{"title":"Task","problemStatement":"Work","repositoryId":"repository"},"initialReview":{"reviewDepth":"FAST","reviewPlan":{"mode":"REPOSITORY"},"reviewInstruction":"Inspect.","model":"gpt-5.6-terra"}}`,
		`{"aiSessionId":"session","title":"Area","problemStatement":"Investigate.","mode":"CREATE_AND_REVIEW","requestId":"receipt","initialTask":{"title":"Task","problemStatement":"Work","repositoryId":"repository"},"initialReview":{"reviewDepth":"FAST","reviewPlan":{"mode":"REPOSITORY","selectedModules":[]},"reviewInstruction":"Inspect.","model":"gpt-5.6-terra"}}`,
	}
	for _, raw := range valid {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(value); err != nil {
			t.Fatalf("valid input rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range invalid {
		var value any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(value); err == nil {
			t.Fatalf("invalid input accepted: %s", raw)
		}
	}
}

func TestFeatureAreaCreateSharedInputSchemasStayAligned(t *testing.T) {
	decode := func(raw string) map[string]any {
		t.Helper()
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		return schema
	}
	properties := func(schema map[string]any) map[string]any {
		t.Helper()
		return schema["properties"].(map[string]any)
	}
	create := decode(mcpserver.ExternalTopicCreateInputSchema)
	createProperties := properties(create)
	initialTask := properties(createProperties["initialTask"].(map[string]any))
	taskCreate := properties(decode(mcpserver.ExternalTaskCreateInputSchema))
	for _, field := range []string{"title", "problemStatement", "constraints", "repositoryId"} {
		if !reflect.DeepEqual(initialTask[field], taskCreate[field]) {
			t.Fatalf("Task field %s drifted between composition and task_create", field)
		}
	}
	initialReview := properties(createProperties["initialReview"].(map[string]any))
	reviewStart := properties(decode(mcpserver.ExternalReviewStartInputSchema))
	for _, field := range []string{"reviewDepth", "reviewPlan", "reviewInstruction", "model", "reasoningEffort"} {
		if !reflect.DeepEqual(initialReview[field], reviewStart[field]) {
			t.Fatalf("Review field %s drifted between composition and task_review_start", field)
		}
	}
}

type compositionSchemaService struct {
	*mcpserver.StaticService
	input   controlapi.FeatureAreaCreateInput
	startup *controlapi.FeatureAreaStartup
}

func (s *compositionSchemaService) CreateFeatureAreaWithInitialReview(_ context.Context, input controlapi.FeatureAreaCreateInput) (controlapi.FeatureAreaCreateResult, error) {
	s.input = input
	now := time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC)
	startup := controlapi.FeatureAreaStartup{Mode: controlapi.CreateAndReview, RequestID: input.RequestID, CompletedSteps: []string{"FEATURE_AREA_CREATED", "TASK_CREATED", "REVIEW_ADMITTED"}, FeatureAreaID: "feature-area-created", TaskID: "task-created", ReviewID: "review_created", RunID: "run-created", ReviewAdmitted: true, RunStatus: "RUNNING", Retryable: false, RefreshRequired: false}
	if s.startup != nil {
		startup = *s.startup
		startup.CompletedSteps = append([]string(nil), s.startup.CompletedSteps...)
	}
	return controlapi.FeatureAreaCreateResult{FeatureArea: featurearea.FeatureArea{ID: "feature-area-created", ProjectID: "project-created", Title: input.Title, ProblemStatement: input.ProblemStatement, Status: featurearea.StatusDiscovering, CreatedAt: now, UpdatedAt: now}, Startup: &startup}, nil
}

func TestFeatureAreaCreateSDKCompositionMapsStartupAndExactContext(t *testing.T) {
	service := &compositionSchemaService{StaticService: readyService(t)}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "feature-area-create-schema", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*sdkmcp.Tool{}
	for _, tool := range tools.Tools {
		byName[tool.Name] = tool
	}
	if byName["feature_area_create"] != nil {
		t.Fatal("retired direct feature_area_create remains model-visible")
	}
	for _, name := range []string{"feature_area_prepare", "confirmation_show", "confirmation_get"} {
		if byName[name] == nil {
			t.Fatalf("current composition tool missing: %s", name)
		}
	}
}
