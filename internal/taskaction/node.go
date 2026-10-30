// Package taskaction defines the shared Task action node. It does not grant
// domain authority: command owners must still validate their original inputs.
package taskaction

import "context"

type Expectation struct {
	TaskID     string `json:"taskId"`
	Generation int64  `json:"generation"`
}

type Node struct {
	Candidates []Candidate `json:"candidates,omitempty"`
	TaskID     string      `json:"taskId"`
	ID         string      `json:"id"`
	Generation int64       `json:"generation"`
	Kind       string      `json:"kind"`
	State      string      `json:"state"`
	Blocking   bool        `json:"blocking"`
	Owned      bool        `json:"owned"`
	Action     string      `json:"action,omitempty"`
	IntentID   string      `json:"intentId,omitempty"`
	Reason     string      `json:"reason,omitempty"`
}

type Candidate struct {
	Consumed    bool   `json:"consumed"`
	IntentID    string `json:"intentId"`
	Version     int    `json:"version"`
	Purpose     string `json:"purpose"`
	ContentHash string `json:"contentHash"`
	Action      string `json:"action"`
	Selectable  bool   `json:"selectable"`
}

type ConflictSelection struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId"`
	Generation  int64  `json:"generation"`
	IntentID    string `json:"intentId"`
	Version     int    `json:"version"`
	Purpose     string `json:"purpose"`
	ContentHash string `json:"contentHash"`
}

type intentKey struct{}
type expectationKey struct{}
type commandKey struct{}

// WithIntent carries an already consumed parent through its canonical steps.
// It is set only inside Core, never decoded from a public request.
func WithIntent(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, intentKey{}, id)
}
func Intent(ctx context.Context) string { v, _ := ctx.Value(intentKey{}).(string); return v }
func WithExpectation(ctx context.Context, v Expectation) context.Context {
	return context.WithValue(ctx, expectationKey{}, v)
}
func Expected(ctx context.Context) (Expectation, bool) {
	v, ok := ctx.Value(expectationKey{}).(Expectation)
	return v, ok
}

func WithCommand(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, commandKey{}, id)
}
func Command(ctx context.Context) string { v, _ := ctx.Value(commandKey{}).(string); return v }
