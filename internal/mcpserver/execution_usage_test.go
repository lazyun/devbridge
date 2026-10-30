package mcpserver

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/run"
)

func TestExternalExecutionUsagePreservesAvailability(t *testing.T) {
	exact := func(n int64) run.Run {
		return run.Run{Status: run.StatusSucceeded, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: &run.TokenCounts{InputTokens: n, TotalTokens: n}}}
	}
	unknown := run.Run{Status: run.StatusSucceeded, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}}
	pending := run.Run{Status: run.StatusRunning, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}}
	for _, tc := range []struct {
		name   string
		runs   []run.Run
		status string
		total  int64
	}{
		{"no runs", nil, "UNAVAILABLE", 0},
		{"missing", []run.Run{unknown}, "UNAVAILABLE", 0},
		{"legacy", []run.Run{{Status: run.StatusSucceeded}}, "UNAVAILABLE", 0},
		{"true zero", []run.Run{exact(0)}, "AVAILABLE", 0},
		{"complete", []run.Run{exact(20), exact(30)}, "AVAILABLE", 50},
		{"partial unavailable", []run.Run{exact(20), unknown}, "UNAVAILABLE", 0},
		{"partial pending", []run.Run{exact(20), pending}, "PENDING", 0},
		{"unavailable dominates pending", []run.Run{pending, unknown}, "UNAVAILABLE", 0},
		{"missing available counts", []run.Run{{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable}}}, "UNAVAILABLE", 0},
		{"invalid negative", []run.Run{exact(-1)}, "UNAVAILABLE", 0},
		{"overflow", []run.Run{exact(math.MaxInt64), exact(1)}, "UNAVAILABLE", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range tc.runs {
				tc.runs[i].AgentExecutionID = "execution"
			}
			// An unrelated Run cannot contribute tokens or poison availability.
			runs := append(append([]run.Run{}, tc.runs...), run.Run{ExecutionProposalID: "other", TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}})
			out := externalExecutionFor(agentexecution.Projection{ExecutionID: "execution"}, runs)
			if out.TokenUsage.Status != tc.status {
				t.Fatalf("usage=%+v", out.TokenUsage)
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status == "AVAILABLE" {
				if out.TokenUsage.Usage == nil || out.TokenUsage.Usage.TotalTokens != tc.total {
					t.Fatal(out.TokenUsage)
				}
			} else if out.TokenUsage.Usage != nil || strings.Contains(string(encoded), "totalTokens") {
				t.Fatalf("unknown usage became tokens: %s", encoded)
			}
		})
	}
}

func TestExternalExecutionSchemaUsesRunUsageEnvelope(t *testing.T) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(externalExecutionOutputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(schema.Properties["tokenUsage"], &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(tokenUsageOutputSchema), &want); err != nil {
		t.Fatal(err)
	}
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("Execution usage schema differs: %s", g)
	}
}
