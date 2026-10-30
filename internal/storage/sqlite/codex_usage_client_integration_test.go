package sqlite

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

// Exercise the real stdio client and SQLite store in the Coordinator's exact
// startup order. A storage-only fixture with an already RUNNING Run would miss
// a fence that poisons the ledger before Thread binding has happened.
func TestUsageClientPersistsAcrossThreadBindingAndEarlyCompletion(t *testing.T) {
	for _, owned := range []bool{true, false} {
		name := "probe"
		if owned {
			name = "run"
		}
		t.Run(name, func(t *testing.T) {
			f := newAgentStorageFixture(t, "client-usage-"+name)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var runID string
			conversations := NewAgentConversationStore(f.db)
			runs := run.NewService(NewRunStore(f.db), nil, nil)
			if owned {
				r, _ := beginRecoveryRun(t, f, agentconversation.Execution, "client-usage", false)
				runID = r.ID
				ctx = runtimeepoch.WithFence(ctx, r.RuntimeEpoch, r.ID)
				if err := conversations.MarkThreadStartPending(ctx, runID); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DEVBRIDGE_USAGE_STDIO_HELPER", "1")
			command := filepath.Join(t.TempDir(), "codex-usage-fixture")
			quotedBinary := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err := os.WriteFile(command, []byte("#!/bin/sh\nexec "+quotedBinary+" -test.run=^TestUsageClientStdioHelper$ -- \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			store := NewCodexUsageStore(f.db)
			client, err := codex.Start(ctx, codex.Config{CodexPath: command, UsageStore: store})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			thread, err := client.StartThread(ctx, codex.ThreadOptions{CWD: f.root, Model: "gpt-5.6-terra"})
			if err != nil {
				t.Fatal(err)
			}
			if owned {
				if _, exists, err := store.Load(ctx, thread.ID); err != nil || exists {
					t.Fatalf("unbound Run must defer durable ledger: exists=%v err=%v", exists, err)
				}
				if err := conversations.BindThread(ctx, runID, thread.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := runs.AttachThread(ctx, runID, thread.ID); err != nil {
					t.Fatal(err)
				}
				if _, err := runs.Transition(ctx, runID, run.StatusRunning); err != nil {
					t.Fatal(err)
				}
				if err := conversations.MarkTurnPending(ctx, runID); err != nil {
					t.Fatal(err)
				}
			}
			result, err := client.StartTurn(ctx, codex.TurnOptions{Model: "gpt-5.6-terra", ThreadID: thread.ID, RunID: runID, Prompt: "fixture", OnStarted: func(_, turnID string) error {
				if !owned {
					return nil
				}
				_, err := runs.AttachTurn(ctx, runID, turnID)
				return err
			}})
			if err != nil || result.TokenUsage == nil || !result.TokenUsage.Reliable || result.TokenUsage.EndCumulative.TotalTokens != 10 {
				t.Fatalf("legitimate client usage lost: result=%+v usage=%+v err=%v", result, result.TokenUsage, err)
			}
			ledger, exists, err := store.Load(ctx, thread.ID)
			if err != nil || !exists || ledger.InFlightTurnID != "" || ledger.LogicalCumulative.TotalTokens != 10 {
				t.Fatalf("durable ledger=%+v exists=%v err=%v", ledger, exists, err)
			}
			if owned {
				a, err := store.Attribution(ctx, runID)
				if err != nil || !a.Complete || !a.Reliable || a.RunID != runID || a.TurnID != result.TurnID {
					t.Fatalf("durable attribution=%+v err=%v", a, err)
				}
			}
		})
	}
}

// Test-binary helper: emits usage and completion before turn/start responds,
// covering callbacks before the Coordinator can durably attach the Turn ID.
func TestUsageClientStdioHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_USAGE_STDIO_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		result := map[string]any{}
		switch request.Method {
		case "config/read":
			result = map[string]any{"config": map[string]any{"mcp_servers": map[string]any{}}}
		case "mcpServerStatus/list":
			result = map[string]any{"data": []any{}, "nextCursor": nil}
		case "thread/start":
			result["thread"] = map[string]any{"id": "client-usage-thread"}
		case "turn/start":
			counts := map[string]any{"inputTokens": 10, "cachedInputTokens": 0, "outputTokens": 0, "reasoningOutputTokens": 0, "totalTokens": 10}
			_ = encoder.Encode(map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "client-usage-thread", "turnId": "client-usage-turn", "tokenUsage": map[string]any{"total": counts, "last": counts}}})
			turn := map[string]any{"id": "client-usage-turn", "status": "completed", "items": []any{}}
			_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "client-usage-thread", "turn": turn}})
			result["turn"] = turn
		}
		_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
	}
	os.Exit(0)
}
