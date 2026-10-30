package confirmation

import (
	"encoding/json"
	"testing"
)

func completeTaskIntent() Intent {
	return Intent{
		IntentID: "confirm_abc", Version: 1, AISessionID: "session", ProjectID: "project",
		Purpose: Task, Action: "CREATE_TASK", State: "PENDING",
		ContentHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Content:     PrepareInput{AISessionID: "session", Purpose: Task, Action: "CREATE_TASK", Variants: []Variant{{ID: "only", Label: "Only", Summary: "s"}}},
	}
}

// A nil Steps or Variants slice on the public copy becomes an empty slice so the
// JSON serializes as [] and the Card entry validator accepts it.
func TestCanonicalizePublicCollectionsMaterializesEmptyArrays(t *testing.T) {
	intent := completeTaskIntent()
	intent.Steps = nil
	intent.Content.Variants = nil
	CanonicalizePublicCollections(&intent)
	if intent.Steps == nil || len(intent.Steps) != 0 {
		t.Fatalf("steps must be non-nil empty, got %#v", intent.Steps)
	}
	if intent.Content.Variants == nil || len(intent.Content.Variants) != 0 {
		t.Fatalf("variants must be non-nil empty, got %#v", intent.Content.Variants)
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if steps, ok := decoded["steps"].([]any); !ok || steps == nil {
		t.Fatalf("steps must serialize as a JSON array, got %T in %s", decoded["steps"], raw)
	}
	content := decoded["content"].(map[string]any)
	if variants, ok := content["variants"].([]any); !ok || variants == nil {
		t.Fatalf("variants must serialize as a JSON array, got %T", content["variants"])
	}
}

// An already-populated collection is left intact; a legitimately empty non-nil
// slice is preserved and never folded back to nil by re-marshaling.
func TestCanonicalizePublicCollectionsPreservesPopulatedAndEmpty(t *testing.T) {
	empty := completeTaskIntent()
	empty.Steps = []Step{}
	empty.Content.Variants = []Variant{{ID: "a", Label: "A", Summary: "s"}, {ID: "b", Label: "B", Summary: "s"}}
	CanonicalizePublicCollections(&empty)
	if len(empty.Content.Variants) != 2 {
		t.Fatalf("populated variants mutated: %#v", empty.Content.Variants)
	}
	if empty.Steps == nil {
		t.Fatal("empty steps folded back to nil")
	}
	raw, _ := json.Marshal(empty.Steps)
	if string(raw) != "[]" {
		t.Fatalf("empty steps must marshal as [], got %s", raw)
	}
}

// Canonicalization must not fabricate or alter authoritative facts. Identity,
// contentHash, a missing content body, a missing projectId and a missing variant
// identity all survive untouched, so the Card still rejects them.
func TestCanonicalizePublicCollectionsNeverFabricatesFacts(t *testing.T) {
	intent := Intent{IntentID: "confirm_xyz", Version: 2, AISessionID: "session", ProjectID: "", Purpose: Review, Action: "REVISE", State: "PENDING", ContentHash: "deadbeef", Content: PrepareInput{Variants: nil}, Steps: nil}
	before := intent
	CanonicalizePublicCollections(&intent)
	if intent.IntentID != before.IntentID || intent.Version != before.Version || intent.ContentHash != before.ContentHash || intent.Purpose != before.Purpose {
		t.Fatal("canonicalization changed an authoritative fact")
	}
	if intent.ProjectID != "" {
		t.Fatal("missing projectId must stay missing")
	}
	if intent.ContentHash != "deadbeef" {
		t.Fatal("invalid contentHash must not be repaired")
	}
	// Only the two required arrays were materialized; nothing else invented.
	if intent.Steps == nil || intent.Content.Variants == nil {
		t.Fatal("required arrays must be materialized")
	}
	if intent.Content.Review != nil {
		t.Fatal("canonicalization must not invent a purpose body")
	}
}

func TestCanonicalizePublicCollectionsNilIntentIsNoop(t *testing.T) {
	CanonicalizePublicCollections(nil)
}

// The shared options canonicalizer (used by MCP, HTTP, Console, Workbench and
// Desktop) materializes every Card-required empty collection as [] so no surface
// can serialize options.projects:null and block the commit preflight.
func TestCanonicalizePublicOptionsMaterializesEmptyArrays(t *testing.T) {
	options := Options{}
	CanonicalizePublicOptions(&options)
	if options.Projects == nil || options.Repositories == nil || options.Branches == nil || options.Variants == nil {
		t.Fatalf("choice collections must be non-nil empty: %#v", options)
	}
	if options.RuntimeOptions == nil || options.DevelopmentContexts == nil {
		t.Fatalf("runtime/development collections must be non-nil empty: %#v", options)
	}
	// Nested row.branches must also serialize as an array, never null.
	options.DevelopmentContexts = []DevelopmentContextOption{{RepositoryID: "repo"}}
	CanonicalizePublicOptions(&options)
	raw, _ := json.Marshal(options)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"projects", "repositories", "branches", "variants", "developmentContexts", "runtimeOptions"} {
		if array, ok := decoded[key].([]any); !ok || array == nil {
			t.Fatalf("options.%s must serialize as a JSON array, got %#v in %s", key, decoded[key], raw)
		}
	}
	row := decoded["developmentContexts"].([]any)[0].(map[string]any)
	if branches, ok := row["branches"].([]any); !ok || branches == nil {
		t.Fatalf("developmentContexts row.branches must serialize as a JSON array, got %#v", row["branches"])
	}
}

// Canonicalization must never fabricate authority: a populated candidate set is
// preserved, and eligibility, identity and backend facts are neither invented nor
// repaired. Genuinely-missing eligibility must stay missing so the Card still
// rejects it (INTENT_ELIGIBILITY_INVALID), rather than being masked by empty arrays.
func TestCanonicalizePublicOptionsNeverFabricatesFacts(t *testing.T) {
	options := Options{
		AISessionID: "session", IntentID: "confirm_x", Version: 3, Purpose: Review,
		ProjectID: "project",
		Variants:  []Choice{{ID: "revise", Label: "R"}},
	}
	before := options
	CanonicalizePublicOptions(&options)
	if options.IntentID != before.IntentID || options.Version != before.Version || options.Purpose != before.Purpose || options.ProjectID != "project" {
		t.Fatal("options canonicalization changed an identity fact")
	}
	if len(options.Variants) != 1 || options.Variants[0].ID != "revise" {
		t.Fatalf("populated variants mutated: %#v", options.Variants)
	}
	if options.Eligibility != (IntentEligibility{}) {
		t.Fatal("canonicalization must not invent eligibility")
	}
	if options.BackendCandidates != nil {
		t.Fatal("canonicalization must not fabricate backend candidates")
	}
}

func TestCanonicalizePublicOptionsNilIsNoop(t *testing.T) {
	CanonicalizePublicOptions(nil)
}
