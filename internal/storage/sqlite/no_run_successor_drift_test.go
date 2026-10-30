package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
)

func TestNoRunAuthorityDriftPriority(t *testing.T) {
	source := runtimepreparation.Plan{
		BackendIdentity: "backend", BackendConfigFingerprint: "fingerprint", BackendSource: "source", BackendKind: "kind",
		Model: "model", ReasoningEffort: "high", ServiceTier: "tier", RuntimeAdapterIdentity: "adapter", RuntimeProtocolIdentity: "protocol",
	}
	if code := noRunAuthorityDriftCode(source, source); code != "" {
		t.Fatal(code)
	}
	for _, tc := range []struct {
		name, code string
		change     func(*runtimepreparation.Plan)
	}{
		{"backend", "BACKEND_CONFIGURATION_CHANGED", func(p *runtimepreparation.Plan) { p.BackendIdentity = "changed" }},
		{"runtime", "RUNTIME_CONFIGURATION_CHANGED", func(p *runtimepreparation.Plan) { p.CodexFastMode = true }},
		{"adapter", "RUNTIME_ADAPTER_IDENTITY_CHANGED", func(p *runtimepreparation.Plan) { p.RuntimeAdapterIdentity = "changed" }},
		{"protocol", "RUNTIME_PROTOCOL_IDENTITY_CHANGED", func(p *runtimepreparation.Plan) { p.RuntimeProtocolIdentity = "changed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := source
			tc.change(&current)
			if code := noRunAuthorityDriftCode(source, current); code != tc.code {
				t.Fatalf("got %s want %s", code, tc.code)
			}
		})
	}
	current := source
	current.BackendIdentity, current.Model, current.RuntimeAdapterIdentity, current.RuntimeProtocolIdentity = "changed", "changed", "changed", "changed"
	if code := noRunAuthorityDriftCode(source, current); code != "BACKEND_CONFIGURATION_CHANGED" {
		t.Fatalf("priority: %s", code)
	}
}

func TestNoRunSuccessorInvalidatedQualificationLeavesRequestUnconsumed(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-successor-qualification")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-qualification")
	platform := NewRuntimePlatformStore(f.db)
	lineage, err := platform.GetLineage(context.Background(), "qualified-platform")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Invalidate(context.Background(), runtimeplatform.InvalidationInput{QualificationID: "qualified-qualification", Generation: 1, RequestID: "invalidate-successor-platform", Reason: "Fixture qualification revoked", ExpectedLineageVersion: lineage.Version}); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{}
	for _, table := range []string{"no_run_successor_command_receipts", "no_run_successor_effect_authorities", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities", "runs"} {
		before[table] = noRunCount(t, f, table)
	}
	_, err = f.store.PrepareSuccessorExecution(context.Background(), in)
	if !errors.Is(err, runtimeplatform.ErrQualificationInvalidated) {
		t.Fatalf("qualification rejection: %#v", err)
	}
	for table, want := range before {
		if got := noRunCount(t, f, table); got != want {
			t.Fatalf("%s changed: %d -> %d", table, want, got)
		}
	}
	var phase task.Phase
	var current string
	if err := f.db.sql.QueryRow(`SELECT phase,current_execution_id FROM tasks WHERE id=?`, owner.ID).Scan(&phase, &current); err != nil || phase != task.PhaseNoRunSuccessorRequired || current != predecessor.ID {
		t.Fatalf("Task changed: %s %s %v", phase, current, err)
	}
	if result, err := f.store.PrepareSuccessorExecution(context.Background(), in); !errors.Is(err, runtimeplatform.ErrQualificationInvalidated) || result.SuccessorExecutionID != "" {
		t.Fatalf("same RequestID borrowed invalid authority: %+v %v", result, err)
	}
}
