package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

// Authorization and workflow routing are host responsibilities. This instruction
// is runtime policy, separate from the byte-identical approved engineering task.
const executorInstructions = `Execute the supplied engineering task in the assigned workspace using the available file and command tools. Workflow authorization and dispatch have already been handled by the host before this turn; do not search for workflow receipts, session or task identifiers, or request them from the user. Respect the assigned scope, repository instructions, sandbox and native permission requests. Report the actual work, checks and limitations honestly; a normal turn completion does not establish engineering success. You do not operate the host's task, approval, recovery or delivery workflow.`

// Codex retains old developer messages across resume and compaction. Send the
// current policy through the per-turn instruction channel as well as thread
// configuration, preserving the approved user instruction byte for byte.
func (c *Client) executorTurnMode(options TurnOptions, binding RuntimeBinding) (*wire.CollaborationMode, error) {
	if !c.executorBoundary {
		return nil, nil
	}
	if strings.TrimSpace(options.Model) == "" {
		return nil, errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: frozen turn model required")
	}
	var effort *string
	if options.ReasoningEffort != "" {
		value := options.ReasoningEffort
		effort = &value
	}
	return &wire.CollaborationMode{Mode: "default", Settings: wire.CollaborationSettings{
		Model: options.Model, ReasoningEffort: effort,
		DeveloperInstructions: executorInstructions + "\nThe following host runtime policy applies to this turn and supersedes earlier host operation roles and sandbox descriptions retained in this conversation, including compacted history. It does not expand the native sandbox or change the approved engineering task.\n" + c.taskInstructions(binding),
	}}, nil
}

// executorConfig resolves all inherited MCP names rather than relying on an
// empty table, which merges with configuration layers instead of clearing them.
// This also covers project-local MCP entries. Native file/command tools and
// explicitly bound dynamic tools are unaffected.
func (c *Client) executorConfig(ctx context.Context, cwd string) (map[string]any, string, error) {
	if !c.executorBoundary {
		return nil, "", nil // In-memory protocol fixtures do not launch a runtime.
	}
	var response struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := c.rpc.Call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, &response); err != nil {
		return nil, "", fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: read effective config: %w", err)
	}
	if response.Config == nil {
		return nil, "", errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: effective config missing")
	}
	servers := map[string]json.RawMessage{}
	if raw, ok := response.Config["mcp_servers"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, "", fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: MCP config: %w", err)
		}
	}
	disabled := map[string]any{}
	for name := range servers {
		disabled[name] = map[string]any{"enabled": false}
	}
	return map[string]any{
		"features.apps":                         false,
		"features.plugins":                      false,
		"features.remote_plugin":                false,
		"features.skill_mcp_dependency_install": false,
		"mcp_servers":                           disabled,
	}, executorInstructions, nil
}

func (c *Client) markExecutorThread(thread string) {
	c.runtimeMu.Lock()
	defer c.runtimeMu.Unlock()
	if c.executorThreads == nil {
		c.executorThreads = map[string]bool{}
	}
	c.executorThreads[thread] = true
}

// Check the actual thread inventory before allowing its first new turn. This
// catches ignored/overridden configuration instead of treating accepted flags
// as proof of isolation. Disabled entries may remain visible to the host only.
func (c *Client) verifyExecutorTools(ctx context.Context, thread string) error {
	if !c.executorBoundary {
		return nil
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		params := map[string]any{"threadId": thread, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data *[]struct {
				Name              string                     `json:"name"`
				RuntimeStatus     string                     `json:"runtimeStatus"`
				Tools             map[string]json.RawMessage `json:"tools"`
				Resources         []json.RawMessage          `json:"resources"`
				ResourceTemplates []json.RawMessage          `json:"resourceTemplates"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := c.rpc.Call(ctx, "mcpServerStatus/list", params, &result); err != nil {
			return fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: inspect thread tools: %w", err)
		}
		if result.Data == nil {
			return errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: thread inventory missing")
		}
		for _, server := range *result.Data {
			if server.RuntimeStatus != "disabled" || len(server.Tools) != 0 || len(server.Resources) != 0 || len(server.ResourceTemplates) != 0 {
				return errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: inherited MCP server remains available")
			}
		}
		if result.NextCursor == nil {
			return nil
		}
		cursor = *result.NextCursor
		if cursor == "" || seen[cursor] {
			break
		}
		seen[cursor] = true
	}
	return errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: thread inventory pagination unproven")
}

// A cold connection may observe/rejoin an old active turn without rewriting it.
// Starting any new turn requires the isolated start/resume contract first.
func (c *Client) ensureExecutorThread(ctx context.Context, options TurnOptions, binding RuntimeBinding) error {
	if !c.executorBoundary {
		return nil
	}
	c.runtimeMu.Lock()
	qualified := c.executorThreads[options.ThreadID]
	c.runtimeMu.Unlock()
	if qualified {
		return nil
	}
	if len(binding.RuntimeWorkspaceRoots) == 0 {
		return errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: workspace binding required")
	}
	var observed struct {
		Thread struct {
			ID     string `json:"id"`
			CWD    string `json:"cwd"`
			Status struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"thread"`
	}
	if err := c.rpc.Call(ctx, "thread/read", map[string]any{"threadId": options.ThreadID, "includeTurns": false}, &observed); err != nil {
		return fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: inspect cold thread: %w", err)
	}
	if observed.Thread.ID != options.ThreadID || observed.Thread.Status.Type != "idle" {
		return errors.New("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: exact idle thread required")
	}
	// Resume only an idle exact thread, preserving history and its actual CWD.
	tier := ""
	if options.ServiceTier != nil {
		tier = *options.ServiceTier
	}
	_, err := c.ResumeThread(ctx, ThreadResumeOptions{ThreadID: options.ThreadID, CWD: observed.Thread.CWD, RuntimeBinding: binding, Model: options.Model, ReasoningEffort: options.ReasoningEffort, ServiceTier: tier})
	return err
}

// injectTurnPolicy appends the current turn's developer policy to the exact
// thread's model-visible history via thread/inject_items, so the current role
// persists as the latest developer message after any compacted older roles.
// It runs only for a new executor turn after the reservation in beginTurnStart;
// the caller must release that reservation when this fails, before turn/start.
func (c *Client) injectTurnPolicy(ctx context.Context, threadID string, mode *wire.CollaborationMode) error {
	if mode == nil || mode.Settings.DeveloperInstructions == "" {
		return nil
	}
	item, err := json.Marshal(map[string]any{
		"type": "message", "role": "developer",
		"content": []any{map[string]any{"type": "input_text", "text": mode.Settings.DeveloperInstructions}},
	})
	if err != nil {
		return fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: encode policy item: %w", err)
	}
	var response wire.ThreadInjectItemsResponse
	params := wire.ThreadInjectItemsParams{ThreadID: threadID, Items: []json.RawMessage{item}}
	if err := c.rpc.Call(ctx, "thread/inject_items", params, &response); err != nil {
		return fmt.Errorf("CODEX_EXECUTOR_BOUNDARY_UNAVAILABLE: inject current turn policy: %w", err)
	}
	return nil
}
