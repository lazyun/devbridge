package controlapi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// rejectedRetryCommit submits the frozen intent with a Coding Agent backend selection
// that this Review executor cannot accept. Core rejects it in preflight, before any
// canonical business command, and records the refusal as a durable
// REJECTED_NOT_CONSUMED receipt - the exact public bytes the Card receives.
func (s *cardLifecycleStack) rejectedRetryCommit(intent c.Intent, requestID string) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	return s.rejectedRetryCommitWithSelection(intent, requestID, map[string]any{
		"backend": map[string]any{"configId": "fixture-coding-backend", "configFingerprint": strings.Repeat("a", 64)},
	})
}

func (s *cardLifecycleStack) rejectedRetryCommitWithSelection(intent c.Intent, requestID string, selection map[string]any) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	return s.call("confirmation_commit", map[string]any{
		"aiSessionId": intent.AISessionID, "intentId": intent.IntentID, "version": intent.Version,
		"purpose": string(intent.Purpose), "action": intent.Action, "requestId": requestID,
		"contentHash": intent.ContentHash, "variantId": "revise", "selection": selection,
	})
}

// §3/§4/§5/§6: the only public state that returns a Confirmation Card to user editing.
// Every fact below comes from the production wire, so the Card-side unlock predicate is
// pinned against real rejection bytes rather than a hand-written fixture.
func TestConfirmationRejectedNotConsumedLeavesTheSameIntentEditable(t *testing.T) {
	s := newCardLifecycleStack(t)
	intent := s.prepareReviewDecision(s.nextID("prepare-rejected-retry"), "")
	contentBefore := s.scalar(`SELECT json_extract(intent_json,'$.content') FROM confirmation_intents WHERE intent_id=?`, intent.IntentID)
	reviewsBefore := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task)
	runsBefore := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task)

	pendingShowResult, pendingShow := s.show(intent, "")
	requireMountedCard(t, pendingShowResult, *pendingShow.Intent)
	pendingOptionsResult, pendingOptions := s.options(intent)
	if pendingOptions.Options == nil || !pendingOptions.Options.Eligibility.Available {
		t.Fatalf("the prepared intent is not committable before the rejection: %s", jsonCard(pendingOptions.Options))
	}

	requestID := s.nextID("rejected-retry-commit")
	commitResult, rejected := s.rejectedRetryCommit(intent, requestID)
	if !commitResult.IsError {
		t.Fatalf("an inapplicable backend selection was accepted: %s", s.lastRaw)
	}
	if rejected.Failure == nil || rejected.Failure.Code != "BACKEND_SELECTION_NOT_APPLICABLE" || rejected.Failure.NextAction != c.EditSelection {
		t.Fatalf("rejection failure=%+v", rejected.Failure)
	}
	if rejected.Intent == nil {
		t.Fatalf("the rejection returned no intent: %s", s.lastRaw)
	}
	submission := rejected.Intent.Submission
	if submission == nil {
		t.Fatalf("the rejection returned no durable submission: %s", jsonCard(rejected))
	}
	// §3: the full unlock conjunction, read from the public envelope.
	if !submission.Recorded || submission.Outcome != c.RejectedNotConsumed ||
		submission.BusinessOutcome != "REJECTED_NOT_CONSUMED" || submission.ConsumptionDisposition != "NOT_CONSUMED" ||
		submission.Consumed == nil || *submission.Consumed ||
		submission.NextAction != c.EditSelection || submission.RetryDisposition != c.RetryEditSelectionAndResubmit {
		t.Fatalf("rejection disposition=%s", jsonCard(submission))
	}
	if submission.RedispatchSameIntent {
		t.Fatal("a rejected submission offered same-intent redispatch (§3)")
	}
	// A not-consumed rejection has no consumption receipt pointer; its only identity is
	// the request it refused, which is what the Card must match before unlocking.
	if submission.ReceiptRequestID != "" {
		t.Fatalf("a REJECTED_NOT_CONSUMED submission carried receipt pointer %q", submission.ReceiptRequestID)
	}
	if submission.Request.RequestID != requestID {
		t.Fatalf("rejected request id=%q, want the submitted %q", submission.Request.RequestID, requestID)
	}
	if len(submission.PayloadSHA256) != 64 {
		t.Fatalf("rejected payload digest=%q", submission.PayloadSHA256)
	}
	if rejected.Intent.State != "PENDING" {
		t.Fatalf("the rejection left the intent in %s; only a still-pending intent can host an edit-and-resubmit", rejected.Intent.State)
	}
	if rejected.Intent.Submission.Request.ContentHash != intent.ContentHash {
		t.Fatalf("receipt names foreign content: %q", rejected.Intent.Submission.Request.ContentHash)
	}

	// The refusal is pre-effect: same frozen content, no Review, no Run, one request row.
	if contentAfter := s.scalar(`SELECT json_extract(intent_json,'$.content') FROM confirmation_intents WHERE intent_id=?`, intent.IntentID); contentAfter != contentBefore {
		t.Fatalf("a pre-consumption rejection rewrote frozen content:\nbefore=%s\nafter=%s", contentBefore, contentAfter)
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, intent.IntentID); rows != 1 {
		t.Fatalf("the rejection recorded %d request rows, want exactly its own", rows)
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore {
		t.Fatalf("a rejected submission created Reviews: %d -> %d", reviewsBefore, reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != runsBefore {
		t.Fatalf("a rejected submission created Runs: %d -> %d", runsBefore, runs)
	}

	// §7: the receipt is readable by its exact identity, and it still says the same
	// thing: not consumed, editable, no redispatch.
	receiptResult, receipt := s.read(intent, requestID)
	if receipt.Intent == nil || receipt.Intent.Submission == nil {
		t.Fatalf("exact receipt readback lost the rejection: %s", jsonCard(receipt))
	}
	if receipt.Intent.Submission.PayloadSHA256 != submission.PayloadSHA256 || receipt.Intent.Submission.RetryDisposition != c.RetryEditSelectionAndResubmit {
		t.Fatalf("receipt readback drifted: %s", jsonCard(receipt.Intent.Submission))
	}

	// Re-sending the ORIGINAL requestId returns only the original rejection receipt; it
	// never becomes a fresh attempt and records nothing new (§3).
	_, replay := s.rejectedRetryCommit(intent, requestID)
	if replay.Intent == nil || replay.Intent.Submission == nil || replay.Intent.Submission.PayloadSHA256 != submission.PayloadSHA256 {
		t.Fatalf("the original request id resolved to a different submission: %s", jsonCard(replay))
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, intent.IntentID); rows != 1 {
		t.Fatalf("replaying the original request id recorded %d rows", rows)
	}

	// Reusing the ORIGINAL requestId with a changed payload is a conflict, and a conflict
	// unlocks nothing and records nothing (§3: REQUEST_CONFLICT must not auto-clear state).
	conflictSelection := map[string]any{
		"backend":       map[string]any{"configId": "fixture-coding-backend", "configFingerprint": strings.Repeat("b", 64)},
		"runtimeConfig": map[string]any{"model": "fixture-model", "reasoningEffort": "medium", "codexFastMode": false},
	}
	conflictResult, conflict := s.rejectedRetryCommitWithSelection(intent, requestID, conflictSelection)
	if !conflictResult.IsError || conflict.Failure == nil || conflict.Failure.Code != "CONFIRMATION_REQUEST_CONFLICT" {
		t.Fatalf("changed payload under the original request id: isError=%v failure=%+v", conflictResult.IsError, conflict.Failure)
	}
	if conflict.Failure.NextAction == c.EditSelection {
		t.Fatalf("a request conflict was presented as an editable retry: %+v", conflict.Failure)
	}
	if conflict.Intent != nil && conflict.Intent.Submission != nil && conflict.Intent.Submission.PayloadSHA256 != submission.PayloadSHA256 {
		t.Fatalf("a request conflict replaced the recorded rejection: %s", jsonCard(conflict.Intent.Submission))
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, intent.IntentID); rows != 1 {
		t.Fatalf("the conflict path grew the request ledger: %d rows", rows)
	}

	// The intent itself is untouched by the rejection: a new user command over the same
	// frozen content remains possible, which is what the Card must re-arm.
	_, afterRejection := s.options(intent)
	if afterRejection.Options == nil || !afterRejection.Options.Eligibility.Available {
		t.Fatalf("the rejection made the intent non-committable: %s", jsonCard(afterRejection.Options))
	}

	captureRejectedRetryFixture(t, pendingShowResult, pendingOptionsResult, commitResult, receiptResult, intent, requestID, submission)
}

// captureRejectedRetryFixture writes the exact production envelopes behind the
// rejected-not-consumed retry contract to DEVBRIDGE_REJECTED_RETRY_FIXTURE_DIR for
// check-in as the shared Host/Card fixture. The Go guard and the Card JavaScript replay
// load the SAME file; a normal run writes nothing.
func captureRejectedRetryFixture(t *testing.T, pendingShow, pendingOptions, commitResult, receiptResult *sdkmcp.CallToolResult, intent c.Intent, requestID string, submission *c.Submission) {
	t.Helper()
	dir := os.Getenv("DEVBRIDGE_REJECTED_RETRY_FIXTURE_DIR")
	if dir == "" {
		return
	}
	file := map[string]any{
		"_meta": map[string]any{
			"schema":              "devbridge/confirmation-rejected-retry/v5",
			"singleSourceOfTruth": "Every envelope here is the exact production structuredContent of confirmation_show, confirmation_options_get, confirmation_commit and confirmation_get, captured over the real Streamable HTTP MCP wire, the production middleware stack and the real SQLite store by internal/controlapi/confirmation_rejected_retry_lifecycle_test.go. The Go guard and the Card JavaScript replay load this SAME file. Never hand-edit an envelope; re-capture with DEVBRIDGE_REJECTED_RETRY_FIXTURE_DIR.",
			"capturedBy":          "internal/controlapi/confirmation_rejected_retry_lifecycle_test.go",
			"staticResourceUri":   confirmationCardResourceURI,
			"rejectionTrigger":    "A Review REVISE decision intent submitted with a Coding Agent backend selection the executor cannot accept; Core refuses in CONFIRMATION_PREFLIGHT before any canonical business command and records the refusal.",
			"unlockPredicate":     "recorded && outcome==REJECTED_NOT_CONSUMED && businessOutcome==REJECTED_NOT_CONSUMED && consumptionDisposition==NOT_CONSUMED && consumed==false && nextAction==EDIT_SELECTION && retryDisposition==EDIT_SELECTION_AND_RESUBMIT && intent.state==PENDING && receipt identity matches the pending commit",
			"cardCopy":            "本次确认未被消费。请修改选择或重新确认。",
			"mainScriptSHA256":    metaString(pendingShow, "devbridge/mainScriptSHA256"),
			"componentVersion":    metaString(pendingShow, "devbridge/componentVersion"),
		},
		"pending": map[string]any{
			"entry":                  rawEnvelope(pendingShow),
			"options":                map[string]any{"entry": rawEnvelope(pendingOptions)},
			"intentId":               intent.IntentID,
			"version":                intent.Version,
			"contentHash":            intent.ContentHash,
			"state":                  intent.State,
			"presentationInstanceId": derivedInstanceID(intent),
			"meta": map[string]any{
				"resourceUri":            cardResourceURI(pendingShow),
				"outputTemplate":         outputTemplate(pendingShow),
				"aiSessionId":            metaString(pendingShow, "devbridge/aiSessionId"),
				"intentId":               metaString(pendingShow, "devbridge/intentId"),
				"version":                metaNumber(pendingShow, "devbridge/version"),
				"contentHash":            metaString(pendingShow, "devbridge/contentHash"),
				"presentationInstanceId": metaString(pendingShow, "devbridge/presentationInstanceId"),
				"componentVersion":       metaString(pendingShow, "devbridge/componentVersion"),
				"mainScriptSHA256":       metaString(pendingShow, "devbridge/mainScriptSHA256"),
			},
		},
		"rejection": map[string]any{
			"entry":       rawEnvelope(commitResult),
			"isError":     true,
			"requestId":   requestID,
			"facts":       submission,
			"failureCode": "BACKEND_SELECTION_NOT_APPLICABLE",
		},
		"receiptReadback": map[string]any{"entry": rawEnvelope(receiptResult)},
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "confirmation_rejected_retry_v5.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("captured shared rejected-not-consumed retry fixture at %s", path)
}
