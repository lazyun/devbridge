package mcpserver

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"unicode/utf16"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	c "github.com/lazyun/devbridge/internal/confirmation"
)

// These patterns mirror the Card's JavaScript literals exactly: the content hash
// and fingerprints use a case-insensitive 64-hex test, the headCommit accepts a
// 40- or 64-hex, so the server and the Card never disagree on a boundary value.
var confirmationContentHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
var confirmationHeadCommitPattern = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

// confirmationPayloadHashPattern is deliberately case-SENSITIVE: the Card's
// SUBMISSION_IDENTITY_MISMATCH guard tests payloadSHA256 against
// /^[a-f0-9]{64}$/ (no /i), unlike the case-insensitive content-hash checks.
var confirmationPayloadHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var confirmationCardPurposes = map[string]bool{"PROJECT_SETUP": true, "FEATURE_AREA": true, "TASK": true, "REVIEW": true, "EXECUTION": true}
var confirmationCardStates = map[string]bool{"PENDING": true, "CONFIRMING": true, "COMPLETED": true, "PARTIAL": true, "UNKNOWN": true, "REJECTED": true, "SUPERSEDED": true, "ABANDONED": true}
var confirmationStepStates = map[string]bool{"PENDING": true, "UNKNOWN": true, "COMPLETED": true, "REJECTED": true}
var confirmationStepEffects = map[string]bool{"NOT_STARTED": true, "COMMAND_COMPLETED": true, "RESERVED": true, "RUNTIME_HANDOFF_ACCEPTED": true, "NO_EFFECT_PROVEN": true, "PREPARATION_READY": true, "UNKNOWN": true}
var confirmationFailureSources = map[string]bool{"CORE": true, "DOMAIN": true, "RUNTIME": true, "CONFIRMATION": true, "STORAGE": true}
var confirmationFailureStages = map[string]bool{"INPUT_IDENTITY": true, "PREPARE": true, "CONFIRMATION_PREFLIGHT": true, "UNIQUE_CONSUMPTION": true, "BUSINESS_STEP": true, "DISPATCH": true, "RECEIPT_READBACK": true}
var confirmationNextActionSet = map[string]bool{"EDIT_SELECTION": true, "READ_CURRENT_TARGET": true, "PREPARE_NEW_VERSION": true, "PREPARE_RECOVERY_STATUS": true, "READ_ORIGINAL_RECEIPT": true, "CONTINUE_ORIGINAL_STEPS": true, "ACCESS_BLOCKED": true}
var confirmationSubmissionOutcomes = map[string]bool{"REJECTED_NOT_CONSUMED": true, "CONSUMED": true, "UNKNOWN": true}
var confirmationSubmissionBusinessOutcomes = map[string]bool{"COMMITTED": true, "REJECTED_NO_EFFECT": true, "REJECTED_NOT_CONSUMED": true, "OUTCOME_UNKNOWN": true}
var confirmationSubmissionDispositions = map[string]bool{"CONSUMED": true, "NOT_CONSUMED": true, "UNKNOWN": true}

// confirmationRetryDispositions mirrors confirmation.RetryDisposition. The value
// is Core-derived; a public envelope may never carry one that its own facts
// contradict, so the Card and the mount gate reject the combination, not just the
// unknown spelling.
var confirmationRetryDispositions = map[string]bool{"NONE": true, "EDIT_SELECTION_AND_RESUBMIT": true, "REPREPARE_REQUIRED": true, "READ_ORIGINAL_RECEIPT": true}
var confirmationModelDefaultStates = map[string]bool{"THREADLESS": true, "KNOWN": true, "HISTORICAL_UNKNOWN": true, "READ_UNAVAILABLE": true}

// bodyKeyForPurpose mirrors the Card's purpose→content-body map.
func bodyKeyForPurpose(purpose string) string {
	switch purpose {
	case "PROJECT_SETUP":
		return "projectSetup"
	case "FEATURE_AREA":
		return "featureArea"
	case "TASK":
		return "task"
	case "REVIEW":
		return "review"
	case "EXECUTION":
		return "execution"
	}
	return ""
}

// ---- JSON predicates that match the Card's obj/str/own/Array.isArray/isNumber ----

func jsonStrOK(m map[string]any, k string) (string, bool) { s, ok := m[k].(string); return s, ok }
func jsonNonemptyStr(m map[string]any, k string) bool     { s, ok := m[k].(string); return ok && s != "" }

// jsonIsStr mirrors the Card's bare `str(x)` guard: the value must be present and
// a string, but the empty string is ACCEPTED. The Card uses this wherever it does
// NOT also test `!x`, so the server must not tighten it to non-empty or it would
// refuse to mount a Card-bindable entry.
func jsonIsStr(m map[string]any, k string) bool        { _, ok := m[k].(string); return ok }
func jsonBool(m map[string]any, k string) (bool, bool) { b, ok := m[k].(bool); return b, ok }
func jsonObject(m map[string]any, k string) (map[string]any, bool) {
	o, ok := m[k].(map[string]any)
	return o, ok && o != nil
}
func jsonArray(m map[string]any, k string) ([]any, bool) { a, ok := m[k].([]any); return a, ok }
func jsonHas(m map[string]any, k string) bool            { _, ok := m[k]; return ok }

// maxSafeJSONInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53-1). The Card
// gates several counters with Number.isSafeInteger, so a Go validator that only
// tests integrality would accept values the Card rejects.
const maxSafeJSONInteger = 9007199254740991

// jsonIntegerAtLeast mirrors the Card's Number.isSafeInteger(v) && v >= min:
// present, finite, integral, within the safe-integer range and not below min.
// A non-finite or out-of-range value is a plain false (rejected), never a panic.
func jsonIntegerAtLeast(m map[string]any, k string, min float64) bool {
	n, ok := m[k].(float64)
	if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
		return false
	}
	return n == math.Trunc(n) && n >= min && n <= maxSafeJSONInteger
}
func jsonIn(m map[string]any, k string, set map[string]bool) bool {
	s, ok := m[k].(string)
	return ok && set[s]
}
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case []any:
		return true
	case map[string]any:
		return true
	}
	return true
}
func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

// jsonCanonicalBytes renders a decoded value to a stable byte form so two values
// can be compared for structural equality the way JSON.stringify does.
func jsonCanonicalBytes(v any) string {
	if v == nil {
		return "null"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "\x00"
	}
	return string(raw)
}

// sameJSONValue compares two decoded JSON values by their canonical byte form.
// It is used everywhere the Card uses === / !== on dynamically-typed fields,
// because Go's == on two interface values panics when both hold an uncomparable
// dynamic type (map / slice). Canonical marshaling is panic-free and, for the
// scalar/absent values the server actually emits, is identical to ===: equal
// strings/numbers/bools/null marshal to equal bytes, differing values to
// different bytes.
func sameJSONValue(a, b any) bool { return jsonCanonicalBytes(a) == jsonCanonicalBytes(b) }

// optionalObjectField mirrors the Card's `v && (!obj(v) -> reject)` guard: when a
// field is present and truthy it must be a JSON object, and the caller then
// validates it. It returns (obj, needsCheck) where needsCheck reports the Card
// entered its guard (truthy) and obj is nil precisely when the value is truthy
// but not an object — the case the single-read Go gate previously skipped and so
// could mount an entry the Card then rejected.
func optionalObjectField(m map[string]any, k string) (map[string]any, bool, bool) {
	value := m[k]
	if !truthy(value) {
		return nil, false, false
	}
	obj, ok := value.(map[string]any)
	if !ok || obj == nil {
		return nil, true, true
	}
	return obj, true, false
}

// ValidateConfirmationCardEntryProjection checks the FINAL public envelope that
// is handed to the Host/Card after canonicalization and JSON marshaling — the
// exact map[string]any the Card decodes — so the server and the Card never drift.
// It returns nil when the Card would bind the entry on first acceptEntry, and a
// typed, content-free failure otherwise. It never leaks task bodies, feedback,
// credentials, SQL or absolute paths.
func ValidateConfirmationCardEntryProjection(result c.Result) *c.Failure {
	raw, err := json.Marshal(result)
	if err != nil {
		return cardEntryProjectionFailure("ENTRY_MARSHAL_FAILED", "$")
	}
	return ValidateConfirmationCardEntryProjectionJSON(raw)
}

// ValidateConfirmationCardEntryProjectionJSON validates a raw envelope (the bytes
// the Card decodes). Production marshals c.Result and calls this; the shared
// contract-vector harness feeds mutated JSON straight in so both Go and the Card
// validate byte-identical input.
func ValidateConfirmationCardEntryProjectionJSON(raw []byte) (failure *c.Failure) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = cardEntryProjectionFailure("ENTRY_PROJECTION_PANIC", "$")
		}
	}()
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return cardEntryProjectionFailure("ENTRY_MARSHAL_FAILED", "$")
	}
	return validateCardEntryEnvelope(envelope)
}

// validateCardEntryEnvelope mirrors, in exact Card order, the envelope-derivable
// (MOUNT_ABSOLUTE) portion of decode → validateIntent → validateSubmission. The
// three entry-relative staleness checks and the pendingCommit selection conflict
// are deliberately NOT reproduced here; they require a previously bound entry the
// single-read Host mount gate does not hold (see the BOUND_ENTRY_RELATIVE vectors).
func validateCardEntryEnvelope(envelope map[string]any) *c.Failure {
	// decode(): envelope shape.
	if len(envelope) == 0 {
		return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "$")
	}
	for key := range envelope {
		switch key {
		case "intent", "options", "failure", "display":
		default:
			// Fixed structural path: mirror the Card, which rejects an unknown
			// envelope member without echoing its name (§5 content-free error).
			return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "$.unknownMember")
		}
	}
	if jsonHas(envelope, "intent") {
		if _, ok := jsonObject(envelope, "intent"); !ok {
			return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "intent")
		}
	}
	if jsonHas(envelope, "options") {
		if _, ok := jsonObject(envelope, "options"); !ok {
			return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "options")
		}
	}
	if jsonHas(envelope, "failure") {
		if _, ok := jsonObject(envelope, "failure"); !ok {
			return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "failure")
		}
	}
	// decode(): a Card entry envelope carries intent OR options, never both.
	if jsonHas(envelope, "intent") && jsonHas(envelope, "options") {
		return cardEntryProjectionFailure("CONFIRMATION_ENVELOPE_INVALID", "intent/options")
	}
	if failureValue, ok := jsonObject(envelope, "failure"); ok && envelope["failure"] != nil {
		if code := validateFailureShape(failureValue); code != "" {
			return cardEntryProjectionFailure(code, "failure")
		}
	}
	intent, ok := jsonObject(envelope, "intent")
	if !ok {
		// A no-intent envelope never binds a fresh Card entry, so refuse the mount.
		return cardEntryProjectionFailure("INTENT_MISSING", "intent")
	}

	// identity(v)
	aiSessionID, _ := jsonStrOK(intent, "aiSessionId")
	intentID, _ := jsonStrOK(intent, "intentId")
	purpose, _ := jsonStrOK(intent, "purpose")
	version := intent["version"]
	if !identityValid(intent) {
		return cardEntryProjectionFailure("ENTRY_IDENTITY_INVALID", "intent")
	}
	content, ok := jsonObject(intent, "content")
	if !ok {
		return cardEntryProjectionFailure("CONTENT_MISSING", "intent.content")
	}
	if _, ok := jsonStrOK(intent, "projectId"); !ok {
		return cardEntryProjectionFailure("PROJECT_ID_MISSING", "intent.projectId")
	}
	if hash, ok := jsonStrOK(intent, "contentHash"); !ok || !confirmationContentHashPattern.MatchString(hash) {
		return cardEntryProjectionFailure("CONTENT_HASH_INVALID", "intent.contentHash")
	}
	if !jsonIn(intent, "state", confirmationCardStates) {
		return cardEntryProjectionFailure("STATE_INVALID", "intent.state")
	}
	if _, isBool := jsonBool(intent, "canResume"); !isBool {
		return cardEntryProjectionFailure("CAN_RESUME_TYPE", "intent.canResume")
	}
	// content identity
	contentSession, _ := jsonStrOK(content, "aiSessionId")
	contentPurpose, _ := jsonStrOK(content, "purpose")
	contentAction, contentActionStr := jsonStrOK(content, "action")
	intentAction, _ := jsonStrOK(intent, "action")
	if contentSession != aiSessionID || contentPurpose != purpose || !contentActionStr || intentAction != contentAction {
		return cardEntryProjectionFailure("CONTENT_IDENTITY_MISMATCH", "intent.content.action")
	}
	variants, ok := jsonArray(content, "variants")
	if !ok {
		return cardEntryProjectionFailure("VARIANTS_MISSING", "intent.content.variants")
	}
	steps, ok := jsonArray(intent, "steps")
	if !ok {
		return cardEntryProjectionFailure("STEPS_MISSING", "intent.steps")
	}
	bodyKey := bodyKeyForPurpose(purpose)
	if _, ok := jsonObject(content, bodyKey); !ok {
		return cardEntryProjectionFailure("PURPOSE_CONTENT_MISSING", "intent.content."+bodyKey)
	}
	// variant identity + duplicate detection (Card folds duplicates into VARIANT_SHAPE_INVALID)
	seenVariants := map[string]bool{}
	for i, item := range variants {
		variant, ok := item.(map[string]any)
		if !ok || variant == nil {
			return cardEntryProjectionFailure("VARIANT_SHAPE_INVALID", jsonPathIndex("intent.content.variants", i))
		}
		id, idStr := jsonStrOK(variant, "id")
		_, labelStr := jsonStrOK(variant, "label")
		_, summaryStr := jsonStrOK(variant, "summary")
		if !idStr || id == "" || seenVariants[id] || !labelStr || !summaryStr {
			return cardEntryProjectionFailure("VARIANT_SHAPE_INVALID", jsonPathIndex("intent.content.variants", i))
		}
		seenVariants[id] = true
	}
	// currentUpperReadAvailable, when present, must be a boolean
	if jsonHas(intent, "currentUpperReadAvailable") {
		if _, isBool := intent["currentUpperReadAvailable"].(bool); !isBool {
			return cardEntryProjectionFailure("UPPER_READ_QUALIFICATION_INVALID", "intent.currentUpperReadAvailable")
		}
	}
	canResume, _ := jsonBool(intent, "canResume")
	commit, commitPresent := jsonObject(intent, "commit")
	if commitPresent && commit == nil {
		commitPresent = false
	}
	if canResume && !commitPresent {
		return cardEntryProjectionFailure("RESUME_RECEIPT_MISSING", "intent.commit")
	}
	if commitPresent {
		if !identityValid(commit) {
			return cardEntryProjectionFailure("ENTRY_IDENTITY_INVALID", "intent.commit")
		}
		if !sameIdentity(commit, aiSessionID, intentID, purpose, version) ||
			!sameJSONValue(commit["contentHash"], intent["contentHash"]) ||
			!sameJSONValue(commit["action"], content["action"]) ||
			!seenVariants[strOr(commit["variantId"])] ||
			!jsonIsStr(commit, "requestId") {
			return cardEntryProjectionFailure("RECEIPT_IDENTITY_MISMATCH", "intent.commit")
		}
		commitSelection, selOK := jsonObject(commit, "selection")
		if !selOK {
			return cardEntryProjectionFailure("RECEIPT_IDENTITY_MISMATCH", "intent.commit.selection")
		}
		if jsonHas(commitSelection, "reviewMode") {
			mode, ok := commitSelection["reviewMode"].(string)
			if !ok || (mode != "UPPER_LLM" && mode != "CODING_AGENT" && mode != "NONE") {
				return cardEntryProjectionFailure("RECEIPT_IDENTITY_MISMATCH", "intent.commit.selection.reviewMode")
			}
		}
		if jsonHas(commitSelection, "startReview") {
			if _, ok := jsonBool(commitSelection, "startReview"); !ok {
				return cardEntryProjectionFailure("RECEIPT_IDENTITY_MISMATCH", "intent.commit.selection.startReview")
			}
		}
		if frozen, entered, invalid := optionalObjectField(commit, "backendSelection"); entered {
			if invalid {
				return cardEntryProjectionFailure("FROZEN_BACKEND_SHAPE", "intent.commit.backendSelection")
			}
			narrow, narrowPresent := jsonObject(commitSelection, "backend")
			frozenConfigID, _ := jsonStrOK(frozen, "configId")
			frozenFP, _ := jsonStrOK(frozen, "configFingerprint")
			narrowConfigID, _ := jsonStrOK(narrow, "configId")
			narrowFP, _ := jsonStrOK(narrow, "configFingerprint")
			if !jsonIsStr(frozen, "configId") ||
				!confirmationContentHashPattern.MatchString(frozenFP) ||
				!jsonIsStr(frozen, "configName") ||
				!jsonIsStr(frozen, "source") ||
				!narrowPresent ||
				frozenConfigID != narrowConfigID ||
				frozenFP != narrowFP {
				return cardEntryProjectionFailure("FROZEN_BACKEND_SHAPE", "intent.commit.backendSelection")
			}
		}
		// CONFIRMATION_SELECTION_CONFLICT vs pendingCommit is BOUND_ENTRY_RELATIVE.
	}
	// step element shape + nested failure
	for i, item := range steps {
		step, ok := item.(map[string]any)
		if !ok || step == nil {
			return cardEntryProjectionFailure("STEP_SHAPE_INVALID", jsonPathIndex("intent.steps", i))
		}
		stateVal, _ := step["state"].(string)
		_, effectPresent := step["effect"]
		effectVal, _ := step["effect"].(string)
		if !jsonIsStr(step, "kind") ||
			!jsonIsStr(step, "requestId") ||
			!confirmationStepStates[stateVal] ||
			(effectPresent && !confirmationStepEffects[effectVal]) {
			return cardEntryProjectionFailure("STEP_SHAPE_INVALID", jsonPathIndex("intent.steps", i))
		}
		if failureValue := step["failure"]; truthy(failureValue) {
			failureObj, _ := failureValue.(map[string]any)
			if code := validateFailureShape(failureObj); code != "" {
				return cardEntryProjectionFailure(code, jsonPathIndex("intent.steps", i)+".failure")
			}
		}
	}
	if code, path := validateAccessAbsolute(intent, steps, content); code != "" {
		return cardEntryProjectionFailure(code, path)
	}
	if submission, entered, invalid := optionalObjectField(intent, "submission"); entered {
		if invalid {
			// Card: `!submission` skips (falsy), else `!obj(submission)` rejects.
			return cardEntryProjectionFailure("SUBMISSION_SHAPE_INVALID", "intent.submission")
		}
		if code, path := validateSubmissionAbsolute(intent, content, seenVariants, commit, submission); code != "" {
			return cardEntryProjectionFailure(code, path)
		}
	}
	return nil
}

func strOr(v any) string { s, _ := v.(string); return s }

func identityValid(m map[string]any) bool {
	if m == nil {
		return false
	}
	if !jsonNonemptyStr(m, "aiSessionId") || !jsonNonemptyStr(m, "intentId") {
		return false
	}
	n, ok := m["version"].(float64)
	if !ok || n != math.Trunc(n) || n < 1 {
		return false
	}
	return confirmationCardPurposes[strOr(m["purpose"])]
}

// sameIdentity compares a decoded sub-object against the intent's identity.
func sameIdentity(m map[string]any, aiSessionID, intentID, purpose string, version any) bool {
	return strOr(m["aiSessionId"]) == aiSessionID &&
		strOr(m["intentId"]) == intentID &&
		sameJSONValue(m["version"], version) &&
		strOr(m["purpose"]) == purpose
}

// validateFailureShape mirrors the Card's validateFailure predicate. Returns ""
// when valid, otherwise the reason code FAILURE_SHAPE_INVALID.
func validateFailureShape(failure map[string]any) string {
	if failure == nil {
		return "FAILURE_SHAPE_INVALID"
	}
	for key := range failure {
		switch key {
		case "code", "reasonCode", "source", "stage", "message", "nextAction":
		default:
			return "FAILURE_SHAPE_INVALID"
		}
	}
	code, codeStr := jsonStrOK(failure, "code")
	if !codeStr || code == "" || jsLen(code) > 128 {
		return "FAILURE_SHAPE_INVALID"
	}
	if jsonHas(failure, "reasonCode") && !jsonNonemptyStr(failure, "reasonCode") {
		return "FAILURE_SHAPE_INVALID"
	}
	if !jsonIn(failure, "source", confirmationFailureSources) {
		return "FAILURE_SHAPE_INVALID"
	}
	if !jsonIn(failure, "stage", confirmationFailureStages) {
		return "FAILURE_SHAPE_INVALID"
	}
	message, messageStr := jsonStrOK(failure, "message")
	if !messageStr || message == "" || jsLen(message) > 1024 {
		return "FAILURE_SHAPE_INVALID"
	}
	if !jsonIn(failure, "nextAction", confirmationNextActionSet) {
		return "FAILURE_SHAPE_INVALID"
	}
	return ""
}

// validateAccessAbsolute mirrors the Card's validateAccess(value): the
// currentDirectFile and currentUpperReview identity/workflow/member-set checks.
func validateAccessAbsolute(intent map[string]any, steps []any, content map[string]any) (string, string) {
	if file, entered, invalid := optionalObjectField(intent, "currentDirectFile"); entered {
		if invalid {
			return "CURRENT_FILE_IDENTITY_MISMATCH", "intent.currentDirectFile"
		}
		owners := map[string]bool{}
		revisions := map[string]bool{}
		workflows := map[string]bool{}
		anyWorkflow := false
		for _, item := range steps {
			step, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if truthy(step["taskId"]) {
				owners[strOr(step["taskId"])] = true
			}
			if truthy(step["revisionId"]) {
				revisions[strOr(step["revisionId"])] = true
			}
			if truthy(step["workflowId"]) {
				workflows[strOr(step["workflowId"])] = true
				anyWorkflow = true
			}
		}
		if execution, ok := jsonObject(content, "execution"); ok {
			if truthy(execution["taskId"]) {
				owners[strOr(execution["taskId"])] = true
			}
			if truthy(execution["taskDefinitionRevisionId"]) {
				revisions[strOr(execution["taskDefinitionRevisionId"])] = true
			}
		}
		_, stateStr := jsonStrOK(file, "state")
		_, enabledBool := jsonBool(file, "enabled")
		fileAISession := strOr(file["aiSessionId"])
		fileProject := strOr(file["projectId"])
		if !stateStr ||
			!enabledBool ||
			!owners[strOr(file["taskId"])] ||
			!revisions[strOr(file["taskDefinitionRevisionId"])] ||
			(fileAISession != "" && fileAISession != strOr(intent["aiSessionId"])) ||
			(fileProject != "" && fileProject != strOr(intent["projectId"])) {
			return "CURRENT_FILE_IDENTITY_MISMATCH", "intent.currentDirectFile"
		}
		if anyWorkflow && !workflows[strOr(file["workflowId"])] {
			return "CURRENT_FILE_WORKFLOW_MISMATCH", "intent.currentDirectFile.workflowId"
		}
		if pv, ok := file["protocolVersion"].(float64); ok && pv == 2 {
			members, isArray := jsonArray(file, "repositoryTargets")
			_, epochStr := jsonStrOK(file, "runtimeEpoch")
			badMembers := false
			for _, item := range members {
				member, ok := item.(map[string]any)
				if !ok {
					badMembers = true
					continue
				}
				if !jsonIsStr(member, "repositoryId") || !jsonIsStr(member, "memberId") || !jsonIntegerAtLeast(member, "memberVersion", 1) {
					badMembers = true
				}
			}
			if !epochStr ||
				!jsonIntegerAtLeast(file, "grantGeneration", 1) ||
				!isArray ||
				len(members) == 0 ||
				badMembers {
				return "CURRENT_FILE_MEMBER_SET_INVALID", "intent.currentDirectFile.repositoryTargets"
			}
		}
	}
	if upper, entered, invalid := optionalObjectField(intent, "currentUpperReview"); entered {
		if invalid {
			return "CURRENT_REVIEW_IDENTITY_MISMATCH", "intent.currentUpperReview"
		}
		target, targetOK := jsonObject(upper, "target")
		stepHasTarget := false
		for _, item := range steps {
			step, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if sameJSONValue(step["reviewId"], target["reviewId"]) && sameJSONValue(step["runId"], target["runId"]) {
				stepHasTarget = true
				break
			}
		}
		_, stateStr := jsonStrOK(upper, "state")
		if !targetOK ||
			strOr(target["aiSessionId"]) != strOr(intent["aiSessionId"]) ||
			!stepHasTarget ||
			!stateStr {
			return "CURRENT_REVIEW_IDENTITY_MISMATCH", "intent.currentUpperReview"
		}
	}
	return "", ""
}

// validateSubmissionAbsolute mirrors validateSubmission's envelope-derivable
// checks (excluding the pendingCommit SUBMISSION_REQUEST_CONFLICT, which is
// BOUND_ENTRY_RELATIVE).
func validateSubmissionAbsolute(intent, content map[string]any, variantIDs map[string]bool, commit map[string]any, submission map[string]any) (string, string) {
	allow := map[string]bool{"request": true, "payloadSHA256": true, "outcome": true, "recorded": true, "failure": true, "nextAction": true, "receiptRequestId": true, "businessOutcome": true, "consumptionDisposition": true, "consumed": true, "redispatchSameIntent": true, "reprepareAllowed": true, "retryDisposition": true}
	for key := range submission {
		if !allow[key] {
			return "SUBMISSION_SHAPE_INVALID", "intent.submission"
		}
	}
	if !jsonIn(submission, "outcome", confirmationSubmissionOutcomes) ||
		!jsonIn(submission, "businessOutcome", confirmationSubmissionBusinessOutcomes) ||
		!jsonIn(submission, "consumptionDisposition", confirmationSubmissionDispositions) ||
		!jsonIn(submission, "retryDisposition", confirmationRetryDispositions) {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission"
	}
	if _, ok := jsonBool(submission, "recorded"); !ok {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.recorded"
	}
	if _, ok := jsonBool(submission, "redispatchSameIntent"); !ok {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.redispatchSameIntent"
	}
	if _, ok := jsonBool(submission, "reprepareAllowed"); !ok {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.reprepareAllowed"
	}
	if redispatch, _ := jsonBool(submission, "redispatchSameIntent"); redispatch != false {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.redispatchSameIntent"
	}
	if !jsonIn(submission, "nextAction", confirmationNextActionSet) {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.nextAction"
	}
	if _, ok := jsonStrOK(submission, "receiptRequestId"); !ok {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.receiptRequestId"
	}
	if _, ok := jsonStrOK(submission, "payloadSHA256"); !ok {
		return "SUBMISSION_SHAPE_INVALID", "intent.submission.payloadSHA256"
	}
	if failureValue := submission["failure"]; truthy(failureValue) {
		failureObj, _ := failureValue.(map[string]any)
		if code := validateFailureShape(failureObj); code != "" {
			return code, "intent.submission.failure"
		}
	}
	recorded, _ := jsonBool(submission, "recorded")
	outcome, _ := jsonStrOK(submission, "outcome")
	retry, _ := jsonStrOK(submission, "retryDisposition")
	if !recorded {
		business, _ := jsonStrOK(submission, "businessOutcome")
		disposition, _ := jsonStrOK(submission, "consumptionDisposition")
		reprepare, _ := jsonBool(submission, "reprepareAllowed")
		if outcome != "UNKNOWN" || business != "OUTCOME_UNKNOWN" || disposition != "UNKNOWN" || jsonHas(submission, "consumed") || reprepare {
			return "UNRECORDED_OUTCOME_INVALID", "intent.submission"
		}
		// An unobserved request can never route to an editable retry: it is not
		// evidence of non-consumption, only of an unread result.
		if retry != "READ_ORIGINAL_RECEIPT" {
			return "RETRY_DISPOSITION_CONFLICT", "intent.submission.retryDisposition"
		}
		return "", ""
	}
	hasFailure := truthy(submission["failure"])
	if outcome == "UNKNOWN" || (outcome == "REJECTED_NOT_CONSUMED" && !hasFailure) {
		return "RECORDED_OUTCOME_INVALID", "intent.submission"
	}
	request, _ := jsonObject(submission, "request")
	if code, path := validateRequestShape(request); code != "" {
		return code, path
	}
	contentHashMatches := sameJSONValue(request["contentHash"], intent["contentHash"])
	actionMatches := sameJSONValue(request["action"], intent["action"])
	variantInIntent := variantIDs[strOr(request["variantId"])]
	_, requestSelectionOK := jsonObject(request, "selection")
	payload, _ := jsonStrOK(submission, "payloadSHA256")
	if request == nil ||
		!sameIdentity(request, strOr(intent["aiSessionId"]), strOr(intent["intentId"]), strOr(intent["purpose"]), intent["version"]) ||
		!contentHashMatches || !actionMatches ||
		!jsonNonemptyStr(request, "requestId") || !jsonIsStr(request, "variantId") ||
		!variantInIntent || !requestSelectionOK ||
		!confirmationPayloadHashPattern.MatchString(payload) {
		return "SUBMISSION_IDENTITY_MISMATCH", "intent.submission.request"
	}
	// SUBMISSION_REQUEST_CONFLICT vs pendingCommit is BOUND_ENTRY_RELATIVE.
	if outcome == "CONSUMED" {
		commitPresent := commit != nil
		// The Card compares submission.receiptRequestId against the intent's frozen
		// commit.requestId, not the request's own id.
		receiptMatches := commitPresent && sameJSONValue(commit["requestId"], submission["receiptRequestId"])
		business, _ := jsonStrOK(submission, "businessOutcome")
		disposition, _ := jsonStrOK(submission, "consumptionDisposition")
		consumed, consumedBool := jsonBool(submission, "consumed")
		if !commitPresent || !receiptMatches || !sameCommit(commit, request) || (business != "COMMITTED" && business != "REJECTED_NO_EFFECT") || disposition != "CONSUMED" || !consumedBool || consumed != true {
			return "CONSUMPTION_RECEIPT_MISMATCH", "intent.submission"
		}
		if business == "REJECTED_NO_EFFECT" {
			raw, err := json.Marshal(intent)
			var proof c.Intent
			if err != nil || json.Unmarshal(raw, &proof) != nil || !c.RejectedContinuationWithoutEffect(&proof) || strOr(submission["nextAction"]) != "READ_CURRENT_TARGET" || submission["reprepareAllowed"] != false {
				return "REJECTED_NO_EFFECT_PROOF_INVALID", "intent.submission"
			}
		}
		// A consumed choice has no retry to advertise.
		if retry != "NONE" {
			return "RETRY_DISPOSITION_CONFLICT", "intent.submission.retryDisposition"
		}
	}
	if outcome == "REJECTED_NOT_CONSUMED" {
		business, _ := jsonStrOK(submission, "businessOutcome")
		disposition, _ := jsonStrOK(submission, "consumptionDisposition")
		consumed, _ := jsonBool(submission, "consumed")
		if jsonNonemptyStr(submission, "receiptRequestId") || business != "REJECTED_NOT_CONSUMED" || disposition != "NOT_CONSUMED" || consumed != false {
			return "REJECTION_RECEIPT_CONFLICT", "intent.submission"
		}
		// EDIT_SELECTION_AND_RESUBMIT is the only value that re-arms a Card
		// confirmation control, so every §3 fact behind it is checked here rather
		// than left to a consumer's own inference. The expiry fact is deliberately
		// not re-derived at the read boundary; Core's commit preflight still owns it.
		if retry == "EDIT_SELECTION_AND_RESUBMIT" {
			nextAction, _ := jsonStrOK(submission, "nextAction")
			if strOr(intent["state"]) != "PENDING" || commit != nil || nextAction != "EDIT_SELECTION" {
				return "RETRY_DISPOSITION_CONFLICT", "intent.submission.retryDisposition"
			}
		}
	}
	return "", ""
}

// validateRequestShape mirrors the Card's validateRequestShape.
func validateRequestShape(request map[string]any) (string, string) {
	keys := []string{"aiSessionId", "intentId", "version", "purpose", "action", "requestId", "contentHash", "variantId", "selection"}
	if request == nil {
		return "SUBMISSION_REQUEST_SHAPE", "intent.submission.request"
	}
	for _, required := range keys {
		if !jsonHas(request, required) {
			return "SUBMISSION_REQUEST_SHAPE", "intent.submission.request"
		}
	}
	keySet := map[string]bool{}
	for _, required := range keys {
		keySet[required] = true
	}
	for key := range request {
		if !keySet[key] {
			return "SUBMISSION_REQUEST_SHAPE", "intent.submission.request"
		}
	}
	selection, ok := jsonObject(request, "selection")
	if !ok {
		return "SUBMISSION_REQUEST_SHAPE", "intent.submission.request.selection"
	}
	selectionAllowed := map[string]bool{"reviewMode": true, "startReview": true, "projectId": true, "repositoryId": true, "developmentRef": true, "backend": true, "runtimeConfig": true, "verificationWaiver": true}
	for key := range selection {
		if !selectionAllowed[key] {
			return "SUBMISSION_REQUEST_SHAPE", "intent.submission.request.selection"
		}
	}
	if jsonHas(selection, "reviewMode") {
		mode, ok := selection["reviewMode"].(string)
		if !ok || (mode != "UPPER_LLM" && mode != "CODING_AGENT" && mode != "NONE") {
			return "SUBMISSION_REVIEW_MODE", "intent.submission.request.selection.reviewMode"
		}
	}
	if jsonHas(selection, "startReview") {
		if _, ok := jsonBool(selection, "startReview"); !ok {
			return "SUBMISSION_SELECTION_SHAPE", "intent.submission.request.selection.startReview"
		}
	}
	if jsonHas(selection, "backend") {
		backend, isObj := selection["backend"].(map[string]any)
		if !isObj || backend == nil {
			return "SUBMISSION_BACKEND_SHAPE", "intent.submission.request.selection.backend"
		}
		for key := range backend {
			if key != "configId" && key != "configFingerprint" {
				return "SUBMISSION_BACKEND_SHAPE", "intent.submission.request.selection.backend"
			}
		}
		configFP, _ := jsonStrOK(backend, "configFingerprint")
		if !jsonIsStr(backend, "configId") || !confirmationContentHashPattern.MatchString(configFP) {
			return "SUBMISSION_BACKEND_SHAPE", "intent.submission.request.selection.backend"
		}
	}
	for _, key := range []string{"projectId", "repositoryId", "developmentRef"} {
		if jsonHas(selection, key) && !jsonNonemptyStr(selection, key) {
			return "SUBMISSION_SELECTION_SHAPE", "intent.submission.request.selection." + key
		}
	}
	if jsonHas(selection, "runtimeConfig") {
		config, isObj := selection["runtimeConfig"].(map[string]any)
		if !isObj || config == nil {
			return "SUBMISSION_RUNTIME_SHAPE", "intent.submission.request.selection.runtimeConfig"
		}
		for key := range config {
			switch key {
			case "model", "reasoningEffort", "codexFastMode", "serviceTier", "agentId", "executionPolicy", "agentConfigFingerprint":
			default:
				return "SUBMISSION_RUNTIME_SHAPE", "intent.submission.request.selection.runtimeConfig"
			}
		}
		_, fastBool := jsonBool(config, "codexFastMode")
		_, serviceTierStr := jsonStrOK(config, "serviceTier")
		if !jsonNonemptyStr(config, "model") || !jsonNonemptyStr(config, "reasoningEffort") || !fastBool ||
			(jsonHas(config, "serviceTier") && !serviceTierStr) || !validRuntimePolicyShape(config) {
			return "SUBMISSION_RUNTIME_SHAPE", "intent.submission.request.selection.runtimeConfig"
		}
	}
	if jsonHas(selection, "verificationWaiver") {
		waiver, isObj := selection["verificationWaiver"].(map[string]any)
		if !isObj || waiver == nil {
			return "SUBMISSION_WAIVER_SHAPE", "intent.submission.request.selection.verificationWaiver"
		}
		for key := range waiver {
			if key != "source" && key != "reason" {
				return "SUBMISSION_WAIVER_SHAPE", "intent.submission.request.selection.verificationWaiver"
			}
		}
		reason, reasonStr := jsonStrOK(waiver, "reason")
		if strOr(waiver["source"]) != "USER_WAIVED" || !reasonStr || len([]rune(reason)) > 2048 {
			return "SUBMISSION_WAIVER_SHAPE", "intent.submission.request.selection.verificationWaiver"
		}
	}
	return "", ""
}

// sameCommit mirrors the Card's sameCommit(a, b) used by the CONSUMED receipt
// equality check, comparing only the fields the Card compares.
func sameCommit(a, b map[string]any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	aSelection, _ := jsonObject(a, "selection")
	bSelection, _ := jsonObject(b, "selection")
	if !sameJSONValue(selectionValue(aSelection, "reviewMode"), selectionValue(bSelection, "reviewMode")) {
		return false
	}
	if !sameJSONValue(selectionValue(aSelection, "startReview"), selectionValue(bSelection, "startReview")) {
		return false
	}
	aContexts := selectionField(aSelection, "developmentContexts", "[]")
	bContexts := selectionField(bSelection, "developmentContexts", "[]")
	if aContexts != bContexts {
		return false
	}
	aWaiver := selectionField(aSelection, "verificationWaiver", "null")
	bWaiver := selectionField(bSelection, "verificationWaiver", "null")
	if aWaiver != bWaiver {
		return false
	}
	if !sameIdentity(a, strOr(b["aiSessionId"]), strOr(b["intentId"]), strOr(b["purpose"]), b["version"]) {
		return false
	}
	if !sameJSONValue(a["action"], b["action"]) || !sameJSONValue(a["contentHash"], b["contentHash"]) || !sameJSONValue(a["variantId"], b["variantId"]) {
		return false
	}
	for _, key := range []string{"projectId", "repositoryId", "developmentRef"} {
		if strOr(selectionValue(aSelection, key)) != strOr(selectionValue(bSelection, key)) {
			return false
		}
	}
	aBackend, aHas := jsonObject(aSelection, "backend")
	bBackend, bHas := jsonObject(bSelection, "backend")
	if aHas != bHas {
		return false
	}
	if aHas && (strOr(aBackend["configId"]) != strOr(bBackend["configId"]) || strOr(aBackend["configFingerprint"]) != strOr(bBackend["configFingerprint"])) {
		return false
	}
	aRuntime, aRuntimeHas := jsonObject(aSelection, "runtimeConfig")
	bRuntime, bRuntimeHas := jsonObject(bSelection, "runtimeConfig")
	if aRuntimeHas != bRuntimeHas {
		return false
	}
	if aRuntimeHas && (strOr(aRuntime["model"]) != strOr(bRuntime["model"]) || strOr(aRuntime["reasoningEffort"]) != strOr(bRuntime["reasoningEffort"]) || !sameJSONValue(aRuntime["codexFastMode"], bRuntime["codexFastMode"]) || strOr(aRuntime["agentId"]) != strOr(bRuntime["agentId"]) || strOr(aRuntime["agentConfigFingerprint"]) != strOr(bRuntime["agentConfigFingerprint"]) || strOr(aRuntime["executionPolicy"]) != strOr(bRuntime["executionPolicy"])) {
		return false
	}
	return true
}

func selectionValue(selection map[string]any, key string) any {
	if selection == nil {
		return nil
	}
	return selection[key]
}

func selectionField(selection map[string]any, key, empty string) string {
	value := selectionValue(selection, key)
	if value == nil {
		return empty
	}
	return jsonCanonicalBytes(value)
}

func jsonPathIndex(base string, i int) string {
	return base + "[" + strconv.Itoa(i) + "]"
}

// ValidateConfirmationOptionsProjection checks the Card-required collections of a
// public options envelope (from confirmation_options_get / the commit preflight
// readOptions) against the entry-independent subset of the Card's validateOptions.
// The OPTIONS_IDENTITY_MISMATCH, query match and the CONFIGURE_DEVELOPMENT_CONTEXT
// branch require a bound entry/query the single options read does not carry, so
// they stay with the Card (BOUND_ENTRY_RELATIVE) and are not claimed here.
func ValidateConfirmationOptionsProjection(result c.Result) *c.Failure {
	raw, err := json.Marshal(result)
	if err != nil {
		return optionsProjectionFailure("ENTRY_MARSHAL_FAILED", "$")
	}
	return ValidateConfirmationOptionsProjectionJSON(raw)
}

// ValidateConfirmationOptionsProjectionJSON validates a raw options envelope (the
// bytes the Card decodes) so the shared contract-vector harness feeds byte-identical
// JSON to both the Go gate and the Card's validateOptions.
func ValidateConfirmationOptionsProjectionJSON(raw []byte) (failure *c.Failure) {
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = optionsProjectionFailure("OPTIONS_PROJECTION_PANIC", "$")
		}
	}()
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return optionsProjectionFailure("ENTRY_MARSHAL_FAILED", "$")
	}
	options, ok := jsonObject(envelope, "options")
	if !ok {
		return optionsProjectionFailure("OPTIONS_MISSING", "options")
	}
	if code, path := validateOptionsAbsolute(options); code != "" {
		return optionsProjectionFailure(code, path)
	}
	return nil
}

func validateOptionsAbsolute(options map[string]any) (string, string) {
	if jsonHas(options, "startReview") {
		if _, ok := jsonBool(options, "startReview"); !ok {
			return "TASK_REVIEW_OPTIONS_INVALID", "options.startReview"
		}
	}
	eligibility, ok := jsonObject(options, "eligibility")
	if !ok {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility"
	}
	if _, present := jsonBool(eligibility, "available"); !present {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.available"
	}
	if _, present := jsonBool(eligibility, "reprepareAllowed"); !present {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.reprepareAllowed"
	}
	if !jsonIsStr(eligibility, "attemptState") || !jsonIsStr(eligibility, "receiptState") {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility"
	}
	if jsonHas(eligibility, "reasonCode") && !jsonNonemptyStr(eligibility, "reasonCode") {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.reasonCode"
	}
	if jsonHas(eligibility, "reasonMessage") && !jsonNonemptyStr(eligibility, "reasonMessage") {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.reasonMessage"
	}
	if jsonHas(eligibility, "nextAction") && !jsonIn(eligibility, "nextAction", confirmationNextActionSet) {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.nextAction"
	}
	if jsonHas(eligibility, "retryDisposition") && !jsonIn(eligibility, "retryDisposition", confirmationRetryDispositions) {
		return "INTENT_ELIGIBILITY_INVALID", "options.eligibility.retryDisposition"
	}
	if !jsonIsStr(options, "runtimeInventorySource") || !jsonIntegerAtLeast(options, "runtimeInventoryRevision", 0) {
		return "RUNTIME_SOURCE_INVALID", "options.runtimeInventorySource"
	}
	for _, key := range []string{"projects", "repositories", "branches", "variants"} {
		items, isArray := jsonArray(options, key)
		if !isArray {
			return "OPTIONS_INVENTORY_UNAVAILABLE", "options." + key
		}
		seen := map[string]bool{}
		for i, item := range items {
			choice, ok := item.(map[string]any)
			path := jsonPathIndex("options."+key, i)
			if !ok || !jsonNonemptyStr(choice, "id") || !jsonHas(choice, "label") {
				return "OPTIONS_SHAPE_INVALID", path
			}
			if _, labelStr := jsonStrOK(choice, "label"); !labelStr {
				return "OPTIONS_SHAPE_INVALID", path
			}
			if _, present := jsonBool(choice, "available"); !present {
				return "OPTIONS_SHAPE_INVALID", path
			}
			if seen[strOr(choice["id"])] {
				return "OPTIONS_SHAPE_INVALID", path
			}
			if jsonHas(choice, "headCommit") {
				head, headStr := jsonStrOK(choice, "headCommit")
				if !headStr || head == "" || !confirmationHeadCommitPattern.MatchString(head) {
					return "OPTIONS_SHAPE_INVALID", path
				}
			}
			if failuresValue := choice["failures"]; jsonHas(choice, "failures") {
				failures, isArray := failuresValue.([]any)
				if !isArray || len(failures) > 8 {
					return "OPTIONS_FAILURES_INVALID", path + ".failures"
				}
				for _, failureItem := range failures {
					failureObj, _ := failureItem.(map[string]any)
					if code := validateFailureShape(failureObj); code != "" {
						return code, path + ".failures"
					}
				}
			}
			seen[strOr(choice["id"])] = true
		}
	}
	if jsonHas(options, "runtimeSelectionMode") {
		mode, ok := jsonStrOK(options, "runtimeSelectionMode")
		if !ok || mode != "FROZEN_EXECUTION" && mode != "EXPLICIT_SELECTION" {
			return "RUNTIME_SELECTION_MODE_INVALID", "options.runtimeSelectionMode"
		}
	}
	if locked, entered, invalid := optionalObjectField(options, "lockedRuntimeConfig"); entered {
		if invalid {
			return "LOCKED_CONFIG_INVALID", "options.lockedRuntimeConfig"
		}
		_, fastBool := jsonBool(locked, "codexFastMode")
		if !jsonIsStr(locked, "model") || !jsonIsStr(locked, "reasoningEffort") || !fastBool || !jsonIsStr(locked, "serviceTier") || !validRuntimePolicyShape(locked) {
			return "LOCKED_CONFIG_INVALID", "options.lockedRuntimeConfig"
		}
	}
	if raw, present := options["agentConfigFingerprints"]; present {
		fingerprints, ok := raw.(map[string]any)
		if !ok {
			return "AGENT_CONFIG_INVENTORY_INVALID", "options.agentConfigFingerprints"
		}
		for id, value := range fingerprints {
			fp, ok := value.(string)
			if !ok || agentpolicy.ValidateFrozenSelection(id, agentpolicy.PolicyCooperativeTrustedV1, fp) != nil {
				return "AGENT_CONFIG_INVENTORY_INVALID", "options.agentConfigFingerprints"
			}
		}
	}
	runtimeOptions, isArray := jsonArray(options, "runtimeOptions")
	if !isArray {
		return "RUNTIME_OPTIONS_UNAVAILABLE", "options.runtimeOptions"
	}
	for i, item := range runtimeOptions {
		option, ok := item.(map[string]any)
		path := jsonPathIndex("options.runtimeOptions", i)
		_, enabledBool := jsonBool(option, "enabled")
		_, fastSupportedBool := jsonBool(option, "codexFastModeSupported")
		efforts, effortsArray := option["supportedReasoningEfforts"].([]any)
		if !ok || !jsonNonemptyStr(option, "modelId") || !enabledBool || !fastSupportedBool || !effortsArray {
			return "RUNTIME_OPTIONS_UNAVAILABLE", path
		}
		for _, effort := range efforts {
			if e, _ := effort.(string); e == "" {
				return "RUNTIME_OPTIONS_UNAVAILABLE", path + ".supportedReasoningEfforts"
			}
		}
	}
	if _, present := jsonBool(options, "backendInventoryComplete"); !present {
		return "BACKEND_OPTIONS_UNAVAILABLE", "options.backendInventoryComplete"
	}
	if candidatesValue := options["backendCandidates"]; jsonHas(options, "backendCandidates") {
		if _, isArray := candidatesValue.([]any); !isArray {
			return "BACKEND_OPTIONS_UNAVAILABLE", "options.backendCandidates"
		}
	}
	candidates, _ := options["backendCandidates"].([]any)
	backendIDs := map[string]bool{}
	for i, item := range candidates {
		backend, ok := item.(map[string]any)
		path := jsonPathIndex("options.backendCandidates", i)
		fingerprint, _ := jsonStrOK(backend, "configFingerprint")
		if !ok || !jsonNonemptyStr(backend, "configId") || !confirmationContentHashPattern.MatchString(fingerprint) ||
			!jsonIsStr(backend, "configName") || !jsonIsStr(backend, "source") || backendIDs[strOr(backend["configId"])] {
			return "BACKEND_OPTIONS_INVALID", path
		}
		backendIDs[strOr(backend["configId"])] = true
	}
	if lockedBackend, entered, invalid := optionalObjectField(options, "lockedBackend"); entered {
		if invalid {
			return "LOCKED_BACKEND_INVALID", "options.lockedBackend"
		}
		fingerprint, _ := jsonStrOK(lockedBackend, "configFingerprint")
		if !jsonIsStr(lockedBackend, "configId") || !confirmationContentHashPattern.MatchString(fingerprint) ||
			!jsonIsStr(lockedBackend, "configName") || !jsonIsStr(lockedBackend, "source") {
			return "LOCKED_BACKEND_INVALID", "options.lockedBackend"
		}
	}
	if source, entered, invalid := optionalObjectField(options, "conversationModelDefault"); entered {
		if invalid {
			return "CONVERSATION_MODEL_DEFAULT_INVALID", "options.conversationModelDefault"
		}
		state := strOr(source["state"])
		if !confirmationModelDefaultStates[state] {
			return "CONVERSATION_MODEL_DEFAULT_INVALID", "options.conversationModelDefault"
		}
		if state == "KNOWN" && (!jsonIsStr(source, "conversationId") || !jsonIsStr(source, "threadId") || !jsonIsStr(source, "initialModel") || !jsonIsStr(source, "sourceRunId")) {
			return "CONVERSATION_MODEL_DEFAULT_INVALID", "options.conversationModelDefault"
		}
		if state == "HISTORICAL_UNKNOWN" && (!jsonIsStr(source, "conversationId") || !jsonIsStr(source, "threadId")) {
			return "CONVERSATION_MODEL_DEFAULT_INVALID", "options.conversationModelDefault"
		}
	}
	return "", ""
}

// cardEntryProjectionFailure builds the typed, content-free error the Host mount
// gate returns when the final Card entry is invalid. The jsonPath is embedded in
// the message (safe structural metadata, never user content) and the reason code
// is carried on Failure.ReasonCode for machine handling.
func cardEntryProjectionFailure(reasonCode, jsonPath string) *c.Failure {
	failure := c.NewFailure(
		"CONFIRMATION_CARD_ENTRY_PROJECTION_INVALID",
		"CONFIRMATION",
		"RECEIPT_READBACK",
		"The prepared confirmation could not be projected as a valid Card entry at "+jsonPath+".",
		c.ReadCurrentTarget,
		nil,
	)
	failure.ReasonCode = reasonCode
	return failure
}

func optionsProjectionFailure(reasonCode, jsonPath string) *c.Failure {
	failure := c.NewFailure(
		"CONFIRMATION_OPTIONS_PROJECTION_INVALID",
		"CONFIRMATION",
		"CONFIRMATION_PREFLIGHT",
		"The confirmation options could not be projected as a valid Card options set at "+jsonPath+".",
		c.ReadCurrentTarget,
		nil,
	)
	failure.ReasonCode = reasonCode
	return failure
}

// The public read contract must preserve the policy identity, not merely the
// model fields. Unknown/malformed selection data must not become legacy mode.
func validRuntimePolicyShape(config map[string]any) bool {
	for _, key := range []string{"agentId", "executionPolicy", "agentConfigFingerprint"} {
		if jsonHas(config, key) && !jsonNonemptyStr(config, key) {
			return false
		}
	}
	return agentpolicy.ValidateFrozenSelection(strOr(config["agentId"]), strOr(config["executionPolicy"]), strOr(config["agentConfigFingerprint"])) == nil
}
