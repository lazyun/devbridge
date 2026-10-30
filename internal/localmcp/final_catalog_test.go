package localmcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type catalogPresentationService struct{ *mcpserver.StaticService }

func (*catalogPresentationService) ListProjects(context.Context, aicontext.ListOptions) ([]aicontext.ProjectContext, error) {
	return []aicontext.ProjectContext{}, nil
}

// TestFinalCatalogAcceptance exercises fresh production GPT and Local MCP
// registrations and bootstrap calls over loopback: no Codex,
// durable database, or external service participates in this catalog gate.
func TestFinalCatalogAcceptance(t *testing.T) {
	service := &catalogPresentationService{StaticService: readyService(t)}
	_, _, gptURL := startGPTTestServer(t, service)
	adapters := map[string]*sdkmcp.ClientSession{
		"gpt":   connectGPT(t, gptURL),
		"local": connectLocalMCP(t, startLocalMCPTestServer(t, service)),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for adapter, session := range adapters {
		adapter, session := adapter, session
		t.Run(adapter, func(t *testing.T) {
			listed, err := session.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			byName := make(map[string]*sdkmcp.Tool, len(listed.Tools))
			for _, tool := range listed.Tools {
				if byName[tool.Name] != nil {
					t.Fatalf("duplicate production tool registration: %s", tool.Name)
				}
				byName[tool.Name] = tool
			}

			modelCount := 0
			for _, tool := range listed.Tools {
				ui, _ := tool.Meta["ui"].(map[string]any)
				visibility, _ := ui["visibility"].([]any)
				appOnly := len(visibility) == 1 && visibility[0] == "app"
				if !appOnly {
					modelCount++
				}
				if adapter == "local" && (ui["resourceUri"] != nil || tool.Meta["openai/outputTemplate"] != nil) {
					t.Fatalf("Local tool mounts a ChatGPT UI resource: %s", tool.Name)
				}
				if adapter == "gpt" {
					show := tool.Name == "panel_show" || tool.Name == "project_selection_show"
					standard, compatibility := ui["resourceUri"], tool.Meta["openai/outputTemplate"]
					if show && (standard == nil || standard != compatibility) {
						t.Fatalf("show resource contracts disagree: %s %#v", tool.Name, tool.Meta)
					}
					if !show && (standard != nil || compatibility != nil) {
						t.Fatalf("silent tool attaches UI: %s %#v", tool.Name, tool.Meta)
					}
				}
			}
			wantCount := len(mcpserver.SurfaceBusinessToolNames)
			if adapter == "gpt" {
				// GPT replaces ten direct commands with the purpose-confirmation set
				// (five prepares, show, get, options, the public commit entry and the
				// formal replacement prepare), exposes panel_show, and keeps secondary
				// card readers app-only.
				// Recovery purpose repair and no-Run stop/revise are public
				// commands; Core-only receipt consumers are absent.
				wantCount = len(mcpserver.SurfaceBusinessToolNames) + 3
			}
			if modelCount != wantCount {
				t.Fatalf("%s actual model count %d want %d", adapter, modelCount, wantCount)
			}
			// Validate a real enveloped call as well as bootstrap, including the
			// Local adapter's text hint/schema mapping after all middleware.
			projects, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "project_list", Arguments: map[string]any{}})
			if err != nil || projects.IsError {
				t.Fatalf("project_list: %+v %v", projects, err)
			}
			projectValue := normalizeJSON(t, projects.StructuredContent).(map[string]any)
			projectSchemaJSON, _ := json.Marshal(byName["project_list"].OutputSchema)
			var projectSchema jsonschema.Schema
			if err := json.Unmarshal(projectSchemaJSON, &projectSchema); err != nil {
				t.Fatal(err)
			}
			resolvedProjects, err := projectSchema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolvedProjects.Validate(projectValue); err != nil {
				t.Fatalf("%s project_list wire schema: %v", adapter, err)
			}
			hint := projectValue["presentationHint"].(map[string]any)
			if adapter == "local" && (hint["tool"] != nil || hint["arguments"] != nil || hint["display"] != "TEXT") {
				t.Fatalf("Local depends on UI handoff: %#v", hint)
			}
			if projects.Meta["openai/outputTemplate"] != nil {
				t.Fatal("silent project_list mounted a Card")
			}
			assertFinalDirectFileCatalog(t, byName, adapter)
			assertFinalWorkflowActions(t, byName, adapter)
			assertFinalLegacyToolsAbsent(t, byName, adapter)
			if adapter == "local" {
				assertTaskAcceptanceSchema(t, byName["decision_respond"])
			}
			diagnostic, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "bridge_diagnostics", Arguments: map[string]any{}})
			if err != nil || diagnostic.IsError {
				t.Fatalf("bootstrap diagnostics failed %v", err)
			}
			identity := normalizeJSON(t, diagnostic.StructuredContent).(map[string]any)
			if identity["applicationName"] != "Dev Bridge" || identity["ready"] != true || identity["protocol"] != "MCP" || identity["serverName"] != "devbridge" || identity["transport"] != "streamable-http" {
				t.Fatalf("invalid bootstrap identity: %#v", identity)
			}
			schema := normalizeJSON(t, byName["bridge_diagnostics"].OutputSchema).(map[string]any)
			rawSchema, _ := json.Marshal(schema)
			var contract jsonschema.Schema
			if err := json.Unmarshal(rawSchema, &contract); err != nil {
				t.Fatal(err)
			}
			resolved, err := contract.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(identity); err != nil {
				t.Fatalf("bootstrap result/schema mismatch: %v", err)
			}
			props := schema["properties"].(map[string]any)
			for _, key := range []string{"result", "context", "workflow", "presentationHint", "workflowGuidance", "nextAction"} {
				if identity[key] != nil || props[key] != nil {
					t.Fatalf("bootstrap wrapped: %s", key)
				}
			}
			if len(props) != 10 || len(schema["required"].([]any)) != 10 || schema["additionalProperties"] != false {
				t.Fatalf("invalid flat schema: %#v", schema)
			}
			for _, key := range []string{"applicationName", "applicationVersion", "protocol", "protocolVersion", "serverName", "serverVersion", "transport", "businessCatalogSHA256", "unifiedReviewSchemaSHA256"} {
				if identity[key] == nil || identity[key] == "" || props[key].(map[string]any)["type"] != "string" {
					t.Fatalf("missing identity/schema %s", key)
				}
			}
			if props["ready"].(map[string]any)["type"] != "boolean" {
				t.Fatal("ready schema")
			}
			// The unified panel owns the private reads, legacy card helpers and
			// explicit confirmation recovery controls. None mounts a new card.
			if adapter == "gpt" && len(listed.Tools)-modelCount != 10 {
				t.Fatalf("app-only count = %d", len(listed.Tools)-modelCount)
			}

			writeFinalCatalogEvidence(t, adapter, listed)
		})
	}
}

func assertFinalDirectFileCatalog(t *testing.T, tools map[string]*sdkmcp.Tool, adapter string) {
	t.Helper()
	want := []string{"direct_file_enable", "direct_file_disable", "direct_file_reconcile_task_authorization", "file_implementation_accept", "file_implementation_result_get", "file_implementation_result_submit", "file_list", "file_read", "file_worker_claim", "file_worker_get", "file_write"}
	if adapter == "gpt" {
		want = []string{"direct_file_disable", "direct_file_reconcile_task_authorization", "file_implementation_accept", "file_implementation_result_get", "file_implementation_result_submit", "file_list", "file_read", "file_worker_claim", "file_worker_get", "file_write"}
	}
	for _, name := range want {
		if tools[name] == nil {
			t.Fatalf("Direct File production tool missing: %s", name)
		}
	}
	var got []string
	for name := range tools {
		if strings.HasPrefix(name, "direct_file_") || strings.HasPrefix(name, "file_") {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Direct File catalog = %v, want exact published tools %v", got, want)
	}
}

func assertFinalWorkflowActions(t *testing.T, tools map[string]*sdkmcp.Tool, adapter string) {
	t.Helper()
	required := mcpserver.SurfaceBusinessToolNames
	for _, name := range required {
		if adapter == "gpt" && (name == "project_open" || name == "development_branch_select" || name == "feature_area_create" || name == "task_create" || name == "task_review_request" || name == "task_review_start" || name == "execution_start" || name == "direct_file_enable" || name == "decision_respond") {
			continue
		}
		if tools[name] == nil {
			t.Fatalf("required lifecycle/Review/Task action missing: %s", name)
		}
	}
}

func assertFinalLegacyToolsAbsent(t *testing.T, tools map[string]*sdkmcp.Tool, adapter string) {
	t.Helper()
	removed := []string{
		"ui_decision_get", "ui_task_decide", "ui_execution_decide", "featureArea_review_start",
		"scope_expansion_propose", "ui_scope_expansion_decide", "scope_review_start", "scope_review_create",
		"task_plan_create", "task_plan_get", "execution_proposal_create", "execution_proposal_get",
		"workflow_get", "review_get", "execution_get", "execution_cancel", "review_approval_respond", "task_acceptance_respond", "codex_request_resolve", "agent_runtime_state", "featureArea_create", "featureArea_activate", "featureArea_retire", "featureArea_decompose", "featureArea_commit", "file_create", "file_replace", "file_delete", "file_move", "workflow_state_get", "task_prepare", "task_preparation_get", "run_get", "approval_get",
	}
	for _, name := range removed {
		if adapter == "gpt" && name == "task_prepare" {
			continue
		}
		if tools[name] != nil {
			t.Fatalf("retired production tool remains registered: %s", name)
		}
	}
}

func assertTaskAcceptanceSchema(t *testing.T, tool *sdkmcp.Tool) {
	t.Helper()
	if tool == nil {
		t.Fatal("decision_respond is not registered")
	}
	got := normalizeJSON(t, tool.InputSchema).(map[string]any)
	var schema map[string]any
	for _, raw := range got["oneOf"].([]any) {
		branch := raw.(map[string]any)
		if branch["properties"].(map[string]any)["kind"].(map[string]any)["const"] == "TASK_ACCEPTANCE" {
			schema = branch
		}
	}
	if schema == nil {
		t.Fatal("typed TASK_ACCEPTANCE branch is absent")
	}
	var source map[string]any
	if err := json.Unmarshal([]byte(mcpserver.TaskAcceptanceInputJSONSchema), &source); err != nil {
		t.Fatal(err)
	}
	source["properties"].(map[string]any)["kind"] = map[string]any{"const": "TASK_ACCEPTANCE"}
	source["required"] = append(source["required"].([]any), "kind")
	if !reflect.DeepEqual(schema, source) {
		t.Fatalf("generated typed acceptance branch drift: %#v", schema)
	}
	required := stringSet(schema["required"])
	for _, field := range []string{"aiSessionId", "taskId", "featureAreaId", "executionId", "taskDefinitionRevisionId", "decisionId", "requestId", "option"} {
		if !required[field] {
			t.Fatalf("Task Acceptance exact-target schema does not require %s", field)
		}
	}
	properties := schema["properties"].(map[string]any)
	options := stringSet(properties["option"].(map[string]any)["enum"])
	for _, option := range []string{"ACCEPT", "REJECT", "REQUEST_CHANGES", "REQUEST_REVERIFICATION"} {
		if !options[option] {
			t.Fatalf("Task Acceptance option missing from schema: %s", option)
		}
	}
	for _, successor := range []string{"nextWorkSpec", "nextVerificationSpec"} {
		if properties[successor] == nil {
			t.Fatalf("REQUEST_CHANGES successor schema missing: %s", successor)
		}
	}
	alternatives, ok := schema["oneOf"].([]any)
	if !ok || len(alternatives) != 2 {
		t.Fatalf("Task Acceptance successor union is not exact: %#v", schema["oneOf"])
	}
}

func normalizeJSON(t *testing.T, value any) any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var normalized any
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	return normalized
}

func stringSet(value any) map[string]bool {
	result := map[string]bool{}
	for _, item := range value.([]any) {
		result[item.(string)] = true
	}
	return result
}

func writeFinalCatalogEvidence(t *testing.T, adapter string, listed *sdkmcp.ListToolsResult) {
	t.Helper()
	directory := os.Getenv("DEVBRIDGE_FINAL_CATALOG_OUTPUT_DIR")
	if directory == "" {
		return
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	sort.Slice(listed.Tools, func(i, j int) bool { return listed.Tools[i].Name < listed.Tools[j].Name })
	raw, err := json.MarshalIndent(listed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(filepath.Join(directory, adapter+"-tools-list.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}
