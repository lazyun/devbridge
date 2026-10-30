package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/observability"
)

func runContext(ctx context.Context, value Run) context.Context {
	return observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ReviewID, TaskID: value.TaskID, ExecutionID: value.AgentExecutionID, RunID: value.ID})
}

func logCreated(ctx context.Context, value Run, err error) {
	if err != nil || value.ID == "" {
		return
	}
	ctx = runContext(ctx, value)
	data := map[string]any{"purpose": value.Purpose, "attempt": value.Attempt, "newState": value.Status}
	if value.CodexInstruction != "" {
		data["instruction"] = observability.Fingerprint(value.CodexInstruction)
	}
	if value.Purpose == PurposeScopeReview {
		data["reviewScope"] = scopeSummary(value.ReviewScope)
		data["mode"] = value.ReviewPlan.Mode
		data["selectedModuleCount"] = len(value.ReviewPlan.SelectedModules)
	}
	observability.Info(ctx, "run", "run.created", "durable Run created", data)
}

func logTransition(ctx context.Context, value Run, previous Status, err error) {
	if err != nil || value.ID == "" {
		return
	}
	// WAITING_APPROVAL is logged after the Approval transaction commits by the
	// approval service. Avoid inserting post-transition work between an
	// observer-visible status update and approval correlation registration.
	if value.Status == StatusWaitingApproval {
		return
	}
	event := map[Status]string{
		StatusCreated: "run.created", StatusPreparing: "run.preparing", StatusRunning: "run.running",
		StatusWaitingApproval: "run.waiting_approval", StatusCollecting: "run.collecting",
		StatusSucceeded: "run.succeeded", StatusFailed: "run.failed",
		StatusInfrastructureFailed: "run.infrastructure_failed", StatusCanceled: "run.canceled",
	}[value.Status]
	if event == "" {
		return
	}
	ctx = runContext(ctx, value)
	data := map[string]any{"purpose": value.Purpose, "previousState": previous, "newState": value.Status, "attempt": value.Attempt}
	if value.FinishedAt != nil {
		data["durationMillis"] = terminalDuration(value)
	}
	if value.Failure != nil {
		data["errorCode"] = value.Failure.Code
		data["failureKind"] = value.Failure.Kind
		data["stage"] = value.Failure.Stage
		data["errorSummary"] = observability.Bound(value.Failure.Message, 512)
	}
	if value.Result != nil {
		data["agentClaimPresent"] = value.Result.AgentClaim.Message != ""
		data["agentClaim"] = observability.Fingerprint(value.Result.AgentClaim.Message)
	}
	observability.Info(ctx, "run", event, "Run state changed", data)
}

type canceledApprovalLister interface {
	ListCanceledApprovalIDs(context.Context, string) ([]string, error)
}

func (s *Service) logCanceledApprovals(ctx context.Context, value Run, transitionErr error) {
	if transitionErr != nil || !terminal(value.Status) {
		return
	}
	store, ok := s.store.(canceledApprovalLister)
	if !ok {
		return
	}
	ids, err := store.ListCanceledApprovalIDs(context.WithoutCancel(ctx), value.ID)
	if err != nil {
		return
	}
	for _, id := range ids {
		logCtx := observability.WithContext(runContext(ctx, value), observability.Correlation{ApprovalID: id})
		data := map[string]any{"newState": "CANCELED", "reasonCode": "run_terminal", "runTerminalState": value.Status}
		observability.Info(logCtx, "approval", "approval.superseded", "Approval superseded by durable Run terminal state", data)
		observability.Info(logCtx, "approval", "approval.canceled", "Approval durably canceled by Run terminal state", data)
	}
}

func logUsage(ctx context.Context, value Run, observation *TokenUsageObservation) {
	if observation == nil || value.ID == "" {
		return
	}
	ctx = runContext(ctx, value)
	semantic := "CUMULATIVE"
	if observation.Attribution != nil {
		semantic = "BRIDGE_LOGICAL_CUMULATIVE"
	}
	for _, snapshot := range []struct {
		phase string
		value TokenCounts
	}{{"START", observation.StartCumulative}, {"END", observation.EndCumulative}} {
		counts, presence := observedCountsData(snapshot.value)
		observability.Info(ctx, "codex", "codex.usage.snapshot", "Usage accounting snapshot recorded", map[string]any{
			"threadId": value.ThreadID, "snapshotPhase": snapshot.phase, "telemetrySemantic": semantic,
			"inputTokens": counts["inputTokens"], "cachedInputTokens": counts["cachedInputTokens"],
			"cacheWriteInputTokens": counts["cacheWriteInputTokens"], "outputTokens": counts["outputTokens"],
			"reasoningOutputTokens": counts["reasoningOutputTokens"], "totalTokens": counts["totalTokens"],
			"fieldPresence": presence, "presenceSemantics": "NON_ZERO_CONFIRMED_ZERO_UNAVAILABLE",
		})
	}
	startCounts, startPresence := observedCountsData(observation.StartCumulative)
	endCounts, endPresence := observedCountsData(observation.EndCumulative)
	data := map[string]any{"derivationStatus": value.TokenUsage.Status, "reliable": observation.Reliable,
		"startCumulative": startCounts, "startFieldPresence": startPresence,
		"endCumulative": endCounts, "endFieldPresence": endPresence}
	if value.TokenUsage.Usage != nil {
		putDerivedDelta(data, "derivedInputDelta", value.TokenUsage.Usage.InputTokens, startPresence["inputTokens"], endPresence["inputTokens"])
		putDerivedDelta(data, "derivedCachedInputDelta", value.TokenUsage.Usage.CachedInputTokens, startPresence["cachedInputTokens"], endPresence["cachedInputTokens"])
		putDerivedDelta(data, "derivedOutputDelta", value.TokenUsage.Usage.OutputTokens, startPresence["outputTokens"], endPresence["outputTokens"])
		putDerivedDelta(data, "derivedTotalDelta", value.TokenUsage.Usage.TotalTokens, startPresence["totalTokens"], endPresence["totalTokens"])
		data["derivationReason"] = "monotonic_cumulative_snapshots"
	} else if !observation.Reliable {
		data["derivationReason"] = "upstream_telemetry_unreliable"
	} else {
		data["derivationReason"] = "missing_or_non_monotonic_snapshot"
	}
	if observation.Attribution != nil {
		data["accountingEpochStart"] = observation.Attribution.StartEpoch
		data["accountingEpochEnd"] = observation.Attribution.EndEpoch
		if value.TokenUsage.Usage != nil {
			data["derivationReason"] = "bridge_logical_cumulative_delta"
		} else {
			data["derivationReason"] = observation.Attribution.Reason
		}
	}
	observability.Info(ctx, "run", "run.usage.delta", "Run usage delta derivation recorded", data)
}

func observedCountsData(value TokenCounts) (map[string]any, map[string]bool) {
	counts := map[string]any{}
	presence := map[string]bool{}
	for _, field := range []struct {
		name  string
		value int64
	}{
		{"inputTokens", value.InputTokens}, {"cachedInputTokens", value.CachedInputTokens},
		{"cacheWriteInputTokens", value.CacheWriteInputTokens}, {"outputTokens", value.OutputTokens},
		{"reasoningOutputTokens", value.ReasoningOutputTokens}, {"totalTokens", value.TotalTokens},
	} {
		// The current upstream wire type uses integers rather than pointers and
		// therefore cannot distinguish an omitted field from an explicit zero.
		// Preserve that uncertainty instead of asserting a missing value was 0.
		presence[field.name] = field.value != 0
		if field.value == 0 {
			counts[field.name] = nil
		} else {
			counts[field.name] = field.value
		}
	}
	return counts, presence
}

func putDerivedDelta(data map[string]any, name string, value int64, startPresent, endPresent bool) {
	if startPresent && endPresent {
		data[name] = value
		data[name+"Present"] = true
		return
	}
	data[name] = nil
	data[name+"Present"] = false
}

func terminalDuration(value Run) int64 {
	start := value.CreatedAt
	if value.StartedAt != nil {
		start = *value.StartedAt
	}
	if value.FinishedAt == nil || value.FinishedAt.Before(start) {
		return 0
	}
	return value.FinishedAt.Sub(start).Milliseconds()
}

func scopeSummary(scope ReviewScope) map[string]any {
	paths := append([]string(nil), scope.Paths...)
	sort.Strings(paths)
	sum := sha256.Sum256([]byte(string(scope.Kind) + "\n" + strings.Join(paths, "\n")))
	return map[string]any{"kind": scope.Kind, "pathCount": len(paths), "sha256": hex.EncodeToString(sum[:])}
}
