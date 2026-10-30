package mcpserver

import (
	"encoding/json"
	"os"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// This is the Go half of the shared Card-instance lifecycle fixture. The envelopes
// below were captured from the PRODUCTION confirmation_show / confirmation_options_get
// results of a real replacement flow (real SQLite store, real MCP wire, the full
// middleware stack) by internal/controlapi/confirmation_card_instance_lifecycle_test.go.
// The Card JavaScript test loads the SAME file and replays the SAME bytes through the
// actual embedded Card script, so the Host-side and Card-side views of one intent pair
// can never drift apart, and neither side restates the other's expectations by hand.

type cardInstanceFixtureCard struct {
	Entry   map[string]any `json:"entry"`
	Options struct {
		Entry map[string]any `json:"entry"`
	} `json:"options"`
	Meta struct {
		ResourceURI            string  `json:"resourceUri"`
		OutputTemplate         string  `json:"outputTemplate"`
		AISessionID            string  `json:"aiSessionId"`
		IntentID               string  `json:"intentId"`
		Version                float64 `json:"version"`
		ContentHash            string  `json:"contentHash"`
		PresentationInstanceID string  `json:"presentationInstanceId"`
		ComponentVersion       string  `json:"componentVersion"`
		MainScriptSHA256       string  `json:"mainScriptSHA256"`
	} `json:"meta"`
	State                    string `json:"state"`
	IntentID                 string `json:"intentId"`
	Version                  int    `json:"version"`
	ContentHash              string `json:"contentHash"`
	PresentationInstanceID   string `json:"presentationInstanceId"`
	ExpectCommittableVariant string `json:"expectCommittableVariant"`
}

type cardInstanceFixture struct {
	Meta struct {
		Schema               string `json:"schema"`
		StaticResourceURI    string `json:"staticResourceUri"`
		InstanceIdentityRule string `json:"instanceIdentityRule"`
		SingleSourceOfTruth  string `json:"singleSourceOfTruth"`
	} `json:"_meta"`
	Superseded  cardInstanceFixtureCard `json:"superseded"`
	Replacement cardInstanceFixtureCard `json:"replacement"`
}

func loadCardInstanceFixture(t *testing.T) cardInstanceFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/confirmation_card_instance_lifecycle_v5.json")
	if err != nil {
		t.Fatalf("read shared Card instance fixture: %v", err)
	}
	var file cardInstanceFixture
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode shared Card instance fixture: %v", err)
	}
	if file.Meta.Schema != "devbridge/confirmation-card-instance-lifecycle/v5" {
		t.Fatalf("unexpected fixture schema %q", file.Meta.Schema)
	}
	if file.Meta.StaticResourceURI != confirmationResourceURI {
		t.Fatalf("fixture static resource %q != embedded Card resource %q", file.Meta.StaticResourceURI, confirmationResourceURI)
	}
	for _, card := range []cardInstanceFixtureCard{file.Superseded, file.Replacement} {
		if card.Entry == nil || card.Options.Entry == nil {
			t.Fatalf("fixture card %s is missing a captured envelope", card.IntentID)
		}
	}
	return file
}

// entryResult decodes a captured public envelope through the advertised Go
// contract, so the assertions below read the same fields the Card receives.
func entryResult(t *testing.T, card cardInstanceFixtureCard) (c.Result, []byte) {
	t.Helper()
	raw, err := json.Marshal(card.Entry)
	if err != nil {
		t.Fatalf("recanonicalize captured envelope for %s: %v", card.IntentID, err)
	}
	var result c.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode captured envelope for %s: %v", card.IntentID, err)
	}
	if result.Intent == nil {
		t.Fatalf("captured envelope for %s carries no intent", card.IntentID)
	}
	return result, raw
}

func optionsResult(t *testing.T, card cardInstanceFixtureCard) (c.Result, []byte) {
	t.Helper()
	raw, err := json.Marshal(card.Options.Entry)
	if err != nil {
		t.Fatalf("recanonicalize captured options envelope for %s: %v", card.IntentID, err)
	}
	var result c.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode captured options envelope for %s: %v", card.IntentID, err)
	}
	if result.Options == nil {
		t.Fatalf("captured options envelope for %s carries no options", card.IntentID)
	}
	return result, raw
}

func availableVariantIDs(variants []c.Choice) []string {
	var out []string
	for _, variant := range variants {
		if variant.Available {
			out = append(out, variant.ID)
		}
	}
	return out
}

// The captured production envelopes are still mountable Cards today: each one
// satisfies the Host mount gate, names the one static Card resource, and its
// Host-facing identity meta is the same frozen identity as its own body. The
// captured main-script digest must be the currently embedded script, otherwise the
// two runtimes would be reasoning about different Cards.
func TestSharedCardInstanceFixtureEnvelopesStillMount(t *testing.T) {
	file := loadCardInstanceFixture(t)
	if file.Meta.SingleSourceOfTruth == "" {
		t.Fatal("fixture must declare its single-source-of-truth contract")
	}
	for name, card := range map[string]cardInstanceFixtureCard{"superseded": file.Superseded, "replacement": file.Replacement} {
		result, raw := entryResult(t, card)
		if failure := ValidateConfirmationCardEntryProjectionJSON(raw); failure != nil {
			t.Fatalf("%s: captured production envelope rejected by the Card entry mount gate: %+v", name, failure)
		}
		if card.Meta.ResourceURI != confirmationResourceURI || card.Meta.OutputTemplate != confirmationResourceURI {
			t.Fatalf("%s: Card resource %q / output template %q != %q", name, card.Meta.ResourceURI, card.Meta.OutputTemplate, confirmationResourceURI)
		}
		if card.Meta.ComponentVersion != confirmationComponentVersion {
			t.Fatalf("%s: component version %q != %q", name, card.Meta.ComponentVersion, confirmationComponentVersion)
		}
		if card.Meta.MainScriptSHA256 != confirmationScriptSHA256() {
			t.Fatalf("%s: fixture was captured from a different Card script (%q != embedded %q)", name, card.Meta.MainScriptSHA256, confirmationScriptSHA256())
		}
		intent := result.Intent
		if card.Meta.AISessionID != intent.AISessionID || card.Meta.IntentID != intent.IntentID || int(card.Meta.Version) != intent.Version || card.Meta.ContentHash != intent.ContentHash {
			t.Fatalf("%s: Host identity meta %+v is separable from the entry body %+v", name, card.Meta, intent)
		}
		if card.Meta.IntentID != card.IntentID || card.Meta.ContentHash != card.ContentHash || int(card.Meta.Version) != card.Version {
			t.Fatalf("%s: fixture header identity drifted from its meta", name)
		}
		if intent.IntentID == file.Superseded.IntentID && intent.State != "SUPERSEDED" {
			t.Fatalf("superseded intent state=%s", intent.State)
		}
	}
}

// §4 + §6 + §7: each intent is its own Card instance, derived only from its frozen
// identity, and the replacement Card inherits nothing from the intent it replaced.
func TestSharedCardInstanceFixtureKeepsTwoIndependentInstances(t *testing.T) {
	file := loadCardInstanceFixture(t)
	superseded, _ := entryResult(t, file.Superseded)
	replacement, _ := entryResult(t, file.Replacement)
	old, fresh := superseded.Intent, replacement.Intent

	if old.State != "SUPERSEDED" {
		t.Fatalf("G3 superseded Card state=%s", old.State)
	}
	if fresh.State != "PENDING" {
		t.Fatalf("G4 replacement Card state=%s", fresh.State)
	}
	if old.IntentID == fresh.IntentID || old.ContentHash == fresh.ContentHash || old.Content.RequestID == fresh.Content.RequestID {
		t.Fatalf("replacement reused the replaced identity: %s / %s", old.IntentID, fresh.IntentID)
	}

	derived := func(intent *c.Intent) string {
		return c.ValueHash([]any{intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash})
	}
	for name, card := range map[string]cardInstanceFixtureCard{"superseded": file.Superseded, "replacement": file.Replacement} {
		intent, _ := entryResult(t, card)
		if got, want := card.PresentationInstanceID, derived(intent.Intent); got != want {
			t.Fatalf("%s: captured instance id %q != derived-from-frozen-identity %q", name, got, want)
		}
		if card.Meta.PresentationInstanceID != card.PresentationInstanceID {
			t.Fatalf("%s: Host meta instance id %q != derived %q", name, card.Meta.PresentationInstanceID, card.PresentationInstanceID)
		}
	}
	if file.Superseded.PresentationInstanceID == file.Replacement.PresentationInstanceID {
		t.Fatalf("G2 one Card instance for two intents: %s", file.Replacement.PresentationInstanceID)
	}
	if file.Superseded.Meta.ResourceURI != file.Replacement.Meta.ResourceURI {
		t.Fatalf("G2 two Card resources for one component version")
	}

	// §6: sourceIntentId is lineage only. It is the single place the old identity
	// appears, and it never appears on the old Card.
	if fresh.Content.SourceIntentID != old.IntentID {
		t.Fatalf("replacement lineage=%q, want %q", fresh.Content.SourceIntentID, old.IntentID)
	}
	if old.Content.SourceIntentID != "" {
		t.Fatalf("superseded intent reports lineage %q", old.Content.SourceIntentID)
	}
	if fresh.AISessionID != old.AISessionID || fresh.ProjectID != old.ProjectID || fresh.Purpose != old.Purpose || fresh.Action != old.Action || fresh.Version != old.Version {
		t.Fatalf("replacement is not the same-operation successor: %+v vs %+v", fresh, old)
	}
	// The two Cards describe the same Task/Review/Decision: no duplicate object and
	// no drift to another target, which is exactly what the product requires.
	for name, card := range map[string]c.Result{"superseded": superseded, "replacement": replacement} {
		if card.Display == nil {
			t.Fatalf("%s: captured Card has no display", name)
		}
		if card.Intent.Content.Review == nil {
			t.Fatalf("%s: captured Review-decision Card has no review body", name)
		}
	}
	if superseded.Display.TaskID != replacement.Display.TaskID || superseded.Display.ReviewID != replacement.Display.ReviewID || superseded.Display.DecisionID != replacement.Display.DecisionID {
		t.Fatalf("replacement Card moved to another Task/Review/Decision")
	}
	if fresh.IntentID != replacement.Display.IntentID || old.IntentID != superseded.Display.IntentID {
		t.Fatalf("display identity crossed intents")
	}

	// §7: no cross-intent business state. Neither captured Card carries a
	// submission, and the replacement has not started any step.
	if old.Submission != nil || fresh.Submission != nil {
		t.Fatalf("prepare captured a commit state: old=%s fresh=%s", jsonProbe(old.Submission), jsonProbe(fresh.Submission))
	}
	if len(fresh.Steps) != 0 {
		t.Fatalf("replacement Card carried steps from the superseded intent: %+v", fresh.Steps)
	}
	if fresh.Commit != nil {
		t.Fatalf("replacement Card carried a commit request: %+v", fresh.Commit)
	}
	if fresh.CurrentUpperReadAvailable != old.CurrentUpperReadAvailable {
		t.Fatalf("replacement Card read a different upper state: %v vs %v", old.CurrentUpperReadAvailable, fresh.CurrentUpperReadAvailable)
	}
}

func jsonProbe(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(raw)
}

// The captured pair is also fed back through the production §8 mount gate, so the
// fixture guards cannot pass merely because nobody re-checks the envelopes: asking
// for one intent and receiving the other Card entry must fail closed, an incomplete
// frozen identity must fail closed, and the identity meta the Host receives must be
// exactly what the current code derives from the entry body.
func TestSharedCardInstanceFixtureIdentityGateRejectsCrossIntentMounts(t *testing.T) {
	file := loadCardInstanceFixture(t)
	supersededEntry, _ := entryResult(t, file.Superseded)
	replacementEntry, _ := entryResult(t, file.Replacement)

	readFor := func(intent *c.Intent) c.ReadInput {
		return c.ReadInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash}
	}
	if failure := ValidateConfirmationCardInstanceIdentity(supersededEntry, readFor(supersededEntry.Intent)); failure != nil {
		t.Fatalf("honest mount of the superseded Card was refused: %+v", failure)
	}
	if failure := ValidateConfirmationCardInstanceIdentity(replacementEntry, readFor(replacementEntry.Intent)); failure != nil {
		t.Fatalf("honest mount of the replacement Card was refused: %+v", failure)
	}
	// The live defect: the Host asked for one intent and was handed the other Card.
	for name, probe := range map[string]struct {
		entry     c.Result
		requested c.ReadInput
	}{
		"new entry into the old request": {replacementEntry, readFor(supersededEntry.Intent)},
		"old entry into the new request": {supersededEntry, readFor(replacementEntry.Intent)},
	} {
		failure := ValidateConfirmationCardInstanceIdentity(probe.entry, probe.requested)
		if failure == nil {
			t.Fatalf("%s was accepted by the mount gate", name)
		}
		if failure.Code != cardInstanceIdentityMismatch || failure.ReasonCode != cardInstanceIdentityMismatch {
			t.Fatalf("%s rejection code=%q reason=%q, want %q", name, failure.Code, failure.ReasonCode, cardInstanceIdentityMismatch)
		}
		if failure.Stage != "HOST_MOUNT" || failure.NextAction != c.ReadCurrentTarget {
			t.Fatalf("%s rejection is not a read-only mount refusal: %+v", name, failure)
		}
	}
	// An entry with no frozen identity can never be given an instance key.
	for _, field := range []string{"aiSessionId", "intentId", "version", "contentHash"} {
		broken, err := json.Marshal(file.Replacement.Entry)
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(broken, &envelope); err != nil {
			t.Fatal(err)
		}
		intent := envelope["intent"].(map[string]any)
		switch field {
		case "version":
			intent["version"] = 0
		default:
			intent[field] = ""
		}
		var result c.Result
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		if failure := ValidateConfirmationCardInstanceIdentity(result, readFor(result.Intent)); failure == nil {
			t.Fatalf("entry with an empty %s was mounted without an instance identity", field)
		} else if failure.Code != cardInstanceNewUnavailable {
			t.Fatalf("empty %s rejection code=%q, want %q", field, failure.Code, cardInstanceNewUnavailable)
		}
	}
	// The Host-facing meta is derived from the entry, never carried over: replaying
	// the current derivation must reproduce the captured production meta exactly.
	for name, card := range map[string]cardInstanceFixtureCard{"superseded": file.Superseded, "replacement": file.Replacement} {
		result, _ := entryResult(t, card)
		meta := primaryCardMeta(confirmationResourceURI)
		decorateConfirmationInstanceMeta(meta, *result.Intent)
		for key, want := range map[string]any{
			"devbridge/aiSessionId":            result.Intent.AISessionID,
			"devbridge/intentId":               result.Intent.IntentID,
			"devbridge/version":                result.Intent.Version,
			"devbridge/contentHash":            result.Intent.ContentHash,
			"devbridge/presentationInstanceId": card.PresentationInstanceID,
		} {
			if got := meta[key]; !sameJSONValue(got, want) {
				t.Fatalf("%s: derived meta %s=%s no longer matches the captured production meta %s", name, key, jsonProbe(got), jsonProbe(want))
			}
		}
		// §5: the static resource stays the shared cache key for both instances.
		if got := metaResourceURI(meta); got != confirmationResourceURI {
			t.Fatalf("%s: static resource URI became instance-specific: %q", name, got)
		}
		// The derivation adds identity facts only; the Card's own version and script
		// digest stay the handler-owned constants checked above.
		for _, private := range []string{"devbridge/componentVersion", "devbridge/mainScriptSHA256", "devbridge/sourceIntentId", "devbridge/reviewId", "devbridge/taskId"} {
			if _, present := meta[private]; present {
				t.Fatalf("%s: instance derivation emitted %s=%s", name, private, jsonProbe(meta[private]))
			}
		}
	}

	// One presentation handle may not carry both intents, while the same identity may
	// refresh on its own handle forever.
	registry := newConfirmationInstanceRegistry()
	const handle = "shared-card-instance-fixture-handle"
	if code := registry.checkOrBind(handle, *supersededEntry.Intent); code != "" {
		t.Fatalf("first mount of the superseded Card was refused: %s", code)
	}
	if code := registry.checkOrBind(handle, *replacementEntry.Intent); code != cardInstanceReuseRejected {
		t.Fatalf("replacement intent reused the superseded handle, code=%q", code)
	}
	if code := registry.checkOrBind(handle, *supersededEntry.Intent); code != "" {
		t.Fatalf("same-intent refresh broke its own instance binding: %s", code)
	}
	if code := registry.checkOrBind("independent-handle", *replacementEntry.Intent); code != "" {
		t.Fatalf("an independent handle must mount the replacement Card: %s", code)
	}
}

// G3/G4 through the capability read: the fixture, not a hand-written table, states
// which Card may still confirm, and both runtimes read that same field.
func TestSharedCardInstanceFixtureCapabilityMatchesProductBehavior(t *testing.T) {
	file := loadCardInstanceFixture(t)
	for name, card := range map[string]cardInstanceFixtureCard{"superseded": file.Superseded, "replacement": file.Replacement} {
		result, raw := optionsResult(t, card)
		if failure := ValidateConfirmationOptionsProjectionJSON(raw); failure != nil {
			t.Fatalf("%s: captured options envelope rejected by the options mount gate: %+v", name, failure)
		}
		available := availableVariantIDs(result.Options.Variants)
		if card.ExpectCommittableVariant == "" {
			if result.Options.Eligibility.Available || len(available) != 0 {
				t.Fatalf("%s: superseded Card still presents a confirm action: eligibility=%+v variants=%v", name, result.Options.Eligibility, available)
			}
			continue
		}
		if !result.Options.Eligibility.Available {
			t.Fatalf("%s: replacement Card cannot confirm: %+v", name, result.Options.Eligibility)
		}
		if len(available) != 1 || available[0] != card.ExpectCommittableVariant {
			t.Fatalf("%s: available variants=%v, want exactly %q", name, available, card.ExpectCommittableVariant)
		}
	}
	if file.Replacement.ExpectCommittableVariant != "revise" {
		t.Fatalf("fixture no longer pins the same-Task re-review action")
	}
}
