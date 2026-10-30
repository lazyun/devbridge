package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/codexconfig"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/observability"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

// Adapter cores: the shared product orchestration behind the model-visible
// tools. Both the GPT adapter and the Local MCP adapter register their own
// schemas and tool descriptions, but call exactly these functions so the
// product contracts (validation, exact prompt passthrough, durable
// transitions) are defined once. This file deliberately contains no
// client-specific behavior: ChatGPT-only presentation (app-private ui_* tools,
// decision cards, continuation) stays in its own registration files.

type (
	ExternalProjectListInput      = projectListInput
	ExternalProjectOpenInput      = externalProjectOpenInput
	ExternalProjectOpenOutput     = externalProjectOpenOutput
	ExternalWorkflowInput         = externalWorkflowInput
	ExternalTopicCreateInput      = externalTopicCreateInput
	ExternalTopicOutput           = externalTopicOutput
	ExternalDocumentCreateInput   = externalDocumentCreateInput
	ExternalDocumentOutput        = externalDocumentOutput
	ExternalRepositorySearchInput = repositorySearchInput
	ExternalReviewInput           = externalReviewInput
	ExternalReviewOutput          = externalReviewOutput
	ExternalReviewGetInput        = externalReviewGetInput
	ExternalTaskCreateInput       = externalTaskCreateInput
	ExternalTaskOutput            = externalTaskOutput
	ExternalScopeExpansionInput   = externalScopeExpansionInput
	ExternalScopeExpansionOutput  = externalScopeExpansionOutput
	ExternalExecutionInput        = externalExecutionInput
	ExternalExecutionOutput       = externalExecutionOutput
	ExternalWorkflowStateOutput   = externalWorkflowStateOutput
	ExternalDecisionOutput        = externalDecisionOutput
	ExternalDecisionOption        = externalDecisionOption
	ExternalProjectSummary        = externalProjectSummary
)

// Exported product schema fragments for adapters that expose equivalent tools
// with their own presentation layer.
const (
	ExternalBridgeDiagnosticsOutputSchema = externalBridgeInfoOutputSchema
	ExternalProjectListInputSchema        = projectListInputSchema
	ExternalProjectListOutputSchema       = projectListOutputSchema
	ExternalProjectOpenInputSchema        = externalProjectOpenSchema
	ExternalProjectOpenOutputSchema       = externalProjectOpenOutputSchema
	ExternalWorkflowGetInputSchema        = externalWorkflowInputSchema
	ExternalWorkflowStateOutputSchema     = externalWorkflowOutputSchema
	ExternalTopicCreateInputSchema        = externalTopicCreateSchema
	ExternalTopicOutputSchema             = externalFeatureAreaOutputSchema
	ExternalDocumentCreateInputSchema     = externalDocumentCreateSchema
	ExternalDocumentOutputSchema          = externalDocumentOutputSchema
	ExternalRepositorySearchInputSchema   = repositorySearchInputSchema
	ExternalRepositorySearchOutputSchema  = repositorySearchOutputSchema
	ExternalReviewStartInputSchema        = externalReviewStartSchema
	ExternalReviewGetInputSchema          = externalReviewGetSchema
	ExternalTaskCreateInputSchema         = externalTaskCreateSchema
	ExternalTaskOutputSchema              = externalTaskOutputSchema
	ExternalScopeExpansionInputSchema     = externalScopeExpansionSchema
	ExternalScopeExpansionOutputSchema    = externalScopeOutputSchema
	ExternalExecutionGetInputSchema       = externalExecutionSchema
	ExternalExecutionOutputSchema         = externalExecutionOutputSchema
)

var ExternalReviewOutputSchema = externalReviewOutputSchema

// DecisionHandle identifies one durable pending decision for adapters that
// expose a generic structured decision capability. The zero value means no
// decision is pending.
type DecisionHandle struct {
	Kind        string
	SubjectID   string // Source Review ID or Task Acceptance Execution ID
	ExpansionID string // SCOPE_EXPANSION
	ApprovalID  string // RUNTIME_APPROVAL
}

// ID renders the stable decision identifier carried by adapter projections.
func (h DecisionHandle) ID() string {
	switch h.Kind {
	case wp.KindReviewApproval:
		return wp.DecisionID(h.SubjectID)
	case "TASK_ACCEPTANCE":
		return "TASK_ACCEPTANCE:" + h.SubjectID
	case "SCOPE_EXPANSION":
		return "SCOPE_EXPANSION:" + h.ExpansionID
	case "RUNTIME_APPROVAL":
		return "RUNTIME_APPROVAL:" + h.ApprovalID
	default:
		return ""
	}
}

// WorkflowState is the canonical workflow projection plus the raw current
// workflow facts a presentation adapter needs for its own projection.
type WorkflowState struct {
	Output   ExternalWorkflowStateOutput
	Current  controlapi.CurrentWorkflow
	Decision *DecisionHandle
}

// BuildWorkflowState projects the canonical workflow for one session. It is
// the shared read model used by the compact workspace reader.
func BuildWorkflowState(ctx context.Context, service BridgeService, aiSessionID, featureAreaID string) (WorkflowState, error) {
	projection, err := buildWorkflowProjection(ctx, service, aiSessionID, featureAreaID)
	if err != nil {
		return WorkflowState{}, err
	}
	if featureAreaID == "" && projection.output.PendingDecision == nil {
		if recovered, ok := recoverProjectTaskDecision(ctx, service, aiSessionID); ok {
			projection = recovered
		}
	}
	return WorkflowState{Output: projection.output, Current: projection.current, Decision: pendingDecisionHandle(projection)}, nil
}

// SetDecisionInteractionAvailability applies transport capability to the
// shared durable projection. Pending state and reachability are separate.
func SetDecisionInteractionAvailability(output *ExternalWorkflowStateOutput, available bool) {
	if output == nil {
		return
	}
	output.Capabilities.HasPendingDecision = output.PendingDecision != nil
	output.Capabilities.DecisionInteractionAvailable = output.PendingDecision != nil && available
	output.Capabilities.CanResolveDecision = output.Capabilities.DecisionInteractionAvailable
}

// pendingDecisionHandle mirrors pendingDecision's precedence exactly so the
// projected decision and its handle can never disagree.
func pendingDecisionHandle(projection workflowProjection) *DecisionHandle {
	if g := projection.current.Guidance; g != nil && !g.Legacy && projection.current.PendingApproval == nil {
		if g.TaskPhase == task.PhaseReviewApprovalPending {
			return &DecisionHandle{Kind: wp.KindReviewApproval, SubjectID: g.ReviewID}
		}
		if g.NextAction.Code == "RESOLVE_TASK_ACCEPTANCE" {
			return &DecisionHandle{Kind: "TASK_ACCEPTANCE", SubjectID: g.ExecutionID}
		}
		return nil
	}

	if projection.current.PendingApproval != nil {
		return &DecisionHandle{Kind: "RUNTIME_APPROVAL", ApprovalID: projection.current.PendingApproval.ID}
	}
	return nil
}

func BridgeDiagnosticsCore(ctx context.Context, service BridgeService, protocolVersion string) (BridgeInfoOutput, error) {
	info, err := service.BridgeInfo(ctx)
	if err != nil {
		return BridgeInfoOutput{}, publicToolError(err)
	}
	catalogSHA, schemaSHA := UnifiedReviewContractIdentity()
	return BridgeInfoOutput{BusinessCatalogSHA256: catalogSHA, UnifiedReviewSchemaSHA256: schemaSHA, ApplicationName: info.ApplicationName, ApplicationVersion: info.ApplicationVersion, Ready: info.Ready, Protocol: "MCP", ProtocolVersion: protocolVersion, ServerName: ServerName, ServerVersion: ServerVersion, Transport: TransportName}, nil
}

func ProjectListCore(ctx context.Context, service BridgeService, in ExternalProjectListInput) (ProjectListOutput, error) {
	values, err := service.ListProjects(ctx, aicontext.ListOptions{Limit: in.Limit, Offset: in.Offset})
	if err != nil {
		return ProjectListOutput{}, publicToolError(err)
	}
	out := ProjectListOutput{Projects: make([]ProjectListItemOutput, 0, len(values))}
	for _, value := range values {
		out.Projects = append(out.Projects, ProjectListItemOutput{ProjectID: value.ID, Name: boundedUTF8(value.Name, maxNameBytes), ProjectContextStatus: string(value.ProjectContextStatus), RepositoryCount: value.RepositoryCount})
	}
	return out, nil
}

func ProjectOpenCore(ctx context.Context, service BridgeService, in ExternalProjectOpenInput) (ExternalProjectOpenOutput, error) {
	if in.AISessionID == "" {
		return ExternalProjectOpenOutput{}, errors.New("SESSION_INITIALIZATION_REQUIRED: call session_initialize before binding a Project")
	}
	if !validSessionInputID(in.AISessionID) || !validSessionInputID(in.ProjectID) || !validSessionInputID(in.RequestID) {
		return ExternalProjectOpenOutput{}, errors.New("project_open requires valid aiSessionId, projectId and requestId")
	}
	provider, ok := service.(sessionInitializationService)
	if !ok {
		return ExternalProjectOpenOutput{}, errors.New("Session binding unavailable")
	}
	bound, err := provider.BindProject(ctx, sessionReferenceFromContext(ctx), in.AISessionID, in.ProjectID, in.RequestID)
	if err != nil {
		return ExternalProjectOpenOutput{}, publicToolError(err)
	}
	// Binding/receipt and preparation availability are separate facts. Do not
	// run a workflow read after commit or let its failure erase this identity.
	return ExternalProjectOpenOutput{
		Result:   externalProjectBindingResult{ProjectBindingResult: bound, Project: ExternalProjectSummary{ID: bound.BoundProjectID, Name: boundedUTF8(bound.Context.Project.Name, maxNameBytes)}},
		Context:  SessionBindingContext{AISessionID: bound.AISessionID, ProjectID: bound.BoundProjectID},
		Workflow: sessionIdentityWorkflow{State: bound.State}, PresentationHint: surfaceHint(),
	}, nil
}

// recoverProjectTaskDecision locates one unambiguous Project-owned pending
// Task Decision without selecting or mutating FeatureArea/session focus.
func recoverProjectTaskDecision(ctx context.Context, service BridgeService, aiSessionID string) (workflowProjection, bool) {
	plans, err := service.ListTaskPlans(ctx, aiSessionID, controlapi.ListOptions{Limit: 100})
	if err != nil {
		return workflowProjection{}, false
	}
	var pending *taskplan.Plan
	for i := range plans {
		if plans[i].Status != taskplan.StatusPendingConfirmation {
			continue
		}
		if pending != nil {
			return workflowProjection{}, false
		}
		value := plans[i]
		pending = &value
	}
	if pending == nil {
		return workflowProjection{}, false
	}
	owner, err := service.GetTask(ctx, aiSessionID, pending.TaskID)
	if err != nil {
		return workflowProjection{}, false
	}
	projection, err := buildWorkflowProjection(ctx, service, aiSessionID, owner.FeatureAreaID)
	if err != nil || projection.plan == nil || projection.plan.ID != pending.ID {
		return workflowProjection{}, false
	}
	return projection, true
}

// Project opening projects inventory without selecting a FeatureArea. Session
// ownership remains Project-only; no selection state is introduced.
type projectOpenProjectionService struct{ BridgeService }

func (s projectOpenProjectionService) CurrentWorkflow(ctx context.Context, sessionID, _ string) (controlapi.CurrentWorkflow, error) {
	areas, err := s.ListFeatureAreas(ctx, sessionID)
	if err != nil {
		return controlapi.CurrentWorkflow{}, err
	}
	value := controlapi.CurrentWorkflow{AvailableFeatureAreas: areas, Reviews: []productreview.Review{}, Runs: []run.Run{}}
	if provider, ok := s.BridgeService.(interface{ ExecutionAllowlist() codexruntime.Allowlist }); ok {
		allowlist := provider.ExecutionAllowlist()
		value.AvailableModels, value.AvailableReasoningEfforts = allowlist.Models, allowlist.ReasoningEfforts
	} else {
		current, err := s.BridgeService.CurrentWorkflow(ctx, sessionID, "")
		if err != nil {
			return value, err
		}
		value.AvailableModels, value.AvailableReasoningEfforts = current.AvailableModels, current.AvailableReasoningEfforts
	}
	return value, nil
}

func TopicCreateCore(ctx context.Context, service BridgeService, in ExternalTopicCreateInput) (ExternalTopicOutput, error) {
	// SDK schema validation normally rejects these shapes. Core callers (and
	// Local adapters in tests) must retain the same boundary rather than
	// silently discarding nested startup input on a CREATE_ONLY request.
	if (in.Mode == "" || in.Mode == controlapi.CreateOnly) && (in.InitialTask != nil || in.InitialReview != nil) {
		return ExternalTopicOutput{}, publicToolError(&controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "CREATE_ONLY rejects initialTask and initialReview"})
	}
	// The legacy shape deliberately remains the original FeatureArea-only Core:
	// old clients never acquire a request receipt or agent-start side effect.
	if (in.Mode == "" || in.Mode == controlapi.CreateOnly) && in.RequestID == "" {
		value, err := service.CreateFeatureArea(ctx, featurearea.CreateInput{AISessionID: in.AISessionID, Title: in.Title, ProblemStatement: in.ProblemStatement})
		if err != nil {
			return ExternalTopicOutput{}, publicToolError(err)
		}
		return externalTopicOutputFor(value), nil
	}
	composer, ok := service.(interface {
		CreateFeatureAreaWithInitialReview(context.Context, controlapi.FeatureAreaCreateInput) (controlapi.FeatureAreaCreateResult, error)
	})
	if !ok {
		return ExternalTopicOutput{}, publicToolError(errors.New("Feature Area startup composition unavailable"))
	}
	input := controlapi.FeatureAreaCreateInput{AISessionID: in.AISessionID, Title: in.Title, ProblemStatement: in.ProblemStatement, Mode: in.Mode, RequestID: in.RequestID}
	if in.InitialTask != nil {
		input.InitialTask = &controlapi.InitialTaskInput{Title: in.InitialTask.Title, ProblemStatement: in.InitialTask.ProblemStatement, Constraints: append([]string(nil), in.InitialTask.Constraints...), RepositoryID: in.InitialTask.RepositoryID}
	}
	if in.InitialReview != nil {
		input.InitialReview = &controlapi.InitialReviewInput{ReviewDepth: in.InitialReview.ReviewDepth, ReviewPlan: controlapi.InitialReviewPlan{Mode: in.InitialReview.ReviewPlan.Mode, SelectedModules: append([]run.ReviewModuleSelection(nil), in.InitialReview.ReviewPlan.SelectedModules...)}, ReviewInstruction: in.InitialReview.ReviewInstruction, Model: in.InitialReview.Model, ReasoningEffort: in.InitialReview.ReasoningEffort}
	}
	result, err := composer.CreateFeatureAreaWithInitialReview(ctx, input)
	if err != nil {
		return ExternalTopicOutput{}, publicToolError(err)
	}
	out := externalTopicOutputFor(result.FeatureArea)
	if result.Startup != nil {
		out.Startup = externalStartupFor(*result.Startup)
	}
	return out, nil
}

func externalStartupFor(value controlapi.FeatureAreaStartup) *externalFeatureAreaStartup {
	result := &externalFeatureAreaStartup{Mode: value.Mode, RequestID: value.RequestID, CompletedSteps: append([]string(nil), value.CompletedSteps...), FeatureAreaID: value.FeatureAreaID, TaskID: value.TaskID, ReviewID: value.ReviewID, RunID: value.RunID, ReviewAdmitted: value.ReviewAdmitted, RunStatus: value.RunStatus, FailedStep: value.FailedStep, Retryable: value.Retryable, RefreshRequired: value.RefreshRequired}
	if value.Error != nil {
		result.Error = &externalStartupError{Code: boundedUTF8(value.Error.Code, 128), Message: boundedUTF8(value.Error.Message, 2048)}
	}
	if value.Retry != nil {
		result.Retry = &externalStartupRetry{Action: value.Retry.Action, RequestID: value.Retry.RequestID}
	}
	return result
}

func DocumentCreateCore(ctx context.Context, service BridgeService, in ExternalDocumentCreateInput) (ExternalDocumentOutput, error) {
	value, err := service.CreateDocument(ctx, document.CreateInput{AISessionID: in.AISessionID, FeatureAreaID: in.FeatureAreaID, Title: in.Title, Content: in.Content})
	if err != nil {
		return ExternalDocumentOutput{}, publicToolError(err)
	}
	return ExternalDocumentOutput{ID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, Title: value.Title, Content: value.Content, CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}, nil
}

func RepositorySearchCore(ctx context.Context, service BridgeService, in ExternalRepositorySearchInput) (RepositorySearchOutput, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	repositoryID := in.RepositoryID
	if repositoryID == "" {
		values, listErr := service.ListRepositories(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
		if listErr != nil {
			return RepositorySearchOutput{}, publicToolError(listErr)
		}
		if len(values) != 1 {
			return RepositorySearchOutput{}, publicToolError(errors.New("repository search requires a Project Repository when more than one semantic Repository is available"))
		}
		repositoryID = values[0].ID
	}
	value, err := service.SearchRepository(ctx, in.AISessionID, repositoryID, in.Query, in.Paths, limit)
	if err != nil {
		return RepositorySearchOutput{}, publicToolError(err)
	}
	out := RepositorySearchOutput{Matches: make([]RepositorySearchMatchOutput, 0, len(value.Matches)), Truncated: value.Truncated}
	for _, match := range value.Matches {
		out.Matches = append(out.Matches, RepositorySearchMatchOutput{Path: match.Path, Line: match.Line, Snippet: boundedUTF8(match.Snippet, 512)})
	}
	return out, nil
}

func StartTopicReviewCore(ctx context.Context, service BridgeService, in ExternalReviewInput) (ExternalReviewOutput, error) {
	in.ExecutorKind = in.ExecutorKind.Effective()
	if !in.ExecutorKind.Valid() || (in.ExecutorKind == productreview.ExecutorUpperLLM && (in.Model != "" || in.ReasoningEffort != "")) {
		return ExternalReviewOutput{}, publicToolError(controlapi.MapError(productreview.ErrExecutorMismatch))
	}
	for _, id := range []string{in.AISessionID, in.TaskID, in.RequestID} {
		if strings.TrimSpace(id) == "" {
			return ExternalReviewOutput{}, publicToolError(controlapi.MapError(productreview.ErrInvalidArgument))
		}
	}

	if (in.ReviewDepth != "FAST" && in.ReviewDepth != "DEEP") || scopereview.ValidateInstruction(in.ReviewInstruction) != nil {
		return ExternalReviewOutput{}, publicToolError(controlapi.MapError(productreview.ErrInvalidArgument))
	}

	owner, ownerErr := service.GetTask(ctx, in.AISessionID, in.TaskID)
	if ownerErr != nil {
		return ExternalReviewOutput{}, publicToolError(ownerErr)
	}
	if in.FeatureAreaID != "" && in.FeatureAreaID != owner.FeatureAreaID || in.RepositoryID != "" && in.RepositoryID != owner.RepositoryID {
		return ExternalReviewOutput{}, errors.New("Task Review owner mismatch")
	}
	in.FeatureAreaID, in.RepositoryID = owner.FeatureAreaID, owner.RepositoryID
	ctx = observability.WithContext(ctx, observability.Correlation{AISessionID: in.AISessionID, FeatureAreaID: in.FeatureAreaID})
	if !observability.ValidRequestID(in.RequestID) {
		return ExternalReviewOutput{}, publicToolError(controlapi.MapError(productreview.ErrInvalidArgument))
	}
	moduleKeys := make([]string, 0, len(in.ReviewPlan.SelectedModules))
	for _, selected := range in.ReviewPlan.SelectedModules {
		moduleKeys = append(moduleKeys, selected.Key)
	}
	featureAreaValue, err := service.GetFeatureArea(ctx, in.AISessionID, in.FeatureAreaID)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	if !featurearea.IsUsable(featureAreaValue) {
		return ExternalReviewOutput{}, publicToolError(errors.New("Feature Area is not usable for Review"))
	}
	if in.ExecutorKind == productreview.ExecutorCodingAgent {
		resolvedEffort, err := validateModelSelection(ctx, service, in.AISessionID, in.Model, in.ReasoningEffort)
		if err != nil {
			return ExternalReviewOutput{}, publicToolError(err)
		}
		in.ReasoningEffort = resolvedEffort
	}
	sessionValue, err := service.GetSession(ctx, in.AISessionID)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	if !sessionValue.ProjectContext.SnapshotUsable() {
		projection, projectionErr := buildWorkflowProjection(ctx, service, in.AISessionID, in.FeatureAreaID)
		if projectionErr != nil {
			return ExternalReviewOutput{}, publicToolError(projectionErr)
		}
		blockers := make([]string, 0, len(projection.output.Blockers))
		for _, blocker := range projection.output.Blockers {
			blockers = append(blockers, blocker.Summary)
		}
		out := externalReviewStartBlocked(featureAreaValue, in, blockers...)
		return out, nil
	}
	planResolution, err := resolveTopicReviewPlan(ctx, service, in)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	if len(planResolution.blockers) != 0 {
		out := externalReviewStartBlocked(featureAreaValue, in, planResolution.blockers...)
		if resolved, resolveErr := resolvePrerequisites(ctx, service, in.AISessionID, in.RepositoryID, "", "", false); resolveErr == nil {
			out.Review.Blockers = append(out.Review.Blockers, resolved.blockers...)
		}
		return out, nil
	}
	var resolved resolvedPrerequisites
	if in.ExecutorKind == productreview.ExecutorUpperLLM {
		repositories, e := service.ListRepositories(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
		err = e
		for _, repo := range repositories {
			if repo.ID == planResolution.repositoryID {
				resolved.repository = repo
			}
		}
	} else {
		resolved, err = resolvePrerequisites(ctx, service, in.AISessionID, planResolution.repositoryID, "", planResolution.primaryModuleID, true)
	}
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	if resolved.repository.ID == "" || (in.ExecutorKind == productreview.ExecutorCodingAgent && resolved.config.ID == "") {
		return externalReviewStartBlocked(featureAreaValue, in, resolved.blockers...), nil
	}
	var backend *codexconfig.BackendSelection
	if in.ExecutorKind == productreview.ExecutorCodingAgent {
		if in.Backend == nil || in.Backend.ConfigID != resolved.config.ID || in.Backend.ConfigFingerprint != resolved.config.Fingerprint() {
			return ExternalReviewOutput{}, publicToolError(controlapi.MapError(productreview.ErrInvalidArgument))
		}
		selection := resolved.config.Selection("local-mcp-direct")
		backend = &selection
	}
	engineeringGoal := owner.ProblemStatement
	focusSummary := "Start with the selected implementation modules, then follow materially relevant dependencies."
	if planResolution.plan.Mode == run.ReviewModeRepository {
		focusSummary = "Review relevant implementation across the selected Repository."
	}
	payload, _ := json.Marshal(struct {
		TaskID, AISessionID, FeatureAreaID, ReviewInstruction, RepositoryID, Model, ReasoningEffort, ReviewDepth string
		ExecutorKind                                                                                             productreview.ExecutorKind
		ReviewPlan                                                                                               externalReviewPlanInput
		ForbiddenPaths                                                                                           []string
		Backend                                                                                                  *codexconfig.BackendSelectionInput
	}{in.TaskID, in.AISessionID, in.FeatureAreaID, in.ReviewInstruction, in.RepositoryID, in.Model, in.ReasoningEffort, in.ReviewDepth, in.ExecutorKind, in.ReviewPlan, in.ForbiddenPaths, in.Backend})
	sum := sha256.Sum256(payload)
	branchKind := string(resolved.repository.BranchKind)
	if branchKind == "" {
		branchKind = productreview.ProvenanceUnknown
	}
	capturedAt := resolved.repository.RepositoryStateCapturedAt
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	provenance := &productreview.RepositoryProvenance{RepositoryID: resolved.repository.ID, Commit: resolved.repository.CurrentHeadCommit, RepositoryHEAD: resolved.repository.CurrentHeadCommit, BranchKind: branchKind, BranchRef: resolved.repository.BranchRef, InspectionMode: productreview.InspectionFeatureAreaWorkspace, CapturedAt: capturedAt}
	if resolved.repository.RepositoryStateAvailable {
		dirty := resolved.repository.WorkingTreeDirty
		provenance.WorkingTreeDirtyAtAdmission = &dirty
	}
	admission, ok := service.(interface {
		StartAdmittedReview(context.Context, scopereview.AdmittedStartInput) (productreview.StartAdmission, error)
	})
	if !ok {
		return ExternalReviewOutput{}, publicToolError(errors.New("Review admission service unavailable"))
	}
	admitted, err := admission.StartAdmittedReview(ctx, scopereview.AdmittedStartInput{AISessionID: in.AISessionID, RequestID: in.RequestID, PayloadSHA256: hex.EncodeToString(sum[:]), Review: productreview.Review{ForbiddenPaths: append([]string(nil), in.ForbiddenPaths...), ExecutorKind: in.ExecutorKind, ReviewDepth: productreview.ReviewDepth(in.ReviewDepth), TaskID: owner.ID, ProjectID: sessionValue.Project.ID, FeatureAreaID: featureAreaValue.ID, CreatedByAISessionID: in.AISessionID, RepositoryID: resolved.repository.ID, CodexConfigID: resolved.config.ID, BackendSelection: backend, BaseCommit: resolved.repository.CurrentHeadCommit, RepositoryProvenance: provenance, Focus: productreview.Focus{Kind: productreview.FocusKind(planResolution.plan.Mode), Summary: focusSummary, Modules: append([]run.ReviewModuleSelection(nil), planResolution.plan.SelectedModules...), Paths: append([]string(nil), planResolution.scope.Paths...)}, EngineeringGoal: engineeringGoal, Instruction: in.ReviewInstruction, Model: in.Model, ReasoningEffort: in.ReasoningEffort}})
	if err != nil {
		code := errorCode(err)
		observability.Info(ctx, "review", "featurearea.review.start.rejected", "Feature Area Review admission or runtime handoff was rejected", map[string]any{"errorCode": code})
		return ExternalReviewOutput{}, publicToolError(err)
	}
	created, value := admitted.Review, admitted.Run
	ctx = observability.WithContext(ctx, observability.Correlation{ReviewID: created.ID, RunID: value.ID})
	observability.Info(ctx, "review", "featurearea.review.start.accepted", "Review admission, product Review, and internal Run committed before response", map[string]any{"mode": in.ReviewPlan.Mode, "selectedModuleKeys": moduleKeys, "selectedModuleCount": len(moduleKeys), "state": externalReviewStatus(value.Status), "replayed": admitted.Replayed})
	evidenceSummary := productreview.EvidenceSummary{}
	if observed, evidenceErr := service.ReviewEvidence(ctx, in.AISessionID, created.ID); evidenceErr == nil {
		evidenceSummary = observed
	}
	out := externalReviewFor(created, featureAreaValue, &value, evidenceSummary)
	return out, nil
}

func ReviewGetCore(ctx context.Context, service BridgeService, in ExternalReviewGetInput) (ExternalReviewOutput, error) {
	product, err := service.GetReview(ctx, in.AISessionID, in.ReviewID)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	featureAreaValue, err := service.GetFeatureArea(ctx, in.AISessionID, product.FeatureAreaID)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	var internalRun *run.Run
	if product.RunID != "" {
		value, runErr := service.GetRunForSession(ctx, in.AISessionID, product.RunID)
		if runErr != nil || value.ReviewID != product.ID {
			return ExternalReviewOutput{}, publicToolError(errors.New("Review runtime is unavailable"))
		}
		internalRun = &value
	}
	evidence, err := service.ReviewEvidence(ctx, in.AISessionID, product.ID)
	if err != nil {
		return ExternalReviewOutput{}, publicToolError(err)
	}
	return externalReviewFor(product, featureAreaValue, internalRun, evidence), nil
}

// TaskCreateCore creates the intent before any Review is authorized.
func TaskCreateCore(ctx context.Context, service BridgeService, in ExternalTaskCreateInput) (SemanticTaskOutput, error) {
	if !observability.ValidRequestID(in.RequestID) || in.Title == "" || in.ProblemStatement == "" {
		return SemanticTaskOutput{}, errors.New("Task requires requestId, title and problemStatement")
	}
	input := controlapi.CreatePlanningTaskInput{AISessionID: in.AISessionID, FeatureAreaID: in.FeatureAreaID, RepositoryID: in.RepositoryID, RequestID: in.RequestID, Title: in.Title, Constraints: in.Constraints, ProblemStatement: in.ProblemStatement, SkipReview: in.SkipReview}
	var value task.Task
	err := task.ErrNotFound
	if replay, ok := service.(interface {
		ReplayPlanningTask(context.Context, controlapi.CreatePlanningTaskInput) (task.Task, error)
	}); ok {
		value, err = replay.ReplayPlanningTask(ctx, input)
	}
	if errors.Is(err, task.ErrNotFound) {
		resolved, e := resolvePrerequisites(ctx, service, in.AISessionID, in.RepositoryID, "", "", true)
		if e != nil {
			return SemanticTaskOutput{}, publicToolError(e)
		}
		input.RepositoryID, input.CodexConfigID, input.ModuleID, input.BaseRevision = resolved.repository.ID, resolved.config.ID, resolved.module.ID, resolved.repository.CurrentHeadCommit
		value, err = service.CreatePlanningTask(ctx, input)
	}
	if err != nil {
		return SemanticTaskOutput{}, publicToolError(err)
	}
	out := SemanticTaskOutput{RequiredReviewDepth: value.RequiredReviewDepth, AllowedReviewDepths: task.AllowedReviewDepths(value.RequiredReviewDepth), ID: value.ID, FeatureAreaID: value.FeatureAreaID, Title: value.Title, Status: string(value.Status), Phase: string(value.Phase)}
	reader, ok := service.(interface {
		WorkflowGuidanceForResource(context.Context, wp.ResourceScope) (wp.Guidance, error)
	})
	if !ok {
		out.RefreshRequired = true
		return out, nil
	}
	g, err := reader.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: in.AISessionID, TaskID: value.ID})
	if err != nil {
		out.RefreshRequired = true
		return out, nil
	}
	out.NextAction = g.NextAction
	return out, nil
}

func ScopeExpansionProposeCore(ctx context.Context, service BridgeService, in ExternalScopeExpansionInput) (ExternalScopeExpansionOutput, error) {
	internal, targetKind, targetID, err := scopeExpansionTarget(ctx, service, in.AISessionID, in.FeatureAreaID, in.ReviewID)
	if err != nil {
		return ExternalScopeExpansionOutput{}, publicToolError(err)
	}
	value, err := service.CreateScopeExpansion(ctx, scopeexpansion.CreateInput{AISessionID: in.AISessionID, ReviewID: internal.ID, CurrentScope: run.ReviewScope{Kind: run.ReviewScopeKind(in.CurrentScope.Kind), Paths: in.CurrentScope.Paths}, AdditionalScope: run.ReviewScope{Kind: run.ReviewScopeKind(in.AdditionalScope.Kind), Paths: in.AdditionalScope.Paths}, Reason: in.Reason})
	if err != nil {
		return ExternalScopeExpansionOutput{}, publicToolError(err)
	}
	return externalScopeExpansionFor(value, targetKind, targetID), nil
}

func ExecutionGetCore(ctx context.Context, service BridgeService, in ExternalExecutionInput) (ExternalExecutionOutput, error) {
	p, err := readAgentExecution(ctx, service, in.AISessionID, in.ExecutionID)
	if err != nil {
		return ExternalExecutionOutput{}, publicToolError(err)
	}
	runs := make([]run.Run, 0, len(p.RunIDs))
	for _, id := range p.RunIDs {
		value, err := service.GetRunForSession(ctx, in.AISessionID, id)
		if err != nil {
			return ExternalExecutionOutput{}, publicToolError(err)
		}
		if value.AgentExecutionID != p.ExecutionID || value.TaskID != p.TaskID || value.ProjectID != p.ProjectID {
			return ExternalExecutionOutput{}, publicToolError(errors.New("Execution Run association changed"))
		}
		runs = append(runs, value)
	}
	out := externalExecutionFor(p, runs)
	if p.ExecutionProposalID != "" && !p.ReadOnly {
		proposal, err := service.GetExecutionProposal(ctx, in.AISessionID, p.ExecutionProposalID)
		if err != nil {
			return out, publicToolError(err)
		}
		out.TaskDefinitionRevisionID = proposal.TaskDefinitionRevisionID
		out.CreatedAt = proposal.CreatedAt.UTC().Format(timeFormat)
		out.GoalProgress = externalGoalProgressFor(ctx, service, in.AISessionID, p.ExecutionProposalID)
	} else if len(runs) > 0 {
		out.CreatedAt = runs[0].CreatedAt.UTC().Format(timeFormat)
	}
	cancellation, err := projectExecutionCancellation(ctx, service, in.AISessionID, &p, runs)
	if err != nil {
		return out, publicToolError(err)
	}
	out.Cancellation = &cancellation
	if in.Detail != "EVIDENCE" {
		for i := range out.Evidence {
			out.Evidence[i].Git.DiffArtifactID = ""
		}
	}
	return out, nil
}
