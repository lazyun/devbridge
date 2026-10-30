package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Phase 3 §4/§8: one intent, one Card instance. A replacement intent must not be
// rendered inside a prior intent's mounted Card. These prove the server-side half:
// the frozen identity is carried per-show and a different intent can never silently
// reuse one instance handle.

const (
	oldIntentID    = "confirm_cb2d5174e6e96b3573f8addbff6172bc"
	oldContentHash = "cb2d5174e6e96b3573f8addbff6172bccb2d5174e6e96b3573f8addbff6172bc"
)

func supersededReviewIntent() c.Intent {
	intent := liveShapeReviewIntent()
	intent.IntentID = oldIntentID
	intent.ContentHash = oldContentHash
	intent.State = "SUPERSEDED"
	intent.Content.SourceIntentID = ""
	return intent
}

func replacementReviewIntent() c.Intent {
	intent := liveShapeReviewIntent()
	intent.Content.SourceIntentID = oldIntentID // lineage only
	return intent
}

func TestPresentationInstanceIdIsFrozenIdentityDerived(t *testing.T) {
	base := replacementReviewIntent()
	same := confirmationPresentationInstanceId(base)
	if same == "" {
		t.Fatal("presentation instance id must not be empty for a complete identity")
	}
	if again := confirmationPresentationInstanceId(base); again != same {
		t.Fatalf("derivation must be deterministic: %s != %s", again, same)
	}
	// Differs when any frozen-identity component changes.
	for name, mutate := range map[string]func(*c.Intent){
		"intentId":    func(i *c.Intent) { i.IntentID = "confirm_other" },
		"version":     func(i *c.Intent) { i.Version = 2 },
		"contentHash": func(i *c.Intent) { i.ContentHash = oldContentHash },
		"aiSessionId": func(i *c.Intent) { i.AISessionID = "session-2" },
	} {
		changed := base
		mutate(&changed)
		if confirmationPresentationInstanceId(changed) == same {
			t.Fatalf("presentation instance id must change when %s changes", name)
		}
	}
	// Purpose / Task / Review / Decision / sourceIntentId are NOT instance identity:
	// two intents sharing (aiSessionId,intentId,version,contentHash) derive the same id
	// even with different lineage or purpose.
	alt := base
	alt.Purpose = c.Execution
	alt.Content.Review = nil
	alt.Content.SourceIntentID = "confirm_unrelated_lineage"
	if confirmationPresentationInstanceId(alt) != same {
		t.Fatal("presentation instance id must depend only on the frozen identity")
	}
}

func TestValidateConfirmationCardInstanceIdentityGate(t *testing.T) {
	intent := replacementReviewIntent()
	match := c.ReadInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash}
	if failure := ValidateConfirmationCardInstanceIdentity(c.Result{Intent: &intent}, match); failure != nil {
		t.Fatalf("matching identity must pass, got %+v", failure)
	}
	// A nil-intent envelope (business/failure) is not an instance to bind.
	if failure := ValidateConfirmationCardInstanceIdentity(c.Result{}, match); failure != nil {
		t.Fatalf("nil intent must be vacuously valid, got %+v", failure)
	}
	// Cross-intent drift: entry identity does not equal the requested identity.
	drift := match
	drift.IntentID = oldIntentID
	failure := ValidateConfirmationCardInstanceIdentity(c.Result{Intent: &intent}, drift)
	if failure == nil || failure.Code != cardInstanceIdentityMismatch {
		t.Fatalf("expected %s, got %+v", cardInstanceIdentityMismatch, failure)
	}
	// A matching intentId/version pair with a stale content digest is still not this
	// Card's frozen instance (§7); the hash is identity, not a filter.
	hashDrift := match
	hashDrift.ContentHash = oldContentHash
	if failure := ValidateConfirmationCardInstanceIdentity(c.Result{Intent: &intent}, hashDrift); failure == nil || failure.Code != cardInstanceIdentityMismatch {
		t.Fatalf("contentHash drift must fail closed with %s, got %+v", cardInstanceIdentityMismatch, failure)
	}
	// Incomplete frozen identity fails closed rather than reusing the shared URI card.
	for name, mutate := range map[string]func(*c.Intent){
		"blank intentId":    func(i *c.Intent) { i.IntentID = "" },
		"zero version":      func(i *c.Intent) { i.Version = 0 },
		"blank contentHash": func(i *c.Intent) { i.ContentHash = "" },
		"blank session":     func(i *c.Intent) { i.AISessionID = "" },
	} {
		incomplete := intent
		mutate(&incomplete)
		failure := ValidateConfirmationCardInstanceIdentity(c.Result{Intent: &incomplete}, match)
		if failure == nil || failure.Code != cardInstanceNewUnavailable {
			t.Fatalf("incomplete identity (%s) must fail closed with %s, got %+v", name, cardInstanceNewUnavailable, failure)
		}
	}
}

func TestConfirmationInstanceRegistryReusesAreRejected(t *testing.T) {
	registry := newConfirmationInstanceRegistry()
	newer := replacementReviewIntent()
	older := supersededReviewIntent()
	if code := registry.checkOrBind("handle-1", older); code != "" {
		t.Fatalf("first bind must succeed, got %q", code)
	}
	if code := registry.checkOrBind("handle-1", older); code != "" {
		t.Fatalf("same-intent refresh must be idempotent, got %q", code)
	}
	// A different intent reusing the same handle must be refused; the prior binding
	// (the older intent) stays intact so a stale card is never overwritten.
	if code := registry.checkOrBind("handle-1", newer); code != cardInstanceReuseRejected {
		t.Fatalf("expected %s for cross-intent reuse, got %q", cardInstanceReuseRejected, code)
	}
	// An independent handle binds the new intent without disturbing the old.
	if code := registry.checkOrBind("handle-2", newer); code != "" {
		t.Fatalf("new handle must bind, got %q", code)
	}
	// Incomplete identity cannot back a handle.
	broken := newer
	broken.ContentHash = ""
	if code := registry.checkOrBind("handle-3", broken); code != cardInstanceNewUnavailable {
		t.Fatalf("incomplete identity must report %s, got %q", cardInstanceNewUnavailable, code)
	}
}

func TestConfirmationInstanceMetaCarriesFrozenIdentity(t *testing.T) {
	intent := replacementReviewIntent()
	meta := sdkmcp.Meta{}
	decorateConfirmationInstanceMeta(meta, intent)
	if meta["devbridge/intentId"] != intent.IntentID || meta["devbridge/aiSessionId"] != intent.AISessionID {
		t.Fatalf("identity fields missing: %+v", meta)
	}
	if meta["devbridge/version"] != intent.Version || meta["devbridge/contentHash"] != intent.ContentHash {
		t.Fatalf("frozen identity fields missing: %+v", meta)
	}
	if meta["devbridge/presentationInstanceId"] != confirmationPresentationInstanceId(intent) {
		t.Fatalf("presentationInstanceId must equal the derived id: %+v", meta)
	}
	// The static resource stays the shared cache key; identity is separate meta.
	if metaResourceURI(primaryCardMeta(confirmationResourceURI)) != confirmationResourceURI {
		t.Fatal("primary card meta must keep the shared resource uri")
	}
}

// ---- SDK-driven two-instance lifecycle fixture (real confirmation_show path) ----

type lifecycleShowService struct {
	*StaticService
	intents map[string]c.Intent
}

func (s lifecycleShowService) ReadConfirmation(_ context.Context, in c.ReadInput) (c.Intent, error) {
	intent, ok := s.intents[in.IntentID]
	if !ok {
		return c.Intent{}, c.NewFailure("INTENT_NOT_FOUND", "CORE", "RECEIPT_READBACK", "confirmation intent not found", c.ReadCurrentTarget, nil)
	}
	return intent, nil
}

func connectLifecycleShowService(t *testing.T) (*sdkmcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	base, err := NewStaticService(BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	service := lifecycleShowService{
		StaticService: base,
		intents: map[string]c.Intent{
			oldIntentID: supersededReviewIntent(),
			bIntentID(): replacementReviewIntent(),
		},
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "confirmation-instance-lifecycle", Version: "1"}, nil)
	RegisterConfirmationTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "confirmation-instance-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, ctx, cancel
}

func bIntentID() string { return "confirm_9b0ddf87d658146faedc28cea9fc8b2f" }

func showArgs(intentID, requestID string) map[string]any {
	// contentHash is part of the public read identity, so a show call cannot be
	// expressed with intentId/version alone (§7).
	contentHash := liveContentHash
	if intentID == oldIntentID {
		contentHash = oldContentHash
	}
	args := map[string]any{"aiSessionId": "session", "intentId": intentID, "version": 1, "purpose": "REVIEW", "contentHash": contentHash}
	if requestID != "" {
		args["requestId"] = requestID
	}
	return args
}

func failureCodeFromResult(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()
	raw, _ := json.Marshal(result.StructuredContent)
	var decoded map[string]any
	json.Unmarshal(raw, &decoded)
	failure, _ := decoded["failure"].(map[string]any)
	code, _ := failure["code"].(string)
	return code
}

// §11/§12: the SAME static resource serves two intents, each addressed by a distinct
// presentationInstanceId; showing B never strands B behind A.
func TestOneResourceTwoCardInstances(t *testing.T) {
	session, ctx, cancel := connectLifecycleShowService(t)
	defer cancel()

	showA, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(oldIntentID, "")})
	if err != nil || showA.IsError {
		t.Fatalf("show A err=%v isError=%v", err, showA.IsError)
	}
	showB, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(bIntentID(), "")})
	if err != nil || showB.IsError {
		t.Fatalf("show B err=%v isError=%v", err, showB.IsError)
	}
	if metaResourceURI(showA.Meta) != confirmationResourceURI || metaResourceURI(showB.Meta) != confirmationResourceURI {
		t.Fatalf("both must share the static resource: A=%v B=%v", metaResourceURI(showA.Meta), metaResourceURI(showB.Meta))
	}
	if showA.Meta["devbridge/intentId"] != oldIntentID || showB.Meta["devbridge/intentId"] != bIntentID() {
		t.Fatalf("per-show identity must reflect the requested intent: A=%v B=%v", showA.Meta["devbridge/intentId"], showB.Meta["devbridge/intentId"])
	}
	idA, _ := showA.Meta["devbridge/presentationInstanceId"].(string)
	idB, _ := showB.Meta["devbridge/presentationInstanceId"].(string)
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("distinct intents must derive distinct presentationInstanceIds: A=%q B=%q", idA, idB)
	}
}

// §8: an explicit presentation handle bound to intent A cannot be reused for intent B.
func TestConfirmationShowRefusesCrossIntentHandleReuse(t *testing.T) {
	session, ctx, cancel := connectLifecycleShowService(t)
	defer cancel()
	first, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(oldIntentID, "shared-handle")})
	if err != nil || first.IsError {
		t.Fatalf("first show err=%v isError=%v", err, first.IsError)
	}
	second, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(bIntentID(), "shared-handle")})
	if err != nil {
		t.Fatal(err)
	}
	if !second.IsError {
		t.Fatal("cross-intent reuse of a presentation handle must error")
	}
	if metaResourceURI(second.Meta) != "" {
		t.Fatalf("reuse rejection must not attach the Card resource: meta=%+v", second.Meta)
	}
	if code := failureCodeFromResult(t, second); code != cardInstanceReuseRejected {
		t.Fatalf("expected %s, got %q", cardInstanceReuseRejected, code)
	}
	// The prior intent remains independently showable (its own handle), never overwritten.
	retry, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(oldIntentID, "shared-handle")})
	if err != nil || retry.IsError {
		t.Fatalf("same-handle same-intent re-show must stay idempotent: err=%v isError=%v", err, retry.IsError)
	}
}

// §12: repeated show of the same new intent is idempotent (same instance id, no reject).
func TestConfirmationShowSameIntentRefreshIsIdempotent(t *testing.T) {
	session, ctx, cancel := connectLifecycleShowService(t)
	defer cancel()
	first, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(bIntentID(), "refresh-handle")})
	if err != nil || first.IsError {
		t.Fatalf("first show err=%v isError=%v", err, first.IsError)
	}
	second, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArgs(bIntentID(), "refresh-handle")})
	if err != nil || second.IsError {
		t.Fatalf("re-show must be idempotent: err=%v isError=%v code=%s", err, second.IsError, failureCodeFromResult(t, second))
	}
	if first.Meta["devbridge/presentationInstanceId"] != second.Meta["devbridge/presentationInstanceId"] {
		t.Fatalf("same intent must map to the same presentationInstanceId: %v vs %v", first.Meta["devbridge/presentationInstanceId"], second.Meta["devbridge/presentationInstanceId"])
	}
}
