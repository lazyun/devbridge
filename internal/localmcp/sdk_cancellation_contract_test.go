package localmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type sdkCancellationHTTPRecord struct {
	Request  json.RawMessage
	Protocol string
	Status   int
	Response string
}

func cancellationEvidence(t *testing.T, name string, v any) {
	t.Helper()
	if root := os.Getenv("DEVBRIDGE_SDK_CONTRACT_EVIDENCE"); root != "" {
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(root, name+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// Real production HTTP adapters, with their production Stateless/JSON/cancellation
// options and only the public static service port. Raw requests are protocol probes,
// NOT evidence of an actual mixed-version SDK client.
func TestSDKCancellationMetadataPublicAdapters(t *testing.T) {
	if os.Getenv("MCPGODEBUG") != "" {
		t.Fatal("run qualification with MCPGODEBUG unset before process startup; SDK flags may be cached during init")
	}
	local, err := localmcp.New(localmcp.Config{}, readyService(t))
	if err != nil {
		t.Fatal(err)
	}
	gpt, err := mcpserver.New(mcpserver.Config{}, readyService(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, adapter := range []struct {
		name, path string
		handler    http.Handler
	}{{"Local", localmcp.MCPPath, local.Handler()}, {"GPT", mcpserver.MCPPath, gpt.Handler()}} {
		t.Run(adapter.name, func(t *testing.T) {
			meta := func(version string) map[string]any {
				return map[string]any{sdkmcp.MetaKeyProtocolVersion: version, sdkmcp.MetaKeyClientInfo: map[string]any{"name": "contract-probe", "version": "1"}, sdkmcp.MetaKeyClientCapabilities: map[string]any{}}
			}
			records := map[string]sdkCancellationHTTPRecord{}
			defer func() { cancellationEvidence(t, "public-"+adapter.name, records) }()
			for _, tc := range []struct {
				name, method, header, version string
				id, withMeta                  bool
				status                        int
			}{
				{"cancel_without_meta", "notifications/cancelled", "2026-07-28", "", false, false, 202},
				{"cancel_matching_meta", "notifications/cancelled", "2026-07-28", "2026-07-28", false, true, 202},
				{"progress_without_meta", "notifications/progress", "2026-07-28", "", false, false, 202},
				{"progress_matching_meta", "notifications/progress", "2026-07-28", "2026-07-28", false, true, 202},
				{"call_missing_meta", "tools/list", "2026-07-28", "", true, false, 400},
				{"call_matching_meta", "tools/list", "2026-07-28", "2026-07-28", true, true, 200},
				{"call_conflicting_meta", "tools/list", "2026-07-28", "2025-06-18", true, true, 400},
				{"cancel_conflicting_meta", "notifications/cancelled", "2026-07-28", "2025-06-18", false, true, 400},
				{"legacy_cancel_without_meta", "notifications/cancelled", "2025-06-18", "", false, false, 202},
				{"legacy_call_without_meta", "tools/list", "2025-06-18", "", true, false, 200},
			} {
				t.Run(tc.name, func(t *testing.T) {
					params := map[string]any{}
					if tc.method == "notifications/cancelled" {
						params["requestId"] = 17
						params["reason"] = "explicit test cancellation"
					}
					if tc.method == "notifications/progress" {
						params["progressToken"] = "unowned-test-token"
						params["progress"] = 1
					}
					if tc.withMeta {
						params["_meta"] = meta(tc.version)
					}
					body := map[string]any{"jsonrpc": "2.0", "method": tc.method, "params": params}
					if tc.id {
						body["id"] = 1
					}
					raw, _ := json.Marshal(body)
					req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+adapter.path, bytes.NewReader(raw))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Accept", "application/json, text/event-stream")
					req.Header.Set("Mcp-Protocol-Version", tc.header)
					req.Header.Set("Mcp-Method", tc.method)
					response := httptest.NewRecorder()
					adapter.handler.ServeHTTP(response, req)
					records[tc.name] = sdkCancellationHTTPRecord{raw, tc.header, response.Code, response.Body.String()}
					if response.Code != tc.status {
						t.Errorf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
					}
					if tc.status == 202 && response.Code == 202 && response.Body.Len() != 0 {
						t.Error("notification response must be empty")
					}
					if tc.status == 200 {
						var reply struct {
							JSONRPC string
							ID      int
							Result  *struct{ Tools []json.RawMessage }
							Error   json.RawMessage
						}
						if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.JSONRPC != "2.0" || reply.ID != 1 || reply.Result == nil || len(reply.Result.Tools) == 0 || len(reply.Error) != 0 {
							t.Errorf("not a successful tools/list JSONRPC result: %s", response.Body.String())
						}
					}
					if tc.status == 400 {
						var reply struct {
							Error struct {
								Code    int
								Message string
							}
						}
						wantCode := -32602 // JSON-RPC InvalidParams
						if strings.Contains(tc.name, "conflicting") {
							wantCode = sdkmcp.CodeHeaderMismatch
						}
						if err := json.Unmarshal(response.Body.Bytes(), &reply); err != nil || reply.Error.Code != wantCode {
							t.Errorf("missing JSONRPC error: %s", response.Body.String())
						}
						if tc.name == "call_missing_meta" && !strings.Contains(reply.Error.Message, "missing or invalid _meta") {
							t.Errorf("wrong missing-meta rejection: %s", reply.Error.Message)
						}
						if strings.Contains(tc.name, "conflicting") && !strings.Contains(reply.Error.Message, "does not match") {
							t.Errorf("wrong mismatch rejection: %s", reply.Error.Message)
						}
					}
				})
			}
		})
	}
}

// Transparent observer for the cleanup POST only. It records actual bytes/status
// and restores response Body bytes; it never repairs metadata or swallows errors.
type cancellationRoundTripper struct {
	base    http.RoundTripper
	records chan sdkCancellationHTTPRecord
}

func (r *cancellationRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var raw []byte
	notification := false
	if req.Body != nil {
		var err error
		raw, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(raw))
		var msg struct{ Method string }
		_ = json.Unmarshal(raw, &msg)
		notification = msg.Method == "notifications/cancelled"
	}
	response, err := r.base.RoundTrip(req)
	if notification && err == nil {
		data, e := io.ReadAll(response.Body)
		_ = response.Body.Close()
		response.Body = io.NopCloser(bytes.NewReader(data))
		if e != nil {
			return response, e
		}
		r.records <- sdkCancellationHTTPRecord{append([]byte(nil), raw...), req.Header.Get("Mcp-Protocol-Version"), response.StatusCode, string(data)}
	}
	return response, err
}

// Separate SDK-only ordinary tools fixture: no fake DevBridge business result.
// Causal barriers keep the unrelated RPC active until the notification HTTP
// response has been read. This is NOT a join of the SDK async cleanup goroutine. All health checks use the SAME original SDK session, never reconnect.
func TestSDKCancellationKeepsOriginalClientAndConcurrentRPC(t *testing.T) {
	if os.Getenv("MCPGODEBUG") != "" {
		t.Fatal("run qualification with MCPGODEBUG unset before process startup; SDK flags may be cached during init")
	}
	ctx, cancelAll := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelAll()
	slowEntered, slowJoined := make(chan struct{}), make(chan struct{})
	canceled := make(chan error, 1)
	otherEntered, otherJoined := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "independent-cancellation-fixture", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "slow", Description: "wait for cancellation"}, func(c context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, map[string]any, error) {
		defer close(slowJoined)
		close(slowEntered)
		<-c.Done()
		canceled <- c.Err()
		return nil, nil, c.Err()
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "independent", Description: "independent gated request"}, func(c context.Context, _ *sdkmcp.CallToolRequest, _ struct{}) (*sdkmcp.CallToolResult, map[string]any, error) {
		defer close(otherJoined)
		close(otherEntered)
		select {
		case <-release:
			return nil, map[string]any{"value": "unrelated-success"}, nil
		case <-c.Done():
			return nil, nil, c.Err()
		}
	})
	host := httptest.NewServer(sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server }, &sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, PropagateRequestCancellation: true}))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	observer := &cancellationRoundTripper{base: transport, records: make(chan sdkCancellationHTTPRecord, 4)}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "same-client-cancellation", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL, HTTPClient: &http.Client{Transport: observer}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		host.Close()
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	defer func() {
		unblock()
		cancelAll()
		_ = session.Close()
		host.Close()
		transport.CloseIdleConnections()
		wg.Wait()
	}()
	if session.InitializeResult().ProtocolVersion != "2026-07-28" {
		t.Fatalf("unexpected negotiated version %s", session.InitializeResult().ProtocolVersion)
	}
	type outcome struct {
		Result *sdkmcp.CallToolResult
		Err    error
	}
	slowDone, otherDone := make(chan outcome, 1), make(chan outcome, 1)
	slowCtx, cancelSlow := context.WithCancel(ctx)
	defer cancelSlow()
	invoke := func(c context.Context, name string, ch chan outcome) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := session.CallTool(c, &sdkmcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			ch <- outcome{v, e}
		}()
	}
	wait := func(ch <-chan struct{}, label string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("%s did not join: %v", label, ctx.Err())
		}
	}
	invoke(slowCtx, "slow", slowDone)
	wait(slowEntered, "slow entered")
	invoke(ctx, "independent", otherDone)
	wait(otherEntered, "other entered")
	cancelSlow()
	var slow outcome
	select {
	case slow = <-slowDone:
	case <-ctx.Done():
		t.Fatal("slow call did not return")
	}
	if slow.Result != nil || !errors.Is(slow.Err, context.Canceled) {
		t.Errorf("canceled call result=%+v err=%v", slow.Result, slow.Err)
	}
	var serverErr error
	select {
	case serverErr = <-canceled:
	case <-ctx.Done():
		t.Fatal("original server context not canceled")
	}
	if !errors.Is(serverErr, context.Canceled) {
		t.Errorf("server context=%v", serverErr)
	}
	wait(slowJoined, "original handler")
	var notice sdkCancellationHTTPRecord
	select {
	case notice = <-observer.records:
	case <-ctx.Done():
		t.Fatal("actual cancellation HTTP exchange not observed")
	}
	if notice.Status != 202 || notice.Response != "" {
		t.Errorf("cleanup response=%d %s", notice.Status, notice.Response)
	}
	// The following two assertions also run on v1.7 RED, exposing connection poison
	// rather than stopping as soon as the metadata rejection is seen.
	unblock()
	var other outcome
	select {
	case other = <-otherDone:
	case <-ctx.Done():
		t.Fatal("unrelated request did not complete")
	}
	wait(otherJoined, "unrelated handler")
	if other.Err != nil || other.Result == nil || other.Result.IsError {
		t.Errorf("unrelated RPC damaged: result=%+v err=%v", other.Result, other.Err)
	} else {
		raw, _ := json.Marshal(other.Result.StructuredContent)
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if !reflect.DeepEqual(got, map[string]any{"value": "unrelated-success"}) {
			t.Errorf("unrelated result changed: %s", raw)
		}
	}
	listed, listErr := session.ListTools(ctx, nil)
	if listErr != nil || listed == nil || len(listed.Tools) != 2 {
		t.Errorf("original session unusable: list=%+v err=%v", listed, listErr)
	}
	cancellationEvidence(t, "same-client", map[string]any{"protocol": session.InitializeResult(), "notification": notice, "clientCanceled": errors.Is(slow.Err, context.Canceled), "clientError": fmtError(slow.Err), "serverCanceled": errors.Is(serverErr, context.Canceled), "originalHandlerJoined": true, "notificationHTTPResponseObserved": true, "sdkAsyncNotificationJoined": false, "unrelatedHandlerJoined": true, "unrelatedError": fmtError(other.Err), "unrelatedResult": other.Result, "listError": fmtError(listErr), "tools": listed})
}
func fmtError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
