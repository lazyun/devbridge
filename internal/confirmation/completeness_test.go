package confirmation

import (
	"strings"
	"testing"
	"time"
)

func completeReviewIntent() *Intent {
	return &Intent{
		IntentID:    "confirm_ok",
		Version:     1,
		AISessionID: "session",
		ProjectID:   "project",
		Purpose:     Review,
		Action:      "START_REVIEW",
		State:       "PENDING",
		ContentHash: strings.Repeat("f", 64),
		CreatedAt:   time.Now().UTC(),
		Steps:       []Step{},
		Content: PrepareInput{
			AISessionID: "session",
			Purpose:     Review,
			Action:      "START_REVIEW",
			Review:      &ReviewBody{TaskID: "task", FeatureAreaID: "area"},
			Variants:    []Variant{{ID: "v", Label: "Label", Summary: "Summary"}},
		},
	}
}

func TestIntentFactsCompleteAcceptsProjectScopeReview(t *testing.T) {
	// Project-scope / multi-repository Review legitimately has no single
	// repositoryId; completeness must never demand one.
	if ok, code, message := IntentFactsComplete(completeReviewIntent()); !ok {
		t.Fatalf("project-scope review rejected: %s %s", code, message)
	}
}

func TestIntentFactsCompleteRejectsMissingFacts(t *testing.T) {
	cases := []struct {
		name string
		want string
		mut  func(*Intent)
	}{
		{"nil intent", "CONFIRMATION_INTENT_MISSING", func(i *Intent) { i = nil }},
		{"no intent id", "CONFIRMATION_IDENTITY_INCOMPLETE", func(i *Intent) { i.IntentID = "" }},
		{"zero version", "CONFIRMATION_VERSION_INVALID", func(i *Intent) { i.Version = 0 }},
		{"bad purpose", "CONFIRMATION_PURPOSE_INVALID", func(i *Intent) { i.Purpose = Purpose("NOPE"); i.Content.Purpose = Purpose("NOPE") }},
		{"no project", "CONFIRMATION_PROJECT_IDENTITY_MISSING", func(i *Intent) { i.ProjectID = "" }},
		{"bad hash", "CONFIRMATION_CONTENT_HASH_MISSING", func(i *Intent) { i.ContentHash = "xyz" }},
		{"content mismatch", "CONFIRMATION_CONTENT_IDENTITY_MISMATCH", func(i *Intent) { i.Content.Action = "APPROVE" }},
		{"missing review task", "CONFIRMATION_PURPOSE_CONTENT_MISSING", func(i *Intent) { i.Content.Review.TaskID = "" }},
		{"no variants", "CONFIRMATION_VARIANT_FACTS_INCOMPLETE", func(i *Intent) { i.Content.Variants = nil }},
		{"duplicate variant", "CONFIRMATION_VARIANT_FACTS_INCOMPLETE", func(i *Intent) {
			i.Content.Variants = []Variant{{ID: "v", Label: "a", Summary: "b"}, {ID: "v", Label: "c", Summary: "d"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			intent := completeReviewIntent()
			if tc.name == "nil intent" {
				if ok, code, _ := IntentFactsComplete(nil); ok || code != tc.want {
					t.Fatalf("nil intent code=%s", code)
				}
				return
			}
			tc.mut(intent)
			if ok, code, _ := IntentFactsComplete(intent); ok || code != tc.want {
				t.Fatalf("got ok=%v code=%q want %q", ok, code, tc.want)
			}
		})
	}
}
