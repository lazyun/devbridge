package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStartTurnPreservesFirstCompletedCommandEvidence(t *testing.T) {
	first := commandEvidenceItem("cmd-1", "go test ./...", "completed", 7, "test failed", 12)
	changed := commandEvidenceItem("cmd-1", "go test ./...", "completed", 0, "test passed", 99)
	metadata := commandEvidenceItem("cmd-1", "go vet ./...", "completed", 0, "replacement", 99)
	metadata["cwd"] = "/other-worktree"
	sparse := commandEvidenceItem("cmd-1", "go test ./...", "completed", nil, nil, nil)
	started := commandEvidenceItem("cmd-1", "go test ./...", "inProgress", nil, nil, nil)
	want := commandEvidenceValue("cmd-1", "go test ./...", "completed", 7, "test failed", 12, true)

	for _, tc := range []struct {
		name      string
		after     []map[string]any
		snapshot  []any
		omitStart bool
		earlyTurn bool
	}{
		{name: "conflicting_completion", after: []map[string]any{commandEvidenceEvent("item/completed", changed, 1_700_000_000_099)}},
		{name: "conflicting_command_identity", after: []map[string]any{commandEvidenceEvent("item/completed", metadata, 1_700_000_000_099)}},
		{name: "sparse_completion_cannot_erase", after: []map[string]any{commandEvidenceEvent("item/completed", sparse, 1_700_000_000_099)}},
		{name: "late_start_cannot_regress", after: []map[string]any{commandEvidenceEvent("item/started", started, 1_700_000_000_099)}},
		{name: "late_start_cannot_invent_timestamp", omitStart: true, after: []map[string]any{commandEvidenceEvent("item/started", started, 1_700_000_000_099)}},
		{name: "exact_replay", after: []map[string]any{commandEvidenceEvent("item/completed", first, 1_700_000_000_012)}},
		{name: "turn_snapshot_preserves_completion_timestamp", snapshot: []any{first}},
		{name: "turn_snapshot_cannot_replace_evidence", snapshot: []any{changed}},
		{name: "completion_before_start_response", earlyTurn: true, after: []map[string]any{commandEvidenceEvent("item/completed", changed, 1_700_000_000_099)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []map[string]any
			if !tc.omitStart {
				events = append(events, commandEvidenceEvent("item/started", started, 1_700_000_000_000))
			}
			events = append(events, commandEvidenceEvent("item/completed", first, 1_700_000_000_012))
			events = append(events, tc.after...)
			got := commandEvidenceTurn(t, events, tc.snapshot, nil, tc.earlyTurn)
			expected := want
			if tc.omitStart {
				expected.StartedAt = nil
			}
			assertCommandEvidence(t, got.Commands, []CommandExecution{expected})
		})
	}
}

func TestStartTurnKeepsCompletedCommandNullsAndBoundedOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		output any
	}{
		{name: "unknown_completed_fields", status: "completed", output: nil},
		{name: "unknown_declined_fields", status: "declined", output: nil},
		{name: "bounded_failed_output", status: "failed", output: strings.Repeat("x", maxCommandOutputBytes+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := commandEvidenceItem("cmd-1", "go test ./...", tc.status, nil, tc.output, nil)
			later := commandEvidenceItem("cmd-1", "go test ./...", "completed", 0, "later claim", 20)
			got := commandEvidenceTurn(t, []map[string]any{
				commandEvidenceEvent("item/completed", first, 1_700_000_000_012),
				commandEvidenceEvent("item/completed", later, 1_700_000_000_020),
			}, []any{later}, nil, false)
			finished := time.UnixMilli(1_700_000_000_012).UTC()
			want := CommandExecution{ItemID: "cmd-1", Command: "go test ./...", CWD: "/worktree", Status: tc.status, Actions: []string{"go test ./..."}, FinishedAt: &finished}
			if tc.output != nil {
				want.Output = strings.Repeat("x", maxCommandOutputBytes)
				want.OutputTruncated = true
			}
			assertCommandEvidence(t, got.Commands, []CommandExecution{want})
		})
	}
}

func TestStartTurnSealsCommandEvidenceAtTurnCompletion(t *testing.T) {
	started := commandEvidenceItem("cmd-1", "go test ./...", "inProgress", nil, nil, nil)
	completed := commandEvidenceItem("cmd-1", "go test ./...", "completed", 0, "late result", 12)
	newCommand := commandEvidenceItem("cmd-2", "go vet ./...", "completed", 0, "late addition", 12)
	got := commandEvidenceTurn(t, []map[string]any{
		commandEvidenceEvent("item/started", started, 1_700_000_000_000),
	}, nil, []map[string]any{
		commandEvidenceEvent("item/completed", completed, 1_700_000_000_012),
		commandEvidenceEvent("item/completed", newCommand, 1_700_000_000_012),
	}, true)
	start := time.UnixMilli(1_700_000_000_000).UTC()
	want := CommandExecution{ItemID: "cmd-1", Command: "go test ./...", CWD: "/worktree", Status: "inProgress", Actions: []string{"go test ./..."}, StartedAt: &start}
	assertCommandEvidence(t, got.Commands, []CommandExecution{want})
}

func TestStartTurnCommandEvidenceCompletionControls(t *testing.T) {
	for _, snapshotOnly := range []bool{false, true} {
		name := "started_to_item_completion"
		if snapshotOnly {
			name = "started_to_turn_snapshot_and_new_item"
		}
		t.Run(name, func(t *testing.T) {
			started := commandEvidenceItem("cmd-1", "go test ./...", "inProgress", nil, nil, nil)
			started["commandActions"] = []any{}
			first := commandEvidenceItem("cmd-1", "go test ./...", "completed", 7, "test failed", 12)
			second := commandEvidenceItem("cmd-2", "go test ./...", "completed", 0, "retry passed", 12)
			events := []map[string]any{commandEvidenceEvent("item/started", started, 1_700_000_000_000)}
			var snapshot []any
			if snapshotOnly {
				snapshot = []any{first, second}
			} else {
				events = append(events,
					commandEvidenceEvent("item/completed", first, 1_700_000_000_012),
					commandEvidenceEvent("item/completed", second, 1_700_000_000_012))
			}
			before := time.Now().UTC()
			got := commandEvidenceTurn(t, events, snapshot, nil, false)
			after := time.Now().UTC()
			want := []CommandExecution{
				commandEvidenceValue("cmd-1", "go test ./...", "completed", 7, "test failed", 12, true),
				commandEvidenceValue("cmd-2", "go test ./...", "completed", 0, "retry passed", 12, false),
			}
			if snapshotOnly {
				if len(got.Commands) != len(want) {
					t.Fatalf("commands=%+v", got.Commands)
				}
				for i := range want {
					finish := got.Commands[i].FinishedAt
					if finish == nil || finish.Before(before) || finish.After(after) {
						t.Fatalf("snapshot-only command has no valid observation time: %+v", got.Commands[i])
					}
					want[i].FinishedAt = finish
				}
			}
			assertCommandEvidence(t, got.Commands, want)
		})
	}
}

func commandEvidenceItem(id, command, status string, exit, output, duration any) map[string]any {
	return map[string]any{"id": id, "type": "commandExecution", "command": command, "cwd": "/worktree", "status": status,
		"commandActions": []any{map[string]any{"type": "unknown", "command": command}}, "exitCode": exit, "aggregatedOutput": output, "durationMs": duration}
}

func commandEvidenceEvent(method string, item map[string]any, timestamp int64) map[string]any {
	params := map[string]any{"threadId": "thread-evidence", "turnId": "turn-evidence", "item": item}
	if method == "item/started" {
		params["startedAtMs"] = timestamp
	} else {
		params["completedAtMs"] = timestamp
	}
	return map[string]any{"method": method, "params": params}
}

func commandEvidenceValue(id, command, status string, exit int, output string, duration int64, started bool) CommandExecution {
	start, finish := time.UnixMilli(1_700_000_000_000).UTC(), time.UnixMilli(1_700_000_000_012).UTC()
	value := CommandExecution{ItemID: id, Command: command, CWD: "/worktree", Status: status, Actions: []string{command}, ExitCode: &exit,
		Output: output, DurationMS: &duration, FinishedAt: &finish}
	if started {
		value.StartedAt = &start
	}
	return value
}

// All observations traverse the real JSON-RPC reader and Client.StartTurn.
// Deferring the response tests supported notifications-before-response ordering
// and makes post-turn observations deterministic without touching client state.
func commandEvidenceTurn(t *testing.T, events []map[string]any, snapshot []any, postTurn []map[string]any, deferResponse bool) TurnResult {
	t.Helper()
	client, requests, responses := newClientFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		value TurnResult
		err   error
	}
	results := make(chan result, 1)
	go func() {
		value, err := client.StartTurn(ctx, TurnOptions{ThreadID: "thread-evidence", Prompt: "capture protocol evidence only"})
		results <- result{value, err}
	}()
	request := readClientRequest(t, requests)
	response := map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-evidence", "status": "inProgress", "items": []any{}}}}
	if !deferResponse {
		writeServerJSON(t, responses, response)
	}
	for _, event := range events {
		writeServerJSON(t, responses, event)
	}
	if snapshot == nil {
		snapshot = []any{}
	}
	writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{
		"threadId": "thread-evidence", "turn": map[string]any{"id": "turn-evidence", "status": "completed", "items": snapshot},
	}})
	for _, event := range postTurn {
		writeServerJSON(t, responses, event)
	}
	if deferResponse {
		writeServerJSON(t, responses, response)
	}
	select {
	case got := <-results:
		if got.err != nil {
			t.Fatalf("StartTurn: %v", got.err)
		}
		return got.value
	case <-ctx.Done():
		t.Fatal("StartTurn did not return command evidence")
		return TurnResult{}
	}
}

func assertCommandEvidence(t *testing.T, got, want []CommandExecution) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		actual, _ := json.Marshal(got)
		expected, _ := json.Marshal(want)
		t.Fatalf("command evidence changed:\n got %s\nwant %s", actual, expected)
	}
}
