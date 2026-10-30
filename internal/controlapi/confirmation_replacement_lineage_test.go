package controlapi_test

import (
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// §10: a Confirmation intent is frozen at prepare, so a replacement is always a NEW
// intentId at version 1 rather than a revision of the prior row. This walks a
// three-generation A → B → C chain over the production wire and pins the facts a
// reader could otherwise infer wrongly from `version`: that it counts replacements,
// orders them, or lets an id/version pair address a successor's content.
//
// Nothing here commits an intent. Replacement is a prepare-level act, and the chain is
// proven without consuming a Decision, materializing a successor Review or touching any
// live object (§2).
func TestConfirmationReplacementLineageNeverIncrementsVersion(t *testing.T) {
	s := newCardLifecycleStack(t)
	reviewsBefore := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task)
	runsBefore := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task)

	first := s.prepareReviewDecision(s.nextID("lineage-prepare-a"), "")
	second := s.prepareReviewDecision(s.nextID("lineage-prepare-b"), first.IntentID)
	third := s.prepareReviewDecision(s.nextID("lineage-prepare-c"), second.IntentID)

	// Read every generation back from the durable store, not from the prepare echo, so
	// the assertions below describe what SQLite actually froze.
	generations := []c.Intent{first, second, third}
	for i := range generations {
		_, envelope := s.read(generations[i], "")
		if envelope.Intent == nil {
			t.Fatalf("generation %s is not readable: %s", generations[i].IntentID, jsonCard(envelope))
		}
		generations[i] = *envelope.Intent
	}
	first, second, third = generations[0], generations[1], generations[2]

	// Only the newest generation is live; each earlier one is history and carries no
	// inherited submission.
	if third.State != "PENDING" || second.State != "SUPERSEDED" || first.State != "SUPERSEDED" {
		t.Fatalf("replacement chain states: A=%s B=%s C=%s", first.State, second.State, third.State)
	}
	for _, intent := range generations {
		if intent.Version != 1 {
			t.Fatalf("intent %s carries version %d: a replacement is a new intentId at version 1, never an incremented revision", intent.IntentID, intent.Version)
		}
		if intent.Submission != nil {
			t.Fatalf("intent %s inherited a submission from its ancestor: %+v", intent.IntentID, intent.Submission)
		}
	}
	ids := map[string]bool{first.IntentID: true, second.IntentID: true, third.IntentID: true}
	if len(ids) != 3 {
		t.Fatalf("a replacement reused an intentId: %v", ids)
	}
	digests := map[string]string{first.IntentID: first.ContentHash, second.IntentID: second.ContentHash, third.IntentID: third.ContentHash}
	if first.ContentHash == second.ContentHash || second.ContentHash == third.ContentHash || first.ContentHash == third.ContentHash {
		t.Fatalf("generations share one content digest, so identity would collapse onto intentId/version: %v", digests)
	}

	// Lineage is expressed ONLY by sourceIntentId.
	if first.Content.SourceIntentID != "" {
		t.Fatalf("the root intent claims a predecessor %q", first.Content.SourceIntentID)
	}
	if second.Content.SourceIntentID != first.IntentID || third.Content.SourceIntentID != second.IntentID {
		t.Fatalf("replacement lineage broken: B=%q C=%q want B=%q C=%q", second.Content.SourceIntentID, third.Content.SourceIntentID, first.IntentID, second.IntentID)
	}
	// Every id/version pair is (intent, 1), so `version` cannot order the chain; walking
	// predecessors from the newest generation is the only ordering this contract offers.
	byIntentID := map[string]c.Intent{}
	for _, intent := range generations {
		byIntentID[intent.IntentID] = intent
	}
	seen := map[string]bool{}
	for cursor := third; ; {
		if seen[cursor.IntentID] {
			t.Fatalf("sourceIntentId chain cycles at %s", cursor.IntentID)
		}
		seen[cursor.IntentID] = true
		if cursor.Content.SourceIntentID == "" {
			break
		}
		parent, ok := byIntentID[cursor.Content.SourceIntentID]
		if !ok {
			t.Fatalf("predecessor %s of %s is outside this chain", cursor.Content.SourceIntentID, cursor.IntentID)
		}
		cursor = parent
	}
	if len(seen) != 3 {
		t.Fatalf("the sourceIntentId chain reaches %d intents, want 3", len(seen))
	}

	// Cross-generation identity: an id paired with any other generation's digest is a
	// read miss, so no reader can step along the chain by reusing a frozen hash (§7).
	for targetID, target := range byIntentID {
		for otherID, other := range byIntentID {
			if targetID == otherID || target.ContentHash == other.ContentHash {
				continue
			}
			arguments := map[string]any{
				"aiSessionId": s.fixture.session, "intentId": targetID, "version": 1,
				"purpose": string(c.Review), "contentHash": other.ContentHash,
			}
			result, envelope, err := s.rawCall("confirmation_get", arguments)
			if err != nil {
				t.Fatalf("cross-generation read: %v", err)
			}
			if envelope.Intent != nil {
				t.Fatalf("%s resolved through %s's digest: %s", targetID, otherID, jsonCard(envelope))
			}
			if !result.IsError || envelope.Failure == nil || envelope.Failure.Code != "CONFIRMATION_READ_IDENTITY_MISMATCH" {
				t.Fatalf("%s with a foreign digest: isError=%v failure=%+v", targetID, result.IsError, envelope.Failure)
			}
		}
	}

	// The durable rows agree with the public read: exactly three intents added by this
	// chain, all at version 1, each with its own digest, and no revision of an earlier
	// row. This is what rules out an in-place overwrite of A.
	if chain := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?,?)`, first.IntentID, second.IntentID, third.IntentID); chain != 3 {
		t.Fatalf("the chain holds %d intent rows, want exactly 3 immutable rows", chain)
	}
	if notOne := s.countRows(`SELECT COUNT(*) FROM confirmation_intents WHERE intent_id IN (?,?,?) AND json_extract(intent_json,'$.version') <> 1`, first.IntentID, second.IntentID, third.IntentID); notOne != 0 {
		t.Fatalf("%d chain intents are not durable version 1", notOne)
	}
	if distinct := s.countRows(`SELECT COUNT(DISTINCT json_extract(intent_json,'$.contentHash')) FROM confirmation_intents WHERE intent_id IN (?,?,?)`, first.IntentID, second.IntentID, third.IntentID); distinct != 3 {
		t.Fatalf("chain intents carry %d distinct durable digests, want 3", distinct)
	}
	for _, intent := range generations {
		if blob := s.intentBlob(intent.IntentID); !strings.Contains(blob, `"contentHash":"`+intent.ContentHash+`"`) {
			t.Fatalf("durable row for %s no longer carries its frozen digest:\n%s", intent.IntentID, blob)
		}
	}

	// Replacement is routing, not business progress: the Task still has its original
	// Review and no successor Run exists.
	if reviews := s.countRows(`SELECT COUNT(*) FROM reviews WHERE task_id=?`, s.fixture.task); reviews != reviewsBefore {
		t.Fatalf("replacement chain created Reviews: %d -> %d", reviewsBefore, reviews)
	}
	if runs := s.countRows(`SELECT COUNT(*) FROM runs WHERE task_id=?`, s.fixture.task); runs != runsBefore {
		t.Fatalf("replacement chain created Runs: %d -> %d", runsBefore, runs)
	}
}
