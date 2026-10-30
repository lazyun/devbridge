package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	eventBuffer              = 128
	diagnosticBuffer         = 32
	maxMessageBytes          = 16 << 20
	serverRequestWorkers     = 4
	serverRequestQueueDepth  = 32
	serverResponseQueueDepth = 64
	serverShutdownFlush      = 100 * time.Millisecond
)

type rpcResult struct {
	result json.RawMessage
	err    error
}

type serverRequestOperation struct {
	transport    *rpcTransport
	request      ServerRequest
	once         sync.Once
	responseDone chan error
}

type serverResponse struct {
	beforeWrite func() error
	data        []byte
	done        chan error
}

type serverRequestProcessor func(context.Context, ServerRequest) (any, error)

type rpcTransport struct {
	reader io.ReadCloser
	writer io.WriteCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[string]chan rpcResult
	stopped bool
	stopErr error

	nextID atomic.Uint64
	events chan Event
	diags  chan error
	done   chan struct{}

	ctx                    context.Context
	cancel                 context.CancelFunc
	readDone               chan struct{}
	serverRequestProcessor serverRequestProcessor
	serverRequests         chan *serverRequestOperation
	serverResponses        chan serverResponse
	serverWriterDone       chan struct{}
	serverWorkers          sync.WaitGroup
	serverMu               sync.Mutex
	serverAccepting        bool
	serverOperations       map[*serverRequestOperation]struct{}
	notificationHandler    func(Event)
	requestObserver        func(ServerRequest)
	notificationMu         sync.Mutex
	ioCloseOnce            sync.Once
	closeOnce              sync.Once
}

func newRPCTransport(
	reader io.ReadCloser,
	writer io.WriteCloser,
	notificationHandler func(Event),
) *rpcTransport {
	return newRPCTransportWithProcessor(reader, writer, builtInServerRequestPolicy, notificationHandler)
}

func newRPCTransportWithProcessor(
	reader io.ReadCloser,
	writer io.WriteCloser,
	processor serverRequestProcessor,
	notificationHandler func(Event),
	requestObservers ...func(ServerRequest),
) *rpcTransport {
	ctx, cancel := context.WithCancel(context.Background())
	t := &rpcTransport{
		reader:                 reader,
		writer:                 writer,
		pending:                make(map[string]chan rpcResult),
		events:                 make(chan Event, eventBuffer),
		diags:                  make(chan error, diagnosticBuffer),
		done:                   make(chan struct{}),
		ctx:                    ctx,
		cancel:                 cancel,
		readDone:               make(chan struct{}),
		serverRequestProcessor: processor,
		serverRequests:         make(chan *serverRequestOperation, serverRequestQueueDepth),
		serverResponses:        make(chan serverResponse, serverResponseQueueDepth),
		serverWriterDone:       make(chan struct{}),
		serverAccepting:        true,
		serverOperations:       make(map[*serverRequestOperation]struct{}),
		notificationHandler:    notificationHandler,
	}
	if len(requestObservers) > 0 {
		t.requestObserver = requestObservers[0]
	}
	t.serverWorkers.Add(serverRequestWorkers)
	for range serverRequestWorkers {
		go func() {
			defer t.serverWorkers.Done()
			t.serverRequestWorker()
		}()
	}
	go t.serverResponseWriter()
	go t.readLoop()
	return t
}

func (t *rpcTransport) Call(ctx context.Context, method string, params any, result any) error {
	id := t.nextID.Add(1)
	if id > math.MaxInt64 {
		return errors.New("JSON-RPC request ID space exhausted")
	}
	idText := strconv.FormatUint(id, 10)
	key := "n:" + idText
	response := make(chan rpcResult, 1)

	t.mu.Lock()
	if t.stopped {
		err := t.stopErr
		t.mu.Unlock()
		return err
	}
	t.pending[key] = response
	t.mu.Unlock()

	request := struct {
		Method string `json:"method"`
		ID     uint64 `json:"id"`
		Params any    `json:"params"`
	}{Method: method, ID: id, Params: params}
	if err := t.writeJSON(request); err != nil {
		if !t.removePending(key, response) {
			return decodeRPCResult(method, <-response, result)
		}
		if stoppedErr := t.Err(); stoppedErr != nil {
			return stoppedErr
		}
		exitErr := fmt.Errorf("%w: write stdin: %v", ErrAppServerExited, err)
		t.terminate(exitErr)
		return exitErr
	}

	select {
	case reply := <-response:
		return decodeRPCResult(method, reply, result)
	case <-ctx.Done():
		if t.removePending(key, response) {
			return ctx.Err()
		}
		// A response or transport failure already claimed this operation.
		return decodeRPCResult(method, <-response, result)
	}
}

func decodeRPCResult(method string, reply rpcResult, result any) error {
	if reply.err != nil {
		return reply.err
	}
	if result == nil || len(reply.result) == 0 || bytes.Equal(reply.result, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(reply.result, result); err != nil {
		return fmt.Errorf("decode %s response: %w", method, err)
	}
	return nil
}

func (t *rpcTransport) Notify(method string, params any) error {
	message := struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{Method: method, Params: params}
	if err := t.writeJSON(message); err != nil {
		if stoppedErr := t.Err(); stoppedErr != nil {
			return stoppedErr
		}
		exitErr := fmt.Errorf("%w: write stdin: %v", ErrAppServerExited, err)
		t.terminate(exitErr)
		return exitErr
	}
	return nil
}

func (t *rpcTransport) Events() <-chan Event      { return t.events }
func (t *rpcTransport) Diagnostics() <-chan error { return t.diags }
func (t *rpcTransport) Done() <-chan struct{}     { return t.done }

func (t *rpcTransport) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopErr == nil {
		return nil
	}
	return t.stopErr
}

func (t *rpcTransport) Close() error {
	t.closeOnce.Do(func() {
		responses := t.shutdownServerRequests()
		t.waitServerResponses(responses, serverShutdownFlush)
		t.terminate(ErrClientClosed)
		t.closeIO()
		<-t.readDone
		<-t.serverWriterDone
		t.serverWorkers.Wait()
	})
	return nil
}

// Detach closes the connection without manufacturing responses to pending
// server questions. The independently lived App Server retains their ownership.
func (t *rpcTransport) Detach() error {
	t.closeOnce.Do(func() {
		t.closeIO()
		t.terminate(ErrClientClosed)
		<-t.readDone
		<-t.serverWriterDone
		t.serverWorkers.Wait()
	})
	return nil
}

func (t *rpcTransport) fail(err error) {
	if err == nil {
		err = ErrAppServerExited
	}
	t.terminate(err)
}

func (t *rpcTransport) removePending(key string, ch chan rpcResult) bool {
	t.mu.Lock()
	removed := false
	if t.pending[key] == ch {
		delete(t.pending, key)
		removed = true
	}
	t.mu.Unlock()
	return removed
}

func (t *rpcTransport) writeJSON(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return t.writePreparedJSON(data)
}

var errServerResponseFenced = errors.New("server response fenced before write")

func (t *rpcTransport) writePreparedJSON(data []byte, beforeWrites ...func() error) error {
	data = append(data, '\n')

	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.mu.Lock()
	stopped, stopErr := t.stopped, t.stopErr
	t.mu.Unlock()
	if stopped {
		return stopErr
	}
	if len(beforeWrites) > 0 && beforeWrites[0] != nil {
		if err := beforeWrites[0](); err != nil {
			return errors.Join(errServerResponseFenced, err)
		}
	}
	_, err := t.writer.Write(data)
	return err
}

func (t *rpcTransport) readLoop() {
	defer close(t.readDone)
	scanner := bufio.NewScanner(t.reader)
	scanner.Buffer(make([]byte, 64<<10), maxMessageBytes)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			t.diagnostic(&ProtocolError{Message: "empty JSONL message"})
			continue
		}
		if err := t.dispatch(line); err != nil {
			t.diagnostic(err)
		}
	}
	if t.isStopped() {
		return
	}
	if err := scanner.Err(); err != nil {
		t.terminate(fmt.Errorf("%w: read stdout: %v", ErrAppServerExited, err))
		return
	}
	t.terminate(fmt.Errorf("%w: stdout closed", ErrAppServerExited))
}

func (t *rpcTransport) dispatch(line []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return &ProtocolError{Message: err.Error(), Input: clippedLine(line)}
	}
	methodRaw, hasMethod := fields["method"]
	idRaw, hasID := fields["id"]

	if hasMethod {
		var method string
		if err := json.Unmarshal(methodRaw, &method); err != nil || method == "" {
			return &ProtocolError{Message: "method must be a non-empty string", Input: clippedLine(line)}
		}
		params := fields["params"]
		if params == nil {
			params = json.RawMessage("null")
		}
		if hasID {
			if _, err := requestIDKey(idRaw); err != nil {
				return &ProtocolError{Message: err.Error(), Input: clippedLine(line)}
			}
			if t.requestObserver != nil {
				t.requestObserver(ServerRequest{ID: append(json.RawMessage(nil), idRaw...), Method: method, Params: append(json.RawMessage(nil), params...)})
			}
			t.handleServerRequest(ServerRequest{ID: append(json.RawMessage(nil), idRaw...), Method: method, Params: append(json.RawMessage(nil), params...)})
			return nil
		}
		t.dispatchEvent(Event{Method: method, Params: append(json.RawMessage(nil), params...)})
		return nil
	}

	if !hasID {
		return &ProtocolError{Message: "message has neither method nor id", Input: clippedLine(line)}
	}
	key, err := requestIDKey(idRaw)
	if err != nil {
		return &ProtocolError{Message: err.Error(), Input: clippedLine(line)}
	}

	t.mu.Lock()
	response := t.pending[key]
	if response != nil {
		delete(t.pending, key)
	}
	t.mu.Unlock()
	if response == nil {
		return &ProtocolError{Message: "response has no pending request", Input: clippedLine(line)}
	}

	resultRaw, hasResult := fields["result"]
	errorRaw, hasError := fields["error"]
	if hasResult == hasError {
		protocolErr := &ProtocolError{Message: "response must contain exactly one of result or error", Input: clippedLine(line)}
		response <- rpcResult{err: protocolErr}
		return protocolErr
	}
	if hasError {
		var rpcErr struct {
			Code    *int64          `json:"code"`
			Message *string         `json:"message"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(errorRaw, &rpcErr); err != nil || rpcErr.Code == nil || rpcErr.Message == nil {
			protocolErr := &ProtocolError{Message: "malformed error response", Input: clippedLine(line)}
			response <- rpcResult{err: protocolErr}
			return protocolErr
		}
		response <- rpcResult{err: &RPCError{Code: *rpcErr.Code, Message: *rpcErr.Message, Data: rpcErr.Data}}
		return nil
	}
	response <- rpcResult{result: append(json.RawMessage(nil), resultRaw...)}
	return nil
}

func (t *rpcTransport) dispatchEvent(event Event) {
	t.notificationMu.Lock()
	defer t.notificationMu.Unlock()

	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped {
		return
	}
	if t.notificationHandler != nil {
		t.notificationHandler(event)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	select {
	case t.events <- event:
	default:
		select {
		case t.diags <- errors.New("app-server event dropped because the event buffer is full"):
		default:
		}
	}
}

func (t *rpcTransport) handleServerRequest(request ServerRequest) {
	operation := &serverRequestOperation{
		transport:    t,
		request:      request,
		responseDone: make(chan error, 1),
	}
	t.serverMu.Lock()
	accepting := t.serverAccepting
	if accepting {
		t.serverOperations[operation] = struct{}{}
	}
	queued := false
	if accepting {
		select {
		case t.serverRequests <- operation:
			queued = true
		default:
		}
	}
	t.serverMu.Unlock()

	if !accepting {
		if !operation.respondKnownError(-32000, "client is shutting down", nil) {
			t.terminate(fmt.Errorf("%w: encode shutdown response", ErrAppServerExited))
		}
		return
	}
	if !queued {
		if !operation.respondKnownError(-32001, "server request queue is full", nil) {
			t.terminate(fmt.Errorf("%w: encode queue-full response", ErrAppServerExited))
		}
	}
}

func (t *rpcTransport) serverRequestWorker() {
	for {
		if t.ctx.Err() != nil {
			return
		}
		select {
		case <-t.ctx.Done():
			return
		case operation := <-t.serverRequests:
			if t.ctx.Err() != nil {
				if !operation.respondKnownError(-32000, "client is shutting down", nil) {
					t.terminate(fmt.Errorf("%w: encode shutdown response", ErrAppServerExited))
				}
				continue
			}
			t.executeServerRequest(operation)
		}
	}
}

func (t *rpcTransport) executeServerRequest(operation *serverRequestOperation) {
	defer func() {
		if recover() != nil {
			t.recoverServerRequest(operation)
		}
	}()

	processor := t.serverRequestProcessor
	if processor == nil {
		processor = builtInServerRequestPolicy
	}
	result, err := processor(t.ctx, operation.request)
	if err != nil {
		operation.respondError(err)
		return
	}
	if resolution, ok := result.(serverRequestResolution); ok {
		if !operation.respondResult(resolution.result, resolution.beforeWrite) {
			resolution.delivered <- ErrClientClosed
			return
		}
		select {
		case delivered := <-operation.responseDone:
			resolution.delivered <- delivered
		case <-t.ctx.Done():
			resolution.delivered <- t.Err()
		}
		return
	}
	operation.respondResult(result)
}

func (t *rpcTransport) recoverServerRequest(operation *serverRequestOperation) {
	defer func() {
		if recover() != nil {
			t.terminate(fmt.Errorf("%w: server-request panic recovery failed", ErrAppServerExited))
		}
	}()
	t.diagnostic(fmt.Errorf("server-request processing panicked for method %q", operation.request.Method))
	if !operation.respondKnownError(-32603, "server request processing failed", nil) {
		t.terminate(fmt.Errorf("%w: server-request panic could not be resolved", ErrAppServerExited))
	}
}

func (operation *serverRequestOperation) respondResult(result any, beforeWrites ...func() error) bool {
	message := struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
	}{ID: operation.request.ID, Result: result}
	data, err := json.Marshal(message)
	if err != nil {
		operation.transport.diagnostic(fmt.Errorf("server-request result is not JSON-encodable for method %q", operation.request.Method))
		if !operation.respondKnownError(-32603, "server request result is not JSON-encodable", nil) {
			operation.transport.terminate(fmt.Errorf("%w: encode internal-error response", ErrAppServerExited))
		}
		return false
	}
	return operation.respondPrepared(data, beforeWrites...)
}

func (operation *serverRequestOperation) respondError(err error) {
	code, message, data := int64(-32000), err.Error(), json.RawMessage(nil)
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		code, message, data = rpcErr.Code, rpcErr.Message, rpcErr.Data
	}
	if !operation.respondKnownError(code, message, data) {
		operation.transport.terminate(fmt.Errorf("%w: encode server error response", ErrAppServerExited))
	}
}

func (operation *serverRequestOperation) respondKnownError(code int64, message string, data json.RawMessage) bool {
	if observer := operation.transport.requestObserver; observer != nil && operation.transport.ctx.Err() == nil && code != -32000 {
		var params map[string]json.RawMessage
		_ = json.Unmarshal(operation.request.Params, &params)
		if params == nil {
			params = map[string]json.RawMessage{}
		}
		params["requestId"] = operation.request.ID
		params["rejectedMethod"], _ = json.Marshal(operation.request.Method)
		params["message"], _ = json.Marshal(message)
		encoded, _ := json.Marshal(params)
		observer(ServerRequest{Method: "bridge/requestRejected", Params: encoded})
	}

	payload := struct {
		ID    json.RawMessage `json:"id"`
		Error any             `json:"error"`
	}{ID: operation.request.ID, Error: struct {
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data,omitempty"`
	}{Code: code, Message: message, Data: data}}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	operation.respondPrepared(encoded)
	return true
}

func (operation *serverRequestOperation) respondPrepared(data []byte, beforeWrites ...func() error) bool {
	var beforeWrite func() error
	if len(beforeWrites) > 0 {
		beforeWrite = beforeWrites[0]
	}
	won := false
	operation.once.Do(func() {
		won = true
		defer operation.transport.forgetServerRequest(operation)
		operation.transport.enqueueServerResponse(serverResponse{data: data, done: operation.responseDone, beforeWrite: beforeWrite})
	})
	return won
}

func (t *rpcTransport) enqueueServerResponse(response serverResponse) {
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped {
		response.done <- t.Err()
		return
	}
	select {
	case t.serverResponses <- response:
		return
	default:
		err := fmt.Errorf("%w: server response queue is full", ErrAppServerExited)
		response.done <- err
		t.terminate(err)
	}
}

func (t *rpcTransport) serverResponseWriter() {
	defer close(t.serverWriterDone)
	for {
		select {
		case response := <-t.serverResponses:
			err := t.writeServerResponse(response.data, response.beforeWrite)
			response.done <- err
			if err != nil && !errors.Is(err, errServerResponseFenced) && !t.isStopped() {
				t.terminate(fmt.Errorf("%w: write server response: %v", ErrAppServerExited, err))
			}
		case <-t.done:
			t.discardServerResponses()
			return
		}
	}
}

func (t *rpcTransport) writeServerResponse(data []byte, beforeWrite func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("server response writer panicked")
		}
	}()
	return t.writePreparedJSON(data, beforeWrite)
}

func (t *rpcTransport) discardServerResponses() {
	for {
		select {
		case response := <-t.serverResponses:
			response.done <- ErrClientClosed
		default:
			return
		}
	}
}

func (t *rpcTransport) waitServerResponses(responses []<-chan error, timeout time.Duration) {
	if len(responses) == 0 {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, response := range responses {
		select {
		case <-response:
		case <-timer.C:
			return
		}
	}
}

func (t *rpcTransport) forgetServerRequest(operation *serverRequestOperation) {
	t.serverMu.Lock()
	delete(t.serverOperations, operation)
	t.serverMu.Unlock()
}

func (t *rpcTransport) shutdownServerRequests() []<-chan error {
	t.serverMu.Lock()
	if !t.serverAccepting {
		t.serverMu.Unlock()
		return nil
	}
	t.serverAccepting = false
	operations := make([]*serverRequestOperation, 0, len(t.serverOperations))
	for operation := range t.serverOperations {
		operations = append(operations, operation)
	}
	t.serverMu.Unlock()

	t.cancel()
	responses := make([]<-chan error, 0, len(operations))
	for _, operation := range operations {
		if !operation.respondKnownError(-32000, "client is shutting down", nil) {
			t.terminate(fmt.Errorf("%w: encode shutdown response", ErrAppServerExited))
		}
		responses = append(responses, operation.responseDone)
	}
	return responses
}

func (t *rpcTransport) abortServerRequests() {
	t.serverMu.Lock()
	t.serverAccepting = false
	t.serverOperations = make(map[*serverRequestOperation]struct{})
	t.serverMu.Unlock()
}

func (t *rpcTransport) diagnostic(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	select {
	case t.diags <- err:
	default:
	}
}

func (t *rpcTransport) terminate(err error) {
	if err == nil {
		err = ErrClientClosed
	}
	t.notificationMu.Lock()
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		t.notificationMu.Unlock()
		return
	}
	t.stopped = true
	t.stopErr = err
	pending := t.pending
	t.pending = make(map[string]chan rpcResult)
	close(t.done)
	close(t.events)
	close(t.diags)
	t.mu.Unlock()
	t.notificationMu.Unlock()

	t.cancel()
	t.abortServerRequests()
	for _, response := range pending {
		response <- rpcResult{err: err}
	}
	t.closeIO()
}

func (t *rpcTransport) closeIO() {
	t.ioCloseOnce.Do(func() {
		_ = t.writer.Close()
		_ = t.reader.Close()
	})
}

func (t *rpcTransport) isStopped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stopped
}

func requestIDKey(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("invalid request id")
	}
	switch id := value.(type) {
	case string:
		return "s:" + id, nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(id), 10, 64)
		if err != nil {
			return "", errors.New("request id must be a signed integer or string")
		}
		return "n:" + strconv.FormatInt(parsed, 10), nil
	default:
		return "", errors.New("request id must be an integer or string")
	}
}

func clippedLine(line []byte) string {
	const limit = 512
	if len(line) <= limit {
		return string(line)
	}
	return string(line[:limit]) + "..."
}

func builtInServerRequestPolicy(_ context.Context, request ServerRequest) (any, error) {
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]any{"decision": "decline"}, nil
	case "item/permissions/requestApproval":
		// The protocol has no decline discriminator for this request. An empty
		// grant is the schema-valid response that grants no added permissions.
		return map[string]any{"permissions": map[string]any{}}, nil
	case "execCommandApproval", "applyPatchApproval":
		return map[string]any{"decision": map[string]any{"denied": map[string]string{
			"rejection": "Dev Bridge M0 does not approve server requests",
		}}}, nil
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "decline"}, nil
	default:
		return nil, &RPCError{Code: -32601, Message: "Dev Bridge M0 does not support server request " + request.Method}
	}
}
