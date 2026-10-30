package bridgehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lazyun/devbridge/internal/contextprobe"
)

func postOperatorJSON(t *testing.T, handler http.Handler, target string, input any, wantStatus int, output any) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(raw))
	request.RemoteAddr = "127.0.0.1:45678"
	request.Host = "127.0.0.1:8080"
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(request.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		var failure any
		_ = json.NewDecoder(response.Body).Decode(&failure)
		t.Fatalf("POST %s status=%d want=%d response=%v", target, response.StatusCode, wantStatus, failure)
	}
	if output != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
		if err = json.NewDecoder(response.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectInspectionHTTPPrepareConfirmAndLegacyWriteClosure(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()
	config, err := fixture.plane.CodexConfigs.Get(context.Background(), fixture.configID)
	if err != nil {
		t.Fatal(err)
	}
	input := contextprobe.PrepareInspectionInput{AISessionID: fixture.sessionID, RequestID: "http-project-inspection", CodexInstruction: "Inspect the confirmed repository.", Backend: config.Selection("http-test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: fixture.repositoryID, Targets: []contextprobe.TargetSelection{{RepositoryID: fixture.repositoryID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}}}}
	var prepared contextprobe.InspectionConfirmationPlan
	postOperatorJSON(t, fixture.handler, "/v1/project-inspections/prepare", input, http.StatusOK, &prepared)
	if prepared.PayloadSHA256 == "" || prepared.InspectionID == "" || len(prepared.Targets) != 1 {
		t.Fatalf("prepared=%+v", prepared)
	}
	var confirmed map[string]any
	postOperatorJSON(t, fixture.handler, "/v1/project-inspections/confirm", contextprobe.ConfirmInspectionInput{Plan: prepared, PayloadSHA256: prepared.PayloadSHA256}, http.StatusCreated, &confirmed)
	if confirmed["ID"] != prepared.InspectionID || confirmed["TaskID"] != prepared.TaskID {
		t.Fatalf("confirmed=%v", confirmed)
	}
	postOperatorJSON(t, fixture.handler, "/v1/context-probes", map[string]any{}, http.StatusConflict, nil)
	postOperatorJSON(t, fixture.handler, "/v1/context-probes/"+prepared.InspectionID+"/start", map[string]string{"ai_session_id": fixture.sessionID}, http.StatusConflict, nil)
}
