package mcpserver

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// This is the Go half of the shared rejected-retry fixture. Every envelope here was
// captured from the PRODUCTION confirmation_show / confirmation_options_get /
// confirmation_commit / confirmation_get structuredContent of one real
// REJECTED_NOT_CONSUMED flow (real Streamable HTTP MCP wire, production middleware,
// real SQLite store) by
// internal/controlapi/confirmation_rejected_retry_lifecycle_test.go. The Card
// JavaScript replay in confirmation_rejected_retry_test.js loads the SAME file, so the
// Core-side durable bytes and the Card-side unlock behavior are proven over one set of
// bytes that neither side restates by hand.

type rejectedRetryFixtureMeta struct {
	Schema              string `json:"schema"`
	SingleSourceOfTruth string `json:"singleSourceOfTruth"`
	CapturedBy          string `json:"capturedBy"`
	StaticResourceURI   string `json:"staticResourceUri"`
	RejectionTrigger    string `json:"rejectionTrigger"`
	UnlockPredicate     string `json:"unlockPredicate"`
	CardCopy            string `json:"cardCopy"`
	ComponentVersion    string `json:"componentVersion"`
	MainScriptSHA256    string `json:"mainScriptSHA256"`
}

// rejectedRetryFixtureCard is one captured read: the public entry envelope, its Host
// identity meta, and the intent identity the §7 read must name exactly.
type rejectedRetryFixtureCard struct {
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
	State                  string `json:"state"`
	IntentID               string `json:"intentId"`
	Version                int    `json:"version"`
	ContentHash            string `json:"contentHash"`
	PresentationInstanceID string `json:"presentationInstanceId"`
}

type rejectedRetryFixture struct {
	Meta      rejectedRetryFixtureMeta `json:"_meta"`
	Pending   rejectedRetryFixtureCard `json:"pending"`
	Rejection struct {
		Entry       map[string]any `json:"entry"`
		Facts       map[string]any `json:"facts"`
		IsError     bool           `json:"isError"`
		RequestID   string         `json:"requestId"`
		FailureCode string         `json:"failureCode"`
	} `json:"rejection"`
	ReceiptReadback struct {
		Entry map[string]any `json:"entry"`
	} `json:"receiptReadback"`
}

func loadRejectedRetryFixture(t *testing.T) rejectedRetryFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/confirmation_rejected_retry_v5.json")
	if err != nil {
		t.Fatalf("read shared rejected-retry fixture: %v", err)
	}
	var file rejectedRetryFixture
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode shared rejected-retry fixture: %v", err)
	}
	if file.Meta.Schema != "devbridge/confirmation-rejected-retry/v5" {
		t.Fatalf("unexpected fixture schema %q", file.Meta.Schema)
	}
	if file.Meta.StaticResourceURI != confirmationResourceURI {
		t.Fatalf("fixture static resource %q != embedded Card resource %q", file.Meta.StaticResourceURI, confirmationResourceURI)
	}
	if file.Meta.SingleSourceOfTruth == "" || file.Meta.CapturedBy == "" || file.Meta.RejectionTrigger == "" {
		t.Fatal("fixture must declare its single-source-of-truth, capture site and rejection trigger")
	}
	for name, envelope := range map[string]map[string]any{
		"pending":          file.Pending.Entry,
		"pending options":  file.Pending.Options.Entry,
		"rejection":        file.Rejection.Entry,
		"receipt readback": file.ReceiptReadback.Entry,
	} {
		if envelope == nil {
			t.Fatalf("fixture is missing its captured %s envelope", name)
		}
	}
	return file
}

// canonicalEnvelope re-encodes one captured envelope so both runtimes validate the
// same bytes the Card would receive.
func canonicalEnvelope(t *testing.T, envelope map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("recanonicalize captured envelope: %v", err)
	}
	return raw
}

func fixtureOptions(t *testing.T, envelope map[string]any) c.Options {
	t.Helper()
	var result c.Result
	if err := json.Unmarshal(canonicalEnvelope(t, envelope), &result); err != nil {
		t.Fatalf("decode captured options envelope: %v", err)
	}
	if result.Options == nil {
		t.Fatal("captured options envelope carries no options")
	}
	return *result.Options
}

// fixtureIntent decodes a captured envelope through the advertised Go contract, so the
// guard reads the same fields the Card receives.
func fixtureIntent(t *testing.T, envelope map[string]any) (c.Result, []byte) {
	t.Helper()
	raw := canonicalEnvelope(t, envelope)
	var result c.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode captured envelope: %v", err)
	}
	if result.Intent == nil {
		t.Fatal("captured envelope carries no intent")
	}
	return result, raw
}

func submissionBytes(t *testing.T, envelope map[string]any) []byte {
	t.Helper()
	result, _ := fixtureIntent(t, envelope)
	if result.Intent.Submission == nil {
		t.Fatal("captured envelope carries no submission")
	}
	raw, err := json.Marshal(result.Intent.Submission)
	if err != nil {
		t.Fatalf("re-encode captured submission: %v", err)
	}
	return raw
}

// The captured bytes still describe this exact embedded Card: one static resource, the
// current component version and the currently embedded main-script digest. A drifted
// digest would mean the two runtimes were reasoning about different Cards.
func TestSharedRejectedRetryFixtureIsFromThisEmbeddedCard(t *testing.T) {
	file := loadRejectedRetryFixture(t)
	if file.Meta.ComponentVersion != confirmationComponentVersion {
		t.Fatalf("fixture component %q != %q", file.Meta.ComponentVersion, confirmationComponentVersion)
	}
	if file.Meta.MainScriptSHA256 != confirmationScriptSHA256() {
		t.Fatalf("fixture was captured from a different Card script (%q != embedded %q)", file.Meta.MainScriptSHA256, confirmationScriptSHA256())
	}
	if file.Pending.Meta.ResourceURI != confirmationResourceURI || file.Pending.Meta.OutputTemplate != confirmationResourceURI {
		t.Fatalf("Card resource %q / output template %q != %q", file.Pending.Meta.ResourceURI, file.Pending.Meta.OutputTemplate, confirmationResourceURI)
	}
	if file.Pending.Meta.MainScriptSHA256 != confirmationScriptSHA256() || file.Rejection.RequestID == "" {
		t.Fatal("fixture pending meta or rejection request identity is incomplete")
	}
	// §4's visible retry copy is frozen with the bytes the Card replays.
	if file.Meta.CardCopy != "本次确认未被消费。请修改选择或重新确认。" {
		t.Fatalf("retry copy drifted: %q", file.Meta.CardCopy)
	}
	if file.Rejection.IsError != true {
		t.Fatal("a Core refusal must be captured as a wire error")
	}
}

// §3 + §7: the pending Card and the rejecting Card are the same instance identity, the
// same frozen content, and both still satisfy the production Host mount gates. The
// rejection is durable read-back evidence, not a transient message.
func TestSharedRejectedRetryFixtureEnvelopesStillMount(t *testing.T) {
	file := loadRejectedRetryFixture(t)

	pending, pendingRaw := fixtureIntent(t, file.Pending.Entry)
	if failure := ValidateConfirmationCardEntryProjectionJSON(pendingRaw); failure != nil {
		t.Fatalf("captured pending envelope rejected by the Card entry mount gate: %+v", failure)
	}
	options := fixtureOptions(t, file.Pending.Options.Entry)
	if failure := ValidateConfirmationOptionsProjectionJSON(canonicalEnvelope(t, file.Pending.Options.Entry)); failure != nil {
		t.Fatalf("captured options envelope rejected by the options mount gate: %+v", failure)
	}
	if !options.Eligibility.Available {
		t.Fatalf("the pending Card's own options were not available at capture: %+v", options.Eligibility)
	}
	if pending.Intent.Submission != nil || pending.Intent.Commit != nil || len(pending.Intent.Steps) != 0 {
		t.Fatalf("prepare captured a commit state: submission=%s commit=%s steps=%d", jsonProbe(pending.Intent.Submission), jsonProbe(pending.Intent.Commit), len(pending.Intent.Steps))
	}
	if pending.Intent.State != "PENDING" || pending.Intent.Version != 1 {
		t.Fatalf("pending Card state=%s version=%d", pending.Intent.State, pending.Intent.Version)
	}
	if file.Pending.State != pending.Intent.State || file.Pending.IntentID != pending.Intent.IntentID || file.Pending.Version != pending.Intent.Version || file.Pending.ContentHash != pending.Intent.ContentHash {
		t.Fatalf("fixture header identity drifted from the captured entry: %+v vs %+v", file.Pending, pending.Intent)
	}
	if file.Pending.Meta.AISessionID != pending.Intent.AISessionID || file.Pending.Meta.IntentID != pending.Intent.IntentID || int(file.Pending.Meta.Version) != pending.Intent.Version || file.Pending.Meta.ContentHash != pending.Intent.ContentHash {
		t.Fatalf("Host identity meta %+v is separable from the entry body", file.Pending.Meta)
	}
	derived := c.ValueHash([]any{pending.Intent.AISessionID, pending.Intent.IntentID, pending.Intent.Version, pending.Intent.ContentHash})
	if file.Pending.PresentationInstanceID != derived || file.Pending.Meta.PresentationInstanceID != derived {
		t.Fatalf("captured instance %q / meta %q != identity-derived %q", file.Pending.PresentationInstanceID, file.Pending.Meta.PresentationInstanceID, derived)
	}

	rejection, rejectionRaw := fixtureIntent(t, file.Rejection.Entry)
	if failure := ValidateConfirmationCardEntryProjectionJSON(rejectionRaw); failure != nil {
		t.Fatalf("captured rejection envelope rejected by the Card entry mount gate: %+v", failure)
	}
	if rejection.Intent.Submission == nil {
		t.Fatal("the captured rejection envelope records no submission")
	}
	if rejection.Failure == nil {
		t.Fatal("the captured refusal carries no public failure")
	}
	// Same intent, same instance, same frozen content: the refusal left the intent
	// exactly where the pending Card was (§10 immutable after prepare).
	if a, b := rejection.Intent, pending.Intent; a.IntentID != b.IntentID || a.Version != b.Version || a.ContentHash != b.ContentHash || a.State != b.State || a.Content.RequestID != b.Content.RequestID {
		t.Fatalf("the refusal moved the intent it refused: %+v vs %+v", a, b)
	}
	if file.Rejection.FailureCode != rejection.Intent.Submission.Failure.Code || rejection.Intent.Submission.Failure.NextAction != c.EditSelection {
		t.Fatalf("refusal code %q != recorded %q (nextAction=%q)", file.Rejection.FailureCode, rejection.Intent.Submission.Failure.Code, rejection.Intent.Submission.Failure.NextAction)
	}
	if file.Rejection.RequestID != rejection.Intent.Submission.Request.RequestID {
		t.Fatalf("submitted requestId %q != recorded %q", file.Rejection.RequestID, rejection.Intent.Submission.Request.RequestID)
	}
	readback, readbackRaw := fixtureIntent(t, file.ReceiptReadback.Entry)
	if failure := ValidateConfirmationCardEntryProjectionJSON(readbackRaw); failure != nil {
		t.Fatalf("captured receipt readback rejected by the Card entry mount gate: %+v", failure)
	}
	if !sameJSONValue(readback.Intent.Submission, rejection.Intent.Submission) {
		t.Fatalf("the receipt read-back changed the durable refusal:\n %s\n %s", submissionBytes(t, file.ReceiptReadback.Entry), submissionBytes(t, file.Rejection.Entry))
	}
	// The read-back is addressed by the same exact five-field identity (§7).
	readFor := c.ReadInput{AISessionID: rejection.Intent.AISessionID, IntentID: rejection.Intent.IntentID, Version: rejection.Intent.Version, Purpose: rejection.Intent.Purpose, ContentHash: rejection.Intent.ContentHash}
	if failure := ValidateConfirmationCardInstanceIdentity(rejection, readFor); failure != nil {
		t.Fatalf("exact read of the rejecting Card was refused: %+v", failure)
	}
	if failure := ValidateConfirmationCardInstanceIdentity(readback, readFor); failure != nil {
		t.Fatalf("exact read of the receipt read-back was refused: %+v", failure)
	}
	stale := readFor
	stale.ContentHash = strings.Repeat("0", 64)
	if failure := ValidateConfirmationCardInstanceIdentity(rejection, stale); failure == nil || failure.Code != cardInstanceIdentityMismatch {
		t.Fatalf("a stale digest read the rejecting Card: %+v", failure)
	}
}

// §3: the recorded refusal satisfies the conjunction on its own, and the reducer
// reproduces every derived fact from the captured bytes rather than trusting them.
func TestSharedRejectedRetryRefusalReDerivesItsRetryFacts(t *testing.T) {
	file := loadRejectedRetryFixture(t)
	captured, _ := fixtureIntent(t, file.Rejection.Entry)
	sub := captured.Intent.Submission
	if sub == nil {
		t.Fatal("captured refusal carries no submission")
	}
	if !sub.Recorded || sub.Outcome != c.RejectedNotConsumed || sub.BusinessOutcome != "REJECTED_NOT_CONSUMED" || sub.ConsumptionDisposition != "NOT_CONSUMED" || sub.Consumed == nil || *sub.Consumed {
		t.Fatalf("captured refusal is not an explicit non-consumption: %s", jsonProbe(sub))
	}
	if sub.NextAction != c.EditSelection || sub.RetryDisposition != c.RetryEditSelectionAndResubmit || sub.RedispatchSameIntent {
		t.Fatalf("captured refusal is not editable-retry: nextAction=%q retry=%q redispatch=%v", sub.NextAction, sub.RetryDisposition, sub.RedispatchSameIntent)
	}
	// A rejection carries no consumption pointer; its receipt identity is the frozen
	// request plus the payload hash Core stored beside it.
	if sub.ReceiptRequestID != "" || sub.PayloadSHA256 == "" || len(sub.PayloadSHA256) != 64 {
		t.Fatalf("rejection receipt identity=%q payload=%q", sub.ReceiptRequestID, sub.PayloadSHA256)
	}

	// Forget every derived fact and re-run the one reducer over the captured durable
	// bytes. Only the captured 24h production window is re-anchored, because the
	// reducer is clock-dependent; nothing else is rewritten.
	derived, _ := fixtureIntent(t, file.Rejection.Entry)
	retry := derived.Intent.Submission
	retry.BusinessOutcome = ""
	retry.ConsumptionDisposition = ""
	retry.Consumed = nil
	retry.RetryDisposition = ""
	retry.RedispatchSameIntent = true
	retry.ReprepareAllowed = false
	derived.Intent.ExpiresAt = time.Now().Add(time.Hour)
	c.NormalizePublicIntent(derived.Intent)
	if got, want := jsonMarshalFlat(derived.Intent.Submission), jsonMarshalFlat(captured.Intent.Submission); got != want {
		t.Fatalf("Core re-derivation no longer reproduces the captured refusal:\n got %s\nwant %s", got, want)
	}

	// The re-anchoring is not hiding a fail-open: the same bytes on an expired intent
	// must leave the retry path, and an unrecorded read must never claim non-consumption.
	expired, _ := fixtureIntent(t, file.Rejection.Entry)
	expired.Intent.ExpiresAt = time.Now().Add(-time.Minute)
	c.NormalizePublicIntent(expired.Intent)
	if got := expired.Intent.Submission.RetryDisposition; got != c.RetryReprepareRequired {
		t.Fatalf("an expired intent advertised %q", got)
	}
	unrecorded, _ := fixtureIntent(t, file.Rejection.Entry)
	unrecorded.Intent.Submission.Recorded = false
	c.NormalizePublicIntent(unrecorded.Intent)
	if got := unrecorded.Intent.Submission.RetryDisposition; got != c.RetryReadOriginalReceipt {
		t.Fatalf("an unrecorded read advertised %q", got)
	}
}

// §5: the predicate the fixture documents is the predicate the bytes satisfy, field by
// field. A documented fact that the captured refusal no longer carries fails here.
func TestSharedRejectedRetryUnlockPredicateMatchesDurableBytes(t *testing.T) {
	file := loadRejectedRetryFixture(t)
	result, _ := fixtureIntent(t, file.Rejection.Entry)
	raw, err := json.Marshal(result.Intent.Submission)
	if err != nil {
		t.Fatal(err)
	}
	var facts map[string]any
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	facts["intent.state"] = result.Intent.State
	facts["request.contentHash"] = result.Intent.Submission.Request.ContentHash
	facts["intent.contentHash"] = result.Intent.ContentHash
	// The predicate's final clause is prose, so it is stated as the one boolean fact it
	// names: a rejection has no consumption pointer, and its own frozen request plus the
	// stored payload hash is the whole receipt the held request must match.
	receipt := result.Intent.Submission
	facts["receipt identity matches the pending commit"] = receipt.ReceiptRequestID == "" &&
		receipt.Request.AISessionID == result.Intent.AISessionID &&
		receipt.Request.IntentID == result.Intent.IntentID &&
		receipt.Request.Version == result.Intent.Version &&
		receipt.Request.Purpose == result.Intent.Purpose &&
		receipt.Request.ContentHash == result.Intent.ContentHash &&
		len(receipt.PayloadSHA256) == 64
	parsed := 0
	for _, clause := range strings.Split(file.Meta.UnlockPredicate, "&&") {
		clause = strings.TrimSpace(clause)
		key, want, found := strings.Cut(clause, "==")
		key = strings.TrimSpace(key)
		value, present := facts[key]
		if !present {
			t.Fatalf("predicate clause %q names a fact the public submission does not carry", clause)
		}
		parsed++
		if !found {
			// A bare clause is a required boolean fact, not prose to skim past.
			if truth, ok := value.(bool); !ok || !truth {
				t.Fatalf("predicate clause %q is false on the captured bytes (got %s)", clause, jsonMarshalFlat(value))
			}
			continue
		}
		want = strings.TrimSpace(want)
		if got := jsonMarshalFlat(value); got != `"`+want+`"` && got != want {
			t.Fatalf("predicate clause %q is false on the captured bytes (got %s)", clause, got)
		}
	}
	// Every documented fact is checked above, so a predicate that silently loses a
	// clause cannot keep claiming the §3 conjunction.
	if parsed < 8 {
		t.Fatalf("the documented unlock predicate only names %d checkable facts", parsed)
	}
	if result.Intent.Submission.Request.ContentHash != result.Intent.ContentHash {
		t.Fatal("the recorded request names a content digest other than the frozen intent")
	}
	// "receipt identity matches the pending commit": a rejection carries no consumption
	// pointer, so the request plus the stored payload hash is its whole receipt.
	if result.Intent.Submission.ReceiptRequestID != "" || len(result.Intent.Submission.PayloadSHA256) != 64 {
		t.Fatalf("the captured rejection receipt is not its own frozen request: %s", jsonProbe(result.Intent.Submission))
	}
}

// The fixture's editable-retry shape is load-bearing: a refusal that claims
// EDIT_SELECTION_AND_RESUBMIT while its intent can no longer accept a command must be
// refused by the same production gate that accepts the captured bytes.
func TestSharedRejectedRetryMountGateRefusesBrokenRetryClaims(t *testing.T) {
	file := loadRejectedRetryFixture(t)
	for _, probe := range []struct {
		name   string
		wants  string
		mutate func(intent map[string]any)
	}{
		{"a terminal intent advertising a same-intent edit", "RETRY_DISPOSITION_CONFLICT", func(intent map[string]any) { intent["state"] = "SUPERSEDED" }},
		{"a consumed outcome advertising non-consumption", "CONSUMPTION_RECEIPT_MISMATCH", func(intent map[string]any) {
			intent["submission"].(map[string]any)["outcome"] = "CONSUMED"
		}},
		{"a retry disposition resting on a non-editable action", "RETRY_DISPOSITION_CONFLICT", func(intent map[string]any) {
			submission := intent["submission"].(map[string]any)
			submission["nextAction"] = "READ_CURRENT_TARGET"
		}},
	} {
		envelope := map[string]any{}
		raw, err := json.Marshal(file.Rejection.Entry)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		probe.mutate(envelope["intent"].(map[string]any))
		broken, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if failure := ValidateConfirmationCardEntryProjectionJSON(broken); failure == nil {
			t.Fatalf("%s was accepted by the Card entry mount gate", probe.name)
		} else {
			t.Logf("%s refused at %s", probe.name, failure.Message)
			if failure.Code != "CONFIRMATION_CARD_ENTRY_PROJECTION_INVALID" || failure.NextAction != c.ReadCurrentTarget {
				t.Fatalf("%s refusal is not a read-only projection refusal: code=%q reason=%q next=%q", probe.name, failure.Code, failure.ReasonCode, failure.NextAction)
			}
			if failure.ReasonCode == "" || !strings.Contains(failure.Message, "intent.submission") {
				t.Fatalf("%s refusal does not name the rejected durable fact: %+q", probe.name, failure)
			}
			if probe.wants != failure.ReasonCode {
				t.Fatalf("%s refusal reason=%q, want the %q clause", probe.name, failure.ReasonCode, probe.wants)
			}
		}
	}
}

func jsonMarshalFlat(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "<unmarshalable>"
	}
	return string(raw)
}
