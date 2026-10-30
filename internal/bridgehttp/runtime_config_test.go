package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlplane"
)

func TestCodexRuntimeInventoryHTTPAppliesCompleteCASnapshot(t *testing.T) {
	plane, err := controlplane.Open(context.Background(), controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}

	call := func(method string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		request := httptest.NewRequest(method, "/v1/runtime/codex-inventory", bytes.NewReader(payload))
		setTestLoopbackTransport(request)
		recorder := httptest.NewRecorder()
		server.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	initial := call(http.MethodGet, nil)
	if initial.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", initial.Code, initial.Body.String())
	}
	var before codexRuntimeInventory
	if err := json.Unmarshal(initial.Body.Bytes(), &before); err != nil {
		t.Fatal(err)
	}
	configs := codexruntime.Default().Clone().ModelConfigs
	for i := range configs {
		configs[i].Enabled = configs[i].ModelID == "gpt-6-astra"
		if configs[i].ModelID == "gpt-6-astra" {
			configs[i].DefaultReasoningEffort = "xhigh"
		}
	}
	tier := "priority"
	configs = append(configs, codexruntime.CodingAgentModelConfig{ModelID: "gpt-new-model", Enabled: true, SupportedReasoningEfforts: []string{"low", "high", "future"}, DefaultReasoningEffort: "high", FastServiceTier: &tier})
	candidate := applyCodexRuntimeInventoryRequest{ExpectedRevision: before.Revision, CodingAgentModels: configs}
	applied := call(http.MethodPut, candidate)
	if applied.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", applied.Code, applied.Body.String())
	}
	got := plane.CodexRuntime.Snapshot()
	if !reflect.DeepEqual(got.Allowlist.ModelConfigs, candidate.CodingAgentModels) || !reflect.DeepEqual(got.Allowlist.Models, []string{"gpt-6-astra", "gpt-new-model"}) {
		t.Fatalf("runtime snapshot=%+v", got)
	}
	selected, err := (codexruntime.NextRunRuntimeConfig{Model: "gpt-new-model", CodexFastMode: true}).Normalize(got.Allowlist)
	if err != nil || selected.ReasoningEffort != "high" || selected.ServiceTier != "priority" {
		t.Fatalf("discovered model selection=%+v err=%v", selected, err)
	}
	stale := call(http.MethodPut, candidate)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale PUT status=%d body=%s", stale.Code, stale.Body.String())
	}
	invalidConfigs := got.Allowlist.Clone().ModelConfigs
	invalidConfigs[0].ModelID = "invalid model id"
	invalid := call(http.MethodPut, applyCodexRuntimeInventoryRequest{ExpectedRevision: got.Revision, CodingAgentModels: invalidConfigs})
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT status=%d body=%s", invalid.Code, invalid.Body.String())
	}
}
