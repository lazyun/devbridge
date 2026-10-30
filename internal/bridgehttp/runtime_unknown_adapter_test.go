package bridgehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Strict handler decoding must reject all redispatch flags before reaching Core.
func TestUnknownRecoveryHTTPRejectsRedispatchFlags(t *testing.T) {
	server := &Server{}
	for _, flag := range []string{"force", "retry", "assumeNotConsumed"} {
		t.Run(flag, func(t *testing.T) {
			body := `{"aiSessionId":"session","recovery":{"runtimeRequestId":"request","runId":"run","commandRequestId":"command","action":"CONTINUE_WAITING","` + flag + `":true}}`
			request := httptest.NewRequest(http.MethodPost, "/v1/runs/run/agent-requests/recovery", strings.NewReader(body))
			request.SetPathValue("id", "run")
			response := httptest.NewRecorder()
			server.recoverAgentRequest(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "unknown field") {
				t.Fatalf("flag reached Core: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
