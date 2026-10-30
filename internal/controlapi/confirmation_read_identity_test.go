package controlapi_test

import (
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// readIdentityStaleHash is a well-formed 64-hex digest that belongs to no intent in
// this fixture. It is deliberately schema-valid so the refusal is attributed to Core's
// durable comparison rather than to input-shape validation.
const readIdentityStaleHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// exactReadTools share one read-identity validator: get, options and show must refuse
// or accept a supplied identity identically, so no surface can be used to bypass the
// frozen content digest.
var exactReadTools = []string{"confirmation_get", "confirmation_options_get", "confirmation_show"}

func readIdentityArguments(s *cardLifecycleStack, intent c.Intent) map[string]any {
	return map[string]any{
		"aiSessionId": s.fixture.session, "intentId": intent.IntentID, "version": intent.Version,
		"purpose": string(intent.Purpose), "contentHash": intent.ContentHash,
	}
}

func withoutField(arguments map[string]any, key string) map[string]any {
	out := map[string]any{}
	for name, value := range arguments {
		if name != key {
			out[name] = value
		}
	}
	return out
}

func withContentHash(arguments map[string]any, hash string) map[string]any {
	out := withoutField(arguments, "contentHash")
	out["contentHash"] = hash
	return out
}

// readIdentityForeignPurpose returns a schema-valid purpose that is not this intent's,
// so the refusal is attributed to Core's identity comparison rather than to input shape.
func readIdentityForeignPurpose(purpose c.Purpose) string {
	for _, candidate := range []string{"PROJECT_SETUP", "FEATURE_AREA", "TASK", "REVIEW", "EXECUTION"} {
		if candidate != string(purpose) {
			return candidate
		}
	}
	return string(purpose)
}

// intentBlob is the entire durable row, so a byte comparison proves a refused read
// wrote nothing at all: not state, not a receipt, not a request ledger row.
func (s *cardLifecycleStack) intentBlob(intentID string) string {
	s.t.Helper()
	var blob string
	if err := s.fixture.plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, intentID).Scan(&blob); err != nil {
		s.t.Fatalf("read durable intent %s: %v", intentID, err)
	}
	return blob
}

// §7/§8: the public Confirmation read identity is
// (aiSessionId, intentId, version, purpose, contentHash). Every call below enters
// through the real Streamable HTTP MCP wire, the production middleware stack and the
// real SQLite store, so the requirement is proven at the public boundary instead of by
// a facade shortcut, and each refusal is shown to be a pure read.
func TestConfirmationExactReadIdentityRequiresFrozenContentHash(t *testing.T) {
	s := newCardLifecycleStack(t)
	// The replacement pair is prepared up front, never inside a probe: preparing a
	// successor supersedes its prior in the same transaction, and no read-only assertion
	// below may be attributed that write.
	prior, intent := s.prepareReplacement()
	blobBefore := s.intentBlob(intent.IntentID)
	intentsBefore := s.countRows(`SELECT COUNT(*) FROM confirmation_intents`)
	requestsBefore := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests`)
	exact := readIdentityArguments(s, intent)

	for _, tool := range exactReadTools {
		t.Run(tool+" exact identity is readable", func(t *testing.T) {
			result, envelope, err := s.rawCall(tool, exact)
			if err != nil || result.IsError {
				t.Fatalf("%s refused the exact identity: err=%v raw=%s", tool, err, jsonCard(envelope))
			}
			if envelope.Intent == nil && envelope.Options == nil {
				t.Fatalf("%s returned neither intent nor options for the exact identity: %s", tool, jsonCard(envelope))
			}
			if envelope.Failure != nil {
				t.Fatalf("%s exact identity must not carry a failure: %+v", tool, envelope.Failure)
			}
		})

		// A schema-valid digest that is not this intent's frozen content is refused by
		// Core with one typed code on every surface, and exposes no business facts.
		t.Run(tool+" stale contentHash is refused", func(t *testing.T) {
			result, envelope, err := s.rawCall(tool, withContentHash(exact, readIdentityStaleHash))
			if err != nil {
				t.Fatalf("%s transport: %v", tool, err)
			}
			if envelope.Intent != nil || envelope.Options != nil {
				t.Fatalf("%s leaked business facts for a stale hash: %s", tool, jsonCard(envelope))
			}
			if !result.IsError || envelope.Failure == nil {
				t.Fatalf("%s must return a typed error result: isError=%v raw=%s", tool, result.IsError, jsonCard(envelope))
			}
			failure := envelope.Failure
			if failure.Code != "CONFIRMATION_READ_IDENTITY_MISMATCH" || failure.Stage != "INPUT_IDENTITY" || failure.NextAction != c.ReadCurrentTarget || failure.Source != "CORE" {
				t.Fatalf("%s failure=%+v", tool, failure)
			}
			if cardResourceURI(result) != "" || metaString(result, "emission") == "CONFIRMATION" {
				t.Fatalf("%s mounted a Card for a refused read: meta=%+v", tool, result.Meta)
			}
		})

		t.Run(tool+" lineage contentHash is refused", func(t *testing.T) {
			if prior.Content.SourceIntentID != "" || intent.Content.SourceIntentID != prior.IntentID {
				t.Fatalf("lineage is carried only by sourceIntentId: prior=%q replacement=%q", prior.Content.SourceIntentID, intent.Content.SourceIntentID)
			}
			if intent.ContentHash == prior.ContentHash {
				t.Fatal("a replacement intent must freeze its own content digest")
			}
			if intent.Version != 1 || prior.Version != 1 {
				t.Fatalf("a replacement is a new intentId at version 1, got prior=%d replacement=%d", prior.Version, intent.Version)
			}
			// The successor's id paired with the prior intent's digest is a read miss.
			_, envelope, err := s.rawCall(tool, withContentHash(exact, prior.ContentHash))
			if err != nil {
				t.Fatalf("%s transport: %v", tool, err)
			}
			if envelope.Intent != nil || envelope.Options != nil {
				t.Fatalf("%s read a successor through the prior digest: %s", tool, jsonCard(envelope))
			}
			if envelope.Failure == nil || envelope.Failure.Code != "CONFIRMATION_READ_IDENTITY_MISMATCH" {
				t.Fatalf("%s prior-digest failure=%+v raw=%s", tool, envelope.Failure, jsonCard(envelope))
			}
			// The prior intent stays addressable only through its own exact identity, and
			// even then never resolves to the successor's content.
			_, own, err := s.rawCall(tool, readIdentityArguments(s, prior))
			if err != nil {
				t.Fatalf("%s transport: %v", tool, err)
			}
			if own.Intent != nil && (own.Intent.IntentID != prior.IntentID || own.Intent.ContentHash != prior.ContentHash) {
				t.Fatalf("%s resolved foreign content through the prior identity: %s", tool, jsonCard(own))
			}
		})

		// The identity is the whole tuple, so an otherwise schema-valid read that names
		// another version or another purpose of this intentId is the same pure miss:
		// intentId + contentHash never address a read without the version and purpose
		// they were frozen under.
		for _, field := range []struct {
			name  string
			value any
		}{{"version", intent.Version + 1}, {"purpose", readIdentityForeignPurpose(intent.Purpose)}} {
			t.Run(tool+" off-identity "+field.name+" is refused", func(t *testing.T) {
				arguments := withoutField(exact, field.name)
				arguments[field.name] = field.value
				result, envelope, err := s.rawCall(tool, arguments)
				if err != nil {
					t.Fatalf("%s transport: %v", tool, err)
				}
				if envelope.Intent != nil || envelope.Options != nil {
					t.Fatalf("%s resolved content through a foreign %s: %s", tool, field.name, jsonCard(envelope))
				}
				if !result.IsError || envelope.Failure == nil {
					t.Fatalf("%s accepted a foreign %s: isError=%v raw=%s", tool, field.name, result.IsError, jsonCard(envelope))
				}
				if envelope.Failure.Code != "CONFIRMATION_READ_IDENTITY_MISMATCH" || envelope.Failure.Stage != "INPUT_IDENTITY" {
					t.Fatalf("%s foreign %s failure=%+v", tool, field.name, envelope.Failure)
				}
				if cardResourceURI(result) != "" || metaString(result, "emission") == "CONFIRMATION" {
					t.Fatalf("%s mounted a Card for a foreign %s: meta=%+v", tool, field.name, result.Meta)
				}
			})
		}

		// Missing, empty and malformed digests are refused at the public boundary before
		// Core ever resolves an intent: no read, no Card, no capability, no commit
		// authority (§8 forbids a hash-free lookup that later gains write rights).
		for _, shape := range []struct {
			name string
			hash any
		}{{"absent", nil}, {"empty", ""}, {"malformed", "abc"}} {
			t.Run(tool+" "+shape.name+" contentHash has no read authority", func(t *testing.T) {
				arguments := withoutField(exact, "contentHash")
				if shape.name != "absent" {
					arguments["contentHash"] = shape.hash
				}
				result, envelope, err := s.rawCall(tool, arguments)
				if err != nil {
					// A transport-level input rejection is the strongest outcome: the call
					// never reached Core at all.
					return
				}
				if envelope.Intent != nil || envelope.Options != nil {
					t.Fatalf("%s exposed facts for a %s contentHash: %s", tool, shape.name, jsonCard(envelope))
				}
				if !result.IsError {
					t.Fatalf("%s accepted a %s contentHash: %s", tool, shape.name, jsonCard(envelope))
				}
				if envelope.Failure != nil && envelope.Failure.Code == "" {
					t.Fatalf("%s returned an untyped failure: %+v", tool, envelope.Failure)
				}
				if cardResourceURI(result) != "" || metaString(result, "emission") == "CONFIRMATION" {
					t.Fatalf("%s mounted a Card without a contentHash: meta=%+v", tool, result.Meta)
				}
			})
		}
	}

	// Every probe above is a pure read: the durable row is byte-identical and the request
	// ledger gained no row.
	if after := s.intentBlob(intent.IntentID); after != blobBefore {
		t.Fatalf("a refused read mutated the durable intent:\nbefore=%s\nafter=%s", blobBefore, after)
	}
	if count := s.countRows(`SELECT COUNT(*) FROM confirmation_intents`); count != intentsBefore {
		t.Fatalf("a read-only probe changed the intent population: %d -> %d", intentsBefore, count)
	}
	if count := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests`); count != requestsBefore {
		t.Fatalf("a read-only probe grew the request ledger: %d -> %d", requestsBefore, count)
	}
}

// §7: a read after a lost response still uses the original exact identity. The receipt
// pointer (requestId) is additive; it never replaces the frozen content digest.
func TestConfirmationReceiptReadbackKeepsExactReadIdentity(t *testing.T) {
	s := newCardLifecycleStack(t)
	intent := s.prepareReviewDecision(s.nextID("prepare-receipt-identity"), "")
	requestID := s.nextID("receipt-identity-commit")
	_, committed := s.commit(intent, requestID)
	if committed.Intent == nil || committed.Intent.Submission == nil || !committed.Intent.Submission.Recorded {
		t.Fatalf("commit produced no recorded receipt: %s", jsonCard(committed))
	}
	payloadSHA256 := committed.Intent.Submission.PayloadSHA256
	if payloadSHA256 == "" {
		t.Fatal("a recorded receipt has no payload digest")
	}

	exact := readIdentityArguments(s, intent)
	exact["requestId"] = requestID
	_, receipt, err := s.rawCall("confirmation_get", exact)
	if err != nil {
		t.Fatalf("exact receipt readback: %v", err)
	}
	if receipt.Intent == nil || receipt.Intent.Submission == nil || receipt.Intent.Submission.PayloadSHA256 != payloadSHA256 {
		t.Fatalf("exact receipt readback lost the original payload: %s", jsonCard(receipt))
	}

	// The same requestId with a stale digest is a read-identity miss, never a receipt and
	// never a licence to resubmit the original request.
	_, stale, err := s.rawCall("confirmation_get", withContentHash(exact, readIdentityStaleHash))
	if err != nil {
		t.Fatalf("stale receipt readback: %v", err)
	}
	if stale.Intent != nil {
		t.Fatalf("a stale digest returned an intent: %s", jsonCard(stale))
	}
	if stale.Failure == nil || stale.Failure.Code != "CONFIRMATION_READ_IDENTITY_MISMATCH" {
		t.Fatalf("stale receipt failure=%+v raw=%s", stale.Failure, jsonCard(stale))
	}
	if requests := s.countRows(`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, intent.IntentID); requests != 1 {
		t.Fatalf("readback added request ledger rows: %d", requests)
	}
}
