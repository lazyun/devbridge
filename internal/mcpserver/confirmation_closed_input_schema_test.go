package mcpserver

import (
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// phase4ClosedReadFields are consumption, retry and lineage facts Core derives from
// durable state. §12 keeps them out of every public input, and §2 forbids a bypass
// switch, so the closed schemas must reject them by name rather than ignore them.
var phase4ClosedReadFields = []string{
	"consumed", "consumptionDisposition", "retryDisposition", "manualRetryAllowed",
	"redispatchSameIntent", "reprepareAllowed", "receiptRequestId", "confirmedOutcome",
	"outcome", "force", "retryAnyway", "assumeNotConsumed", "retry",
	"sourceIntentId", "versionOrdinal", "lineageOrdinal", "presentationInstanceId",
}

func phase4ExactReadInput() map[string]any {
	return map[string]any{
		"aiSessionId": "session", "intentId": "intent", "version": 1,
		"purpose": string(c.Review), "contentHash": phase4ValidReadHash,
	}
}

const phase4ValidReadHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// §7/§8/§12 at the public boundary: every read tool is built from one schema, so the
// frozen digest is mandatory on all of them and none of the Core-derived facts can be
// injected to buy read or commit authority.
func TestConfirmationPublicReadSchemaRequiresContentHashAndRejectsClosedFields(t *testing.T) {
	for _, variant := range []struct {
		name   string
		schema any
	}{
		{"confirmation_get", confirmationReadSchema(false)},
		{"confirmation_show", confirmationReadSchema(false)},
		{"confirmation_options_get", confirmationReadSchema(true)},
	} {
		t.Run(variant.name, func(t *testing.T) {
			schema := resolveSchema(t, variant.schema)
			input := phase4ExactReadInput()
			if err := schema.Validate(input); err != nil {
				t.Fatalf("the exact five-field read identity is not schema-valid: %v", err)
			}
			for _, shape := range []struct {
				name  string
				value any
			}{{"absent", nil}, {"empty", ""}, {"short", "abc"}, {"uppercase", strings.ToUpper(phase4ValidReadHash)}, {"tooLong", phase4ValidReadHash + "0"}} {
				probe := map[string]any{}
				for key, value := range input {
					probe[key] = value
				}
				if shape.value != nil {
					probe["contentHash"] = shape.value
				} else {
					delete(probe, "contentHash")
				}
				if err := schema.Validate(probe); err == nil {
					t.Fatalf("accepted a %s contentHash: no hash-free read surface may exist (§8)", shape.name)
				}
			}
			for _, field := range phase4ClosedReadFields {
				probe := map[string]any{}
				for key, value := range input {
					probe[key] = value
				}
				probe[field] = true
				if err := schema.Validate(probe); err == nil {
					t.Fatalf("%s accepted the injected Core-derived field %q (§12)", variant.name, field)
				}
			}
		})
	}

	commit := resolveSchema(t, confirmationCommitSchema())
	baseline := phase4ExactReadInput()
	baseline["requestId"] = "request"
	baseline["action"] = "START_REVIEW"
	baseline["variantId"] = "start"
	baseline["selection"] = map[string]any{}
	if err := commit.Validate(baseline); err != nil {
		t.Fatalf("the exact commit submission is not schema-valid: %v", err)
	}
	// Commit inherits the same required digest instead of restating it, so the identity
	// rule cannot drift between the read and write surfaces.
	delete(baseline, "contentHash")
	if err := commit.Validate(baseline); err == nil {
		t.Fatal("commit accepted a hash-free submission identity")
	}
	for _, field := range phase4ClosedReadFields {
		probe := map[string]any{"contentHash": phase4ValidReadHash}
		for key, value := range baseline {
			probe[key] = value
		}
		probe[field] = true
		if err := commit.Validate(probe); err == nil {
			t.Fatalf("commit accepted the injected Core-derived field %q (§12)", field)
		}
	}
}
