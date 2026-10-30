package codex

import (
	"context"

	"github.com/lazyun/devbridge/internal/agentruntime"
)

// finishRuntimeTurn drains observations before the executor checks its gate.
// An unresolved response keeps its exact notification routing until Client.Close.
func (c *Client) finishRuntimeTurn(ctx context.Context, runID, threadID string, session *agentruntime.Session) {
	if err := session.Flush(context.WithoutCancel(ctx)); err == nil && c.agentRuntime != nil {
		if snapshot, err := c.agentRuntime.Read(context.WithoutCancel(ctx), runID); err == nil && agentruntime.HasUnresolvedResponse(snapshot) {
			return
		}
	}
	session.Close()
	c.runtimeMu.Lock()
	if c.runtimeSessions[threadID] == session {
		delete(c.runtimeSessions, threadID)
	}
	c.runtimeMu.Unlock()
}
