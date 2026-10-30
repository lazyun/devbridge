package codex

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/codex/wire"
)

// steerCurrentTurn is only installed on the transport owner's exact runtime
// Session. Core records the dispatch intent before invoking this handler.
func (c *Client) steerCurrentTurn(ctx context.Context, in agentruntime.SteeringInput) string {
	key := turnKey{threadID: in.ThreadID, turnID: in.TurnID}
	c.mu.Lock()
	state := c.turns[key]
	active := state != nil && state.completion == nil
	c.mu.Unlock()
	if !active {
		return "REJECTED"
	}
	var response wire.TurnSteerResponse
	err := c.rpc.Call(ctx, "turn/steer", wire.TurnSteerParams{
		ThreadID: in.ThreadID, ExpectedTurnID: in.TurnID,
		Input: []wire.UserInput{{Type: "text", Text: in.Text}},
	}, &response)
	if err != nil {
		var rejection *RPCError
		if errors.As(err, &rejection) && (rejection.Code == -32600 || rejection.Code == -32601 || rejection.Code == -32602) {
			return "REJECTED"
		}
		return "UNKNOWN"
	}
	if response.TurnID != in.TurnID {
		return "UNKNOWN"
	}
	return "ACCEPTED"
}
