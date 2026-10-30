// Package review owns the durable product-level engineering Review. It is
// deliberately separate from the internal Run which may perform its work.
package review

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

var (
	ErrNotFound        = errors.New("review not found")
	ErrConflict        = errors.New("review conflict")
	ErrInvalidArgument = errors.New("invalid review argument")
	ErrRequestConflict = errors.New("Review requestId was already used with a different payload")
)

// AdmissionBusyError is a rejected Review start command, not a Review. The
// immutable owner identity lets adapters explain exactly which legitimate
// Feature Area operation must finish without suggesting recovery.
type DepthTooShallowError struct {
	RequestedDepth      task.ReviewDepth `json:"requestedDepth"`
	RequiredReviewDepth task.ReviewDepth `json:"requiredReviewDepth"`
	TaskID              string           `json:"taskId"`
	Phase               task.Phase       `json:"phase"`
}

func (e *DepthTooShallowError) Error() string { return "REVIEW_DEPTH_TOO_SHALLOW" }

type AdmissionBusyError struct {
	Owner agentconversation.Claim
}

func (e *AdmissionBusyError) Error() string {
	return fmt.Sprintf("%s: active %s operation %s owns Run %s", agentconversation.ErrBusy, e.Owner.Kind, e.Owner.OperationID, e.Owner.RunID)
}
func (e *AdmissionBusyError) Unwrap() error { return agentconversation.ErrBusy }

type FocusKind string

const (
	FocusFocused    FocusKind = "FOCUSED"
	FocusRepository FocusKind = "REPOSITORY"
)

// Focus describes the product request, independently of any Run scope.
type Focus struct {
	Kind    FocusKind
	Summary string
	Paths   []string
	Modules []run.ReviewModuleSelection
}
type StartFailure struct {
	Code       string
	Blocker    string
	RecordedAt time.Time
}
type AccessOutcome string

const (
	AccessSuccess   AccessOutcome = "SUCCESS"
	AccessForbidden AccessOutcome = "FORBIDDEN"
	AccessFailure   AccessOutcome = "FAILURE"
)

// AccessEvidence deliberately contains no request/response body.
type AccessEvidence struct {
	ID         string
	ReviewID   string
	Operation  string
	Outcome    AccessOutcome
	Code       string
	RecordedAt time.Time
}
type EvidenceSummary struct {
	AuthorizationGranted           bool
	Successes, Forbidden, Failures int
	LastObservedAt                 *time.Time
}
type ReviewDepth = task.ReviewDepth

type AuthorKind string

const (
	AuthorAISession     AuthorKind = "AI_SESSION"
	AuthorLocalOperator AuthorKind = "LOCAL_OPERATOR"
)

const (
	ReviewDepthFast = task.ReviewDepthFast
	ReviewDepthDeep = task.ReviewDepthDeep
)

type Review struct {
	WorkspaceID                                               string           `json:"workspaceId,omitempty"`
	RepositoryTargets                                         []workset.Target `json:"repositoryTargets,omitempty"`
	PrimaryMemberID                                           string           `json:"primaryMemberId,omitempty"`
	ForbiddenPaths                                            []string
	ExecutorKind                                              ExecutorKind
	ExecutorMetadata                                          *ExecutorMetadata
	UpperAccess                                               *UpperAccess
	ReportID                                                  string
	ReviewDepth                                               ReviewDepth
	ReviewDisposition                                         Disposition
	Ordinal                                                   int
	TaskID                                                    string
	TerminalStatus                                            string
	ID, ProjectID, FeatureAreaID, CreatedByAISessionID, RunID string
	AuthorKind, AuthorID                                      string
	SupplementalRequestID                                     string
	StartRequestID                                            string
	RepositoryID, CodexConfigID, BaseCommit                   string
	BackendSelection                                          *codexconfig.BackendSelection
	RepositoryProvenance                                      *RepositoryProvenance
	RuntimeConfig                                             codexruntime.NextRunRuntimeConfig
	RuntimeConfirmationID                                     string
	Model                                                     string
	ReasoningEffort                                           string
	CreatedAt                                                 time.Time
	Focus                                                     Focus
	EngineeringGoal, Instruction                              string
	FinalCodexPrompt, PromptSHA256                            string
	PromptBytes                                               int64
	PromptSentAt                                              *time.Time
	StartFailure                                              *StartFailure
	ContinuationAttemptedAt                                   *time.Time
}

// StartAdmission is the durable result of a Review start command. Replayed is
// true when the same requestId and payload returned the original accepted
// Review/Run rather than starting another runtime handoff.
type StartAdmission struct {
	Review   Review
	Run      run.Run
	Replayed bool
}

// AuthorizedStart is the exact durable Review/Run pair that an asynchronous
// continuation worker may dispatch. It is read from the accepted admission
// receipt; workers must never construct a Review candidate from this value.
type AuthorizedStart struct {
	Admission     StartAdmission
	RequestID     string
	PayloadSHA256 string
}

type admissionStore interface {
	AdmitStart(context.Context, Review, string, string, string, string, run.ReviewScope, run.ReviewPlan) (StartAdmission, error)
}

type Store interface {
	Create(context.Context, Review) error
	Get(context.Context, string) (Review, error)
	ListByFeatureArea(context.Context, string) ([]Review, error)
	LinkRun(context.Context, string, string) error
	RecordPrompt(context.Context, string, string, time.Time) (Review, error)
	RecordAccess(context.Context, AccessEvidence) error
	SummarizeEvidence(context.Context, string) (EvidenceSummary, error)
	RecordStartFailure(context.Context, string, StartFailure) error
	ClaimContinuation(context.Context, string, time.Time) (bool, error)
}

type Service struct {
	store Store
	newID func() (string, error)
	now   func() time.Time
}

func NewService(store Store, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, ErrInvalidArgument
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store, newID, now}, nil
}

func (s *Service) ListByFeatureArea(ctx context.Context, featureAreaID string) ([]Review, error) {
	if featureAreaID == "" {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.ListByFeatureArea(ctx, featureAreaID)
	for index := range values {
		values[index] = clone(values[index])
	}
	return values, err
}

func (s *Service) LinkRun(ctx context.Context, reviewID, runID string) error {
	if reviewID == "" || runID == "" {
		return ErrInvalidArgument
	}
	err := s.store.LinkRun(ctx, reviewID, runID)
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{ReviewID: reviewID, RunID: runID})
		observability.Info(logCtx, "review", "review.run_linked", "product Review linked to internal Run", nil)
	}
	return err
}

func (s *Service) RecordPrompt(ctx context.Context, reviewID, prompt string, sentAt time.Time) (Review, error) {
	if reviewID == "" || sentAt.IsZero() {
		return Review{}, ErrInvalidArgument
	}
	value, err := s.store.RecordPrompt(ctx, reviewID, prompt, sentAt.UTC())
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ReviewID: reviewID, RunID: value.RunID})
		observability.Info(logCtx, "review", "review.prompt.persisted", "exact local Review prompt persisted", map[string]any{"prompt": observability.Fingerprint(prompt)})
	}
	return value, err
}

func (s *Service) RecordAccess(ctx context.Context, reviewID, operation string, outcome AccessOutcome, code string) error {
	if reviewID == "" {
		return ErrInvalidArgument
	}
	id, err := s.newID()
	if err != nil {
		return fmt.Errorf("allocate Review evidence id: %w", err)
	}
	return s.store.RecordAccess(ctx, AccessEvidence{ID: "review_access_" + strings.TrimPrefix(id, "review_access_"), ReviewID: reviewID, Operation: operation, Outcome: outcome, Code: code, RecordedAt: s.now().UTC()})
}

func (s *Service) SummarizeEvidence(ctx context.Context, reviewID string) (EvidenceSummary, error) {
	if reviewID == "" {
		return EvidenceSummary{}, ErrInvalidArgument
	}
	return s.store.SummarizeEvidence(ctx, reviewID)
}

func (s *Service) RecordStartFailure(ctx context.Context, reviewID, code, blocker string) error {
	if reviewID == "" {
		return ErrInvalidArgument
	}
	err := s.store.RecordStartFailure(ctx, reviewID, StartFailure{Code: code, Blocker: blocker, RecordedAt: s.now().UTC()})
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{ReviewID: reviewID})
		observability.Info(logCtx, "review", "review.start_failed", "product Review start failed", map[string]any{"errorCode": code})
	}
	return err
}

// ClaimContinuation durably records the one semantic continuation attempt for
// a terminal Review. A false result is a duplicate observation.
func (s *Service) ClaimContinuation(ctx context.Context, reviewID string) (bool, error) {
	if reviewID == "" {
		return false, ErrInvalidArgument
	}
	return s.store.ClaimContinuation(ctx, reviewID, s.now().UTC())
}
func (s *Service) Create(ctx context.Context, value Review) (Review, error) {
	value, err := s.Prepare(value)
	if err != nil {
		return Review{}, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: value.CreatedByAISessionID, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ID})
	observability.Info(logCtx, "review", "featurearea.review.start.requested", "product Review start requested", map[string]any{"focusMode": value.Focus.Kind, "focusPathCount": len(value.Focus.Paths), "goal": observability.Fingerprint(value.EngineeringGoal), "instruction": observability.Fingerprint(value.Instruction)})
	if err := s.store.Create(ctx, value); err != nil {
		return Review{}, err
	}
	observability.Info(logCtx, "review", "review.created", "durable product Review created", map[string]any{"focusMode": value.Focus.Kind, "focusPathCount": len(value.Focus.Paths)})
	return clone(value), nil
}

// Prepare allocates and validates an immutable Review without persisting it.
// Admission-aware callers pass this candidate to AdmitStart, whose single
// store transaction decides whether it becomes formal Review history.
func (s *Service) Prepare(value Review) (Review, error) {
	if len(value.RepositoryTargets) != 0 {
		if !value.ExecutorKind.Effective().Valid() || value.RepositoryID != "" || value.BaseCommit != "" || value.WorkspaceID == "" {
			return Review{}, ErrInvalidArgument
		}
		if err := workset.ValidateTargets(value.RepositoryTargets); err != nil {
			return Review{}, err
		}
		for _, target := range value.RepositoryTargets {
			if target.Write.Kind != "NONE" {
				return Review{}, ErrInvalidArgument
			}
		}
	}
	if value.AuthorKind == "" && value.AuthorID == "" && value.CreatedByAISessionID != "" {
		value.AuthorKind, value.AuthorID = string(AuthorAISession), value.CreatedByAISessionID
	}
	if value.AuthorKind == string(AuthorAISession) {
		if value.AuthorID == "" || value.CreatedByAISessionID != value.AuthorID {
			return Review{}, ErrInvalidArgument
		}
	} else if value.AuthorKind == string(AuthorLocalOperator) {
		if value.AuthorID == "" || value.CreatedByAISessionID != "" {
			return Review{}, ErrInvalidArgument
		}
	} else {
		return Review{}, ErrInvalidArgument
	}
	value.ExecutorKind = value.ExecutorKind.Effective()
	if value.ExecutorKind == ExecutorCodingAgent || value.ExecutorKind == ExecutorPiAgent {
		// Legacy local callers already choose model/effort explicitly; capture
		// that selection at creation, never reconstruct historical records.
		if value.RuntimeConfig == (codexruntime.NextRunRuntimeConfig{}) && value.RuntimeConfirmationID == "" {
			value.RuntimeConfig = codexruntime.NextRunRuntimeConfig{Model: value.Model, ReasoningEffort: value.ReasoningEffort}
		}
		if agentpolicy.ValidateFrozenSelection(value.RuntimeConfig.AgentID, value.RuntimeConfig.ExecutionPolicy, value.RuntimeConfig.AgentConfigFingerprint) != nil || value.ExecutorKind == ExecutorPiAgent && value.RuntimeConfig.AgentID != "" && value.RuntimeConfig.AgentID != "pi" {
			return Review{}, ErrInvalidArgument
		}
		if value.RuntimeConfig.Model == "" || value.RuntimeConfig.ReasoningEffort == "" {
			return Review{}, ErrInvalidArgument
		}
		if value.Model != "" && value.Model != value.RuntimeConfig.Model || value.ReasoningEffort != "" && value.ReasoningEffort != value.RuntimeConfig.ReasoningEffort {
			return Review{}, ErrInvalidArgument
		}
		value.Model, value.ReasoningEffort = value.RuntimeConfig.Model, value.RuntimeConfig.ReasoningEffort
		// PI Agent doesn't use CodexConfigID or BackendSelection
		if value.ExecutorKind == ExecutorCodingAgent {
			if value.BackendSelection == nil || value.BackendSelection.ConfigID == "" || value.BackendSelection.ConfigFingerprint == "" || value.BackendSelection.ConfigID != value.CodexConfigID {
				return Review{}, ErrInvalidArgument
			}
		}
	} else if value.RuntimeConfig != (codexruntime.NextRunRuntimeConfig{}) {
		return Review{}, ErrExecutorMismatch
	}
	if value.ExecutorKind == ExecutorUpperLLM && value.ExecutorMetadata == nil {
		value.ExecutorMetadata = &ExecutorMetadata{Provenance: "UNAVAILABLE"}
	}
	if value.ID == "" {
		id, err := s.newID()
		if err != nil {
			return Review{}, err
		}
		value.ID = "review_" + strings.TrimPrefix(id, "review_")
	}
	value.CreatedAt = s.now().UTC()
	if err := validate(value); err != nil {
		return Review{}, err
	}
	return clone(value), nil
}

// AdmitStart durably applies one idempotent Review start command. Busy
// admission is recorded as a command receipt and creates no Review or Run.
func (s *Service) AdmitStart(ctx context.Context, value Review, requestID, payloadSHA256, instruction string, scope run.ReviewScope, plan run.ReviewPlan) (StartAdmission, error) {
	store, ok := s.store.(admissionStore)
	if !ok {
		return StartAdmission{}, errors.New("Review admission store is unavailable")
	}
	digest, digestErr := hex.DecodeString(payloadSHA256)
	if instruction != value.Instruction || strings.TrimSpace(instruction) == "" || requestID == "" || len(requestID) > 128 || digestErr != nil || len(digest) != sha256.Size {
		return StartAdmission{}, ErrInvalidArgument
	}
	prepared, err := s.Prepare(value)
	if err != nil {
		return StartAdmission{}, err
	}
	if len(prepared.RepositoryTargets) == 0 {
		if prepared.RepositoryProvenance == nil {
			prepared.RepositoryProvenance = &RepositoryProvenance{ReviewID: prepared.ID, RepositoryID: prepared.RepositoryID, Commit: prepared.BaseCommit, RepositoryHEAD: prepared.BaseCommit, BranchKind: ProvenanceUnknown, InspectionMode: InspectionFeatureAreaWorkspace, CapturedAt: prepared.CreatedAt}
		} else {
			prepared.RepositoryProvenance.ReviewID = prepared.ID
		}
		if err := validateRepositoryProvenance(prepared, *prepared.RepositoryProvenance); err != nil {
			return StartAdmission{}, err
		}
	}
	rawRunID, err := s.newID()
	if err != nil {
		return StartAdmission{}, err
	}
	runID := "run_" + strings.TrimPrefix(strings.TrimPrefix(rawRunID, "review_"), "run_")
	logCtx := observability.WithContext(ctx, observability.Correlation{RequestID: requestID, ProjectID: prepared.ProjectID, AISessionID: prepared.CreatedByAISessionID, FeatureAreaID: prepared.FeatureAreaID, ReviewID: prepared.ID, RunID: runID})
	observability.Info(logCtx, "review", "featurearea.review.start.requested", "Feature Area Review admission requested", map[string]any{"focusMode": prepared.Focus.Kind, "focusPathCount": len(prepared.Focus.Paths)})
	result, err := store.AdmitStart(ctx, prepared, requestID, payloadSHA256, runID, instruction, scope, plan)
	if err != nil {
		return result, err
	}
	if !result.Replayed {
		logCtx = observability.WithContext(logCtx, observability.Correlation{TaskID: result.Run.TaskID, ExecutionID: result.Run.AgentExecutionID, RunID: result.Run.ID})
		observability.Info(logCtx, "review", "review.created", "admitted durable product Review and Run created", map[string]any{"focusMode": prepared.Focus.Kind, "focusPathCount": len(prepared.Focus.Paths)})
		observability.Info(logCtx, "run", "run.created", "durable Review Run created by atomic admission", map[string]any{"purpose": result.Run.Purpose, "status": result.Run.Status})
	}
	return result, nil
}

func validateRepositoryProvenance(value Review, provenance RepositoryProvenance) error {
	if provenance.ReviewID != value.ID || provenance.RepositoryID != value.RepositoryID || provenance.Commit != value.BaseCommit || provenance.RepositoryHEAD != value.BaseCommit || provenance.CapturedAt.IsZero() || provenance.InspectionMode != InspectionFeatureAreaWorkspace {
		return ErrInvalidArgument
	}
	switch provenance.BranchKind {
	case ProvenanceBranch:
		if !strings.HasPrefix(provenance.BranchRef, "refs/heads/") {
			return ErrInvalidArgument
		}
	case ProvenanceDetached, ProvenanceUnknown:
		if provenance.BranchRef != "" {
			return ErrInvalidArgument
		}
	default:
		return ErrInvalidArgument
	}
	return nil
}
func (s *Service) Get(ctx context.Context, id string) (Review, error) {
	if id == "" {
		return Review{}, ErrInvalidArgument
	}
	v, e := s.store.Get(ctx, id)
	return clone(v), e
}
func validate(v Review) error {
	if !v.ExecutorKind.Effective().Valid() {
		return ErrExecutorMismatch
	}
	if v.ExecutorKind.Effective() == ExecutorCodingAgent && v.ForbiddenPaths != nil {
		return ErrExecutorMismatch
	}
	if len(v.ForbiddenPaths) > 128 {
		return ErrInvalidArgument
	}
	for _, p := range v.ForbiddenPaths {
		if p == "" || len(p) > 1024 || p == "." || p == ".." || strings.HasPrefix(p, "/") || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "\\\x00\r\n") || path.Clean(p) != p {
			return ErrInvalidArgument
		}
	}
	if v.ExecutorKind == ExecutorUpperLLM && (v.Model != "" || v.ReasoningEffort != "" || v.CodexConfigID != "" || v.BackendSelection != nil) {
		return ErrExecutorMismatch
	}
	if v.ExecutorKind == ExecutorPiAgent && v.CodexConfigID != "" {
		return ErrExecutorMismatch
	}
	if v.ReviewDepth != ReviewDepthFast && v.ReviewDepth != ReviewDepthDeep {
		return ErrInvalidArgument
	}
	if !strings.HasPrefix(v.ID, "review_") || len(v.ID) <= len("review_") || len(v.ID) > 256 || v.TaskID == "" || v.ProjectID == "" || v.FeatureAreaID == "" || v.AuthorKind == "" || v.AuthorID == "" || (v.AuthorKind == string(AuthorAISession) && v.CreatedByAISessionID == "") || (v.AuthorKind == string(AuthorLocalOperator) && v.CreatedByAISessionID != "") || (len(v.RepositoryTargets) == 0 && v.RepositoryID == "") || (v.ExecutorKind.Effective() == ExecutorCodingAgent && v.CodexConfigID == "") || (v.ExecutorKind.Effective() == ExecutorPiAgent && v.CodexConfigID != "") || (len(v.RepositoryTargets) == 0 && !validBaseCommit(v.BaseCommit)) || len(v.EngineeringGoal) == 0 || len(v.EngineeringGoal) > maxInstructionBytes || strings.TrimSpace(v.Instruction) == "" || strings.ContainsRune(v.Instruction, '\x00') || len(v.Instruction) > maxInstructionBytes || len(v.Focus.Summary) > 4096 {
		return ErrInvalidArgument
	}
	if v.Focus.Kind != FocusFocused && v.Focus.Kind != FocusRepository {
		return ErrInvalidArgument
	}
	if v.Focus.Kind == FocusRepository && (len(v.Focus.Paths) != 0 || len(v.Focus.Modules) != 0) {
		return ErrInvalidArgument
	}
	if len(v.Focus.Paths) > 128 {
		return ErrInvalidArgument
	}
	if (v.Model != "" && codexruntime.ValidateModelID(v.Model) != nil) || !validSelection(v.ReasoningEffort, codexruntime.AllReasoningEfforts) {
		return ErrInvalidArgument
	}
	for _, p := range v.Focus.Paths {
		if strings.TrimSpace(p) == "" || len(p) > 1024 {
			return ErrInvalidArgument
		}
	}
	seenModules := map[string]bool{}
	for _, selected := range v.Focus.Modules {
		if selected.Key == "" || seenModules[selected.Key] || (selected.Role != run.ReviewModulePrimary && selected.Role != run.ReviewModuleSupporting) {
			return ErrInvalidArgument
		}
		seenModules[selected.Key] = true
	}
	return nil
}
func validSelection(value string, closed []string) bool {
	if value == "" {
		return true
	}
	for _, allowed := range closed {
		if value == allowed {
			return true
		}
	}
	return false
}
func clone(v Review) Review {
	v.RepositoryTargets = workset.CloneTargets(v.RepositoryTargets)
	v.ForbiddenPaths = append([]string(nil), v.ForbiddenPaths...)
	if v.BackendSelection != nil {
		x := *v.BackendSelection
		v.BackendSelection = &x
	}
	if v.ExecutorMetadata != nil {
		x := *v.ExecutorMetadata
		v.ExecutorMetadata = &x
	}
	if v.UpperAccess != nil {
		x := *v.UpperAccess
		x.AllowedPaths = append([]string{}, x.AllowedPaths...)
		x.ForbiddenPaths = append([]string{}, x.ForbiddenPaths...)
		if x.LastObservedAt != nil {
			observed := *x.LastObservedAt
			x.LastObservedAt = &observed
		}
		v.UpperAccess = &x
	}
	v.Focus.Paths = append([]string(nil), v.Focus.Paths...)
	v.Focus.Modules = append([]run.ReviewModuleSelection(nil), v.Focus.Modules...)
	if v.PromptSentAt != nil {
		x := *v.PromptSentAt
		v.PromptSentAt = &x
	}
	if v.StartFailure != nil {
		x := *v.StartFailure
		v.StartFailure = &x
	}
	if v.ContinuationAttemptedAt != nil {
		x := *v.ContinuationAttemptedAt
		v.ContinuationAttemptedAt = &x
	}
	if v.RepositoryProvenance != nil {
		x := *v.RepositoryProvenance
		if x.WorkingTreeDirtyAtAdmission != nil {
			dirty := *x.WorkingTreeDirtyAtAdmission
			x.WorkingTreeDirtyAtAdmission = &dirty
		}
		v.RepositoryProvenance = &x
	}
	return v
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validBaseCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
