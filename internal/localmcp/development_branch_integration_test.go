package localmcp_test

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDevelopmentBranchSharedContractsAndProjectTruth(t *testing.T) {
	f := newIntegrationFixtureWithBaseline(t, false)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", f.repoPath}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("branch", "dev/v0.1.0")
	git("branch", "experiment/test")
	before := git("show-ref", "--heads") + git("symbolic-ref", "HEAD") + git("status", "--porcelain=v1")
	gpt := connectGPT(t, f.gptURL)
	local := connectLocalMCP(t, f.libreURL)
	sessions := []*sdkmcp.ClientSession{gpt, local}
	ids := make([]string, 2)
	for i, client := range sessions {
		var workspaceOutputSchema any
		listed, err := client.ListTools(f.ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, tool := range listed.Tools {
			var input, output string
			switch tool.Name {
			case "workspace_get":
				input, output = mcpserver.WorkspaceGetInputSchema, mcpserver.WorkspaceGetOutputSchema
				workspaceOutputSchema = tool.OutputSchema
			case "development_branch_select":
				input, output = mcpserver.DevelopmentBranchSelectInputSchema, mcpserver.DevelopmentCycleOutputSchema
			default:
				continue
			}
			found++
			advertisedOutput := tool.OutputSchema
			if tool.Name == "development_branch_select" {
				advertisedOutput = normalizeJSON(t, advertisedOutput).(map[string]any)["properties"].(map[string]any)["result"]
			}
			for _, pair := range []struct {
				got  any
				want string
			}{{tool.InputSchema, input}, {advertisedOutput, output}} {
				var want, got any
				if err := json.Unmarshal([]byte(pair.want), &want); err != nil {
					t.Fatal(err)
				}
				encoded, _ := json.Marshal(pair.got)
				if err := json.Unmarshal(encoded, &got); err != nil {
					t.Fatal(err)
				}
				if i == 1 && tool.Name == "workspace_get" {
					props, _ := want.(map[string]any)["properties"].(map[string]any)
					if hint, ok := props["presentationHint"].(map[string]any); ok {
						// Local's only adapter-specific schema difference is its
						// text presentation; every business property stays exact.
						hp := hint["properties"].(map[string]any)
						delete(hp, "tool")
						delete(hp, "arguments")
						hp["display"] = map[string]any{"type": "string", "const": "TEXT"}
						hint["required"] = []any{"required", "reason", "display", "hostVisibility"}
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("adapter %d %s schema diverged", i, tool.Name)
				}
			}
		}
		wantFound := 2
		if i == 0 {
			// ChatGPT replaces the direct branch-selection command with its
			// purpose-confirmation flow. workspace_get remains its canonical
			// read-side catalog entry; Local MCP retains the direct command.
			wantFound = 1
		}
		if found != wantFound {
			t.Fatalf("adapter %d branch tool count=%d want=%d", i, found, wantFound)
		}
		opened := callToolMap(t, f.ctx, client, "project_open", map[string]any{"projectId": f.projectID})
		ids[i] = opened["aiSessionId"].(string)
		workspace := callToolMap(t, f.ctx, client, "workspace_get", map[string]any{"aiSessionId": ids[i], "include": []string{"BRANCHES"}})
		rawSchema, _ := json.Marshal(workspaceOutputSchema)
		var contract jsonschema.Schema
		if err := json.Unmarshal(rawSchema, &contract); err != nil {
			t.Fatal(err)
		}
		resolved, err := contract.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(workspace); err != nil {
			t.Fatalf("adapter %d workspace wire/schema mismatch: %v", i, err)
		}
		result := workspace["result"].(map[string]any)
		if result["selectedFeatureArea"] != nil || workspace["context"].(map[string]any)["featureAreaId"] != nil {
			t.Fatalf("startup read selected FeatureArea: %#v", workspace)
		}
		development := result["development"].(map[string]any)
		inventory := result["branches"].(map[string]any)
		if development["selectionRequired"] != true || inventory["developmentBranchSelectionRequired"] != true || len(development["repositories"].([]any)) != len(inventory["repositories"].([]any)) {
			t.Fatalf("compact development/inventory disagree: %#v %#v", development, inventory)
		}
	}
	// Local retains the canonical exact branch-selection command. ChatGPT has
	// deliberately retired that command in favor of PROJECT_SETUP confirmation;
	// both adapters still read the resulting Project mapping below.
	args := map[string]any{"aiSessionId": ids[1], "repositoryId": f.repositoryID, "developmentRef": "refs/heads/dev/v0.1.0", "requestId": "branch-shared-selection"}
	selected := callToolMap(t, f.ctx, local, "development_branch_select", args)
	replay := callToolMap(t, f.ctx, local, "development_branch_select", args)
	if !reflect.DeepEqual(selected, replay) {
		t.Fatalf("canonical branch-selection replay changed result: %+v/%+v", selected, replay)
	}
	args["developmentRef"] = "refs/heads/experiment/test"
	if err := callToolErrorText(t, f.ctx, local, "development_branch_select", args); !strings.Contains(err, "REQUEST_CONFLICT") {
		t.Fatalf("conflict=%s", err)
	}
	for i, client := range sessions {
		reopened := callToolMap(t, f.ctx, client, "project_open", map[string]any{"projectId": f.projectID, "aiSessionId": ids[i]})
		if reopened["aiSessionId"] != ids[i] {
			t.Fatal("project_open changed durable Session")
		}
		workspace := callToolMap(t, f.ctx, client, "workspace_get", map[string]any{"aiSessionId": ids[i], "include": []string{"BRANCHES"}})
		result := workspace["result"].(map[string]any)
		if result["selectedFeatureArea"] != nil {
			t.Fatalf("branch read selected FeatureArea: %#v", result)
		}
		development := result["branches"].(map[string]any)
		compact := result["development"].(map[string]any)
		repo := development["repositories"].([]any)[0].(map[string]any)
		compactRepo := compact["repositories"].([]any)[0].(map[string]any)
		if development["developmentBranchSelectionRequired"] != false || compact["selectionRequired"] != false || repo["developmentRef"] != "refs/heads/dev/v0.1.0" || compactRepo["developmentRef"] != repo["developmentRef"] || repo["refChanged"] != false || repo["repositoryCheckoutMatchesDevelopmentRef"] != false {
			t.Fatalf("selected projection=%#v compact=%#v", development, compact)
		}
	}

	after := git("show-ref", "--heads") + git("symbolic-ref", "HEAD") + git("status", "--porcelain=v1")
	if before != after {
		t.Fatal("branch selection mutated repository")
	}
	git("branch", "-D", "dev/v0.1.0")
	missing := callToolMap(t, f.ctx, local, "workspace_get", map[string]any{"aiSessionId": ids[1], "include": []string{"BRANCHES"}})["result"].(map[string]any)["branches"].(map[string]any)["repositories"].([]any)[0].(map[string]any)
	if missing["developmentRefStatus"] != "MISSING" || missing["statusCode"] != "DEVELOPMENT_REF_MISSING" || missing["developmentRef"] != "refs/heads/dev/v0.1.0" {
		t.Fatalf("missing projection=%+v", missing)
	}
	args["developmentRef"] = "refs/heads/dev/v0.1.0"
	if replay := callToolMap(t, f.ctx, local, "development_branch_select", args); !reflect.DeepEqual(replay, selected) {
		t.Fatal("deleted-ref retry changed receipt")
	}
	args["requestId"] = "missing-new-selection"
	if err := callToolErrorText(t, f.ctx, local, "development_branch_select", args); !strings.Contains(err, "DEVELOPMENT_BRANCH_NOT_FOUND") {
		t.Fatalf("missing error=%s", err)
	}
}
