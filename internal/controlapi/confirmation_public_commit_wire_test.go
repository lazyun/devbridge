package controlapi_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// §12/§13: the submission entry is one public, model-discoverable MCP tool that the
// Card and an explicit text instruction both call. These gates enter through the real
// Streamable HTTP session the lifecycle stack already runs, so discovery, the
// advertised closed schemas, the shared output envelope and the one Core handler are
// all read from the wire rather than restated from Go builders.

type wireTool struct {
	name       string
	visibility []string
	input      map[string]any
	output     map[string]any
	rendered   bool
}

func wireSchema(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func wireVisibility(tool *sdkmcp.Tool) []string {
	ui, _ := tool.Meta["ui"].(map[string]any)
	switch values := ui["visibility"].(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if item, ok := value.(string); ok {
				out = append(out, item)
			}
		}
		return out
	}
	return []string{"model", "app"}
}

// toolCatalog is what a Host actually receives from tools/list on this session.
func (s *cardLifecycleStack) toolCatalog() map[string]wireTool {
	s.t.Helper()
	list, err := s.session.ListTools(s.ctx, nil)
	if err != nil {
		s.t.Fatalf("tools/list: %v", err)
	}
	catalog := make(map[string]wireTool, len(list.Tools))
	for _, tool := range list.Tools {
		ui, _ := tool.Meta["ui"].(map[string]any)
		catalog[tool.Name] = wireTool{
			name: tool.Name, visibility: wireVisibility(tool),
			input: wireSchema(tool.InputSchema), output: wireSchema(tool.OutputSchema),
			rendered: ui["resourceUri"] != nil,
		}
	}
	return catalog
}

func wireRequires(schema map[string]any, field string) bool {
	list, _ := schema["required"].([]any)
	for _, item := range list {
		if item == field {
			return true
		}
	}
	return false
}

func (s *cardLifecycleStack) mustHave(t *testing.T, catalog map[string]wireTool, name string) wireTool {
	t.Helper()
	tool, ok := catalog[name]
	if !ok {
		t.Fatalf("%s is not discoverable on the live MCP catalog", name)
	}
	return tool
}

// G14: one public submission entry, discoverable to the model, schema-closed, sharing
// the Confirmation output envelope with every other confirmation surface.
func TestPublicConfirmationCommitIsTheOnlyModelDiscoverableSubmissionEntry(t *testing.T) {
	s := newCardLifecycleStack(t)
	catalog := s.toolCatalog()

	commit := s.mustHave(t, catalog, "confirmation_commit")
	reprepare := s.mustHave(t, catalog, "confirmation_reprepare")
	for _, tool := range []wireTool{commit, reprepare} {
		if !slices.Contains(tool.visibility, "model") {
			t.Fatalf("%s visibility=%v, want model-discoverable so text and Card reach one entry", tool.name, tool.visibility)
		}
		if tool.rendered {
			t.Fatalf("%s advertises a Card resource; a submission or replacement prepare is not a presentation", tool.name)
		}
		if tool.input["additionalProperties"] != false {
			t.Fatalf("%s input schema is open, so a caller could inject Core-derived authority", tool.name)
		}
	}
	if slices.Contains(reprepare.visibility, "app") {
		t.Fatalf("confirmation_reprepare visibility=%v; only the public commit entry is app-shared", reprepare.visibility)
	}

	// App-only retirement tools do not add model submission authority; only one entry can
	// submit a decision.
	names := []string{}
	for name := range catalog {
		if strings.HasPrefix(name, "confirmation_") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if want := []string{"confirmation_commit", "confirmation_discard", "confirmation_get", "confirmation_options_get", "confirmation_reprepare", "confirmation_show", "confirmation_submission_withdraw"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("confirmation catalog=%v, want %v", names, want)
	}
	// A submission-capable entry is identified by what it demands: the frozen content
	// digest, the variant the user chose and the request identity. Any second tool
	// requiring that trio would be a competing submission authority.
	submitters := []string{}
	for name, tool := range catalog {
		if slices.Contains(tool.visibility, "model") && strings.HasPrefix(name, "confirmation_") && wireRequires(tool.input, "intentId") &&
			wireRequires(tool.input, "contentHash") && wireRequires(tool.input, "requestId") && wireRequires(tool.input, "variantId") {
			submitters = append(submitters, name)
		}
	}
	sort.Strings(submitters)
	if !reflect.DeepEqual(submitters, []string{"confirmation_commit"}) {
		t.Fatalf("submission-capable confirmation entries=%v, want only confirmation_commit", submitters)
	}

	// §12 envelope parity: switching from the Card to the text path never meets a
	// second result shape.
	for name, tool := range catalog {
		if !strings.HasPrefix(name, "confirmation_") {
			continue
		}
		if !reflect.DeepEqual(tool.output, commit.output) {
			t.Fatalf("%s advertises a different result envelope than confirmation_commit", name)
		}
	}
	// The exact read identity and the commit identity are the same frozen fields, and
	// only the submission entry demands the request and variant the user chose.
	for _, field := range []string{"aiSessionId", "intentId", "version", "purpose", "contentHash"} {
		if !wireRequires(commit.input, field) || !wireRequires(s.mustHave(t, catalog, "confirmation_get").input, field) {
			t.Fatalf("%s is no longer required by both the commit entry and the exact read", field)
		}
	}
	for _, field := range []string{"requestId", "variantId", "selection"} {
		if !wireRequires(commit.input, field) {
			t.Fatalf("confirmation_commit no longer requires %s, so it could be called without a user decision", field)
		}
	}
	// The one app-private tool left is the authoritative Card read, not a submission.
	if card := s.mustHave(t, catalog, "dev_bridge_card_get"); !reflect.DeepEqual(card.visibility, []string{"app"}) {
		t.Fatalf("dev_bridge_card_get visibility=%v, want app-only", card.visibility)
	}
}

// G15: the Card-shaped command and the text-Continue-shaped command run through the
// same registered handler and leave the same durable facts.
func TestCardShapedAndTextShapedConfirmationCommitRunOnOneHandler(t *testing.T) {
	s := newCardLifecycleStack(t)
	intent := s.prepareReviewDecision(s.nextID("prepare-one-handler"), "")
	reviewsBefore := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task)

	showResult, show := s.show(intent, "")
	requireMountedCard(t, showResult, *show.Intent)
	_, optionRead := s.options(intent)
	if optionRead.Options == nil || !optionRead.Options.Eligibility.Available {
		t.Fatalf("the prepared intent is not committable: %s", jsonCard(optionRead.Options))
	}

	// The text Continue command: §13's exact five-field read first.
	requestID := s.nextID("one-handler-commit")
	_, before := s.read(intent, "")
	if before.Intent == nil || before.Intent.State != "PENDING" || before.Intent.Submission != nil {
		t.Fatalf("§13 requires a PENDING intent with no recorded submission before a commit: %s", jsonCard(before))
	}

	// The command body is the shape the Card sends over the Host bridge: the frozen
	// identity, one new request identity, the chosen variant and a structured
	// selection - nothing else.
	commitResult, committed := s.commit(intent, requestID)
	if commitResult.IsError || committed.Intent == nil || committed.Intent.State != "COMPLETED" {
		t.Fatalf("the shared commit entry refused a confirmed intent: %s", s.lastRaw)
	}
	if committed.Intent.IntentID != intent.IntentID || committed.Intent.ContentHash != intent.ContentHash {
		t.Fatalf("the commit entry completed a different intent: %s", jsonCard(committed.Intent))
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore+1 {
		t.Fatalf("one commit produced %d successor Reviews over the baseline %d", reviews-reviewsBefore, reviewsBefore)
	}
	successor := s.scalar(`SELECT id FROM reviews WHERE task_id=? AND id<>?`, s.fixture.task, s.fixture.review)
	if starts := s.countRows(`SELECT COUNT(*) FROM feature_area_review_start_requests WHERE review_id=? AND outcome='ACCEPTED'`, successor); starts != 1 {
		t.Fatalf("successor start authority rows=%d, want exactly one", starts)
	}

	// The text path then reads the same request back: identical durable submission
	// facts, so neither surface restates an outcome the other invented.
	_, receipt := s.read(intent, requestID)
	if receipt.Intent == nil || receipt.Intent.Submission == nil {
		t.Fatalf("exact readback after the shared commit lost the submission: %s", s.lastRaw)
	}
	if receipt.Intent.State != committed.Intent.State || receipt.Intent.Submission.Outcome != committed.Intent.Submission.Outcome {
		t.Fatalf("readback drifted from the commit envelope: read=%s commit=%s", jsonCard(receipt.Intent.Submission), jsonCard(committed.Intent.Submission))
	}
	if receipt.Intent.Submission.Request.RequestID != requestID || !receipt.Intent.Submission.Recorded {
		t.Fatalf("readback names another request: %s", jsonCard(receipt.Intent.Submission))
	}
	if receipt.Intent.Submission.Consumed == nil || !*receipt.Intent.Submission.Consumed {
		t.Fatalf("a completed submission was read back as unconsumed: %s", jsonCard(receipt.Intent.Submission))
	}
	if _, again := s.options(intent); again.Options != nil && again.Options.Eligibility.Available {
		t.Fatalf("the consumed intent is still offered for submission: %s", jsonCard(again.Options))
	}

	// §12 closed DTO over the real wire: every derived authority a caller might steer
	// with is refused, and the refusal changes nothing.
	for _, injected := range []struct {
		field string
		value any
	}{
		{"outcome", string(c.Consumed)},
		{"consumed", true},
		{"retryDisposition", string(c.RetryEditSelectionAndResubmit)},
		{"reprepareAllowed", true},
		{"decisionVersion", strings.Repeat("c", 64)},
		{"successorReviewId", "review-forged"},
		{"force", true},
		{"assumeNotConsumed", true},
		{"retryAnyway", true},
	} {
		arguments := map[string]any{
			"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version,
			"purpose": string(intent.Purpose), "action": intent.Action, "requestId": s.nextID("injected"),
			"contentHash": intent.ContentHash, "variantId": "revise", "selection": map[string]any{},
			injected.field: injected.value,
		}
		result, envelope, err := s.rawCall("confirmation_commit", arguments)
		if err == nil && !result.IsError && (envelope.Failure == nil || envelope.Intent == nil) {
			t.Fatalf("the public commit entry accepted the derived fact %s=%v: %s", injected.field, injected.value, jsonCard(envelope))
		}
		if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore+1 {
			t.Fatalf("the %s injection changed the successor count: %d", injected.field, reviews)
		}
	}
}

// G16: a recorded typed rejection stays a definite result through the exact read-back,
// and a request Core never observed is refused instead of being read as proof that
// nothing was consumed.
func TestTypedRejectionReadbackNeverDegradesToUnknown(t *testing.T) {
	s := newCardLifecycleStack(t)
	intent := s.prepareReviewDecision(s.nextID("prepare-typed-rejection"), "")
	requestID := s.nextID("typed-rejection-commit")
	commitResult, rejected := s.rejectedRetryCommit(intent, requestID)
	if !commitResult.IsError || rejected.Failure == nil || rejected.Intent == nil || rejected.Intent.Submission == nil {
		t.Fatalf("expected a durable typed rejection: %s", s.lastRaw)
	}
	if rejected.Intent.Submission.RetryDisposition != c.RetryEditSelectionAndResubmit || !rejected.Intent.Submission.Recorded {
		t.Fatalf("rejection disposition=%s", jsonCard(rejected.Intent.Submission))
	}

	// §13: the text path reads the original requestId back and must see the same
	// definite rejection, never an unknown outcome.
	_, receipt := s.read(intent, requestID)
	if receipt.Intent == nil || receipt.Intent.Submission == nil {
		t.Fatalf("exact rejection read-back lost the record: %s", s.lastRaw)
	}
	if receipt.Intent.Submission.Outcome != c.RejectedNotConsumed || !receipt.Intent.Submission.Recorded ||
		receipt.Intent.Submission.Consumed == nil || *receipt.Intent.Submission.Consumed ||
		receipt.Intent.Submission.NextAction != c.EditSelection {
		t.Fatalf("rejection read-back degraded: %s", jsonCard(receipt.Intent.Submission))
	}
	if receipt.Intent.Submission.RetryDisposition != c.RetryEditSelectionAndResubmit {
		t.Fatalf("rejection read-back changed the retry path: %s", jsonCard(receipt.Intent.Submission))
	}
	if raw := jsonCard(receipt); strings.Contains(raw, string(c.Unknown)) || strings.Contains(raw, string(c.ReadOriginalReceipt)) {
		t.Fatalf("a definite rejection was restated as an unproven outcome: %s", raw)
	}

	// A request Core never saw is a read miss, never evidence about consumption. The
	// surface may refuse it outright or report it as explicitly unrecorded, but it
	// cannot declare the write absent - absence of a receipt is not non-consumption.
	_, unobserved := s.read(intent, "confirm-never-submitted-"+s.nextID("unobserved"))
	switch {
	case unobserved.Failure != nil:
		if unobserved.Failure.Code != "CONFIRMATION_REQUEST_NOT_OBSERVED" {
			t.Fatalf("an invented requestId failed with %q: %s", unobserved.Failure.Code, s.lastRaw)
		}
		if unobserved.Failure.NextAction == c.EditSelection || unobserved.Failure.NextAction == c.PrepareNewVersion {
			t.Fatalf("a missing receipt was presented as a retry instruction: %+v", unobserved.Failure)
		}
		if unobserved.Intent != nil && unobserved.Intent.Submission != nil {
			t.Fatalf("a refused requestId still surfaced a consumption claim: %s", jsonCard(unobserved.Intent.Submission))
		}
	case unobserved.Intent != nil && unobserved.Intent.Submission != nil:
		miss := unobserved.Intent.Submission
		if miss.Recorded || miss.Outcome != c.Unknown || miss.Consumed != nil {
			t.Fatalf("an unobserved requestId claimed a consumption disposition: %s", jsonCard(miss))
		}
		if miss.PayloadSHA256 != "" || miss.ReceiptRequestID != "" || miss.BusinessOutcome != "OUTCOME_UNKNOWN" {
			t.Fatalf("an unobserved requestId surfaced another request's receipt identity: %s", jsonCard(miss))
		}
		if miss.NextAction != c.ReadOriginalReceipt || miss.RetryDisposition != c.RetryReadOriginalReceipt || miss.ReprepareAllowed {
			t.Fatalf("a missing receipt was offered as a retry or a replacement: %s", jsonCard(miss))
		}
	default:
		t.Fatalf("the unobserved requestId stated no outcome at all: %s", s.lastRaw)
	}
	// The read miss is pure: the durable rejection and the absence of effects stand.
	_, afterMiss := s.read(intent, requestID)
	if afterMiss.Intent == nil || afterMiss.Intent.Submission == nil || afterMiss.Intent.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("reading an invented request disturbed the recorded rejection: %s", s.lastRaw)
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, intent.IntentID); rows != 1 {
		t.Fatalf("the unobserved read recorded a request row: %d", rows)
	}
}

// reprepareSourceOnTheWire drives the public surfaces only: it prepares a Review
// decision confirmation, moves the Project topology underneath it, and submits it, so
// Core answers with the terminal REPREPARE_REQUIRED rejection §10 replaces.
func (s *cardLifecycleStack) reprepareSourceOnTheWire(alias string) (c.Intent, string) {
	s.t.Helper()
	intent := s.prepareReviewDecision(s.nextID("prepare-reprepare-source"), "")
	requestID := s.nextID("reprepare-source-commit")
	s.fixture.appendTopologyMember(s.t, alias)
	commitResult, rejected := s.commit(intent, requestID)
	if !commitResult.IsError || rejected.Failure == nil || rejected.Intent == nil || rejected.Intent.Submission == nil {
		s.t.Fatalf("expected a terminal rejection over the wire: %s", s.lastRaw)
	}
	if rejected.Failure.Code != "PROJECT_TOPOLOGY_CHANGED" || rejected.Failure.NextAction != c.PrepareNewVersion {
		s.t.Fatalf("rejection failure=%+v", rejected.Failure)
	}
	submission := rejected.Intent.Submission
	if !submission.Recorded || submission.Outcome != c.RejectedNotConsumed || submission.Consumed == nil || *submission.Consumed ||
		submission.RetryDisposition != c.RetryReprepareRequired || !submission.ReprepareAllowed || submission.RedispatchSameIntent {
		s.t.Fatalf("rejection receipt=%s", jsonCard(submission))
	}
	return intent, requestID
}

// reprepareOnTheWire is the client surface §10 allows: the source identity, one fresh
// request identity and optional feedback. Nothing else is a caller's to supply.
func (s *cardLifecycleStack) reprepareOnTheWire(source c.Intent, requestID, feedback string) (*sdkmcp.CallToolResult, c.Result) {
	s.t.Helper()
	arguments := map[string]any{
		"aiSessionId": s.fixture.session, "sourceIntentId": source.IntentID,
		"sourceVersion": source.Version, "sourceContentHash": source.ContentHash, "requestId": requestID,
	}
	if feedback != "" {
		arguments["reviewFeedback"] = feedback
	}
	return s.call("confirmation_reprepare", arguments)
}

// G17: the text Continue route over a REPREPARE_REQUIRED rejection reports the typed
// rejection, commits nothing, and reaches the replacement through the one public
// reprepare entry - which replays exactly and refuses a changed payload.
func TestTextContinueOverAReprepareRequiredRejectionUsesThePublicReplacementEntry(t *testing.T) {
	s := newCardLifecycleStack(t)
	source, rejectedRequestID := s.reprepareSourceOnTheWire("wire-reprepare")
	reviewsBefore := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task)
	intentsBefore := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE ai_session_id=?`, s.fixture.session)

	// §13 step one is always the exact read; it already names the replacement action.
	_, readBack := s.read(source, rejectedRequestID)
	if readBack.Intent == nil || readBack.Intent.Submission == nil ||
		readBack.Intent.Submission.RetryDisposition != c.RetryReprepareRequired ||
		readBack.Intent.Submission.NextAction != c.PrepareNewVersion {
		t.Fatalf("read-back did not name the replacement prepare: %s", s.lastRaw)
	}
	if raw := jsonCard(readBack); strings.Contains(raw, string(c.Unknown)) || strings.Contains(raw, `"reprepareAllowed":false`) {
		t.Fatalf("a terminal rejection was restated as unproven or unreplaceable: %s", raw)
	}

	// §8 over the public options surface: nothing on this intent is committable.
	_, optionRead := s.options(source)
	if optionRead.Options == nil || optionRead.Options.Eligibility.Available ||
		optionRead.Options.Eligibility.AttemptState != "TERMINAL_REJECTED" ||
		optionRead.Options.Eligibility.RetryDisposition != string(c.RetryReprepareRequired) {
		t.Fatalf("options still offer the rejected intent: %s", jsonCard(optionRead.Options))
	}

	// A second user command on the rejected intent is refused and creates nothing: the
	// rejection is terminal for this intentId, and never becomes an edit-and-resubmit.
	secondCommit, secondEnvelope := s.commit(source, s.nextID("commit-rejected-again"))
	if !secondCommit.IsError || secondEnvelope.Failure == nil {
		t.Fatalf("a REPREPARE_REQUIRED rejection still accepted a fresh commit: %s", s.lastRaw)
	}
	if secondEnvelope.Failure.NextAction == c.EditSelection {
		t.Fatalf("a terminal rejection was offered back as an editable retry: %+v", secondEnvelope.Failure)
	}
	if secondEnvelope.Intent == nil || secondEnvelope.Intent.Submission == nil ||
		secondEnvelope.Intent.Submission.Outcome != c.RejectedNotConsumed ||
		secondEnvelope.Intent.Submission.RetryDisposition != c.RetryReprepareRequired {
		t.Fatalf("the refused retry left the intent's terminal rejection unreadable: %s", s.lastRaw)
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore {
		t.Fatalf("a refused commit created successors: %d -> %d", reviewsBefore, reviews)
	}

	// The formal replacement, through the same public catalog the Card shares.
	reprepareRequestID := s.nextID("reprepare")
	receiptsBefore := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, s.fixture.session)
	reprepareResult, replaced := s.reprepareOnTheWire(source, reprepareRequestID, "")
	if reprepareResult.IsError || replaced.Intent == nil {
		t.Fatalf("public reprepare failed: %s", s.lastRaw)
	}
	replacement := *replaced.Intent
	if replacement.IntentID == source.IntentID || replacement.Version != 1 ||
		replacement.Content.Review == nil || replacement.Content.SourceIntentID != source.IntentID ||
		replacement.ContentHash == source.ContentHash {
		t.Fatalf("replacement identity=%s version=%d lineage=%q", replacement.IntentID, replacement.Version, replacement.Content.SourceIntentID)
	}
	if replacement.State != "PENDING" || replacement.Submission != nil {
		t.Fatalf("the replacement carried submission state: %s", jsonCard(replacement))
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore {
		t.Fatalf("the replacement prepare created a successor Review: %d -> %d", reviewsBefore, reviews)
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE ai_session_id=?`, s.fixture.session); rows != receiptsBefore {
		t.Fatalf("the replacement prepare wrote submission receipts: %d -> %d", receiptsBefore, rows)
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE ai_session_id=?`, s.fixture.session); rows != intentsBefore+1 {
		t.Fatalf("one reprepare produced %d extra intents", rows-intentsBefore)
	}

	// A lost response re-opens the same request: the same replacement, never a fork.
	replayResult, replayed := s.reprepareOnTheWire(source, reprepareRequestID, "")
	if replayResult.IsError || replayed.Intent == nil || replayed.Intent.IntentID != replacement.IntentID ||
		replayed.Intent.ContentHash != replacement.ContentHash {
		t.Fatalf("exact reprepare replay diverged: %s", s.lastRaw)
	}
	conflictResult, conflict := s.reprepareOnTheWire(source, reprepareRequestID, "Narrower feedback that must not rewrite the frozen request.")
	if !conflictResult.IsError || conflict.Failure == nil || conflict.Failure.Code != "CONFIRMATION_REQUEST_CONFLICT" {
		t.Fatalf("changed payload under the same requestId: %s", s.lastRaw)
	}
	if rows := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE ai_session_id=?`, s.fixture.session); rows != intentsBefore+1 {
		t.Fatalf("a replayed or conflicting reprepare forked the replacement: %d", rows)
	}

	// The rejected source is now history: readable, superseded, never commandable.
	_, superseded := s.read(source, rejectedRequestID)
	if superseded.Intent == nil || superseded.Intent.State != "SUPERSEDED" ||
		superseded.Intent.Submission == nil || superseded.Intent.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("the replaced source lost its rejection history: %s", s.lastRaw)
	}

	// Only the replacement's own confirmed commit creates the successor.
	_, replacementRead := s.read(replacement, "")
	if replacementRead.Intent == nil || replacementRead.Intent.State != "PENDING" || replacementRead.Intent.Submission != nil {
		t.Fatalf("§13 pre-commit read rejected the replacement: %s", s.lastRaw)
	}
	commitResult, committed := s.commit(replacement, s.nextID("commit-replacement"))
	if commitResult.IsError || committed.Intent == nil || committed.Intent.State != "COMPLETED" {
		t.Fatalf("the replacement commit failed: %s", s.lastRaw)
	}
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore+1 {
		t.Fatalf("the replacement produced %d successor Reviews, want exactly one", reviews-reviewsBefore)
	}
	successor := s.scalar(`SELECT id FROM reviews WHERE task_id=? AND id<>?`, s.fixture.task, s.fixture.review)
	if current := s.optionalScalar(`SELECT current_review_id FROM tasks WHERE id=?`, s.fixture.task); current != successor {
		t.Fatalf("Task.currentReviewId=%q, want the single successor %q", current, successor)
	}
	if chain := s.countRows(`SELECT COUNT(*) FROM review_re_review_requests WHERE previous_review_id=?`, s.fixture.review); chain != 1 {
		t.Fatalf("successor re-review chain rows=%d, want exactly one", chain)
	}
}
