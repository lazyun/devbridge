package mcpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/workflowevents"
	"github.com/modelcontextprotocol/go-sdk/auth"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type workflowEventProvider interface {
	WorkflowEvents() *workflowevents.Service
}

func (s *StaticService) WorkflowEvents() *workflowevents.Service {
	if s.control == nil {
		return nil
	}
	return s.control.WorkflowEvents()
}

// The pinned SDK negotiates MCP 2.0 but has no draft Events method registry.
// This narrow HTTP extension shares the original auth, origin and body bounds;
// ordinary tools and discovery still pass through the production SDK.
func workflowEventsHandler(next http.Handler, service *workflowevents.Service, maxBytes int64) http.Handler {
	if service == nil {
		return next
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
		if e != nil {
			http.Error(w, "MCP request too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var rpc struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if json.Unmarshal(raw, &rpc) != nil {
			next.ServeHTTP(w, r)
			return
		}
		if rpc.Method == "server/discover" {
			rec := &eventDiscoveryWriter{ResponseWriter: w}
			next.ServeHTTP(rec, r)
			var envelope map[string]any
			body := rec.body.Bytes()
			if json.Unmarshal(body, &envelope) == nil {
				if result, ok := envelope["result"].(map[string]any); ok {
					if caps, ok := result["capabilities"].(map[string]any); ok {
						caps["events"] = map[string]any{}
						body, _ = json.Marshal(envelope)
					}
				}
			}
			w.Header().Del("Content-Length")
			if rec.status != 0 {
				w.WriteHeader(rec.status)
			}
			w.Write(body)
			return
		}
		if rpc.Method != "events/list" && rpc.Method != "events/subscribe" && rpc.Method != "events/unsubscribe" {
			next.ServeHTTP(w, r)
			return
		}
		observability.Info(r.Context(), "mcp", "mcp.events.requested", "MCP Events request received", map[string]any{"method": rpc.Method})
		write := func(result any, code int, message string) {
			if code != 0 {
				observability.Error(r.Context(), "mcp", "mcp.events.failed", "MCP Events request failed", map[string]any{"method": rpc.Method, "jsonrpcCode": code})
			} else {
				observability.Info(r.Context(), "mcp", "mcp.events.succeeded", "MCP Events request succeeded", map[string]any{"method": rpc.Method})
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			out := map[string]any{"jsonrpc": "2.0", "id": rpc.ID}
			if code != 0 {
				out["error"] = map[string]any{"code": code, "message": message}
			} else {
				out["result"] = result
			}
			json.NewEncoder(w).Encode(out)
		}
		var requestID any
		json.Unmarshal(rpc.ID, &requestID)
		_, stringID := requestID.(string)
		_, numberID := requestID.(float64)
		if rpc.JSONRPC != "2.0" || !stringID && !numberID {
			write(nil, -32600, "A JSON-RPC request ID is required")
			return
		}
		// Version may be carried in either the HTTP binding or per-request MCP meta.
		var meta struct {
			Meta map[string]any `json:"_meta"`
		}
		json.Unmarshal(rpc.Params, &meta)
		contentType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if contentType != "application/json" {
			write(nil, -32600, "JSON content type required")
			return
		}
		version := r.Header.Get("MCP-Protocol-Version")
		if v, ok := meta.Meta[sdkmcp.MetaKeyProtocolVersion].(string); ok {
			if version != "" && version != v {
				write(nil, -32602, "Conflicting MCP protocol versions")
				return
			}
			version = v
		}
		_, hasClientCapabilities := meta.Meta[sdkmcp.MetaKeyClientCapabilities].(map[string]any)
		if version != "2026-07-28" || !hasClientCapabilities || meta.Meta[sdkmcp.MetaKeyProtocolVersion] != "2026-07-28" || r.Header.Get("Mcp-Method") != rpc.Method {
			write(nil, -32602, "MCP Events requires protocol 2026-07-28")
			return
		}
		if rpc.Method == "events/list" {
			write(map[string]any{"resultType": "complete", "events": []any{workflowEventDefinition()}}, 0, "")
			return
		}
		// Do not let protocol metadata become business input or authenticated scope.
		var fields map[string]json.RawMessage
		if json.Unmarshal(rpc.Params, &fields) != nil {
			write(nil, -32602, "Invalid event parameters")
			return
		}
		delete(fields, "_meta")
		params, _ := json.Marshal(fields)
		var in workflowevents.Input
		decoder := json.NewDecoder(bytes.NewReader(params))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&in) != nil {
			write(nil, -32602, "Invalid event parameters")
			return
		}
		ctx, e := withSessionRequest(r.Context(), &sdkmcp.CallToolRequest{Extra: &sdkmcp.RequestExtra{TokenInfo: auth.TokenInfoFromContext(r.Context())}})
		if e != nil {
			write(nil, -32602, "Session ownership unavailable")
			return
		}
		scope, _ := aisession.CallerScopeFromContext(ctx)
		if rpc.Method == "events/unsubscribe" {
			e = service.Unsubscribe(ctx, scope, in)
			if e != nil {
				write(nil, -32602, "Subscription is invalid or unavailable")
				return
			}
			write(map[string]any{}, 0, "")
			return
		}
		result, e := service.Subscribe(ctx, scope, in)
		if e != nil {
			write(nil, -32015, "Subscription or callback verification unavailable")
			return
		}
		write(result, 0, "")
	})
	return http.NewCrossOriginProtection().Handler(handler)
}

type eventDiscoveryWriter struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
}

func (w *eventDiscoveryWriter) WriteHeader(code int)        { w.status = code }
func (w *eventDiscoveryWriter) Write(b []byte) (int, error) { return w.body.Write(b) }
func workflowEventDefinition() map[string]any {
	fields := map[string]any{"aiSessionId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "taskId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}
	payload := map[string]any{}
	for _, key := range []string{"aiSessionId", "projectId", "taskId", "runId", "sourceType"} {
		payload[key] = map[string]any{"type": "string"}
	}
	return map[string]any{"name": workflowevents.Name, "description": "Observe durable changes for one explicitly selected Task. Read current facts before responding. For NATURAL_V1 native completion with analysisState=REQUIRED, read run_analysis_get and submit independent GPT analysis through run_analysis_submit before requesting a user decision. An event never approves, resumes, or starts work.", "delivery": []string{"webhook"}, "inputSchema": map[string]any{"type": "object", "properties": fields, "required": []string{"aiSessionId", "taskId"}, "additionalProperties": false}, "payloadSchema": map[string]any{"type": "object", "properties": payload, "required": []string{"aiSessionId", "projectId", "taskId", "runId", "sourceType"}, "additionalProperties": false}}
}
