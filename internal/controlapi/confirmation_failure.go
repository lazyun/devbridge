package controlapi

import (
	"context"
	"errors"
	"strings"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// ConfirmationFailure preserves the originating typed code without interpreting
// message text. NextAction is protocol routing, never permission to redispatch.
func ConfirmationFailure(err error, stage string) *c.Failure {
	if err == nil {
		return nil
	}
	var original *c.Failure
	if errors.As(err, &original) {
		return original
	}
	if code := runtimeAuthorityError(err); code != "" {
		return c.NewFailure(code, "CORE", stage, err.Error(), c.ReadOriginalReceipt, err)
	}
	next := c.ReadOriginalReceipt
	code, source, message := "CONFIRMATION_FACTS_UNAVAILABLE", "CORE", "The required confirmation facts could not be confirmed."
	var runtime *codexruntime.SelectionError
	var file *directfile.Error
	var domain *wp.Error
	var api *Error
	var handoff *executionproposal.HandoffError
	switch {
	case errors.As(err, &handoff):
		code, source, message = handoff.Handoff.Code, "RUNTIME", "The exact Run is reserved; Supervisor acceptance is not confirmed. Read its original handoff receipt."
	case errors.As(err, &runtime):
		code, source, message = string(runtime.Kind), "RUNTIME", "The selected runtime configuration is unavailable."
		next = c.EditSelection
		switch runtime.Kind {
		case codexruntime.FrozenConfigurationMismatch:
			next = c.PrepareNewVersion
		case codexruntime.RuntimeConfigurationUnknown, codexruntime.BackendConfigurationUnavailable:
			next = c.ReadCurrentTarget
		}
	case errors.As(err, &file):
		code, source, message, next = file.Code, "DOMAIN", file.Message, c.ReadCurrentTarget
	case errors.Is(err, task.ErrUnobservableRequiredCheck):
		code, source, message, next = "UNOBSERVABLE_REQUIRED_CHECK", "DOMAIN", err.Error(), c.PrepareNewVersion
	case errors.Is(err, c.ErrRequestConflict):
		code, source, message = "CONFIRMATION_REQUEST_CONFLICT", "STORAGE", "This request ID belongs to a different submitted payload."
	case errors.Is(err, decisionresolution.ErrInvalidArgument):
		code, source, message = "DECISION_TARGET_CONFLICT", "DOMAIN", "The exact Decision was resolved by another authorized request or no longer accepts this choice. Read the current target and original receipt."
		next = c.ReadCurrentTarget
	case errors.Is(err, executionproposal.ErrConflict):
		code, source, message = "BUSINESS_TARGET_CONFLICT", "DOMAIN", "The exact business target or payload no longer matches the current authorized object. Read the current target and original receipt."
		next = c.ReadCurrentTarget
	case errors.Is(err, c.ErrLegacyReplayUnproven):
		code, source, message = "PREPARE_REPLAY_EVIDENCE_UNAVAILABLE", "STORAGE", "The historical original prepare input cannot be proven; read the known intent."
	case errors.Is(err, c.ErrRequestNotObserved):
		code, source, message = "CONFIRMATION_REQUEST_NOT_OBSERVED", "STORAGE", "No authoritative result for this exact request has been observed."
	case errors.Is(err, aisession.ErrScopeMismatch):
		code, message, next = "SESSION_SCOPE_MISMATCH", "The Session belongs to another caller scope.", c.AccessBlocked
	case errors.Is(err, aisession.ErrNotFound):
		code, message, next = "AI_SESSION_NOT_FOUND", "The exact Session is unavailable.", c.AccessBlocked
	case errors.Is(err, aisession.ErrProjectUnbound):
		code, message, next = "SESSION_PROJECT_REQUIRED", "This purpose requires the existing Session to be bound.", c.AccessBlocked
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code, message = "CONFIRMATION_REQUEST_INTERRUPTED", "Read the exact original request before any further action."
	case errors.As(err, &domain):
		code, source, message = domain.Code, "DOMAIN", domain.Message
		next = c.ReadCurrentTarget
	case errors.As(err, &api):
		code, message = string(api.Code), api.Message
		switch api.Code {
		case CodePermissionDenied, CodeConfirmationCallerContextRequired:
			next = c.AccessBlocked
		case CodeInvalidArgument:
			next = c.PrepareNewVersion
		case CodeNotFound, CodeConflict, CodeInvalidState, CodeFeatureAreaRetiring, CodeFeatureAreaBusy:
			next = c.ReadCurrentTarget
		}
	}
	if len(message) > 512 {
		message = message[:512]
	}
	return c.NewFailure(code, source, stage, strings.ToValidUTF8(message, ""), next, err)
}
func confirmationSelectionFailure(code, message string) *c.Failure {
	return c.NewFailure(code, "CONFIRMATION", "CONFIRMATION_PREFLIGHT", message, c.EditSelection, nil)
}

// The public read identity carries the frozen content digest as a lowercase SHA-256
// hex string. Core never accepts a shorter, upper-case or padded substitute, and
// never falls back to intentId-only addressing when the field is absent (§8).
func validConfirmationContentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// confirmationReadIdentityFailure is §7's pure read refusal: it is emitted before
// any write, so an incorrect hash cannot advance an intent, record a request row,
// consume a Decision or mount a Card. It grants no lookup-without-hash path either.
func confirmationReadIdentityFailure(err error) *c.Failure {
	return c.NewFailure(
		"CONFIRMATION_READ_IDENTITY_MISMATCH",
		"CORE",
		"INPUT_IDENTITY",
		"The supplied confirmation read identity does not match the durable frozen intent content. Re-read the current target and use the exact intentId, version and contentHash together.",
		c.ReadCurrentTarget,
		err,
	)
}
func appendConfirmationFailure(choice *c.Choice, failure *c.Failure) {
	if failure == nil {
		return
	}
	choice.Available = false
	if choice.Reason == "" {
		choice.Reason = failure.Code
	}
	if len(choice.Failures) < 8 {
		choice.Failures = append(choice.Failures, *failure)
	}
}

// This marker is set only before entering a canonical business command, or
// after its dedicated pre-effect refusal proves no business effects occurred.
// A generic error class or missing returned IDs is not such proof.
type confirmationStepNotStarted struct{ cause error }

func (e *confirmationStepNotStarted) Error() string { return e.cause.Error() }
func (e *confirmationStepNotStarted) Unwrap() error { return e.cause }
