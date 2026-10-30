package bridgehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRecoveryStatusHTTPRejectsClientFactsBeforeCore(t *testing.T) {
	s := &Server{}
	for _, field := range []string{"engineeringDisposition", "admission", "report", "assessment", "force"} {
		t.Run(field, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/recovery-status/r/resolve", strings.NewReader(`{"aiSessionId":"s","command":{"id":"r","`+field+`":true}}`))
			r.SetPathValue("id", "r")
			w := httptest.NewRecorder()
			s.resolveRecoveryStatus(w, r)
			if w.Code != 400 || !strings.Contains(w.Body.String(), "unknown field") {
				t.Fatalf("untrusted facts reached Core: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestInspectionRecoveryContinuationHTTPRejectsOutcomeInjectionBeforeCore(t *testing.T) {
	s := &Server{}
	for _, field := range []string{"engineeringDisposition", "inspectionResult", "findings", "targetSet", "force"} {
		t.Run(field, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/recovery-status/r/continue-inspection", strings.NewReader(`{"aiSessionId":"s","command":{"recoveryStatusId":"r","requestId":"q","instruction":"exact","payloadHash":"hash","expectedRuntimeEpoch":"epoch","`+field+`":true}}`))
			r.SetPathValue("id", "r")
			w := httptest.NewRecorder()
			s.continueInspectionRecovery(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "unknown field") {
				t.Fatalf("untrusted continuation facts reached Core: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
