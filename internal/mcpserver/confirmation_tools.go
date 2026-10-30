package mcpserver

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"strings"

	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/taskaction"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const confirmationResourceURI = "ui://devbridge/confirmation/v14"
const confirmationComponentVersion = "PURPOSE_CONFIRMATION_V14"

//go:embed confirmation_ui.html
var confirmationHTML string

type confirmationService interface {
	PrepareConfirmation(context.Context, c.PrepareInput) (c.Intent, error)
	ReprepareConfirmation(context.Context, c.ReprepareInput) (c.Intent, error)
	ReadConfirmation(context.Context, c.ReadInput) (c.Intent, error)
	ConfirmationOptions(context.Context, c.OptionsInput) (c.Options, error)
	CommitConfirmation(context.Context, c.CommitInput) (c.Intent, error)
}

// The projection is optional and deliberately outside the frozen Intent.
func (s *StaticService) ConfirmationDisplay(ctx context.Context, intent c.Intent) *c.DisplayMetadata {
	if s.control == nil {
		return nil
	}
	return s.control.ConfirmationDisplay(ctx, intent)
}

func (s *StaticService) PrepareConfirmation(ctx context.Context, in c.PrepareInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation service unavailable")
	}
	return s.control.PrepareConfirmation(ctx, in)
}
func (s *StaticService) ReprepareConfirmation(ctx context.Context, in c.ReprepareInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation service unavailable")
	}
	return s.control.ReprepareConfirmation(ctx, in)
}
func (s *StaticService) ReadConfirmation(ctx context.Context, in c.ReadInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation service unavailable")
	}
	return s.control.ReadConfirmation(ctx, in)
}
func (s *StaticService) ConfirmationOptions(ctx context.Context, in c.OptionsInput) (c.Options, error) {
	if s.control == nil {
		return c.Options{}, errors.New("confirmation options unavailable")
	}
	return s.control.ConfirmationOptions(ctx, in)
}
func (s *StaticService) CommitConfirmation(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation commit unavailable")
	}
	return s.control.CommitConfirmationAndStart(ctx, in)
}

func (s *StaticService) WithdrawConfirmationSubmission(ctx context.Context, in c.CommitInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation submission withdrawal unavailable")
	}
	return s.control.WithdrawConfirmationSubmission(ctx, in)
}

func (s *StaticService) DiscardConfirmationProposal(ctx context.Context, in c.ReadInput) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation service unavailable")
	}
	return s.control.DiscardConfirmationProposal(ctx, in)
}

func (s *StaticService) ResolveTaskActionConflict(ctx context.Context, in taskaction.ConflictSelection) (c.Intent, error) {
	if s.control == nil {
		return c.Intent{}, errors.New("confirmation service unavailable")
	}
	return s.control.ResolveTaskActionConflict(ctx, in)
}

func confirmationScriptSHA256() string {
	const start = `<script id="confirmation-main">`
	i := strings.Index(confirmationHTML, start)
	if i < 0 {
		return ""
	}
	source := confirmationHTML[i+len(start):]
	end := strings.Index(source, "</script>")
	if end < 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(source[:end]))
	return hex.EncodeToString(sum[:])
}
func confirmationToolMeta(appOnly bool) sdkmcp.Meta {
	visibility := []string{"model", "app"}
	if appOnly {
		visibility = []string{"app"}
	}
	return sdkmcp.Meta{"ui": map[string]any{"visibility": visibility}}
}

// confirmationResponse keeps call failure distinct from authorized durable facts.
// An empty or inaccessible intent is omitted, never serialized as a zero DTO.
func confirmationResponse(out c.Intent, err error, stage string) (*sdkmcp.CallToolResult, c.Result, error) {
	if err == nil && out.IntentID == "" {
		err = c.NewFailure("CONFIRMATION_RESULT_MISSING", "CONFIRMATION", stage, "The exact confirmation result is unavailable.", c.ReadOriginalReceipt, nil)
	}
	result := c.Result{Failure: controlapi.ConfirmationFailure(err, stage)}
	if out.IntentID != "" && (result.Failure == nil || result.Failure.NextAction != c.AccessBlocked) {
		result.Intent = &out
	}
	return &sdkmcp.CallToolResult{IsError: err != nil}, result, nil
}

// normalizePublicConfirmationFailure is deliberately a read-side adapter.  A
// historical receipt may contain an empty internal message; changing it would
// change the immutable receipt bytes.  The public MCP contract, however,
// requires a non-empty message whenever a failure is present.
func normalizePublicConfirmationFailure(in *c.Failure) *c.Failure {
	if in == nil {
		return nil
	}
	out := *in
	c.NormalizePublicFailure(&out)
	out.Code = boundedPublicConfirmationText(out.Code, 128)
	out.ReasonCode = boundedPublicConfirmationText(out.ReasonCode, 128)
	out.Message = strings.ToValidUTF8(out.Message, "")
	if strings.TrimSpace(out.Message) == "" {
		out.Message = "The confirmation command did not complete successfully."
	}
	out.Message = boundedPublicConfirmationText(out.Message, 1024)
	return &out
}

func boundedPublicConfirmationText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// normalizePublicConfirmationResult copies the public value before fixing
// display-only failure fields.  It never mutates an intent or durable receipt.
func normalizePublicConfirmationResult(in c.Result) c.Result {
	out := in
	out.Failure = normalizePublicConfirmationFailure(in.Failure)
	if in.Intent != nil {
		intent := *in.Intent
		if in.Intent.Submission != nil {
			submission := *in.Intent.Submission
			submission.Failure = normalizePublicConfirmationFailure(in.Intent.Submission.Failure)
			intent.Submission = &submission
		}
		// Copy without folding an empty slice back to nil: append([]Step(nil))
		// with zero elements yields nil and serializes steps as JSON null, which
		// the strict Card entry validator rejects as INTENT_FACTS_UNAVAILABLE.
		intent.Steps = make([]c.Step, len(in.Intent.Steps))
		copy(intent.Steps, in.Intent.Steps)
		for i := range intent.Steps {
			intent.Steps[i].Failure = normalizePublicConfirmationFailure(in.Intent.Steps[i].Failure)
		}
		// Materialize Card-required empty collections on the detached public copy
		// so a zero-step intent projects as steps:[] / variants:[], never null.
		c.CanonicalizePublicCollections(&intent)
		out.Intent = &intent
	}
	if in.Options != nil {
		options := *in.Options
		options.Projects = normalizeConfirmationChoices(in.Options.Projects)
		options.Repositories = normalizeConfirmationChoices(in.Options.Repositories)
		options.Branches = normalizeConfirmationChoices(in.Options.Branches)
		options.Variants = normalizeConfirmationChoices(in.Options.Variants)
		// Same non-folding discipline as intent steps: an empty candidate set must
		// project as [], never null, because the Card validateOptions gate requires
		// Array.isArray on developmentContexts, its row.branches and runtimeOptions.
		options.DevelopmentContexts = make([]c.DevelopmentContextOption, len(in.Options.DevelopmentContexts))
		copy(options.DevelopmentContexts, in.Options.DevelopmentContexts)
		for i := range options.DevelopmentContexts {
			options.DevelopmentContexts[i].Branches = normalizeConfirmationChoices(in.Options.DevelopmentContexts[i].Branches)
		}
		if options.RuntimeOptions == nil {
			options.RuntimeOptions = []codexruntime.RuntimeOption{}
		}
		out.Options = &options
	}
	// The confirmation package owns compatibility derivation for durable
	// submission facts (business outcome and consumption disposition). Every
	// pointer above is detached, so this remains read-only with respect to the
	// persisted intent and receipt.
	c.NormalizePublicResult(&out)
	return out
}

func normalizeConfirmationChoices(in []c.Choice) []c.Choice {
	// make+copy preserves a legal empty candidate set as [] instead of folding it
	// to nil (which marshals to JSON null and the strict Card rejects as an
	// unavailable/invalid options collection).
	out := make([]c.Choice, len(in))
	copy(out, in)
	for i := range out {
		out[i].Failures = make([]c.Failure, len(in[i].Failures))
		copy(out[i].Failures, in[i].Failures)
		for j := range out[i].Failures {
			out[i].Failures[j] = *normalizePublicConfirmationFailure(&in[i].Failures[j])
		}
	}
	return out
}

// validatePublicConfirmationResult uses the exact schema advertised through
// the MCP SDK.  Keep this immediately before handler return so a malformed
// projection cannot become a host-level output-validation error.
func validatePublicConfirmationResult(in c.Result) (c.Result, error) {
	out := normalizePublicConfirmationResult(in)
	raw, err := json.Marshal(out)
	if err != nil {
		return c.Result{}, err
	}
	schemaRaw, err := json.Marshal(confirmationResultSchema())
	if err != nil {
		return c.Result{}, err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return c.Result{}, err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return c.Result{}, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return c.Result{}, err
	}
	if err := resolved.Validate(value); err != nil {
		return c.Result{}, err
	}
	return out, nil
}

// publicConfirmationResultOrFallback projects a confirmation result and NEVER
// panics. It has two safe layers plus a protocol-level last resort:
//
//	Layer 0 — the primary projection satisfies the advertised OutputSchema: return
//	          it unchanged.
//	Layer 1 — the primary projection is malformed: replace it with a fixed,
//	          self-normalizing failure-only envelope (CONFIRMATION_PUBLIC_PROJECTION_
//	          INVALID) that carries no business intent/options and mounts no Card.
//	          The durable receipt is untouched.
//	Layer 2 — even the fixed fallback cannot satisfy the schema (an internal defect):
//	          do NOT panic and do NOT emit a malformed envelope. Return a non-nil
//	          error so the MCP SDK reports an IsError tool result with no structured
//	          content, no ui.resourceUri and no outputTemplate; the connection and
//	          later tool calls stay healthy. The business outcome is never rewritten.
//
// The bool reports "projection was not the caller's original value"; the error is
// non-nil only in Layer 2.
func publicConfirmationResultOrFallback(in c.Result) (c.Result, bool, error) {
	if out, err := validatePublicConfirmationResult(in); err == nil {
		return out, false, nil
	}
	fallback := c.Result{Failure: c.NewFailure("CONFIRMATION_PUBLIC_PROJECTION_INVALID", "CONFIRMATION", "RECEIPT_READBACK", "The confirmation result could not be projected safely.", c.ReadOriginalReceipt, nil)}
	if out, err := validatePublicConfirmationResult(fallback); err == nil {
		return out, true, nil
	}
	// Layer 2: the fallback itself would violate the advertised schema. Surface a
	// protocol-supported tool error instead of panicking or emitting bad data.
	return c.Result{}, true, errors.New("confirmation public projection is unavailable")
}

// Admission can fail before the typed SDK handler. Preserve the same closed
// failure-only contract, explicitly validating it instead of bypassing schema.
func confirmationAdmissionFailure(err error) (*sdkmcp.CallToolResult, error) {
	failure := controlapi.ConfirmationFailure(err, "INPUT_IDENTITY")
	if failure.NextAction != c.AccessBlocked {
		failure = c.NewFailure("CONFIRMATION_CALLER_CONTEXT_REQUIRED", "CONFIRMATION", "INPUT_IDENTITY", "Trusted caller context is required.", c.AccessBlocked, err)
	}
	result, out, _ := confirmationResponse(c.Intent{}, failure, "INPUT_IDENTITY")
	out, projectionInvalid, projectionErr := publicConfirmationResultOrFallback(out)
	if projectionErr != nil {
		// Layer 2: even the fixed admission failure could not be projected. Do not
		// panic or emit a malformed envelope; the SDK turns this into an IsError
		// tool result with no structured content, Card resource or outputTemplate.
		return nil, projectionErr
	}
	if projectionInvalid {
		result.IsError = true
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return nil, errors.New("confirmation error result unavailable")
	}
	schemaRaw, e := json.Marshal(confirmationResultSchema())
	if e != nil {
		return nil, errors.New("confirmation error schema unavailable")
	}
	var schema jsonschema.Schema
	if e = json.Unmarshal(schemaRaw, &schema); e != nil {
		return nil, errors.New("confirmation error schema unavailable")
	}
	resolved, e := schema.Resolve(nil)
	if e != nil {
		return nil, errors.New("confirmation error schema unavailable")
	}
	var value any
	if e = json.Unmarshal(raw, &value); e != nil {
		return nil, errors.New("confirmation error result unavailable")
	}
	if e = resolved.Validate(value); e != nil {
		return nil, errors.New("confirmation error result unavailable")
	}
	result.StructuredContent = json.RawMessage(raw)
	result.Content = []sdkmcp.Content{&sdkmcp.TextContent{Text: string(raw)}}
	return result, nil
}
func confirmationArgumentHash(call *sdkmcp.CallToolRequest) (string, error) {
	if call == nil || call.Params == nil {
		return "", errors.New("confirmation arguments unavailable")
	}
	return c.InputHash(call.Params.Arguments)
}

// RegisterConfirmationTools adds the single ChatGPT prepare/show/read/confirm
// catalog. Typed handlers return their own intent envelope without workspace
// enrichment. Local retains its existing canonical commands and is not filtered.
func RegisterConfirmationTools(server *sdkmcp.Server, service BridgeService) {
	provider, ok := service.(confirmationService)
	confirmationInstances := newConfirmationInstanceRegistry()
	unavailable := func() error {
		if !ok {
			return errors.New("confirmation service unavailable")
		}
		return nil
	}
	decorate := func(ctx context.Context, result c.Result) c.Result {
		if result.Intent != nil {
			if reader, ok := service.(interface {
				ConfirmationDisplay(context.Context, c.Intent) *c.DisplayMetadata
			}); ok {
				result.Display = reader.ConfirmationDisplay(ctx, *result.Intent)
			}
		}
		return result
	}
	respond := func(ctx context.Context, out c.Intent, err error, stage string) (*sdkmcp.CallToolResult, c.Result, error) {
		result, value, e := confirmationResponse(out, err, stage)
		value, projectionInvalid, projectionErr := publicConfirmationResultOrFallback(decorate(ctx, value))
		if projectionErr != nil {
			return nil, c.Result{}, projectionErr
		}
		if projectionInvalid {
			result.IsError = true
		}
		return result, value, e
	}
	// respondPrepare adds the prepare-only contract facts Core owns:
	// businessOutcome, factsComplete and presentationReady. A prepare succeeds
	// only with a complete intent; presentationReady reflects whether the exact
	// confirmation_show envelope for that intent satisfies the advertised MCP
	// OutputSchema (the Host mount gate re-checks the Card entry schema at
	// mount). A complete-but-unpresentable intent reports PREPARED with
	// presentationReady=false, which the spec reserves for "created, presentation
	// temporarily unavailable" and never for a half-written intent.
	respondPrepare := func(ctx context.Context, out c.Intent, err error) (*sdkmcp.CallToolResult, c.Result, error) {
		result, value, e := confirmationResponse(out, err, "PREPARE")
		value, projectionInvalid, projectionErr := publicConfirmationResultOrFallback(decorate(ctx, value))
		if projectionErr != nil {
			return nil, c.Result{}, projectionErr
		}
		if projectionInvalid {
			result.IsError = true
		}
		truth := func(b bool) *bool { return &b }
		if value.Intent != nil && err == nil && value.Failure == nil {
			complete, _, _ := c.IntentFactsComplete(value.Intent)
			// presentationReady is the prepare-time mirror of the confirmation_show
			// Host mount gate: the exact show envelope must satisfy the advertised
			// OutputSchema AND carry a Card-bindable absolute entry projection. A
			// complete intent that would fail the mount gate is reported PREPARED
			// with presentationReady=false, never as a half-written intent.
			show := c.Result{Intent: value.Intent, Display: value.Display}
			_, showInvalid, showErr := publicConfirmationResultOrFallback(show)
			showInvalid = showInvalid || (showErr != nil)
			if !showInvalid && ValidateConfirmationCardEntryProjection(show) != nil {
				showInvalid = true
			}
			value.BusinessOutcome = c.Prepared
			value.FactsComplete = truth(complete)
			value.PresentationReady = truth(complete && !showInvalid)
			if !complete || showInvalid {
				result.IsError = true
			}
		} else {
			outcome := c.RejectedNotPrepared
			if errors.Is(err, c.ErrRequestConflict) {
				outcome = c.PrepareConflict
			}
			value.BusinessOutcome = outcome
			value.FactsComplete = truth(false)
			value.PresentationReady = truth(false)
		}
		value, finalInvalid, finalErr := publicConfirmationResultOrFallback(value)
		if finalErr != nil {
			return nil, c.Result{}, finalErr
		}
		if finalInvalid {
			result.IsError = true
		}
		return result, value, e
	}

	if resolve, supported := service.(interface {
		ResolveTaskActionConflict(context.Context, taskaction.ConflictSelection) (c.Intent, error)
	}); supported {
		schema := confirmationObject(map[string]any{"aiSessionId": confirmationString(1, 128), "taskId": confirmationString(1, 128), "generation": map[string]any{"type": "integer", "minimum": 1}, "intentId": confirmationString(1, 128), "version": map[string]any{"type": "integer", "minimum": 1}, "purpose": confirmationEnum("PROJECT_SETUP", "FEATURE_AREA", "TASK", "REVIEW", "EXECUTION", "RECOVERY_PURPOSE"), "contentHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "aiSessionId", "taskId", "generation", "intentId", "version", "purpose", "contentHash")
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "task_action_conflict_resolve", Title: "Keep one original Task action", Description: "App-only explicit user choice. Keep the exact selected existing proposal and retire only its unconsumed siblings atomically. Never discard another consumed or unknown effect, confirm a proposal or start work. The expected Task generation and exact original confirmation identity are mandatory.", InputSchema: schema, OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Resolve Task proposal conflict", true), Meta: confirmationToolMeta(true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in taskaction.ConflictSelection) (*sdkmcp.CallToolResult, c.Result, error) {
			out, err := resolve.ResolveTaskActionConflict(ctx, in)
			return respond(ctx, out, err, "UNIQUE_CONSUMPTION")
		})
	}
	if discard, supported := service.(interface {
		DiscardConfirmationProposal(context.Context, c.ReadInput) (c.Intent, error)
	}); supported {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_discard", Title: "Discard unconsumed proposal", Description: "App-only explicit user action. Atomically retire this exact unconsumed proposal and fence late submissions. A consumed proposal is only read back; no business result is rolled back.", InputSchema: confirmationReadSchema(false), OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Discard unconsumed proposal", true), Meta: confirmationToolMeta(true)}, func(ctx context.Context, call *sdkmcp.CallToolRequest, in c.ReadInput) (*sdkmcp.CallToolResult, c.Result, error) {
			out, err := discard.DiscardConfirmationProposal(ctx, in)
			return respond(ctx, out, err, "UNIQUE_CONSUMPTION")
		})
	}
	if withdraw, supported := service.(interface {
		WithdrawConfirmationSubmission(context.Context, c.CommitInput) (c.Intent, error)
	}); supported {
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_submission_withdraw", Title: "Withdraw an uncertain confirmation submission", Description: "App-only explicit user action. Accept the complete unchanged original submission. Atomically record a no-consumption rejection if that request has not been consumed, fencing late copies; otherwise return its existing receipt without dispatch, continuation or rollback. Never treat a missing read receipt as proof and never confirm a replacement automatically.", InputSchema: confirmationCommitSchema(), OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Withdraw uncertain submission", true), Meta: confirmationToolMeta(true)}, func(ctx context.Context, call *sdkmcp.CallToolRequest, in c.CommitInput) (*sdkmcp.CallToolResult, c.Result, error) {
			hash, err := confirmationArgumentHash(call)
			if err != nil {
				return respond(ctx, c.Intent{}, err, "INPUT_IDENTITY")
			}
			in.SubmittedHash = hash
			out, err := withdraw.WithdrawConfirmationSubmission(ctx, in)
			return respond(ctx, out, err, "SUBMISSION_WITHDRAWAL")
		})
	}
	for _, entry := range []struct {
		name        string
		purpose     c.Purpose
		description string
	}{
		{"project_setup_prepare", c.ProjectSetup, "Prepare SELECT_PROJECT to bind the existing Session to a Project, or CONFIGURE_DEVELOPMENT_CONTEXT to configure an explicit set of repository development branch mappings in its bound Project. Reads never bind, select a branch or create a worktree. SELECT_PROJECT_BRANCH is retained only for legacy exact interpretation."},
		{"feature_area_prepare", c.FeatureArea, "Prepare a new FeatureArea, optionally its explicit initial Task and one complete Review. CREATE_AREA creates no Review and takes no runtime selection; CREATE_AREA_AND_REVIEW includes full initial Task, depth, plan and applicable Review instruction variants. MERGE_FEATURE_AREA freezes a merge plan for explicit confirmation. After releasing a Task completion plan, pass featureArea.completionTaskId for that exact original Task; Core derives its accepted commits and current targets without repeating acceptance."},
		{"task_prepare", c.Task, "Prepare an uncreated Task in the exact existing FeatureArea. Read the current Tasks first when the user intends to continue existing work. The Card projects unfinished Tasks in that exact FeatureArea and asks the user to continue one or explicitly create an independent Task; choosing continuation only requests current-state guidance and never consumes this creation intent. A new Task never completes or closes its predecessor. CREATE_TASK only creates it after confirmation and cannot enable an unprepared Review. CREATE_TASK_AND_REVIEW must prepare both UPPER_LLM and CODING_AGENT variants with complete executor-specific Review instructions, depth and plan so the USER chooses the method on the Card. The Card also offers NONE: explicitly skip Review, create a Task awaiting implementation-definition confirmation, and never dispatch work from creation. After NONE, use execution_prepare action CONFIRM_AND_START_IMPLEMENTATION with complete single-repository Coding workSpec/verificationSpec and the exact Task revision; if the Task has no Repository yet, select execution.repositoryId and its immutable full baseCommit in this implementation proposal. Its one user confirmation freezes the plan and explicitly selected backend/runtime, then starts implementation; do not request another START_APPROVED confirmation. Legacy false startReview without reviewMode means defer, not skip. Supply an explicit target repository when preparing the three-way review choice."},
		{"review_prepare", c.Review, "Prepare a first, revised or supplemental Review of this exact Task, or the exact pending Review REVISE/REJECT decision. For an existing Task, read requiredReviewDepth/allowedReviewDepths from workspace_get (use the REQUEST_TASK_REVIEW capability for a supplemental request, since approval can clear the Task-level floor) and choose an allowed depth; a narrower supplemental scope never lowers the depth floor. If confirmation_get returns reviewStartPreparation for a previously PARTIAL supplemental confirmation, preserve that consumed intent and prepare a NEW START_REVIEW on its exact current Task with the returned expectedTarget and an allowed depth; do not repeat REQUEST_SUPPLEMENTAL_REVIEW, replace its consumed intent or start directly. Supplemental action preserves the canonical five-field expectedTarget and performs task_review_request then task_review_start under one confirmation. A REVISE/REJECT decision carries only exact Task/FeatureArea/Review/Decision identity and one neutral decision variant: it never invents a Review plan, instruction, model or runtime configuration. Prepare itself never changes phase or starts work."},
		{"execution_prepare", c.Execution, "Prepare the exact approval/start/retry/changed-implementation operation. When the existing Task panel is open, its scrollable operation list reads the prepared intent automatically: keep that panel and avoid another confirmation_show widget. For clients without that panel retain confirmation_show. The model never commits for the user. At a verification checkpoint expose CONTINUE_TASK, COMPLETE_TASK and DEFER_TASK as the three primary choices. COMPLETE_TASK confirms acceptance, commit and integration into the Core-frozen development targets in one user confirmation; the Task succeeds only after all merges complete. DEFER_TASK requires execution.note with progress, results and remaining work; it saves a checkpoint and never runs git stash. CONTINUE_TASK retains the original Task and records continuation without starting a Run; then read current same-Task review/reverification qualification. These three actions use one decision-only variant and exact Task/FeatureArea/Execution/revision/Decision identity. ACCEPT is legacy acceptance-only compatibility, not the primary finish action. CONFIRM_AND_START_IMPLEMENTATION is the single plan-and-start user confirmation for an explicitly Review-skipped Task: pass exact Task/revision/FeatureArea, empty Review/Decision/Execution identities and complete single-repository CODING_AGENT workSpec/verificationSpec; for a Task without a Repository, also provide execution.repositoryId and full immutable baseCommit. The confirmation displays the baseline and full plan, requires explicit backend/runtime selection, freezes the definition and starts implementation in the same confirmed operation. CONFIRM_IMPLEMENTATION remains legacy definition-only compatibility and never starts a Run; an already frozen legacy plan can still use START_APPROVED. For other actions, or the exact pending Task ACCEPT/REJECT/REQUEST_REVERIFICATION decision. New implementation actions include complete applicable implementation and verification variants. A decision action carries only exact Task/FeatureArea/Execution/revision/Decision identity and one neutral decision variant: it never invents WorkSpec, VerificationSpec, instructions, backend, model, effort or Fast Mode. The Card confirms one explicit Codex backend and one runtime configuration shared by Implementation and Verification when a new Coding Execution needs them. DIRECT_FILE keeps GPT file implementation and runs all actual verification in Codex: START_DIRECT_FILE requires an explicit verification backend/model for RUN, frozen before file work; result submission starts a separate read-only verification Run. Verification proposals must include exact executable checks and repository roots, preserving requiredness. An already approved immutable candidate retains its exact frozen backend and runtime configuration; new content/configuration requires its original successor protocol."},
	} {
		description := entry.description + confirmationTaskActionGenerationGuidance
		if entry.purpose == c.FeatureArea || entry.purpose == c.Task || entry.purpose == c.Review {
			description += confirmationReviewPlanGuidance
		}
		if entry.purpose == c.Execution {
			description += confirmationExecutionGuidance
			description += " Preserve all reviewed required checks, including CUSTOM instructions. CUSTOM instructions are required indexed GPT analysis obligations, not Bridge-observed proof: report actual Codex findings, evidence and any BLOCKED/NOT_RUN/UNKNOWN outcome without inventing commands or pass verdicts. CUSTOM commands and typed command/Git checks retain exact observable execution requirements. Never silently make checks optional, add commands, or infer a USER_WAIVED waiver."
		}
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: entry.name, Title: "Prepare " + string(entry.purpose) + " confirmation", Description: description + " Persist only an immutable pending confirmation intent; no formal business object, writer claim, workspace preparation, file grant or dispatch. Return the closed result envelope with intent.intentId/version/contentHash; typed failure is independent of intent state. Let an existing unified Task panel display this intent; use confirmation_show when no such panel is open. sourceIntentId replaces only a still-pending prior intent; never overwrite confirmed content. Until confirmation_commit succeeds, report only that this confirmation intent is prepared (optionally this intentId, version, repository count, topology revision and PENDING state); never state that the Task or Review was recreated, that a successor Review exists, that re-review started, or that the Card was shown. Only a committed intent with durable successor identities may be described as a created successor Review. Recommended runtime selection is not authorization. For new variants, optionally provide displaySummary.summary (1–160 Unicode characters, one or two sentences about this engineering change only) and displaySummary.keyConstraints (at most 3 explicit decision-relevant boundaries, each 1–60 Unicode characters). Keep complete canonical instructions/specs and the full summary separately; never shorten engineering content to this display text. Use a neutral label naming the difference between proposals rather than repeating executor prefixes. Do not include historical repair ledgers, IDs, hashes or routing explanations in the short display text. Missing displaySummary is valid and never an approval prerequisite. Live display names are separate read metadata, not frozen content or authorization.", InputSchema: confirmationPrepareSchema(entry.purpose), OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Prepare pending confirmation", true), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}}}, func(ctx context.Context, call *sdkmcp.CallToolRequest, in c.PrepareInput) (*sdkmcp.CallToolResult, c.Result, error) {
			if err := unavailable(); err != nil {
				return respondPrepare(ctx, c.Intent{}, err)
			}
			if in.Purpose != entry.purpose {
				return respondPrepare(ctx, c.Intent{}, c.NewFailure("CONFIRMATION_PURPOSE_MISMATCH", "CONFIRMATION", "INPUT_IDENTITY", "The purpose does not match this prepare tool.", c.PrepareNewVersion, nil))
			}
			hash, err := confirmationArgumentHash(call)
			if err != nil {
				return respondPrepare(ctx, c.Intent{}, err)
			}
			in.SubmittedHash = hash
			out, err := provider.PrepareConfirmation(ctx, in)
			return respondPrepare(ctx, out, err)
		})
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_get", Title: "Read exact confirmation", Description: "Pure read of the exact original intent and recorded canonical subcommand receipts/results. The read identity is aiSessionId + intentId + version + purpose + contentHash together: contentHash is required frozen content identity, not an optional filter, and intentId/version alone never address a read. A digest that differs from the durable intent is refused with CONFIRMATION_READ_IDENTITY_MISMATCH as a pure read miss that changes nothing. No prepare, confirm, replay dispatch or repair. Use the intent already returned in this conversation, never guess the latest Task. Optional requestId reads only that exact submission outcome, including recorded pre-consumption rejection. To read a replacement receipt, use replacementRequestId instead with the original source identity; absent or mismatched replacement receipts return a typed read error without changing intent state. NOT_OBSERVED/PENDING does not prove non-consumption. UNKNOWN/PARTIAL is not full failure or success.", InputSchema: confirmationReadSchema(false), OutputSchema: confirmationResultSchema(), Annotations: toolAnnotations("Read confirmation", true, true), Meta: confirmationToolMeta(false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in c.ReadInput) (*sdkmcp.CallToolResult, c.Result, error) {
		if err := unavailable(); err != nil {
			return respond(ctx, c.Intent{}, err, "INPUT_IDENTITY")
		}
		out, err := provider.ReadConfirmation(ctx, in)
		return respond(ctx, out, err, "RECEIPT_READBACK")
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_options_get", Title: "Read confirmation choices", Description: "Pure current options for this exact intent/purpose/version and its required frozen contentHash; the same five-field read identity as confirmation_get, and a hash miss is refused the same way without changing anything. Runtime candidates are per-model Core capabilities, not defaults. Project candidates for an unbound Session are authorized without project_open. For TASK, optional startReview reads capabilities for that exact choice (omission keeps the prepared action); false qualifies only Task creation. Selecting inputs here never binds, selects a branch, prepares a workspace or dispatches.", InputSchema: confirmationReadSchema(true), OutputSchema: confirmationResultSchema(), Annotations: toolAnnotations("Read confirmation options", true, true), Meta: confirmationToolMeta(false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in c.OptionsInput) (*sdkmcp.CallToolResult, c.Result, error) {
		if err := unavailable(); err != nil {
			return respond(ctx, c.Intent{}, err, "INPUT_IDENTITY")
		}
		out, err := provider.ConfirmationOptions(ctx, in)
		if err == nil && out.IntentID == "" {
			err = c.NewFailure("CONFIRMATION_OPTIONS_MISSING", "CONFIRMATION", "CONFIRMATION_PREFLIGHT", "The exact confirmation options are unavailable.", c.ReadOriginalReceipt, nil)
		}
		result := c.Result{Failure: controlapi.ConfirmationFailure(err, "CONFIRMATION_PREFLIGHT")}
		if out.IntentID != "" && (result.Failure == nil || result.Failure.NextAction != c.AccessBlocked) {
			result.Options = &out
		}
		result, projectionInvalid, projectionErr := publicConfirmationResultOrFallback(result)
		if projectionErr != nil {
			return nil, c.Result{}, projectionErr
		}
		// §6 options mount parity: an options envelope the Card's validateOptions
		// would reject must not be served as a valid candidate set. Only re-check a
		// result the canonicalizer/Schema already accepted, and replace a bad
		// options payload with the fixed typed failure (never mask missing authority
		// behind empty arrays).
		if !projectionInvalid && result.Options != nil {
			if optionsFailure := ValidateConfirmationOptionsProjection(result); optionsFailure != nil {
				rejected, _, rejectedErr := publicConfirmationResultOrFallback(c.Result{Failure: optionsFailure})
				if rejectedErr != nil {
					return nil, c.Result{}, rejectedErr
				}
				return &sdkmcp.CallToolResult{IsError: true}, rejected, nil
			}
		}
		return &sdkmcp.CallToolResult{IsError: err != nil || projectionInvalid}, result, nil
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_commit", Title: "Confirm this prepared operation", Description: "Public submission entry for one explicit user confirmation decision, shared with the Card: this ChatGPT surface has no other way to consume a prepared intent. Call it only for the exact intent/version/contentHash/action the user just confirmed, with a stored intent, an explicit action, a prepared variant and structured selections. For TASK creation, selection.startReview=false creates only the Task; true requires the already-prepared Review. Omission preserves the prepared action, and the action/contentHash never change with this selection. A new Coding operation submits the Core-issued backend configId/configFingerprint together with model, effort and Fast Mode; display availability is never authorization. No arbitrary tool name, replacement prompt or approved=true. The closed DTO accepts no outcome, consumed flag, Decision status or version, successor identity, retryDisposition, reprepareAllowed, force, assumeNotConsumed or retryAnyway; any of those is a Core-derived read fact, never an input. One durable consumption fixes selections and stable canonical subrequests. Return intent/submission and safe typed failure in the closed result envelope even when isError is true. Never call it on the user's behalf from model orchestration, never to bypass a Card, and never to continue work the user did not confirm. Only a recorded REJECTED_NOT_CONSUMED whose submission retryDisposition is EDIT_SELECTION_AND_RESUBMIT, on a current PENDING intent with an unconsumed exact request, permits one new user-selected attempt with a fresh requestId; a REPREPARE_REQUIRED rejection is terminal for that intent and goes through confirmation_reprepare instead. Unknown results require exact requestId readback, not fresh request IDs. A Host transport error is an UNKNOWN outcome: read the original requestId receipt before anything else. Visibility is not cryptographic proof of a user click; original caller/Session/target/receipt checks remain authoritative.", InputSchema: confirmationCommitSchema(), OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Confirm prepared operation", true), Meta: confirmationToolMeta(false)}, func(ctx context.Context, call *sdkmcp.CallToolRequest, in c.CommitInput) (*sdkmcp.CallToolResult, c.Result, error) {
		if err := unavailable(); err != nil {
			// §13: a missing public commit entry is its own typed condition. It is
			// never reported as an unknown submission outcome.
			return respond(ctx, c.Intent{}, c.NewFailure("CONFIRMATION_COMMIT_TOOL_UNAVAILABLE", "CONFIRMATION", "INPUT_IDENTITY", "This surface exposes no confirmation_commit entry, so no submission was made and no outcome is known.", c.ReadCurrentTarget, err), "INPUT_IDENTITY")
		}
		hash, err := confirmationArgumentHash(call)
		if err != nil {
			return respond(ctx, c.Intent{}, err, "INPUT_IDENTITY")
		}
		in.SubmittedHash = hash
		out, err := provider.CommitConfirmation(ctx, in)
		return respond(ctx, out, err, "UNIQUE_CONSUMPTION")
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_reprepare", Title: "Prepare the replacement confirmation", Description: "Prepare a replacement for an exact Review Decision or Recovery purpose confirmation. Core requires either a stale unsubmitted source or a recorded no-effect rejection with replacement authority, and revalidates current source facts in the writer transaction. Supply exact source identity, a fresh requestId, and optional Review feedback or Recovery purpose candidate; submitted candidates never grant owner authority. Recovery source drift requires a new Recovery status assessment instead of same-generation replacement. The new intent has version 1 and sourceIntentId lineage; source supersession, Recovery link history/current selection, and the immutable replacement receipt commit together. No business successor, Run or start authority is created. Exact request replay returns the same replacement even after current state advances; changed payload or a competing successor is refused. Read confirmation_get with the original source identity and replacementRequestId to recover the replacement receipt.", InputSchema: confirmationReprepareSchema(), OutputSchema: confirmationResultSchema(), Annotations: stateChangingAnnotations("Prepare replacement confirmation", true), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}}}, func(ctx context.Context, call *sdkmcp.CallToolRequest, in c.ReprepareInput) (*sdkmcp.CallToolResult, c.Result, error) {
		if err := unavailable(); err != nil {
			return respondPrepare(ctx, c.Intent{}, err)
		}
		hash, err := confirmationArgumentHash(call)
		if err != nil {
			return respondPrepare(ctx, c.Intent{}, err)
		}
		in.SubmittedHash = hash
		out, err := provider.ReprepareConfirmation(ctx, in)
		return respondPrepare(ctx, out, err)
	})
	meta := primaryCardMeta(confirmationResourceURI)
	meta["devbridge/componentVersion"] = confirmationComponentVersion
	meta["devbridge/mainScriptSHA256"] = confirmationScriptSHA256()
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "confirmation_show", Title: "Show prepared confirmation", Description: "Render only this existing exact purpose-scoped confirmation intent. Reuse its immutable intentId/version/contentHash identity when reading; showing or refreshing is a pure read, never prepare, binding, authorization or dispatch. Each intent is its own Card instance: devbridge/presentationInstanceId is derived from aiSessionId, intentId, version and contentHash only, so a refreshed or replacement intent never rebinds a previously shown Card or inherits its business state. Without an explicit Host display acknowledgement, return this tool result only and do not state that the Card was shown. No UI host means pending confirmation, not automatic direct-start fallback. Subsequent phases must not replace this Card's operation identity.", InputSchema: confirmationReadSchema(false), OutputSchema: confirmationResultSchema(), Annotations: toolAnnotations("Show prepared confirmation", true, true), Meta: meta}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in c.ReadInput) (*sdkmcp.CallToolResult, c.Result, error) {
		if err := unavailable(); err != nil {
			return respond(ctx, c.Intent{}, err, "INPUT_IDENTITY")
		}
		out, err := provider.ReadConfirmation(ctx, in)
		if err != nil || out.IntentID == "" {
			return respond(ctx, out, err, "RECEIPT_READBACK")
		}
		result, projectionInvalid, projectionErr := publicConfirmationResultOrFallback(decorate(ctx, c.Result{Intent: &out}))
		// Host mount gate. Only after the final envelope is canonicalized and
		// SDK-validated do we validate the ACTUAL Card entry; a projection the
		// SDK rejected, or an entry the Card itself would reject, must not attach
		// a Card resource and must not display PENDING.
		if projectionErr != nil {
			return nil, c.Result{}, projectionErr
		}
		if projectionInvalid {
			return &sdkmcp.CallToolResult{IsError: true}, result, nil
		}
		if entryFailure := ValidateConfirmationCardEntryProjection(result); entryFailure != nil {
			rejected, _, rejectedErr := publicConfirmationResultOrFallback(c.Result{Failure: entryFailure})
			if rejectedErr != nil {
				return nil, c.Result{}, rejectedErr
			}
			return &sdkmcp.CallToolResult{IsError: true}, rejected, nil
		}
		// §8 Card-instance identity gate, the last check before the Card resource/meta
		// is attached: the entry must carry a complete frozen identity that matches
		// the exact intent this show was asked to render, and the same presentation
		// handle must not be reused for a different intent. On failure the Card entry
		// is NOT attached, PENDING is NOT displayed, and no shown claim is made.
		if instanceFailure := ValidateConfirmationCardInstanceIdentity(result, in); instanceFailure != nil {
			rejected, _, rejectedErr := publicConfirmationResultOrFallback(c.Result{Failure: instanceFailure})
			if rejectedErr != nil {
				return nil, c.Result{}, rejectedErr
			}
			return &sdkmcp.CallToolResult{IsError: true}, rejected, nil
		}
		resultMeta := primaryCardMeta(confirmationResourceURI)
		resultMeta["emission"] = "CONFIRMATION"
		resultMeta["devbridge/componentVersion"] = confirmationComponentVersion
		resultMeta["devbridge/mainScriptSHA256"] = confirmationScriptSHA256()
		if result.Intent != nil {
			// A presentation handle, when the caller supplies one, must not be reused
			// for a different intent (fail closed). The identity meta is emitted
			// regardless so the Host opens this intent's own Card instance.
			if strings.TrimSpace(in.RequestID) != "" {
				if reuseCode := confirmationInstances.checkOrBind(in.RequestID, *result.Intent); reuseCode != "" {
					rejected, _, rejectedErr := publicConfirmationResultOrFallback(c.Result{Failure: confirmationInstanceFailure(reuseCode)})
					if rejectedErr != nil {
						return nil, c.Result{}, rejectedErr
					}
					return &sdkmcp.CallToolResult{IsError: true}, rejected, nil
				}
			}
			decorateConfirmationInstanceMeta(resultMeta, *result.Intent)
		}
		return &sdkmcp.CallToolResult{Meta: resultMeta}, result, nil
	})
	resourceMeta := sdkmcp.Meta{"devbridge/componentVersion": confirmationComponentVersion, "devbridge/mainScriptSHA256": confirmationScriptSHA256(), "ui": map[string]any{"csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{}}, "prefersBorder": false}}
	for _, uri := range []string{confirmationResourceURI, "ui://devbridge/confirmation/v13", "ui://devbridge/confirmation/v12", "ui://devbridge/confirmation/v11", "ui://devbridge/confirmation/v10", "ui://devbridge/confirmation/v9", "ui://devbridge/confirmation/v8", "ui://devbridge/confirmation/v7", "ui://devbridge/confirmation/v6", "ui://devbridge/confirmation/v5"} {
		server.AddResource(&sdkmcp.Resource{URI: uri, Name: "devbridge_confirmation", Title: "Prepared operation confirmation", MIMEType: decisionResourceMIME, Meta: resourceMeta}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: uri, MIMEType: decisionResourceMIME, Text: confirmationHTML, Meta: resourceMeta}}}, nil
		})
	}
}
