package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReviewReasoningEffortFreshSchemaMatchesRuntimeAdmission(t *testing.T) {
	full := codexruntime.Default()
	sol, ok := full.ModelConfig("gpt-5.6-sol")
	if !ok || !reflect.DeepEqual(sol.SupportedReasoningEfforts, codexruntime.AllReasoningEfforts) {
		t.Fatalf("runtime model efforts=%v universe=%v", sol.SupportedReasoningEfforts, codexruntime.AllReasoningEfforts)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, url := startTestServer(t, readyService(t))
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "reasoning-parity", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: url + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 5 * time.Second}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	if tools["task_review_start"] != nil {
		t.Fatal("retired direct Review start is model-visible")
	}
	prepare := tools["review_prepare"]
	if prepare == nil {
		t.Fatal("current Review confirmation preparation tool missing")
	}
	raw, err := json.Marshal(prepare.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("Review prepare input schema is no longer closed")
	}
	encoded := string(raw)
	for _, effort := range codexruntime.AllReasoningEfforts {
		if strings.Contains(encoded, `"reasoningEffort":{"enum":["`+effort) {
			t.Fatalf("Review prepare schema captured runtime authority at %q", effort)
		}
	}
}

func TestReviewSchemaDoesNotCaptureRuntimeAuthority(t *testing.T) {
	hidden := struct{ mcpserver.BridgeService }{readyService(t)}
	if _, err := mcpserver.New(mcpserver.Config{}, hidden); err != nil {
		t.Fatalf("stable schema required runtime authority: %v", err)
	}
	invalid := &invalidReasoningService{StaticService: readyService(t)}
	if _, err := mcpserver.New(mcpserver.Config{}, invalid); err != nil {
		t.Fatalf("stable schema read invalid runtime authority: %v", err)
	}
}

type invalidReasoningService struct{ *mcpserver.StaticService }

func (*invalidReasoningService) ExecutionAllowlist() codexruntime.Allowlist {
	return codexruntime.Allowlist{Models: []string{"gpt-5.6-sol"}}
}
