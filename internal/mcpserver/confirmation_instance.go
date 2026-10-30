package mcpserver

import (
	"sync"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// This file enforces the §4 "one intent, one Card instance" invariant on the Host
// mount side of confirmation_show. A Card instance is permanently bound to the full
// frozen identity (aiSessionId, intentId, version, contentHash). The constant static
// resource URI is a cache key for the shared HTML/JS only and can NEVER be a Card
// business-instance key (§5), so the presentation meta additionally carries a
// Core-derived, content-free presentationInstanceId that a per-instance-aware Host
// uses to spin up an independent Card for a new intent rather than pushing a second
// tool-result into the already-mounted instance.

// confirmationPresentationInstanceId derives the per-instance id from the frozen
// identity via the shared canonical hash helper (c.ValueHash). Semantically
// equivalent to H(aiSessionId, intentId, version, contentHash); deterministic and
// stable across shows for the same intent, distinct for a replacement intent. It
// embeds no Task/Review/Decision/purpose/sourceIntentId, so a Host that keys card
// instances on it cannot collide two intents that share every business object.
func confirmationPresentationInstanceId(intent c.Intent) string {
	return c.ValueHash([]any{intent.AISessionID, intent.IntentID, intent.Version, intent.ContentHash})
}

// confirmationInstanceFailure returns a typed, content-free mount-gate error. The
// specific reason is carried in Failure.Code (never echoing intent ids, hashes or
// user content) so callers can branch on it; the message is fixed safe text.
func confirmationInstanceFailure(code string) *c.Failure {
	failure := c.NewFailure(
		code,
		"CONFIRMATION",
		"HOST_MOUNT",
		"The confirmation Card instance identity could not be safely mounted.",
		c.ReadCurrentTarget,
		nil,
	)
	failure.ReasonCode = code
	return failure
}

const (
	cardInstanceIdentityMismatch = "CONFIRMATION_CARD_INSTANCE_IDENTITY_MISMATCH"
	cardInstanceReuseRejected    = "CONFIRMATION_CARD_INSTANCE_REUSE_REJECTED"
	cardInstanceNewUnavailable   = "CONFIRMATION_NEW_CARD_INSTANCE_UNAVAILABLE"
)

// ValidateConfirmationCardInstanceIdentity is the §8 pre-mount assertion. The Card
// entry about to be attached must carry a complete frozen identity and must match
// the exact identity the caller asked confirmation_show to render. It is deliberately
// the last gate before the Card resource/meta is attached: on any failure the caller
// must NOT attach a Card entry, must NOT display PENDING, and must NOT claim the new
// confirmation was shown.
func ValidateConfirmationCardInstanceIdentity(result c.Result, requested c.ReadInput) *c.Failure {
	intent := result.Intent
	if intent == nil {
		// A no-intent envelope is a business/failure result; the entry/options gates
		// own it. There is no Card instance to bind, so identity is vacuously fine.
		return nil
	}
	// Fail closed if the frozen identity is not complete: without it there is no
	// verified per-instance key, and reusing the shared URI instance would silently
	// show this intent inside another intent's Card.
	if intent.AISessionID == "" || intent.IntentID == "" || intent.Version < 1 || intent.ContentHash == "" {
		return confirmationInstanceFailure(cardInstanceNewUnavailable)
	}
	// The frozen identity is intentId + version + contentHash; a requested hash that
	// differs from the durable intent is not this Card's instance, even when the two
	// ids match. Core already refuses such a read, so this is the last gate, not the
	// only one, and it stays inside the mount-gate reason codes (§7/§8).
	if intent.IntentID != requested.IntentID || intent.Version != requested.Version || intent.AISessionID != requested.AISessionID || intent.ContentHash != requested.ContentHash {
		return confirmationInstanceFailure(cardInstanceIdentityMismatch)
	}
	return nil
}

// boundConfirmationInstance records the single frozen identity a confirmation instance
// handle (aiSessionId + presentationRequestId) has been mounted for. A second, different
// intent arriving under the same handle is a Host/client trying to reuse one Card widget
// for another intent — refused per §8; the Host must request its own instance instead.
type boundConfirmationInstance struct {
	intentID               string
	version                int
	contentHash            string
	presentationInstanceID string
}

// confirmationInstanceRegistry is a process-local guard against cross-intent Card
// instance reuse. It makes no host-mounting guarantee on its own (mirroring the
// presentation leases); it only rejects a same-handle, different-intent show so the
// server never advertises a fresh Card entry while the Host still renders a prior one.
type confirmationInstanceRegistry struct {
	mu        sync.Mutex
	instances map[string]boundConfirmationInstance
}

func newConfirmationInstanceRegistry() *confirmationInstanceRegistry {
	return &confirmationInstanceRegistry{instances: make(map[string]boundConfirmationInstance)}
}

func confirmationInstanceKey(aiSessionID, requestID string) string {
	return aiSessionID + "\x00" + requestID
}

// checkOrBind records the single frozen identity a confirmation instance handle
// (aiSessionId + presentationRequestId) has been mounted for and rejects a different
// intent trying to reuse the same handle. It returns "" when the handle is free or is
// re-shown with the exact same identity (idempotent refresh), and a typed reason code
// when the identity is incomplete or a different intent reuses the handle. The server
// cannot observe the Host's iframe state; this only guards an explicit presentation
// handle. Forcing an independent instance is the presentationInstanceId's job (§4/§5).
func (r *confirmationInstanceRegistry) checkOrBind(requestID string, intent c.Intent) (code string) {
	if intent.IntentID == "" || intent.Version < 1 || intent.ContentHash == "" || intent.AISessionID == "" {
		return cardInstanceNewUnavailable
	}
	id := confirmationPresentationInstanceId(intent)
	next := boundConfirmationInstance{intentID: intent.IntentID, version: intent.Version, contentHash: intent.ContentHash, presentationInstanceID: id}
	key := confirmationInstanceKey(intent.AISessionID, requestID)
	r.mu.Lock()
	defer r.mu.Unlock()
	prior, ok := r.instances[key]
	if !ok {
		r.instances[key] = next
		return ""
	}
	if prior.intentID == next.intentID && prior.version == next.version && prior.contentHash == next.contentHash {
		return "" // same-intent refresh: keep the existing instance binding
	}
	return cardInstanceReuseRejected
}

// decorateConfirmationInstanceMeta attaches the frozen identity and the derived
// presentationInstanceId to a passing confirmation_show result meta. The static
// resourceUri/outputTemplate stay constant (shared cache); only these dynamic
// identity fields let the Host address this intent's own Card instance instead of
// pushing a second tool-result into an already-mounted one.
func decorateConfirmationInstanceMeta(meta sdkmcp.Meta, intent c.Intent) {
	if meta == nil {
		return
	}
	meta["devbridge/aiSessionId"] = intent.AISessionID
	meta["devbridge/intentId"] = intent.IntentID
	meta["devbridge/version"] = intent.Version
	meta["devbridge/contentHash"] = intent.ContentHash
	meta["devbridge/presentationInstanceId"] = confirmationPresentationInstanceId(intent)
}
