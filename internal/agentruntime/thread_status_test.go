package agentruntime

import "testing"

func TestNativeThreadStatusIsScopedObservationOnly(t *testing.T) {
	_, s, store := newSession(t)
	emit(t, s, "thread/status/changed", map[string]any{"threadId": "thread", "status": map[string]any{"type": "active", "activeFlags": []string{"waitingOnUserInput"}}})
	v, _ := store.Read(s.ctx, "run")
	if v.NativeThreadStatus == nil || v.NativeThreadStatus.Type != "active" || len(v.NativeThreadStatus.ActiveFlags) != 1 || v.TurnStatus != "" || len(v.Requests) != 0 {
		t.Fatalf("status inferred turn or request authority: %+v", v)
	}
	before := v.Sequence
	for _, params := range []string{`{"threadId":"foreign","status":{"type":"idle"}}`, `{"status":{"type":"idle"}}`, `{"threadId":"thread","status":{"type":"active","activeFlags":["unknown"]}}`} {
		if err := s.Observe(Event{Method: "thread/status/changed", Params: []byte(params)}); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	v, _ = store.Read(s.ctx, "run")
	if v.NativeThreadStatus.Type != "active" || v.Sequence > before+1 {
		t.Fatalf("foreign/invalid status changed observation: %+v", v)
	}
}
