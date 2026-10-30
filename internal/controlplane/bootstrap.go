package controlplane

import (
	"context"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/workflowevents"
	"path/filepath"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/classification"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/piagent"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/projectselection"
	"github.com/lazyun/devbridge/internal/recovery"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/repositoryunlink"
	"github.com/lazyun/devbridge/internal/retirement"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewdrift"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/taskplan"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
)

type Config struct {
	DataRoot     string
	Git          bridgegit.Config
	Codex        codex.Config
	CodexRuntime codexruntime.Allowlist
	// PiAgent enables the PI_AGENT review executor. Nil disables the runtime
	// (PI_AGENT reviews fail with PI_AGENT_UNAVAILABLE at dispatch).
	PiAgent *piagent.Config
	// AppServerContinuity opts new operations into the verified reconnectable
	// transport. Existing bound operations are recovered regardless of this flag.
	AppServerContinuity bool
	AppServerSpec       func(codexconfig.Config) appserver.Spec
}

type ControlPlane struct {
	WorkflowEvents           *workflowevents.Service
	RuntimeQualification     *runtimequalification.Service
	Confirmations            *sqlite.ConfirmationStore
	RecoveryPurposeConsumers *sqlite.RecoveryPurposeStore
	UpperReviewAccess        *sqlite.UpperReviewAccessStore
	UpperReviewResults       *sqlite.UpperReviewResultStore
	UpperReviewWorkers       *sqlite.UpperReviewWorkerStore
	WorktreeInventory        *worktreeinventory.Service
	DirectFiles              *directfile.Service
	AgentExecutions          agentexecution.Store
	WorkflowProgression      *sqlite.WorkflowProgressionStore
	ownedRoot                *dataroot.DataRoot
	ExecutionCancellations   *sqlite.ExecutionCancellationStore
	DevelopmentCycles        *developmentcycle.Service
	DataPaths                dataroot.DataPaths
	DB                       *sqlite.DB
	AgentConversations       *sqlite.AgentConversationStore
	Projects                 *project.Service
	ProjectAuthorizations    *project.AuthorizationService
	ProjectRegistration      *projectregistration.Service
	ProjectSelection         *projectselection.Service
	AIProjectSelections      *aiprojectselection.Service
	AISessions               *aisession.Service
	AIContext                *aicontext.Service
	Repositories             *repository.Service
	Modules                  *module.Service
	ProjectContexts          *projectcontext.Service
	ContextProbes            *contextprobe.Service
	ContextProbeRunner       *probeexecution.Runner
	Classifications          *classification.Service
	CodexConfigs             *codexconfig.Service
	Tasks                    *task.Service
	TaskPlans                *taskplan.Service
	FeatureAreas             *featurearea.Service
	Documents                *document.Service
	DecisionResolutions      *decisionresolution.Service
	Reviews                  *review.Service
	ScopeReviews             *scopereview.Service
	ReviewGrants             *reviewgrant.Service
	ScopeExpansions          *scopeexpansion.Service
	ExecutionProposals       *executionproposal.Service
	ExecutionGoals           *taskgoal.Service
	Runs                     *run.Service
	Approvals                *approval.Service
	Events                   *sqlite.EventStore
	Evidence                 *sqlite.EvidenceStore
	Artifacts                *sqlite.ArtifactStore
	Worktrees                *sqlite.WorktreeStore
	Workspaces               *workspace.Service
	Coordinator              *execution.Coordinator
	Retirement               *retirement.Coordinator
	Supervisor               *execution.RuntimeSupervisor
	RecoveryStatus           *execution.RecoveryStatusService
	RepositoryUnlink         *repositoryunlink.Service
	ReviewDrift              *reviewdrift.Service
	CodexRuntime             *codexruntime.Authority
	Ready                    bool
}

func Open(ctx context.Context, config Config) (*ControlPlane, error) {
	opened, err := dataroot.Open(config.DataRoot)
	if err != nil {
		return nil, err
	}
	plane, err := OpenWithDataRoot(ctx, config, opened)
	if err != nil {
		_ = opened.Close()
		return nil, err
	}
	plane.ownedRoot = &opened
	return plane, nil
}

// OpenWithDataRoot borrows bootstrap ownership from the process. The caller
// must keep opened alive until the plane and all other writers have closed.
func OpenWithDataRoot(ctx context.Context, config Config, opened dataroot.DataRoot) (*ControlPlane, error) {
	if !opened.WriterHeld() {
		return nil, errors.New("control plane requires live DataRoot writer ownership")
	}
	if config.DataRoot != "" {
		root, err := filepath.EvalSymlinks(config.DataRoot)
		if err != nil {
			return nil, err
		}
		paths, err := dataroot.Derive(root)
		if err != nil || paths.Root != opened.Paths.Root {
			return nil, errors.New("control plane DataRoot does not match writer ownership")
		}
	}
	paths := opened.Paths
	db, err := sqlite.Open(ctx, paths.State)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*ControlPlane, error) { _ = db.Close(); return nil, err }
	projectStore := sqlite.NewProjectStore(db)
	projectSelectionStore := sqlite.NewProjectSelectionStore(db)
	aiProjectSelectionStore := sqlite.NewAIProjectSelectionStore(db)
	aiSessionStore := sqlite.NewAISessionStore(db)
	repositoryStore := sqlite.NewRepositoryStore(db)
	moduleStore := sqlite.NewModuleStore(db)
	projectContextStore := sqlite.NewProjectContextStore(db)
	contextProbeStore := sqlite.NewContextProbeStore(db)
	codexConfigStore := sqlite.NewCodexConfigStore(db)
	taskStore := sqlite.NewTaskStore(db)
	featureAreaStore := sqlite.NewFeatureAreaStore(db)
	documentStore := sqlite.NewDocumentStore(db)
	reviewStore := sqlite.NewReviewStore(db)
	taskPlanStore := sqlite.NewTaskPlanStore(db)
	scopeExpansionStore := sqlite.NewScopeExpansionStore(db)
	executionProposalStore := sqlite.NewExecutionProposalStore(db)
	decisionResolutionStore := sqlite.NewDecisionResolutionStore(db)
	runStore := sqlite.NewRunStore(db)
	config.Codex.UsageStore = sqlite.NewCodexUsageStore(db)
	runtimeStore := sqlite.NewAgentRuntimeStore(db)
	config.Codex.AgentRuntime = agentruntime.NewService(runtimeStore)
	approvalStore := sqlite.NewApprovalStore(db)
	worktreeStore := sqlite.NewWorktreeStore(db)
	workspaceStore := sqlite.NewWorkspaceStore(db)
	evidenceStore := sqlite.NewEvidenceStore(db)
	artifactStore := sqlite.NewArtifactStore(db)
	artifactFS, err := artifact.NewFilesystem(paths.Root, nil, nil)
	if err != nil {
		return fail(err)
	}
	gitClient := bridgegit.NewClient(config.Git)
	// Resume only exact Recovery-purpose Reviews with no claimed external effect.
	// This must precede generic interrupted-operation classification.
	if err := sqlite.NewReviewStore(db).AdoptUnstartedRecoveryReviewPreparations(ctx); err != nil {
		return fail(err)
	}
	recoveryService := recovery.NewService(sqlite.NewRecoveryStore(db), nil)
	if err := recoveryService.Recover(ctx); err != nil {
		return fail(fmt.Errorf("recover durable control plane: %w", err))
	}
	workflowProgression := sqlite.NewWorkflowProgressionStore(db)
	if err := workflowProgression.Reconcile(ctx); err != nil {
		return fail(err)
	}
	externalWorktrees, err := worktree.NewManager(paths.Worktrees, gitClient)
	if err != nil {
		return fail(err)
	}
	repositories := repository.NewService(repositoryStore, gitClient, nil)
	repositoryUnlink := repositoryunlink.NewService(sqlite.NewRepositoryUnlinkStore(db))
	projects := project.NewService(projectStore, nil, nil)
	projectAuthorizations := project.NewAuthorizationService(sqlite.NewAuthorizationStore(db), nil)
	projectRegistration, err := projectregistration.NewService(projects, repositories, sqlite.NewProjectRegistrationStore(db))
	if err != nil {
		return fail(err)
	}
	developmentCycles := developmentcycle.NewService(sqlite.NewDevelopmentCycleStore(db), projects, repositories, gitClient)
	projectContexts := projectcontext.NewService(projectContextStore, nil)
	if err := contextProbeStore.RecoverRunning(ctx, time.Now().UTC()); err != nil {
		return fail(fmt.Errorf("recover context probes: %w", err))
	}
	if err := projectContexts.RecoverRunning(ctx); err != nil {
		return fail(fmt.Errorf("recover project contexts: %w", err))
	}
	projectSelection := projectselection.NewService(projectSelectionStore, projects, nil)
	aiProjectSelections := aiprojectselection.NewService(aiProjectSelectionStore, projects, nil, nil)
	aiSessions := aisession.NewService(aiSessionStore, projects, nil, nil)
	featureAreas, err := featurearea.NewService(featureAreaStore, aiSessions, taskStore.CountByFeatureArea, nil, nil)
	if err != nil {
		return fail(err)
	}
	documents, err := document.NewService(documentStore, aiSessions, featureAreas, nil, nil)
	if err != nil {
		return fail(err)
	}
	modules := module.NewService(moduleStore, projects, nil, nil)
	codexConfigs := codexconfig.NewService(codexConfigStore, nil, nil)
	runtimeQualification := &runtimequalification.Service{Store: sqlite.NewRuntimePlatformStore(db), Selections: sqlite.NewRuntimeSelectionStore(db), Configurations: codexConfigs, EvidenceRoot: filepath.Join(paths.Root, "platform-evidence"), BaseCodexPath: config.Codex.CodexPath}
	baseResolver := repository.NewCommitResolver(repositoryStore, gitClient)
	tasks := task.NewService(taskStore, baseResolver, modules, codexConfigs, nil, nil)
	reviewGrants, err := reviewgrant.NewService(sqlite.NewReviewGrantStore(db), nil)
	if err != nil {
		return fail(err)
	}
	taskPlans, err := taskplan.NewService(taskPlanStore, aiSessions, tasks, repositories, nil, nil)
	if err != nil {
		return fail(err)
	}
	scopeExpansions, err := scopeexpansion.NewService(scopeExpansionStore, aiSessions, reviewStore, repositories, nil, nil)
	if err != nil {
		return fail(err)
	}
	runs := run.NewService(runStore, nil, nil)
	runs.AgentRuntime = config.Codex.AgentRuntime
	reviews, err := review.NewService(reviewStore, nil, nil)
	if err != nil {
		return fail(err)
	}
	scopeExpansions.SetReviewGrantLookup(reviewGrants)
	classifications, err := classification.NewService(sqlite.NewClassificationStore(db), aiSessions, modules, tasks, runs, nil, nil)
	if err != nil {
		return fail(err)
	}
	workspaces := workspace.NewService(workspaceStore, nil, nil)
	approvals := approval.NewService(approvalStore, nil)
	contextProbes, err := contextprobe.NewService(contextProbeStore, aiSessions, repositories, codexConfigs, projectContexts, nil, nil)
	if err != nil {
		return fail(err)
	}
	contextProbeRunner, err := probeexecution.NewRunner(contextProbes, repositories, codexConfigs, probeexecution.DefaultCodexFactory{Config: config.Codex})
	if err != nil {
		return fail(err)
	}
	contextProbeRunner.AgentRuntime = config.Codex.AgentRuntime
	aiContext, err := aicontext.NewService(aiSessions, aiProjectSelections, projects, repositories, modules, tasks, codexConfigs, projectContexts)
	if err != nil {
		return fail(err)
	}
	contextBootstrapper, err := projectcontext.NewBootstrapper(projectContexts, repositories, modules)
	if err != nil {
		return fail(err)
	}
	aiContext.SetProjectContextBootstrapper(contextBootstrapper)
	events := sqlite.NewEventStore(db)
	reviewInspector, err := reviewaccess.NewInspector(gitClient)
	if err != nil {
		return fail(err)
	}
	agentConversations := sqlite.NewAgentConversationStore(db)
	executionCancellations := sqlite.NewExecutionCancellationStore(db)
	contextProbeRunner.Cancellations = executionCancellations
	contextProbeRunner.Canonical = &probeexecution.CanonicalRunner{
		Store:        sqlite.NewInspectionRuntimeStore(db),
		Materializer: probeexecution.ManagedInspectionMaterializer{Workspaces: workspaces, Worktrees: &worktree.DurableManager{External: externalWorktrees, Store: worktreeStore}, Repositories: repositories},
		AppServers:   runtimeStore, Configs: codexConfigs, AgentRuntime: config.Codex.AgentRuntime, Cancellations: executionCancellations, Config: config.Codex, DataRoot: paths.Root, AppServerSpec: config.AppServerSpec,
	}
	reviewCapabilities := &execution.ReviewCapabilityDispatcher{Grants: reviewGrants, Conversations: agentConversations, Runs: runs, Reviews: reviews, Repositories: repositories, Worktrees: worktreeStore, Inspector: reviewInspector}
	upperAccess := sqlite.NewUpperReviewAccessStore(db)
	coordinator := &execution.Coordinator{QualifiedRuntime: runtimeQualification, Handoffs: executionProposalStore, UpperReviews: upperAccess, Cancellations: executionCancellations, Conversations: agentConversations, ProjectAuthorizations: projectAuthorizations, Repositories: repositories, CodexConfigs: codexConfigs, Tasks: tasks, Runs: runs, Worktrees: &worktree.DurableManager{External: externalWorktrees, Store: worktreeStore}, Workspaces: workspaces, Codex: execution.DefaultCodexFactory{Config: config.Codex, DynamicToolHandler: reviewCapabilities.Handle}, Evidence: evidence.NewCollector(gitClient), Results: &execution.DurableResultRecorder{Filesystem: artifactFS, Artifacts: artifactStore, Evidence: evidenceStore}, Approvals: approvals, Diagnostics: events, Reviews: reviews}
	supervisor, err := execution.NewRuntimeSupervisor(coordinator)
	if err != nil {
		return fail(fmt.Errorf("construct runtime supervisor: %w", err))
	}
	executionProposals, err := executionproposal.NewService(executionProposalStore, aiSessions, taskPlans, tasks, supervisor, nil, nil, nil)
	if err != nil {
		return fail(fmt.Errorf("construct execution proposal service: %w", err))
	}
	decisionResolutions, err := decisionresolution.NewService(decisionResolutionStore, aiSessions)
	if err != nil {
		return fail(fmt.Errorf("construct Decision resolution service: %w", err))
	}
	executionGoalService := taskgoal.NewService(taskStore, &taskgoal.ExecutionAdapter{Proposals: executionProposalStore}, taskStore, nil)
	goalCapabilities := &execution.GoalCapabilityDispatcher{Conversations: agentConversations, Runs: runs, Goals: executionGoalService}
	dynamicToolHandler := func(ctx context.Context, call codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
		if call.Tool == execution.GoalProgressTool {
			return goalCapabilities.Handle(ctx, call)
		}
		return reviewCapabilities.Handle(ctx, call)
	}
	coordinator.Goals = executionGoalService
	coordinator.Executions = &taskgoal.ExecutionAdapter{Proposals: executionProposalStore}
	coordinator.AppServers = runtimeStore
	coordinator.Codex = execution.DefaultCodexFactory{Config: config.Codex, DynamicToolHandler: dynamicToolHandler, AppServers: runtimeStore, AppServerSpec: config.AppServerSpec}
	if config.AppServerContinuity {
		coordinator.AppServers = runtimeStore
		coordinator.Codex = execution.DefaultCodexFactory{Config: config.Codex, DynamicToolHandler: dynamicToolHandler, ContinuityDirectory: paths.Root, AppServers: runtimeStore, AppServerSpec: config.AppServerSpec}
	}
	// Initialize PI Agent factory when the runtime is enabled.
	if config.PiAgent != nil {
		coordinator.PiAgent = &piagent.Factory{PiPath: piagent.Resolve(config.PiAgent.PiPath), Model: config.PiAgent.Model, ThinkingLevel: config.PiAgent.ThinkingLevel, SessionDir: config.PiAgent.SessionDir, ExtraArgs: config.PiAgent.ExtraArgs}
	}
	// Register only factories implementing the separate neutral launch seam.
	// No native Codex launch is silently repurposed as cooperative policy.
	var agentEntries []agent.Registration[agent.Factory]
	for _, configured := range []any{coordinator.Codex, coordinator.PiAgent} {
		if factory, ok := configured.(agent.Factory); ok {
			agentEntries = append(agentEntries, agent.Registration[agent.Factory]{Descriptor: factory.AgentDescriptor(), Value: factory})
		}
	}
	coordinator.Agents, err = agent.NewRegistry(agentEntries...)
	if err != nil {
		return fail(fmt.Errorf("register execution agents: %w", err))
	}
	reviewDrift, err := reviewdrift.New(reviews, reviewStore, repositories, gitClient)
	if err != nil {
		return fail(fmt.Errorf("construct review drift service: %w", err))
	}
	scopeReviews, err := scopereview.NewService(aiSessions, repositories, runs, supervisor, reviewStore)
	if err != nil {
		return fail(err)
	}
	scopeReviews.SetReviews(reviews)
	scopeReviews.SetAuthorizedStartQueue(reviewStore)
	scopeReviews.SetUpperStarter(coordinator)
	scopeReviews.SetInitialGrantService(reviewGrants)
	runtimeInventory, err := codexruntime.NewAuthority(config.CodexRuntime)
	if err != nil {
		return fail(fmt.Errorf("configure Codex runtime inventory: %w", err))
	}
	runtimeInventory.SetAgentConfigFingerprintProvider(func() map[string]string {
		fingerprints := make(map[string]string)
		for _, entry := range agentEntries {
			descriptor := entry.Value.AgentDescriptor()
			if descriptor.ID == entry.Descriptor.ID && descriptor.ConfigFingerprint != "" {
				fingerprints[descriptor.ID] = descriptor.ConfigFingerprint
			}
		}
		return fingerprints
	})
	workflowProgression.SetRuntimeConfigProvider(runtimeInventory.ExecutionAllowlist)
	coordinator.RuntimeConfigProvider = runtimeInventory.ExecutionAllowlist
	directFileAuthority := sqlite.NewDirectFileAuthority(db)
	directFileAuthority.SetTaskWorkspacePreparer(coordinator.PrepareApprovedFileWorkspace)
	// Continue only durable approval intents; model reads never initialize grants.
	fileContinuation := func(ctx context.Context) {
		if err := directFileAuthority.ReconcilePendingTaskAuthorizations(ctx); err != nil {
			observability.Error(ctx, "directfile", "directfile.preparation.failed", "Approved file authorization preparation could not converge", map[string]any{"errorSummary": err.Error()})
		}
	}
	purposeConsumers := sqlite.NewRecoveryPurposeStore(db)
	retireCoordinator := retirement.Start(featureAreaStore, supervisor, executionProposals.ApprovedContinuation(), scopeReviews.AuthorizedContinuation(), fileContinuation, recoveryPurposeContinuation(purposeConsumers))
	featureAreas.SetRetirementWake(retireCoordinator.Wake)
	workflowProgression.SetApprovalWake(retireCoordinator.Wake)
	supervisor.SetTerminalWake(retireCoordinator.Wake)
	recoveryStatus := &execution.RecoveryStatusService{Store: sqlite.NewRecoveryStatusStore(db), Runs: runs, Coordinator: coordinator, AppServers: runtimeStore, Config: config.Codex, Supervisor: supervisor, InspectionOwner: contextProbeRunner.Canonical, OwnerFacts: coordinator.ValidateRecoveryOwnerFacts}
	if err := recoveryStatus.RecoverAll(ctx); err != nil {
		recoveryStatus.Close()
		retireCoordinator.Close()
		_ = supervisor.Shutdown()
		return fail(err)
	}
	if err := recoverAppServerRuns(ctx, paths.Root, runtimeStore, runs, supervisor, executionProposals, sqlite.NewRecoveryStatusStore(db)); err != nil {
		recoveryStatus.Close()
		retireCoordinator.Close()
		_ = supervisor.Shutdown()
		return fail(err)
	}
	if err := contextProbeRunner.RecoverAll(ctx); err != nil {
		contextProbeRunner.Shutdown()
		recoveryStatus.Close()
		retireCoordinator.Close()
		_ = supervisor.Shutdown()
		return fail(err)
	}
	eventService := workflowevents.New(sqlite.NewWorkflowEventStore(db), func(ctx context.Context, in workflowevents.Arguments) (string, error) {
		owner, err := aiSessions.GetOwned(ctx, in.AISessionID)
		if err != nil {
			return "", err
		}
		value, err := tasks.Get(ctx, in.TaskID)
		if err != nil || value.ProjectID != owner.ProjectID {
			return "", errors.New("task subscription unavailable")
		}
		return owner.ProjectID, nil
	})
	eventService.Start(context.Background())
	return &ControlPlane{WorkflowEvents: eventService, RuntimeQualification: runtimeQualification, RecoveryStatus: recoveryStatus, RepositoryUnlink: repositoryUnlink, Confirmations: sqlite.NewConfirmationStore(db), RecoveryPurposeConsumers: purposeConsumers, UpperReviewAccess: upperAccess, UpperReviewResults: sqlite.NewUpperReviewResultStore(db), UpperReviewWorkers: sqlite.NewUpperReviewWorkerStore(db), WorktreeInventory: &worktreeinventory.Service{Dependencies: worktreeinventory.Dependencies{Repositories: repositories, Worktrees: worktreeStore, Workspaces: workspaces, Runs: runs, Conversations: agentConversations, DevelopmentCycles: sqlite.NewDevelopmentCycleStore(db), Git: gitClient}}, DirectFiles: directfile.NewService(directFileAuthority), AgentExecutions: sqlite.NewAgentExecutionStore(db), WorkflowProgression: workflowProgression, Retirement: retireCoordinator, ExecutionCancellations: executionCancellations, DevelopmentCycles: developmentCycles, DataPaths: paths, DB: db, AgentConversations: agentConversations, Projects: projects, ProjectAuthorizations: projectAuthorizations, ProjectRegistration: projectRegistration, ProjectSelection: projectSelection, AIProjectSelections: aiProjectSelections, AISessions: aiSessions, AIContext: aiContext, Repositories: repositories, Modules: modules, ProjectContexts: projectContexts, ContextProbes: contextProbes, ContextProbeRunner: contextProbeRunner, Classifications: classifications, CodexConfigs: codexConfigs, FeatureAreas: featureAreas, Documents: documents, DecisionResolutions: decisionResolutions, Reviews: reviews, Tasks: tasks, TaskPlans: taskPlans, ScopeReviews: scopeReviews, ReviewGrants: reviewGrants, ScopeExpansions: scopeExpansions, ExecutionProposals: executionProposals, ExecutionGoals: executionGoalService, Runs: runs, Approvals: approvals, Events: events, Evidence: evidenceStore, Artifacts: artifactStore, Worktrees: worktreeStore, Workspaces: workspaces, Coordinator: coordinator, Supervisor: supervisor, ReviewDrift: reviewDrift, CodexRuntime: runtimeInventory, Ready: true}, nil
}

func (c *ControlPlane) Close() error {
	if c == nil || c.DB == nil {
		return nil
	}
	c.Ready = false
	if c.WorkflowEvents != nil {
		c.WorkflowEvents.Close()
	}
	if c.RecoveryStatus != nil {
		c.RecoveryStatus.Close()
	}
	if c.Retirement != nil {
		c.Retirement.Close()
	}
	if c.ContextProbeRunner != nil {
		c.ContextProbeRunner.Shutdown()
	}
	var shutdownErr error
	if c.Supervisor != nil {
		shutdownErr = c.Supervisor.Shutdown()
	}
	err := errors.Join(shutdownErr, c.DB.Close())
	if c.ownedRoot != nil {
		err = errors.Join(err, c.ownedRoot.Close())
	}
	return err
}
