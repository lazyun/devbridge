package consoleapi

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/run"
	"strings"
	"testing"
)

func TestRunRuntimeDetailPreservesObjectsBoundsAndSecrets(t *testing.T) {
	value := run.Run{ID: "run", AgentRuntime: &agentruntime.Snapshot{Identity: agentruntime.Identity{RunID: "run"}, Requests: []agentruntime.Request{{Payload: json.RawMessage(`{"api_key":"secret","questions":[{"id":"q","question":"Continue?"}]}`)}}, Items: []agentruntime.Item{{OutputTail: strings.Repeat("x", 100000)}}}}
	got := boundedRunRuntime(value)
	if got == nil || len(got.Items[0].OutputTail) > agentruntime.MaxOutput+32 || strings.Contains(string(got.Requests[0].Payload), "secret") {
		t.Fatalf("unbounded or unsafe runtime: %+v", got)
	}
	var payload map[string]any
	if json.Unmarshal(got.Requests[0].Payload, &payload) != nil || payload["api_key"] != "[REDACTED]" || payload["questions"] == nil {
		t.Fatalf("runtime payload lost object semantics: %s", got.Requests[0].Payload)
	}
	value.AgentRuntime.RunID = "foreign"
	if boundedRunRuntime(value) != nil {
		t.Fatal("borrowed another Run runtime")
	}
}
