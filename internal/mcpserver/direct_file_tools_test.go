package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type directFileAdapterService struct{ *mcpserver.StaticService }

func (s *directFileAdapterService) DirectFileEnable(_ context.Context, in directfile.Scope) (directfile.State, error) {
	return directfile.State{Enabled: true, FeatureAreaID: in.FeatureAreaID, WorkflowID: in.WorkflowID}, nil
}
func (*directFileAdapterService) DirectFileDisable(context.Context, directfile.Scope) (directfile.State, error) {
	return directfile.State{}, nil
}
func (*directFileAdapterService) DirectFileFileList(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}
func (*directFileAdapterService) DirectFileFileRead(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}
func (*directFileAdapterService) DirectFileFileCreate(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}
func (*directFileAdapterService) DirectFileFileReplace(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}
func (*directFileAdapterService) DirectFileFileDelete(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}
func (*directFileAdapterService) DirectFileFileMove(context.Context, directfile.Input) (directfile.Result, error) {
	return directfile.Result{}, directfile.Failure(directfile.NotEnabled, "Direct File is disabled")
}

func TestDirectFileToolsExposeBoundedSchemasAndStructuredErrors(t *testing.T) {
	service := &directFileAdapterService{StaticService: readyService(t)}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "direct-file-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]*sdkmcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"direct_file_disable", "file_list", "file_read", "file_write"} {
		if byName[name] == nil {
			t.Fatalf("direct file tool %s is absent", name)
		}
	}
	if byName["direct_file_enable"] != nil {
		t.Fatal("retired direct_file_enable is exposed to ChatGPT; Task v2 requires explicit confirmation start")
	}
	var createSchema map[string]any
	raw, _ := json.Marshal(byName["file_write"].InputSchema)
	if err := json.Unmarshal(raw, &createSchema); err != nil {
		t.Fatal(err)
	}
	var contentBranch map[string]any
	for _, raw := range createSchema["oneOf"].([]any) {
		branch := raw.(map[string]any)
		if branch["properties"].(map[string]any)["action"].(map[string]any)["const"] == "CREATE" {
			contentBranch = branch
		}
	}
	if contentBranch == nil {
		t.Fatal("CREATE branch absent")
	}
	createSchema = contentBranch
	content := createSchema["properties"].(map[string]any)["content"].(map[string]any)
	if _, boundedAtRoot := content["maxLength"]; boundedAtRoot {
		t.Fatalf("content limit must depend on contentEncoding: %#v", content)
	}
	branches, ok := createSchema["allOf"].([]any)
	if !ok || len(branches) < 1 {
		t.Fatalf("content encoding schema branches = %#v", createSchema["allOf"])
	}
	branch := branches[0].(map[string]any)
	thenContent := branch["then"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)
	elseContent := branch["else"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)
	if thenContent["maxLength"] != float64(directfile.MaxBase64EncodedBytes) || elseContent["maxLength"] != float64(directfile.MaxWriteBytes) {
		t.Fatalf("content bounds = base64:%v utf8:%v", thenContent["maxLength"], elseContent["maxLength"])
	}
	required, ok := createSchema["required"].([]any)
	if !ok {
		t.Fatal("file_create required fields are absent")
	}
	for _, field := range required {
		if field == "workflowComplete" {
			t.Fatal("workflowComplete must remain optional")
		}
	}
	var outputSchema map[string]any
	raw, _ = json.Marshal(byName["file_list"].OutputSchema)
	if err := json.Unmarshal(raw, &outputSchema); err != nil {
		t.Fatal(err)
	}
	if outputSchema["properties"].(map[string]any)["result"].(map[string]any)["properties"].(map[string]any)["code"] == nil {
		t.Fatalf("direct-file output schema omits structured error code: %#v", outputSchema)
	}
	// The production server rejects an unbound Session before a file tool is
	// invoked. Transport delegation and its direct-file structured error are
	// exercised separately with an isolated admitted server below.
}
