package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestInteractiveApprovalDecisionsAndDelivery(t *testing.T) {
	tests := []struct {
		name, method string
		params       map[string]any
		decision     ApprovalDecision
		want         string
	}{
		{"command approve once", "item/commandExecution/requestApproval", modernApprovalParams(), ApprovalDecision{Type: DecisionApproveOnce}, `{"decision":"accept"}`},
		{"command approve session", "item/commandExecution/requestApproval", modernApprovalParams(), ApprovalDecision{Type: DecisionApproveForSession}, `{"decision":"acceptForSession"}`},
		{"file decline", "item/fileChange/requestApproval", modernApprovalParams(), ApprovalDecision{Type: DecisionDecline}, `{"decision":"decline"}`},
		{"file cancel", "item/fileChange/requestApproval", modernApprovalParams(), ApprovalDecision{Type: DecisionCancel}, `{"decision":"cancel"}`},
		{"legacy command approve", "execCommandApproval", legacyApprovalParams(), ApprovalDecision{Type: DecisionApproveOnce}, `{"decision":"approved"}`},
		{"legacy file session", "applyPatchApproval", legacyApprovalParams(), ApprovalDecision{Type: DecisionApproveForSession}, `{"decision":"approved_for_session"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			broker := newApprovalBroker()
			fixture := newRPCFixture(t, broker.process, nil)
			fixture.write(t, map[string]any{"method": test.method, "id": "approval-rpc", "params": test.params})
			request, err := broker.next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			resolved := make(chan error, 1)
			go func() { resolved <- broker.resolve(context.Background(), request.ID, test.decision) }()
			response := fixture.readRequest(t)
			if got := compactJSON(t, response["result"]); got != test.want {
				t.Fatalf("result=%s want=%s", got, test.want)
			}
			if err := <-resolved; err != nil {
				t.Fatalf("resolve did not observe delivery: %v", err)
			}
		})
	}
}

func TestInteractivePermissionGrantIsExactlyRequested(t *testing.T) {
	broker := newApprovalBroker()
	fixture := newRPCFixture(t, broker.process, nil)
	requested := map[string]any{"fileSystem": map[string]any{"write": []string{"/workspace/exact"}}, "network": map[string]any{"enabled": true}}
	params := modernApprovalParams()
	params["permissions"] = requested
	params["cwd"] = "/workspace"
	fixture.write(t, map[string]any{"method": "item/permissions/requestApproval", "id": 7, "params": params})
	request, err := broker.next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	broader := json.RawMessage(`{"fileSystem":{"write":["/"]},"network":{"enabled":true}}`)
	if err := broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionGrantRequestedPermissions, Permissions: broader}); err == nil {
		t.Fatal("broader permission grant accepted")
	}
	exact, _ := json.Marshal(requested)
	resolved := make(chan error, 1)
	go func() {
		resolved <- broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionGrantRequestedPermissions, Permissions: exact})
	}()
	response := fixture.readRequest(t)
	var result struct {
		Permissions map[string]any `json:"permissions"`
	}
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(result.Permissions)
	if !rawJSONEqual(got, exact) {
		t.Fatalf("granted=%s requested=%s", got, exact)
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyAuthorizationCeilingRejectsWriteAuthority(t *testing.T) {
	for _, test := range []struct {
		name, method string
		params       map[string]any
	}{
		{name: "file change", method: "item/fileChange/requestApproval", params: modernApprovalParams()},
		{name: "permissions", method: "item/permissions/requestApproval", params: func() map[string]any {
			value := modernApprovalParams()
			value["permissions"] = map[string]any{"fileSystem": map[string]any{"write": []string{"/workspace"}}}
			return value
		}()},
		{name: "legacy patch", method: "applyPatchApproval", params: legacyApprovalParams()},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker := newApprovalBroker(AuthorizationReadOnly)
			_, err := broker.process(context.Background(), ServerRequest{Method: test.method, Params: approvalJSON(t, test.params)})
			var authorization *AuthorizationError
			if !errors.As(err, &authorization) || !errors.Is(err, ErrAuthorizationCeiling) {
				t.Fatalf("error=%v, want AuthorizationError", err)
			}
			if authorization.Ceiling != AuthorizationReadOnly {
				t.Fatalf("authorization error=%+v", authorization)
			}
			select {
			case request := <-broker.queue:
				t.Fatalf("write-capable approval reached host queue: %+v", request)
			default:
			}
		})
	}

	broker := newApprovalBroker(AuthorizationReadOnly)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queued := make(chan error, 1)
	go func() {
		_, err := broker.process(ctx, ServerRequest{Method: "item/commandExecution/requestApproval", Params: approvalJSON(t, modernApprovalParams())})
		queued <- err
	}()
	request, err := broker.next(context.Background())
	if err != nil || request.Kind != ApprovalKindCommandExecution {
		t.Fatalf("read-only command approval=%+v err=%v", request, err)
	}
	if !reflect.DeepEqual(request.AllowedDecisions, []DecisionType{DecisionDecline, DecisionCancel}) {
		t.Fatalf("read-only command decisions=%v", request.AllowedDecisions)
	}
	if err := broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionApproveOnce}); !errors.Is(err, ErrAuthorizationCeiling) {
		t.Fatalf("read-only command elevation err=%v", err)
	}
	cancel()
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveApprovalRejectsIllegalDuplicateAndUnknownResolution(t *testing.T) {
	broker := newApprovalBroker()
	fixture := newRPCFixture(t, broker.process, nil)
	fixture.write(t, map[string]any{"method": "item/fileChange/requestApproval", "id": 1, "params": modernApprovalParams()})
	request, _ := broker.next(context.Background())
	if err := broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionGrantRequestedPermissions}); !errors.Is(err, ErrApprovalInvalidDecision) {
		t.Fatalf("illegal decision err=%v", err)
	}
	resolved := make(chan error, 1)
	go func() {
		resolved <- broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionDecline})
	}()
	_ = fixture.readRequest(t)
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if err := broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionDecline}); !errors.Is(err, ErrApprovalAlreadySolved) {
		t.Fatalf("duplicate err=%v", err)
	}
	if err := broker.resolve(context.Background(), "approval_unknown", ApprovalDecision{Type: DecisionDecline}); !errors.Is(err, ErrApprovalNotFound) {
		t.Fatalf("unknown err=%v", err)
	}
}

func TestInteractiveApprovalDoesNotBlockStdoutReadLoop(t *testing.T) {
	broker := newApprovalBroker()
	fixture := newRPCFixture(t, broker.process, nil)
	fixture.write(t, map[string]any{"method": "item/commandExecution/requestApproval", "id": "approval", "params": modernApprovalParams()})
	request, _ := broker.next(context.Background())
	call := callValue(context.Background(), fixture.transport, "still-reading")
	clientRequest := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": clientRequest["id"], "result": map[string]string{"value": "read"}})
	if got := <-call; got.err != nil || got.value != "read" {
		t.Fatalf("interleaved call=%+v", got)
	}
	resolved := make(chan error, 1)
	go func() {
		resolved <- broker.resolve(context.Background(), request.ID, ApprovalDecision{Type: DecisionDecline})
	}()
	_ = fixture.readRequest(t)
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
}

func TestInteractiveApprovalCloseAndBounds(t *testing.T) {
	broker := newApprovalBroker()
	waiting := make(chan error, 1)
	go func() {
		_, err := broker.next(context.Background())
		waiting <- err
	}()
	broker.close()
	if err := <-waiting; !errors.Is(err, ErrClientClosed) {
		t.Fatalf("NextApproval close err=%v", err)
	}

	broker = newApprovalBroker()
	contexts := make([]context.CancelFunc, 0, approvalQueueDepth)
	var workers sync.WaitGroup
	for i := 0; i < approvalQueueDepth; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		contexts = append(contexts, cancel)
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			_, _ = broker.process(ctx, ServerRequest{Method: "item/fileChange/requestApproval", Params: json.RawMessage(fmt.Sprintf(`{"threadId":"t","turnId":"turn","itemId":"%d"}`, index))})
		}(i)
	}
	deadline := time.Now().Add(time.Second)
	for {
		broker.mu.Lock()
		count := len(broker.pending)
		broker.mu.Unlock()
		if count == approvalQueueDepth {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending approvals=%d", count)
		}
		time.Sleep(time.Millisecond)
	}
	result, err := broker.process(context.Background(), ServerRequest{Method: "item/fileChange/requestApproval", Params: json.RawMessage(`{"threadId":"t","turnId":"turn","itemId":"overflow"}`)})
	if err != nil || compactAnyJSON(t, result) != `{"decision":"decline"}` {
		t.Fatalf("overflow result=%v err=%v", result, err)
	}
	broker.close()
	for _, cancel := range contexts {
		cancel()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("approval processors leaked after Close")
	}
}

func modernApprovalParams() map[string]any {
	return map[string]any{"threadId": "thread-1", "turnId": "turn-1", "itemId": "item-1", "startedAtMs": 1}
}

func legacyApprovalParams() map[string]any {
	return map[string]any{"conversationId": "thread-1", "callId": "call-1"}
}

func compactJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func compactAnyJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return compactJSON(t, encoded)
}

func approvalJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
