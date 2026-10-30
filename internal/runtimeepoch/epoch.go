// Package runtimeepoch carries the immutable identity of a runtime callback.
// Stores must validate it against durable authority inside the write transaction.
package runtimeepoch

import (
	"context"
	"errors"
)

var ErrFenced = errors.New("RUNTIME_EPOCH_FENCED")

type Fence struct {
	HostTool bool
	Epoch    string
	RunID    string
}

type key struct{}

func WithFence(ctx context.Context, epoch, runID string) context.Context {
	return context.WithValue(ctx, key{}, Fence{Epoch: epoch, RunID: runID})
}

func FromContext(ctx context.Context) (Fence, bool) {
	fence, ok := ctx.Value(key{}).(Fence)
	return fence, ok
}

// WithHostToolFence closes a previously authorized interaction at the actual
// write boundary, including the interval between finalization intent and Run terminal.
func WithHostToolFence(ctx context.Context, epoch, runID string) context.Context {
	return context.WithValue(ctx, key{}, Fence{Epoch: epoch, RunID: runID, HostTool: true})
}
