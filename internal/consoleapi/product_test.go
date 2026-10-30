package consoleapi

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestProductTopicSummaryJSONUsesCoreFeatureAreaProductProjection(t *testing.T) {
	value := ProductTopicSummary{Product: presentation.NewFeatureAreaProductProjection("area-1", "Chat", "DISCOVERING", false)}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	product, ok := wire["product"].(map[string]any)
	if !ok || product["domainStatus"] != presentation.DomainStatusActive || product["internalStatus"] != "DISCOVERING" {
		t.Fatalf("HTTP/Console FeatureArea product projection = %#v", wire["product"])
	}
}

func TestProductTaskDevelopmentBranchesUseOnlySourceReviewEvidence(t *testing.T) {
	reviewBranches := map[string]string{
		"review-a":  "refs/heads/dev/a",
		"review-b":  "refs/heads/dev/b",
		"unrelated": "refs/heads/current-checkout-must-not-leak",
	}
	got := productTaskDevelopmentBranches([]string{"review-b", "missing", "review-a", "review-b", ""}, reviewBranches)
	want := []string{"refs/heads/dev/a", "refs/heads/dev/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("development branches = %v, want %v", got, want)
	}
	if unknown := productTaskDevelopmentBranches([]string{"missing", ""}, reviewBranches); len(unknown) != 0 {
		t.Fatalf("historical Task without source Review evidence must remain unknown: %v", unknown)
	}
}

func TestProductExecutionStateDistinguishesExecutionAndVerification(t *testing.T) {
	if got := productExecutionState(run.Run{Purpose: run.PurposeImplementation, Status: run.StatusPreparing}); got != "EXECUTING" {
		t.Fatalf("implementation preparing = %q", got)
	}
	if got := productExecutionState(run.Run{Purpose: run.PurposeVerification, Status: run.StatusPreparing}); got != "VERIFYING" {
		t.Fatalf("verification preparing = %q", got)
	}
}

func TestOperationUsageProjectsOnlyAuthoritativeRunDelta(t *testing.T) {
	started := time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC)
	finished := started.Add(37 * time.Second)
	available := run.TokenUsageAvailable
	tests := []struct {
		name string
		run  run.Run
		want *OperationUsage
	}{
		{
			name: "available delta with cached input",
			run:  run.Run{StartedAt: &started, FinishedAt: &finished, TokenUsage: run.TokenUsageAccounting{Status: available, Usage: &run.TokenCounts{InputTokens: 39842, CachedInputTokens: 35102, OutputTokens: 2817, TotalTokens: 42659}}},
			want: &OperationUsage{Status: available, Usage: &OperationTokenCounts{InputTokens: int64Pointer(39842), CachedInputTokens: int64Pointer(35102), OutputTokens: int64Pointer(2817), TotalTokens: int64Pointer(42659)}, DurationMilliseconds: int64Pointer(37000)},
		},
		{
			name: "unknown cached input stays nil",
			run:  run.Run{StartedAt: &started, FinishedAt: &finished, TokenUsage: run.TokenUsageAccounting{Status: available, Usage: &run.TokenCounts{InputTokens: 39842, CachedInputTokens: 0, OutputTokens: 2817, TotalTokens: 42659}}},
			want: &OperationUsage{Status: available, Usage: &OperationTokenCounts{InputTokens: int64Pointer(39842), OutputTokens: int64Pointer(2817), TotalTokens: int64Pointer(42659)}, DurationMilliseconds: int64Pointer(37000)},
		},
		{
			name: "available without an authoritative delta is unavailable",
			run:  run.Run{StartedAt: &started, FinishedAt: &finished, TokenUsage: run.TokenUsageAccounting{Status: available}},
			want: &OperationUsage{Status: run.TokenUsageUnavailable, DurationMilliseconds: int64Pointer(37000)},
		},
		{
			name: "unavailable run retains unavailable status",
			run:  run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}},
			want: &OperationUsage{Status: run.TokenUsageUnavailable},
		},
		{
			name: "pending run retains pending status",
			run:  run.Run{StartedAt: &started, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}},
			want: &OperationUsage{Status: run.TokenUsagePending},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := operationUsage(test.run); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("operationUsage() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestOperationUsageJSONStatusAndOptionalCountsParity(t *testing.T) {
	tests := []struct {
		name string
		run  run.Run
		want string
	}{
		{name: "available exact zero remains explicit", run: run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: &run.TokenCounts{}}}, want: `{"status":"AVAILABLE","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`},
		{name: "available positive reasoning remains an optional authoritative breakdown", run: run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, Usage: &run.TokenCounts{InputTokens: 8, OutputTokens: 3, ReasoningOutputTokens: 2, TotalTokens: 11}}}, want: `{"status":"AVAILABLE","usage":{"input_tokens":8,"reasoning_output_tokens":2,"output_tokens":3,"total_tokens":11}}`},
		{name: "pending suppresses bogus counts", run: run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending, Usage: &run.TokenCounts{InputTokens: 8, OutputTokens: 3, TotalTokens: 11}}}, want: `{"status":"PENDING"}`},
		{name: "unavailable suppresses bogus counts", run: run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable, Usage: &run.TokenCounts{InputTokens: 8, OutputTokens: 3, TotalTokens: 11}}}, want: `{"status":"UNAVAILABLE"}`},
		{name: "available nil counts is malformed and closes unavailable", run: run.Run{TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable}}, want: `{"status":"UNAVAILABLE"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(operationUsage(test.run))
			if err != nil {
				t.Fatal(err)
			}
			if got := string(encoded); got != test.want {
				t.Fatalf("JSON = %s, want %s", got, test.want)
			}
		})
	}
}

func TestProductSummaryExposesFrozenExecutionSelection(t *testing.T) {
	taskSummary := TaskSummary{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	if taskSummary.Model != "gpt-5.6-terra" || taskSummary.ReasoningEffort != "high" {
		t.Fatalf("TaskSummary selection = %+v", taskSummary)
	}
	reviewSummary := ProductReviewSummary{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh"}
	if reviewSummary.Model != "gpt-5.6-sol" || reviewSummary.ReasoningEffort != "xhigh" {
		t.Fatalf("Review summary selection = %+v", reviewSummary)
	}
}

func TestReviewUsageExactRunAndNoWorkflowAuthority(t *testing.T) {
	window := int64(258400)
	exact := run.Run{ID: "exact", Status: run.StatusRunning, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageAvailable, ModelContextWindow: &window, Usage: &run.TokenCounts{InputTokens: 584383, CachedInputTokens: 474624, OutputTokens: 3111, ReasoningOutputTokens: 1166, TotalTokens: 587494}}}
	newer := run.Run{ID: "newer", Status: run.StatusSucceeded, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}}
	value := review.Review{RunID: "exact"}
	state, usage := productReviewPresentation(value, []run.Run{newer, exact})
	if state != "REVIEWING" || usage.Usage == nil || *usage.Usage.TotalTokens != 587494 || *usage.ModelContextWindow != 258400 {
		t.Fatalf("%s %+v", state, usage)
	}
	for _, status := range []run.TokenUsageStatus{run.TokenUsagePending, run.TokenUsageUnavailable} {
		exact.Status = run.StatusSucceeded
		exact.TokenUsage.Status = status
		state, usage = productReviewPresentation(value, []run.Run{newer, exact})
		if state != "COMPLETE" || usage.Status != status || usage.Usage != nil {
			t.Fatalf("telemetry became workflow authority: %s %+v", state, usage)
		}
	}
	_, usage = productReviewPresentation(value, []run.Run{newer})
	if usage.Status != run.TokenUsageUnavailable || usage.Usage != nil {
		t.Fatal(usage)
	}
}
