package mcpserver_test

import (
	"os"
	"strings"
	"testing"
)

// phase4CanonicalDocumentationFreeze is §7/§10 plus §3/§6 as prose the project
// actually publishes. A contract that only lives in code can be quietly narrowed by
// the next edit to a comment, so the canonical documents are asserted with the code.
var phase4CanonicalDocumentationFreeze = map[string][]string{
	"../../docs/interfaces.md": {
		"The public Confirmation read identity is the five fields",
		"`aiSessionId + intentId + version + purpose + contentHash` taken together",
		"`contentHash` is required on every read surface",
		"A supplied identity whose\ndigest, `version` or `purpose` differs from the durable frozen intent is refused with",
		"`CONFIRMATION_READ_IDENTITY_MISMATCH` at stage `INPUT_IDENTITY`. That refusal is\na pure read miss",
		"`intentId` and\n`version` alone cannot address a read, `sourceIntentId` is never current read\nidentity",
		"There is no hash-free public read that later gains commit\nauthority",
		"`retryDisposition = EDIT_SELECTION_AND_RESUBMIT`, provided the intent itself can still\naccept a command",
		// The reducer also refuses an editable disposition on a terminal, committed or
		// expired intent; the docs must keep those three conjuncts or the prose describes
		// a wider unlock than Core grants.
		"`state = PENDING`, no frozen `commit` and an `expiresAt` in the future",
		"re-derives to `REPREPARE_REQUIRED`",
		"On the admitted conjunction the pending commit\nselection is released",
		// The public enum is REJECTED_NOT_CONSUMED; a doc that shortens it to REJECTED
		// describes a value the contract never carries.
		"`businessOutcome = REJECTED_NOT_CONSUMED`, `consumptionDisposition = NOT_CONSUMED`",
		// §4's Card-visible vocabulary: one named state and one frozen sentence. They are
		// pinned here so a later rewrite of the docs cannot quietly drop the copy the Card
		// and the shared fixture are held to.
		"`EDITABLE_RETRY_READY`",
		"本次确认未被消费。请修改选择或重新确认。",
		"The\nretry is a new user command with a new `requestId` and a new payload digest",
		"`OUTCOME_UNKNOWN` never enters this path",
		"Every consumption and retry fact above is derived by Core from durable state at the read boundary",
		"There is deliberately no `force`, `retryAnyway` or `assumeNotConsumed` option anywhere in this contract",
		"Replacing a still-pending intent creates a new `intentId`\nat `version: 1` through `sourceIntentId`",
		"The exact immutable identity of one proposal is\n`intentId + version + contentHash`, and a `version: 1` intent has no in-place\nrevision",
		// §10/§11: the replacement is a formal action over the existing prepare
		// builder, never a second constructor and never an in-place version bump.
		"`confirmation_reprepare` is the formal `PREPARE_NEW_VERSION` action and the only\nreplacement constructor",
		"`PREPARE_NEW_VERSION` is a product action name\nrather than an in-place upgrade of the rejected intent to version 2",
		// §12: the submission entry is public and closed. If the docs ever call it
		// app-private again, the prose contradicts the registered model visibility.
		"`confirmation_commit` accepts only `aiSessionId`, `intentId`, `version`, `contentHash`,\n`requestId`, `variantId` and the structured selection",
		"A surface with no commit entry reports `CONFIRMATION_COMMIT_TOOL_UNAVAILABLE` and states no\noutcome instead of inventing one",
		// §13: the typed read-back must survive a documentation rewrite.
		"A typed rejection is never\nreported as `recorded=false`, `OUTCOME_UNKNOWN` or `READ_ORIGINAL_RECEIPT`",
	},
	"../../docs/architecture/purpose-scoped-confirmation.md": {
		"Every public read\ncarries the exact identity `aiSessionId`, `intentId`, `version`, `purpose` and the\nrequired frozen `contentHash`",
		"`version` is never incremented to express a\nsuccessor",
		"`intentId + version + contentHash`\nis the complete immutable identity of one proposal and lineage is expressed only by\n`sourceIntentId`",
		"`retryDisposition = EDIT_SELECTION_AND_RESUBMIT` requires all of",
		"`businessOutcome = REJECTED_NOT_CONSUMED`, `consumptionDisposition = NOT_CONSUMED`",
		// Core's reducer also demands an intent that can still accept a command; the prose
		// has to keep all three or it overstates when a Card may re-arm.
		"intent that can still accept a command: `state = PENDING`, no frozen `commit`, and an\n`expiresAt` in the future",
		"an expired or already-committed rejection re-derives to\n`REPREPARE_REQUIRED` and unlocks nothing",
		"The retry is a new user command with a fresh `requestId` and a\nfresh payload digest, never a replay of the original call, and `redispatchSameIntent`\nstays `false`",
		"produces `Continue confirmation <intentId> version <version> contentHash <contentHash>`",
		// §5: one reducer owns the Decision on every surface and inside the writer.
		"The workspace and workflow projections, `TaskView`, the\nFeatureArea product projection, prepare preflight, `confirmation_options_get` and\nthe commit fence all read that same reducer, and the commit writer re-reads and\nrecomputes it inside its own SQLite transaction",
		// §6: identity and version are separate facts, and the refusal vocabulary is
		// four distinct codes rather than one blurred "no longer pending".
		"`decisionId` and `decisionVersion` are different facts",
		"Terminal refusals keep distinct codes: `DECISION_NOT_PENDING`,\n`DECISION_VERSION_CHANGED`, `DECISION_SUPERSEDED` and `DECISION_OWNER_CHANGED`",
		// §8/§9: the receipt-first options read and the Card copy the fixture is held to.
		"`available=false`,\n`attemptState=TERMINAL_REJECTED`, `receiptState=REJECTED_NOT_CONSUMED`",
		"`reasonCode=INTENT_REPREPARE_REQUIRED`. `NOT_ATTEMPTED` and `NOT_OBSERVED` are\nonly ever reported when no submission exists",
		"本次确认已明确拒绝且未产生业务副作用。需要根据当前 Decision 状态重新准备新的确认。",
		// §10/§11/§12/§13/§14.
		"`confirmation_reprepare` is the formal `PREPARE_NEW_VERSION` action and the only\nreplacement constructor",
		"`PREPARE_NEW_VERSION` is therefore a product action name and never an\nin-place upgrade of the rejected intent to version 2",
		"`confirmation_commit` is the one public submission entry and is model-discoverable\nexactly as the Card calls it; both reach the same Core service and the same closed\nDTO",
		"本次重新审查确认未被消费。原因：冻结的审查处置状态已发生变化。需要重新准备新的确认。",
		"新的重新审查确认已准备",
	},
}

// phase4CanonicalDocumentationProhibitions are the §8 pseudo-closures: wording that
// would re-open a hash-free read, an auto-retry or a version-incrementing replacement.
var phase4CanonicalDocumentationProhibitions = map[string][]string{
	"../../docs/interfaces.md": {
		"optional `contentHash`",
		"contentHash is optional",
		"Continue confirmation <intentId> version <version>`",
		"automatically redispatch the rejected request",
		"version increments to",
		"`businessOutcome = REJECTED`",
		// §12 makes the submission entry model-visible, so prose that re-labels it
		// private describes a Card-only authority the code no longer has.
		"app-private confirmation commit",
		"`confirmation_commit` is app-private",
		// §10 forbids an automatic replacement: the user issues the reprepare command.
		"automatically prepare a replacement intent",
		"`confirmation_reprepare` creates a successor Review",
	},
	"../../docs/architecture/purpose-scoped-confirmation.md": {
		"optional `contentHash`",
		"contentHash is optional",
		"Continue confirmation <intentId> version <version>`",
		"`businessOutcome = REJECTED`",
		"app-private confirmation commit",
		"`confirmation_commit` is app-private",
		"automatically prepare a replacement intent",
		"`confirmation_reprepare` creates a successor Review",
	},
}

func TestPhase4CanonicalDocumentationFreezesReadIdentityRetryAndVersionSemantics(t *testing.T) {
	// Prose is compared on normalized whitespace so a later re-wrap of the same
	// sentence cannot silently drop a frozen contract phrase.
	normalize := func(text string) string { return strings.Join(strings.Fields(text), " ") }
	for path, required := range phase4CanonicalDocumentationFreeze {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read canonical document %s: %v", path, err)
		}
		text := normalize(string(content))
		for _, phrase := range required {
			if !strings.Contains(text, normalize(phrase)) {
				t.Fatalf("%s no longer freezes %q", path, phrase)
			}
		}
		for _, forbidden := range phase4CanonicalDocumentationProhibitions[path] {
			if strings.Contains(text, normalize(forbidden)) {
				t.Fatalf("%s publishes a pseudo-closure phrase %q", path, forbidden)
			}
		}
	}
}
