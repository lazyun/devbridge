package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

func TestRecoveryVerificationDoesNotSkipUncertainLaterCommand(t *testing.T) {
	for _, kind := range []string{"complete", "later-failure", "truncated", "malformed", "missing-command", "missing-cwd", "partial-readback"} {
		t.Run(kind, func(t *testing.T) {
			spec, report, scope, commands := qualifiedVerificationFixture(t)
			w := scope.workset
			c := &Coordinator{Tasks: &fakeTasks{value: task.Task{ID: w.TaskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, CurrentDefinitionRevisionID: w.TaskDefinitionRevisionID, VerificationSpec: spec}}}
			source := run.Run{ID: w.RunID, TaskID: w.TaskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, Purpose: run.PurposeVerification, OperationID: w.ExecutionID, TurnID: "turn", RuntimeWorkset: &w, AgentRuntime: &agentruntime.Snapshot{}}
			for _, command := range commands {
				raw, _ := json.Marshal(map[string]any{"command": command.Command, "cwd": command.CWD, "status": command.Status, "exitCode": command.ExitCode})
				source.AgentRuntime.Items = append(source.AgentRuntime.Items, agentruntime.Item{ID: command.ItemID, Type: "commandExecution", Metadata: raw})
			}
			if kind != "complete" {
				wire := map[string]any{"command": commands[0].Command, "cwd": commands[0].CWD, "status": "completed", "exitCode": 1}
				if kind == "missing-command" {
					delete(wire, "command")
				}
				if kind == "missing-cwd" {
					delete(wire, "cwd")
				}
				raw, _ := json.Marshal(wire)
				if kind == "malformed" {
					raw = json.RawMessage(`{"exitCode":`)
				}
				source.AgentRuntime.Items = append(source.AgentRuntime.Items, agentruntime.Item{ID: "later", Type: "commandExecution", Metadata: raw, Truncated: kind == "truncated"})
			}
			if kind == "partial-readback" {
				first := source.AgentRuntime.Items[0]
				var wire map[string]any
				if err := json.Unmarshal(first.Metadata, &wire); err != nil {
					t.Fatal(err)
				}
				wire["id"], wire["type"] = first.ID, first.Type
				raw, _ := json.Marshal(wire)
				var err error
				source.AgentRuntime.Items, err = mergeRecoverySourceItems(source.AgentRuntime.Items, []json.RawMessage{raw})
				if err != nil {
					t.Fatal(err)
				}
			}
			facts := recoverystatus.FactualObservation{Source: recoverystatus.Source{RunID: source.ID, OperationID: source.OperationID, TurnID: source.TurnID, RuntimeWorksetHash: w.ContentHash, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID}}
			if err := c.ValidateRecoveryOwnerFacts(context.Background(), source, recoverystatus.Report{VerificationReport: json.RawMessage(report)}, &facts); err != nil {
				t.Fatal(err)
			}
			if facts.OwnerContractValidated != (kind == "complete") {
				t.Fatalf("uncertain command history acquired recovery authority: validated=%v", facts.OwnerContractValidated)
			}
		})
	}
}

func TestRecoverySourceReadbackPreservesCommandHistory(t *testing.T) {
	item := func(id string) agentruntime.Item {
		return agentruntime.Item{ID: id, Type: "commandExecution", Status: "completed"}
	}
	wire := func(id string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"id": id, "type": "commandExecution", "status": "completed"})
		return raw
	}
	for _, tt := range []struct {
		name     string
		captured []agentruntime.Item
		backend  []json.RawMessage
		want     string
		reject   bool
	}{
		{"partial", []agentruntime.Item{item("early"), item("late")}, []json.RawMessage{wire("early")}, "early,late", false},
		{"full", []agentruntime.Item{item("late")}, []json.RawMessage{wire("early"), wire("late")}, "early,late", false},
		{"refreshed", []agentruntime.Item{{ID: "early", Type: "commandExecution", Truncated: true}}, []json.RawMessage{wire("early")}, "early", false},
		{"new", nil, []json.RawMessage{wire("early")}, "early", false},
		{"retained", []agentruntime.Item{item("early")}, nil, "early", false},
		{"incomparable", []agentruntime.Item{item("early"), item("local")}, []json.RawMessage{wire("early"), wire("remote")}, "", true},
		{"reordered", []agentruntime.Item{item("early"), item("late")}, []json.RawMessage{wire("late"), wire("early")}, "", true},
		{"duplicate", nil, []json.RawMessage{wire("early"), wire("early")}, "", true},
		{"missing-id", nil, []json.RawMessage{wire("")}, "", true},
		{"malformed", nil, []json.RawMessage{json.RawMessage(`{"type":`)}, "", true},
		{"missing-type", nil, []json.RawMessage{json.RawMessage(`{}`)}, "", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items, err := mergeRecoverySourceItems(tt.captured, tt.backend)
			if (err != nil) != tt.reject {
				t.Fatalf("merge error=%v, reject=%v", err, tt.reject)
			}
			if tt.reject {
				return
			}
			var ids []string
			for _, it := range items {
				ids = append(ids, it.ID)
				if tt.name == "refreshed" && it.Truncated {
					t.Fatal("exact full refresh did not replace truncated item")
				}
			}
			if strings.Join(ids, ",") != tt.want {
				t.Fatalf("command order=%v, want %s", ids, tt.want)
			}
		})
	}
	for _, field := range []string{"aggregatedOutput", "extra"} {
		t.Run("oversized-"+field, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"id": "later", "type": "commandExecution", field: strings.Repeat("x", agentruntime.MaxPayload+agentruntime.MaxOutput)})
			if _, err := mergeRecoverySourceItems([]agentruntime.Item{item("early")}, []json.RawMessage{raw}); err == nil {
				t.Fatal("oversized later command was silently discarded")
			}
		})
	}
}
