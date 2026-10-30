package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/formalapproval"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskaction"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func confirmationHash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

const CodeConfirmationCallerContextRequired ErrorCode = "CONFIRMATION_CALLER_CONTEXT_REQUIRED"

var errConfirmationCallerContextRequired = errors.New("confirmation caller context required")

func confirmationReviewDecision(action string) bool {
	return action == "REVISE" || action == "REJECT"
}

func confirmationAcceptanceDecision(action string) bool {
	return action == "ACCEPT" || action == "REJECT" || action == "REQUEST_REVERIFICATION"
}

func confirmationDecisionAction(purpose confirmation.Purpose, action string) bool {
	return purpose == confirmation.Review && confirmationReviewDecision(action) ||
		purpose == confirmation.Execution && (confirmationAcceptanceDecision(action) || verificationTaskChoice(action))
}

// Confirmation consumes the neutral identity already established by the
// authenticated adapter. Exact Session reads and binding need no host handle.
func confirmationCallerReference(ctx context.Context) (aiprojectselection.ClientBindingReference, error) {
	scope, present := aisession.CallerScopeFromContext(ctx)
	ref := aiprojectselection.ClientBindingReference{CallerScope: scope}
	if !present || !ref.Valid() {
		return aiprojectselection.ClientBindingReference{}, &Error{
			Code:    CodeConfirmationCallerContextRequired,
			Message: "Confirmation requires an established caller context; the existing Session and prepare requestId must be retained",
			cause:   errConfirmationCallerContextRequired,
		}
	}
	return ref, nil
}

func (f *Facade) confirmationSession(ctx context.Context, id string) (aicontext.InitializationResult, error) {
	ref, err := confirmationCallerReference(ctx)
	if err != nil {
		return aicontext.InitializationResult{}, err
	}
	if id == "" || f.deps.ConfirmationSessions == nil || f.deps.Confirmations == nil {
		return aicontext.InitializationResult{}, invalid("confirmation session service unavailable")
	}
	// An explicit existing id takes the original pure read branch; no request id
	// is supplied, so this cannot initialize a Session or bind a Project.
	return f.deps.ConfirmationSessions.InitializeSession(ctx, ref, "", id)
}
func (f *Facade) PrepareConfirmation(ctx context.Context, in confirmation.PrepareInput) (confirmation.Intent, error) {
	current, e := f.confirmationSession(ctx, in.AISessionID)
	if e != nil {
		return confirmation.Intent{}, e
	}
	if in.SourceIntentID != "" {
		return confirmation.Intent{}, invalid("replacement requires confirmation_reprepare")
	}
	if in.SubmittedHash == "" {
		in.SubmittedHash = confirmation.ValueHash(in)
	}
	replayed, replayErr := f.deps.Confirmations.ReplayPrepare(ctx, in.AISessionID, in.RequestID, in.SubmittedHash)
	if replayErr == nil {
		return f.confirmationAccessReadback(ctx, replayed)
	}
	if !errors.Is(replayErr, confirmation.ErrRequestNotObserved) {
		return replayed, ConfirmationFailure(replayErr, "RECEIPT_READBACK")
	}
	// Keep normalized proposal content detached from the submitted request.
	submittedHash := in.SubmittedHash
	raw, cloneErr := json.Marshal(in)
	if cloneErr != nil {
		return confirmation.Intent{}, cloneErr
	}
	var detached confirmation.PrepareInput
	if cloneErr = json.Unmarshal(raw, &detached); cloneErr != nil {
		return confirmation.Intent{}, cloneErr
	}
	in = detached
	in.SubmittedHash = submittedHash
	project := ""
	if current.BoundProjectID != nil {
		project = *current.BoundProjectID
	}
	if in.RequestID == "" || len(in.Variants) == 0 {
		return confirmation.Intent{}, invalid("requestId and prepared variants are required")
	}
	count := 0
	for _, has := range []bool{in.ProjectSetup != nil, in.FeatureArea != nil, in.Task != nil, in.Review != nil, in.Execution != nil, in.RecoveryPurpose != nil} {
		if has {
			count++
		}
	}
	if count != 1 {
		return confirmation.Intent{}, invalid("exactly one purpose body is required")
	}
	valid := false
	switch in.Purpose {
	case confirmation.ProjectSetup:
		valid = in.ProjectSetup != nil && (in.Action == "SELECT_PROJECT_BRANCH" || in.Action == "SELECT_PROJECT" || in.Action == "CONFIGURE_DEVELOPMENT_CONTEXT")
	case confirmation.FeatureArea:
		valid = in.FeatureArea != nil && (in.Action == "CREATE_AREA" || in.Action == "CREATE_AREA_AND_REVIEW" || in.Action == "MERGE_FEATURE_AREA")
		if valid && in.Action == "CREATE_AREA_AND_REVIEW" {
			valid = in.FeatureArea.InitialTask != nil
		}
	case confirmation.Task:
		valid = in.Task != nil && (in.Action == "CREATE_TASK" || in.Action == "CREATE_TASK_AND_REVIEW")
	case confirmation.Review:
		valid = in.Review != nil && (in.Action == "START_REVIEW" || in.Action == "REQUEST_SUPPLEMENTAL_REVIEW" || confirmationReviewDecision(in.Action))
	case confirmation.Execution:
		valid = in.Execution != nil && in.Action == in.Execution.Action && (in.Action == "APPROVE" || in.Action == "APPROVE_AND_START" || in.Action == "START_APPROVED" || in.Action == "CONFIRM_IMPLEMENTATION" || in.Action == "CONFIRM_AND_START_IMPLEMENTATION" || in.Action == "RETRY_IMPLEMENTATION" || in.Action == "REQUEST_CHANGES" || confirmationAcceptanceDecision(in.Action) || confirmationDirectFileAction(in.Action) || verificationTaskChoice(in.Action))
	case confirmation.RecoveryPurpose:
		// Only Recovery Resolve may prepare this purpose; a public Prepare request
		// cannot mint a purpose intent without the same-transaction Recovery link.
		return confirmation.Intent{}, invalid("Recovery purpose intents require Recovery Resolve")
	}
	if !valid {
		return confirmation.Intent{}, invalid("purpose/action requires an exact bound context")
	}
	if (in.Purpose != confirmation.ProjectSetup || in.Action == "CONFIGURE_DEVELOPMENT_CONTEXT") && project == "" {
		return confirmation.Intent{}, aisession.ErrProjectUnbound
	}
	ids := map[string]bool{}
	for _, v := range in.Variants {
		if err := v.DisplaySummary.Validate(); err != nil {
			return confirmation.Intent{}, invalid(err.Error())
		}
		if strings.TrimSpace(v.ID) == "" || ids[v.ID] || strings.TrimSpace(v.Summary) == "" {
			return confirmation.Intent{}, invalid("variant identity and complete summary are required")
		}
		ids[v.ID] = true
		if confirmationDecisionAction(in.Purpose, in.Action) {
			if v.ExecutorKind != "" || v.ReviewInstruction != "" || v.ImplementationMode != "" || v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil || in.RecommendedRuntimeConfig != nil {
				return confirmation.Intent{}, invalid("decision confirmation cannot carry replacement content or runtime configuration")
			}
			continue
		}
		if in.Action == "CREATE_TASK" || in.Action == "CREATE_AREA" || in.Action == "MERGE_FEATURE_AREA" {
			if v.ExecutorKind != "" || v.ReviewInstruction != "" || v.ImplementationMode != "" || v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil || in.RecommendedRuntimeConfig != nil {
				return confirmation.Intent{}, invalid("create-only variants cannot carry executor or runtime configuration")
			}
		}
		if in.Purpose == confirmation.Review || strings.HasSuffix(in.Action, "_AND_REVIEW") {
			if (v.ExecutorKind != string(review.ExecutorCodingAgent) && v.ExecutorKind != string(review.ExecutorUpperLLM)) || scopereview.ValidateInstruction(v.ReviewInstruction) != nil {
				return confirmation.Intent{}, invalid("complete executor-specific Review candidate is required")
			}
		}
		if in.Purpose == confirmation.Execution {
			if v.ImplementationMode != task.ImplementationCodingAgent && v.ImplementationMode != task.ImplementationDirectFile {
				return confirmation.Intent{}, invalid("explicit implementation mode is required")
			}
			if in.Action == "APPROVE" || in.Action == "APPROVE_AND_START" || in.Action == "REQUEST_CHANGES" {
				if v.WorkSpec == nil || v.VerificationSpec == nil || v.WorkSpec.ImplementationMode != v.ImplementationMode {
					return confirmation.Intent{}, invalid("complete implementation variant is required")
				}
			}
		}
	}

	taskID := ""
	if in.Execution != nil {
		taskID = in.Execution.TaskID
	}
	if in.Review != nil {
		taskID = in.Review.TaskID
	}
	if in.FeatureArea != nil {
		taskID = in.FeatureArea.CompletionTaskID
	}
	if taskID != "" {
		if reader, ok := f.deps.Confirmations.(taskActionNodeReader); ok {
			node, e := reader.ReadTaskActionNode(ctx, project, taskID, in.AISessionID)
			if e != nil {
				return confirmation.Intent{}, e
			}
			if in.TaskActionGeneration != 0 && in.TaskActionGeneration != node.Generation {
				return confirmation.Intent{}, confirmation.NewFailure("TASK_ACTION_STALE", "CORE", "PREPARE", "任务步骤已变化，请保留原请求并刷新，不自动替换为新目标。", confirmation.ReadCurrentTarget, nil)
			}
			if node.Blocking {
				return confirmation.Intent{}, confirmation.NewFailure("TASK_ACTION_PENDING", "CORE", "PREPARE", node.Reason, confirmation.ReadCurrentTarget, nil)
			}
			ctx = taskaction.WithExpectation(ctx, taskaction.Expectation{TaskID: taskID, Generation: node.Generation})
		}
	}
	if err := f.validatePreparedConfirmation(ctx, &in, project); err != nil {
		return confirmation.Intent{}, err
	}
	return f.deps.Confirmations.Prepare(ctx, project, in)
}

// ReprepareConfirmation is the formal PREPARE_NEW_VERSION action. It is a thin
// adapter over the existing prepare builder rather than a second intent
// constructor: it proves the source intent's durable no-effect rejection, then
// re-freezes the current Decision version, topology revision and development
// context through the same preflight prepare uses. It never reuses the source
// intentId, never raises version above 1, and never creates a successor Review -
// only a committed replacement may do that.
func (f *Facade) ReprepareConfirmation(ctx context.Context, in confirmation.ReprepareInput) (confirmation.Intent, error) {
	current, err := f.confirmationSession(ctx, in.AISessionID)
	if err != nil {
		return confirmation.Intent{}, err
	}
	if in.RequestID == "" || in.SourceIntentID == "" || in.SourceVersion <= 0 || !validConfirmationContentHash(in.SourceContentHash) {
		return confirmation.Intent{}, invalid("reprepare requires an exact source intentId, version, contentHash and requestId")
	}
	store, ok := f.deps.Confirmations.(confirmation.ReplacementStore)
	if !ok {
		return confirmation.Intent{}, invalid("confirmation replacement service unavailable")
	}
	// Durable exact replay precedes every mutable source/current-context guard.
	replayed, err := store.ReplayReplacement(ctx, in)
	if err == nil {
		return f.confirmationAccessReadback(ctx, replayed)
	}
	if !errors.Is(err, confirmation.ErrRequestNotObserved) {
		return confirmation.Intent{}, ConfirmationFailure(err, "RECEIPT_READBACK")
	}
	source, err := f.deps.Confirmations.Get(ctx, in.AISessionID, in.SourceIntentID)
	if err != nil {
		return confirmation.Intent{}, ConfirmationFailure(err, "RECEIPT_READBACK")
	}
	project := ""
	if current.BoundProjectID != nil {
		project = *current.BoundProjectID
	}
	if source.Version != in.SourceVersion || source.ContentHash != in.SourceContentHash || source.ProjectID != project {
		return confirmation.Intent{}, confirmation.NewFailure("REPREPARE_SOURCE_IDENTITY_INVALID", "CORE", "INPUT_IDENTITY", "Replacement requires the exact source in this Session's Project.", confirmation.ReadCurrentTarget, nil)
	}
	if source.Purpose == confirmation.RecoveryPurpose {
		out, err := store.ReprepareRecoveryPurpose(ctx, in)
		if err != nil {
			return out, ConfirmationFailure(err, "PREPARE")
		}
		return f.confirmationAccessReadback(ctx, out)
	}
	if source.Purpose != confirmation.Review || source.Content.Review == nil || (source.Action != "REVISE" && source.Action != "REJECT") || in.RecoveryPurpose != nil {
		return confirmation.Intent{}, invalid("replacement requires a Review Decision or Recovery purpose")
	}
	if source.State == "SUPERSEDED" {
		return confirmation.Intent{}, confirmation.NewFailure("SOURCE_INTENT_SUPERSEDED", "CORE", "INPUT_IDENTITY", "This confirmation already has a replacement.", confirmation.ReadCurrentTarget, nil)
	}
	raw, err := json.Marshal(source.Content)
	if err != nil {
		return confirmation.Intent{}, err
	}
	var derived confirmation.PrepareInput
	if err = json.Unmarshal(raw, &derived); err != nil {
		return confirmation.Intent{}, err
	}
	derived.AISessionID, derived.RequestID, derived.SourceIntentID = in.AISessionID, in.RequestID, source.IntentID
	derived.SubmittedHash = ""
	body := *derived.Review
	body.PreparedDecisionVersion, body.PreparedTopologyRevision, body.PreparedDevelopmentContextHash = "", 0, ""
	if in.ReviewFeedback != "" {
		body.ReviewFeedback = in.ReviewFeedback
	}
	derived.Review = &body
	if err = f.validatePreparedConfirmation(ctx, &derived, project); err != nil {
		return confirmation.Intent{}, ConfirmationFailure(err, "PREPARE")
	}
	out, err := store.ReprepareDecision(ctx, project, in, derived)
	if err != nil {
		return out, ConfirmationFailure(err, "PREPARE")
	}
	return f.confirmationAccessReadback(ctx, out)
}
func (f *Facade) ReadConfirmation(ctx context.Context, in confirmation.ReadInput) (out confirmation.Intent, err error) {
	defer func() { confirmation.NormalizePublicIntent(&out) }()
	if _, e := f.confirmationSession(ctx, in.AISessionID); e != nil {
		return confirmation.Intent{}, e
	}
	// §7: contentHash is part of the public read identity, not an optional filter.
	// This gate runs before any state write, so a wrong or absent hash is refused
	// without mutating the intent, consuming a receipt or mounting a Card.
	if !validConfirmationContentHash(in.ContentHash) {
		return confirmation.Intent{}, confirmationReadIdentityFailure(nil)
	}
	if in.RequestID != "" && in.ReplacementRequestID != "" {
		return confirmation.Intent{}, confirmationReadIdentityFailure(nil)
	}
	out, e := f.deps.Confirmations.Get(ctx, in.AISessionID, in.IntentID)
	if e != nil && out.IntentID == "" {
		return out, e
	}
	if out.Version != in.Version || out.Purpose != in.Purpose || out.ContentHash != in.ContentHash {
		// Durable content always wins over the supplied hash; intentId and version
		// alone can never address this read.
		return confirmation.Intent{}, confirmationReadIdentityFailure(nil)
	}
	if in.ReplacementRequestID != "" {
		out.Submission = nil
		store, ok := f.deps.Confirmations.(confirmation.ReplacementStore)
		if !ok {
			return out, invalid("confirmation replacement receipt service unavailable")
		}
		in.RequestID = in.ReplacementRequestID
		receipt, readErr := store.ReadReplacementReceipt(ctx, in)
		if errors.Is(readErr, confirmation.ErrRequestNotObserved) {
			return out, confirmation.NewFailure("REPLACEMENT_RECEIPT_NOT_FOUND", "STORAGE", "RECEIPT_READBACK", "No replacement receipt exists for this exact request.", confirmation.ReadOriginalReceipt, readErr)
		}
		if readErr != nil {
			return out, ConfirmationFailure(readErr, "RECEIPT_READBACK")
		}
		out.ReplacementReceipt = receipt
		return f.confirmationAccessReadback(ctx, out)
	}
	if in.RequestID != "" {
		if store, ok := f.deps.Confirmations.(confirmation.ReplacementStore); ok {
			receipt, readErr := store.ReadReplacementReceipt(ctx, in)
			if readErr == nil {
				out.Submission = nil
				out.ReplacementReceipt = receipt
				return f.confirmationAccessReadback(ctx, out)
			}
			if !errors.Is(readErr, confirmation.ErrRequestNotObserved) {
				out.Submission = nil
				return out, ConfirmationFailure(readErr, "RECEIPT_READBACK")
			}
		}
		result, err := f.deps.Confirmations.ReadSubmission(ctx, in)
		if err != nil {
			// A different request's result must not masquerade as this read.
			out.Submission = nil
			return out, ConfirmationFailure(err, "RECEIPT_READBACK")
		}
		out.Submission = &result
		if out.State == "REJECTED" && out.Commit != nil && result.ReceiptRequestID == out.Commit.RequestID {
			for _, step := range out.Steps {
				if step.State == "REJECTED" && step.Effect == "NO_EFFECT_PROVEN" && step.Failure != nil && step.Failure.Code == requestChangesRequiresReview {
					out.Submission.NextAction = confirmation.ReadCurrentTarget
				}
			}
		}
	}
	if e != nil {
		return out, ConfirmationFailure(e, "RECEIPT_READBACK")
	}
	return f.confirmationAccessReadback(ctx, out)
}
func (f *Facade) ConfirmationOptions(ctx context.Context, in confirmation.OptionsInput) (out confirmation.Options, err error) {
	defer func() { confirmation.NormalizePublicOptions(&out) }()
	intent, err := f.ReadConfirmation(ctx, in.ReadInput)
	if err != nil {
		// A refused read returns no envelope at all: echoing the caller's own identity
		// fields back would hand out authority-looking facts for content Core never
		// confirmed (§7).
		out = confirmation.Options{}
		return
	}
	ctx = taskaction.WithIntent(ctx, intent.IntentID)
	selectedReview := in.StartReview
	if selectedReview == nil && intent.Commit != nil {
		selectedReview = intent.Commit.Selection.StartReview
	}
	startReview, err := confirmationTaskReviewChoice(intent, selectedReview)
	if err != nil {
		return confirmation.Options{}, err
	}
	out = confirmation.Options{DevelopmentContexts: []confirmation.DevelopmentContextOption{}, AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ProjectID: in.ProjectID, RepositoryID: in.RepositoryID, Projects: []confirmation.Choice{}, Repositories: []confirmation.Choice{}, Branches: []confirmation.Choice{}, Variants: []confirmation.Choice{}, RuntimeOptions: []codexruntime.RuntimeOption{}}
	out.Eligibility = confirmationEligibility(intent)
	if intent.Purpose == confirmation.Task {
		out.StartReview = &startReview
		out.TaskContinuity = f.confirmationTaskContinuity(ctx, intent)
		if confirmationTaskReviewDeferred(intent, selectedReview) {
			intent = confirmationTaskOnlyOptionsIntent(intent)
		}
	}
	if intent.Content.Review != nil && intent.Content.Review.DecisionID != "" {
		// The options surface reports the Decision's current version from the same
		// reducer prepare and commit use, so a drifted confirmation is visibly
		// stale here and not only at commit time.
		if g, e := f.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: in.AISessionID, ProjectID: intent.ProjectID, TaskID: intent.Content.Review.TaskID}); e == nil && g.Approval != nil && g.Approval.DecisionID == intent.Content.Review.DecisionID {
			out.Eligibility.CurrentDecisionVersion = g.Approval.DecisionVersion
			if g.Approval.Status != "PENDING" && out.Eligibility.Available {
				out.Eligibility.Available = false
				out.Eligibility.ReasonCode = g.Approval.ReasonCode
				if g.Approval.ReasonCode == "" {
					out.Eligibility.ReasonCode = "DECISION_NOT_PENDING"
				}
				out.Eligibility.ReasonMessage = "The frozen Review Decision is no longer pending. Prepare a new confirmation from its current state."
				out.Eligibility.ReprepareAllowed = intent.State == "PENDING" && intent.Commit == nil
				out.Eligibility.NextAction = confirmation.PrepareNewVersion
				out.Eligibility.RetryDisposition = string(confirmation.RetryReprepareRequired)
			}
		}
	}
	if reader, ok := f.deps.Repositories.(interface {
		TopologyRevision(context.Context, string) (int64, error)
	}); ok && intent.ProjectID != "" {
		if revision, readErr := reader.TopologyRevision(ctx, intent.ProjectID); readErr == nil {
			out.Eligibility.CurrentTopologyRevision = revision
			out.Eligibility.CurrentGeneration = confirmation.ValueHash([]any{intent.ContentHash, revision, out.Eligibility.CurrentDecisionVersion})
		}
	}
	defer func() {
		if err != nil {
			return
		}
		available := false
		for _, variant := range out.Variants {
			if variant.Available {
				available = true
				continue
			}
			if out.Eligibility.ReasonCode == "" && len(variant.Failures) > 0 {
				failure := variant.Failures[0]
				out.Eligibility.ReasonCode = failure.Code
				out.Eligibility.ReasonMessage = failure.Message
				out.Eligibility.ReprepareAllowed = failure.NextAction == confirmation.PrepareNewVersion || failure.NextAction == confirmation.EditSelection
			}
		}
		out.Eligibility.Available = out.Eligibility.Available && available
		if !out.Eligibility.Available && out.Eligibility.ReasonCode == "" {
			out.Eligibility.ReasonCode = "VARIANT_NO_LONGER_AVAILABLE"
			out.Eligibility.ReasonMessage = "No prepared variant remains available for this exact confirmation."
			out.Eligibility.ReprepareAllowed = intent.State == "PENDING" && intent.Commit == nil
		}
	}()
	out.ConversationModelDefault = f.conversationModelDefault(ctx, confirmationFeatureAreaID(intent))
	backendNeeded := false
	for _, v := range intent.Content.Variants {
		backendNeeded = backendNeeded || (intent.Purpose != confirmation.RecoveryPurpose && confirmationVariantNeedsRuntimeSelection(intent.Action, v))
	}
	if backendNeeded {
		available, e := f.backendSelectionOptions(ctx, in.AISessionID, intent.ProjectID, confirmationFeatureAreaID(intent))
		if e != nil {
			out.BackendInventoryReadError = "CODEX_BACKEND_INVENTORY_UNAVAILABLE"
		} else {
			out.BackendCandidates, out.BackendInventoryComplete = available.Candidates, available.Complete
			out.BackendContinuity = available.Continuity
			if available.Continuity != nil && available.Continuity.State == agentconversation.BackendContinuityBoundAvailable {
				out.LockedBackend = available.Continuity.BoundBackend
			}
		}
	}
	// A terminal rejection or a resolved Decision makes every variant of this
	// intent unavailable: only a fresh prepare can continue the work.
	reprepareRequired := !out.Eligibility.Available && (out.Eligibility.ReasonCode == "INTENT_REPREPARE_REQUIRED" || strings.HasPrefix(out.Eligibility.ReasonCode, "DECISION_"))
	for _, v := range intent.Content.Variants {
		out.Variants = append(out.Variants, confirmation.Choice{ID: v.ID, Label: v.Label, Available: intent.State == "PENDING" && !reprepareRequired})
	}

	if intent.State == "PENDING" {
		current, e := f.confirmationActionCurrent(ctx, intent)
		if e != nil {
			return out, e
		}
		if !current {
			for i := range out.Variants {
				appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("TASK_ACTION_STALE", "CORE", "CONFIRMATION_PREFLIGHT", "此确认不属于任务当前操作，请刷新并核对原链路。", confirmation.ReadCurrentTarget, nil))
			}
		}
	}
	if reprepareRequired {
		for i := range out.Variants {
			appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure(out.Eligibility.ReasonCode, "CORE", "UNIQUE_CONSUMPTION", out.Eligibility.ReasonMessage, confirmation.PrepareNewVersion, nil))
		}
	}
	if backendNeeded && out.BackendContinuity != nil && out.BackendContinuity.State != agentconversation.BackendContinuityThreadless && out.BackendContinuity.State != agentconversation.BackendContinuityBoundAvailable {
		for i, variant := range intent.Content.Variants {
			if confirmationVariantUsesCodex(intent.Action, variant) {
				appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("FEATURE_AREA_BACKEND_CONTINUITY_UNAVAILABLE", "CORE", "CONFIRMATION_PREFLIGHT", out.BackendContinuity.Reason, confirmation.ReadCurrentTarget, nil))
			}
		}
	}
	inventory, inventoryErr := f.CodexRuntimeInventory()
	if inventoryErr != nil {
		out.RuntimeInventoryReadError = "CODEX_RUNTIME_INVENTORY_UNAVAILABLE"
		out.RuntimeInventorySource = "UNAVAILABLE"
	} else {
		out.RuntimeOptions = inventory.Allowlist.RuntimeOptions()
		out.RuntimeInventoryRevision = inventory.Revision
		out.RuntimeInventorySource = "CORE_CODEX_RUNTIME_AUTHORITY"
	}
	if intent.State == "PENDING" {
		checked := intent.Content
		if e := f.validatePreparedConfirmation(ctx, &checked, intent.ProjectID); e != nil {
			var public *Error
			var gate *wp.Error
			var typed *confirmation.Failure
			if !errors.As(e, &public) && !errors.As(e, &gate) && !errors.As(e, &typed) {
				return out, e
			}
			if public != nil && (public.Code == CodeInternal || public.Code == CodeInfrastructure) {
				return out, e
			}
			for i := range out.Variants {
				appendConfirmationFailure(&out.Variants[i], ConfirmationFailure(e, "CONFIRMATION_PREFLIGHT"))
			}
		}
	}
	codingReview, upperReview := false, false
	if capabilities, ok := f.deps.ScopeReviews.(interface{ ConfirmationReviewCapabilities() (bool, bool) }); ok {
		codingReview, upperReview = capabilities.ConfirmationReviewCapabilities()
	}
	for i, v := range intent.Content.Variants {
		if intent.Purpose == confirmation.RecoveryPurpose {
			continue // exact durable link and source fences run in BeginCommit
		}
		body := intent.Content.Task
		if body == nil && intent.Content.FeatureArea != nil {
			body = intent.Content.FeatureArea.InitialTask
		}
		if body != nil {
			_, e := f.confirmationCreationPrerequisites(ctx, in.AISessionID, body.RepositoryID, body.StartReview && v.ExecutorKind == string(review.ExecutorCodingAgent))
			if e != nil {
				appendConfirmationFailure(&out.Variants[i], ConfirmationFailure(e, "CONFIRMATION_PREFLIGHT"))
			}
		}
		block := func(code string) {
			appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure(code, "CORE", "CONFIRMATION_PREFLIGHT", "This executor's required capability is unavailable.", confirmation.ReadCurrentTarget, nil))
		}
		if confirmationVariantNeedsRuntimeSelection(intent.Action, v) && inventoryErr != nil {
			block("CODEX_RUNTIME_INVENTORY_UNAVAILABLE")
		}
		needsBackend := confirmationVariantNeedsRuntimeSelection(intent.Action, v)
		backendInherited := intent.Action == "RETRY_IMPLEMENTATION" || intent.Action == "REQUEST_CHANGES"
		if needsBackend && !backendInherited {
			if out.BackendInventoryReadError != "" {
				block("CODEX_BACKEND_INVENTORY_UNAVAILABLE")
			} else if !out.BackendInventoryComplete {
				block("CODEX_BACKEND_INVENTORY_INCOMPLETE")
			} else if len(out.BackendCandidates) == 0 {
				block("CODEX_BACKEND_CONFIGURATION_UNAVAILABLE")
			}
		}
		if v.ExecutorKind == string(review.ExecutorCodingAgent) && !codingReview {
			block("CODING_REVIEW_UNAVAILABLE")
		}
		if v.ExecutorKind == string(review.ExecutorUpperLLM) && (!upperReview || f.deps.UpperReviewAccess == nil || f.deps.UpperReviewResults == nil) {
			block("UPPER_REVIEW_UNAVAILABLE")
		}
		if v.ImplementationMode == task.ImplementationCodingAgent && intent.Content.Execution != nil {
			// A configless Upper/create-only Task remains valid. The backend is an
			// explicit choice for the approved Execution, never a Task mutation.
			owner, e := f.deps.Tasks.Get(ctx, intent.Content.Execution.TaskID)
			if e != nil {
				return out, e
			}
			if owner.ProjectID != intent.ProjectID || owner.FeatureAreaID != intent.Content.Execution.FeatureAreaID {
				return out, notFound()
			}
		}
		if len(v.RepositoryTargets) > 0 && intent.Action != "APPROVE" && !(confirmationDirectFileAction(intent.Action) && v.ImplementationMode == task.ImplementationDirectFile) {
			if v.ExecutorKind != string(review.ExecutorCodingAgent) && v.ImplementationMode != task.ImplementationCodingAgent {
				block("REPOSITORY_TARGETS_UNSUPPORTED")
			}
		}
		if directFileCodexVerification(v) && intent.Action == "START_DIRECT_FILE" && len(v.RepositoryTargets) > 1 && v.PrimaryMemberID == "" {
			block("RUNTIME_WORKSET_INCOMPLETE")
		}
		if v.ImplementationMode == task.ImplementationDirectFile && intent.Action != "APPROVE" {
			if f.deps.DirectFiles == nil {
				block("DIRECT_FILE_UNAVAILABLE")
			}
			if intent.Action == "REQUEST_CHANGES" || intent.Action == "RETRY_IMPLEMENTATION" {
				block("DIRECT_FILE_CONTINUATION_REQUIRES_NEW_PROPOSAL")
			}

		}
	}
	if intent.Purpose == confirmation.ProjectSetup {
		current, e := f.confirmationSession(ctx, in.AISessionID)
		if e != nil {
			return out, e
		}
		if current.BoundProjectID != nil {
			out.Projects = append(out.Projects, confirmation.Choice{ID: *current.BoundProjectID, Label: current.Project.Name, Available: true})
		} else {
			if current.AvailableProjectsStatus != "AVAILABLE" {
				return out, invalid(current.AvailableProjectsReadError)
			}
			for _, p := range current.AvailableProjects {
				out.Projects = append(out.Projects, confirmation.Choice{ID: p.ID, Label: p.Name, Available: true})
			}
		}
		if in.ProjectID == "" {
			return out, nil
		}
		permitted := false
		for _, p := range out.Projects {
			if p.ID == in.ProjectID {
				permitted = true
			}
		}
		if !permitted {
			return out, notFound()
		}
		if intent.Action == "SELECT_PROJECT" {
			return out, nil
		}
		development, e := f.DevelopmentContext(ctx, in.ProjectID)
		if e != nil {
			return out, e
		}
		out.RepositoryInventoryComplete = true
		for _, r := range development.Repositories {
			available := r.Availability != "UNAVAILABLE"
			row := confirmation.DevelopmentContextOption{RepositoryID: r.RepositoryID, Label: r.RepositoryName, DevelopmentRef: r.DevelopmentRef, Available: available, Reason: r.StatusCode, Branches: []confirmation.Choice{}}
			for _, b := range r.Branches {
				row.Branches = append(row.Branches, confirmation.Choice{ID: b.FullRef, Label: b.ShortName, HeadCommit: b.HeadSHA, Available: available})
			}
			out.DevelopmentContexts = append(out.DevelopmentContexts, row)
			out.Repositories = append(out.Repositories, confirmation.Choice{ID: r.RepositoryID, Label: r.RepositoryName, Available: available, Reason: r.StatusCode})
			if r.RepositoryID == in.RepositoryID {
				for _, b := range r.Branches {
					out.Branches = append(out.Branches, confirmation.Choice{ID: b.FullRef, Label: b.ShortName, HeadCommit: b.HeadSHA, Available: true})
				}
			}
		}
	}
	if intent.Content.Execution != nil && (intent.Action == "START_APPROVED" || intent.Action == "RETRY_IMPLEMENTATION" || intent.Action == "REQUEST_CHANGES") {
		b := intent.Content.Execution
		t, e := f.deps.Tasks.Get(ctx, b.TaskID)
		if e != nil {
			return out, e
		}
		if t.ProjectID != intent.ProjectID || t.FeatureAreaID != b.FeatureAreaID || t.CurrentDefinitionRevisionID != b.TaskDefinitionRevisionID {
			return out, notFound()
		}
		for i, v := range intent.Content.Variants {
			if v.ImplementationMode != t.WorkSpec.ImplementationMode {
				appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("IMMUTABLE_IMPLEMENTATION_MODE_REQUIRES_NEW_PROPOSAL", "CORE", "CONFIRMATION_PREFLIGHT", "The frozen implementation mode requires a new proposal.", confirmation.PrepareNewVersion, nil))
			}
		}
		if t.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
			proposalID := b.ExecutionID
			if intent.Action == "START_APPROVED" && proposalID == "" {
				proposalID = t.CurrentExecutionID
			}
			if intent.Action == "START_APPROVED" && proposalID != "" && proposalID == t.CurrentExecutionID {
				revising, err := f.noRunRevisionStartEligible(ctx, t)
				if err != nil {
					return out, err
				}
				if revising {
					// NO_START froze the new definition only. Its canceled
					// predecessor must not lock this start to the old runtime.
					proposalID = ""
				}
			}
			if proposalID != "" {
				p, e := f.deps.ExecutionProposals.Get(ctx, in.AISessionID, proposalID)
				if e != nil {
					return out, e
				}
				if intent.Action == "START_APPROVED" || intent.Action == "RETRY_IMPLEMENTATION" && v2CodingDefinition(t) {
					out.RuntimeSelectionMode = "FROZEN_EXECUTION"
					out.LockedRuntimeConfig = &p.RuntimeConfig
				}
				if v2CodingDefinition(t) && (intent.Action == "RETRY_IMPLEMENTATION" || intent.Action == "START_APPROVED" && p.TaskDefinitionRevisionID == t.CurrentDefinitionRevisionID) {
					available, reason, err := f.executionRuntimeEligibility(ctx, in.AISessionID, t)
					if err != nil {
						return out, err
					}
					if !available {
						for i := range out.Variants {
							appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure(reason, "CORE", "CONFIRMATION_PREFLIGHT", "The frozen execution runtime authority is unavailable. Read the current Task; a changed definition or runtime requires a new formal approval.", confirmation.ReadCurrentTarget, nil))
						}
					}
				}
				out.LockedBackend = p.BackendSelection
				if out.LockedBackend != nil {
					if e := f.validateFrozenBackendSelection(ctx, in.AISessionID, intent.ProjectID, b.FeatureAreaID, out.LockedBackend); e != nil {
						for i := range out.Variants {
							appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("FROZEN_BACKEND_SELECTION_UNAVAILABLE", "CORE", "CONFIRMATION_PREFLIGHT", "The approved backend is disabled or has changed.", confirmation.ReadCurrentTarget, nil))
						}
					}
				}
				if out.LockedBackend == nil {
					for i := range out.Variants {
						appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("FROZEN_BACKEND_EVIDENCE_UNAVAILABLE", "CORE", "CONFIRMATION_PREFLIGHT", "The approved Execution has no provable backend binding.", confirmation.ReadCurrentTarget, nil))
					}
				}
			} else if intent.Action == "START_APPROVED" {
				// Definition-only approval intentionally has no Execution runtime.
				// The explicit start confirmation must select and freeze it.
				out.RuntimeSelectionMode = "EXPLICIT_SELECTION"
			} else {
				for i := range out.Variants {
					appendConfirmationFailure(&out.Variants[i], confirmation.NewFailure("FROZEN_BACKEND_EVIDENCE_UNAVAILABLE", "CORE", "CONFIRMATION_PREFLIGHT", "The predecessor Execution backend cannot be proven.", confirmation.ReadCurrentTarget, nil))
				}
			}
		}
		if out.LockedRuntimeConfig != nil {
			if e := out.LockedRuntimeConfig.ValidateFrozen(inventory.Allowlist); e != nil {
				failure := ConfirmationFailure(e, "CONFIRMATION_PREFLIGHT")
				// A frozen selection cannot be edited even when its model or
				// capability has since become unavailable.
				failure = confirmation.NewFailure(failure.Code, failure.Source, failure.Stage, failure.Message, confirmation.ReadCurrentTarget, e)
				for i := range out.Variants {
					appendConfirmationFailure(&out.Variants[i], failure)
				}
			}
		}

	}
	return
}

func confirmationEligibility(intent confirmation.Intent) confirmation.IntentEligibility {
	value := confirmation.IntentEligibility{
		Available:          intent.State == "PENDING" && intent.Commit == nil && time.Now().Before(intent.ExpiresAt),
		PreparedGeneration: intent.ContentHash,
		CurrentGeneration:  intent.ContentHash,
		AttemptState:       "NOT_ATTEMPTED",
		ReceiptState:       "NOT_OBSERVED",
	}
	if intent.Content.Review != nil {
		value.PreparedTopologyRevision = intent.Content.Review.PreparedTopologyRevision
		value.CurrentTopologyRevision = value.PreparedTopologyRevision
		// The Decision version is the Core-frozen token, never the Decision ID.
		value.PreparedDecisionVersion = intent.Content.Review.PreparedDecisionVersion
		value.CurrentDecisionVersion = value.PreparedDecisionVersion
	}
	if intent.Commit != nil {
		value.AttemptState = intent.State
	}
	if intent.State == "REJECTED" {
		for _, step := range intent.Steps {
			if step.State == "REJECTED" && step.Failure != nil && step.Failure.Code == requestChangesRequiresReview {
				value.ReasonCode, value.ReasonMessage = step.Failure.Code, step.Failure.Message
				value.NextAction = confirmation.ReadCurrentTarget
				value.ReprepareAllowed = false
			}
		}
	}
	if sub := intent.Submission; sub != nil {
		value.ReceiptState = string(sub.Outcome)
		if sub.Recorded {
			value.AttemptState = "RECORDED"
			value.NextAction = sub.NextAction
			value.RetryDisposition = string(sub.RetryDisposition)
		}
		if sub.Recorded && sub.Outcome == confirmation.RejectedNotConsumed && intent.State == "PENDING" && intent.Commit == nil {
			// The shared retry reducer, not this surface, decides whether the same
			// intent may be commanded again. A REPREPARE_REQUIRED rejection
			// consumed nothing and is terminal for this intent: only a fresh
			// prepare against the current Decision continues the work.
			value.ReprepareAllowed = sub.ReprepareAllowed
			if sub.RetryDisposition == confirmation.RetryReprepareRequired {
				value.Available = false
				value.AttemptState = "TERMINAL_REJECTED"
				value.ReasonCode = "INTENT_REPREPARE_REQUIRED"
				value.ReasonMessage = "This confirmation was rejected without any business effect. Prepare a new confirmation from the current Review Decision."
			}
		}
	}
	if !value.Available && value.ReasonCode == "" {
		value.ReasonCode = "INTENT_ALREADY_CONSUMED"
		value.ReasonMessage = "This confirmation is no longer available for a new commit."
		value.ReprepareAllowed = intent.State != "UNKNOWN" && intent.State != "CONFIRMING"
		if intent.State == "UNKNOWN" || intent.State == "CONFIRMING" {
			value.ReasonCode = "COMMIT_OUTCOME_UNKNOWN"
			value.ReasonMessage = "The original request outcome is not proven; read its exact receipt without redispatching."
		}
	}
	return value
}

func confirmationSteps(in confirmation.Intent) []confirmation.Step {
	kinds := []string{}
	switch in.Action {
	case "SELECT_PROJECT":
		kinds = []string{"BIND_PROJECT"}
	case "CONFIGURE_DEVELOPMENT_CONTEXT":
		out := []confirmation.Step{}
		if in.Commit == nil {
			return out
		}
		for _, row := range in.Commit.Selection.DevelopmentContexts {
			out = append(out, confirmation.Step{Kind: "SELECT_BRANCH", RepositoryID: row.RepositoryID, DevelopmentRef: row.DevelopmentRef, RequestID: "confirmation_" + confirmationHash([]string{in.IntentID, "SELECT_BRANCH", row.RepositoryID})[:32], State: "PENDING"})
		}
		return out
	case "SELECT_PROJECT_BRANCH":
		kinds = []string{"BIND_PROJECT", "SELECT_BRANCH"}
	case "CREATE_AREA":
		kinds = []string{"CREATE_AREA"}
	case "CREATE_AREA_AND_REVIEW":
		kinds = []string{"CREATE_AREA", "CREATE_TASK", "START_REVIEW"}
	case "MERGE_FEATURE_AREA":
		kinds = []string{"MERGE_FEATURE_AREA"}
	case "CREATE_TASK":
		kinds = []string{"CREATE_TASK"}
	case "CREATE_TASK_AND_REVIEW":
		kinds = []string{"CREATE_TASK"}
		if in.Commit == nil || !confirmationTaskReviewDeferred(in, in.Commit.Selection.StartReview) {
			kinds = append(kinds, "START_REVIEW")
		}
	case "START_REVIEW":
		kinds = []string{"START_REVIEW"}
	case "REQUEST_SUPPLEMENTAL_REVIEW":
		kinds = []string{"REQUEST_REVIEW", "START_REVIEW"}
	case "REVISE", "REJECT":
		if in.Purpose == confirmation.Review {
			kinds = []string{"RESOLVE_REVIEW_DECISION"}
		} else {
			kinds = []string{"RESOLVE_ACCEPTANCE_DECISION"}
		}
	case "ACCEPT", "REQUEST_REVERIFICATION", "COMPLETE_TASK":
		kinds = []string{"RESOLVE_ACCEPTANCE_DECISION"}
	case "APPROVE":
		kinds = []string{"APPROVE"}
	case "APPROVE_AND_START":
		codingOnly := len(in.Content.Variants) > 0
		for _, variant := range in.Content.Variants {
			codingOnly = codingOnly && variant.ImplementationMode == task.ImplementationCodingAgent && len(variant.RepositoryTargets) > 0
		}
		if in.Commit != nil {
			for _, variant := range in.Content.Variants {
				if variant.ID == in.Commit.VariantID {
					codingOnly = variant.ImplementationMode == task.ImplementationCodingAgent && len(variant.RepositoryTargets) > 0
				}
			}
		}
		if codingOnly {
			// The qualified Coding producer commits preparation in three
			// transactions. Runtime start is an independent subsequent command.
			kinds = []string{"APPROVE"}
		} else {
			kinds = []string{"APPROVE", "START_IMPLEMENTATION"}
		}
	case "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE":
		kinds = []string{in.Action}
	case "DEFER_TASK", "CONTINUE_TASK":
		kinds = []string{"RECORD_TASK_CHOICE"}
	case "CONFIRM_AND_START_IMPLEMENTATION":
		kinds = []string{"CONFIRM_IMPLEMENTATION", "START_IMPLEMENTATION"}
	case "CONFIRM_IMPLEMENTATION":
		kinds = []string{"CONFIRM_IMPLEMENTATION"}
	case "START_APPROVED":
		kinds = []string{"START_IMPLEMENTATION"}
	case "RETRY_IMPLEMENTATION", "REQUEST_CHANGES":
		kinds = []string{"RESOLVE_CONTINUATION", "START_IMPLEMENTATION"}
	case confirmation.ConfirmRecoveryPurpose:
		kinds = []string{"WRITE_PURPOSE_RECEIPT"}
	}
	out := []confirmation.Step{}
	for _, kind := range kinds {
		out = append(out, confirmation.Step{Kind: kind, RequestID: "confirmation_" + confirmationHash([]string{in.IntentID, kind})[:32], State: "PENDING"})
	}
	return out
}
func (f *Facade) CommitConfirmation(ctx context.Context, in confirmation.CommitInput) (out confirmation.Intent, err error) {
	defer func() {
		defer confirmation.NormalizePublicIntent(&out)
		if out.IntentID == "" {
			return
		}
		observed, readErr := f.deps.Confirmations.ReadSubmission(ctx, confirmation.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID})
		if readErr == nil {
			out.Submission = &observed
		} else if err == nil {
			err = ConfirmationFailure(readErr, "RECEIPT_READBACK")
		}
	}()
	if in.SubmittedHash == "" {
		in.SubmittedHash = confirmation.ValueHash(in)
	}
	original := in
	original.OriginalRequest = nil
	in.OriginalRequest = &original
	current, e := f.ReadConfirmation(ctx, confirmation.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: in.RequestID})
	if e != nil {
		return current, e
	}
	ctx = taskaction.WithIntent(ctx, current.IntentID)
	if prior := current.Submission; prior != nil && prior.Recorded {
		if prior.PayloadSHA256 != in.SubmittedHash {
			return current, confirmation.ErrRequestConflict
		}
		if prior.Outcome == confirmation.RejectedNotConsumed {
			return current, prior.Failure
		}
	}
	if current.Commit != nil {
		// Replay first verifies the original receipt. Resume may continue only
		// canonical-proven, not-started steps; uncertain steps stay fenced.
		if in.Selection.RuntimeConfig != nil && current.Commit.Selection.RuntimeConfig != nil {
			requested, saved := *in.Selection.RuntimeConfig, *current.Commit.Selection.RuntimeConfig
			if requested.Model == saved.Model && requested.ReasoningEffort == saved.ReasoningEffort && requested.CodexFastMode == saved.CodexFastMode && requested.ServiceTier == "" {
				requested.ServiceTier = saved.ServiceTier
				in.Selection.RuntimeConfig = &requested
			}
		}
		out, _, e := f.deps.Confirmations.BeginCommit(ctx, in, nil)
		if e != nil {
			return out, e
		}
		if formalApprovalIntent(out) {
			return f.runFormalExecutionConfirmation(ctx, out)
		}
		for _, chosen := range out.Content.Variants {
			if chosen.ID == out.Commit.VariantID && formalAdoptionIntent(out, chosen) {
				return f.runConfirmationSteps(ctx, out, chosen)
			}
		}
		resumed, fresh, e := f.deps.Confirmations.Resume(ctx, in.AISessionID, in.IntentID, in.Version)
		if e != nil {
			return resumed, e
		}
		if !fresh {
			return f.confirmationAccessReadback(ctx, resumed)
		}
		var chosen confirmation.Variant
		for _, v := range resumed.Content.Variants {
			if v.ID == resumed.Commit.VariantID {
				chosen = v
			}
		}
		return f.runConfirmationSteps(ctx, resumed, chosen)
	}
	variant, preflightErr := func() (confirmation.Variant, error) {
		var variant confirmation.Variant
		if current.State != "PENDING" || !time.Now().Before(current.ExpiresAt) {
			return variant, confirmation.NewFailure("CONFIRMATION_INTENT_NOT_CURRENT", "CORE", "CONFIRMATION_PREFLIGHT", "Read the current target and prepare a new confirmation version.", confirmation.PrepareNewVersion, nil)
		}
		if _, e := confirmationTaskReviewChoice(current, in.Selection.StartReview); e != nil {
			return variant, e
		}
		opts, e := f.ConfirmationOptions(ctx, confirmation.OptionsInput{ReadInput: confirmation.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash}, ProjectID: in.Selection.ProjectID, RepositoryID: in.Selection.RepositoryID, StartReview: in.Selection.StartReview})
		if e != nil {
			return variant, e
		}
		allowed := false
		for _, o := range opts.Variants {
			if o.ID == in.VariantID && o.Available {
				allowed = true
			}
		}
		for _, v := range current.Content.Variants {
			if v.ID == in.VariantID {
				variant = v
			}
		}
		if !allowed {
			for _, option := range opts.Variants {
				if option.ID == in.VariantID && len(option.Failures) > 0 {
					return variant, &option.Failures[0]
				}
			}
			return variant, confirmationSelectionFailure("CONFIRMATION_VARIANT_UNAVAILABLE", "Select a currently available prepared variant.")
		}
		if waiver := in.Selection.VerificationWaiver; waiver != nil {
			if current.Purpose != confirmation.Execution || current.Action != "APPROVE" || variant.ImplementationMode != task.ImplementationDirectFile || len(variant.RepositoryTargets) == 0 {
				return variant, invalid("verification waiver requires explicit DIRECT_FILE v2 approval")
			}
			if e := task.ValidateVerificationSpec(task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: waiver}); e != nil {
				return variant, e
			}
		}
		if e := validateTaskReviewMode(current, in, variant); e != nil {
			return confirmation.Variant{}, e
		}
		coding := !confirmationTaskReviewDeferred(current, in.Selection.StartReview) && current.Purpose != confirmation.RecoveryPurpose && confirmationVariantNeedsRuntimeSelection(current.Action, variant)
		if coding {
			if in.Selection.Backend == nil {
				return variant, confirmationSelectionFailure("BACKEND_SELECTION_REQUIRED", "Select an explicit Coding Agent backend.")
			}
			var backend codexconfig.BackendSelection
			if opts.LockedBackend != nil {
				if in.Selection.Backend.ConfigID != opts.LockedBackend.ConfigID || in.Selection.Backend.ConfigFingerprint != opts.LockedBackend.ConfigFingerprint {
					return variant, confirmationSelectionFailure("FROZEN_BACKEND_SELECTION_CONFLICT", "The approved backend selection is immutable.")
				}
				if e := f.validateFrozenBackendSelection(ctx, in.AISessionID, current.ProjectID, confirmationFeatureAreaID(current), opts.LockedBackend); e != nil {
					return variant, confirmationSelectionFailure("FROZEN_BACKEND_SELECTION_UNAVAILABLE", e.Error())
				}
				backend = *opts.LockedBackend
			} else {
				var e error
				backend, e = f.resolveBackendSelection(ctx, in.AISessionID, current.ProjectID, confirmationFeatureAreaID(current), in.Selection.Backend)
				if e != nil {
					return variant, confirmationSelectionFailure("BACKEND_SELECTION_UNAVAILABLE", e.Error())
				}
			}
			in.BackendSelection = &backend
			if in.Selection.RuntimeConfig == nil {
				return variant, confirmationSelectionFailure("RUNTIME_CONFIGURATION_REQUIRED", "An explicit runtime configuration is required.")
			}
			if strings.TrimSpace(in.Selection.RuntimeConfig.Model) == "" || strings.TrimSpace(in.Selection.RuntimeConfig.ReasoningEffort) == "" {
				return variant, confirmationSelectionFailure("RUNTIME_SELECTION_REQUIRED", "Select both model and reasoning effort.")
			}
			c, e := in.Selection.RuntimeConfig.Normalize(f.ExecutionAllowlist())
			if e != nil {
				return variant, e
			}
			in.Selection.RuntimeConfig = &c
			if opts.LockedRuntimeConfig != nil && c != *opts.LockedRuntimeConfig {
				return variant, confirmation.NewFailure("FROZEN_RUNTIME_SELECTION_CONFLICT", "RUNTIME", "CONFIRMATION_PREFLIGHT", "The approved runtime configuration is immutable.", confirmation.ReadCurrentTarget, nil)
			}
		} else {
			if in.Selection.RuntimeConfig != nil || in.Selection.Backend != nil || in.BackendSelection != nil {
				return variant, confirmationSelectionFailure("BACKEND_SELECTION_NOT_APPLICABLE", "This executor does not accept Coding Agent backend selection.")
			}
		}
		if current.Purpose == confirmation.ProjectSetup {
			if e := validateProjectSetupSelection(current, in.Selection, opts); e != nil {
				return variant, e
			}
		} else if in.Selection.ProjectID != "" || in.Selection.RepositoryID != "" || in.Selection.DevelopmentRef != "" || len(in.Selection.DevelopmentContexts) > 0 {
			return variant, invalid("project selection is only accepted for PROJECT_SETUP")
		}
		return variant, nil
	}()
	if preflightErr != nil {
		return f.deps.Confirmations.RejectCommit(ctx, in, ConfirmationFailure(preflightErr, "CONFIRMATION_PREFLIGHT"))
	}
	stepIntent := current
	stepIntent.Commit = &in
	current, fresh, e := f.deps.Confirmations.BeginCommit(ctx, in, confirmationSteps(stepIntent))
	if e != nil || !fresh {
		return current, e
	}
	return f.runConfirmationSteps(ctx, current, variant)
}
func (f *Facade) runConfirmationSteps(ctx context.Context, current confirmation.Intent, variant confirmation.Variant) (confirmation.Intent, error) {
	if formalApprovalIntent(current) {
		return f.runFormalExecutionConfirmation(ctx, current)
	}
	if current.State == "COMPLETED" && formalAdoptionIntent(current, variant) {
		archive, readErr := f.finishFormalAdoption(ctx, current)
		if readErr != nil && !errors.Is(readErr, errFormalAdoptionNotApplicable) {
			return current, readErr
		}
		if readErr == nil && archive.Archive.AtomicConfirmation && (len(current.Steps) != 1 || current.Steps[0].RunID != "" || current.Steps[0].WorkflowID != "" || current.Steps[0].Effect != "PREPARATION_READY") {
			return current, formalexecution.ErrCorrupt
		}
		if readErr == nil {
			if len(current.Steps) != 1 || current.Steps[0].Kind != "START_IMPLEMENTATION" || current.Steps[0].TaskID != archive.Archive.TaskID || current.Steps[0].RevisionID != archive.Archive.RevisionID || current.Steps[0].ExecutionID != archive.Archive.ExecutionID {
				return current, formalexecution.ErrCorrupt
			}
		}
		return f.confirmationAccessReadback(ctx, current)
	}
	in := current.Commit
	var e error
	for index := range current.Steps {
		step := current.Steps[index]
		if step.State == "COMPLETED" {
			continue
		}
		if step.State != "PENDING" {
			return f.confirmationAccessReadback(ctx, current)
		}
		step.State = "UNKNOWN"
		current, e = f.deps.Confirmations.RecordStep(ctx, in.AISessionID, in.IntentID, in.Version, step, "CONFIRMING")
		if e != nil {
			return current, e
		}
		// The original subrequest is durable before any business command. No write
		// transaction or file-operation lock spans the command below.
		step, e = f.performConfirmationStep(ctx, current, variant, step)
		state := "CONFIRMING"
		if e != nil {
			stage := "BUSINESS_STEP"
			if step.Effect == "RESERVED" {
				stage = "DISPATCH"
			}
			step.Failure = ConfirmationFailure(e, stage)
			step.Code, step.Message = step.Failure.Code, step.Failure.Message
			if step.Effect == "" {
				step.Effect = "UNKNOWN"
			}
			state = "UNKNOWN"
			if index > 0 {
				state = "PARTIAL"
			}
			if code, definite := confirmationDefiniteRejection(e, step); definite {
				step.State = "REJECTED"
				step.Code = code
				step.Message = step.Failure.Message
				if index == 0 {
					state = "REJECTED"
				}
			}

		} else {
			step.State = "COMPLETED"
			if step.Effect == "" {
				step.Effect = "COMMAND_COMPLETED"
			}
			if index == len(current.Steps)-1 {
				state = "COMPLETED"
			}
		}
		saved, saveErr := f.deps.Confirmations.RecordStep(context.WithoutCancel(ctx), in.AISessionID, in.IntentID, in.Version, step, state)
		if saveErr != nil {
			return current, ConfirmationFailure(saveErr, "RECEIPT_READBACK")
		}
		current = saved
		if e != nil {
			value, readErr := f.confirmationAccessReadback(ctx, current)
			if readErr != nil {
				return value, ConfirmationFailure(readErr, "RECEIPT_READBACK")
			}
			return value, step.Failure
		}
	}
	return f.confirmationAccessReadback(ctx, current)
}
func (f *Facade) performConfirmationStep(ctx context.Context, intent confirmation.Intent, v confirmation.Variant, step confirmation.Step) (result confirmation.Step, resultErr error) {
	entered := false
	defer func() {
		if resultErr != nil && !entered {
			result.Effect = "NOT_STARTED"
			resultErr = &confirmationStepNotStarted{cause: resultErr}
		}
	}()
	in := intent.Commit
	if in != nil {
		copy := *in
		in = &copy
	}
	session := intent.AISessionID
	project := intent.ProjectID
	areaID, taskID, revision, executionID := "", "", "", ""
	if b := intent.Content.Task; b != nil {
		areaID = b.FeatureAreaID
	}
	if b := intent.Content.Review; b != nil {
		areaID = b.FeatureAreaID
		taskID = b.TaskID
	}
	if b := intent.Content.Execution; b != nil {
		areaID = b.FeatureAreaID
		taskID = b.TaskID
		revision = b.TaskDefinitionRevisionID
		executionID = b.ExecutionID
	}
	for _, s := range intent.Steps {
		if s.State == "COMPLETED" {
			if s.ProjectID != "" {
				project = s.ProjectID
			}
			if s.FeatureAreaID != "" {
				areaID = s.FeatureAreaID
			}
			if s.TaskID != "" {
				taskID = s.TaskID
			}
			if s.RevisionID != "" {
				revision = s.RevisionID
			}
			if s.ExecutionID != "" {
				executionID = s.ExecutionID
			}
		}
	}
	if in != nil && in.BackendSelection == nil && in.Selection.Backend != nil && confirmationVariantUsesCodex(intent.Action, v) {
		resolved, resolveErr := f.resolveBackendSelection(ctx, session, project, areaID, in.Selection.Backend)
		if resolveErr != nil {
			return step, resolveErr
		}
		in.BackendSelection = &resolved
	}
	step.ProjectID = project
	step.FeatureAreaID = areaID
	step.TaskID = taskID
	switch step.Kind {
	case "CONFIRM_IMPLEMENTATION":
		writer, ok := f.deps.Tasks.(interface {
			ConfirmUnreviewedImplementation(context.Context, task.UnreviewedImplementationInput) (task.Task, error)
		})
		if !ok || in == nil || v.WorkSpec == nil || v.VerificationSpec == nil {
			return step, invalid("implementation definition confirmation unavailable")
		}
		entered = true
		value, err := writer.ConfirmUnreviewedImplementation(ctx, task.UnreviewedImplementationInput{AISessionID: session, TaskID: taskID, IntentID: intent.IntentID, RequestID: in.RequestID, ExpectedRevisionID: revision, RepositoryID: intent.Content.Execution.RepositoryID, BaseCommit: intent.Content.Execution.BaseCommit, WorkSpec: *v.WorkSpec, VerificationSpec: *v.VerificationSpec})
		step.TaskID = value.ID
		step.RevisionID = value.CurrentDefinitionRevisionID
		if err == nil {
			step.Effect = "COMMAND_COMPLETED"
		}
		return step, err
	case "WRITE_PURPOSE_RECEIPT":
		writer, ok := f.deps.Confirmations.(interface {
			WriteRecoveryPurposeReceipt(context.Context, confirmation.CommitInput) error
		})
		if !ok || in == nil || intent.Purpose != confirmation.RecoveryPurpose {
			return step, invalid("purpose receipt writer unavailable")
		}
		entered = true
		e := writer.WriteRecoveryPurposeReceipt(ctx, *in)
		if e == nil {
			step.Effect = "COMMAND_COMPLETED"
		}
		return step, e
	case "BIND_PROJECT":
		ref, e := confirmationCallerReference(ctx)
		if e != nil {
			return step, e
		}
		entered = true
		result, e := f.deps.ConfirmationSessions.BindProject(ctx, ref, session, in.Selection.ProjectID, step.RequestID)
		if result.BoundProjectID != "" {
			step.ProjectID = result.BoundProjectID
		}
		if e == nil && result.PreparationStatus == "FAILED" {
			return step, confirmation.NewFailure("PROJECT_PREPARATION_INCOMPLETE", "CORE", "BUSINESS_STEP", "The Project is bound, but its preparation did not complete.", confirmation.ReadCurrentTarget, nil)
		}
		return step, e
	case "SELECT_BRANCH":
		entered = true
		repositoryID, developmentRef := in.Selection.RepositoryID, in.Selection.DevelopmentRef
		if intent.Action == "CONFIGURE_DEVELOPMENT_CONTEXT" {
			repositoryID, developmentRef = step.RepositoryID, step.DevelopmentRef
		}
		result, e := f.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: session, ProjectID: in.Selection.ProjectID, RepositoryID: repositoryID, DevelopmentRef: developmentRef, RequestID: step.RequestID})
		step.ProjectID = result.ProjectID
		return step, e
	case "CREATE_AREA":
		b := intent.Content.FeatureArea
		entered = true
		result, e := f.CreateFeatureAreaWithInitialReview(ctx, FeatureAreaCreateInput{AISessionID: session, Title: b.Title, ProblemStatement: b.ProblemStatement, Mode: CreateOnly, RequestID: step.RequestID})
		step.FeatureAreaID = result.FeatureArea.ID
		return step, e
	case "CREATE_TASK":
		b := intent.Content.Task
		if b == nil {
			b = intent.Content.FeatureArea.InitialTask
		}
		prepared, e := f.confirmationTaskInput(ctx, session, areaID, *b, step.RequestID, in.Selection.ReviewMode == "NONE", b.StartReview && !confirmationTaskReviewDeferred(intent, in.Selection.StartReview) && v.ExecutorKind == string(review.ExecutorCodingAgent))
		if e != nil {
			return step, e
		}
		prepared.SkipReview = in.Selection.ReviewMode == "NONE"
		entered = true
		result, e := f.CreatePlanningTaskForSession(ctx, prepared)
		step.TaskID = result.ID
		step.RevisionID = result.CurrentDefinitionRevisionID
		return step, e
	case "REQUEST_REVIEW":
		b := intent.Content.Review
		entered = true
		result, e := f.RequestTaskReview(ctx, wp.TaskReviewRequestInput{AISessionID: session, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, RequestID: step.RequestID, Purpose: b.SupplementalPurpose, ExpectedTarget: b.ExpectedTarget})
		step.TaskID = result.TaskID
		return step, e
	case "START_REVIEW":
		if confirmationTaskReviewDeferred(intent, in.Selection.StartReview) {
			return step, invalid("This Task confirmation did not select Review startup")
		}
		b := intent.Content.Review
		if b == nil {
			t := intent.Content.Task
			if t == nil {
				t = intent.Content.FeatureArea.InitialTask
			}
			b = &confirmation.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryID: t.RepositoryID, ReviewDepth: t.ReviewDepth, ReviewPlan: t.ReviewPlan}
		}
		if step.ReviewID != "" && step.RunID != "" {
			payload := confirmationHash([]any{intent.ContentHash, in.VariantID, in.Selection, b})
			saved, found, e := f.ReplayAdmittedReview(ctx, session, taskID, areaID, step.RequestID, payload)
			if e != nil {
				return step, e
			}
			if !found || saved.Review.ID != step.ReviewID || saved.Run.ID != step.RunID {
				return step, invalid("original Review admission is unavailable")
			}
			entered = true
			resumed, e := f.StartAdmittedReview(ctx, scopereview.AdmittedStartInput{AISessionID: session, RequestID: step.RequestID, PayloadSHA256: payload, Review: saved.Review})
			if resumed.Run.ID != "" {
				step.RunID = resumed.Run.ID
			}
			return step, e
		}
		owner, e := f.deps.Tasks.Get(ctx, taskID)
		if e != nil {
			return step, e
		}
		if owner.ProjectID != project || owner.FeatureAreaID != areaID || owner.RepositoryID != b.RepositoryID {
			return step, notFound()
		}
		var plan ReviewPlanResolution
		if len(b.RepositoryTargets) > 0 {
			plan = ReviewPlanResolution{Plan: b.ReviewPlan, Scope: b.ReviewPlan.InitialScope}
		} else {
			plan, e = ResolveInitialReviewPlan(ctx, f.deps.StartupContext, session, b.RepositoryID, InitialReviewPlan{Mode: string(b.ReviewPlan.Mode), SelectedModules: b.ReviewPlan.SelectedModules})
		}
		if e != nil {
			return step, e
		}
		if len(plan.Blockers) > 0 {
			return step, invalid(strings.Join(plan.Blockers, "; "))
		}
		if plan.Plan.InitialScope.Kind != b.ReviewPlan.InitialScope.Kind || !slices.Equal(plan.Plan.InitialScope.Paths, b.ReviewPlan.InitialScope.Paths) {
			return step, invalid("Review scope changed; prepare a new version")
		}
		if intent.Content.Review != nil {
			if intent.Action == "START_REVIEW" {
				expected := b.ExpectedTarget
				if expected == nil || owner.Phase != expected.Phase || owner.CurrentReviewID != expected.CurrentReviewID || owner.CurrentDefinitionRevisionID != expected.TaskDefinitionRevisionID || owner.CurrentExecutionID != expected.CurrentExecutionID {
					return step, invalid("Review target changed")
				}
			}
			if intent.Action == "REQUEST_SUPPLEMENTAL_REVIEW" {
				reader, ok := f.deps.WorkflowProgression.(interface {
					TaskReviewRequestEligibility(context.Context, string, string) (wp.TaskReviewRequestEligibility, error)
				})
				if !ok {
					return step, invalid("Supplemental receipt reader unavailable")
				}
				eligibility, e := reader.TaskReviewRequestEligibility(ctx, session, taskID)
				if e != nil {
					return step, e
				}
				expectedRequest := ""
				for _, s := range intent.Steps {
					if s.Kind == "REQUEST_REVIEW" {
						expectedRequest = s.RequestID
					}
				}
				if owner.Phase != task.PhaseReviewRequired || eligibility.CurrentRequest == nil || eligibility.CurrentRequest.RequestID != expectedRequest {
					return step, invalid("Supplemental request changed")
				}
			}
		}
		rv := review.Review{WorkspaceID: b.WorkspaceID, RepositoryTargets: b.RepositoryTargets, RuntimeConfirmationID: intent.IntentID, TaskID: taskID, ProjectID: project, FeatureAreaID: areaID, CreatedByAISessionID: session, RepositoryID: b.RepositoryID, ExecutorKind: review.ExecutorKind(v.ExecutorKind), ReviewDepth: b.ReviewDepth, Instruction: v.ReviewInstruction, EngineeringGoal: owner.ProblemStatement, Focus: review.Focus{Kind: review.FocusKind(plan.Plan.Mode), Summary: v.Summary, Paths: plan.Scope.Paths, Modules: plan.Plan.SelectedModules}}
		rv.PrimaryMemberID = v.PrimaryMemberID
		if in.Selection.RuntimeConfig != nil {
			rv.RuntimeConfig = *in.Selection.RuntimeConfig
			rv.RuntimeConfirmationID = intent.IntentID
			rv.Model = rv.RuntimeConfig.Model
			rv.ReasoningEffort = rv.RuntimeConfig.ReasoningEffort
			if in.BackendSelection == nil {
				return step, errors.New("explicit backend selection required")
			}
			if e := f.validateFrozenBackendSelection(ctx, session, project, areaID, in.BackendSelection); e != nil {
				return step, e
			}
			rv.CodexConfigID = in.BackendSelection.ConfigID
			selection := *in.BackendSelection
			rv.BackendSelection = &selection
		}
		entered = true
		result, e := f.StartAdmittedReview(ctx, scopereview.AdmittedStartInput{AISessionID: session, RequestID: step.RequestID, PayloadSHA256: confirmationHash([]any{intent.ContentHash, in.VariantID, in.Selection, b}), Review: rv})
		step.ReviewID = result.Review.ID
		step.RunID = result.Run.ID
		if e != nil && result.Run.ID != "" {
			step.Effect = "RESERVED"
		}
		if e == nil && rv.ExecutorKind.Effective() != review.ExecutorUpperLLM {
			reader, ok := f.deps.ExecutionProposals.(interface {
				ReadHandoff(context.Context, string, string) (executionproposal.Handoff, error)
			})
			if !ok {
				e = errors.New("runtime handoff evidence unavailable")
			} else {
				receipt, readErr := reader.ReadHandoff(ctx, project, result.Run.ID)
				e = readErr
				if e == nil && !receipt.Accepted() {
					e = &executionproposal.HandoffError{Handoff: receipt}
				}
				if e == nil {
					step.Effect = "RUNTIME_HANDOFF_ACCEPTED"
				}
			}
			if e != nil && step.RunID != "" {
				step.Effect = "RESERVED"
			}
		}

		return step, e
	case "APPROVE":
		b := intent.Content.Execution
		// Receipt recovery precedes current Task/Decision checks. A durable
		// business snapshot owns the historical output even after owner drift.
		entered = true
		archived, archiveErr := f.finishDefinitionApproval(ctx, intent)
		if archiveErr == nil {
			step.TaskID = archived.Snapshot.Fence.TaskID
			step.RevisionID = archived.Snapshot.RevisionID
			return step, nil
		}
		if !definitionApprovalAbsent(archiveErr) {
			return step, archiveErr
		}
		startRequestID := ""
		startEffect := wp.StartEffectNoStart
		if intent.Action == "APPROVE_AND_START" && v.ImplementationMode == task.ImplementationCodingAgent {
			startRequestID = "confirmation_" + confirmationHash([]string{intent.IntentID, "START_IMPLEMENTATION"})[:32]
			startEffect = wp.StartEffectStartAuthorized
		}
		for _, pending := range intent.Steps {
			if pending.Kind == "START_IMPLEMENTATION" && v.ImplementationMode == task.ImplementationCodingAgent {
				startRequestID = pending.RequestID
				startEffect = wp.StartEffectStartAuthorized
			}
		}
		entered = true
		result, e := f.ResolveReviewApproval(ctx, wp.ResolveInput{AISessionID: session, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, ReviewID: b.ReviewID, DecisionID: b.DecisionID, Option: "APPROVE", RequestID: step.RequestID, ConfirmationIntentID: intent.IntentID, ExecutionInstructions: v.ExecutionInstructions, NextRunRuntimeConfig: in.Selection.RuntimeConfig, BackendSelection: in.BackendSelection, StartEffect: startEffect, StartRequestID: startRequestID})
		if result.Task != nil {
			step.TaskID = result.Task.ID
			step.RevisionID = result.Task.CurrentDefinitionRevisionID
		}
		if result.DirectFile != nil {
			step.WorkflowID = result.DirectFile.WorkflowID
		}
		if e == nil {
			archived, archiveErr = f.finishDefinitionApproval(ctx, intent)
			if archiveErr == nil {
				step.TaskID = archived.Snapshot.Fence.TaskID
				step.RevisionID = archived.Snapshot.RevisionID
			} else if !errors.Is(archiveErr, formalapproval.ErrNotApplicable) {
				// The business writer must already have sealed the final receipt.
				// Missing or corrupt evidence cannot authorize a repair transaction.
				step.Effect = "COMMAND_COMPLETED"
				return step, archiveErr
			}
		}
		return step, e
	case "RESOLVE_REVIEW_DECISION":
		b := intent.Content.Review
		confirmationIntentID := ""
		if intent.Action == "REVISE" {
			confirmationIntentID = intent.IntentID
		}
		entered = true
		result, e := f.ResolveReviewApproval(ctx, wp.ResolveInput{AISessionID: session, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, ReviewID: b.ReviewID, DecisionID: b.DecisionID, Option: intent.Action, RequestID: step.RequestID, ConfirmationIntentID: confirmationIntentID, ReviewFeedback: b.ReviewFeedback})
		if result.Task != nil {
			step.TaskID = result.Task.ID
			step.RevisionID = result.Task.CurrentDefinitionRevisionID
		}
		if result.SuccessorReviewID != "" {
			step.ReviewID = result.SuccessorReviewID
			step.RunID = result.SuccessorRunID
			step.StartRequestID = result.ReviewStartRequestID
			step.Effect = "RESERVED"
		}
		return step, e
	case "RESOLVE_ACCEPTANCE_DECISION":
		b := intent.Content.Execution
		entered = true
		option := intent.Action
		if option == "COMPLETE_TASK" {
			option = "ACCEPT"
		}
		result, e := f.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: session, TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, TaskDefinitionRevisionID: b.TaskDefinitionRevisionID, ExecutionID: b.ExecutionID, DecisionID: b.DecisionID, Option: option, RequestID: step.RequestID, CompletionPlan: b.CompletionPlan})
		step.ExecutionID = result.SuccessorExecutionID
		if e == nil {
			step.RevisionID = result.Task.CurrentDefinitionRevisionID
		}
		return step, e
	case "RECORD_TASK_CHOICE":
		b := intent.Content.Execution
		entered = true
		result, e := f.ChangeTaskHoldForSession(ctx, session, task.HoldInput{TaskID: b.TaskID, FeatureAreaID: b.FeatureAreaID, ExecutionID: b.ExecutionID, RevisionID: b.TaskDefinitionRevisionID, Action: intent.Action, RequestID: step.RequestID, Note: b.Note})
		step.TaskID, step.ExecutionID, step.RevisionID = result.ID, result.CurrentExecutionID, result.CurrentDefinitionRevisionID
		return step, e
	case "RESOLVE_CONTINUATION":
		b := intent.Content.Execution
		input := wp.TaskAcceptanceInput{AISessionID: session, TaskID: taskID, FeatureAreaID: areaID, TaskDefinitionRevisionID: revision, ExecutionID: executionID, DecisionID: b.DecisionID, Option: intent.Action, RequestID: step.RequestID, ExecutionInstructions: v.ExecutionInstructions, NextRunRuntimeConfig: in.Selection.RuntimeConfig, BackendSelection: in.BackendSelection, StartEffect: wp.StartEffectStartAuthorized}
		if intent.Action == "REQUEST_CHANGES" {
			input.NextWorkSpec = v.WorkSpec
			input.NextVerificationSpec = v.VerificationSpec
			input.NextRepositoryTargets = v.RepositoryTargets
			input.NextPrimaryMemberID = v.PrimaryMemberID
			input.ConfirmationIntentID = intent.IntentID
		}
		entered = true
		result, e := f.ResolveTaskAcceptance(ctx, input)
		step.ExecutionID = result.SuccessorExecutionID
		step.RevisionID = result.Task.CurrentDefinitionRevisionID
		return step, e
	case "MERGE_FEATURE_AREA":
		b := intent.Content.FeatureArea
		store, ok := f.deps.WorkflowProgression.(featureAreaPlanStore)
		if !ok || b == nil || b.DeliveryPlan == nil {
			return step, invalid("exact FeatureArea delivery plan required")
		}
		step.FeatureAreaID = b.FeatureAreaID
		entered = true
		_, e := store.CommitFeatureAreaPlan(ctx, project, b.FeatureAreaID, step.RequestID, *b.DeliveryPlan)
		return step, e
	case "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE":
		if f.deps.DirectFiles == nil {
			return step, invalid("Direct File unavailable")
		}
		b := intent.Content.Execution
		entered = true
		state, e := f.deps.DirectFiles.StartTaskAuthorization(ctx, directfile.StartInput{AISessionID: session, TaskID: taskID, TaskDefinitionRevisionID: revision, DecisionID: b.DecisionID, RequestID: step.RequestID, ConfirmationIntentID: intent.IntentID, Action: step.Kind, WorkflowID: b.WorkflowID, RuntimeEpoch: b.RuntimeEpoch, GrantGeneration: b.GrantGeneration})
		if e == nil && state.State == "PREPARING" && step.Kind != "CANCEL_DIRECT_FILE" {
			observed, reconcileErr := f.deps.DirectFiles.ReconcileTaskAuthorization(ctx, directfile.Registration{AISessionID: session, TaskID: taskID, TaskDefinitionRevisionID: revision, DecisionID: b.DecisionID, ImplementationMode: string(task.ImplementationDirectFile), Confirmed: true})
			e = reconcileErr
			if observed.WorkflowID != "" {
				state = observed
			}
		}
		if e == nil && ((step.Kind == "CANCEL_DIRECT_FILE" && state.State != "CLOSED") || (step.Kind != "CANCEL_DIRECT_FILE" && state.State != "ACTIVE")) {
			e = directfile.Failure(directfile.NotEnabled, "exact Direct File confirmation effect is not complete")
		}
		step.WorkflowID, step.RevisionID, step.TaskID = state.WorkflowID, state.TaskDefinitionRevisionID, taskID
		if e != nil && state.WorkflowID != "" {
			step.Effect = "RESERVED"
		}
		return step, e
	case "START_IMPLEMENTATION":
		if v.ImplementationMode == task.ImplementationDirectFile {
			state, e := f.deps.DirectFiles.TaskStatus(ctx, session, taskID)
			if e == nil && (state.TaskID != taskID || state.ProjectID != project || state.FeatureAreaID != areaID || state.TaskDefinitionRevisionID != revision) {
				return step, invalid("Direct File authorization target changed")
			}
			step.WorkflowID = state.WorkflowID
			step.RevisionID = state.TaskDefinitionRevisionID
			if e == nil && state.State != "ACTIVE" {
				return step, confirmation.NewFailure("DIRECT_FILE_ACCESS_NOT_READY", "DOMAIN", "BUSINESS_STEP", "Current Direct File authorization is not ACTIVE; read its exact access state.", confirmation.ReadCurrentTarget, nil)
			}
			return step, e
		}
		if intent.Action == "APPROVE_AND_START" {
			// The preceding APPROVE step atomically created the revision,
			// Execution and START_AUTHORIZED effect. Re-read those exact facts;
			// this composition step is an idempotent dispatch/readback of the
			// same start request, never a second authorization.
			current, readErr := f.deps.Tasks.Get(ctx, taskID)
			if readErr != nil {
				return step, readErr
			}
			revision, executionID = current.CurrentDefinitionRevisionID, current.CurrentExecutionID
		}
		expected := wp.TaskExecutionExpectation{FeatureAreaID: areaID, TaskDefinitionRevisionID: revision, ExecutionID: executionID, Phase: task.PhaseImplementationReady}
		if intent.Action == "APPROVE_AND_START" && executionID != "" {
			proposal, readErr := f.deps.ExecutionProposals.Get(ctx, session, executionID)
			if readErr != nil {
				return step, readErr
			}
			expected.PrimaryMemberID = proposal.PrimaryMemberID
			expected.ApprovedWorksetHash = proposal.ApprovedWorksetHash
		}
		if intent.Action == "START_APPROVED" && intent.Content.Execution.ExpectedTarget != nil {
			expected = *intent.Content.Execution.ExpectedTarget
		}
		if formalAdoptionIntent(intent, v) {
			entered = true
			adopted, adoptErr := f.finishFormalAdoption(ctx, intent)
			if adopted.Archive.ExecutionID != "" {
				step.ExecutionID = adopted.Archive.ExecutionID
				step.RevisionID = adopted.Archive.RevisionID
				step.TaskID = adopted.Archive.TaskID
			}
			if adoptErr != nil {
				return step, adoptErr
			}
			step.Effect = "PREPARATION_READY"
			return step, nil
		}
		// A continuation creates its successor in the prior durable step. Fill
		// only omitted v2 layout facts from that exact proposal; supplied facts
		// remain assertions checked by the reservation store.
		continuation := intent.Action == "REQUEST_CHANGES" || intent.Action == "RETRY_IMPLEMENTATION"
		if continuation && expected.ExecutionID != "" && (expected.PrimaryMemberID == "" || expected.ApprovedWorksetHash == "") {
			proposal, readErr := f.deps.ExecutionProposals.Get(ctx, session, expected.ExecutionID)
			if readErr != nil {
				return step, readErr
			}
			if expected.PrimaryMemberID == "" {
				expected.PrimaryMemberID = proposal.PrimaryMemberID
			}
			if expected.ApprovedWorksetHash == "" {
				expected.ApprovedWorksetHash = proposal.ApprovedWorksetHash
			}
		}
		entered = true
		result, e := f.StartTaskExecutionExpectedCommand(ctx, session, taskID, "START_EXECUTION", step.RequestID, expected)
		if result.Proposal.ID != "" {
			step.ExecutionID = result.Proposal.ID
		}
		step.RunID = result.Run.ID
		if result.Proposal.TaskDefinitionRevisionID != "" {
			step.RevisionID = result.Proposal.TaskDefinitionRevisionID
		}
		if e != nil && result.Run.ID != "" {
			step.Effect = "RESERVED"
		}
		if e == nil && !result.Handoff.Accepted() {
			step.Effect = "RESERVED"
			e = &executionproposal.HandoffError{Handoff: result.Handoff}
		}
		if e == nil {
			step.Effect = "RUNTIME_HANDOFF_ACCEPTED"
		}

		return step, e
	}
	return step, invalid("unsupported confirmation step")
}

func (f *Facade) confirmationTaskInput(ctx context.Context, session, area string, b confirmation.TaskBody, request string, skipReview, codingRequired bool) (out CreatePlanningTaskInput, err error) {
	out = CreatePlanningTaskInput{SkipReview: skipReview, AISessionID: session, FeatureAreaID: area, RepositoryID: b.RepositoryID, Title: b.Title, ProblemStatement: b.ProblemStatement, Constraints: b.Constraints, RequestID: request}
	// Canonical replay precedes current metadata/default reads.
	if _, e := f.ReplayPlanningTaskForSession(ctx, out); e == nil {
		return out, nil
	} else if !errors.Is(e, task.ErrNotFound) {
		return out, e
	}
	prerequisites, e := f.confirmationCreationPrerequisites(ctx, session, b.RepositoryID, codingRequired)
	if e != nil {
		return out, e
	}
	out.CodexConfigID, out.ModuleID, out.BaseRevision = prerequisites.CodexConfigID, prerequisites.ModuleID, prerequisites.BaseRevision
	return out, nil
}

func confirmationDefiniteRejection(err error, step confirmation.Step) (string, bool) {
	var before *confirmationStepNotStarted
	if !errors.As(err, &before) {
		return "", false
	}
	return ConfirmationFailure(before.cause, "BUSINESS_STEP").Code, true
}

func (f *Facade) confirmationAccessReadback(ctx context.Context, intent confirmation.Intent) (confirmation.Intent, error) {
	f.attachReviewStartPreparation(ctx, &intent)
	if intent.Commit == nil {
		return intent, nil
	}
	var mode task.ImplementationMode
	var executor string
	for _, v := range intent.Content.Variants {
		if v.ID == intent.Commit.VariantID {
			mode = v.ImplementationMode
			executor = v.ExecutorKind
		}
	}
	if executor == string(review.ExecutorUpperLLM) {
		for _, s := range intent.Steps {
			if s.ReviewID != "" {
				access, e := f.UpperReviewAccessForSession(ctx, intent.AISessionID, s.ReviewID)
				if e != nil {
					intent.CurrentAccessReadError = "UPPER_REVIEW_ACCESS_UNAVAILABLE"
					return intent, nil
				}
				if access.Target.ReviewID != s.ReviewID || access.Target.RunID != s.RunID {
					intent.CurrentAccessReadError = "UPPER_REVIEW_TARGET_CHANGED"
					return intent, nil
				}
				intent.CurrentUpperReview = &access
				if reader, ok := f.deps.UpperReviewAccess.(interface {
					ConfirmationReadEligibility(context.Context, review.UpperTarget) (bool, error)
				}); ok {
					available, e := reader.ConfirmationReadEligibility(ctx, access.Target)
					if e != nil {
						intent.CurrentAccessReadError = "UPPER_READ_ELIGIBILITY_UNAVAILABLE"
					} else {
						intent.CurrentUpperReadAvailable = available
					}
				} else {
					intent.CurrentAccessReadError = "UPPER_READ_ELIGIBILITY_UNAVAILABLE"
				}
				break
			}
		}
	}
	if mode == task.ImplementationDirectFile {
		taskID, revision, workflow := "", "", ""
		if b := intent.Content.Execution; b != nil {
			taskID = b.TaskID
			revision = b.TaskDefinitionRevisionID
		}
		for _, s := range intent.Steps {
			if s.TaskID != "" {
				taskID = s.TaskID
			}
			if s.RevisionID != "" {
				revision = s.RevisionID
			}
			if s.WorkflowID != "" {
				workflow = s.WorkflowID
			}
		}
		if taskID != "" && f.deps.DirectFiles != nil {
			access, e := f.deps.DirectFiles.TaskStatus(ctx, intent.AISessionID, taskID)
			if e != nil {
				intent.CurrentAccessReadError = "DIRECT_FILE_ACCESS_UNAVAILABLE"
				return intent, nil
			}
			if access.TaskID != taskID || access.TaskDefinitionRevisionID != revision || (workflow != "" && access.WorkflowID != workflow) {
				intent.CurrentAccessReadError = "DIRECT_FILE_TARGET_CHANGED"
				return intent, nil
			}
			intent.CurrentDirectFile = &access
		}
	}
	return intent, nil
}

func confirmationDirectFileAction(action string) bool {
	return action == "START_DIRECT_FILE" || action == "RECOVER_DIRECT_FILE" || action == "CANCEL_DIRECT_FILE"
}

// Direct File changes stay in GPT; actual verification has its own frozen Codex selection.
func directFileCodexVerification(v confirmation.Variant) bool {
	return v.ImplementationMode == task.ImplementationDirectFile && v.VerificationSpec != nil && v.VerificationSpec.Mode == task.VerificationModeRun
}
func confirmationVariantUsesCodex(action string, v confirmation.Variant) bool {
	return v.ExecutorKind == string(review.ExecutorCodingAgent) || v.ImplementationMode == task.ImplementationCodingAgent || directFileCodexVerification(v) && (action == "START_DIRECT_FILE" || (action == "APPROVE" || action == "APPROVE_AND_START") && len(v.RepositoryTargets) == 0)
}

// Scalar approval freezes its explicit runtime selection even with NO_START.
// Workset APPROVE only freezes the definition; runtime authority is selected by
// its later start. Options and commit must apply this same distinction.
func confirmationVariantNeedsRuntimeSelection(action string, v confirmation.Variant) bool {
	return action != "CONFIRM_IMPLEMENTATION" && (action != "APPROVE" || len(v.RepositoryTargets) == 0) && confirmationVariantUsesCodex(action, v)
}
