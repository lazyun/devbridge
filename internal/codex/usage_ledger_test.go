package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func usageTurn(t *testing.T, client *Client, requests *bufio.Reader, responses io.Writer, turnID string, beforeResponse bool, samples ...int64) TurnResult {
	t.Helper()
	done := make(chan TurnResult, 1)
	go func() {
		value, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", Prompt: "continue"})
		if err != nil {
			t.Errorf("turn: %v", err)
		}
		done <- value
	}()
	request := readClientRequest(t, requests)
	respond := func() {
		writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}})
	}
	if !beforeResponse {
		respond()
	}
	for _, total := range samples {
		// last deliberately contradicts total and must not be summed.
		writeTokenUsage(t, responses, "canonical", turnID, tokenBreakdown(total, total/2, 0, 0, 0, total), tokenBreakdown(99999, 0, 0, 0, 0, 99999), nil)
		writeTokenUsage(t, responses, "canonical", "old-turn", tokenBreakdown(1, 0, 0, 0, 0, 1), tokenBreakdown(1, 0, 0, 0, 0, 1), nil)
		writeTokenUsage(t, responses, "foreign", turnID, tokenBreakdown(99999, 0, 0, 0, 0, 99999), tokenBreakdown(1, 0, 0, 0, 0, 1), nil)
	}
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}}}})
	// Late telemetry must be rejected even before the result is consumed.
	writeTokenUsage(t, responses, "canonical", turnID, tokenBreakdown(99999, 0, 0, 0, 0, 99999), tokenBreakdown(1, 0, 0, 0, 0, 1), nil)
	if beforeResponse {
		respond()
	}
	return <-done
}

func TestUsagePersistenceRetainsRunFence(t *testing.T) {
	store := &fenceCheckingUsageStore{}
	client, requests, responses := newClientFixture(t)
	client.usageStore = store
	ctx := runtimeepoch.WithFence(context.Background(), "runtime-current", "run-current")
	client.installUsageLedger(ctx, "canonical", true, nil)
	if store.saves != 0 {
		t.Fatalf("Run-fenced Thread bootstrap wrote before durable Run binding: saves=%d", store.saves)
	}
	done := make(chan TurnResult, 1)
	go func() {
		value, err := client.StartTurn(ctx, TurnOptions{RunID: "run-current", ThreadID: "canonical", Prompt: "continue"})
		if err != nil {
			t.Errorf("turn: %v", err)
		}
		done <- value
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-current", "status": "inProgress"}}})
	writeTokenUsage(t, responses, "canonical", "turn-current", tokenBreakdown(10, 0, 0, 0, 0, 10), tokenBreakdown(10, 0, 0, 0, 0, 10), nil)
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn-current", "status": "completed"}}})
	<-done
	if !store.sawRunFence {
		t.Fatal("attribution persistence discarded the Run fence")
	}
}

func TestEpochLedgerSurvivesColdClientsAndUnavailableRun(t *testing.T) {
	store := &serializedUsageStore{}
	for i, tc := range []struct {
		total int64
		exact bool
		delta int64
		epoch int64
	}{
		{100, true, 100, 1}, {140, true, 40, 1}, {10, false, 0, 2}, {30, true, 20, 2}, {30, true, 0, 2},
	} {
		client, requests, responses := newClientFixture(t)
		client.usageStore = store
		// The stale caller baseline must lose to the durable ledger, including
		// after an UNAVAILABLE reset Run. Load decodes the serialized ledger.
		client.installUsageLedger(context.Background(), "canonical", i == 0, &TokenCounts{TotalTokens: 999999})
		got := usageTurn(t, client, requests, responses, string(rune('a'+i)), i%2 == 0, tc.total, tc.total)
		if got.ThreadID != "canonical" || got.TokenUsage == nil || got.TokenUsage.Reliable != tc.exact || got.TokenUsage.Attribution.EndEpoch != tc.epoch {
			t.Fatalf("step %d: %+v", i, got.TokenUsage)
		}
		if tc.exact && got.TokenUsage.EndCumulative.TotalTokens-got.TokenUsage.StartCumulative.TotalTokens != tc.delta {
			t.Fatalf("step %d: %+v", i, got.TokenUsage)
		}
		ledger, exists, err := store.Load(context.Background(), "canonical")
		if err != nil || !exists || !ledger.Anchored || ledger.InFlightTurnID != "" || ledger.PreviousRawCumulative.TotalTokens != tc.total || ledger.AccountingEpoch != tc.epoch {
			t.Fatalf("step %d ledger=%+v exists=%v err=%v", i, ledger, exists, err)
		}
		if _, exists, _ := store.Load(context.Background(), "foreign"); exists {
			t.Fatal("foreign telemetry created ledger")
		}
	}
}

func TestWarmResumePreservesEpochAndIgnoresOldReplay(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.installUsageLedger(context.Background(), "canonical", true, nil)
	usageTurn(t, client, requests, responses, "first", false, 100)
	done := make(chan error, 1)
	go func() {
		_, err := client.ResumeThread(context.Background(), ThreadResumeOptions{ThreadID: "canonical", CWD: "/work", UsageBaseline: &TokenCounts{}})
		done <- err
	}()
	request := readClientRequest(t, requests)
	writeTokenUsage(t, responses, "canonical", "first", tokenBreakdown(2, 0, 0, 0, 0, 2), tokenBreakdown(1000, 0, 0, 0, 0, 1000), nil)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"thread": map[string]any{"id": "canonical"}, "cwd": "/work"}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := usageTurn(t, client, requests, responses, "second", true, 120)
	if !got.TokenUsage.Reliable || got.TokenUsage.StartCumulative.TotalTokens != 100 || got.TokenUsage.EndCumulative.TotalTokens != 120 || got.TokenUsage.Attribution.EndEpoch != 1 {
		t.Fatal(got.TokenUsage)
	}
}

type failedUsageStore struct{ load bool }

func (s failedUsageStore) Load(context.Context, string) (codexusage.CodexUsageLedger, bool, error) {
	if s.load {
		return codexusage.CodexUsageLedger{}, false, errors.New("load failed")
	}
	return codexusage.CodexUsageLedger{}, false, nil
}
func (failedUsageStore) Save(context.Context, int64, codexusage.CodexUsageLedger, *codexusage.RunUsageAttribution) error {
	return errors.New("save failed")
}

func TestLedgerIOFailureDoesNotFabricateExactUsageOrFailConversation(t *testing.T) {
	for _, load := range []bool{false, true} {
		client, requests, responses := newClientFixture(t)
		client.usageStore = failedUsageStore{load: load}
		client.installUsageLedger(context.Background(), "canonical", true, nil)
		got := usageTurn(t, client, requests, responses, "turn", false, 100)
		if got.ThreadID != "canonical" || got.TokenUsage.Reliable || got.TokenUsage.Attribution.Reason != "PERSISTENCE_FAILED" {
			t.Fatal(got)
		}
	}
}

func TestMissingRawCumulativeCannotBecomeZero(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"totalTokens":0}`, `{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":null}`} {
		if _, err := decodeRawCumulative(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted missing usage: %q", raw)
		}
	}
	zero, err := decodeRawCumulative(json.RawMessage(`{"inputTokens":0,"cachedInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":0}`))
	if err != nil || zero != (TokenCounts{}) {
		t.Fatalf("explicit zero=%+v err=%v", zero, err)
	}
}

func TestMissingTotalAtEndDegradesRunAndInvalidatesNextBaseline(t *testing.T) {
	client, requests, responses := newClientFixture(t)
	client.installUsageLedger(context.Background(), "canonical", true, nil)
	done := make(chan TurnResult, 1)
	go func() {
		v, _ := client.StartTurn(context.Background(), TurnOptions{ThreadID: "canonical", Prompt: "continue"})
		done <- v
	}()
	request := readClientRequest(t, requests)
	writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn", "status": "inProgress"}}})
	writeTokenUsage(t, responses, "canonical", "turn", tokenBreakdown(10, 0, 0, 0, 0, 10), tokenBreakdown(10, 0, 0, 0, 0, 10), nil)
	writeServerJSON(t, responses, map[string]any{"method": "thread/tokenUsage/updated", "params": map[string]any{"threadId": "canonical", "turnId": "turn", "tokenUsage": map[string]any{"last": map[string]any{"totalTokens": 100}}}})
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "canonical", "turn": map[string]any{"id": "turn", "status": "completed"}}})
	got := <-done
	if got.TokenUsage.Reliable || got.TokenUsage.Attribution.Reason != "INVALID_TELEMETRY" {
		t.Fatal(got.TokenUsage)
	}
	client.mu.Lock()
	anchored := client.threadUsage["canonical"].ledger.Anchored
	client.mu.Unlock()
	if anchored {
		t.Fatal("invalid end left an exact baseline")
	}
}

// Exercise the production Store boundary without importing the SQLite package
// (which depends on codex through approvals). SQLite atomic/reopen behavior is
// covered separately by codex_usage_store_test.go.
type serializedUsageStore struct {
	mu   sync.Mutex
	data []byte
}

type fenceCheckingUsageStore struct {
	serializedUsageStore
	sawRunFence bool
	saves       int
}

func (s *fenceCheckingUsageStore) Save(ctx context.Context, expected int64, ledger codexusage.CodexUsageLedger, attribution *codexusage.RunUsageAttribution) error {
	s.saves++
	if attribution != nil {
		fence, ok := runtimeepoch.FromContext(ctx)
		if !ok || fence.Epoch != "runtime-current" || fence.RunID != attribution.RunID {
			return errors.New("usage attribution missing its Run fence")
		}
		s.sawRunFence = true
	}
	return s.serializedUsageStore.Save(ctx, expected, ledger, attribution)
}

func (s *serializedUsageStore) Load(_ context.Context, threadID string) (codexusage.CodexUsageLedger, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ledger codexusage.CodexUsageLedger
	if len(s.data) == 0 {
		return ledger, false, nil
	}
	err := json.Unmarshal(s.data, &ledger)
	return ledger, ledger.ThreadID == threadID, err
}
func (s *serializedUsageStore) Save(_ context.Context, expected int64, ledger codexusage.CodexUsageLedger, _ *codexusage.RunUsageAttribution) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var current codexusage.CodexUsageLedger
	if len(s.data) > 0 {
		if err := json.Unmarshal(s.data, &current); err != nil {
			return err
		}
	}
	if current.Revision != expected {
		return errors.New("stale usage writer")
	}
	s.data, _ = json.Marshal(ledger)
	return nil
}
