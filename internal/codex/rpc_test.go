package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type rpcFixture struct {
	transport *rpcTransport
	requests  *bufio.Reader
	responses *io.PipeWriter
}

func newRPCFixture(t *testing.T, processor serverRequestProcessor, notificationHandler func(Event)) *rpcFixture {
	t.Helper()
	stdoutReader, stdoutWriter := io.Pipe()
	stdinReader, stdinWriter := io.Pipe()
	var transport *rpcTransport
	if processor == nil {
		transport = newRPCTransport(stdoutReader, stdinWriter, notificationHandler)
	} else {
		transport = newRPCTransportWithProcessor(stdoutReader, stdinWriter, processor, notificationHandler)
	}
	fixture := &rpcFixture{
		transport: transport,
		requests:  bufio.NewReader(stdinReader),
		responses: stdoutWriter,
	}
	t.Cleanup(func() {
		_ = fixture.transport.Close()
		_ = stdinReader.Close()
		_ = stdoutWriter.Close()
	})
	return fixture
}

func (f *rpcFixture) readRequest(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	line, err := f.requests.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read request: %v", err)
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return request
}

func (f *rpcFixture) write(t *testing.T, message any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("encode server message: %v", err)
	}
	data = append(data, '\n')
	if _, err := f.responses.Write(data); err != nil {
		t.Fatalf("write server message: %v", err)
	}
}

func callValue(ctx context.Context, transport *rpcTransport, method string) <-chan struct {
	value string
	err   error
} {
	result := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		var response struct {
			Value string `json:"value"`
		}
		err := transport.Call(ctx, method, map[string]string{"method": method}, &response)
		result <- struct {
			value string
			err   error
		}{response.Value, err}
	}()
	return result
}

func TestRPCRequestCorrelationWithInterleavedNotifications(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	a := callValue(context.Background(), fixture.transport, "request/A")
	requestA := fixture.readRequest(t)
	fixture.write(t, map[string]any{"method": "progress", "params": map[string]int{"n": 1}})
	b := callValue(context.Background(), fixture.transport, "request/B")
	requestB := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": requestB["id"], "result": map[string]string{"value": "B"}})
	fixture.write(t, map[string]any{"method": "progress", "params": map[string]int{"n": 2}})
	fixture.write(t, map[string]any{"id": requestA["id"], "result": map[string]string{"value": "A"}})

	if got := <-a; got.err != nil || got.value != "A" {
		t.Fatalf("request A = %#v", got)
	}
	if got := <-b; got.err != nil || got.value != "B" {
		t.Fatalf("request B = %#v", got)
	}
}

func TestRPCConcurrentRequestsResolveOutOfOrder(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	results := []<-chan struct {
		value string
		err   error
	}{
		callValue(context.Background(), fixture.transport, "first"),
		callValue(context.Background(), fixture.transport, "second"),
	}
	requests := []map[string]json.RawMessage{fixture.readRequest(t), fixture.readRequest(t)}

	fixture.write(t, map[string]any{"id": requests[1]["id"], "result": map[string]string{"value": "later-request"}})
	fixture.write(t, map[string]any{"id": requests[0]["id"], "result": map[string]string{"value": "earlier-request"}})

	values := map[string]bool{}
	for _, result := range results {
		got := <-result
		if got.err != nil {
			t.Fatal(got.err)
		}
		values[got.value] = true
	}
	if !values["later-request"] || !values["earlier-request"] {
		t.Fatalf("unexpected values: %#v", values)
	}
}

func TestRPCDispatchesServerRequest(t *testing.T) {
	handled := make(chan ServerRequest, 1)
	fixture := newRPCFixture(t, func(_ context.Context, request ServerRequest) (any, error) {
		handled <- request
		return map[string]string{"decision": "decline"}, nil
	}, nil)
	fixture.write(t, map[string]any{"method": "item/fileChange/requestApproval", "id": "server-7", "params": map[string]string{"reason": "test"}})

	select {
	case request := <-handled:
		if request.Method != "item/fileChange/requestApproval" {
			t.Fatalf("method = %q", request.Method)
		}
	case <-time.After(time.Second):
		t.Fatal("server request was not dispatched")
	}
	response := fixture.readRequest(t)
	if string(response["id"]) != `"server-7"` || response["result"] == nil {
		t.Fatalf("server response = %s", mustJSON(t, response))
	}
	select {
	case event := <-fixture.transport.Events():
		t.Fatalf("server request was also dispatched as event: %#v", event)
	default:
	}
}

func TestRPCCancelPendingAndIgnoreLateResponse(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := callValue(ctx, fixture.transport, "cancelled")
	request := fixture.readRequest(t)
	cancel()
	if got := <-cancelled; !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancelled call error = %v", got.err)
	}

	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "late"}})
	next := callValue(context.Background(), fixture.transport, "next")
	nextRequest := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": nextRequest["id"], "result": map[string]string{"value": "next"}})
	if got := <-next; got.err != nil || got.value != "next" {
		t.Fatalf("next call = %#v", got)
	}
}

func TestRPCProcessExitFailsAllPendingCalls(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	a := callValue(context.Background(), fixture.transport, "A")
	_ = fixture.readRequest(t)
	b := callValue(context.Background(), fixture.transport, "B")
	_ = fixture.readRequest(t)
	if err := fixture.responses.Close(); err != nil {
		t.Fatal(err)
	}

	for name, result := range map[string]<-chan struct {
		value string
		err   error
	}{"A": a, "B": b} {
		select {
		case got := <-result:
			if !errors.Is(got.err, ErrAppServerExited) {
				t.Fatalf("%s error = %v", name, got.err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s remained blocked after process exit", name)
		}
	}
}

func TestRPCResponseWinsAfterCorrelationBeforeExit(t *testing.T) {
	for i := 0; i < 100; i++ {
		reader, server := io.Pipe()
		writer := newGatedWriteCloser()
		transport := newRPCTransport(reader, writer, nil)
		call := callValue(context.Background(), transport, "terminal-race")
		<-writer.entered

		if err := transport.dispatch([]byte(`{"id":1,"result":{"value":"ok"}}`)); err != nil {
			t.Fatal(err)
		}
		transport.fail(ErrAppServerExited)
		writer.releaseWrite()
		if got := <-call; got.err != nil || got.value != "ok" {
			t.Fatalf("iteration %d: call = %#v", i, got)
		}
		_ = transport.Close()
		_ = server.Close()
	}
}

func TestRPCExitWinsBeforeResponse(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	transport := newRPCTransport(reader, writer, nil)
	call := callValue(context.Background(), transport, "terminal-race")
	<-writer.entered

	transport.fail(ErrAppServerExited)
	writer.releaseWrite()
	if got := <-call; !errors.Is(got.err, ErrAppServerExited) {
		t.Fatalf("call error = %v", got.err)
	}
	_ = transport.Close()
	_ = server.Close()
}

func TestRPCResponseWinsAfterCorrelationBeforeWriteError(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	writer.writeErr = io.ErrUnexpectedEOF
	transport := newRPCTransport(reader, writer, nil)
	call := callValue(context.Background(), transport, "write-race")
	<-writer.entered
	if err := transport.dispatch([]byte(`{"id":1,"result":{"value":"ok"}}`)); err != nil {
		t.Fatal(err)
	}
	writer.releaseWrite()
	if got := <-call; got.err != nil || got.value != "ok" {
		t.Fatalf("call = %#v", got)
	}
	_ = transport.Close()
	_ = server.Close()
}

func TestRPCMalformedMatchedResponseFailsAndCleansPending(t *testing.T) {
	for _, test := range []struct {
		name     string
		response func(json.RawMessage) map[string]any
	}{
		{name: "neither result nor error", response: func(id json.RawMessage) map[string]any {
			return map[string]any{"id": id}
		}},
		{name: "both result and error", response: func(id json.RawMessage) map[string]any {
			return map[string]any{"id": id, "result": map[string]string{"value": "bad"}, "error": map[string]any{"code": -1, "message": "bad"}}
		}},
		{name: "null error", response: func(id json.RawMessage) map[string]any {
			return map[string]any{"id": id, "error": nil}
		}},
		{name: "error missing code", response: func(id json.RawMessage) map[string]any {
			return map[string]any{"id": id, "error": map[string]any{"message": "bad"}}
		}},
		{name: "error missing message", response: func(id json.RawMessage) map[string]any {
			return map[string]any{"id": id, "error": map[string]any{"code": -1}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRPCFixture(t, nil, nil)
			call := callValue(context.Background(), fixture.transport, "malformed")
			request := fixture.readRequest(t)
			fixture.write(t, test.response(request["id"]))
			got := <-call
			var protocolErr *ProtocolError
			if !errors.As(got.err, &protocolErr) {
				t.Fatalf("call error = %T %v", got.err, got.err)
			}
			fixture.transport.mu.Lock()
			pending := len(fixture.transport.pending)
			fixture.transport.mu.Unlock()
			if pending != 0 {
				t.Fatalf("pending requests = %d", pending)
			}
		})
	}
}

func TestRPCCloseIsIdempotentAndUnblocksCall(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	call := callValue(context.Background(), fixture.transport, "pending")
	_ = fixture.readRequest(t)
	if err := fixture.transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-call:
		if !errors.Is(got.err, ErrClientClosed) {
			t.Fatalf("pending call error = %v", got.err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending call remained blocked after Close")
	}
}

func TestRPCSlowEventConsumerDoesNotBlockResponses(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	call := callValue(context.Background(), fixture.transport, "after-events")
	request := fixture.readRequest(t)
	for i := 0; i < eventBuffer*4; i++ {
		fixture.write(t, map[string]any{"method": "progress", "params": map[string]int{"n": i}})
	}
	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "done"}})
	select {
	case got := <-call:
		if got.err != nil || got.value != "done" {
			t.Fatalf("call = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("response blocked behind slow event consumer")
	}
}

func TestRPCMalformedMessageIsDiagnosticNotPanic(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	if _, err := io.WriteString(fixture.responses, "not-json\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-fixture.transport.Diagnostics():
		var protocolErr *ProtocolError
		if !errors.As(err, &protocolErr) {
			t.Fatalf("diagnostic = %T %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing malformed-message diagnostic")
	}
	call := callValue(context.Background(), fixture.transport, "still-healthy")
	request := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "ok"}})
	if got := <-call; got.err != nil || got.value != "ok" {
		t.Fatalf("call after malformed input = %#v", got)
	}
}

func TestRPCServerRequestPanicReturnsErrorAndTransportSurvives(t *testing.T) {
	fixture := newRPCFixture(t, func(_ context.Context, _ ServerRequest) (any, error) {
		panic("sensitive panic value")
	}, nil)
	fixture.write(t, map[string]any{"method": "panic", "id": "panic-1", "params": map[string]any{}})
	response := fixture.readRequest(t)
	assertRPCErrorResponse(t, response, `"panic-1"`, -32603)

	call := callValue(context.Background(), fixture.transport, "still-healthy")
	request := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "ok"}})
	if got := <-call; got.err != nil || got.value != "ok" {
		t.Fatalf("call after panic = %#v", got)
	}
	select {
	case diagnostic := <-fixture.transport.Diagnostics():
		if diagnostic == nil || diagnostic.Error() == "" {
			t.Fatal("missing panic diagnostic")
		}
		if strings.Contains(diagnostic.Error(), "sensitive panic value") {
			t.Fatalf("panic value leaked into diagnostic: %v", diagnostic)
		}
	case <-time.After(time.Second):
		t.Fatal("missing panic diagnostic")
	}
}

func TestRPCSecondaryServerRequestPanicsReturnInternalErrorAndTransportSurvives(t *testing.T) {
	tests := []struct {
		name      string
		processor serverRequestProcessor
	}{
		{name: "Error", processor: func(_ context.Context, request ServerRequest) (any, error) {
			if request.Method == "panic" {
				return nil, panicError{}
			}
			return builtInServerRequestPolicy(context.Background(), request)
		}},
		{name: "As", processor: func(_ context.Context, request ServerRequest) (any, error) {
			if request.Method == "panic" {
				return nil, panicAsError{}
			}
			return builtInServerRequestPolicy(context.Background(), request)
		}},
		{name: "Unwrap", processor: func(_ context.Context, request ServerRequest) (any, error) {
			if request.Method == "panic" {
				return nil, panicUnwrapError{}
			}
			return builtInServerRequestPolicy(context.Background(), request)
		}},
		{name: "MarshalJSON", processor: func(_ context.Context, request ServerRequest) (any, error) {
			if request.Method == "panic" {
				return panicJSONResult{}, nil
			}
			return builtInServerRequestPolicy(context.Background(), request)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRPCFixture(t, test.processor, nil)
			fixture.write(t, map[string]any{"method": "panic", "id": "panic-secondary", "params": map[string]any{}})
			assertRPCErrorResponse(t, fixture.readRequest(t), `"panic-secondary"`, -32603)

			fixture.write(t, map[string]any{"method": "unknown-after-panic", "id": "healthy-worker", "params": map[string]any{}})
			assertRPCErrorResponse(t, fixture.readRequest(t), `"healthy-worker"`, -32601)

			call := callValue(context.Background(), fixture.transport, "rpc-after-panic")
			request := fixture.readRequest(t)
			fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "ok"}})
			if got := <-call; got.err != nil || got.value != "ok" {
				t.Fatalf("RPC after secondary panic = %#v", got)
			}

			select {
			case diagnostic := <-fixture.transport.Diagnostics():
				if strings.Contains(diagnostic.Error(), "boom") {
					t.Fatalf("panic value leaked into diagnostic: %v", diagnostic)
				}
			case <-time.After(time.Second):
				t.Fatal("missing secondary-panic diagnostic")
			}
		})
	}
}

func TestRPCPanickingWorkersRemainFullyReusable(t *testing.T) {
	panicEntered := make(chan struct{}, serverRequestWorkers)
	releasePanics := make(chan struct{})
	normalEntered := make(chan struct{}, serverRequestWorkers+2)
	releaseNormal := make(chan struct{})
	normalBatchDispatched := make(chan struct{})
	var normalBatchOnce sync.Once
	var normalActive atomic.Int32
	var maximumNormalActive atomic.Int32

	fixture := newRPCFixture(t, func(_ context.Context, request ServerRequest) (any, error) {
		switch request.Method {
		case "panic-error", "panic-as", "panic-unwrap", "panic-json":
			panicEntered <- struct{}{}
			<-releasePanics
			switch request.Method {
			case "panic-error":
				return nil, panicError{}
			case "panic-as":
				return nil, panicAsError{}
			case "panic-unwrap":
				return nil, panicUnwrapError{}
			default:
				return panicJSONResult{}, nil
			}
		case "normal":
			current := normalActive.Add(1)
			for {
				previous := maximumNormalActive.Load()
				if current <= previous || maximumNormalActive.CompareAndSwap(previous, current) {
					break
				}
			}
			normalEntered <- struct{}{}
			<-releaseNormal
			normalActive.Add(-1)
			return map[string]bool{"ok": true}, nil
		default:
			return builtInServerRequestPolicy(context.Background(), request)
		}
	}, func(event Event) {
		if event.Method == "test/normal-batch-dispatched" {
			normalBatchOnce.Do(func() { close(normalBatchDispatched) })
		}
	})
	t.Cleanup(func() {
		select {
		case <-releasePanics:
		default:
			close(releasePanics)
		}
		select {
		case <-releaseNormal:
		default:
			close(releaseNormal)
		}
	})

	panicMethods := []string{"panic-error", "panic-as", "panic-unwrap", "panic-json"}
	for index, method := range panicMethods {
		fixture.write(t, map[string]any{"method": method, "id": "panic-" + strconv.Itoa(index), "params": map[string]any{}})
	}
	for range serverRequestWorkers {
		select {
		case <-panicEntered:
		case <-time.After(time.Second):
			t.Fatal("all server-request workers did not enter panic-producing processors")
		}
	}
	close(releasePanics)

	panicResponses := make(map[string]bool, serverRequestWorkers)
	for range serverRequestWorkers {
		response := fixture.readRequest(t)
		assertRPCErrorResponse(t, response, string(response["id"]), -32603)
		id := string(response["id"])
		if panicResponses[id] {
			t.Fatalf("duplicate terminal response for %s", id)
		}
		panicResponses[id] = true
	}
	for index := range serverRequestWorkers {
		id := `"panic-` + strconv.Itoa(index) + `"`
		if !panicResponses[id] {
			t.Fatalf("missing terminal response for %s", id)
		}
	}

	const normalRequests = serverRequestWorkers + 2
	for index := range normalRequests {
		fixture.write(t, map[string]any{"method": "normal", "id": "normal-" + strconv.Itoa(index), "params": map[string]any{}})
	}
	fixture.write(t, map[string]any{"method": "test/normal-batch-dispatched", "params": map[string]any{}})
	select {
	case <-normalBatchDispatched:
	case <-time.After(time.Second):
		t.Fatal("read loop did not finish dispatching the normal request batch")
	}
	for range serverRequestWorkers {
		select {
		case <-normalEntered:
		case <-time.After(time.Second):
			t.Fatal("the recovered worker pool did not regain all four execution slots")
		}
	}
	if active := normalActive.Load(); active != serverRequestWorkers {
		t.Fatalf("active post-panic workers = %d, want %d", active, serverRequestWorkers)
	}
	if queued := len(fixture.transport.serverRequests); queued != normalRequests-serverRequestWorkers {
		t.Fatalf("queued post-panic requests = %d, want %d", queued, normalRequests-serverRequestWorkers)
	}
	select {
	case <-normalEntered:
		t.Fatal("more than four post-panic handlers executed concurrently")
	default:
	}
	close(releaseNormal)

	normalResponses := make(map[string]bool, normalRequests)
	for range normalRequests {
		response := fixture.readRequest(t)
		if response["result"] == nil || response["error"] != nil {
			t.Fatalf("post-panic response = %s", mustJSON(t, response))
		}
		id := string(response["id"])
		if normalResponses[id] {
			t.Fatalf("duplicate post-panic response for %s", id)
		}
		normalResponses[id] = true
	}
	for index := range normalRequests {
		id := `"normal-` + strconv.Itoa(index) + `"`
		if !normalResponses[id] {
			t.Fatalf("missing post-panic response for %s", id)
		}
	}
	if active := normalActive.Load(); active != 0 {
		t.Fatalf("active post-panic workers after responses = %d, want 0", active)
	}
	if maximum := maximumNormalActive.Load(); maximum != serverRequestWorkers {
		t.Fatalf("maximum post-panic concurrency = %d, want %d", maximum, serverRequestWorkers)
	}
}

func TestRPCNonMarshalableServerResultReturnsError(t *testing.T) {
	fixture := newRPCFixture(t, func(_ context.Context, _ ServerRequest) (any, error) {
		return func() {}, nil
	}, nil)
	fixture.write(t, map[string]any{"method": "bad-result", "id": 17, "params": map[string]any{}})
	response := fixture.readRequest(t)
	assertRPCErrorResponse(t, response, "17", -32603)
	call := callValue(context.Background(), fixture.transport, "after-marshal-error")
	request := fixture.readRequest(t)
	fixture.write(t, map[string]any{"id": request["id"], "result": map[string]string{"value": "ok"}})
	if got := <-call; got.err != nil || got.value != "ok" {
		t.Fatalf("RPC after marshal error = %#v", got)
	}
}

func TestRPCQueueOverflowResponseCannotBlockReadLoop(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	started := make(chan struct{}, serverRequestWorkers)
	releaseHandlers := make(chan struct{})
	transport := newRPCTransportWithProcessor(reader, writer, func(_ context.Context, request ServerRequest) (any, error) {
		if request.Method == "active" {
			started <- struct{}{}
		}
		<-releaseHandlers
		return map[string]bool{"ok": true}, nil
	}, nil)

	call := callValue(context.Background(), transport, "ordinary")
	<-writer.entered
	for i := 0; i < serverRequestWorkers; i++ {
		writeRPCServerJSON(t, server, map[string]any{"method": "active", "id": 100 + i, "params": map[string]any{}})
	}
	for i := 0; i < serverRequestWorkers; i++ {
		<-started
	}
	for i := 0; i < serverRequestQueueDepth; i++ {
		writeRPCServerJSON(t, server, map[string]any{"method": "queued", "id": 200 + i, "params": map[string]any{}})
	}
	writeRPCServerJSON(t, server, map[string]any{"method": "overflow", "id": 999, "params": map[string]any{}})
	writeRPCServerJSON(t, server, map[string]any{"id": 1, "result": map[string]string{"value": "processed"}})

	deadline := time.Now().Add(time.Second)
	for {
		transport.mu.Lock()
		pending := len(transport.pending)
		transport.mu.Unlock()
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("ordinary RPC response was starved behind the blocked overflow response")
		}
		time.Sleep(time.Millisecond)
	}

	close(releaseHandlers)
	writer.releaseWrite()
	if got := <-call; got.err != nil || got.value != "processed" {
		t.Fatalf("ordinary RPC call = %#v", got)
	}
	_ = transport.Close()
	_ = server.Close()
}

func TestRPCServerResponseQueueSaturationTerminatesTransport(t *testing.T) {
	reader, server := io.Pipe()
	writer := newGatedWriteCloser()
	transport := newRPCTransport(reader, writer, nil)
	writeRPCServerJSON(t, server, map[string]any{"method": "unknown", "id": 1, "params": map[string]any{}})
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("server response writer did not block")
	}
	senderDone := make(chan struct{})
	go func() {
		defer close(senderDone)
		for i := 1; i < serverResponseQueueDepth*2; i++ {
			data, _ := json.Marshal(map[string]any{"method": "unknown", "id": i + 1, "params": map[string]any{}})
			if _, err := server.Write(append(data, '\n')); err != nil {
				return
			}
		}
	}()

	select {
	case <-transport.Done():
	case <-time.After(time.Second):
		t.Fatal("response queue saturation did not terminate the transport")
	}
	if err := transport.Err(); err == nil || !strings.Contains(err.Error(), "server response queue is full") {
		t.Fatalf("transport error = %v", err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	_ = server.Close()
	<-senderDone
}

func TestRPCUnknownServerRequestReturnsMethodNotFound(t *testing.T) {
	fixture := newRPCFixture(t, nil, nil)
	fixture.write(t, map[string]any{"method": "unknown", "id": 18, "params": map[string]any{}})
	response := fixture.readRequest(t)
	assertRPCErrorResponse(t, response, "18", -32601)
}

func TestRPCServerRequestConcurrencyIsBounded(t *testing.T) {
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, serverRequestWorkers+2)
	release := make(chan struct{})
	fixture := newRPCFixture(t, func(_ context.Context, _ ServerRequest) (any, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return map[string]bool{"ok": true}, nil
	}, nil)

	total := serverRequestWorkers + 2
	for i := 0; i < total; i++ {
		fixture.transport.handleServerRequest(ServerRequest{ID: json.RawMessage(strconv.Itoa(i + 1)), Method: "slow", Params: json.RawMessage(`{}`)})
	}
	for i := 0; i < serverRequestWorkers; i++ {
		<-started
	}
	select {
	case <-started:
		t.Fatal("more handlers started than the configured concurrency limit")
	default:
	}
	if got := maximum.Load(); got != serverRequestWorkers {
		t.Fatalf("maximum concurrent handlers = %d", got)
	}

	close(release)
	for i := 0; i < total; i++ {
		response := fixture.readRequest(t)
		if response["result"] == nil {
			t.Fatalf("server response = %s", mustJSON(t, response))
		}
	}
}

func TestRPCServerRequestQueueOverflowGetsTerminalError(t *testing.T) {
	started := make(chan struct{}, serverRequestWorkers)
	release := make(chan struct{})
	fixture := newRPCFixture(t, func(_ context.Context, request ServerRequest) (any, error) {
		if request.Method == "active" {
			started <- struct{}{}
		}
		<-release
		return map[string]bool{"ok": true}, nil
	}, nil)

	for i := 0; i < serverRequestWorkers; i++ {
		fixture.transport.handleServerRequest(ServerRequest{ID: json.RawMessage(strconv.Itoa(i + 1)), Method: "active", Params: json.RawMessage(`{}`)})
	}
	for i := 0; i < serverRequestWorkers; i++ {
		<-started
	}
	for i := 0; i < serverRequestQueueDepth; i++ {
		id := serverRequestWorkers + i + 1
		fixture.transport.handleServerRequest(ServerRequest{ID: json.RawMessage(strconv.Itoa(id)), Method: "queued", Params: json.RawMessage(`{}`)})
	}

	overflow := make(chan map[string]json.RawMessage, 1)
	go func() { overflow <- fixture.readRequest(t) }()
	overflowID := serverRequestWorkers + serverRequestQueueDepth + 1
	fixture.transport.handleServerRequest(ServerRequest{ID: json.RawMessage(strconv.Itoa(overflowID)), Method: "overflow", Params: json.RawMessage(`{}`)})
	assertRPCErrorResponse(t, <-overflow, strconv.Itoa(overflowID), -32001)

	close(release)
	for i := 0; i < serverRequestWorkers+serverRequestQueueDepth; i++ {
		response := fixture.readRequest(t)
		if response["result"] == nil {
			t.Fatalf("server response = %s", mustJSON(t, response))
		}
	}
}

func TestRPCCloseTerminatesActiveAndQueuedServerRequests(t *testing.T) {
	started := make(chan struct{}, serverRequestWorkers+2)
	cancelled := make(chan struct{}, serverRequestWorkers)
	fixture := newRPCFixture(t, func(ctx context.Context, _ ServerRequest) (any, error) {
		started <- struct{}{}
		<-ctx.Done()
		cancelled <- struct{}{}
		return nil, ctx.Err()
	}, nil)

	total := serverRequestWorkers + 2
	for i := 0; i < total; i++ {
		fixture.transport.handleServerRequest(ServerRequest{ID: json.RawMessage(strconv.Itoa(i + 1)), Method: "slow", Params: json.RawMessage(`{}`)})
	}
	for i := 0; i < serverRequestWorkers; i++ {
		<-started
	}

	responses := make(chan map[string]json.RawMessage, total)
	readErr := make(chan error, 1)
	go func() {
		for i := 0; i < total; i++ {
			line, err := fixture.requests.ReadBytes('\n')
			if err != nil {
				readErr <- err
				return
			}
			var response map[string]json.RawMessage
			if err := json.Unmarshal(line, &response); err != nil {
				readErr <- err
				return
			}
			responses <- response
		}
		readErr <- nil
	}()
	closed := make(chan struct{})
	go func() {
		_ = fixture.transport.Close()
		close(closed)
	}()
	for i := 0; i < total; i++ {
		response := <-responses
		assertRPCErrorResponse(t, response, string(response["id"]), -32000)
	}
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close blocked on server-request handlers")
	}
	for i := 0; i < serverRequestWorkers; i++ {
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("cooperative handler did not receive cancellation")
		}
	}
	select {
	case <-started:
		t.Fatal("queued handler started during shutdown")
	default:
	}
}

func TestRPCCloseJoinsBuiltInWorkersAcrossRepeatedLifecycles(t *testing.T) {
	for iteration := 0; iteration < 20; iteration++ {
		reader, server := io.Pipe()
		requestReader, writer := io.Pipe()
		transport := newRPCTransport(reader, writer, nil)
		closed := make(chan struct{})
		go func() {
			_ = transport.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(time.Second):
			t.Fatalf("iteration %d: Close did not join Bridge-owned workers", iteration)
		}
		_ = server.Close()
		_ = requestReader.Close()
	}
}

func TestRPCCloseUnblocksAndJoinsBlockedResponseWriter(t *testing.T) {
	reader, server := io.Pipe()
	writer := newStubbornWriteCloser()
	started := make(chan struct{})
	transport := newRPCTransportWithProcessor(reader, writer, func(ctx context.Context, _ ServerRequest) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}, nil)
	transport.handleServerRequest(ServerRequest{ID: json.RawMessage(`1`), Method: "blocked-write", Params: json.RawMessage(`{}`)})
	<-started

	closed := make(chan struct{})
	go func() {
		_ = transport.Close()
		close(closed)
	}()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("shutdown response write did not start")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not terminate and join the blocked response writer")
	}
	_ = server.Close()
}

func assertRPCErrorResponse(t *testing.T, response map[string]json.RawMessage, id string, code int64) {
	t.Helper()
	if string(response["id"]) != id {
		t.Fatalf("response id = %s, want %s", response["id"], id)
	}
	var rpcErr struct {
		Code int64 `json:"code"`
	}
	if err := json.Unmarshal(response["error"], &rpcErr); err != nil {
		t.Fatalf("decode error response: %v; response = %s", err, mustJSON(t, response))
	}
	if rpcErr.Code != code {
		t.Fatalf("error code = %d, want %d; response = %s", rpcErr.Code, code, mustJSON(t, response))
	}
}

func writeRPCServerJSON(t *testing.T, writer io.Writer, message any) {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

type panicError struct{}

func (panicError) Error() string { panic("boom from Error") }

type panicAsError struct{}

func (panicAsError) Error() string { return "panic As" }
func (panicAsError) As(any) bool   { panic("boom from As") }

type panicUnwrapError struct{}

func (panicUnwrapError) Error() string { return "panic Unwrap" }
func (panicUnwrapError) Unwrap() error { panic("boom from Unwrap") }

type panicJSONResult struct{}

func (panicJSONResult) MarshalJSON() ([]byte, error) { panic("boom from MarshalJSON") }

type gatedWriteCloser struct {
	entered     chan struct{}
	release     chan struct{}
	writeErr    error
	enteredOnce sync.Once
	releaseOnce sync.Once
}

type stubbornWriteCloser struct {
	entered     chan struct{}
	release     chan struct{}
	enteredOnce sync.Once
	releaseOnce sync.Once
}

func newStubbornWriteCloser() *stubbornWriteCloser {
	return &stubbornWriteCloser{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *stubbornWriteCloser) Write(data []byte) (int, error) {
	w.enteredOnce.Do(func() { close(w.entered) })
	<-w.release
	return len(data), nil
}

func (w *stubbornWriteCloser) releaseWrite() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func (w *stubbornWriteCloser) Close() error {
	w.releaseWrite()
	return nil
}

func newGatedWriteCloser() *gatedWriteCloser {
	return &gatedWriteCloser{entered: make(chan struct{}), release: make(chan struct{})}
}

func (w *gatedWriteCloser) Write(data []byte) (int, error) {
	w.enteredOnce.Do(func() { close(w.entered) })
	<-w.release
	return len(data), w.writeErr
}

func (w *gatedWriteCloser) releaseWrite() {
	w.releaseOnce.Do(func() { close(w.release) })
}

func (w *gatedWriteCloser) Close() error {
	w.releaseWrite()
	return nil
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRequestIDKeyConcurrentUse(t *testing.T) {
	// This also gives the race detector a focused pass over ID parsing.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if key, err := requestIDKey(json.RawMessage(`"id"`)); err != nil || key != "s:id" {
				t.Errorf("key = %q, err = %v", key, err)
			}
		}()
	}
	wg.Wait()
}

func TestResponseGateRunsInsideTransportWriteLock(t *testing.T) {
	writer := newGatedWriteCloser()
	defer writer.Close()
	transport := &rpcTransport{writer: writer}
	gateErr := errors.New("ownership changed")
	checked := make(chan struct{})
	done := make(chan error, 1)
	transport.writeMu.Lock()
	go func() {
		done <- transport.writeServerResponse([]byte(`{"id":1,"result":{}}`), func() error { close(checked); return gateErr })
	}()
	select {
	case <-checked:
		transport.writeMu.Unlock()
		t.Fatal("ownership checked before earlier writes drained")
	case <-time.After(10 * time.Millisecond):
	}
	transport.writeMu.Unlock()
	select {
	case err := <-done:
		if !errors.Is(err, gateErr) || !errors.Is(err, errServerResponseFenced) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("response gate did not complete")
	}
	select {
	case <-writer.entered:
		t.Fatal("fenced response reached the wire")
	default:
	}
}
