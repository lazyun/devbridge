package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxHTTPResponseBytes = 256 << 10

type RealAdapter struct {
	BridgePath   string
	MCPPath      string
	CodexPath    string
	PollInterval time.Duration
	HTTPClient   *http.Client

	mu          sync.Mutex
	provisioned map[string]provisionWire
	gates       map[GateID]*realGateState
}

type realGateState struct {
	address    string
	dataRoot   string
	runID      string
	taskID     string
	taskPlanID string
	approvalID string
}

type provisionWire struct {
	ProjectID     string `json:"project_id"`
	RepositoryID  string `json:"repository_id"`
	ModuleID      string `json:"module_id"`
	CodexConfigID string `json:"codex_config_id"`
	AISessionID   string `json:"ai_session_id"`
}

type taskWire struct {
	ID     string
	Status string
}

type taskPlanWire struct {
	ID     string
	Status string
}

type repositorySearchWire struct {
	Matches []struct {
		Path    string
		Line    int
		Snippet string
	}
	Truncated bool
}

type consoleRunPageWire struct {
	Items []struct {
		ID              string `json:"id"`
		TaskID          string `json:"task_id"`
		Purpose         string `json:"purpose"`
		PromptAvailable bool   `json:"prompt_available"`
	} `json:"items"`
}

type consoleTaskPageWire struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
}

type executionProposalWire struct {
	ID         string
	TaskID     string
	TaskPlanID string
	Status     string
}

type taskPlanContinuationWire struct {
	TaskPlan          taskPlanWire
	Task              taskWire
	ExecutionProposal executionProposalWire
}

type executionStartWire struct {
	ExecutionProposal executionProposalDetailWire
	Run               struct {
		ID     string
		TaskID string
	}
}

type executionProposalDetailWire struct {
	ID                  string
	TaskID              string
	ImplementationRunID string
	VerificationRunID   string
	Outcome             string
	WorkSpec            struct {
		Goal             string `json:"goal"`
		CodexInstruction string `json:"codexInstruction"`
		ExecutionScope   struct {
			Kind  string   `json:"kind"`
			Paths []string `json:"paths"`
		} `json:"executionScope"`
	}
	VerificationSpec struct {
		Mode   string `json:"mode"`
		Scope  string `json:"scope"`
		Checks []struct {
			Kind     string `json:"kind"`
			Command  string `json:"command"`
			Required bool   `json:"required"`
		} `json:"checks"`
		Waiver *struct {
			Source string `json:"source"`
			Reason string `json:"reason"`
		} `json:"waiver"`
	}
}

type externalProjectListWire struct {
	Projects []struct {
		ProjectID string `json:"projectId"`
	} `json:"projects"`
}

type externalProjectOpenWire struct {
	AISessionID  string                    `json:"aiSessionId"`
	CurrentState externalWorkflowStateWire `json:"currentState"`
}

type externalProjectContextWire struct {
	Guidance string `json:"guidance"`
	Status   string `json:"status"`
	Modules  []struct {
		Key             string   `json:"key"`
		Name            string   `json:"name"`
		Responsibility  string   `json:"responsibility"`
		Repository      string   `json:"repository"`
		ProductionRoots []string `json:"productionRoots"`
		Entrypoints     []string `json:"entrypoints"`
		RelatedModules  []string `json:"relatedModules"`
	} `json:"modules"`
}

func moduleMapHasRoot(value externalProjectContextWire, wanted string) bool {
	for _, item := range value.Modules {
		for _, root := range item.ProductionRoots {
			if root == wanted {
				return true
			}
		}
	}
	return false
}

func moduleMapOmitsTestPaths(value externalProjectContextWire) bool {
	for _, item := range value.Modules {
		for _, path := range append(append([]string{}, item.ProductionRoots...), item.Entrypoints...) {
			if strings.Contains(strings.ToLower(filepath.ToSlash(path)), "tests/") || strings.Contains(strings.ToLower(path), "_test.") {
				return false
			}
		}
	}
	return true
}

type externalWorkflowStateWire struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
	Current struct {
		Project struct {
			Context externalProjectContextWire `json:"context"`
		} `json:"project"`
		Topic *struct {
			ID           string `json:"id"`
			ReviewStatus string `json:"reviewStatus"`
		} `json:"featureArea"`
		Task *struct {
			ID                   string `json:"id"`
			Status               string `json:"status"`
			ConfirmationRequired bool   `json:"confirmationRequired"`
		} `json:"task"`
	} `json:"current"`
	PendingDecision *struct {
		DecisionID   string `json:"decisionId"`
		DecisionKind string `json:"decisionKind"`
		SubjectKind  string `json:"subjectKind"`
		SubjectID    string `json:"subjectId"`
	} `json:"pendingDecision"`
	AvailableFeatureAreas []externalTopicWire `json:"availableFeatureAreas"`
	Capabilities          struct {
		CanCreateFeatureArea         bool `json:"canCreateFeatureArea"`
		CanStartReview               bool `json:"canStartReview"`
		CanCreateTask                bool `json:"canCreateTask"`
		CanRequestAnotherReview      bool `json:"canRequestAnotherReview"`
		CanResolveDecision           bool `json:"canResolveDecision"`
		HasPendingDecision           bool `json:"hasPendingDecision"`
		DecisionInteractionAvailable bool `json:"decisionInteractionAvailable"`
	} `json:"capabilities"`
}

type uiWorkflowStateWire struct {
	Continuation *struct {
		Attempt       bool   `json:"attempt"`
		Kind          string `json:"kind"`
		FeatureAreaID string `json:"featureAreaId"`
		ReviewID      string `json:"reviewId"`
		TaskID        string `json:"taskId"`
	} `json:"continuation"`
	TaskPlan *struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"taskPlan"`
}

type externalTopicWire struct {
	ID string `json:"id"`
}

type externalTopicDetailsWire struct {
	Topic struct {
		ID string `json:"id"`
	} `json:"featureArea"`
	Tasks []json.RawMessage `json:"tasks"`
}

type externalTopicListWire struct {
	FeatureAreas []externalTopicWire `json:"featureAreas"`
}

type externalReviewWire struct {
	Review struct {
		ID                  string            `json:"id"`
		RequestID           string            `json:"requestId"`
		ProjectID           string            `json:"projectId"`
		RepositoryID        string            `json:"repositoryId"`
		RunID               string            `json:"runId"`
		OperationKind       string            `json:"operationKind"`
		Topic               externalTopicWire `json:"featureArea"`
		State               string            `json:"state"`
		Summary             string            `json:"summary"`
		ResultAvailable     bool              `json:"resultAvailable"`
		CompletedTransition bool              `json:"completedTransition"`
		Usage               struct {
			Status string `json:"status"`
		} `json:"usage"`
		Focus struct {
			Mode              string   `json:"mode"`
			PrimaryModules    []string `json:"primaryModules"`
			SupportingModules []string `json:"supportingModules"`
			ProductionRoots   []string `json:"productionRoots"`
		} `json:"focus"`
	} `json:"review"`
	Result *struct {
		EngineeringReview string `json:"engineeringReview"`
		AgentClaim        struct {
			Available bool   `json:"available"`
			Message   string `json:"message"`
		} `json:"agentClaim"`
	} `json:"result"`
	ObservedEvidence struct {
		AuthorizationStatus          string `json:"authorizationStatus"`
		RepositoryInspectionObserved bool   `json:"repositoryInspectionObserved"`
		ObservationBoundary          string `json:"observationBoundary"`
		SuccessfulHostToolOperations int    `json:"successfulHostToolOperations"`
	} `json:"observedEvidence"`
	RepositoryProvenance *struct {
		ReviewBaseline              string `json:"reviewBaseline"`
		RepositoryHEADAtAdmission   string `json:"repositoryHEADAtAdmission"`
		BranchKind                  string `json:"branchKind"`
		BranchRef                   string `json:"branchRef"`
		WorkingTreeDirtyAtAdmission *bool  `json:"workingTreeDirtyAtAdmission"`
		InspectionMode              string `json:"inspectionMode"`
	} `json:"repositoryProvenance"`
}

type reviewPromptDiagnosticWire struct {
	ReviewID  string `json:"review_id"`
	Available bool   `json:"available"`
	Prompt    string `json:"prompt"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type runPromptDiagnosticWire struct {
	RunID     string `json:"run_id"`
	Purpose   string `json:"purpose"`
	Available bool   `json:"available"`
	Prompt    string `json:"prompt"`
	SHA256    string `json:"sha256"`
	Bytes     int64  `json:"bytes"`
}

type externalDocumentWire struct {
	ID            string `json:"id"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId"`
	Title         string `json:"title"`
	Content       string `json:"content"`
}

type externalTaskWire struct {
	ID                   string `json:"id"`
	FeatureAreaID        string `json:"featureAreaId"`
	Status               string `json:"status"`
	ConfirmationRequired bool   `json:"confirmationRequired"`
	Diagnostics          struct {
		InternalTaskID string `json:"internalTaskId"`
	} `json:"diagnostics"`
}

type scopeExpansionWire struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	DecisionRequired bool   `json:"decisionRequired"`
	CurrentScope     struct {
		Kind  string   `json:"kind"`
		Paths []string `json:"paths"`
	} `json:"currentScope"`
	AdditionalScope struct {
		Kind  string   `json:"kind"`
		Paths []string `json:"paths"`
	} `json:"additionalScope"`
}

type externalExecutionWire struct {
	ID           string            `json:"id"`
	Status       string            `json:"status"`
	ChangedFiles []string          `json:"changedFiles"`
	Evidence     []json.RawMessage `json:"evidence"`
	AgentClaim   struct {
		Available bool   `json:"available"`
		Message   string `json:"message"`
	} `json:"agentClaim"`
}

type taskStatusWire struct {
	ID     string
	Status string
}

type approvalWire struct {
	ID           string `json:"id"`
	RunID        string `json:"run_id"`
	ThreadID     string `json:"thread_id"`
	Kind         string `json:"kind"`
	Status       string `json:"status"`
	DecisionType string `json:"decision_type"`
}

type approvalDetailWire struct {
	ID     string
	Kind   string
	Status string
}

type eventWire struct {
	Sequence     int64     `json:"sequence"`
	Type         string    `json:"type"`
	To           string    `json:"to"`
	DecisionType string    `json:"decision_type"`
	CreatedAt    time.Time `json:"created_at"`
}

type runObservationWire struct {
	RunID            string     `json:"run_id"`
	TaskID           string     `json:"task_id"`
	Purpose          string     `json:"purpose"`
	ExecutionID      string     `json:"execution_proposal_id"`
	WorkspaceID      string     `json:"workspace_id"`
	Status           string     `json:"status"`
	FailureCode      string     `json:"failure_code"`
	FailureMessage   string     `json:"failure_message"`
	WorktreeID       string     `json:"worktree_id"`
	WorktreePath     string     `json:"worktree_path"`
	WorktreeBranch   string     `json:"worktree_branch"`
	ThreadID         string     `json:"thread_id"`
	TurnID           string     `json:"turn_id"`
	StartSnapshot    string     `json:"start_snapshot"`
	EndSnapshot      string     `json:"end_snapshot"`
	LiveSession      bool       `json:"live_session"`
	StartedAt        *time.Time `json:"started_at"`
	FinishedAt       *time.Time `json:"finished_at"`
	TokenUsageStatus string     `json:"token_usage_status"`
	TokenUsage       *struct {
		CachedInputTokens int64 `json:"CachedInputTokens"`
		TotalTokens       int64 `json:"TotalTokens"`
	} `json:"token_usage"`
	VerificationResult *struct {
		Status string `json:"Status"`
		Checks []struct {
			Index           int    `json:"Index"`
			Kind            string `json:"Kind"`
			Required        bool   `json:"Required"`
			Status          string `json:"Status"`
			ObservedCommand string `json:"ObservedCommand"`
			ExitCode        *int   `json:"ExitCode"`
			FailureReason   string `json:"FailureReason"`
		} `json:"Checks"`
	} `json:"verification_result"`
	BaseCommit            string         `json:"base_commit"`
	HeadCommit            string         `json:"head_commit"`
	Dirty                 bool           `json:"dirty"`
	ChangedFiles          []string       `json:"changed_files"`
	ChangedFilesTruncated bool           `json:"changed_files_truncated"`
	DiffArtifactID        string         `json:"diff_artifact_id"`
	Diff                  string         `json:"diff"`
	DiffTruncated         bool           `json:"diff_truncated"`
	AgentClaim            string         `json:"agent_claim"`
	Approvals             []approvalWire `json:"approvals"`
	Events                []eventWire    `json:"events"`
	RecoveryEventCount    int            `json:"recovery_event_count"`
}

type httpAPIError struct {
	Status  int
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *httpAPIError) Error() string {
	return fmt.Sprintf("Bridge HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func (a *RealAdapter) Name() string { return "real-loopback-http" }

func (a *RealAdapter) NormalExecution(ctx context.Context, request GateRequest) (NormalObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-normal")
	if err != nil {
		return NormalObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return NormalObservation{}, err
	}
	goal := fmt.Sprintf("Replace the complete contents of %s with exactly the following single line, including its trailing newline:\n%sDo not change any other file and do not create a commit.", request.ExpectedPath, request.ExpectedContent)
	started, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, goal)
	if err != nil {
		return NormalObservation{}, err
	}
	state.runID, state.taskID, state.taskPlanID = started.runID, started.taskID, started.taskPlanID
	observed, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return NormalObservation{}, err
	}
	if observed.Status != "SUCCEEDED" {
		return NormalObservation{}, fmt.Errorf("normal Run ended as %s (%s)", observed.Status, observed.FailureCode)
	}
	if observed.DiffTruncated || observed.ChangedFilesTruncated {
		return NormalObservation{}, errors.New("normal Run evidence exceeded a bounded observation limit")
	}
	return NormalObservation{
		RunID: observed.RunID, ThreadID: observed.ThreadID, WorktreePath: observed.WorktreePath,
		Timeline: checkpoints(observed), AgentClaim: observed.AgentClaim,
		BaseCommit: observed.BaseCommit, HeadCommit: observed.HeadCommit, Dirty: observed.Dirty,
		ChangedFiles: observed.ChangedFiles, Diff: observed.Diff, DiffArtifactID: observed.DiffArtifactID,
		TokenStatus: observed.TokenUsageStatus,
	}, nil
}

func (a *RealAdapter) RuntimeApproval(ctx context.Context, request GateRequest, decision string) (ApprovalObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-approval")
	if err != nil {
		return ApprovalObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return ApprovalObservation{}, err
	}
	started, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, approvalGoal())
	if err != nil {
		return ApprovalObservation{}, err
	}
	state.runID, state.taskID, state.taskPlanID = started.runID, started.taskID, started.taskPlanID
	waiting, err := a.waitObservation(ctx, state, hasPendingApproval)
	if err != nil {
		return ApprovalObservation{}, err
	}
	pending := firstPending(waiting)
	state.approvalID = pending.ID
	var delivered approvalWire
	err = a.post(ctx, state, "/v1/approvals/"+url.PathEscape(pending.ID)+"/decide", map[string]any{
		"ai_session_id": fixture.AISessionID,
		"decision":      map[string]string{"type": decision},
	}, &delivered)
	if err != nil {
		return ApprovalObservation{}, err
	}
	observed, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return ApprovalObservation{}, err
	}
	return ApprovalObservation{RunID: observed.RunID, ApprovalID: pending.ID, CorrelatedRunID: pending.RunID, Decision: decision, FinalStatus: observed.Status, Timeline: checkpoints(observed)}, nil
}

func (a *RealAdapter) ActiveInterruptionCommand(_ context.Context, request GateRequest) (CommandSpec, error) {
	state, err := a.newGateState(request)
	if err != nil {
		return CommandSpec{}, err
	}
	return a.command(request, state), nil
}

func (a *RealAdapter) WaitUntilActive(ctx context.Context, request GateRequest) error {
	state, err := a.gate(request.GateID)
	if err != nil {
		return err
	}
	if err := a.waitReady(ctx, state); err != nil {
		return err
	}
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return err
	}
	started, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, "Run exactly the command `sleep 300` in the current working tree and wait for it to finish. Do not perform any other work.")
	if err != nil {
		return err
	}
	state.runID, state.taskID, state.taskPlanID = started.runID, started.taskID, started.taskPlanID
	_, err = a.waitObservation(ctx, state, func(value runObservationWire) bool {
		return value.Status == "RUNNING" && value.WorktreePath != "" && value.ThreadID != "" && value.LiveSession
	})
	return err
}

func (a *RealAdapter) RestartCommand(_ context.Context, request GateRequest) (CommandSpec, error) {
	state, err := a.gate(request.GateID)
	if err != nil {
		return CommandSpec{}, err
	}
	return a.command(request, state), nil
}

func (a *RealAdapter) ObserveActiveRecovery(ctx context.Context, request GateRequest, _ *ChildProcess) (RecoveryObservation, error) {
	return a.observeRecovery(ctx, request, false)
}

func (a *RealAdapter) ApprovalInterruptionCommand(_ context.Context, request GateRequest) (CommandSpec, error) {
	state, err := a.newGateState(request)
	if err != nil {
		return CommandSpec{}, err
	}
	return a.command(request, state), nil
}

func (a *RealAdapter) WaitUntilWaitingApproval(ctx context.Context, request GateRequest) error {
	state, err := a.gate(request.GateID)
	if err != nil {
		return err
	}
	if err := a.waitReady(ctx, state); err != nil {
		return err
	}
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return err
	}
	started, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, approvalGoal())
	if err != nil {
		return err
	}
	state.runID, state.taskID, state.taskPlanID = started.runID, started.taskID, started.taskPlanID
	observed, err := a.waitObservation(ctx, state, hasPendingApproval)
	if err == nil {
		state.approvalID = firstPending(observed).ID
	}
	return err
}

func (a *RealAdapter) ObserveApprovalRecovery(ctx context.Context, request GateRequest, _ *ChildProcess) (RecoveryObservation, error) {
	return a.observeRecovery(ctx, request, true)
}

func (a *RealAdapter) ParallelIsolation(ctx context.Context, request GateRequest) (ParallelObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-parallel")
	if err != nil {
		return ParallelObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return ParallelObservation{}, err
	}
	runA, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, approvalGoal())
	if err != nil {
		return ParallelObservation{}, err
	}
	state.runID, state.taskID, state.taskPlanID = runA.runID, runA.taskID, runA.taskPlanID
	waitingA, err := a.waitObservation(ctx, state, hasPendingApproval)
	if err != nil {
		return ParallelObservation{}, err
	}
	pendingA := firstPending(waitingA)

	goalB := fmt.Sprintf("Replace the complete contents of %s with exactly the following single line, including its trailing newline:\n%sDo not change any other file and do not create a commit.", request.ExpectedPath, request.ExpectedContent)
	runB, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, goalB)
	if err != nil {
		return ParallelObservation{}, err
	}
	stateB := &realGateState{address: state.address, dataRoot: state.dataRoot, runID: runB.runID, taskID: runB.taskID, taskPlanID: runB.taskPlanID}
	activeB, err := a.waitObservation(ctx, stateB, func(value runObservationWire) bool {
		return value.WorktreePath != "" && value.ThreadID != "" && (value.Status == "RUNNING" || value.Status == "COLLECTING" || terminalStatus(value.Status))
	})
	if err != nil {
		return ParallelObservation{}, err
	}
	stillWaitingA, err := a.observe(ctx, state)
	if err != nil {
		return ParallelObservation{}, err
	}

	rejectionCode, err := a.startSecondExecution(ctx, state, fixture, runA.taskPlanID)
	if err != nil {
		return ParallelObservation{}, err
	}
	finishedB, err := a.waitObservation(ctx, stateB, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return ParallelObservation{}, err
	}
	if err := a.post(ctx, state, "/v1/approvals/"+url.PathEscape(pendingA.ID)+"/decide", map[string]any{
		"ai_session_id": fixture.AISessionID,
		"decision":      map[string]string{"type": "DECLINE"},
	}, &approvalWire{}); err != nil {
		return ParallelObservation{}, err
	}
	finishedA, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return ParallelObservation{}, err
	}
	return ParallelObservation{
		RunAID: runA.runID, RunBID: runB.runID, TaskAID: runA.taskID, TaskBID: runB.taskID,
		WorktreeA: waitingA.WorktreePath, WorktreeB: activeB.WorktreePath,
		ThreadA: waitingA.ThreadID, ThreadB: activeB.ThreadID,
		RunAStatusWhileBActive: stillWaitingA.Status, RunAFinalStatus: finishedA.Status, RunBFinalStatus: finishedB.Status,
		SameTaskRejectionCode: rejectionCode, RunATokenStatus: finishedA.TokenUsageStatus,
		RunBTokenStatus: finishedB.TokenUsageStatus, RunAChangedFiles: finishedA.ChangedFiles,
		RunBChangedFiles: finishedB.ChangedFiles,
	}, nil
}

func (a *RealAdapter) WorkspaceContinuity(ctx context.Context, request GateRequest) (ContinuityObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-continuity-run-one")
	if err != nil {
		return ContinuityObservation{}, err
	}
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		stopChild(child)
		return ContinuityObservation{}, err
	}
	firstGoal := fmt.Sprintf("Replace the complete contents of %s with exactly the following single line, including its trailing newline:\n%sDo not change any other file and do not create a commit.", request.ExpectedPath, request.ExpectedContent)
	first, err := a.startTask(ctx, request, state, fixture, request.BaseCommit, firstGoal)
	if err != nil {
		stopChild(child)
		return ContinuityObservation{}, err
	}
	state.runID, state.taskID, state.taskPlanID = first.runID, first.taskID, first.taskPlanID
	observedOne, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		stopChild(child)
		return ContinuityObservation{}, err
	}
	if observedOne.Status != "SUCCEEDED" {
		stopChild(child)
		return ContinuityObservation{}, fmt.Errorf("continuity Run 1 ended as %s (%s)", observedOne.Status, observedOne.FailureCode)
	}

	// Force the second Run through cold durable state, including Codex's
	// persisted thread rollout and the Workspace token baseline.
	stopChild(child)
	restarted, err := StartChild(a.command(request, state), request.ArtifactDir, "bridge-continuity-run-two")
	if err != nil {
		return ContinuityObservation{}, err
	}
	defer stopChild(restarted)
	if err := a.waitReady(ctx, state); err != nil {
		return ContinuityObservation{}, err
	}
	secondPrepared, err := a.prepareRepeatedExecution(ctx, state, fixture, first.taskID, first.taskPlanID)
	if err != nil {
		return ContinuityObservation{}, err
	}
	secondRunID, err := a.startConfirmedExecution(ctx, request, state, fixture, secondPrepared.executionProposal)
	if err != nil {
		return ContinuityObservation{}, err
	}
	second := startedWorkflow{taskPlanID: secondPrepared.taskPlanID, taskID: secondPrepared.taskID, runID: secondRunID}
	// M5.1.3 rejects the competing active Execution at durable proposal
	// creation, before it can become a second pending authorization or Run.
	_, competingErr := a.prepareRepeatedExecution(ctx, state, fixture, first.taskID, first.taskPlanID)
	var activeErr *httpAPIError
	if !errors.As(competingErr, &activeErr) || activeErr.Code != "TASK_ALREADY_HAS_ACTIVE_RUN" {
		if competingErr == nil {
			return ContinuityObservation{}, errors.New("competing same-Task continuation unexpectedly started")
		}
		return ContinuityObservation{}, fmt.Errorf("competing same-Task continuation error: %w", competingErr)
	}
	if second.taskID != first.taskID {
		return ContinuityObservation{}, fmt.Errorf("continuation changed Task identity from %s to %s", first.taskID, second.taskID)
	}
	state.runID, state.taskID, state.taskPlanID = second.runID, second.taskID, second.taskPlanID
	observedTwo, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return ContinuityObservation{}, err
	}
	if observedTwo.Status != "SUCCEEDED" {
		return ContinuityObservation{}, fmt.Errorf("continuity Run 2 ended as %s (%s)", observedTwo.Status, observedTwo.FailureCode)
	}
	return ContinuityObservation{
		TaskID: first.taskID, RunOneID: first.runID, RunTwoID: second.runID,
		WorkspaceOne: observedOne.WorkspaceID, WorkspaceTwo: observedTwo.WorkspaceID,
		WorktreeOne: observedOne.WorktreeID, WorktreeTwo: observedTwo.WorktreeID,
		WorktreePathOne: observedOne.WorktreePath, WorktreePathTwo: observedTwo.WorktreePath,
		ThreadOne: observedOne.ThreadID, ThreadTwo: observedTwo.ThreadID,
		TurnOne: observedOne.TurnID, TurnTwo: observedTwo.TurnID,
		StartSnapshotOne: observedOne.StartSnapshot, EndSnapshotOne: observedOne.EndSnapshot,
		StartSnapshotTwo: observedTwo.StartSnapshot, EndSnapshotTwo: observedTwo.EndSnapshot,
		RunOneStatus: observedOne.Status, RunTwoStatus: observedTwo.Status,
		RunOneTokenStatus: observedOne.TokenUsageStatus, RunTwoTokenStatus: observedTwo.TokenUsageStatus,
		RunOneTokenTotal: tokenTotal(observedOne), RunTwoTokenTotal: tokenTotal(observedTwo),
		RunOneChangedFiles: observedOne.ChangedFiles, RunTwoChangedFiles: observedTwo.ChangedFiles,
		Restarted: true, OneActiveRejection: activeErr.Code,
	}, nil
}

func (a *RealAdapter) WorkVerification(ctx context.Context, request GateRequest) (WorkVerificationObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-work-verification")
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	implementationInstruction := fmt.Sprintf("Replace the complete contents of %s with exactly the following single line, including its trailing newline:\n%sDo not change any other file and do not create a commit.", request.ExpectedPath, request.ExpectedContent)
	runVerification := map[string]any{
		"mode":  "RUN",
		"scope": "fixture tracked file",
		"checks": []map[string]any{{
			"kind": "GIT_ASSERTION", "command": "git diff --check", "paths": []string{request.ExpectedPath}, "required": true,
		}},
		"acceptanceCriteria": []string{"git diff --check passes and tracked.txt has the confirmed contents"},
		"codexInstruction":   "Validate only the confirmed fixture change. Use the shell command-execution tool to execute exactly `git diff --check` as a standalone command and wait for its exit code. Then inspect tracked.txt without modifying any file. Report your findings naturally.",
	}
	runPlan, err := a.startStructuredTask(ctx, request, state, fixture, request.BaseCommit, implementationInstruction, runVerification)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	runExecution, err := a.waitExecutionProposal(ctx, state, fixture.AISessionID, runPlan.executionID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	if runExecution.VerificationRunID == "" {
		return WorkVerificationObservation{}, errors.New("RUN execution completed without a Verification Run ID")
	}
	implementation, err := a.observePlanRun(ctx, state, fixture.AISessionID, runExecution.ImplementationRunID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	verification, err := a.observePlanRun(ctx, state, fixture.AISessionID, runExecution.VerificationRunID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	runTask, err := a.getTaskStatus(ctx, state, fixture.AISessionID, runPlan.taskID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	failVerification := map[string]any{
		"mode":  "RUN",
		"scope": "controlled nonzero command",
		"checks": []map[string]any{{
			"kind": "TEST_COMMAND", "command": "false", "required": true,
		}},
		"acceptanceCriteria": []string{"the controlled command outcome is recorded from its real exit code"},
		"codexInstruction":   "Run exactly `false` as an observable command operation. It is expected to exit nonzero. Do not modify any file. Report the finding naturally; prose cannot override the command result.",
	}
	failPlan, err := a.startStructuredTask(ctx, request, state, fixture, request.BaseCommit, implementationInstruction, failVerification)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	failExecution, err := a.waitExecutionProposal(ctx, state, fixture.AISessionID, failPlan.executionID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	if failExecution.VerificationRunID == "" {
		return WorkVerificationObservation{}, errors.New("failing RUN execution completed without a Verification Run ID")
	}
	failImplementation, err := a.observePlanRun(ctx, state, fixture.AISessionID, failExecution.ImplementationRunID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	failVerificationRun, err := a.observePlanRun(ctx, state, fixture.AISessionID, failExecution.VerificationRunID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	failTask, err := a.getTaskStatus(ctx, state, fixture.AISessionID, failPlan.taskID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	noneVerification := map[string]any{
		"mode":   "NONE",
		"waiver": map[string]any{"source": "USER_WAIVED", "reason": "Real E2E case explicitly waives verification."},
	}
	nonePlan, err := a.startStructuredTask(ctx, request, state, fixture, request.BaseCommit, implementationInstruction, noneVerification)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	noneExecution, err := a.waitExecutionProposal(ctx, state, fixture.AISessionID, nonePlan.executionID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	noneImplementation, err := a.observePlanRun(ctx, state, fixture.AISessionID, noneExecution.ImplementationRunID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	noneTask, err := a.getTaskStatus(ctx, state, fixture.AISessionID, nonePlan.taskID)
	if err != nil {
		return WorkVerificationObservation{}, err
	}
	observation := WorkVerificationObservation{
		RunTaskID: runPlan.taskID, RunExecutionID: runPlan.executionID, RunOutcome: runExecution.Outcome, RunTaskStatus: runTask.Status,
		Implementation: implementation, Verification: verification,
		FailTaskID: failPlan.taskID, FailExecutionID: failPlan.executionID, FailOutcome: failExecution.Outcome, FailTaskStatus: failTask.Status,
		FailImplementation: failImplementation, FailVerification: failVerificationRun,
		NoneTaskID: nonePlan.taskID, NoneExecutionID: nonePlan.executionID, NoneOutcome: noneExecution.Outcome, NoneTaskStatus: noneTask.Status,
		NoneMode: noneExecution.VerificationSpec.Mode, NoneVerificationRunID: noneExecution.VerificationRunID, NoneImplementation: noneImplementation,
	}
	if len(runExecution.VerificationSpec.Checks) > 0 {
		observation.RunScope = runExecution.VerificationSpec.Scope
		observation.RunCheckKind = runExecution.VerificationSpec.Checks[0].Kind
		observation.RunCheckCommand = runExecution.VerificationSpec.Checks[0].Command
	}
	if noneExecution.VerificationSpec.Waiver != nil {
		observation.NoneWaiverSource = noneExecution.VerificationSpec.Waiver.Source
		observation.NoneWaiverReason = noneExecution.VerificationSpec.Waiver.Reason
	}
	return observation, nil
}

func (a *RealAdapter) ExecutionScope(ctx context.Context, request GateRequest) (ExecutionScopeObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-execution-scope")
	if err != nil {
		return ExecutionScopeObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return ExecutionScopeObservation{}, err
	}
	verification := map[string]any{
		"mode": "RUN", "scope": "allowed target only",
		"checks":             []map[string]any{{"kind": "GIT_ASSERTION", "command": "git diff --check", "paths": []string{"allowed/target.txt"}, "required": true}},
		"acceptanceCriteria": []string{"git diff --check passes and allowed/target.txt has the confirmed contents"},
		"codexInstruction":   "Use the shell command-execution tool to execute exactly `git diff --check` as a standalone command and wait for its exit code. Then inspect allowed/target.txt without modifying any file. Report your findings naturally.",
	}
	validInstruction := "Replace the complete contents of allowed/target.txt with exactly `allowed by PATHS` followed by a newline. Do not modify any other file and do not create a commit."
	valid, err := a.runExecutionScopeCase(ctx, request, state, fixture, request.BaseCommit, validInstruction, map[string]any{"kind": "PATHS", "paths": []string{"allowed"}}, verification, false)
	if err != nil {
		return ExecutionScopeObservation{}, err
	}
	violationInstruction := "From the repository root, run the existing executable `./allowed/scope_fixture_mutation.sh` exactly once. Do not inspect or edit the script, do not make any other change, and do not revert or commit its result. This is a deterministic disposable integration fixture."
	violation, err := a.runExecutionScopeCase(ctx, request, state, fixture, request.BaseCommit, violationInstruction, map[string]any{"kind": "PATHS", "paths": []string{"allowed"}}, verification, true)
	if err != nil {
		return ExecutionScopeObservation{}, err
	}
	repositoryInstruction := "Replace allowed/target.txt with exactly `repository allowed target` followed by a newline AND replace outside/protected.txt with exactly `repository allowed protected` followed by a newline. Do not modify any other file and do not create a commit."
	repository, err := a.runExecutionScopeCase(ctx, request, state, fixture, request.BaseCommit, repositoryInstruction, map[string]any{"kind": "REPOSITORY"}, implementationOnlyVerification("Repository-wide scope case explicitly waives verification."), false)
	if err != nil {
		return ExecutionScopeObservation{}, err
	}
	return ExecutionScopeObservation{ValidPaths: valid, Violation: violation, Repository: repository}, nil
}

func (a *RealAdapter) TaskFirstScopeReview(ctx context.Context, request GateRequest) (TaskFirstScopeReviewObservation, error) {
	state, child, err := a.startOwnedBridge(ctx, request, "bridge-task-first-scope-review")
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	defer stopChild(child)
	fixture, err := a.ensureProvisioned(ctx, request, state)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}

	createTask := func(problem string) (taskWire, error) {
		var created taskWire
		err := a.post(ctx, state, "/v1/tasks", map[string]any{
			"AISessionID": fixture.AISessionID, "ModuleID": fixture.ModuleID, "RepositoryID": fixture.RepositoryID,
			"CodexConfigID": fixture.CodexConfigID, "ProblemStatement": problem, "BaseRevision": request.BaseCommit,
		}, &created)
		return created, err
	}
	created, err := createTask("Inspect and then update the disposable tracked fixture through an explicitly confirmed TaskPlan")
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	initialRunCount, err := a.countTaskRuns(ctx, state, fixture.ProjectID, created.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}

	var search repositorySearchWire
	if err := a.post(ctx, state, "/v1/repository-search", map[string]any{
		"AISessionID": fixture.AISessionID, "RepositoryID": fixture.RepositoryID,
		"Query": "base line", "Paths": []string{"tracked.txt"}, "Limit": 10,
	}, &search); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	if len(search.Matches) != 1 {
		return TaskFirstScopeReviewObservation{}, fmt.Errorf("fixed repository search returned %d matches", len(search.Matches))
	}

	var reviewRun struct {
		ID     string
		TaskID string
		Status string
	}
	if err := a.post(ctx, state, "/v1/scope-reviews", map[string]any{
		"AISessionID": fixture.AISessionID, "TaskID": created.ID,
		"Scope":            map[string]any{"kind": "PATHS", "paths": []string{"tracked.txt"}},
		"CodexInstruction": "Read tracked.txt and report its current one-line contents and the safest exact-file implementation scope. This is a read-only Scope Review: do not modify files, run mutating commands, or create a commit.",
	}, &reviewRun); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	state.runID, state.taskID = reviewRun.ID, created.ID
	review, err := a.observePlanRun(ctx, state, fixture.AISessionID, reviewRun.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	reviewRaw, err := a.observeRunByID(ctx, state, fixture.AISessionID, reviewRun.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}

	verificationSpec := map[string]any{
		"mode": "RUN", "scope": "tracked.txt exact-file verification",
		"checks":             []map[string]any{{"kind": "GIT_ASSERTION", "command": "git diff --check", "paths": []string{"tracked.txt"}, "required": true}},
		"acceptanceCriteria": []string{"git diff --check passes and tracked.txt contains the confirmed single line"},
		"codexInstruction":   "Use the shell command-execution tool to execute exactly `git diff --check` as a standalone command and wait for its exit code. Then inspect tracked.txt without modifying any file. Report your findings naturally.",
	}
	var pendingPlan taskPlanWire
	if err := a.post(ctx, state, "/v1/task-plans", map[string]any{
		"AISessionID": fixture.AISessionID, "TaskID": created.ID,
		"WorkSpec": map[string]any{
			"goal": "Apply the confirmed exact tracked-file change", "constraints": []string{"change only tracked.txt"},
			"codexInstruction": fmt.Sprintf("Replace the complete contents of tracked.txt with exactly the following single line, including its trailing newline:\n%sDo not change any other file and do not create a commit.", request.ExpectedContent),
			"executionScope":   map[string]any{"kind": "PATHS", "paths": []string{"tracked.txt"}},
		},
		"VerificationSpec": verificationSpec,
	}, &pendingPlan); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	action := map[string]string{"ai_session_id": fixture.AISessionID}
	planApplyRouteClosed, err := a.legacyDecisionRouteClosed(ctx, state, "/v1/task-plans/"+url.PathEscape(pendingPlan.ID)+"/apply", action)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	mcpSession, mcpChild, err := a.startMCPClient(ctx, request)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	defer mcpSession.Close()
	defer stopChild(mcpChild)
	var continued taskPlanContinuationWire
	if err := callMCPTool(ctx, mcpSession, "ui_task_decide", map[string]any{"aiSessionId": fixture.AISessionID, "decisionId": "TASK_CONFIRMATION:" + pendingPlan.ID, "option": "CONFIRM", "requestId": "real-adapter-pending-confirm"}, &continued); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	if continued.TaskPlan.ID != pendingPlan.ID || continued.TaskPlan.Status != "APPLIED" || continued.Task.Status != "READY" || continued.ExecutionProposal.ID == "" || continued.ExecutionProposal.TaskPlanID != pendingPlan.ID || continued.ExecutionProposal.Status != "PENDING_CONFIRMATION" {
		return TaskFirstScopeReviewObservation{}, fmt.Errorf("MCP TaskPlan continuation returned inconsistent projection: %+v", continued)
	}
	runsAfterPlanContinuation, err := a.countTaskRuns(ctx, state, fixture.ProjectID, created.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	var repeatedPlan taskPlanContinuationWire
	if err := callMCPTool(ctx, mcpSession, "ui_task_decide", map[string]any{"aiSessionId": fixture.AISessionID, "decisionId": "TASK_CONFIRMATION:" + pendingPlan.ID, "option": "CONFIRM", "requestId": "real-adapter-pending-confirm"}, &repeatedPlan); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	if repeatedPlan.ExecutionProposal.ID != continued.ExecutionProposal.ID {
		return TaskFirstScopeReviewObservation{}, fmt.Errorf("repeated MCP Task confirmation did not return the existing Execution: %+v", repeatedPlan)
	}
	pendingExecution := continued.ExecutionProposal
	executionStartRouteClosed, err := a.legacyDecisionRouteClosed(ctx, state, "/v1/execution-proposals/"+url.PathEscape(pendingExecution.ID)+"/start", action)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	var started executionStartWire
	if err := callMCPTool(ctx, mcpSession, "ui_execution_decide", map[string]any{"aiSessionId": fixture.AISessionID, "proposalId": pendingExecution.ID, "decision": "EXECUTE"}, &started); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	if started.ExecutionProposal.ID != pendingExecution.ID || started.Run.ID == "" || started.Run.TaskID != created.ID {
		return TaskFirstScopeReviewObservation{}, fmt.Errorf("MCP Execute returned inconsistent projection: %+v", started)
	}
	var repeatedExecute executionStartWire
	if err := callMCPTool(ctx, mcpSession, "ui_execution_decide", map[string]any{"aiSessionId": fixture.AISessionID, "proposalId": pendingExecution.ID, "decision": "EXECUTE"}, &repeatedExecute); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	if repeatedExecute.Run.ID != started.Run.ID {
		return TaskFirstScopeReviewObservation{}, fmt.Errorf("repeated MCP Execute did not return the existing Run: %+v", repeatedExecute)
	}
	execution, err := a.waitExecutionProposal(ctx, state, fixture.AISessionID, pendingExecution.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	implementation, err := a.observePlanRun(ctx, state, fixture.AISessionID, execution.ImplementationRunID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	verification, err := a.observePlanRun(ctx, state, fixture.AISessionID, execution.VerificationRunID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	completedTask, err := a.getTaskStatus(ctx, state, fixture.AISessionID, created.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	persistedReview, err := a.observeRunByID(ctx, state, fixture.AISessionID, reviewRun.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}

	rejectedTask, err := createTask("Demonstrate that rejecting a TaskPlan leaves its Task in PLANNING and creates no Run")
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	var rejectedPlan taskPlanWire
	if err := a.post(ctx, state, "/v1/task-plans", map[string]any{
		"AISessionID": fixture.AISessionID, "TaskID": rejectedTask.ID,
		"WorkSpec": map[string]any{
			"goal": "This plan must never execute", "constraints": []string{"rejection fixture"},
			"codexInstruction": "Do not execute this rejected plan.", "executionScope": map[string]any{"kind": "PATHS", "paths": []string{"tracked.txt"}},
		},
		"VerificationSpec": implementationOnlyVerification("Rejected TaskPlan must not create a Run."),
	}, &rejectedPlan); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	var rejectedContinuation taskPlanContinuationWire
	if err := callMCPTool(ctx, mcpSession, "ui_task_decide", map[string]any{
		"aiSessionId": fixture.AISessionID, "decisionId": "TASK_CONFIRMATION:" + rejectedPlan.ID,
		"option": "CANCEL", "requestId": "real-adapter-rejected-plan-cancel",
	}, &rejectedContinuation); err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	rejectedTaskStatus, err := a.getTaskStatus(ctx, state, fixture.AISessionID, rejectedTask.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}
	rejectedRunCount, err := a.countTaskRuns(ctx, state, fixture.ProjectID, rejectedTaskStatus.ID)
	if err != nil {
		return TaskFirstScopeReviewObservation{}, err
	}

	match := search.Matches[0]
	return TaskFirstScopeReviewObservation{
		TaskID: created.ID, InitialTaskStatus: created.Status, InitialRunCount: initialRunCount,
		SearchPath: match.Path, SearchLine: match.Line, SearchSnippet: match.Snippet, SearchTruncated: search.Truncated,
		ReviewResult: reviewRaw.AgentClaim, ReviewResultAfter: persistedReview.AgentClaim, Review: review,
		TaskPlanID: pendingPlan.ID, PendingTaskPlanStatus: pendingPlan.Status, PendingTaskPlanApplyRouteClosed: planApplyRouteClosed,
		AppliedTaskPlanStatus: continued.TaskPlan.Status, AppliedTaskStatus: continued.Task.Status,
		RunsAfterPlanContinuation: runsAfterPlanContinuation, RepeatedPlanConverged: repeatedPlan.ExecutionProposal.ID == pendingExecution.ID,
		ExecutionID: pendingExecution.ID, PendingExecutionStartRouteClosed: executionStartRouteClosed, RepeatedExecuteConverged: repeatedExecute.Run.ID == started.Run.ID, ExecutionOutcome: execution.Outcome,
		CompletedTaskStatus: completedTask.Status, Implementation: implementation, Verification: verification,
		RejectedTaskID: rejectedTaskStatus.ID, RejectedTaskStatus: rejectedTaskStatus.Status, RejectedTaskPlanStatus: rejectedContinuation.TaskPlan.Status, RejectedTaskRunCount: rejectedRunCount,
	}, nil
}

func (a *RealAdapter) runExecutionScopeCase(ctx context.Context, request GateRequest, state *realGateState, fixture provisionWire, baseCommit, instruction string, scope, verification map[string]any, approveFileChanges bool) (ExecutionScopeCaseObservation, error) {
	plan, err := a.startStructuredTaskWithScope(ctx, request, state, fixture, baseCommit, instruction, scope, verification)
	if err != nil {
		return ExecutionScopeCaseObservation{}, err
	}
	var execution executionProposalDetailWire
	var approved []string
	if approveFileChanges {
		execution, approved, err = a.waitExecutionProposalApprovingMutationActions(ctx, state, fixture.AISessionID, plan.executionID, plan.implementationRunID)
	} else {
		execution, err = a.waitExecutionProposal(ctx, state, fixture.AISessionID, plan.executionID)
	}
	if err != nil {
		return ExecutionScopeCaseObservation{}, err
	}
	implementation, err := a.observePlanRun(ctx, state, fixture.AISessionID, execution.ImplementationRunID)
	if err != nil {
		return ExecutionScopeCaseObservation{}, err
	}
	var verificationRun PlanRunObservation
	if execution.VerificationRunID != "" {
		verificationRun, err = a.observePlanRun(ctx, state, fixture.AISessionID, execution.VerificationRunID)
		if err != nil {
			return ExecutionScopeCaseObservation{}, err
		}
	}
	taskValue, err := a.getTaskStatus(ctx, state, fixture.AISessionID, plan.taskID)
	if err != nil {
		return ExecutionScopeCaseObservation{}, err
	}
	return ExecutionScopeCaseObservation{
		TaskID: plan.taskID, ExecutionID: plan.executionID, Outcome: execution.Outcome, TaskStatus: taskValue.Status,
		ScopeKind: execution.WorkSpec.ExecutionScope.Kind, ScopePaths: append([]string(nil), execution.WorkSpec.ExecutionScope.Paths...),
		VerificationRunID: execution.VerificationRunID, ApprovedProtocolActionIDs: approved, Implementation: implementation, Verification: verificationRun,
	}, nil
}

type startedPlan struct {
	executionID         string
	taskID              string
	taskPlanID          string
	implementationRunID string
}

func (a *RealAdapter) startStructuredTask(ctx context.Context, request GateRequest, state *realGateState, fixture provisionWire, baseCommit, implementationInstruction string, verificationSpec map[string]any) (startedPlan, error) {
	return a.startStructuredTaskWithScope(ctx, request, state, fixture, baseCommit, implementationInstruction, map[string]any{"kind": "REPOSITORY"}, verificationSpec)
}

func (a *RealAdapter) startStructuredTaskWithScope(ctx context.Context, request GateRequest, state *realGateState, fixture provisionWire, baseCommit, implementationInstruction string, executionScope, verificationSpec map[string]any) (startedPlan, error) {
	var created taskWire
	if err := a.post(ctx, state, "/v1/tasks", map[string]any{
		"AISessionID": fixture.AISessionID, "ModuleID": fixture.ModuleID, "RepositoryID": fixture.RepositoryID,
		"CodexConfigID": fixture.CodexConfigID, "ProblemStatement": "Apply the requested disposable fixture change", "BaseRevision": baseCommit,
	}, &created); err != nil {
		return startedPlan{}, err
	}
	if created.ID == "" || created.Status != "PLANNING" {
		return startedPlan{}, fmt.Errorf("task-first creation returned %+v", created)
	}
	var plan taskPlanWire
	if err := a.post(ctx, state, "/v1/task-plans", map[string]any{
		"AISessionID": fixture.AISessionID, "TaskID": created.ID,
		"WorkSpec": map[string]any{
			"goal": "Apply the confirmed disposable fixture change", "constraints": []string{"bounded real E2E fixture only"},
			"codexInstruction": implementationInstruction, "executionScope": executionScope,
		},
		"VerificationSpec": verificationSpec,
	}, &plan); err != nil {
		return startedPlan{}, err
	}
	// Authoritative decision write path: ConfirmTask atomically applies the
	// plan and creates the pending Execution; EXECUTE authorizes its start.
	mcpSession, mcpChild, err := a.startMCPClient(ctx, request)
	if err != nil {
		return startedPlan{}, err
	}
	defer mcpSession.Close()
	defer stopChild(mcpChild)
	var applied taskPlanContinuationWire
	if err := callMCPTool(ctx, mcpSession, "ui_task_decide", map[string]any{
		"aiSessionId": fixture.AISessionID, "decisionId": "TASK_CONFIRMATION:" + plan.ID,
		"option": "CONFIRM", "requestId": "structured-confirm-" + plan.ID,
	}, &applied); err != nil {
		return startedPlan{}, err
	}
	if applied.TaskPlan.ID != plan.ID || applied.TaskPlan.Status != "APPLIED" || applied.Task.ID == "" || applied.ExecutionProposal.ID == "" {
		return startedPlan{}, fmt.Errorf("MCP Task confirmation returned inconsistent projection: %+v", applied)
	}
	execution := applied.ExecutionProposal
	var started executionStartWire
	if err := callMCPTool(ctx, mcpSession, "ui_execution_decide", map[string]any{
		"aiSessionId": fixture.AISessionID, "proposalId": execution.ID, "decision": "EXECUTE",
	}, &started); err != nil {
		return startedPlan{}, err
	}
	if started.Run.ID == "" || started.Run.TaskID != applied.Task.ID {
		return startedPlan{}, errors.New("structured execution start returned inconsistent identities")
	}
	return startedPlan{executionID: execution.ID, taskID: applied.Task.ID, taskPlanID: plan.ID, implementationRunID: started.Run.ID}, nil
}

func (a *RealAdapter) waitExecutionProposal(ctx context.Context, state *realGateState, aiSessionID, executionID string) (executionProposalDetailWire, error) {
	var last executionProposalDetailWire
	err := a.poll(ctx, func() (bool, error) {
		path := "/v1/execution-proposals/" + url.PathEscape(executionID) + "?ai_session_id=" + url.QueryEscape(aiSessionID)
		if err := a.get(ctx, state, path, &last); err != nil {
			return false, err
		}
		switch last.Outcome {
		case "SUCCEEDED", "FAILED", "CANCELED":
			return true, nil
		default:
			return false, nil
		}
	})
	return last, err
}

// The deliberately contradictory scope-violation instruction can cause real
// Codex to request a one-time FILE_CHANGE or COMMAND_EXECUTION protocol
// approval. The harness grants only the pending mutation action owned by this
// Run; it never changes or widens the confirmed WorkSpec. Bridge must still
// reject the resulting Git delta.
func (a *RealAdapter) waitExecutionProposalApprovingMutationActions(ctx context.Context, state *realGateState, aiSessionID, executionID, implementationRunID string) (executionProposalDetailWire, []string, error) {
	var last executionProposalDetailWire
	approved := make([]string, 0)
	approvedSet := make(map[string]struct{})
	err := a.poll(ctx, func() (bool, error) {
		path := "/v1/execution-proposals/" + url.PathEscape(executionID) + "?ai_session_id=" + url.QueryEscape(aiSessionID)
		if err := a.get(ctx, state, path, &last); err != nil {
			return false, err
		}
		switch last.Outcome {
		case "SUCCEEDED", "FAILED", "CANCELED":
			return true, nil
		}
		runState := &realGateState{address: state.address, dataRoot: state.dataRoot, runID: implementationRunID}
		observed, err := a.observe(ctx, runState)
		if err != nil {
			return false, err
		}
		for _, pending := range observed.Approvals {
			if pending.Status != "PENDING" {
				continue
			}
			if _, alreadyApproved := approvedSet[pending.ID]; alreadyApproved {
				continue
			}
			var details []approvalDetailWire
			approvalPath := "/v1/runs/" + url.PathEscape(implementationRunID) + "/approvals?ai_session_id=" + url.QueryEscape(aiSessionID)
			if err := a.get(ctx, state, approvalPath, &details); err != nil {
				return false, err
			}
			kind := ""
			for _, detail := range details {
				if detail.ID == pending.ID && detail.Status == "PENDING" {
					kind = detail.Kind
					break
				}
			}
			if kind != "FILE_CHANGE" && kind != "COMMAND_EXECUTION" {
				return false, fmt.Errorf("scope violation requested unexpected %s approval %s", kind, pending.ID)
			}
			var delivered approvalWire
			if err := a.post(ctx, state, "/v1/approvals/"+url.PathEscape(pending.ID)+"/decide", map[string]any{
				"ai_session_id": aiSessionID,
				"decision":      map[string]string{"type": "APPROVE_ONCE"},
			}, &delivered); err != nil {
				return false, err
			}
			if delivered.ID != pending.ID || delivered.Status != "DELIVERED" {
				return false, fmt.Errorf("scope violation %s approval was not durably delivered: %+v", kind, delivered)
			}
			approvedSet[pending.ID] = struct{}{}
			approved = append(approved, pending.ID)
			break
		}
		return false, nil
	})
	return last, approved, err
}

func (a *RealAdapter) observePlanRun(ctx context.Context, state *realGateState, aiSessionID, runID string) (PlanRunObservation, error) {
	if runID == "" {
		return PlanRunObservation{}, errors.New("plan Run ID is empty")
	}
	runState := &realGateState{address: state.address, dataRoot: state.dataRoot, runID: runID}
	value, err := a.waitObservation(ctx, runState, func(value runObservationWire) bool { return terminalStatus(value.Status) })
	if err != nil {
		return PlanRunObservation{}, err
	}
	result := PlanRunObservation{
		ID: value.RunID, TaskID: value.TaskID, Purpose: value.Purpose, ExecutionProposalID: value.ExecutionID,
		WorkspaceID: value.WorkspaceID, WorktreeID: value.WorktreeID, WorktreePath: value.WorktreePath, ThreadID: value.ThreadID, TurnID: value.TurnID,
		Status: value.Status, StartSnapshot: value.StartSnapshot, EndSnapshot: value.EndSnapshot, StartedAt: value.StartedAt, FinishedAt: value.FinishedAt,
		TokenStatus: value.TokenUsageStatus, TokenTotal: tokenTotal(value), TokenCachedInput: tokenCachedInput(value), ChangedFiles: append([]string(nil), value.ChangedFiles...),
		DiffArtifactID: value.DiffArtifactID, FailureCode: value.FailureCode, FailureMessage: value.FailureMessage, AgentClaim: value.AgentClaim,
	}
	if value.VerificationResult != nil {
		result.VerificationStatus = value.VerificationResult.Status
		result.VerificationChecks = len(value.VerificationResult.Checks)
		if len(value.VerificationResult.Checks) > 0 {
			check := value.VerificationResult.Checks[0]
			result.VerificationCheckStatus = check.Status
			result.VerificationObservedCommand = check.ObservedCommand
			result.VerificationExitCode = check.ExitCode
			result.VerificationFailureReason = check.FailureReason
		}
	}
	return result, nil
}

func (a *RealAdapter) getTaskStatus(ctx context.Context, state *realGateState, aiSessionID, taskID string) (taskStatusWire, error) {
	var value taskStatusWire
	path := "/v1/tasks/" + url.PathEscape(taskID) + "?ai_session_id=" + url.QueryEscape(aiSessionID)
	err := a.get(ctx, state, path, &value)
	return value, err
}

type startedWorkflow struct {
	taskPlanID string
	taskID     string
	runID      string
}

func (a *RealAdapter) startTask(ctx context.Context, request GateRequest, state *realGateState, fixture provisionWire, baseCommit, goal string) (startedWorkflow, error) {
	plan, err := a.startStructuredTask(ctx, request, state, fixture, baseCommit, goal, implementationOnlyVerification("real E2E gate exercises implementation behavior only"))
	if err != nil {
		return startedWorkflow{}, err
	}
	return startedWorkflow{taskPlanID: plan.taskPlanID, taskID: plan.taskID, runID: plan.implementationRunID}, nil
}

type preparedExecution struct {
	taskPlanID        string
	taskID            string
	executionProposal string
}

func (a *RealAdapter) prepareRepeatedExecution(ctx context.Context, state *realGateState, fixture provisionWire, taskID, taskPlanID string) (preparedExecution, error) {
	var execution executionProposalWire
	if err := a.post(ctx, state, "/v1/execution-proposals", struct {
		AISessionID string
		TaskPlanID  string
	}{fixture.AISessionID, taskPlanID}, &execution); err != nil {
		return preparedExecution{}, err
	}
	return preparedExecution{taskPlanID: taskPlanID, taskID: taskID, executionProposal: execution.ID}, nil
}

func implementationOnlyVerification(reason string) map[string]any {
	return map[string]any{"mode": "NONE", "waiver": map[string]any{"source": "USER_WAIVED", "reason": reason}}
}

func (a *RealAdapter) startConfirmedExecution(ctx context.Context, request GateRequest, state *realGateState, fixture provisionWire, proposalID string) (string, error) {
	return a.startExecutionViaMCP(ctx, request, fixture, proposalID)
}

func (a *RealAdapter) startExecutionViaMCP(ctx context.Context, request GateRequest, fixture provisionWire, proposalID string) (string, error) {
	mcpSession, mcpChild, err := a.startMCPClient(ctx, request)
	if err != nil {
		return "", err
	}
	defer mcpSession.Close()
	defer stopChild(mcpChild)
	var started executionStartWire
	if err := callMCPTool(ctx, mcpSession, "ui_execution_decide", map[string]any{
		"aiSessionId": fixture.AISessionID, "proposalId": proposalID, "decision": "EXECUTE",
	}, &started); err != nil {
		return "", err
	}
	if started.Run.ID == "" {
		return "", errors.New("MCP execution start returned an empty Run ID")
	}
	return started.Run.ID, nil
}

func tokenTotal(value runObservationWire) int64 {
	if value.TokenUsage == nil {
		return 0
	}
	return value.TokenUsage.TotalTokens
}

func tokenCachedInput(value runObservationWire) int64 {
	if value.TokenUsage == nil {
		return 0
	}
	return value.TokenUsage.CachedInputTokens
}

func (a *RealAdapter) observeRunByID(ctx context.Context, state *realGateState, aiSessionID, runID string) (runObservationWire, error) {
	var value runObservationWire
	path := "/v1/runs/" + url.PathEscape(runID) + "/observation?ai_session_id=" + url.QueryEscape(aiSessionID)
	err := a.get(ctx, state, path, &value)
	return value, err
}

func (a *RealAdapter) countTaskRuns(ctx context.Context, state *realGateState, projectID, taskID string) (int, error) {
	var page consoleRunPageWire
	path := "/v1/runs?project_id=" + url.QueryEscape(projectID) + "&limit=100"
	if err := a.get(ctx, state, path, &page); err != nil {
		return 0, err
	}
	count := 0
	for _, item := range page.Items {
		if item.TaskID == taskID {
			count++
		}
	}
	return count, nil
}

func (a *RealAdapter) countTaskRunsByPurpose(ctx context.Context, state *realGateState, projectID, taskID, purpose string) (int, error) {
	var page consoleRunPageWire
	path := "/v1/runs?project_id=" + url.QueryEscape(projectID) + "&limit=100"
	if err := a.get(ctx, state, path, &page); err != nil {
		return 0, err
	}
	count := 0
	for _, item := range page.Items {
		if item.TaskID == taskID && item.Purpose == purpose {
			count++
		}
	}
	return count, nil
}

func (a *RealAdapter) countProjectRuns(ctx context.Context, state *realGateState, projectID string) (int, error) {
	var page consoleRunPageWire
	path := "/v1/runs?project_id=" + url.QueryEscape(projectID) + "&limit=100"
	if err := a.get(ctx, state, path, &page); err != nil {
		return 0, err
	}
	return len(page.Items), nil
}

func (a *RealAdapter) hasExactRunPrompt(ctx context.Context, state *realGateState, projectID, taskID, prompt string) (bool, error) {
	var page consoleRunPageWire
	path := "/v1/runs?project_id=" + url.QueryEscape(projectID) + "&limit=100"
	if err := a.get(ctx, state, path, &page); err != nil {
		return false, err
	}
	wantHash := sha256.Sum256([]byte(prompt))
	for _, item := range page.Items {
		if item.TaskID != taskID || !item.PromptAvailable {
			continue
		}
		var diagnostic runPromptDiagnosticWire
		path := "/v1/runs/" + url.PathEscape(item.ID) + "/prompt?project_id=" + url.QueryEscape(projectID)
		if err := a.get(ctx, state, path, &diagnostic); err != nil {
			return false, err
		}
		if diagnostic.Available && diagnostic.Prompt == prompt && diagnostic.SHA256 == fmt.Sprintf("%x", wantHash) && diagnostic.Bytes == int64(len([]byte(prompt))) {
			return true, nil
		}
	}
	return false, nil
}

func (a *RealAdapter) latestProjectScopeReviewRun(ctx context.Context, state *realGateState, projectID string) (string, string, error) {
	var page consoleRunPageWire
	path := "/v1/runs?project_id=" + url.QueryEscape(projectID) + "&limit=100"
	if err := a.get(ctx, state, path, &page); err != nil {
		return "", "", err
	}
	for _, item := range page.Items {
		if item.Purpose == "SCOPE_REVIEW" {
			return item.ID, item.TaskID, nil
		}
	}
	return "", "", errors.New("product Review internal Run was not observable in the local Console")
}

// legacyDecisionRouteClosed asserts that a removed legacy decision route now
// returns Go mux 404, proving no bypass of the authoritative decision contract.
func (a *RealAdapter) legacyDecisionRouteClosed(ctx context.Context, state *realGateState, path string, input any) (bool, error) {
	err := a.post(ctx, state, path, input, nil)
	var apiErr *httpAPIError
	if !errors.As(err, &apiErr) {
		if err == nil {
			return false, fmt.Errorf("%s unexpectedly succeeded", path)
		}
		return false, err
	}
	return apiErr.Status == http.StatusNotFound, nil
}

// The competing Execution must be rejected durably at proposal creation.
func (a *RealAdapter) startSecondExecution(ctx context.Context, state *realGateState, fixture provisionWire, taskPlanID string) (string, error) {
	var proposal executionProposalWire
	err := a.post(ctx, state, "/v1/execution-proposals", struct {
		AISessionID string
		TaskPlanID  string
	}{fixture.AISessionID, taskPlanID}, &proposal)
	var apiErr *httpAPIError
	if !errors.As(err, &apiErr) {
		if err == nil {
			return "", errors.New("second active Run for the same Task unexpectedly started")
		}
		return "", err
	}
	return apiErr.Code, nil
}

func (a *RealAdapter) observeRecovery(ctx context.Context, request GateRequest, withApproval bool) (RecoveryObservation, error) {
	state, err := a.gate(request.GateID)
	if err != nil {
		return RecoveryObservation{}, err
	}
	if err := a.waitReady(ctx, state); err != nil {
		return RecoveryObservation{}, err
	}
	observed, err := a.waitObservation(ctx, state, func(value runObservationWire) bool { return value.Status == "INFRASTRUCTURE_FAILED" })
	if err != nil {
		return RecoveryObservation{}, err
	}
	result := RecoveryObservation{
		RunID: observed.RunID, FinalStatus: observed.Status, FailureCode: observed.FailureCode,
		DataRoot: request.DataRoot, WorktreePath: observed.WorktreePath, ThreadID: observed.ThreadID,
		TokenStatus: observed.TokenUsageStatus, LiveSession: observed.LiveSession,
		RecoveryEventCount: observed.RecoveryEventCount,
	}
	if observed.FinishedAt != nil {
		result.FinishedAt = observed.FinishedAt.UTC().Format(time.RFC3339Nano)
	}
	if withApproval {
		for _, item := range observed.Approvals {
			if item.ID == state.approvalID {
				result.ApprovalStatus = item.Status
				result.ApprovalDecision = item.DecisionType
			}
		}
	}
	return result, nil
}

func (a *RealAdapter) startOwnedBridge(ctx context.Context, request GateRequest, label string) (*realGateState, *ChildProcess, error) {
	state, err := a.newGateState(request)
	if err != nil {
		return nil, nil, err
	}
	child, err := StartChild(a.command(request, state), request.ArtifactDir, label)
	if err != nil {
		return nil, nil, err
	}
	if err := a.waitReady(ctx, state); err != nil {
		stopChild(child)
		return nil, nil, err
	}
	return state, child, nil
}

func (a *RealAdapter) startMCPClient(ctx context.Context, request GateRequest) (*sdkmcp.ClientSession, *ChildProcess, error) {
	if strings.TrimSpace(a.MCPPath) == "" {
		return nil, nil, &CapabilityUnavailableError{Reason: "real Dev Bridge MCP executable path was not configured"}
	}
	absolute, err := filepath.Abs(a.MCPPath)
	if err != nil {
		return nil, nil, err
	}
	if _, err := exec.LookPath(absolute); err != nil {
		return nil, nil, &CapabilityUnavailableError{Reason: "real Dev Bridge MCP executable is unavailable: " + err.Error()}
	}
	core, err := a.gate(request.GateID)
	if err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, nil, err
	}
	child, err := StartChild(CommandSpec{Path: absolute, Args: []string{"-data-root", request.DataRoot, "-core-url", "http://" + core.address, "-listen", address}, Dir: filepath.Dir(absolute)}, request.ArtifactDir, "mcp-card-continuation")
	if err != nil {
		return nil, nil, err
	}
	state := &realGateState{address: address, dataRoot: request.DataRoot}
	readyContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := a.poll(readyContext, func() (bool, error) {
		var response struct {
			Status string `json:"status"`
		}
		if err := a.get(readyContext, state, "/healthz", &response); err != nil {
			return false, nil
		}
		return response.Status == "ok", nil
	}); err != nil {
		stopChild(child)
		return nil, nil, fmt.Errorf("wait for Dev Bridge MCP readiness at %s: %w", address, err)
	}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "devbridge-real-e2e", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: "http://" + address + "/mcp", HTTPClient: a.HTTPClient, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		stopChild(child)
		return nil, nil, fmt.Errorf("connect real MCP client: %w", err)
	}
	return session, child, nil
}

func callMCPTool(ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any, output any) error {
	if err := validateMCPToolArguments(ctx, session, name, arguments); err != nil {
		return err
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return err
	}
	if result.IsError {
		encoded, _ := json.Marshal(result.Content)
		return fmt.Errorf("MCP tool %s failed: %s", name, encoded)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode MCP tool %s result: %w", name, err)
	}
	return nil
}

// validateMCPToolArguments makes tools/list the harness contract authority.
// A production schema change therefore fails before a workflow is partially
// executed, rather than surfacing later as an ambiguous handler error.
func validateMCPToolArguments(ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) error {
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("HARNESS_CONTRACT_DRIFT: discover schema for %s: %w", name, err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != name {
			continue
		}
		return validateDiscoveredInputSchema(name, tool.InputSchema, arguments)
	}
	return fmt.Errorf("HARNESS_CONTRACT_DRIFT: tool %s is absent from tools/list", name)
}

func validateDiscoveredInputSchema(name string, inputSchema any, arguments map[string]any) error {
	encoded, err := json.Marshal(inputSchema)
	if err != nil {
		return fmt.Errorf("HARNESS_CONTRACT_DRIFT: encode discovered %s schema: %w", name, err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return fmt.Errorf("HARNESS_CONTRACT_DRIFT: decode discovered %s schema: %w", name, err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return fmt.Errorf("HARNESS_CONTRACT_DRIFT: resolve discovered %s schema: %w", name, err)
	}
	if err := resolved.Validate(arguments); err != nil {
		return fmt.Errorf("HARNESS_CONTRACT_DRIFT: %s payload does not satisfy tools/list input schema: %w", name, err)
	}
	return nil
}

func expectedMCPToolErrorCode(ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) (string, error) {
	if err := validateMCPToolArguments(ctx, session, name, arguments); err != nil {
		return "", err
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	if !result.IsError {
		return "", fmt.Errorf("MCP tool %s unexpectedly succeeded", name)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		return "", err
	}
	for _, code := range []reviewaccess.FailureCode{reviewaccess.FailureScopeForbidden} {
		if bytes.Contains(encoded, []byte(code)) {
			return string(code), nil
		}
	}
	return "", fmt.Errorf("MCP tool %s returned no stable Review policy code: %s", name, encoded)
}

func expectedMCPToolErrorContains(ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any, expected string) (bool, error) {
	if err := validateMCPToolArguments(ctx, session, name, arguments); err != nil {
		return false, err
	}
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return false, err
	}
	if !result.IsError {
		return false, fmt.Errorf("MCP tool %s unexpectedly succeeded", name)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		return false, err
	}
	if !bytes.Contains(encoded, []byte(expected)) {
		return false, fmt.Errorf("MCP tool %s returned no %s error: %s", name, expected, encoded)
	}
	return true, nil
}

func (a *RealAdapter) command(request GateRequest, state *realGateState) CommandSpec {
	return CommandSpec{Path: a.BridgePath, Args: []string{
		"-mode", "server", "-data-root", request.DataRoot,
		"-listen", state.address, "-admin-test",
	}, Dir: filepath.Dir(a.BridgePath)}
}

func (a *RealAdapter) newGateState(request GateRequest) (*realGateState, error) {
	if strings.TrimSpace(a.BridgePath) == "" {
		return nil, &CapabilityUnavailableError{Reason: "real Bridge executable path was not configured"}
	}
	absolute, err := filepath.Abs(a.BridgePath)
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath(absolute); err != nil {
		return nil, &CapabilityUnavailableError{Reason: "real Bridge executable is unavailable: " + err.Error()}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		return nil, err
	}
	state := &realGateState{address: address, dataRoot: request.DataRoot}
	a.mu.Lock()
	if a.gates == nil {
		a.gates = make(map[GateID]*realGateState)
	}
	a.BridgePath = absolute
	a.gates[request.GateID] = state
	a.mu.Unlock()
	return state, nil
}

func (a *RealAdapter) gate(id GateID) (*realGateState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	state := a.gates[id]
	if state == nil {
		return nil, fmt.Errorf("gate %s has no Bridge process state", id)
	}
	return state, nil
}

func (a *RealAdapter) ensureProvisioned(ctx context.Context, request GateRequest, state *realGateState) (provisionWire, error) {
	a.mu.Lock()
	if value, ok := a.provisioned[request.DataRoot]; ok {
		a.mu.Unlock()
		return value, nil
	}
	a.mu.Unlock()
	var value provisionWire
	if err := a.post(ctx, state, "/admin/test/v1/fixture", map[string]string{
		"source_repository": request.SourceRepo, "codex_path": a.CodexPath,
	}, &value); err != nil {
		return provisionWire{}, err
	}
	if value.ProjectID == "" || value.RepositoryID == "" || value.ModuleID == "" || value.CodexConfigID == "" || value.AISessionID == "" {
		return provisionWire{}, errors.New("fixture provisioning returned incomplete identities")
	}
	a.mu.Lock()
	if a.provisioned == nil {
		a.provisioned = make(map[string]provisionWire)
	}
	a.provisioned[request.DataRoot] = value
	a.mu.Unlock()
	return value, nil
}

func (a *RealAdapter) waitReady(ctx context.Context, state *realGateState) error {
	readyContext, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := a.poll(readyContext, func() (bool, error) {
		var response struct {
			Ready bool `json:"ready"`
		}
		err := a.get(ctx, state, "/readyz", &response)
		if err != nil {
			return false, nil
		}
		return response.Ready, nil
	})
	if err != nil {
		return fmt.Errorf("wait for Bridge readiness at %s: %w", state.address, err)
	}
	return nil
}

func (a *RealAdapter) waitObservation(ctx context.Context, state *realGateState, ready func(runObservationWire) bool) (runObservationWire, error) {
	var last runObservationWire
	err := a.poll(ctx, func() (bool, error) {
		value, err := a.observe(ctx, state)
		if err != nil {
			return false, err
		}
		last = value
		if terminalStatus(value.Status) && !ready(value) {
			return false, fmt.Errorf("Run %s became terminal as %s before the expected state (failure=%s)", value.RunID, value.Status, value.FailureCode)
		}
		return ready(value), nil
	})
	return last, err
}

func (a *RealAdapter) observe(ctx context.Context, state *realGateState) (runObservationWire, error) {
	fixture, ok := a.fixtureForDataRoot(state)
	if !ok {
		return runObservationWire{}, errors.New("fixture has not been provisioned")
	}
	path := "/v1/runs/" + url.PathEscape(state.runID) + "/observation?ai_session_id=" + url.QueryEscape(fixture.AISessionID)
	var value runObservationWire
	err := a.get(ctx, state, path, &value)
	return value, err
}

func (a *RealAdapter) fixtureForDataRoot(state *realGateState) (provisionWire, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	value, ok := a.provisioned[state.dataRoot]
	return value, ok
}

func (a *RealAdapter) poll(ctx context.Context, probe func() (bool, error)) error {
	interval := a.PollInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		ready, err := probe()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (a *RealAdapter) post(ctx context.Context, state *realGateState, path string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	return a.do(ctx, state, http.MethodPost, path, bytes.NewReader(data), output)
}

func (a *RealAdapter) get(ctx context.Context, state *realGateState, path string, output any) error {
	return a.do(ctx, state, http.MethodGet, path, nil, output)
}

func (a *RealAdapter) do(ctx context.Context, state *realGateState, method, path string, body io.Reader, output any) error {
	request, err := http.NewRequestWithContext(ctx, method, "http://"+state.address+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxHTTPResponseBytes {
		return errors.New("Bridge HTTP response exceeded 256 KiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		apiErr := &httpAPIError{Status: response.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return apiErr
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode Bridge HTTP response: %w", err)
	}
	return nil
}

func checkpoints(value runObservationWire) []Checkpoint {
	var result []Checkpoint
	for _, item := range value.Events {
		switch item.Type {
		case "run.status_changed":
			switch item.To {
			case "WAITING_APPROVAL":
				result = append(result, CheckpointWaitingApproval)
			case "COLLECTING":
				result = append(result, CheckpointCodexCompleted, CheckpointEvidenceCollecting)
			case "SUCCEEDED":
				result = append(result, CheckpointRunSucceeded)
			}
		case "approval.decided":
			result = append(result, CheckpointApprovalDecided)
			if item.DecisionType == "DECLINE" {
				result = append(result, CheckpointApprovalDeclined)
			}
		case "approval.delivered":
			result = append(result, CheckpointApprovalDelivered)
		}
	}
	return result
}

func approvalGoal() string {
	return "Run exactly `curl --max-time 30 https://example.com >/dev/null` from the current working tree. The network operation must go through the normal Codex approval flow. If permission is declined, do not retry or use an alternative; finish without changing files or creating a commit."
}

func hasPendingApproval(value runObservationWire) bool {
	return value.Status == "WAITING_APPROVAL" && firstPending(value).ID != ""
}

func firstPending(value runObservationWire) approvalWire {
	for _, item := range value.Approvals {
		if item.Status == "PENDING" {
			return item
		}
	}
	return approvalWire{}
}

func terminalStatus(status string) bool {
	switch status {
	case "SUCCEEDED", "FAILED", "INFRASTRUCTURE_FAILED", "CANCELED":
		return true
	default:
		return false
	}
}

func stopChild(child *ChildProcess) {
	if child == nil {
		return
	}
	_ = child.KillNonGracefully()
	_ = child.Wait()
}
