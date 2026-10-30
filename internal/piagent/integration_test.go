package piagent

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/review"
)

// TestFactoryImplementsCodexFactory verifies that Factory implements execution.CodexFactory
func TestFactoryImplementsCodexFactory(t *testing.T) {
	var _ execution.CodexFactory = (*Factory)(nil)
}

// TestSessionImplementsCodexSession verifies that Session implements execution.CodexSession
func TestSessionImplementsCodexSession(t *testing.T) {
	var _ execution.CodexSession = (*Session)(nil)
}

// TestExecutorKindPiAgentIsValid verifies that PI_AGENT is a valid executor kind
func TestExecutorKindPiAgentIsValid(t *testing.T) {
	if !review.ExecutorPiAgent.Valid() {
		t.Error("ExecutorPiAgent should be a valid executor kind")
	}
}

// TestFactoryStartCreatesSession verifies that Factory.Start creates a session
func TestFactoryStartCreatesSession(t *testing.T) {
	factory := &Factory{}
	ctx := context.Background()
	config := codexconfig.Config{}

	session, err := factory.Start(ctx, config)
	if err != nil {
		t.Fatalf("Factory.Start failed: %v", err)
	}
	if session == nil {
		t.Fatal("Factory.Start returned nil session")
	}
	defer session.Close()
}
