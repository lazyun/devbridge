package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/buildinfo"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerStartsAndShutsDownCleanly(t *testing.T) {
	server, cancel, baseURL := startTestServer(t, readyService(t))

	response, err := http.Get(baseURL + mcpserver.HealthPath)
	if err != nil {
		t.Fatalf("GET health: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	cancel()
}

func TestGPTHandlerRejectsCrossOriginBrowserMutationRequests(t *testing.T) {
	server, err := mcpserver.New(mcpserver.Config{}, readyService(t))
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		origin string
		status int
	}{"non-browser": {"", http.StatusOK}, "same-origin browser": {"http://127.0.0.1", http.StatusOK}, "hostile browser": {"https://hostile.example", http.StatusForbidden}} {
		t.Run(name, func(t *testing.T) {
			body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"browser","version":"1"}}}`)
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1"+mcpserver.MCPPath, body)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("origin %q status=%d, want %d; body=%q", test.origin, response.Code, test.status, response.Body.String())
			}
		})
	}
}

func TestContextCancellationStopsServer(t *testing.T) {
	server, cancel, _ := startTestServer(t, readyService(t))
	cancel()

	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after owner context cancellation")
	}
}

func TestStreamableHTTPDiscoveryIsDeterministicJSON(t *testing.T) {
	_, _, baseURL := startTestServer(t, readyService(t))
	request := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`

	first := postRPCVersion(t, baseURL+mcpserver.MCPPath, request, "2026-07-28")
	second := postRPCVersion(t, baseURL+mcpserver.MCPPath, request, "2026-07-28")
	if first.status != http.StatusOK || second.status != http.StatusOK {
		t.Fatalf("discovery statuses = %d, %d; want 200; bodies=%q, %q", first.status, second.status, first.body, second.body)
	}
	if first.contentType != "application/json" || second.contentType != "application/json" {
		t.Fatalf("discovery content types = %q, %q", first.contentType, second.contentType)
	}
	if !bytes.Equal(first.body, second.body) {
		t.Fatalf("discovery was not byte-stable:\nfirst:  %s\nsecond: %s", first.body, second.body)
	}
	if bytes.Contains(first.body, []byte("event:")) || bytes.Contains(first.body, []byte("data:")) {
		t.Fatalf("discovery unexpectedly used SSE: %s", first.body)
	}

	var envelope struct {
		Result struct {
			Meta         map[string]json.RawMessage `json:"_meta"`
			Capabilities struct {
				Tools *struct {
					ListChanged bool `json:"listChanged"`
				} `json:"tools"`
			} `json:"capabilities"`
			SupportedVersions []string `json:"supportedVersions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(first.body, &envelope); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	var serverInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(envelope.Result.Meta["io.modelcontextprotocol/serverInfo"], &serverInfo); err != nil {
		t.Fatalf("decode discovery server info: %v; body=%s", err, first.body)
	}
	if serverInfo.Name != mcpserver.ServerName || serverInfo.Version != mcpserver.ServerVersion {
		t.Fatalf("server info = %+v; body=%s", serverInfo, first.body)
	}
	if envelope.Result.Capabilities.Tools == nil || envelope.Result.Capabilities.Tools.ListChanged {
		t.Fatalf("tool capabilities = %+v, want stable listChanged=false", envelope.Result.Capabilities.Tools)
	}
	if len(envelope.Result.SupportedVersions) == 0 {
		t.Fatal("discovery returned no supported protocol versions")
	}
}

func TestInProcessClientListsAndCallsBridgeDiagnostics(t *testing.T) {
	_, _, baseURL := startTestServer(t, readyService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "integration-test", Version: "1.0.0"}, &sdkmcp.ClientOptions{Capabilities: &sdkmcp.ClientCapabilities{}})
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{
		Endpoint:             baseURL + mcpserver.MCPPath,
		HTTPClient:           &http.Client{Timeout: 2 * time.Second},
		MaxRetries:           -1,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "bridge_diagnostics", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if result.IsError {
		t.Fatalf("bridge_diagnostics returned tool error: %+v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("bridge_diagnostics structured content = %T", result.StructuredContent)
	}
	for _, key := range []string{"result", "context", "workflow", "presentationHint"} {
		if _, present := structured[key]; present {
			t.Fatalf("unexpected bootstrap envelope field %s: %+v", key, structured)
		}
	}
	encoded, err := json.Marshal(structured)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var info mcpserver.BridgeInfoOutput
	if err := json.Unmarshal(encoded, &info); err != nil {
		t.Fatalf("decode bridge_diagnostics: %v", err)
	}
	if info.ApplicationName != "Dev Bridge" || info.ApplicationVersion != "test" || !info.Ready {
		t.Fatalf("bridge_diagnostics = %+v", info)
	}
	if info.Protocol != "MCP" || info.ProtocolVersion == "" || info.ServerName != mcpserver.ServerName || info.ServerVersion != mcpserver.ServerVersion || info.Transport != mcpserver.TransportName {
		t.Fatalf("bridge_diagnostics protocol metadata = %+v", info)
	}
}

func TestBridgeDiagnosticsReflectsInjectedBuildMetadata(t *testing.T) {
	oldVersion, oldCommit, oldServerVersion := buildinfo.ProductVersion, buildinfo.GitCommit, mcpserver.ServerVersion
	buildinfo.ProductVersion = "0.1.0-dev"
	buildinfo.GitCommit = "0123456789abcdef0123456789abcdef01234567"
	mcpserver.ServerVersion = buildinfo.Version()
	t.Cleanup(func() {
		buildinfo.ProductVersion, buildinfo.GitCommit = oldVersion, oldCommit
		mcpserver.ServerVersion = oldServerVersion
	})

	service, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{
		ApplicationName:    "Dev Bridge",
		ApplicationVersion: buildinfo.Version(),
		Ready:              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "build-info-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "bridge_diagnostics", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("bridge_diagnostics structured content = %T", result.StructuredContent)
	}
	for key := range structured {
		if strings.Contains(strings.ToLower(key), "project") {
			t.Fatalf("bridge_diagnostics returned Project data in field %q", key)
		}
	}
	encoded, _ := json.Marshal(structured)
	var info mcpserver.BridgeInfoOutput
	if err := json.Unmarshal(encoded, &info); err != nil {
		t.Fatal(err)
	}
	if info.ApplicationVersion != "0.1.0-dev+g0123456" || info.ServerVersion != info.ApplicationVersion {
		t.Fatalf("bridge_diagnostics versions = application %q, server %q", info.ApplicationVersion, info.ServerVersion)
	}
}

func TestMalformedAndUnsupportedRequestsFailSafely(t *testing.T) {
	_, _, baseURL := startTestServer(t, readyService(t))
	endpoint := baseURL + mcpserver.MCPPath

	malformed := postRPC(t, endpoint, `{`)
	if malformed.status != http.StatusBadRequest {
		t.Fatalf("malformed status = %d, want 400; body=%s", malformed.status, malformed.body)
	}

	toolsList := postRPC(t, endpoint, `{"jsonrpc":"2.0","id":6,"method":"tools/list","params":{}}`)
	if toolsList.status != http.StatusOK || toolsList.contentType != "application/json" {
		t.Fatalf("tools/list response = status %d, content-type %q, body %s", toolsList.status, toolsList.contentType, toolsList.body)
	}
	if bytes.Contains(toolsList.body, []byte("event:")) || !bytes.Contains(toolsList.body, []byte(`"bridge_diagnostics"`)) || bytes.Contains(toolsList.body, []byte(`"bridge_info"`)) {
		t.Fatalf("tools/list response was not deterministic JSON metadata: %s", toolsList.body)
	}

	unsupportedRequest := `{"jsonrpc":"2.0","id":7,"method":"bridge/missing","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"test-client","version":"1.0.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	unsupported := postRPCVersion(t, endpoint, unsupportedRequest, "2026-07-28")
	if unsupported.status != http.StatusNotFound || unsupported.contentType != "application/json" {
		t.Fatalf("unsupported response = status %d, content-type %q, body %s", unsupported.status, unsupported.contentType, unsupported.body)
	}
	var rpcError struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(unsupported.body, &rpcError); err != nil {
		t.Fatalf("decode unsupported method: %v", err)
	}
	if rpcError.Error.Code != -32601 {
		t.Fatalf("unsupported method code = %d, want -32601; body=%s", rpcError.Error.Code, unsupported.body)
	}

	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("GET MCP: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || response.Header.Get("Allow") != http.MethodPost {
		t.Fatalf("GET MCP status=%d Allow=%q", response.StatusCode, response.Header.Get("Allow"))
	}
}

func TestShutdownGracefullyDrainsActiveToolCall(t *testing.T) {
	service := &blockingService{StaticService: readyService(t), entered: make(chan struct{}), release: make(chan struct{})}
	server, cancel, baseURL := startTestServer(t, service)
	defer cancel()

	callDone := make(chan rpcResponse, 1)
	go func() {
		callDone <- postRPCNoTest(baseURL+mcpserver.MCPPath, `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"bridge_diagnostics","arguments":{}}}`, "")
	}()
	select {
	case <-service.entered:
	case <-time.After(time.Second):
		t.Fatal("bridge_diagnostics handler did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		ctx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		shutdownDone <- server.Shutdown(ctx)
	}()
	select {
	case err := <-shutdownDone:
		t.Fatalf("Shutdown returned before active request drained: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(service.release)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	response := <-callDone
	if response.err != nil || response.status != http.StatusOK {
		t.Fatalf("active call response: status=%d err=%v body=%s", response.status, response.err, response.body)
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestReadinessIsSeparateFromMCP(t *testing.T) {
	_, _, baseURL := startTestServer(t, readyService(t))
	response, err := http.Get(baseURL + mcpserver.ReadinessPath)
	if err != nil {
		t.Fatalf("GET readiness: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read readiness: %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "{\"status\":\"ready\"}\n" {
		t.Fatalf("readiness status=%d body=%q", response.StatusCode, body)
	}
}

type blockingService struct {
	*mcpserver.StaticService
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (s *blockingService) BridgeInfo(ctx context.Context) (mcpserver.BridgeInfo, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true}, nil
	case <-ctx.Done():
		return mcpserver.BridgeInfo{}, ctx.Err()
	}
}

func readyService(t *testing.T) *mcpserver.StaticService {
	t.Helper()
	service, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func startTestServer(t *testing.T, service mcpserver.BridgeService) (*mcpserver.Server, context.CancelFunc, string) {
	t.Helper()
	server, err := mcpserver.New(mcpserver.Config{
		ListenAddress:   "127.0.0.1:0",
		RequestTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
	}, service)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Wait()
	})
	return server, cancel, "http://" + server.Addr().String()
}

type rpcResponse struct {
	status      int
	contentType string
	body        []byte
	err         error
}

func postRPC(t *testing.T, endpoint, body string) rpcResponse {
	t.Helper()
	response := postRPCNoTest(endpoint, body, "")
	if response.err != nil {
		t.Fatalf("POST RPC: %v", response.err)
	}
	return response
}

func postRPCVersion(t *testing.T, endpoint, body, protocolVersion string) rpcResponse {
	t.Helper()
	response := postRPCNoTest(endpoint, body, protocolVersion)
	if response.err != nil {
		t.Fatalf("POST RPC: %v", response.err)
	}
	return response
}

func postRPCNoTest(endpoint, body, protocolVersion string) rpcResponse {
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return rpcResponse{err: err}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	if protocolVersion != "" {
		request.Header.Set("MCP-Protocol-Version", protocolVersion)
		var message struct {
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(body), &message); err == nil && message.Method != "" {
			request.Header.Set("MCP-Method", message.Method)
		}
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return rpcResponse{err: err}
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(response.Body)
	return rpcResponse{
		status:      response.StatusCode,
		contentType: response.Header.Get("Content-Type"),
		body:        responseBody,
		err:         errors.Join(readErr),
	}
}

func testToolVisibility(ui map[string]any) []string {
	if ui == nil {
		return []string{"model", "app"}
	}
	switch values := ui["visibility"].(type) {
	case []string:
		return values
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok {
				result = append(result, item)
			}
		}
		return result
	default:
		return []string{"model", "app"}
	}
}
