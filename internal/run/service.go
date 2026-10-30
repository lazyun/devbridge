package run

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
)

type Store interface {
	CreateAttempt(context.Context, string, string, time.Time) (Run, error)
	Get(context.Context, string) (Run, error)
	List(context.Context) ([]Run, error)
	Update(context.Context, string, func(*Run) error) (Run, error)
}

type executionStore interface {
	CreateExecutionAttempt(context.Context, string, string, Purpose, string, time.Time) (Run, error)
}

type scopeReviewStore interface {
	CreateScopeReviewAttempt(context.Context, string, string, string, ReviewScope, time.Time) (Run, error)
}

type focusedScopeReviewStore interface {
	CreateFocusedScopeReviewAttempt(context.Context, string, string, string, ReviewScope, ReviewPlan, time.Time) (Run, error)
}

type productScopeReviewStore interface {
	CreateProductScopeReviewAttempt(context.Context, string, string, string, ReviewScope, ReviewPlan, time.Time) (Run, error)
}

type promptStore interface {
	RecordPrompt(context.Context, string, string, time.Time) (Run, error)
}

// repositoryBindingStore is intentionally optional: v1 stores retain their
// scalar worktree contract while durable v2 stores expose exact member use.
type repositoryBindingStore interface {
	ListRepositoryBindings(context.Context, string) ([]RepositoryBinding, error)
	PutRepositoryBinding(context.Context, RepositoryBinding) error
}

type runtimeWorksetStore interface {
	BindRuntimeWorkset(context.Context, workset.RuntimeWorkset) (Run, error)
	RuntimeWorkset(context.Context, string) (workset.RuntimeWorkset, error)
	ObserveRepositoryBinding(context.Context, RepositoryObservation) error
}

type Service struct {
	AgentRuntime *agentruntime.Service
	store        Store
	newID        func() (string, error)
	now          func() time.Time
}

func NewService(store Store, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, newID: newID, now: now}
}

// ValidateRunExecution re-proves the immutable ownership chain against durable
// storage. Stores without this capability cannot authorize backend work.
func (s *Service) ValidateRunExecution(ctx context.Context, value Run) error {
	if s == nil || s.store == nil {
		return errors.New("durable AgentExecution validation is unavailable")
	}
	validator, ok := s.store.(interface {
		ValidateRunExecution(context.Context, Run) error
	})
	if !ok {
		return errors.New("run store cannot validate durable AgentExecution ownership")
	}
	return validator.ValidateRunExecution(ctx, value)
}

// ValidateRunExecutionDispatch also requires the exact conversation boundary
// for FeatureArea work, after BeginRun and before each backend handoff.
func (s *Service) ValidateRunExecutionDispatch(ctx context.Context, value Run) error {
	if s == nil || s.store == nil {
		return errors.New("durable AgentExecution dispatch validation is unavailable")
	}
	validator, ok := s.store.(interface {
		ValidateRunExecutionDispatch(context.Context, Run) error
	})
	if !ok {
		return errors.New("run store cannot validate durable AgentExecution dispatch")
	}
	return validator.ValidateRunExecutionDispatch(ctx, value)
}

func (s *Service) Create(ctx context.Context, taskID string) (Run, error) {
	if s.store == nil {
		return Run{}, errors.New("run store is required")
	}
	if taskID == "" {
		return Run{}, errors.New("run task id is required")
	}
	id, err := s.newID()
	if err != nil {
		return Run{}, fmt.Errorf("allocate run id: %w", err)
	}
	value, err := s.store.CreateAttempt(ctx, taskID, id, s.now().UTC())
	logCreated(ctx, value, err)
	return value, err
}

func (s *Service) ListRepositoryBindings(ctx context.Context, runID string) ([]RepositoryBinding, error) {
	store, ok := s.store.(repositoryBindingStore)
	if !ok {
		return nil, errors.New("run store does not support repository bindings")
	}
	return store.ListRepositoryBindings(ctx, runID)
}

func (s *Service) PutRepositoryBinding(ctx context.Context, value RepositoryBinding) error {
	store, ok := s.store.(repositoryBindingStore)
	if !ok {
		return errors.New("run store does not support repository bindings")
	}
	return store.PutRepositoryBinding(ctx, value)
}

// BindRuntimeWorkset durably freezes the exact target/worktree layout before
// a backend conversation is authorized. Replays may only write identical
// content for the same Run.
func (s *Service) BindRuntimeWorkset(ctx context.Context, value workset.RuntimeWorkset) (Run, error) {
	store, ok := s.store.(runtimeWorksetStore)
	if !ok {
		return Run{}, errors.New("run store does not support RuntimeWorkset")
	}
	return store.BindRuntimeWorkset(ctx, value)
}

// PreparationIdentity returns the committed Core attempt for an exact Run.
// A caller cannot infer preparation authority from a Run's existence.
func (s *Service) PreparationIdentity(ctx context.Context, runID string) (string, string, string, error) {
	store, ok := s.store.(interface {
		PreparationIdentity(context.Context, string) (string, string, string, error)
	})
	if !ok {
		return "", "", "", ErrNotFound
	}
	return store.PreparationIdentity(ctx, runID)
}

func (s *Service) CreatePreparationRun(ctx context.Context, planID, runID, requestID, payloadHash string, now time.Time) (Run, error) {
	store, ok := s.store.(interface {
		CreatePreparationRun(context.Context, string, string, string, string, time.Time) (Run, error)
	})
	if !ok {
		return Run{}, errors.New("durable RuntimePreparation Run creation unavailable")
	}
	return store.CreatePreparationRun(ctx, planID, runID, requestID, payloadHash, now)
}

func (s *Service) ClaimWorkspacePreparation(ctx context.Context, planID, runID, requestID, payloadHash string, now time.Time) (bool, error) {
	store, ok := s.store.(interface {
		ClaimWorkspacePreparation(context.Context, string, string, string, string, time.Time) (bool, error)
	})
	if !ok {
		return false, errors.New("durable workspace preparation unavailable")
	}
	return store.ClaimWorkspacePreparation(ctx, planID, runID, requestID, payloadHash, now)
}

func (s *Service) RecordWorkspaceObservations(ctx context.Context, planID, runID, requestID, payloadHash string, observations []runtimepreparation.WorkspaceObservation, now time.Time) (string, error) {
	store, ok := s.store.(interface {
		RecordWorkspaceObservations(context.Context, string, string, string, string, []runtimepreparation.WorkspaceObservation, time.Time) (string, error)
	})
	if !ok {
		return "", errors.New("durable workspace observation unavailable")
	}
	return store.RecordWorkspaceObservations(ctx, planID, runID, requestID, payloadHash, observations, now)
}

func (s *Service) BindPreparedRuntimeWorkset(ctx context.Context, planID, requestID, payloadHash, authorityID string, value workset.RuntimeWorkset, now time.Time) (Run, error) {
	store, ok := s.store.(interface {
		BindPreparedRuntimeWorkset(context.Context, string, string, string, string, workset.RuntimeWorkset, time.Time) (Run, error)
	})
	if !ok {
		return Run{}, errors.New("durable RuntimeWorkset binding unavailable")
	}
	return store.BindPreparedRuntimeWorkset(ctx, planID, requestID, payloadHash, authorityID, value, now)
}

func (s *Service) ReadPreparationReceipt(ctx context.Context, requestID, payloadHash string) (string, string, string, error) {
	store, ok := s.store.(interface {
		ReadPreparationReceipt(context.Context, string, string) (string, string, string, error)
	})
	if !ok {
		return "", "", "", errors.New("durable RuntimePreparation readback unavailable")
	}
	return store.ReadPreparationReceipt(ctx, requestID, payloadHash)
}

func (s *Service) ValidatePreparedStart(ctx context.Context, runID string) error {
	store, ok := s.store.(interface {
		ValidatePreparedStart(context.Context, string) error
	})
	if !ok {
		return errors.New("durable RuntimePreparation start authority unavailable")
	}
	return store.ValidatePreparedStart(ctx, runID)
}

func (s *Service) ValidatePreparedRejoin(ctx context.Context, runID string) error {
	store, ok := s.store.(interface {
		ValidatePreparedRejoin(context.Context, string) error
	})
	if !ok {
		return errors.New("durable RuntimePreparation rejoin authority unavailable")
	}
	return store.ValidatePreparedRejoin(ctx, runID)
}

func (s *Service) RuntimeWorkset(ctx context.Context, runID string) (workset.RuntimeWorkset, error) {
	store, ok := s.store.(runtimeWorksetStore)
	if !ok {
		return workset.RuntimeWorkset{}, errors.New("run store does not support RuntimeWorkset")
	}
	return store.RuntimeWorkset(ctx, runID)
}

func (s *Service) ObserveRepositoryBinding(ctx context.Context, value RepositoryObservation) error {
	store, ok := s.store.(runtimeWorksetStore)
	if !ok {
		return errors.New("run store does not support repository observations")
	}
	return store.ObserveRepositoryBinding(ctx, value)
}

func (s *Service) CreateForExecution(ctx context.Context, taskID string, purpose Purpose, executionProposalID string) (Run, error) {
	if taskID == "" || executionProposalID == "" || (purpose != PurposeImplementation && purpose != PurposeVerification) {
		return Run{}, errors.New("run task, purpose and execution proposal are required")
	}
	store, ok := s.store.(executionStore)
	if !ok {
		return Run{}, errors.New("run store does not support execution-plan linkage")
	}
	id, err := s.newID()
	if err != nil {
		return Run{}, fmt.Errorf("allocate run id: %w", err)
	}
	value, err := store.CreateExecutionAttempt(ctx, taskID, id, purpose, executionProposalID, s.now().UTC())
	logCreated(ctx, value, err)
	return value, err
}

func (s *Service) CreateScopeReview(ctx context.Context, taskID, instruction string, scope ReviewScope) (Run, error) {
	if taskID == "" || instruction == "" || (scope.Kind != ReviewScopePaths && scope.Kind != ReviewScopeRepository) {
		return Run{}, errors.New("scope review task, instruction and scope are required")
	}
	store, ok := s.store.(scopeReviewStore)
	if !ok {
		return Run{}, errors.New("run store does not support Scope Review")
	}
	id, err := s.newID()
	if err != nil {
		return Run{}, fmt.Errorf("allocate run id: %w", err)
	}
	value, err := store.CreateScopeReviewAttempt(ctx, taskID, id, instruction, scope, s.now().UTC())
	logCreated(ctx, value, err)
	return value, err
}

func (s *Service) CreateFocusedScopeReview(ctx context.Context, taskID, instruction string, scope ReviewScope, plan ReviewPlan) (Run, error) {
	if taskID == "" || instruction == "" || (scope.Kind != ReviewScopePaths && scope.Kind != ReviewScopeRepository) || (plan.Mode != ReviewModeFocused && plan.Mode != ReviewModeRepository) {
		return Run{}, errors.New("focused scope review task, instruction, scope and plan are required")
	}
	store, ok := s.store.(focusedScopeReviewStore)
	if !ok {
		return Run{}, errors.New("run store does not support focused Scope Review")
	}
	id, err := s.newID()
	if err != nil {
		return Run{}, err
	}
	value, err := store.CreateFocusedScopeReviewAttempt(ctx, taskID, id, instruction, scope, plan, s.now().UTC())
	logCreated(ctx, value, err)
	return value, err
}

// CreateFocusedScopeReviewForReview allocates a Review-owned attempt. The store
// resolves Project and FeatureArea directly from the durable Review.
func (s *Service) CreateFocusedScopeReviewForReview(ctx context.Context, reviewID, instruction string, scope ReviewScope, plan ReviewPlan) (Run, error) {
	if reviewID == "" || instruction == "" || scope.Kind != ReviewScopeRepository || (plan.Mode != ReviewModeFocused && plan.Mode != ReviewModeRepository) {
		return Run{}, errors.New("Review, instruction, repository scope and plan are required")
	}
	store, ok := s.store.(productScopeReviewStore)
	if !ok {
		return Run{}, errors.New("run store does not support product Review linkage")
	}
	id, err := s.newID()
	if err != nil {
		return Run{}, err
	}
	value, err := store.CreateProductScopeReviewAttempt(ctx, reviewID, id, instruction, scope, plan, s.now().UTC())
	logCreated(ctx, value, err)
	return value, err
}

func (s *Service) GetByInteraction(ctx context.Context, threadID, turnID string) (Run, error) {
	if threadID == "" || turnID == "" {
		return Run{}, errors.New("thread and turn ids are required")
	}
	store, ok := s.store.(interface {
		GetByInteraction(context.Context, string, string) (Run, error)
	})
	if !ok {
		return Run{}, errors.New("run store does not support interaction lookup")
	}
	return store.GetByInteraction(ctx, threadID, turnID)
}

// RecordPrompt persists the exact bytes handed to the backend. It is separate
// from role/purpose metadata so execution routing cannot author prompt prose.
func (s *Service) RecordPrompt(ctx context.Context, id, prompt string, sentAt time.Time) (Run, error) {
	if id == "" || prompt == "" || len(prompt) > 16<<20 || sentAt.IsZero() {
		return Run{}, errors.New("run id, prompt and sent time are required")
	}
	var value Run
	var err error
	if store, ok := s.store.(promptStore); ok {
		value, err = store.RecordPrompt(ctx, id, prompt, sentAt.UTC())
	} else {
		sum := sha256.Sum256([]byte(prompt))
		value, err = s.store.Update(ctx, id, func(value *Run) error {
			if value.FinalCodexPrompt != "" {
				if value.FinalCodexPrompt == prompt {
					return nil
				}
				return errors.New("run prompt is immutable")
			}
			if value.Status != StatusRunning {
				return errors.New("run prompt requires a running Run")
			}
			at := sentAt.UTC()
			value.FinalCodexPrompt = prompt
			value.PromptSHA256 = hex.EncodeToString(sum[:])
			value.PromptBytes = int64(len([]byte(prompt)))
			value.PromptSentAt = &at
			value.UpdatedAt = at
			return nil
		})
	}
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{RunID: id})
		observability.Info(logCtx, "run", "run.prompt.persisted", "exact backend prompt persisted", map[string]any{"purpose": value.Purpose, "prompt": observability.Fingerprint(prompt)})
	}
	return value, err
}

// LatestForTask returns one phase-relevant Run through a targeted store query
// when available. executionProposalID is empty for Feature Area Review.
func (s *Service) LatestForTask(ctx context.Context, taskID string, purpose Purpose, executionProposalID string) (Run, error) {
	if store, ok := s.store.(interface {
		LatestByTaskPurpose(context.Context, string, Purpose, string) (Run, error)
	}); ok {
		v, err := store.LatestByTaskPurpose(ctx, taskID, purpose, executionProposalID)
		if err != nil {
			return v, err
		}
		return s.Get(ctx, v.ID)
	}
	values, err := s.store.List(ctx)
	if err != nil {
		return Run{}, err
	}
	var latest Run
	for _, value := range values {
		if value.TaskID != taskID || value.Purpose != purpose || (executionProposalID != "" && value.ExecutionProposalID != executionProposalID) || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Run{}, ErrNotFound
	}
	return latest, nil
}

func (s *Service) AttachVerificationReport(ctx context.Context, id string, result VerificationReport) (Run, error) {
	if err := ValidateVerificationReportV2(result); err != nil {
		return Run{}, err
	}
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Purpose != PurposeVerification || terminal(value.Status) {
			return errors.New("verification result can only be attached to an active Verification Run")
		}
		if value.VerificationReport != nil {
			return errors.New("verification result is immutable")
		}
		copy := result
		copy.Checks = append([]VerificationCheckOutcome(nil), result.Checks...)
		copy.AcceptanceCriteria = append([]VerificationCriterionOutcome(nil), result.AcceptanceCriteria...)
		value.VerificationReport = &copy
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

// AttachResult persists observed evidence and the exact AgentClaim before a
// verification outcome is known, so a domain verification failure does not
// discard the completed turn's independently readable result.
func (s *Service) AttachResult(ctx context.Context, id string, result Result) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if terminal(value.Status) {
			return errors.New("result can only be attached to an active Run")
		}
		if value.Result != nil {
			return errors.New("run result is immutable")
		}
		copy := result
		copy.GitEvidence.ChangedFiles = append([]string(nil), result.GitEvidence.ChangedFiles...)
		copy.Artifacts = append([]evidence.Artifact(nil), result.Artifacts...)
		value.Result = &copy
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) Transition(ctx context.Context, id string, to Status) (Run, error) {
	var previous Status
	value, err := s.store.Update(ctx, id, func(value *Run) error {
		previous = value.Status
		if !validTransition(value.Status, to) {
			return &TransitionError{From: value.Status, To: to}
		}
		now := s.now().UTC()
		value.Status = to
		value.UpdatedAt = now.UTC()
		if to == StatusRunning && value.StartedAt == nil {
			value.StartedAt = &now
		}
		if terminal(to) {
			if value.FinishedAt == nil {
				value.FinishedAt = &now
			}
			finalizeTokenUsage(value, nil)
		}
		return nil
	})
	logTransition(ctx, value, previous, err)
	s.logCanceledApprovals(ctx, value, err)
	return value, err
}

func (s *Service) AttachWorktree(ctx context.Context, id, worktreeID string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Status != StatusPreparing {
			return &TransitionError{From: value.Status, To: value.Status}
		}
		if worktreeID == "" {
			return errors.New("worktree id is required")
		}
		value.WorktreeID = worktreeID
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) AttachWorkspace(ctx context.Context, id, workspaceID string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Status != StatusPreparing {
			return &TransitionError{From: value.Status, To: value.Status}
		}
		if workspaceID == "" {
			return errors.New("workspace id is required")
		}
		if value.WorkspaceID != "" && value.WorkspaceID != workspaceID {
			return errors.New("run workspace is immutable")
		}
		value.WorkspaceID = workspaceID
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) AttachThread(ctx context.Context, id, threadID string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		_, err := BindThread(value, threadID, s.now())
		return err
	})
}

func (s *Service) AttachTurn(ctx context.Context, id, turnID string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Status != StatusRunning && value.Status != StatusWaitingApproval {
			return &TransitionError{From: value.Status, To: value.Status}
		}
		if turnID == "" {
			return errors.New("turn id is required")
		}
		if value.TurnID != "" && value.TurnID != turnID {
			return errors.New("run turn is immutable")
		}
		value.TurnID = turnID
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) AttachStartSnapshot(ctx context.Context, id, snapshot string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Status != StatusPreparing {
			return &TransitionError{From: value.Status, To: value.Status}
		}
		if !objectID(snapshot) {
			return errors.New("start snapshot must be a full Git object id")
		}
		if value.StartSnapshot != "" && value.StartSnapshot != snapshot {
			return errors.New("run start snapshot is immutable")
		}
		value.StartSnapshot = snapshot
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) AttachEndSnapshot(ctx context.Context, id, snapshot string) (Run, error) {
	return s.store.Update(ctx, id, func(value *Run) error {
		if value.Status == StatusCreated || value.Status == StatusPreparing {
			return &TransitionError{From: value.Status, To: value.Status}
		}
		if !objectID(snapshot) {
			return errors.New("end snapshot must be a full Git object id")
		}
		if value.EndSnapshot != "" && value.EndSnapshot != snapshot {
			return errors.New("run end snapshot is immutable")
		}
		value.EndSnapshot = snapshot
		value.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) Succeed(ctx context.Context, id string, result Result) (Run, error) {
	return s.SucceedWithUsage(ctx, id, result, nil)
}

func (s *Service) SucceedWithUsage(ctx context.Context, id string, result Result, usage *TokenUsageObservation) (Run, error) {
	var previous Status
	value, err := s.store.Update(ctx, id, func(value *Run) error {
		previous = value.Status
		if value.Status != StatusCollecting {
			return &TransitionError{From: value.Status, To: StatusSucceeded}
		}
		now := s.now().UTC()
		value.Status = StatusSucceeded
		value.FinishedAt = &now
		value.UpdatedAt = now.UTC()
		v := result
		value.Result = &v
		finalizeTokenUsage(value, usage)
		return nil
	})
	logUsage(ctx, value, usage)
	logTransition(ctx, value, previous, err)
	s.logCanceledApprovals(ctx, value, err)
	return value, err
}

func (s *Service) Fail(ctx context.Context, id string, failure Failure) (Run, error) {
	return s.FailWithUsage(ctx, id, failure, nil)
}

func (s *Service) FailWithUsage(ctx context.Context, id string, failure Failure, usage *TokenUsageObservation) (Run, error) {
	if failure.Kind != FailureTask && failure.Kind != FailureInfrastructure && failure.Kind != FailureCanceled {
		return Run{}, fmt.Errorf("unknown run failure kind %q", failure.Kind)
	}
	to := StatusFailed
	if failure.Kind == FailureInfrastructure {
		to = StatusInfrastructureFailed
	}
	if failure.Kind == FailureCanceled {
		to = StatusCanceled
	}
	var previous Status
	value, err := s.store.Update(ctx, id, func(value *Run) error {
		previous = value.Status
		if terminal(value.Status) {
			return &TransitionError{From: value.Status, To: to}
		}
		now := s.now().UTC()
		value.Status = to
		value.FinishedAt = &now
		value.UpdatedAt = now.UTC()
		v := failure
		value.Failure = &v
		finalizeTokenUsage(value, usage)
		return nil
	})
	logUsage(ctx, value, usage)
	logTransition(ctx, value, previous, err)
	s.logCanceledApprovals(ctx, value, err)
	return value, err
}

func finalizeTokenUsage(value *Run, observation *TokenUsageObservation) {
	if value.TokenUsage.Status == TokenUsageAvailable || value.TokenUsage.Status == TokenUsageUnavailable {
		return
	}
	value.TokenUsage.Status = TokenUsageUnavailable
	if observation != nil && observation.ModelContextWindow != nil && *observation.ModelContextWindow >= 0 {
		window := *observation.ModelContextWindow
		value.TokenUsage.ModelContextWindow = &window
	}
	if observation == nil || !observation.Reliable || !validCounts(observation.StartCumulative) || !validCounts(observation.EndCumulative) {
		return
	}
	delta, ok := subtractCounts(observation.EndCumulative, observation.StartCumulative)
	if !ok {
		return
	}
	start, end := observation.StartCumulative, observation.EndCumulative
	value.TokenUsage.Status = TokenUsageAvailable
	value.TokenUsage.StartCumulative = start
	value.TokenUsage.EndCumulative = &end
	value.TokenUsage.Usage = &delta
}

func subtractCounts(end, start TokenCounts) (TokenCounts, bool) {
	if end.InputTokens < start.InputTokens || end.CachedInputTokens < start.CachedInputTokens ||
		end.CacheWriteInputTokens < start.CacheWriteInputTokens || end.OutputTokens < start.OutputTokens ||
		end.ReasoningOutputTokens < start.ReasoningOutputTokens || end.TotalTokens < start.TotalTokens {
		return TokenCounts{}, false
	}
	return TokenCounts{
		InputTokens:           end.InputTokens - start.InputTokens,
		CachedInputTokens:     end.CachedInputTokens - start.CachedInputTokens,
		CacheWriteInputTokens: end.CacheWriteInputTokens - start.CacheWriteInputTokens,
		OutputTokens:          end.OutputTokens - start.OutputTokens,
		ReasoningOutputTokens: end.ReasoningOutputTokens - start.ReasoningOutputTokens,
		TotalTokens:           end.TotalTokens - start.TotalTokens,
	}, true
}

func validCounts(value TokenCounts) bool {
	return value.InputTokens >= 0 && value.CachedInputTokens >= 0 && value.CacheWriteInputTokens >= 0 &&
		value.OutputTokens >= 0 && value.ReasoningOutputTokens >= 0 && value.TotalTokens >= 0
}

func objectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func validTransition(from, to Status) bool {
	switch from {
	case StatusCreated:
		return to == StatusPreparing
	case StatusPreparing:
		return to == StatusRunning
	case StatusRunning:
		return to == StatusWaitingApproval || to == StatusCollecting
	case StatusWaitingApproval:
		return to == StatusRunning
	case StatusCollecting:
		return to == StatusSucceeded
	default:
		return false
	}
}
func terminal(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusInfrastructureFailed || status == StatusCanceled
}
func (s *Service) Get(ctx context.Context, id string) (Run, error) {
	v, err := s.store.Get(ctx, id)
	if err == nil && s.AgentRuntime != nil {
		state, e := s.AgentRuntime.Read(ctx, id)
		if e != nil {
			state = unavailableRuntime(id)
		}
		v.AgentRuntime = &state
	}
	return v, err
}
func (s *Service) List(ctx context.Context) ([]Run, error) {
	values, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if s.AgentRuntime != nil {
		for i := range values {
			state, e := s.AgentRuntime.Read(ctx, values[i].ID)
			if e != nil {
				state = unavailableRuntime(values[i].ID)
			}
			values[i].AgentRuntime = &state
		}
	}
	return values, nil
}
func (s *Service) ResolveAgentRequest(ctx context.Context, in agentruntime.Resolution) (agentruntime.Snapshot, error) {
	if s.AgentRuntime == nil {
		return agentruntime.Snapshot{}, agentruntime.ErrStale
	}
	return s.AgentRuntime.Resolve(ctx, in)
}
func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(raw[:]), nil
}

// ListByTask is a scoped read across every attempt and Execution of one Task.
func (s *Service) ListByTask(ctx context.Context, taskID string) ([]Run, error) {
	var values []Run
	var err error
	if reader, ok := s.store.(interface {
		ListByTask(context.Context, string) ([]Run, error)
	}); ok {
		values, err = reader.ListByTask(ctx, taskID)
	} else {
		values, err = s.store.List(ctx)
	}
	if err != nil {
		return nil, err
	}
	out := []Run{}
	for _, v := range values {
		if v.TaskID != taskID {
			continue
		}
		if s.AgentRuntime != nil {
			state, e := s.AgentRuntime.Read(ctx, v.ID)
			if e != nil {
				state = unavailableRuntime(v.ID)
			}
			v.AgentRuntime = &state
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) SubmitUserDecision(ctx context.Context, in agentruntime.UserDecisionInput) (agentruntime.UserDecision, error) {
	if s.AgentRuntime == nil {
		return agentruntime.UserDecision{}, agentruntime.ErrStale
	}
	return s.AgentRuntime.SubmitUserDecision(ctx, in)
}

func (s *Service) ReadExecutionPreparationPlan(ctx context.Context, runID string) (runtimepreparation.Plan, error) {
	store, ok := s.store.(interface {
		ReadExecutionPreparationPlan(context.Context, string) (runtimepreparation.Plan, error)
	})
	if !ok {
		return runtimepreparation.Plan{}, ErrNotFound
	}
	return store.ReadExecutionPreparationPlan(ctx, runID)
}

func (s *Service) ReadWorkspaceObservations(ctx context.Context, planID, runID string) ([]runtimepreparation.WorkspaceObservation, error) {
	store, ok := s.store.(interface {
		ReadWorkspaceObservations(context.Context, string, string) ([]runtimepreparation.WorkspaceObservation, error)
	})
	if !ok {
		return nil, errors.New("durable workspace observation readback unavailable")
	}
	return store.ReadWorkspaceObservations(ctx, planID, runID)
}
