package confirmation

// PreparationState classifies a durable confirmation intent that predates (or
// bypasses) the atomic prepare contract, so an operator can decide a safe next
// step without ever guessing at consumption. Classification is derived only from
// durable facts; the absence of a client-side COMMIT_SENT event is never proof
// of non-consumption.
type PreparationState string

const (
	// PreparationIncompleteNotCommitted: facts are incomplete AND no attempt,
	// request, receipt, decision consumption or business object proves any
	// consumption. A fresh intent may be prepared.
	PreparationIncompleteNotCommitted PreparationState = "PREPARE_INCOMPLETE_NOT_COMMITTED"
	// PreparedPresentationUnavailable: the intent is complete and readable but
	// was never committed; only the Card/show presentation failed. Re-show the
	// same intent; never re-prepare.
	PreparedPresentationUnavailable PreparationState = "PREPARED_PRESENTATION_UNAVAILABLE"
	// PreparationCommittedFactsCorrupt: durable consumption evidence exists but
	// the intent's required facts cannot be reconstructed. Fail closed for repair.
	PreparationCommittedFactsCorrupt PreparationState = "PREPARE_COMMITTED_FACTS_CORRUPT"
	// AttemptExistsOutcomeUnknown: a commit attempt or receipt exists but the
	// effect is not provably terminal. Hold as UNKNOWN; no redispatch, no reprepare.
	AttemptExistsOutcomeUnknown PreparationState = "ATTEMPT_EXISTS_OUTCOME_UNKNOWN"
	// PreparationCommitted: the intent committed; follow its original objects.
	PreparationCommitted PreparationState = "COMMITTED"
)

// PreparationEvidence is the set of durable facts a store read gathers for one
// intent. Each field is independently observable in SQLite so the classification
// rule stays a pure function of these facts.
type PreparationEvidence struct {
	IntentRowExists      bool
	IntentFactsComplete  bool
	State                string
	CommitAttemptExists  bool // durable commit request/ledger for this intent
	ReceiptRecorded      bool // an immutable submission receipt exists
	ReceiptOutcome       Outcome
	BusinessObjectsExist bool // step-produced objects or decision consumption persist
}

// PreparationClassification is the operator-facing disposition and its safety
// consequences. Consumed is nil when durable facts cannot prove consumption.
type PreparationClassification struct {
	State            PreparationState
	Consumed         *bool
	ReprepareAllowed bool
	Redispatch       bool
}

func boolPtr(v bool) *bool { return &v }

// ClassifyPreparationEvidence is the authoritative taxonomy of section 14. It
// never reports non-consumption without proving that no attempt, request,
// receipt, decision consumption or business object survives; and it never
// authorises reprepare while any consumption evidence remains.
func ClassifyPreparationEvidence(e PreparationEvidence) PreparationClassification {
	if !e.IntentRowExists {
		// Nothing durable exists for this intent: safe to prepare a fresh one.
		return PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}
	}
	switch e.State {
	case "COMPLETED", "PARTIAL", "REJECTED":
		return PreparationClassification{State: PreparationCommitted, Consumed: boolPtr(true)}
	case "CONFIRMING", "UNKNOWN":
		// A consumption transition was entered but its terminal effect is not
		// provable here. Fail closed to the original intent, never reprepare.
		return PreparationClassification{State: AttemptExistsOutcomeUnknown, Consumed: nil, ReprepareAllowed: false}
	case "SUPERSEDED", "ABANDONED":
		return PreparationClassification{State: PreparationCommitted, Consumed: boolPtr(true)}
	}
	// PENDING (or an unrecognized lifecycle value) is the prepare-only region.
	if e.CommitAttemptExists || e.ReceiptRecorded {
		// Durable consumption evidence under a non-terminal state: unknown until
		// the exact receipt is read back.
		if e.ReceiptRecorded && e.ReceiptOutcome == RejectedNotConsumed {
			return PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}
		}
		return PreparationClassification{State: AttemptExistsOutcomeUnknown, Consumed: nil, ReprepareAllowed: false}
	}
	if !e.IntentFactsComplete {
		// No attempt, request, receipt, decision consumption or business object
		// survives, and the facts are incomplete: this is a half-written prepare
		// that never committed. A fresh intent may be prepared.
		if e.BusinessObjectsExist {
			// Durable business objects contradict "nothing committed".
			return PreparationClassification{State: PreparationCommittedFactsCorrupt, Consumed: nil, ReprepareAllowed: false}
		}
		return PreparationClassification{State: PreparationIncompleteNotCommitted, Consumed: boolPtr(false), ReprepareAllowed: true}
	}
	// Complete, readable, and never committed: presentation enrichment alone
	// failed. Re-show this exact intent; do not re-prepare.
	return PreparationClassification{State: PreparedPresentationUnavailable, Consumed: boolPtr(false), ReprepareAllowed: false}
}
