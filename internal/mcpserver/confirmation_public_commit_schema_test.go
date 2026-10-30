package mcpserver

import (
	"encoding/json"
	"reflect"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// §12: the public submission entry and the formal replacement prepare are closed
// requests. Every fact a caller might try to steer — an outcome, a consumption flag, a
// Decision status or version, a successor identity, a retry disposition or any
// force-flavoured escape hatch — is a Core-derived read fact, so the advertised
// schemas must refuse it before a handler ever runs. A schema that merely ignores an
// unknown field would still accept the command.

func schemaObject(value any) map[string]any {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		panic(err)
	}
	return out
}

func TestPublicCommitAndReprepareSchemasAdmitOnlyTheClosedRequest(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	commit := map[string]any{"aiSessionId": "session", "intentId": "intent", "version": 1, "purpose": "REVIEW", "requestId": "request", "action": "REVISE", "contentHash": hash, "variantId": "revise", "selection": map[string]any{}}
	reprepare := map[string]any{"aiSessionId": "session", "sourceIntentId": "intent", "sourceVersion": 1, "sourceContentHash": hash, "requestId": "request-2"}
	for _, tc := range []struct {
		name   string
		schema any
		valid  map[string]any
	}{
		{"confirmation_commit", confirmationCommitSchema(), commit},
		{"confirmation_reprepare", confirmationReprepareSchema(), reprepare},
	} {
		resolved := resolveSchema(t, tc.schema)
		if err := resolved.Validate(tc.valid); err != nil {
			t.Fatalf("%s refused its own canonical request %s: %v", tc.name, mustJSON(t, tc.valid), err)
		}
	}
}

func TestPublicCommitAndReprepareSchemasRefuseEveryDerivedAuthority(t *testing.T) {
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	base := func() map[string]any {
		return map[string]any{"aiSessionId": "session", "intentId": "intent", "version": 1, "purpose": "REVIEW", "requestId": "request", "action": "REVISE", "contentHash": hash, "variantId": "revise", "selection": map[string]any{}}
	}
	// §12's forbidden input vocabulary, stated as the facts §13 says are read-only.
	for _, injected := range []struct {
		field string
		value any
	}{
		{"outcome", "CONSUMED"},
		{"businessOutcome", "REJECTED_NOT_CONSUMED"},
		{"consumed", false},
		{"consumptionDisposition", "NOT_CONSUMED"},
		{"recorded", true},
		{"retryDisposition", string(c.RetryEditSelectionAndResubmit)},
		{"reprepareAllowed", true},
		{"redispatchSameIntent", true},
		{"manualRetryAllowed", true},
		{"decisionStatus", "PENDING"},
		{"decisionVersion", hash},
		{"successorReviewId", "review-forged"},
		{"force", true},
		{"assumeNotConsumed", true},
		{"retryAnyway", true},
		{"sourceIntentId", "intent"},
	} {
		args := base()
		args[injected.field] = injected.value
		if err := resolveSchema(t, confirmationCommitSchema()).Validate(args); err == nil {
			t.Fatalf("confirmation_commit accepted the derived fact %s=%v", injected.field, injected.value)
		}
	}
	missing := base()
	for _, required := range []string{"contentHash", "requestId", "variantId", "selection", "action"} {
		delete(missing, required)
		if err := resolveSchema(t, confirmationCommitSchema()).Validate(missing); err == nil {
			t.Fatalf("confirmation_commit accepted a request without %s", required)
		}
		missing[required] = base()[required]
	}

	reprepare := func() map[string]any {
		return map[string]any{"aiSessionId": "session", "sourceIntentId": "intent", "sourceVersion": 1, "sourceContentHash": hash, "requestId": "request-2"}
	}
	for _, injected := range []struct {
		field string
		value any
	}{
		{"intentId", "pre-assigned-replacement-id"},
		{"version", 2},
		{"taskId", "task-forged"},
		{"reviewId", "review-forged"},
		{"decisionId", "INVALID_REVIEW_RESULT_RESOLUTION:review-forged"},
		{"decisionVersion", hash},
		{"preparedTopologyRevision", 9},
		{"preparedDevelopmentContextHash", hash},
		{"successorReviewId", "review-forged"},
		{"variants", []any{map[string]any{"id": "revise", "label": "Revise"}}},
		{"sourceIntentVersionAsNew", 2},
	} {
		args := reprepare()
		args[injected.field] = injected.value
		if err := resolveSchema(t, confirmationReprepareSchema()).Validate(args); err == nil {
			t.Fatalf("confirmation_reprepare accepted the Core-owned fact %s=%v", injected.field, injected.value)
		}
	}
	for _, missingCase := range []struct {
		field string
		value any
	}{
		{"sourceIntentId", ""},
		{"sourceVersion", 0},
		{"sourceContentHash", "not-a-digest"},
		{"requestId", ""},
	} {
		args := reprepare()
		args[missingCase.field] = missingCase.value
		if err := resolveSchema(t, confirmationReprepareSchema()).Validate(args); err == nil {
			t.Fatalf("confirmation_reprepare accepted %s=%v", missingCase.field, missingCase.value)
		}
	}
}

func TestPublicCommitAndReprepareShareTheConfirmationResultEnvelope(t *testing.T) {
	// §12: one output contract for every confirmation surface, so a caller that
	// switches from the Card to the text path never meets a second envelope shape.
	read := schemaObject(confirmationReadSchema(false))
	commit := schemaObject(confirmationCommitSchema())
	reprepare := schemaObject(confirmationReprepareSchema())
	for name, schema := range map[string]map[string]any{"confirmation_commit": commit, "confirmation_reprepare": reprepare} {
		if schema["additionalProperties"] != false {
			t.Fatalf("%s input schema is not closed", name)
		}
	}
	for _, required := range []string{"aiSessionId", "intentId", "version", "purpose", "requestId", "action", "contentHash", "variantId", "selection"} {
		if !schemaRequires(commit, required) {
			t.Fatalf("confirmation_commit no longer requires %s: %s", required, mustJSON(t, commit["required"]))
		}
	}
	for _, required := range []string{"aiSessionId", "sourceIntentId", "sourceVersion", "sourceContentHash", "requestId"} {
		if !schemaRequires(reprepare, required) {
			t.Fatalf("confirmation_reprepare no longer requires %s: %s", required, mustJSON(t, reprepare["required"]))
		}
	}
	if !reflect.DeepEqual(commit["properties"].(map[string]any)["version"], read["properties"].(map[string]any)["version"]) {
		t.Fatal("the commit version field diverged from the exact read identity")
	}
}

func schemaRequires(schema map[string]any, want string) bool {
	list, ok := schema["required"].([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
