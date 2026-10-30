// Package bridgehttp exposes the bounded loopback HTTP surface used by local
// Bridge clients. Fixture provisioning is compiled into the executable but is
// disabled unless explicitly enabled by the process owner.
package bridgehttp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/event"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

const (
	maxRequestBytes  = 64 << 10
	maxObservedFiles = 256
)

type Server struct {
	plane           *controlplane.ControlPlane
	api             *controlapi.Facade
	console         *consoleapi.Reader
	operatorIngress *localoperator.Ingress
	adminEnabled    bool
	handler         http.Handler
}

type ProvisionRequest struct {
	SourceRepository string `json:"source_repository"`
	CodexPath        string `json:"codex_path,omitempty"`
}

type ReviewCommandObservationRequest struct {
	Command string `json:"command"`
}

type ProvisionResult struct {
	ProjectID     string `json:"project_id"`
	RepositoryID  string `json:"repository_id"`
	ModuleID      string `json:"module_id"`
	CodexConfigID string `json:"codex_config_id"`
	AISessionID   string `json:"ai_session_id"`
}

type RunObservation struct {
	AgentRuntime          *agentruntime.Snapshot  `json:"agent_runtime,omitempty"`
	ProjectID             string                  `json:"project_id,omitempty"`
	FeatureAreaID         string                  `json:"feature_area_id,omitempty"`
	OperationKind         run.OperationKind       `json:"operation_kind,omitempty"`
	OperationID           string                  `json:"operation_id,omitempty"`
	ReviewID              string                  `json:"review_id,omitempty"`
	RunID                 string                  `json:"run_id"`
	TaskID                string                  `json:"task_id"`
	Purpose               run.Purpose             `json:"purpose"`
	ExecutionProposalID   string                  `json:"execution_proposal_id,omitempty"`
	WorkspaceID           string                  `json:"workspace_id,omitempty"`
	Status                run.Status              `json:"status"`
	FailureCode           string                  `json:"failure_code,omitempty"`
	FailureStage          string                  `json:"failure_stage,omitempty"`
	FailureMessage        string                  `json:"failure_message,omitempty"`
	WorktreeID            string                  `json:"worktree_id,omitempty"`
	WorktreePath          string                  `json:"worktree_path,omitempty"`
	WorktreeBranch        string                  `json:"worktree_branch,omitempty"`
	ThreadID              string                  `json:"thread_id,omitempty"`
	TurnID                string                  `json:"turn_id,omitempty"`
	StartSnapshot         string                  `json:"start_snapshot,omitempty"`
	EndSnapshot           string                  `json:"end_snapshot,omitempty"`
	LiveSession           bool                    `json:"live_session"`
	StartedAt             *time.Time              `json:"started_at,omitempty"`
	FinishedAt            *time.Time              `json:"finished_at,omitempty"`
	TokenUsageStatus      run.TokenUsageStatus    `json:"token_usage_status"`
	TokenUsage            *run.TokenCounts        `json:"token_usage,omitempty"`
	VerificationReport    *run.VerificationReport `json:"verification_report,omitempty"`
	BaseCommit            string                  `json:"base_commit,omitempty"`
	HeadCommit            string                  `json:"head_commit,omitempty"`
	Dirty                 bool                    `json:"dirty"`
	ChangedFiles          []string                `json:"changed_files,omitempty"`
	ChangedFilesTruncated bool                    `json:"changed_files_truncated"`
	DiffArtifactID        string                  `json:"diff_artifact_id,omitempty"`
	Diff                  string                  `json:"diff,omitempty"`
	DiffTruncated         bool                    `json:"diff_truncated"`
	AgentClaim            string                  `json:"agent_claim,omitempty"`
	Approvals             []ApprovalObservation   `json:"approvals"`
	Events                []EventObservation      `json:"events"`
	RecoveryEventCount    int                     `json:"recovery_event_count"`
}

type ApprovalObservation struct {
	ID           string                `json:"id"`
	RunID        string                `json:"run_id"`
	ThreadID     string                `json:"thread_id,omitempty"`
	Status       approval.Status       `json:"status"`
	DecisionType approval.DecisionType `json:"decision_type,omitempty"`
	DecidedAt    *time.Time            `json:"decided_at,omitempty"`
	DeliveredAt  *time.Time            `json:"delivered_at,omitempty"`
}

type EventObservation struct {
	Sequence     int64     `json:"sequence"`
	Type         string    `json:"type"`
	From         string    `json:"from,omitempty"`
	To           string    `json:"to,omitempty"`
	ReasonCode   string    `json:"reason_code,omitempty"`
	DecisionType string    `json:"decision_type,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type sessionAction struct {
	AISessionID string `json:"ai_session_id"`
}

type taskPlanCreateInput struct {
	AISessionID      string
	TaskID           string
	WorkSpec         task.WorkSpec
	VerificationSpec task.VerificationSpec
}
type repositorySearchInput struct {
	AISessionID  string
	RepositoryID string
	Query        string
	Paths        []string
	Limit        int
}
type scopeReviewStartInput struct {
	AISessionID string `json:"aiSessionId"`
	ReviewID    string `json:"reviewId"`
}

type approvalAction struct {
	AISessionID string            `json:"ai_session_id"`
	Decision    approval.Decision `json:"decision"`
}

type moduleCreateAction struct {
	AISessionID string  `json:"ai_session_id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	ParentID    *string `json:"parent_id,omitempty"`
}

type taskClassifyAction struct {
	AISessionID string `json:"ai_session_id"`
	ModuleID    string `json:"module_id"`
}

type contextProbeCreateAction struct {
	AISessionID      string             `json:"ai_session_id"`
	RepositoryID     string             `json:"repository_id"`
	CodexConfigID    string             `json:"codex_config_id"`
	Scope            contextprobe.Scope `json:"scope"`
	CodexInstruction string             `json:"codex_instruction"`
}

func New(plane *controlplane.ControlPlane, adminEnabled bool) (*Server, error) {
	return NewWithLogDirectory(plane, adminEnabled, "")
}

// NewWithLogDirectory configures the only directory from which the local
// Console diagnostics endpoint may read structured logs.
func NewWithLogDirectory(plane *controlplane.ControlPlane, adminEnabled bool, logDirectory string) (*Server, error) {
	if plane == nil || !plane.Ready || plane.Worktrees == nil {
		return nil, errors.New("ready control plane with worktree observation is required")
	}
	api, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		return nil, err
	}
	console, err := consoleapi.NewWithLogDirectory(plane, api, logDirectory)
	if err != nil {
		return nil, err
	}
	s := &Server{plane: plane, api: api, console: console, adminEnabled: adminEnabled}
	mux := http.NewServeMux()
	mux.Handle("GET /ui/", s.ui())
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/runtime/codex", func(w http.ResponseWriter, r *http.Request) {
		selected, err := codexlaunch.Runtime("")
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		writeJSON(w, http.StatusOK, map[string]string{"selectionFile": os.Getenv(codexlaunch.SelectionEnv), "executable": selected.Executable, "error": detail})
	})
	mux.HandleFunc("GET /v1/runtime/codex-inventory", s.getCodexRuntimeInventory)
	mux.HandleFunc("PUT /v1/runtime/codex-inventory", s.applyCodexRuntimeInventory)
	mux.HandleFunc("POST /admin/test/v1/fixture", s.provision)
	mux.HandleFunc("POST /admin/test/v1/review-command-observation", s.observeReviewCommand)
	mux.HandleFunc("GET /v1/projects", s.listConsoleProjects)
	mux.HandleFunc("POST /v1/projects", s.registerProject)
	mux.HandleFunc("GET /v1/projects/{id}/execution-authorization", s.getExecutionAuthorization)
	mux.HandleFunc("PUT /v1/projects/{id}/execution-authorization", s.updateExecutionAuthorization)
	mux.HandleFunc("GET /v1/project-selection", s.getConsoleProjectSelection)
	mux.HandleFunc("POST /v1/project-selection", s.selectConsoleProject)
	mux.HandleFunc("GET /v1/project-overview", s.getConsoleProjectOverview)
	mux.HandleFunc("GET /v1/development-context", s.getDevelopmentContext)
	mux.HandleFunc("POST /v1/development-branch-selection", s.selectDevelopmentBranch)
	mux.HandleFunc("POST /v1/repository-unlink/prepare", s.prepareRepositoryUnlink)
	mux.HandleFunc("GET /v1/repository-unlink", s.readRepositoryUnlink)
	mux.HandleFunc("GET /v1/repository-unlink/list", s.listRepositoryUnlink)
	mux.HandleFunc("POST /v1/repository-unlink/commit", s.commitRepositoryUnlink)
	mux.HandleFunc("GET /ui/api/project-workspace", s.getProductProjectWorkspace)
	mux.HandleFunc("GET /ui/api/work-tabs/{tab}", s.getProductWorkTab)
	mux.HandleFunc("GET /ui/api/tasks/{id}", s.getProductTask)
	mux.HandleFunc("GET /ui/api/tasks/{id}/runs", s.listProductTaskRuns)
	mux.HandleFunc("GET /ui/api/tasks/{id}/definition-revisions/{revision}", s.getProductTaskDefinitionRevision)
	mux.HandleFunc("POST /ui/api/tasks/{id}/local-approve-and-start-receipt", s.readLocalApproveAndStartReceipt)
	mux.HandleFunc("POST /ui/api/tasks/{id}/action", s.resolveTaskAction)
	mux.HandleFunc("GET /ui/api/tasks/{id}/completion-plan", s.prepareTaskCompletion)
	mux.HandleFunc("POST /ui/api/featureAreas/{id}/action", s.featureAreaAction)
	mux.HandleFunc("POST /ui/api/executions/{id}/cancel", s.cancelProductExecution)
	mux.HandleFunc("POST /ui/api/runs/{id}/agent-requests/resolve", s.resolveProductAgentRequest)
	mux.HandleFunc("POST /ui/api/runs/{id}/agent-user-decisions", s.submitProductAgentUserDecision)
	mux.HandleFunc("POST /ui/api/runs/{id}/steering", s.steerProductRun)
	mux.HandleFunc("POST /ui/api/runs/{id}/agent-requests/recovery", s.recoverProductAgentRequest)
	mux.HandleFunc("GET /ui/api/runs/{id}/session", s.getRuntimeSession)
	mux.HandleFunc("GET /ui/api/runs/{id}/session/items", s.getRuntimeSessionItems)
	mux.HandleFunc("GET /ui/api/runs/{id}/session/native-history", s.getCodexHistory)
	mux.HandleFunc("GET /ui/api/tasks/{id}/notifications", s.getTaskEventStatus)
	mux.HandleFunc("GET /ui/api/runs/{id}/session/instructions", s.getRuntimeSessionInstructions)
	mux.HandleFunc("GET /ui/api/runs/{id}/session/events", s.streamRuntimeSession)
	mux.HandleFunc("POST /ui/api/runs/{id}/approvals/{approval}/decide", s.decideRuntimeApproval)
	mux.HandleFunc("GET /ui/api/featureAreas/{id}/presentation", s.getFeatureAreaPresentation)
	mux.HandleFunc("GET /ui/api/development-workspace", s.getDevelopmentWorkspace)
	mux.HandleFunc("GET /ui/api/worktree-inventory", s.getWorktreeInventory)
	mux.HandleFunc("POST /ui/api/featureAreas/{id}/close", s.closeProductFeatureArea)
	mux.HandleFunc("GET /ui/api/reviews/{id}/result", s.getProductReviewResult)
	mux.HandleFunc("GET /ui/api/documents/{id}", s.getProductDocument)
	s.registerFeatureAreaLifecycle(mux)
	mux.HandleFunc("POST /v1/repository-search", s.searchRepository)
	mux.HandleFunc("POST /v1/task-plans", s.createTaskPlan)
	mux.HandleFunc("GET /v1/task-plans/{id}", s.getTaskPlan)
	mux.HandleFunc("POST /v1/scope-reviews", s.startScopeReview)
	mux.HandleFunc("GET /v1/project-context", s.getProjectContext)
	mux.HandleFunc("GET /v1/modules", s.listConsoleModules)
	mux.HandleFunc("POST /v1/modules", s.createModule)
	mux.HandleFunc("GET /v1/repositories", s.listConsoleRepositories)
	mux.HandleFunc("GET /v1/ai-sessions", s.listConsoleAISessions)
	mux.HandleFunc("GET /v1/context-probes", s.listConsoleContextProbes)
	mux.HandleFunc("POST /v1/tasks/{id}/classify", s.classifyTask)
	mux.HandleFunc("POST /v1/context-probes", s.createContextProbe)
	mux.HandleFunc("GET /v1/context-probes/{id}", s.getContextProbe)
	mux.HandleFunc("GET /v1/context-probes/{id}/prompt", s.getConsoleContextProbePrompt)
	mux.HandleFunc("POST /v1/context-probes/{id}/start", s.startContextProbe)
	mux.HandleFunc("POST /v1/project-inspections/prepare", s.prepareProjectInspection)
	mux.HandleFunc("POST /v1/project-inspections/confirm", s.confirmProjectInspection)
	mux.HandleFunc("POST /v1/project-inspections/{id}/start", s.startProjectInspection)
	mux.HandleFunc("GET /v1/tasks", s.listConsoleTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.getTask)
	mux.HandleFunc("GET /v1/reviews/{id}/prompt", s.getConsoleReviewPrompt)
	mux.HandleFunc("GET /v1/reviews/{id}/repository-drift", s.getConsoleReviewRepositoryDrift)
	mux.HandleFunc("POST /v1/execution-proposals", s.createExecutionProposal)
	mux.HandleFunc("GET /v1/execution-proposals/{id}", s.getExecutionProposal)
	mux.HandleFunc("GET /v1/runs", s.listConsoleRuns)
	mux.HandleFunc("GET /v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /v1/runs/{id}/prompt", s.getConsoleRunPrompt)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancelRun)
	mux.HandleFunc("POST /v1/operations/cancel", s.cancelOperation)
	mux.HandleFunc("POST /v1/executions/successor/prepare", s.prepareSuccessorExecution)
	mux.HandleFunc("POST /v1/tasks/stop-and-revise", s.stopAndReviseNoRunTask)
	mux.HandleFunc("GET /v1/runs/{id}/approvals", s.listApprovals)
	mux.HandleFunc("GET /v1/approvals", s.listConsoleApprovals)
	mux.HandleFunc("GET /v1/logs", s.listConsoleLogs)
	mux.HandleFunc("POST /v1/approvals/{id}/decide", s.decideApproval)
	mux.HandleFunc("GET /v1/runs/{id}/agent-runtime", s.agentRuntimeState)
	mux.HandleFunc("POST /v1/runs/{id}/agent-requests/resolve", s.resolveAgentRequest)
	mux.HandleFunc("POST /v1/runs/{id}/agent-user-decisions", s.submitAgentUserDecision)
	mux.HandleFunc("POST /v1/runs/{id}/agent-requests/recovery", s.recoverAgentRequest)
	mux.HandleFunc("POST /v1/recovery-status/prepare", s.prepareRecoveryStatus)
	mux.HandleFunc("POST /v1/recovery-status/{id}/start", s.startRecoveryStatus)
	mux.HandleFunc("POST /v1/recovery-status/{id}/resolve", s.resolveRecoveryStatus)
	mux.HandleFunc("POST /v1/recovery-status/{id}/continue-inspection", s.continueInspectionRecovery)
	mux.HandleFunc("GET /v1/recovery-status/{id}", s.readRecoveryStatus)
	mux.HandleFunc("POST /ui/api/recovery-status/prepare", s.prepareProductRecoveryStatus)
	mux.HandleFunc("POST /ui/api/recovery-status/{id}/start", s.startProductRecoveryStatus)
	mux.HandleFunc("POST /ui/api/recovery-status/{id}/resolve", s.resolveProductRecoveryStatus)
	mux.HandleFunc("POST /ui/api/recovery-status/{id}/continue-inspection", s.continueProductInspectionRecovery)
	mux.HandleFunc("GET /ui/api/recovery-status/{id}", s.readProductRecoveryStatus)
	mux.HandleFunc("GET /v1/runs/{id}/observation", s.observeRun)
	s.registerWorkflowProgression(mux)
	operatorIngress, err := localoperator.NewIngress(plane.DataPaths.Root)
	if err != nil {
		return nil, fmt.Errorf("construct local operator access: %w", err)
	}
	s.operatorIngress = operatorIngress
	s.handler = localRequestLogging(localOperatorAdmission(operatorIngress, s.workflowMutationGuidance(mux)))
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ready": s.plane.Ready})
}

func (s *Server) provision(w http.ResponseWriter, r *http.Request) {
	if !s.adminEnabled || !remoteIsLoopback(r.RemoteAddr) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodePermissionDenied, Message: "test administration is disabled"})
		return
	}
	var input ProvisionRequest
	if !decode(w, r, &input) {
		return
	}
	ctx := r.Context()
	projectValue, err := s.plane.Projects.Create(ctx, "Real E2E fixture")
	if err != nil {
		writeError(w, err)
		return
	}
	repositoryValue, err := s.plane.Repositories.RegisterInProject(ctx, projectValue.ID, input.SourceRepository, "disposable-fixture")
	if err != nil {
		writeError(w, err)
		return
	}
	moduleValue, err := s.plane.Modules.Create(ctx, module.CreateInput{ProjectID: projectValue.ID, Key: "fixture", Name: "Fixture"})
	if err != nil {
		writeError(w, err)
		return
	}
	configValue, err := s.plane.CodexConfigs.Create(ctx, codexconfig.CreateInput{Name: "Real Codex", CodexPath: input.CodexPath, Enabled: true})
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := s.plane.ProjectSelection.Select(ctx, projectValue.ID); err != nil {
		writeError(w, err)
		return
	}
	ctx = aisession.WithCallerScope(ctx, "bridgehttp-admin-fixture")
	ref := aiprojectselection.ClientBindingReference{CallerScope: "bridgehttp-admin-fixture"}
	initialized, err := s.plane.AIContext.InitializeSession(ctx, ref, "admin-fixture-initialize", "")
	if err != nil {
		writeError(w, err)
		return
	}
	bound, err := s.plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectValue.ID, "admin-fixture-bind")
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ProvisionResult{ProjectID: projectValue.ID, RepositoryID: repositoryValue.ID, ModuleID: moduleValue.ID, CodexConfigID: configValue.ID, AISessionID: bound.AISessionID})
}

// observeReviewCommand is an admin-test-only black-box seam proving that
// observed command text is diagnostic data, not a semantic authorization
// input. It never executes the supplied command.
func (s *Server) observeReviewCommand(w http.ResponseWriter, r *http.Request) {
	if !s.adminEnabled || !remoteIsLoopback(r.RemoteAddr) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodePermissionDenied, Message: "test administration is disabled"})
		return
	}
	var input ReviewCommandObservationRequest
	if !decode(w, r, &input) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"observed": true})
}

func (s *Server) createTaskPlan(w http.ResponseWriter, r *http.Request) {
	var input taskPlanCreateInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CreateTaskPlan(r.Context(), taskplan.CreateInput{AISessionID: input.AISessionID, TaskID: input.TaskID, WorkSpec: input.WorkSpec, VerificationSpec: input.VerificationSpec})
	writeResult(w, value, err, http.StatusCreated)
}
func (s *Server) searchRepository(w http.ResponseWriter, r *http.Request) {
	var input repositorySearchInput
	if !decode(w, r, &input) {
		return
	}
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}
	value, err := s.api.SearchRepositoryForSession(r.Context(), input.AISessionID, input.RepositoryID, input.Query, input.Paths, limit)
	writeResult(w, value, err, http.StatusOK)
}
func (s *Server) getTaskPlan(w http.ResponseWriter, r *http.Request) {
	value, err := s.api.GetTaskPlan(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	writeResult(w, value, err, http.StatusOK)
}
func (s *Server) startScopeReview(w http.ResponseWriter, r *http.Request) {
	var input scopeReviewStartInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.StartScopeReview(r.Context(), scopereview.StartInput{AISessionID: input.AISessionID, ReviewID: input.ReviewID})
	writeResult(w, value, err, http.StatusCreated)
}

func writeResult(w http.ResponseWriter, value any, err error, success int) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, success, value)
}

func (s *Server) getProjectContext(w http.ResponseWriter, r *http.Request) {
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		value, err := s.console.GetProjectOverview(r.Context(), projectID)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, value.Context)
		return
	}
	value, err := s.api.GetProjectContextForSession(r.Context(), r.URL.Query().Get("ai_session_id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) createModule(w http.ResponseWriter, r *http.Request) {
	var input moduleCreateAction
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CreateModuleForSession(r.Context(), input.AISessionID, input.Key, input.Name, input.Description, input.ParentID)
	respond(w, http.StatusCreated, value, err)
}

func (s *Server) classifyTask(w http.ResponseWriter, r *http.Request) {
	var input taskClassifyAction
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.ClassifyTaskForSession(r.Context(), input.AISessionID, r.PathValue("id"), input.ModuleID)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) createContextProbe(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, &controlapi.Error{Code: controlapi.CodeConflict, Message: "new ContextProbe writes require Project Inspection prepare and confirm"})
}

func (s *Server) prepareProjectInspection(w http.ResponseWriter, r *http.Request) {
	var input contextprobe.PrepareInspectionInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.PrepareProjectInspection(r.Context(), input)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) confirmProjectInspection(w http.ResponseWriter, r *http.Request) {
	var input contextprobe.ConfirmInspectionInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.ConfirmProjectInspection(r.Context(), input)
	respond(w, http.StatusCreated, value, err)
}

func (s *Server) startProjectInspection(w http.ResponseWriter, r *http.Request) {
	var input contextprobe.StartInspectionInput
	if !decode(w, r, &input) {
		return
	}
	if input.InspectionOperationID != r.PathValue("id") {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Inspection path and operation identity differ"})
		return
	}
	value, err := s.api.StartConfirmedProjectInspection(r.Context(), input)
	respond(w, http.StatusAccepted, value, err)
}

func (s *Server) getContextProbe(w http.ResponseWriter, r *http.Request) {
	value, err := s.api.GetContextProbeForSession(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) startContextProbe(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, &controlapi.Error{Code: controlapi.CodeConflict, Message: "legacy ContextProbe runtime cannot be resumed; start a new canonical Project Inspection"})
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		value, err := s.console.GetTask(r.Context(), projectID, r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
		return
	}
	value, err := s.plane.AIContext.GetTask(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	if err == nil {
		err = s.api.EnrichTaskExecution(r.Context(), &value)
	}
	respond(w, http.StatusOK, value, err)
}

func (s *Server) createExecutionProposal(w http.ResponseWriter, r *http.Request) {
	var input executionproposal.CreateInput
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CreateExecutionProposal(r.Context(), input)
	respond(w, http.StatusCreated, value, err)
}

func (s *Server) getExecutionProposal(w http.ResponseWriter, r *http.Request) {
	value, err := s.api.GetExecutionProposal(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	if taskID := r.URL.Query().Get("task_id"); taskID != "" {
		value, err := s.console.GetRunForTask(r.Context(), r.URL.Query().Get("project_id"), taskID, r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
		return
	}
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		value, err := s.console.GetRun(r.Context(), projectID, r.PathValue("id"))
		respond(w, http.StatusOK, value, err)
		return
	}
	value, err := s.api.GetRunForSession(r.Context(), r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request) {
	var input sessionAction
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.CancelRunForSession(r.Context(), input.AISessionID, r.PathValue("id"))
	respond(w, http.StatusOK, value, err)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	value, err := s.api.ListApprovalsForSession(r.Context(), r.URL.Query().Get("ai_session_id"), controlapi.ApprovalFilter{RunID: r.PathValue("id"), ListOptions: controlapi.ListOptions{Limit: 100}})
	respond(w, http.StatusOK, value, err)
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	var input approvalAction
	if !decode(w, r, &input) {
		return
	}
	value, err := s.api.DecideApprovalForSession(r.Context(), input.AISessionID, r.PathValue("id"), input.Decision)
	respond(w, http.StatusOK, value, err)
}

func (s *Server) observeRun(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	runValue, err := s.api.GetRunForSession(ctx, r.URL.Query().Get("ai_session_id"), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	value := RunObservation{
		AgentRuntime: runValue.AgentRuntime,
		RunID:        runValue.ID, ProjectID: runValue.ProjectID, FeatureAreaID: runValue.FeatureAreaID, OperationKind: runValue.OperationKind, OperationID: runValue.OperationID, ReviewID: runValue.ReviewID, TaskID: runValue.TaskID, Purpose: runValue.Purpose, ExecutionProposalID: runValue.ExecutionProposalID, Status: runValue.Status,
		WorkspaceID: runValue.WorkspaceID, WorktreeID: runValue.WorktreeID, ThreadID: runValue.ThreadID, TurnID: runValue.TurnID,
		StartSnapshot: runValue.StartSnapshot, EndSnapshot: runValue.EndSnapshot,
		StartedAt: runValue.StartedAt, FinishedAt: runValue.FinishedAt,
		TokenUsageStatus: runValue.TokenUsage.Status, TokenUsage: runValue.TokenUsage.Usage, VerificationReport: runValue.VerificationReport,
	}
	if runValue.Failure != nil {
		value.FailureCode, value.FailureStage, value.FailureMessage = runValue.Failure.Code, runValue.Failure.Stage, runValue.Failure.Message
	}
	if _, live := s.plane.Supervisor.GetSession(runValue.ID); live {
		value.LiveSession = true
	}
	if runValue.WorktreeID != "" {
		worktreeValue, getErr := s.plane.Worktrees.Get(ctx, runValue.WorktreeID)
		if getErr != nil {
			writeError(w, getErr)
			return
		}
		value.WorktreePath, value.WorktreeBranch, value.BaseCommit = worktreeValue.Path, worktreeValue.Branch, worktreeValue.BaseCommit
	}
	if evidenceValue, getErr := s.api.GetEvidence(ctx, runValue.ID); getErr == nil {
		value.BaseCommit = evidenceValue.Evidence.BaseCommit
		value.HeadCommit = evidenceValue.Evidence.HeadCommit
		value.Dirty = evidenceValue.Evidence.Dirty
		value.ChangedFiles = append([]string(nil), evidenceValue.Evidence.ChangedFiles...)
		if len(value.ChangedFiles) > maxObservedFiles {
			value.ChangedFiles = value.ChangedFiles[:maxObservedFiles]
			value.ChangedFilesTruncated = true
		}
		value.DiffArtifactID = evidenceValue.Evidence.DiffArtifactID
		for _, item := range evidenceValue.Artifacts {
			switch item.Kind {
			case artifact.KindGitDiff:
				value.Diff, value.DiffTruncated, err = s.readArtifact(ctx, item.ID)
			case artifact.KindAgentMessage:
				value.AgentClaim, _, err = s.readArtifact(ctx, item.ID)
			}
			if err != nil {
				writeError(w, err)
				return
			}
		}
	} else {
		var apiErr *controlapi.Error
		if !errors.As(getErr, &apiErr) || apiErr.Code != controlapi.CodeNotFound {
			writeError(w, getErr)
			return
		}
	}
	approvalValues, err := s.api.ListApprovalsForSession(ctx, r.URL.Query().Get("ai_session_id"), controlapi.ApprovalFilter{RunID: runValue.ID, ListOptions: controlapi.ListOptions{Limit: 100}})
	if err != nil {
		writeError(w, err)
		return
	}
	value.Approvals = make([]ApprovalObservation, 0, len(approvalValues))
	for _, item := range approvalValues {
		observed := ApprovalObservation{ID: item.ID, RunID: item.RunID, ThreadID: item.ThreadID, Status: item.Status, DecidedAt: item.DecidedAt, DeliveredAt: item.DeliveredAt}
		if item.Decision != nil {
			observed.DecisionType = item.Decision.Type
		}
		value.Approvals = append(value.Approvals, observed)
	}
	eventValues, err := s.api.ListEvents(ctx, controlapi.EventFilter{RunID: runValue.ID, Limit: 100})
	if err != nil {
		writeError(w, err)
		return
	}
	value.Events = make([]EventObservation, 0, len(eventValues))
	for _, item := range eventValues {
		var payload struct {
			From       string            `json:"from"`
			To         string            `json:"to"`
			ReasonCode string            `json:"reason_code"`
			Decision   approval.Decision `json:"decision"`
		}
		_ = json.Unmarshal(item.Payload, &payload)
		value.Events = append(value.Events, EventObservation{Sequence: item.Sequence, Type: item.Type, From: payload.From, To: payload.To, ReasonCode: payload.ReasonCode, DecisionType: string(payload.Decision.Type), CreatedAt: item.CreatedAt})
		if item.Type == event.RecoveryRunInterrupted {
			value.RecoveryEventCount++
		}
	}
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) readArtifact(ctx context.Context, id string) (string, bool, error) {
	// The API reads only a registered artifact by ID and enforces a 64 KiB
	// bound; no path supplied by the caller reaches the filesystem.
	chunk, err := s.api.ReadArtifact(ctx, id, 0, controlapi.MaxArtifactReadBytes)
	if err != nil {
		return "", false, err
	}
	data, err := base64.StdEncoding.DecodeString(chunk.Data)
	if err != nil {
		return "", false, err
	}
	return string(data), chunk.More, nil
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "invalid JSON request: " + err.Error()})
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "request must contain one JSON value"})
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, status, value)
}

func writeError(w http.ResponseWriter, err error) {
	var progressionErr *wp.Error
	if errors.As(err, &progressionErr) {
		writeJSON(w, http.StatusConflict, progressionErr)
		return
	}
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) {
		apiErr = controlapi.MapError(err)
	}
	writeAPIError(w, apiErr)
}

func writeAPIError(w http.ResponseWriter, err *controlapi.Error) {
	writeJSON(w, apiErrorStatus(err), map[string]string{"code": string(err.Code), "message": err.Message})
}

func apiErrorStatus(err *controlapi.Error) int {
	status := http.StatusInternalServerError
	switch err.Code {
	case "TASK_ACTION_PENDING", "TASK_ACTION_STALE", "TASK_ACTION_CONFLICT":
		status = http.StatusConflict
	case "TASK_RUNTIME_SELECTION_SOURCE_MISSING", "TASK_RUNTIME_SELECTION_BINDING_MISSING", "TASK_RUNTIME_SELECTION_BINDING_CONFLICT", "TASK_RUNTIME_SELECTION_BINDING_NOT_CURRENT_REVISION", "RUNTIME_PLATFORM_DESCRIPTOR_MISSING", "RUNTIME_PLATFORM_QUALIFICATION_MISSING", "RUNTIME_PLATFORM_QUALIFICATION_AMBIGUOUS", "RUNTIME_PLATFORM_QUALIFICATION_INVALIDATED", "RUNTIME_PLATFORM_QUALIFICATION_NOT_CURRENT_FOR_NEW_BINDING", "RUNTIME_PLATFORM_IDENTITY_MISMATCH", "RUNTIME_PLATFORM_SOURCE_IDENTITY_CONFLICT", "FORMAL_EXECUTION_MISSING", "FORMAL_EXECUTION_IDENTITY_CONFLICT", "FORMAL_EXECUTION_PARENT_REVISION_MISSING", "FORMAL_ADOPTION_QUALIFICATION_REQUIRED":
		status = http.StatusConflict
	case controlapi.CodeInvalidArgument:
		status = http.StatusBadRequest
	case controlapi.CodeInvalidPath, controlapi.CodeNotGitRepository:
		status = http.StatusBadRequest
	case controlapi.CodeNotFound, "DEVELOPMENT_BRANCH_NOT_FOUND", "DEVELOPMENT_REF_MISSING":
		status = http.StatusNotFound
	case "DELIVERY_REPREPARE_REQUIRED", "DELIVERY_RETRY_REQUEST_CONFLICT", "DELIVERY_RETRY_ALREADY_EXISTS", "DELIVERY_ABANDONED", "MERGE_PLAN_RELEASED", "GIT_FINALIZATION_PENDING", "RECOVERY_REQUIRED", "MERGE_CONFLICT", "RESPONSE_DISPOSITION_UNKNOWN", controlapi.CodeFeatureAreaRetiring, "DEVELOPMENT_BRANCH_NOT_SELECTED", "REPOSITORY_PROJECT_MISMATCH", "REQUEST_CONFLICT", "REPOSITORY_UNLINK_BLOCKED", "REPOSITORY_UNLINK_STALE", "REPOSITORY_UNLINK_REQUEST_CONFLICT", "REPOSITORY_ALREADY_UNLINKED", controlapi.CodeConflict, controlapi.CodeInvalidState, controlapi.CodeTaskAlreadyHasActiveRun, controlapi.CodeProjectAlreadyRegistered:
		status = http.StatusConflict
	case controlapi.CodePermissionDenied, controlapi.CodeReviewScopeForbidden:
		status = http.StatusForbidden
	case controlapi.CodeCanceled:
		status = http.StatusRequestTimeout
	}
	return status
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func remoteIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ValidateLoopbackListen rejects wildcard and non-loopback listeners before
// the administration surface can become reachable.
func ValidateLoopbackListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address must use an explicit loopback IP")
	}
	if value, err := strconv.Atoi(port); err != nil || value < 0 || value > 65535 {
		return errors.New("listen port must be between 0 and 65535")
	}
	return nil
}
