package agentruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu     sync.Mutex
	state  Snapshot
	reject bool
}

func (m *memoryStore) Open(_ context.Context, id Identity) (Identity, error) {
	id.ProjectID = "project"
	id.FeatureAreaID = "area"
	id.TaskID = "task"
	return id, nil
}
func (m *memoryStore) Save(_ context.Context, expected uint64, s Snapshot, resolution bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.reject && resolution {
		return ErrStale
	}
	if m.state.Sequence != expected {
		return ErrStale
	}
	m.state = clone(s)
	return nil
}
func (m *memoryStore) Read(_ context.Context, _ string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.state), nil
}
func newSession(t *testing.T) (*Service, *Session, *memoryStore) {
	t.Helper()
	m := &memoryStore{}
	service := NewService(m)
	s := service.NewTurn(context.Background(), "run", "thread", false)
	if err := s.Bind("turn"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return service, s, m
}
func waitRuntimeSnapshot(t *testing.T, service *Service, ready func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		v, err := service.Read(context.Background(), "run")
		if err != nil {
			t.Fatal(err)
		}
		if ready(v) {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("durable runtime observation timed out: %+v", v)
		}
		time.Sleep(time.Millisecond)
	}
}
func emit(t *testing.T, s *Session, method string, p any) {
	t.Helper()
	before, _ := s.service.Read(context.Background(), "run")
	at := time.Now().UTC()
	params := raw(p)
	if err := s.Observe(Event{Method: method, Params: params, ReceivedAt: at}); err != nil {
		t.Fatal(err)
	}
	var owner struct {
		ThreadID string `json:"threadId"`
		TurnID   string `json:"turnId"`
		Turn     struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &owner)
	if owner.Turn.ID != "" {
		owner.TurnID = owner.Turn.ID
	}
	// Stale and post-terminal events intentionally have no durable effect.
	if owner.ThreadID != "" && owner.ThreadID != before.ThreadID || owner.TurnID != "" && owner.TurnID != before.TurnID || before.TurnStatus == "completed" || before.TurnStatus == "failed" || before.TurnStatus == "interrupted" {
		return
	}
	// A duplicate lifecycle acknowledgement is intentionally a no-op and does
	// not advance durable activity. Drain the real queue instead of treating
	// timestamp advancement as proof that an event was processed.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Flush(ctx); err != nil {
		t.Fatal(err)
	}
}

func requestFixture(method string) json.RawMessage {
	p := map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "item", "startedAtMs": 1, "cwd": "/tmp"}
	switch method {
	case "item/permissions/requestApproval":
		p["permissions"] = map[string]any{"network": map[string]any{"enabled": true}, "fileSystem": map[string]any{"read": []string{"/tmp/a", "/tmp/b"}}}
	case "item/tool/requestUserInput":
		p["isBlocking"] = true
		p["questions"] = []any{map[string]any{"id": "q", "header": "Choose", "question": "Which?", "isOther": true, "options": []any{map[string]any{"label": "one", "description": "first"}}}}
	case "mcpServer/elicitation/request":
		p["serverName"] = "fixture"
		p["mode"] = "form"
		p["message"] = "Name"
		p["requestedSchema"] = map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "minLength": 1}}, "required": []string{"name"}}
	}
	return raw(p)
}
func addRequest(t *testing.T, s *Session, method string) Request {
	t.Helper()
	if err := s.Observe(Event{Method: method, Params: requestFixture(method), RequestID: raw("server-id")}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.service.Read(context.Background(), "run")
	if len(v.Requests) != 1 {
		t.Fatalf("request not persisted: %+v", v)
	}
	return v.Requests[0]
}
func TestOfficialRequestsPersistIntendAndConfirm(t *testing.T) {
	for method, response := range map[string]string{
		"item/commandExecution/requestApproval": `{"decision":"acceptForSession"}`,
		"item/fileChange/requestApproval":       `{"decision":"decline"}`,
		"item/permissions/requestApproval":      `{"permissions":{"fileSystem":{"read":["/tmp/a"]}},"scope":"turn"}`,
	} {
		t.Run(method, func(t *testing.T) {
			service, s, _ := newSession(t)
			r := addRequest(t, s, method)
			in := Resolution{Identity: r.Identity, RequestID: r.RequestID, ItemID: r.ItemID, Response: json.RawMessage(response)}
			v, err := service.Resolve(context.Background(), in)
			if err != nil || v.Requests[0].State != "INTENDED" || v.WaitingState != WaitingState(method) {
				t.Fatalf("resolution: %+v %v", v, err)
			}
			got, err := s.Await(context.Background(), r.RequestID)
			if err != nil || string(got) != response {
				t.Fatalf("dispatch: %s %v", got, err)
			}
			if _, err = service.Resolve(context.Background(), in); !errors.Is(err, ErrStale) {
				t.Fatal("duplicate resolution accepted", err)
			}
			s.Delivered(r.RequestID, nil)
			v, _ = service.Read(context.Background(), "run")
			if v.Requests[0].State != "SENT" {
				t.Fatal(v)
			}
			emit(t, s, "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "server-id"})
			emit(t, s, "serverRequest/resolved", map[string]any{"threadId": "thread", "requestId": "server-id"})
			v, _ = service.Read(context.Background(), "run")
			if v.Requests[0].State != "CONFIRMED_RESOLVED" || v.WaitingState != "RUNNING" {
				t.Fatal(v)
			}
		})
	}
}
func TestPlanItemsAndTerminalAreOnlyObservations(t *testing.T) {
	service, s, _ := newSession(t)
	steps := []PlanStep{{"inspect", "completed"}, {"implement", "inProgress"}, {"test", "pending"}, {"review", "pending"}, {"finish", "pending"}}
	emit(t, s, "turn/plan/updated", map[string]any{"threadId": "thread", "turnId": "turn", "plan": steps})
	item := map[string]any{"id": "cmd", "type": "commandExecution", "status": "inProgress", "command": "go test ./...", "cwd": "/tmp", "commandActions": []any{}}
	emit(t, s, "item/started", map[string]any{"threadId": "thread", "turnId": "turn", "item": item})
	emit(t, s, "item/commandExecution/outputDelta", map[string]any{"threadId": "thread", "turnId": "turn", "itemId": "cmd", "delta": strings.Repeat("a", MaxOutput*3) + "<script>"})
	item["status"] = "completed"
	item["exitCode"] = 0
	emit(t, s, "item/completed", map[string]any{"threadId": "thread", "turnId": "turn", "item": item})
	v, _ := service.Read(context.Background(), "run")
	if v.LatestPlan.Total != 5 || v.LatestPlan.Completed != 1 || v.CurrentItem.CompletedAt == nil || len(v.CurrentItem.OutputTail) > MaxOutput || v.TurnStatus == "completed" {
		t.Fatal(v)
	}
	emit(t, s, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "failed", "error": map[string]any{"message": "network failed", "codexErrorInfo": map[string]any{"httpConnectionFailed": map[string]any{"httpStatusCode": 401}}, "additionalDetails": "detail"}}})
	before, _ := service.Read(context.Background(), "run")
	emit(t, s, "turn/plan/updated", map[string]any{"threadId": "thread", "turnId": "turn", "plan": []PlanStep{{"late", "completed"}}})
	after, _ := service.Read(context.Background(), "run")
	if before.Sequence != after.Sequence || after.LastError == nil || after.LastError.Summary != "Codex Turn failed" {
		t.Fatal(after)
	}
}

func TestContextCompactionItemMetadataIsExactObject(t *testing.T) {
	service, s, _ := newSession(t)
	item := json.RawMessage(`{"id":"compact-1","type":"contextCompaction"}`)
	emit(t, s, "item/started", map[string]any{
		"threadId": "thread",
		"turnId":   "turn",
		"item":     item,
	})

	v, err := service.Read(context.Background(), "run")
	if err != nil {
		t.Fatal(err)
	}
	if v.CurrentItem == nil || len(v.Items) != 1 {
		t.Fatalf("context compaction item missing: %+v", v)
	}
	if v.CurrentItem.Status != "inProgress" || v.Items[0].Status != "inProgress" {
		t.Fatalf("context compaction lifecycle status = current %q, item %q", v.CurrentItem.Status, v.Items[0].Status)
	}
	for label, metadata := range map[string]json.RawMessage{
		"currentItem": v.CurrentItem.Metadata,
		"items[0]":    v.Items[0].Metadata,
	} {
		if !bytes.Equal(metadata, item) {
			t.Fatalf("%s metadata = %s, want exact object %s", label, metadata, item)
		}
		var object map[string]any
		if err := json.Unmarshal(metadata, &object); err != nil || len(object) != 2 || object["id"] != "compact-1" || object["type"] != "contextCompaction" {
			t.Fatalf("%s metadata is not the exact contextCompaction object: %s (%v)", label, metadata, err)
		}
	}
}

func TestStaleIdentityAndCancellationFence(t *testing.T) {
	service, s, m := newSession(t)
	r := addRequest(t, s, "item/commandExecution/requestApproval")
	in := Resolution{Identity: r.Identity, RequestID: r.RequestID, ItemID: r.ItemID, Response: raw(map[string]any{"decision": "accept"})}
	bad := in
	bad.TurnID = "new-turn"
	if _, err := service.Resolve(context.Background(), bad); err == nil {
		t.Fatal("stale accepted")
	}
	m.reject = true
	if _, err := service.Resolve(context.Background(), in); err == nil {
		t.Fatal("cancellation fence ignored")
	}
	v, _ := service.Read(context.Background(), "run")
	if v.Requests[0].State != "PENDING" {
		t.Fatal(v)
	}
	emit(t, s, "turn/plan/updated", map[string]any{"threadId": "thread", "turnId": "old", "plan": []PlanStep{{"bad", "completed"}}})
	emit(t, s, "turn/completed", map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
	v, _ = service.Read(context.Background(), "run")
	if v.LatestPlan != nil || v.WaitingState != "RUNNING" || v.Requests[0].State != "CLOSED_TURN" {
		t.Fatal(v)
	}
}
func TestTransportLossPreservesIntentWithoutConfirmation(t *testing.T) {
	service, s, _ := newSession(t)
	r := addRequest(t, s, "item/fileChange/requestApproval")
	_, err := service.Resolve(context.Background(), Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "accept"})})
	if err != nil {
		t.Fatal(err)
	}
	s.Delivered(r.RequestID, errors.New("broken pipe"))
	s.Close()
	v, _ := service.Read(context.Background(), "run")
	if v.Requests[0].State != "CONNECTION_LOST" || v.Requests[0].Delivery != "RESPONSE_UNCERTAIN" || len(v.Requests[0].Resolution) == 0 || v.TurnStatus != "" {
		t.Fatal(v)
	}
	if _, err = service.Resolve(context.Background(), Resolution{Identity: r.Identity}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}
func TestPermissionEscalationAndUnknownResponseRejected(t *testing.T) {
	_, s, _ := newSession(t)
	r := addRequest(t, s, "item/permissions/requestApproval")
	for _, response := range []string{`{"permissions":{"fileSystem":{"write":["/tmp/a"]}}}`, `{"permissions":{"fileSystem":{"read":["/"]}}}`, `{"permissions":{},"scope":"forever"}`, `{"permissions":{},"extra":true}`} {
		if validateResponse(r, []byte(response), false) == nil {
			t.Fatal("accepted escalation", response)
		}
	}
}
func TestUnknownRequestAndUnknownFieldsForwardCompatible(t *testing.T) {
	service, s, _ := newSession(t)
	p := requestFixture("item/fileChange/requestApproval")
	var v map[string]any
	_ = json.Unmarshal(p, &v)
	v["futureField"] = true
	if err := Validate("item/fileChange/requestApproval", "request", raw(v)); err != nil {
		t.Fatal(err)
	}
	emit(t, s, "future/notification", map[string]any{"threadId": "thread", "turnId": "turn"})
	if err := s.Observe(Event{Method: "future/request", RequestID: raw(1), Params: raw(v)}); err != nil {
		t.Fatal(err)
	}
	state, _ := service.Read(context.Background(), "run")
	if len(state.Diagnostics) != 2 || state.LastWarning == nil || len(state.Requests) != 0 {
		t.Fatal(state)
	}
}
func TestGeneratedContractCoverage(t *testing.T) {
	if err := initContract(); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"turn/started", "turn/completed", "turn/plan/updated", "turn/diff/updated", "item/started", "item/completed", "item/commandExecution/outputDelta", "warning", "configWarning", "error", "serverRequest/resolved", "thread/tokenUsage/updated"} {
		if !contains(contract.Notifications, method) {
			t.Fatal(method)
		}
	}
	for method := range contract.Methods {
		if !contains(contract.ServerRequests, method) {
			t.Fatal(method)
		}
		if err := Validate(method, "request", requestFixture(method)); err != nil {
			t.Fatal(method, err)
		}
	}
}
func TestResponseVsInterruptRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		service, s, _ := newSession(t)
		r := addRequest(t, s, "item/fileChange/requestApproval")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = service.Resolve(context.Background(), Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "accept"})})
		}()
		go func() {
			defer wg.Done()
			_ = s.Observe(Event{Method: "turn/completed", Params: raw(map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}})})
		}()
		wg.Wait()
		v := waitRuntimeSnapshot(t, service, func(v Snapshot) bool { return v.TurnStatus == "interrupted" })
		if v.Requests[0].State != "CLOSED_TURN" {
			t.Fatal(v)
		}
	}
}

func TestDispatchRechecksIntentAfterCancellationOrOfficialCleanup(t *testing.T) {
	for _, event := range []string{"cancellation", "serverRequest/resolved", "turn/completed"} {
		t.Run(event, func(t *testing.T) {
			service, s, m := newSession(t)
			r := addRequest(t, s, "item/fileChange/requestApproval")
			_, err := service.Resolve(context.Background(), Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "accept"})})
			if err != nil {
				t.Fatal(err)
			}
			if event == "cancellation" {
				m.reject = true
			} else if event == "serverRequest/resolved" {
				emit(t, s, event, map[string]any{"threadId": "thread", "requestId": "server-id"})
			} else {
				emit(t, s, event, map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "interrupted"}})
			}
			if err = s.BeforeDispatch(r.RequestID); !errors.Is(err, ErrStale) {
				t.Fatalf("queued response escaped fencing: %v", err)
			}
		})
	}
}

func TestCancelRequiresCanonicalAdmissionAndKeepsIntentDistinct(t *testing.T) {
	service, s, m := newSession(t)
	r := addRequest(t, s, "item/commandExecution/requestApproval")
	in := Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "cancel"})}
	if _, err := service.Resolve(context.Background(), in); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	calls := 0
	ctx := WithCanonicalCancellation(context.Background(), func() error { calls++; m.reject = true; return nil })
	v, err := service.Resolve(ctx, in)
	if err != nil || calls != 1 || v.Requests[0].State != "INTENDED" {
		t.Fatal(v, err, calls)
	}
	if err = s.BeforeDispatch(r.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Resolve(ctx, in); !errors.Is(err, ErrStale) || calls != 1 {
		t.Fatal(err, calls)
	}
}

func TestCanonicalRequestIDAndURLElicitation(t *testing.T) {
	if requestKey([]byte(`"server\u002did"`)) != requestKey(raw("server-id")) || requestKey(raw("1")) == requestKey(raw(1)) {
		t.Fatal("request ID identity")
	}
	_, s, _ := newSession(t)
	payload := raw(map[string]any{"threadId": "thread", "turnId": "turn", "serverName": "fixture", "mode": "url", "message": "Authorize", "url": "https://example.com/auth", "elicitationId": "elicitation-1"})
	if err := Validate("mcpServer/elicitation/request", "request", payload); err != nil {
		t.Fatal(err)
	}
	if err := s.Observe(Event{Method: "mcpServer/elicitation/request", Params: payload, RequestID: raw(1)}); err != nil {
		t.Fatal(err)
	}
	v, _ := s.service.Read(context.Background(), "run")
	if len(v.Requests) != 1 {
		t.Fatal(v)
	}
	for _, action := range []string{"accept", "decline", "cancel"} {
		if err := validateResponse(v.Requests[0], raw(map[string]any{"action": action}), false); err != nil {
			t.Fatal(action, err)
		}
	}
	if err := validateResponse(v.Requests[0], raw(map[string]any{"action": "accept", "content": map[string]any{"secret": "value"}}), false); err == nil {
		t.Fatal("URL elicitation accepted form content")
	}
}

func TestFileMetadataRetainsBoundedPathSummaries(t *testing.T) {
	changes := make([]any, 200)
	for i := range changes {
		changes[i] = map[string]any{"path": strings.Repeat("a", 4096), "kind": map[string]any{"type": "update"}, "diff": strings.Repeat("+x\n", 500)}
	}
	metadata := itemMetadata(raw(map[string]any{"id": "files", "type": "fileChange", "status": "completed", "changes": changes}))
	var v map[string]any
	if json.Unmarshal(metadata, &v) != nil || len(metadata) > MaxPayload || len(v["changes"].([]any)) == 0 || v["changeCount"] != float64(200) || v["truncated"] != true {
		t.Fatal("file summaries missing or unbounded")
	}
}

func TestPermissionDenyAndTraversalLimitCannotBeDropped(t *testing.T) {
	requested := map[string]any{"fileSystem": map[string]any{"entries": []any{
		map[string]any{"access": "write", "path": map[string]any{"type": "path", "path": "/tmp/project"}},
		map[string]any{"access": "deny", "path": map[string]any{"type": "path", "path": "/tmp/project/secret"}},
	}, "globScanMaxDepth": float64(2)}}
	r := Request{Method: "item/permissions/requestApproval", Payload: raw(map[string]any{"permissions": requested})}
	if err := validateResponse(r, raw(map[string]any{"permissions": requested, "scope": "session"}), false); err != nil {
		t.Fatal(err)
	}
	var grant map[string]any
	_ = json.Unmarshal(raw(requested), &grant)
	fs := grant["fileSystem"].(map[string]any)
	fs["entries"] = fs["entries"].([]any)[:1]
	if err := validateResponse(r, raw(map[string]any{"permissions": grant}), false); err == nil {
		t.Fatal("deny restriction dropped")
	}
	_ = json.Unmarshal(raw(requested), &grant)
	delete(grant["fileSystem"].(map[string]any), "globScanMaxDepth")
	if err := validateResponse(r, raw(map[string]any{"permissions": grant}), false); err == nil {
		t.Fatal("traversal limit dropped")
	}
	requested["futurePermission"] = true
	r.Payload = raw(map[string]any{"permissions": requested})
	if err := validateResponse(r, raw(map[string]any{"permissions": requested}), false); err == nil {
		t.Fatal("unknown permission echoed into grant")
	}
}

func TestPlanAndItemRacesCannotOutliveOfficialTerminal(t *testing.T) {
	for _, method := range []string{"turn/plan/updated", "item/completed"} {
		t.Run(method, func(t *testing.T) {
			for n := 0; n < 10; n++ {
				service, s, _ := newSession(t)
				p := map[string]any{"threadId": "thread", "turnId": "turn", "plan": []PlanStep{{"one", "completed"}, {"two", "completed"}, {"three", "completed"}, {"four", "completed"}, {"five", "completed"}}, "item": map[string]any{"id": "cmd", "type": "commandExecution", "status": "completed"}}
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); _ = s.Observe(Event{Method: method, Params: raw(p)}) }()
				go func() {
					defer wg.Done()
					_ = s.Observe(Event{Method: "turn/completed", Params: raw(map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": "completed"}})})
				}()
				wg.Wait()
				before := waitRuntimeSnapshot(t, service, func(v Snapshot) bool { return v.TurnStatus == "completed" })
				emit(t, s, method, p)
				after, _ := service.Read(context.Background(), "run")
				if before.TurnStatus != "completed" || before.Sequence != after.Sequence {
					t.Fatal(after)
				}
			}
		})
	}
}

func TestAllPlanStepsAreCompletedWithoutTerminal(t *testing.T) {
	service, s, _ := newSession(t)
	emit(t, s, "turn/plan/updated", map[string]any{"threadId": "thread", "turnId": "turn", "plan": []PlanStep{{"one", "completed"}, {"two", "completed"}, {"three", "completed"}, {"four", "completed"}, {"five", "completed"}}})
	v, _ := service.Read(context.Background(), "run")
	if v.LatestPlan.Completed != 5 || v.LatestPlan.Total != 5 || v.TurnStatus != "" || v.ConnectionState != "CONNECTED" {
		t.Fatal(v)
	}
}

func TestResponseRacesWithConnectionLoss(t *testing.T) {
	for n := 0; n < 10; n++ {
		service, s, _ := newSession(t)
		r := addRequest(t, s, "item/fileChange/requestApproval")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = service.Resolve(context.Background(), Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "accept"})})
		}()
		go func() { defer wg.Done(); s.Close() }()
		wg.Wait()
		v, _ := service.Read(context.Background(), "run")
		if v.Requests[0].State != "CONNECTION_LOST" || v.TurnStatus != "" {
			t.Fatal(v)
		}
	}
}

func TestOldTurnRequestCannotConsumeCurrentRequestWithSameID(t *testing.T) {
	service, s, _ := newSession(t)
	r := addRequest(t, s, "item/fileChange/requestApproval")
	var old map[string]any
	_ = json.Unmarshal(r.Payload, &old)
	old["turnId"] = "previous-turn"
	stale := Event{Method: r.Method, Params: raw(old), RequestID: r.RequestID}
	if err := s.Observe(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Await(context.Background(), r.RequestID, stale); !errors.Is(err, ErrStale) {
		t.Fatal("old Turn captured current waiter", err)
	}
	in := Resolution{Identity: r.Identity, ItemID: r.ItemID, RequestID: r.RequestID, Response: raw(map[string]any{"decision": "decline"})}
	if _, err := service.Resolve(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if answer, err := s.Await(ctx, r.RequestID, Event{Method: r.Method, Params: r.Payload}); err != nil || string(answer) != string(in.Response) {
		t.Fatal("current response was stolen", string(answer), err)
	}
}
