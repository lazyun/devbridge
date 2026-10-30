package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/mcpserver"
)

// This is deliberately a prompt/routing contract test, not a claim that a host
// mounted a Card or that a model selected a tool. Host acceptance remains manual.
func TestPresentationFlowInstructionsLeadWithRoutingAndAuthorityBoundaries(t *testing.T) {
	instructions := mcpserver.ExternalServerInstructions
	if !strings.HasPrefix(instructions, "Session-first initialization is evaluated before every first-use display rule.") {
		t.Fatal("session-first admission must lead the server instructions")
	}
	for _, want := range []string{
		"Presentation: project_selection_show starts EXPANDED",
		"A bound workspace_show defaults COLLAPSED; an explicit show/open uses EXPANDED.",
		"Analysis reads and mutations are SILENT; present the final accurate state once.",
		"Display, refresh, remount\nnever initialize or bind a Session.",
		"Exact\nArea/Task/Decision/Operation targets remain explicit",
		"Never default to the first, newest, unique,\ncurrently displayed or last browsed Task.",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("presentation instruction omits %q", want)
		}
	}
	if strings.Index(instructions, "Session-first initialization") > strings.Index(instructions, "5. Presentation:") {
		t.Fatal("presentation guidance must remain after session admission")
	}
}

func TestReviewCompletionDescriptionPreservesSilentReadAndOneExactPresentation(t *testing.T) {
	description := mcpserver.ExternalReviewGetDescription
	for _, want := range []string{
		"Read the exact Feature Area Review findings", "verify Session/Project ownership",
		"A historical result stays historical; never substitute the newest Review.",
		"A read-only workspace_show is optional and is not approval.",
		"otherwise ask for the outstanding decision.",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("review completion description omits %q", want)
		}
	}
}

// This locks upper-layer routing only. It neither claims that a host will choose
// these calls nor changes the verbatim Coding Agent review instruction contract.
func TestFeatureAreaCreateCompositionInstructionsKeepAuthorizationBoundaries(t *testing.T) {
	instructions := mcpserver.ExternalServerInstructions
	for _, want := range []string{
		"feature_area_create defaults to CREATE_ONLY",
		"nextAction=CREATE_TASK is guidance, not automatic\nauthorization",
		"explicit CREATE_AND_REVIEW, stable requestId, complete\ninitialTask and complete initialReview",
		"Do not stop after the Feature Area merely to ask again",
		"do not create another Feature Area just to use the composition",
		"use task_review_start and do not\ncreate another Task",
		"actual Review Approval\nand Task Acceptance still require the exact user decision",
		"nextAction.required is business guidance,\nnot a host auto-execution mechanism",
		"do not change, add to, or rewrite the Coding Agent engineering prompt",
	} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("composition instruction omits %q", want)
		}
	}

}
