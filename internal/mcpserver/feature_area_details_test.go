package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workspace"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type detailsOnlyService struct {
	BridgeService
	value presentation.DetailsPage
	reads int
}

func detailsToolVisibility(ui map[string]any) []string {
	if values, ok := ui["visibility"].([]string); ok {
		return values
	}
	values, _ := ui["visibility"].([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if item, ok := value.(string); ok {
			out = append(out, item)
		}
	}
	return out
}

func (s *detailsOnlyService) FeatureAreaDetailsForSession(_ context.Context, sessionID, areaID string, options controlapi.ListOptions) (presentation.DetailsPage, error) {
	if sessionID != "session" || areaID != "area" {
		return presentation.DetailsPage{}, context.Canceled
	}
	s.reads++
	if options.Limit != 7 || options.Offset != 2 {
		return presentation.DetailsPage{}, context.DeadlineExceeded
	}
	return s.value, nil
}

func TestFeatureAreaDetailsIsReadOnlyAndReturnsStructuredFacts(t *testing.T) {
	service := &detailsOnlyService{value: presentation.DetailsPage{SchemaVersion: "1", Reviews: []review.Review{}, Tasks: []task.Task{}, TaskPlans: []taskplan.Plan{}, TaskDefinitionRevisions: []task.DefinitionRevision{}, Executions: []executionproposal.Proposal{}, Runs: []run.Run{}, Workspaces: []workspace.Workspace{}, Approvals: []approval.Approval{}, TaskDecisionReceipts: []decisionresolution.Receipt{}, CancellationReceipts: []executioncancel.Receipt{}, RecoveryConversations: []agentconversation.Conversation{}, RecoveryReceipts: []agentconversation.RecoveryReceipt{}, ScopeExpansions: []scopeexpansion.Expansion{}, SectionTotals: map[string]int{}, SectionHasMore: map[string]bool{}}}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test", Version: "1"}, nil)
	RegisterFeatureAreaDetails(server, service)
	ctx := context.Background()
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	connected, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connected.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 {
		t.Fatalf("details discovery=%+v err=%v", listed, err)
	}
	tools := make(map[string]*sdkmcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	get := tools["feature_area_details_get"]
	if get == nil || !get.Annotations.ReadOnlyHint || tools["feature_area_details_show"] != nil {
		t.Fatal("legacy detail show leaked or read missing")
	}
	getUI, _ := get.Meta["ui"].(map[string]any)
	if getUI["resourceUri"] != nil || get.Meta["openai/outputTemplate"] != nil || strings.Join(detailsToolVisibility(getUI), ",") != "app" {
		t.Fatalf("silent details read has a template association: %#v", get.Meta)
	}
	schema, marshalErr := json.Marshal(get.InputSchema)
	if marshalErr != nil || strings.Contains(string(schema), "exactKind") || strings.Contains(string(schema), "exactId") {
		t.Fatalf("details schema advertises an unimplemented selector: %s", schema)
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_details_get", Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area", "limit": 7, "offset": 2}})
	if err != nil || result.IsError {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if service.reads != 1 {
		t.Fatalf("reads=%d", service.reads)
	}
	if result.StructuredContent == nil {
		t.Fatal("missing structured details")
	}
	encoded, marshalErr := json.Marshal(result.StructuredContent)
	if marshalErr != nil || !strings.Contains(string(encoded), `"sectionHasMore"`) || !strings.Contains(string(encoded), `"workspaces"`) || !strings.Contains(string(encoded), `"recoveryConversations"`) || !strings.Contains(string(encoded), `"recoveryReceipts"`) {
		t.Fatalf("structured Details DTO is not camelCase/complete: %s err=%v", encoded, marshalErr)
	}
	if result.Meta["aiSessionId"] != "session" {
		t.Fatalf("metadata=%v", result.Meta)
	}
	bad, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_details_show", Arguments: map[string]any{"aiSessionId": "session", "featureAreaId": "area"}})
	if err == nil && !bad.IsError {
		t.Fatal("removed alias is callable")
	}
}
