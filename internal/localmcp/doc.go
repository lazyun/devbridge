// Package localmcp is the dedicated local Local MCP adapter for Dev
// Bridge. It exposes the same product capabilities as the GPT adapter through
// its own MCP registration, plus one generic structured decision capability
// (decision_respond) in place of the ChatGPT app-private ui_* decision tools.
//
// Architecture (frozen):
//
//	ChatGPT   → OpenAI Tunnel      → GPT adapter (internal/mcpserver) ─┐
//	                                                              ▼
//	                                       Bridge application services
//	                                                              ▲
//	Local MCP-capable AI client → local MCP → this adapter ────────────────┘
//
// Both adapters may run at the same time. They share the Bridge application
// services (mcpserver.BridgeService, implemented by mcpserver.StaticService)
// and the shared tool cores in internal/mcpserver/adapter_core.go, but never
// each other's presentation, session, or decision adapter code. No public
// MCP endpoint and no OpenAI Tunnel involvement exists for local MCP clients.
//
// Session mechanism (v0.1): each local MCP client conversation establishes its own
// Bridge aiSessionId through project_list → project_open, and carries that
// aiSessionId explicitly in every subsequent tool call. Reopening an existing
// aiSessionId is allowed only for its immutable Project binding. Bridge does not
// attach any Local-MCP-specific identity to domain objects: Project, Feature Area,
// Document, Review, Task, and Execution state is durable Project-owned state
// that any authorized session — ChatGPT or a local MCP client — can read and continue.
//
// Asynchronous operations (Review, Execution) remain owned
// by Bridge. This adapter implements no automatic conversation wake/resume:
// the user returns later, the LLM calls workspace_get or a detail read, and
// continues from durable state.
package localmcp
