package agentruntime

import (
	"encoding/json"
	"testing"
)

// MCP presentation relies on the installed method contract, not one example.
// New non-object methods must force an explicit presentation contract decision.
func TestSupportedRuntimeParamsAreObjects(t *testing.T) {
	if err := initContract(); err != nil {
		t.Fatal(err)
	}
	for method, c := range contract.Methods {
		t.Run(method, func(t *testing.T) {
			for kind, source := range map[string]json.RawMessage{"request": c.Request, "response": c.Response} {
				var schema map[string]any
				if err := json.Unmarshal(source, &schema); err != nil {
					t.Fatal(err)
				}
				if schema["type"] != "object" {
					t.Fatalf("%s contract changed: %s", kind, source)
				}
				for _, raw := range []string{`[]`, `null`, `"text"`, `42`, `true`} {
					if Validate(method, kind, json.RawMessage(raw)) == nil {
						t.Fatalf("accepted %s %s", kind, raw)
					}
				}
			}
			service, session, _ := newSession(t)
			before := requestFixture(method)
			if err := session.Observe(Event{Method: method, RequestID: json.RawMessage(`17`), Params: before}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := service.Read(session.ctx, "run")
			if err != nil || len(snapshot.Requests) != 1 {
				t.Fatalf("request not retained: %+v %v", snapshot, err)
			}
			request := snapshot.Requests[0]
			expectedState := "PENDING"
			if request.Kind == "CODEX_REQUEST" {
				expectedState = "WAITING_FOR_USER"
			}
			if request.State != expectedState || string(request.Payload) != string(before) || string(request.RequestID) != "17" {
				t.Fatalf("request changed: %+v", request)
			}
		})
	}
}
