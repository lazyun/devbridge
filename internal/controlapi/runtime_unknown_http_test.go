package controlapi_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/controlapi"
)

// Real local HTTP handler, canonical facade and SQLite, with the same protocol
// fault used by the production owner tests. No final state is inserted.
func TestRuntimeUnknownProductionHTTPAdapter(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_ASK_USER", "turn-implementation")
	t.Setenv("DEVBRIDGE_CODING_V1_DROP_RESOLVED", "1")
	t.Setenv("DEVBRIDGE_CODING_V1_HOLD_AFTER_CONSUMPTION", "1")
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		q, _ := waitUserRequest(t, f, f.task, "turn-implementation")
		if _, err := f.facade.SubmitAgentUserDecisionForSession(f.ctx, f.session, explicitDecision(t, f, q, "RESPOND", "yes")); err != nil {
			t.Fatal(err)
		}
		q = waitUnknownRequest(t, f, q, false)
		server, err := bridgehttp.New(f.plane, false)
		if err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server.Handler())
		defer httpServer.Close()
		call := func(method, path string, body any) (int, []byte) {
			data, _ := json.Marshal(body)
			req, err := http.NewRequest(method, httpServer.URL+path, bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			res, err := httpServer.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			result, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			return res.StatusCode, result
		}
		read := func() agentruntime.Request {
			status, b := call(http.MethodGet, "/ui/api/runs/"+q.RunID+"/session?project_id="+f.project+"&task_id="+f.task, nil)
			if status != http.StatusOK {
				t.Fatalf("HTTP read %d %s", status, b)
			}
			var body struct {
				Session agentruntime.SessionSnapshot `json:"session"`
			}
			if err := json.Unmarshal(b, &body); err != nil {
				t.Fatal(err)
			}
			for _, v := range body.Session.Runtime.Requests {
				if v.ID == q.ID {
					return v
				}
			}
			t.Fatal("HTTP request missing")
			return agentruntime.Request{}
		}
		first := read()
		if first.Projection == nil || !reflect.DeepEqual(first.Projection.AvailableActions, []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}) || first.Projection.ResendAllowed || first.Projection.DisplayTitle != "回答状态无法确认" {
			t.Fatalf("HTTP projection %+v", first.Projection)
		}
		before, _ := json.Marshal(first.UserDecision)
		res := agentruntime.Resolution{Identity: q.Identity, RuntimeRequestID: q.ID, UserDecisionID: q.UserDecision.ID, PayloadSHA256: q.UserDecision.PayloadSHA256, RequestID: q.RequestID, ItemID: q.ItemID}
		status, b := call(http.MethodPost, "/ui/api/runs/"+q.RunID+"/agent-requests/resolve", res)
		if status < 400 || !bytes.Contains(b, []byte("RESPONSE_DISPOSITION_UNKNOWN")) {
			t.Fatalf("HTTP resend %d %s", status, b)
		}
		recovery := controlapi.AgentRequestRecoveryInput{Identity: q.Identity, RuntimeRequestID: q.ID, CommandRequestID: "http-wait", Action: "CONTINUE_WAITING"}
		for i := 0; i < 3; i++ {
			status, b = call(http.MethodPost, "/ui/api/runs/"+q.RunID+"/agent-requests/recovery", recovery)
			if status != http.StatusOK {
				t.Fatalf("HTTP wait %d %s", status, b)
			}
		}
		forged, _ := json.Marshal(recovery)
		var fields map[string]any
		_ = json.Unmarshal(forged, &fields)
		fields["force"] = true
		status, b = call(http.MethodPost, "/ui/api/runs/"+q.RunID+"/agent-requests/recovery", fields)
		if status < 400 {
			t.Fatalf("force admitted %d %s", status, b)
		}
		current := read()
		after, _ := json.Marshal(current.UserDecision)
		if !bytes.Equal(before, after) || current.Projection.ResponseDisposition != "UNKNOWN" || len(current.DispatchAttempts) != 1 {
			t.Fatal("HTTP refresh/reject/wait mutated answer")
		}
		recovery.CommandRequestID = "http-cancel"
		recovery.Action = "CANCEL_OPERATION"
		status, b = call(http.MethodPost, "/ui/api/runs/"+q.RunID+"/agent-requests/recovery", recovery)
		if status != http.StatusOK {
			t.Fatalf("HTTP cancellation %d %s", status, b)
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			current = read()
			if current.State == "CANCELED" {
				after, _ = json.Marshal(current.UserDecision)
				if !bytes.Equal(before, after) || current.Projection.ResponseDisposition != "UNKNOWN" {
					t.Fatal("HTTP cancel rewrote consumption")
				}
				return false
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("HTTP cancellation did not quiesce")
		return false
	})
}
