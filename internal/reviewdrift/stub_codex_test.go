package reviewdrift_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
)

// stubCodexSequence makes every stub thread/turn identifier globally unique;
// Bridge enforces their uniqueness across Runs.
var stubCodexSequence atomic.Int64

// stubCodexSession satisfies the execution Codex port without Codex: every
// turn completes immediately with a bounded AgentClaim, so background
// Review Runs finish deterministically.
type stubCodexSession struct {
	id      string
	mu      sync.Mutex
	threads int
	turns   int
}

func (s *stubCodexSession) StartThread(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads++
	return fmt.Sprintf("thread-stub-%s", s.id), nil
}

func (s *stubCodexSession) StartTurn(_ context.Context, _, _ string) (execution.CodexTurnResult, error) {
	s.mu.Lock()
	s.turns++
	turn := execution.CodexTurnResult{TurnID: fmt.Sprintf("turn-stub-%s-%d", s.id, s.turns), Message: "Stub engineering claim."}
	s.mu.Unlock()
	return turn, nil
}

func (s *stubCodexSession) Close() error { return nil }

type stubCodexFactory struct{}

func (f *stubCodexFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return &stubCodexSession{id: fmt.Sprintf("%d", stubCodexSequence.Add(1))}, nil
}

func (s *stubCodexSession) ResumeThread(_ context.Context, threadID, _ string, _ *run.TokenCounts) (string, error) {
	return threadID, nil
}

func (s *stubCodexSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, _ json.RawMessage) (execution.CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if err == nil {
		result.Message = `{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","summary":"Stub review completed.","workSpec":null,"verificationSpec":null}`
	}
	return result, err
}
