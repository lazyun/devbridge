package confirmation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

type Outcome string
type NextAction string
type RetryDisposition string

const (
	RejectedNotConsumed   Outcome    = "REJECTED_NOT_CONSUMED"
	Consumed              Outcome    = "CONSUMED"
	Unknown               Outcome    = "UNKNOWN"
	EditSelection         NextAction = "EDIT_SELECTION"
	ReadCurrentTarget     NextAction = "READ_CURRENT_TARGET"
	PrepareNewVersion     NextAction = "PREPARE_NEW_VERSION"
	PrepareRecoveryStatus NextAction = "PREPARE_RECOVERY_STATUS"
	ReadOriginalReceipt   NextAction = "READ_ORIGINAL_RECEIPT"
	ContinueOriginalSteps NextAction = "CONTINUE_ORIGINAL_STEPS"
	AccessBlocked         NextAction = "ACCESS_BLOCKED"
)

// RetryDisposition is the single Core answer to "what may the user do next with
// this intent". It is derived from the durable receipt only, so no public surface
// re-implements the branching that §3 freezes.
const (
	RetryNone                     RetryDisposition = "NONE"
	RetryEditSelectionAndResubmit RetryDisposition = "EDIT_SELECTION_AND_RESUBMIT"
	RetryReprepareRequired        RetryDisposition = "REPREPARE_REQUIRED"
	RetryReadOriginalReceipt      RetryDisposition = "READ_ORIGINAL_RECEIPT"
)

var ErrRequestNotObserved = errors.New("confirmation request not observed")
var ErrRequestConflict = errors.New("confirmation request payload conflicts")
var ErrLegacyReplayUnproven = errors.New("historical prepare input identity cannot be proven")

// Failure describes this call/step; it never changes Task or intent lifecycle.
type Failure struct {
	Code       string     `json:"code"`
	ReasonCode string     `json:"reasonCode,omitempty"`
	Source     string     `json:"source"`
	Stage      string     `json:"stage"`
	Message    string     `json:"message"`
	NextAction NextAction `json:"nextAction"`
	cause      error
}

func (e *Failure) Error() string { return e.Code + ": " + e.Message }
func (e *Failure) Unwrap() error { return e.cause }
func NewFailure(code, source, stage, message string, next NextAction, cause error) *Failure {
	return &Failure{Code: code, Source: source, Stage: stage, Message: message, NextAction: next, cause: cause}
}

// Recorded=false is NOT_OBSERVED, never evidence of safe non-consumption.
// Request is the original submitted choice, independent of normalized intent.Commit.
type Submission struct {
	Request                CommitInput      `json:"request"`
	PayloadSHA256          string           `json:"payloadSHA256"`
	Outcome                Outcome          `json:"outcome"`
	Recorded               bool             `json:"recorded"`
	Failure                *Failure         `json:"failure,omitempty"`
	NextAction             NextAction       `json:"nextAction"`
	ReceiptRequestID       string           `json:"receiptRequestId"`
	BusinessOutcome        string           `json:"businessOutcome"`
	ConsumptionDisposition string           `json:"consumptionDisposition"`
	Consumed               *bool            `json:"consumed,omitempty"`
	RedispatchSameIntent   bool             `json:"redispatchSameIntent"`
	ReprepareAllowed       bool             `json:"reprepareAllowed"`
	RetryDisposition       RetryDisposition `json:"retryDisposition"`
}

// PrepareBusinessOutcome is the prepare-call disposition reported by the public
// prepare contract. It is distinct from a submission's business outcome: it
// describes only whether this prepare persisted a complete, presentable intent.
type PrepareBusinessOutcome string

const (
	Prepared            PrepareBusinessOutcome = "PREPARED"
	RejectedNotPrepared PrepareBusinessOutcome = "REJECTED_NOT_PREPARED"
	PrepareConflict     PrepareBusinessOutcome = "REQUEST_CONFLICT"
)

// Result is the closed transport envelope. Unauthorized errors omit Intent.
// The three prepare-only facts are populated solely by a *_prepare result. Read
// and commit envelopes (confirmation_get/options/show/commit) always leave them
// nil or empty, so omitempty keeps those envelopes to the four members the Card
// entry contract decodes; a client can never inject these fields.
type Result struct {
	Display *DisplayMetadata `json:"display,omitempty"`
	Intent  *Intent          `json:"intent,omitempty"`
	Options *Options         `json:"options,omitempty"`
	Failure *Failure         `json:"failure,omitempty"`
	// BusinessOutcome is non-empty only on a prepare result.
	BusinessOutcome PrepareBusinessOutcome `json:"businessOutcome,omitempty"`
	// FactsComplete reports that the production reader reconstructs every
	// immutable intent fact. PresentationReady reports that the canonical
	// confirmation_show envelope for this intent satisfies the advertised MCP
	// OutputSchema. Both are computed by Core and never accepted from a client.
	FactsComplete     *bool `json:"factsComplete,omitempty"`
	PresentationReady *bool `json:"presentationReady,omitempty"`
}

const publicFailureFallback = "The confirmation command did not complete successfully."

// NormalizePublicFailure repairs historical/internal failure facts at the
// read boundary. It never rewrites stored receipt bytes or changes machine
// disposition. Message text is presentation only; callers branch on codes.
func NormalizePublicFailure(failure *Failure) *Failure {
	if failure == nil {
		return nil
	}
	if failure.Code == "" {
		failure.Code = "CONFIRMATION_COMMAND_FAILED"
	}
	if failure.ReasonCode == "" {
		failure.ReasonCode = failure.Code
	}
	switch failure.Source {
	case "CORE", "DOMAIN", "RUNTIME", "CONFIRMATION", "STORAGE":
	default:
		failure.Source = "CONFIRMATION"
	}
	switch failure.Stage {
	case "INPUT_IDENTITY", "PREPARE", "CONFIRMATION_PREFLIGHT", "UNIQUE_CONSUMPTION", "BUSINESS_STEP", "DISPATCH", "RECEIPT_READBACK":
	default:
		failure.Stage = "RECEIPT_READBACK"
	}
	if len([]rune(failure.Message)) == 0 {
		switch failure.ReasonCode {
		case "PROJECT_TOPOLOGY_CHANGED":
			failure.Message = "The Project repository topology changed after this confirmation was prepared."
		case "DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED":
			failure.Message = "The Project development branch configuration changed after this confirmation was prepared."
		case "DECISION_VERSION_CHANGED":
			// A version token drift says the disposition facts moved, not that the
			// Decision stopped being pending; the same reducer may still report it as
			// actionable and expect a fresh prepare.
			failure.Message = "The Review Decision changed after this confirmation was prepared."
		case "VARIANT_NO_LONGER_AVAILABLE", "CONFIRMATION_VARIANT_UNAVAILABLE":
			failure.Message = "The prepared confirmation variant is no longer available."
		default:
			failure.Message = publicFailureFallback
		}
	}
	return failure
}

// NormalizePublicIntent applies read-side compatibility to nested historical
// failures and derives explicit consumption facts without altering authority.
func NormalizePublicIntent(intent *Intent) {
	if intent == nil {
		return
	}
	// Share the exact same empty-collection canonicalization the MCP surface uses,
	// so HTTP/Console/Workbench/Desktop project a zero-step/zero-variant intent as
	// steps:[] / variants:[], never JSON null. Runs only on the detached public
	// copy every caller passes here and fabricates no authoritative fact.
	CanonicalizePublicCollections(intent)
	for index := range intent.Steps {
		intent.Steps[index].Failure = NormalizePublicFailure(intent.Steps[index].Failure)
	}
	if intent.Submission == nil {
		return
	}
	sub := intent.Submission
	sub.Failure = NormalizePublicFailure(sub.Failure)
	sub.RedispatchSameIntent = false
	switch sub.Outcome {
	case Consumed:
		consumed := true
		sub.Consumed = &consumed
		sub.BusinessOutcome = "COMMITTED"
		sub.ConsumptionDisposition = "CONSUMED"
		if RejectedContinuationWithoutEffect(intent) {
			// The immutable confirmation submission was consumed, but its
			// retired business writer is proven to have committed no effect.
			sub.BusinessOutcome = "REJECTED_NO_EFFECT"
		}
	case RejectedNotConsumed:
		consumed := false
		sub.Consumed = &consumed
		sub.BusinessOutcome = "REJECTED_NOT_CONSUMED"
		sub.ConsumptionDisposition = "NOT_CONSUMED"
		sub.ReprepareAllowed = sub.NextAction == PrepareNewVersion || sub.NextAction == EditSelection
		if intent.Purpose == RecoveryPurpose && sub.NextAction == EditSelection {
			sub.ReprepareAllowed = false
		}
	default:
		sub.Consumed = nil
		sub.BusinessOutcome = "OUTCOME_UNKNOWN"
		sub.ConsumptionDisposition = "UNKNOWN"
	}
	sub.RetryDisposition = deriveRetryDisposition(intent, sub)
}

// RejectedContinuationWithoutEffect validates the detached readback proof shape.
// It grants no execution authority and does not alter the original consumption.
func RejectedContinuationWithoutEffect(intent *Intent) bool {
	if intent == nil || intent.CanResume || intent.State != "REJECTED" || intent.Purpose != Execution || intent.Action != "REQUEST_CHANGES" || intent.Commit == nil || intent.Submission == nil || !intent.Submission.Recorded || intent.Submission.ReceiptRequestID != intent.Commit.RequestID || len(intent.Steps) != 2 {
		return false
	}
	first, next := intent.Steps[0], intent.Steps[1]
	if first.Kind != "RESOLVE_CONTINUATION" || first.State != "REJECTED" || first.Effect != "NO_EFFECT_PROVEN" || first.Failure == nil || first.Failure.Code != "REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW" || first.Failure.Stage != "RECEIPT_READBACK" || next.Kind != "START_IMPLEMENTATION" || next.State != "PENDING" {
		return false
	}
	for _, step := range intent.Steps {
		if step.ExecutionID != "" || step.RunID != "" || step.RevisionID != "" || step.WorkflowID != "" {
			return false
		}
	}
	return true
}

// deriveRetryDisposition is the one reducer behind submission.retryDisposition.
// EDIT_SELECTION_AND_RESUBMIT is the only value that lets a Card re-arm its
// confirmation control, and it requires every §3 fact at once: a durable,
// recorded rejection, an explicit non-consumption, an editable next action, and an
// intent that can still accept a new command. A non-recorded read is never
// non-consumption evidence, so it can only ever route to a receipt read.
func deriveRetryDisposition(intent *Intent, sub *Submission) RetryDisposition {
	if !sub.Recorded || sub.Outcome != RejectedNotConsumed {
		if sub.Outcome == Consumed {
			return RetryNone
		}
		return RetryReadOriginalReceipt
	}
	if sub.Consumed == nil || *sub.Consumed || sub.BusinessOutcome != "REJECTED_NOT_CONSUMED" || sub.ConsumptionDisposition != "NOT_CONSUMED" {
		return RetryNone
	}
	switch sub.NextAction {
	case EditSelection:
		// The intent must still be confirmable at all: a terminal, expired or
		// already-committed intent cannot host a same-intent edit-and-resubmit.
		if intent.State != "PENDING" || intent.Commit != nil || !time.Now().Before(intent.ExpiresAt) {
			return RetryReprepareRequired
		}
		return RetryEditSelectionAndResubmit
	case PrepareNewVersion, ReadCurrentTarget:
		return RetryReprepareRequired
	default:
		return RetryNone
	}
}

func NormalizePublicOptions(options *Options) {
	if options == nil {
		return
	}
	// Share the exact same empty-collection canonicalization every public surface
	// uses, so HTTP/Console/Workbench/Desktop project the same array shape as MCP.
	CanonicalizePublicOptions(options)
	for i := range options.Variants {
		for j := range options.Variants[i].Failures {
			NormalizePublicFailure(&options.Variants[i].Failures[j])
		}
	}
}

func NormalizePublicResult(result *Result) {
	if result == nil {
		return
	}
	result.Failure = NormalizePublicFailure(result.Failure)
	NormalizePublicIntent(result.Intent)
	NormalizePublicOptions(result.Options)
}

// InputHash preserves JSON field presence, explicit null/empty and array order.
// Map encoding sorts keys; numbers retain their original JSON representation.
func InputHash(raw []byte) (string, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func ValueHash(v any) string { b, _ := json.Marshal(v); h, _ := InputHash(b); return h }
