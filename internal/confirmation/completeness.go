package confirmation

import "regexp"

var confirmationContentHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// IntentFactsComplete reports whether an intent carries the full set of
// immutable facts a caller needs to present and commit it. It is a pure,
// read-side validator shared by the prepare in-transaction read-after-write
// gate and the public presentation contract, so a half-written intent can never
// be returned as a displayable PENDING confirmation. Project-scope and
// multi-repository intents legitimately omit a single repositoryId; completeness
// never requires one.
func IntentFactsComplete(intent *Intent) (bool, string, string) {
	if intent == nil {
		return false, "CONFIRMATION_INTENT_MISSING", "No confirmation intent was reconstructed."
	}
	if intent.IntentID == "" || intent.AISessionID == "" {
		return false, "CONFIRMATION_IDENTITY_INCOMPLETE", "The confirmation caller or intent identity is missing."
	}
	if intent.Version < 1 {
		return false, "CONFIRMATION_VERSION_INVALID", "The confirmation version is missing."
	}
	if !confirmationPurposeAllowed(intent.Purpose) {
		return false, "CONFIRMATION_PURPOSE_INVALID", "The confirmation purpose is not recognized."
	}
	// Project-scope confirmations (FeatureArea/Task/Review/Execution) must carry
	// a frozen Project identity. PROJECT_SETUP is the confirmation that selects
	// or binds the Project, so it legitimately prepares before one is bound.
	if intent.Purpose != ProjectSetup && intent.ProjectID == "" {
		return false, "CONFIRMATION_PROJECT_IDENTITY_MISSING", "The confirmation Project identity is missing."
	}
	if !confirmationContentHashPattern.MatchString(intent.ContentHash) {
		return false, "CONFIRMATION_CONTENT_HASH_MISSING", "The confirmation content hash is missing or malformed."
	}
	if intent.Action == "" || intent.CreatedAt.IsZero() {
		return false, "CONFIRMATION_PREPARE_FACTS_INCOMPLETE", "The confirmation action or creation time is missing."
	}
	content := intent.Content
	if content.AISessionID != intent.AISessionID || content.Purpose != intent.Purpose || content.Action != intent.Action {
		return false, "CONFIRMATION_CONTENT_IDENTITY_MISMATCH", "The confirmation content identity does not match the intent."
	}
	// A freshly prepared intent has an empty step ledger by design; the ledger
	// only fills during commit. Its emptiness is never an incompleteness.
	if !confirmationPurposeBodyPresent(intent.Purpose, content) {
		return false, "CONFIRMATION_PURPOSE_CONTENT_MISSING", "The confirmation purpose-specific facts are missing."
	}
	seen := make(map[string]bool, len(content.Variants))
	for _, variant := range content.Variants {
		// Variant identity is commit-critical; Label/Summary are optional
		// display enrichment and never a presentation prerequisite.
		if variant.ID == "" || seen[variant.ID] {
			return false, "CONFIRMATION_VARIANT_FACTS_INCOMPLETE", "The confirmation variants are missing or malformed."
		}
		seen[variant.ID] = true
	}
	if len(content.Variants) == 0 {
		return false, "CONFIRMATION_VARIANT_FACTS_INCOMPLETE", "The confirmation has no prepared variant."
	}
	return true, "", ""
}

func confirmationPurposeAllowed(purpose Purpose) bool {
	switch purpose {
	case ProjectSetup, FeatureArea, Task, Review, Execution, RecoveryPurpose:
		return true
	}
	return false
}

func confirmationPurposeBodyPresent(purpose Purpose, content PrepareInput) bool {
	switch purpose {
	case ProjectSetup:
		return content.ProjectSetup != nil
	case FeatureArea:
		return content.FeatureArea != nil
	case Task:
		return content.Task != nil
	case Review:
		body := content.Review
		return body != nil && body.TaskID != "" && body.FeatureAreaID != ""
	case Execution:
		return content.Execution != nil
	case RecoveryPurpose:
		body := content.RecoveryPurpose
		return body != nil && body.ExpectedTaskStatus == "ACTIVE" && body.ExpectedTaskPhase == "RECOVERY_REQUIRED" && body.PreparedDevelopmentContextHash != "" && body.Proposal.RecoveryID != "" && body.Proposal.SubmittedHash == body.Proposal.ComputeSubmittedHash()
	}
	return false
}
