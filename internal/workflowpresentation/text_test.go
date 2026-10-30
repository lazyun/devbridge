package workflowpresentation

import (
	"strings"
	"testing"
	"time"
)

func TestRenderTextIncludesEveryProjectionSurfaceWithoutInferringState(t *testing.T) {
	refChanged := true
	checkoutMatches := false
	p := Projection{
		FeatureArea: Identity{ID: "fa-1", Name: "line one\nline two"},
		DevelopmentContext: DevelopmentContext{
			Project:           Identity{ID: "project-1", Name: "Dev Bridge"},
			SelectionRequired: true,
			Repositories: []RepositoryContext{{
				Repository:         Identity{ID: "repo-1", Name: "repo"},
				DevelopmentCycleID: "cycle-1",
				DevelopmentRef:     "refs/heads/dev",
				Branch:             "dev",
				Revision:           "abc123",
				SelectedRevision:   "def456",
				Status:             "CHECKOUT_MISMATCH",
				SelectionRequired:  true,
				RefChanged:         &refChanged,
				CheckoutMatches:    &checkoutMatches,
			}},
		},
		LifecycleStatus: "ACTIVE",
		WorkflowStatus:  "UNKNOWN",
		CurrentPhase:    "IMPLEMENTATION",
		Progress: []PhaseProgress{
			{Phase: "REVIEW", Status: "COMPLETE", SourceID: "review-1", Ordinal: 1},
			{Phase: "IMPLEMENTATION", Status: "FAILED", SourceID: "implementation-2", Ordinal: 2},
		},
		CurrentOperation: &Operation{Kind: "EXECUTION", ID: "execution-1", RunID: "run-1", Status: "RUNNING"},
		PendingDecision:  &Decision{Kind: "EXECUTION_AUTHORIZATION", ID: "decision-1", Summary: "Authorize?", Guarantee: "AUTHORITATIVE_ENTITY_STATE", CancelEffect: "DECLINE_ACTION", AllowedActions: []string{"AUTHORIZE", "REJECT"}},
		Cancellation:     &Cancellation{State: "CANCEL_REQUESTED", RunID: "exact-run", RecoveryRequired: false},
		Recovery:         &Recovery{Status: "RECOVERY_REQUIRED", Required: true, Summary: "Need provenance"},
		Blockers:         []string{"host unavailable"},
		Capabilities: []Capability{
			{Action: "AUTHORIZE", Available: false, Reason: "receipt conflict", Target: &ActionTarget{SubjectID: "exact-proposal"}},
			{Action: "VIEW_DETAILS", Available: true},
		},
		Details:   Details{View: "feature-area", ProjectID: "project-1", FeatureAreaID: "fa-1", ReviewID: "review-1", TaskID: "task-1", TaskDefinitionRevisionID: "task-revision-1", TaskPlanID: "plan-1", ExecutionID: "execution-1", RunID: "run-1", Baseline: "abc123"},
		UpdatedAt: time.Date(2026, 9, 8, 12, 30, 1, 123, time.FixedZone("CST", 8*60*60)),
	}

	got := RenderText(p)
	for _, want := range []string{
		"Name: \"line one\\nline two\"",
		"Workflow status: \"UNKNOWN\"",
		"Current phase: \"IMPLEMENTATION\"",
		"Status: \"FAILED\"",
		"Ordinal: 2",
		"Current operation:",
		"Pending decision:",
		"Guarantee: \"AUTHORITATIVE_ENTITY_STATE\"",
		"Cancel effect: \"DECLINE_ACTION\"",
		"State: \"CANCEL_REQUESTED\"",
		"Run ID: \"exact-run\"",
		"exact-proposal",
		"Declared allowed actions:",
		"Recovery:",
		"Required: true",
		"Blockers:",
		"Action: \"AUTHORIZE\"",
		"Available: false",
		"Reason: \"receipt conflict\"",
		"Available actions:\n  - \"VIEW_DETAILS\"\n",
		"Task definition revision ID: \"task-revision-1\"",
		"Updated at: \"2026-09-08T12:30:01.000000123+08:00\"",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderText() missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Available actions:\n  - \"AUTHORIZE\"") {
		t.Fatalf("RenderText() presented unavailable action as available:\n%s", got)
	}
}

func TestRenderTextHandlesAbsentOptionalSurfaces(t *testing.T) {
	got := RenderText(Projection{DevelopmentContext: DevelopmentContext{
		Repositories: []RepositoryContext{{}},
	}})
	for _, want := range []string{
		"Ref changed: (unknown)",
		"Checkout matches: (unknown)",
		"Progress:\n  (none)",
		"Current operation:\n  (none)",
		"Pending decision:\n  (none)",
		"Recovery:\n  (none)",
		"Blockers:\n  (none)",
		"Capabilities:\n  (none)",
		"Available actions:\n  (none)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderText() missing %q:\n%s", want, got)
		}
	}
}

func TestAvailableActionsOnlyReturnsExplicitlyAvailableCapabilities(t *testing.T) {
	p := Projection{Capabilities: []Capability{
		{Action: "AUTHORIZE", Available: false},
		{Action: "VIEW_DETAILS", Available: true},
		{Action: "RECOVER", Available: true},
		{Action: "CANCEL", Available: false},
	}}

	got := AvailableActions(p)
	want := []string{"VIEW_DETAILS", "RECOVER"}
	if len(got) != len(want) {
		t.Fatalf("AvailableActions() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AvailableActions() = %#v, want %#v", got, want)
		}
	}
}
