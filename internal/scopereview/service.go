// Package scopereview starts read-only Runs from durable Review ownership and focus.
package scopereview

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
)

const (
	MaxInstructionBytes = 64 << 10
	MaxPathCount        = 128
	MaxPathBytes        = 4096
)

var (
	ErrInvalidArgument = errors.New("invalid Scope Review argument")
	ErrNotFound        = errors.New("Scope Review entity not found")
	ErrConflict        = errors.New("Scope Review conflict")
)

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}
type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}
type ProductFocusedRunService interface {
	CreateFocusedScopeReviewForReview(context.Context, string, string, run.ReviewScope, run.ReviewPlan) (run.Run, error)
}

type InitialGrantService interface {
	Ensure(context.Context, reviewgrant.Grant) (reviewgrant.Grant, error)
}

type StartInput struct {
	AISessionID string
	ReviewID    string
}

type ReviewLookup interface {
	Get(context.Context, string) (productreview.Review, error)
}
type ProductStarter interface {
	StartReview(context.Context, string, func(context.Context) (run.Run, error)) (run.Run, error)
}

type admittedReviewService interface {
	AdmitStart(context.Context, productreview.Review, string, string, string, run.ReviewScope, run.ReviewPlan) (productreview.StartAdmission, error)
}

type reservedProductStarter interface {
	StartReservedReview(context.Context, string, run.Run) (run.Run, error)
}

// AuthorizedStartQueue exposes only exact Review/Run reservations whose
// durable admission was committed but whose existing runtime handoff has not
// started. Implementations must bind each row to its accepted request receipt.
type AuthorizedStartQueue interface {
	PendingAuthorizedReviewStarts(context.Context, string) ([]productreview.AuthorizedStart, error)
}

// UpperStarter prepares a managed workspace for an externally driven review.
// It must not invoke the Coding Agent runtime.
type UpperStarter interface {
	StartUpperReview(context.Context, productreview.Review, run.Run) (run.Run, error)
}

type AdmittedStartInput struct {
	AISessionID         string
	OperatorPrincipalID string
	RequestID           string
	PayloadSHA256       string
	Review              productreview.Review
}

type Service struct {
	reviews      ReviewLookup
	sessions     SessionLookup
	repositories RepositoryLookup
	runs         ProductFocusedRunService
	starter      ProductStarter
	grants       InitialGrantService
	provenance   ProvenanceRecorder
	upperStarter UpperStarter
	startQueue   AuthorizedStartQueue
}

func (s *Service) SetReviews(reviews ReviewLookup) { s.reviews = reviews }

func (s *Service) SetInitialGrantService(grants InitialGrantService)  { s.grants = grants }
func (s *Service) SetUpperStarter(starter UpperStarter)               { s.upperStarter = starter }
func (s *Service) SetAuthorizedStartQueue(queue AuthorizedStartQueue) { s.startQueue = queue }

// AuthorizedContinuation first prepares already confirmed Recovery-purpose
// Reviews from their frozen plans, then dispatches exact persisted reservations.
// Runtime handoff fencing and continuity remain owned by the existing starters.
func (s *Service) AuthorizedContinuation() func(context.Context) {
	after := ""
	preparationAfter := ""
	return func(ctx context.Context) {
		if s == nil || s.startQueue == nil {
			return
		}
		if preparer, ok := s.startQueue.(interface {
			ContinueRecoveryReviewPreparations(context.Context, string) (string, error)
		}); ok {
			var prepareErr error
			preparationAfter, prepareErr = preparer.ContinueRecoveryReviewPreparations(ctx, preparationAfter)
			if prepareErr != nil {
				observability.Error(ctx, "review", "review.purpose.preparation_failed", "Confirmed Review purpose preparation could not converge", map[string]any{"errorSummary": prepareErr.Error()})
			}
		}
		pending, err := s.startQueue.PendingAuthorizedReviewStarts(ctx, after)
		if err != nil {
			observability.Error(ctx, "review", "review.continuation.read_failed", "authorized Review continuation read failed", map[string]any{"errorCode": "REVIEW_CONTINUATION_READ_FAILED"})
			return
		}
		for _, authorized := range pending {
			if ctx.Err() != nil {
				return
			}
			admission := authorized.Admission
			value, reserved := admission.Review, admission.Run
			_, payloadHashErr := hex.DecodeString(authorized.PayloadSHA256)
			if !admission.Replayed || authorized.RequestID == "" || authorized.RequestID != value.StartRequestID || len(authorized.PayloadSHA256) != 64 || payloadHashErr != nil || value.ID == "" || value.TaskID == "" || value.RunID == "" || reserved.ID != value.RunID || reserved.ReviewID != value.ID || reserved.TaskID != value.TaskID || reserved.ProjectID != value.ProjectID || reserved.FeatureAreaID != value.FeatureAreaID || reserved.OperationKind != run.OperationReview || reserved.OperationID != value.ID || reserved.Purpose != run.PurposeScopeReview {
				observability.Error(ctx, "review", "review.continuation.authority_invalid", "authorized Review continuation did not match its durable reservation", map[string]any{"errorCode": "REVIEW_CONTINUATION_AUTHORITY_INVALID", "reviewId": value.ID, "runId": reserved.ID})
				after = value.ID
				continue
			}
			_, startErr := s.dispatchAuthorized(ctx, admission)
			if startErr != nil {
				observability.Error(ctx, "review", "review.continuation.handoff_failed", "authorized Review handoff did not converge", map[string]any{"errorCode": "REVIEW_CONTINUATION_HANDOFF_FAILED", "reviewId": value.ID, "runId": reserved.ID})
			}
			after = value.ID
		}
		if len(pending) < 16 {
			after = ""
		}
	}
}

func (s *Service) dispatchAuthorized(ctx context.Context, admission productreview.StartAdmission) (productreview.StartAdmission, error) {
	result := admission
	if result.Review.ExecutorKind.Effective() == productreview.ExecutorUpperLLM {
		if s.upperStarter == nil {
			return result, errors.New("Upper Review workspace preparation is unavailable")
		}
		started, err := s.upperStarter.StartUpperReview(ctx, result.Review, result.Run)
		if started.ID != "" {
			result.Run = started
		}
		return result, err
	}
	starter, ok := s.starter.(reservedProductStarter)
	if !ok {
		return result, errors.New("admitted Review runtime handoff is unavailable")
	}
	started, err := starter.StartReservedReview(ctx, result.Review.ID, result.Run)
	result.Run = started
	return result, err
}

// ProvenanceRecorder durably records the repository state a product Review
// is based on. It is optional: legacy callers without a recorder behave as
// before, and historical Reviews carry no provenance.
type ProvenanceRecorder interface {
	RecordRepositoryProvenance(context.Context, productreview.RepositoryProvenance) error
}

func NewService(sessions SessionLookup, repositories RepositoryLookup, runs ProductFocusedRunService, starter ProductStarter, provenance ProvenanceRecorder) (*Service, error) {
	if sessions == nil || repositories == nil || runs == nil || starter == nil {
		return nil, errors.New("Scope Review dependencies are incomplete")
	}
	return &Service{sessions: sessions, repositories: repositories, runs: runs, starter: starter, provenance: provenance}, nil
}

func (s *Service) Start(ctx context.Context, input StartInput) (run.Run, error) {
	return s.startReview(ctx, input)
}

// StartAdmitted validates the complete candidate before the store atomically
// decides admission. A busy result is only a command receipt; no Review or Run
// exists. Accepted replay returns its original correlation without a second
// runtime handoff.
func (s *Service) StartAdmitted(ctx context.Context, input AdmittedStartInput) (productreview.StartAdmission, error) {
	if input.RequestID == "" || input.PayloadSHA256 == "" || s.reviews == nil {
		return productreview.StartAdmission{}, ErrInvalidArgument
	}
	if input.AISessionID != "" {
		if input.Review.AuthorKind == "" && input.Review.AuthorID == "" {
			input.Review.AuthorKind, input.Review.AuthorID = string(productreview.AuthorAISession), input.AISessionID
		}
		session, err := s.sessions.Get(ctx, input.AISessionID)
		if err != nil || input.Review.ProjectID != session.ProjectID || input.Review.CreatedByAISessionID != input.AISessionID || input.Review.AuthorKind != string(productreview.AuthorAISession) || input.Review.AuthorID != input.AISessionID || input.Review.TaskID == "" {
			return productreview.StartAdmission{}, ErrNotFound
		}
	} else {
		actor, ok := localoperator.FromContext(ctx)
		if !ok || input.OperatorPrincipalID == "" || actor.PrincipalID() != input.OperatorPrincipalID || actor.ProjectID() != input.Review.ProjectID || input.Review.CreatedByAISessionID != "" || input.Review.AuthorKind != string(productreview.AuthorLocalOperator) || input.Review.AuthorID != input.OperatorPrincipalID || input.Review.TaskID == "" {
			return productreview.StartAdmission{}, ErrNotFound
		}
	}
	var repositoryRoot string
	var err error
	ids := []string{input.Review.RepositoryID}
	if len(input.Review.RepositoryTargets) > 0 {
		ids = nil
		for _, target := range input.Review.RepositoryTargets {
			ids = append(ids, target.RepositoryID)
		}
	}
	for _, id := range ids {
		repo, e := s.repositories.Get(ctx, id)
		if e != nil || repo.ProjectID != input.Review.ProjectID {
			return productreview.StartAdmission{}, ErrNotFound
		}
		if len(ids) == 1 {
			repositoryRoot = repo.Path
		}
	}
	instruction := input.Review.Instruction
	if ValidateInstruction(instruction) != nil {
		return productreview.StartAdmission{}, ErrInvalidArgument
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	initial := scope
	if input.Review.Focus.Kind == productreview.FocusFocused && len(input.Review.Focus.Paths) > 0 {
		initial, err = CanonicalizeScope(repositoryRoot, run.ReviewScope{Kind: run.ReviewScopePaths, Paths: input.Review.Focus.Paths})
		if err != nil {
			return productreview.StartAdmission{}, err
		}
	}
	plan := run.ReviewPlan{Mode: run.ReviewMode(input.Review.Focus.Kind), InitialScope: initial, SelectedModules: append([]run.ReviewModuleSelection(nil), input.Review.Focus.Modules...)}
	admissions, ok := s.reviews.(admittedReviewService)
	if !ok {
		return productreview.StartAdmission{}, errors.New("Review admission is unavailable")
	}
	result, err := admissions.AdmitStart(ctx, input.Review, input.RequestID, input.PayloadSHA256, instruction, scope, plan)
	if err != nil {
		return result, err
	}
	if result.Review.ExecutorKind.Effective() == productreview.ExecutorUpperLLM {
		switch result.Run.Status {
		case run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled:
			return result, nil
		}
		if s.upperStarter == nil {
			return result, errors.New("Upper Review workspace preparation is unavailable")
		}
		started, e := s.upperStarter.StartUpperReview(ctx, result.Review, result.Run)
		if started.ID != "" {
			result.Run = started
		}
		if e != nil {
			return result, e
		}
		// Prepare changed only durable grant state. Refresh the product projection
		// so adapters do not render stale PREPARING access after a successful run.
		refreshed, getErr := s.reviews.Get(ctx, result.Review.ID)
		if getErr != nil {
			return result, getErr
		}
		result.Review = refreshed
		return result, nil
	}
	starter, ok := s.starter.(reservedProductStarter)
	if !ok {
		return result, errors.New("admitted Review runtime handoff is unavailable")
	}
	started, err := starter.StartReservedReview(ctx, result.Review.ID, result.Run)
	result.Run = started
	return result, err
}

// CanonicalizeScope validates a repository-relative read scope without widening it.
// Scope-expansion proposals reuse this exact boundary validator so proposed paths
// and started Scope Review Runs have identical path semantics.
func CanonicalizeScope(repositoryRoot string, scope run.ReviewScope) (run.ReviewScope, error) {
	policy := reviewaccess.RepositoryBoundary()
	if scope.Kind == run.ReviewScopeRepository {
		if len(scope.Paths) != 0 {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		return run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}, nil
	}
	if scope.Kind != run.ReviewScopePaths || len(scope.Paths) == 0 || len(scope.Paths) > MaxPathCount {
		return run.ReviewScope{}, ErrInvalidArgument
	}
	// Reject malformed or escaping repository-relative path syntax before any
	// filesystem lookup. Filename semantics are intentionally irrelevant.
	if err := policy.ValidatePaths(reviewaccess.OperationScope, scope.Paths); err != nil {
		return run.ReviewScope{}, err
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return run.ReviewScope{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return run.ReviewScope{}, err
	}
	seen := map[string]struct{}{}
	result := run.ReviewScope{Kind: run.ReviewScopePaths, Paths: make([]string, 0, len(scope.Paths))}
	for _, supplied := range scope.Paths {
		if supplied == "" || len(supplied) > MaxPathBytes || filepath.IsAbs(supplied) || strings.Contains(supplied, "\\") || strings.ContainsRune(supplied, '\x00') {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		for _, component := range strings.Split(supplied, "/") {
			if component == ".." {
				return run.ReviewScope{}, ErrInvalidArgument
			}
		}
		clean := filepath.Clean(filepath.FromSlash(supplied))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
		if err != nil {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return run.ReviewScope{}, err
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return run.ReviewScope{}, &reviewaccess.Violation{Code: reviewaccess.FailureScopeForbidden, Operation: reviewaccess.OperationScope, Paths: []string{filepath.ToSlash(supplied)}}
		}
		info, err := os.Stat(target)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		normalized := filepath.ToSlash(relative)
		if _, duplicate := seen[normalized]; duplicate {
			return run.ReviewScope{}, ErrInvalidArgument
		}
		seen[normalized] = struct{}{}
		result.Paths = append(result.Paths, normalized)
	}
	return result, nil
}

// canonicalizeScope keeps the package-internal compatibility seam while the
// exported validator is shared by scope-expansion authorization.
func canonicalizeScope(repositoryRoot string, scope run.ReviewScope) (run.ReviewScope, error) {
	return CanonicalizeScope(repositoryRoot, scope)
}

// recordProvenance durably records the repository commit the Review is based
// on. The Task's immutable base commit is the exact state the Review's
// worktree is created at, so the provenance always matches what the Agent
// reviews. Recording is best-effort for the Review start itself and never
// rewritten: a failure leaves the Review with unknown provenance rather than
// a fabricated one.
func (s *Service) recordProvenance(ctx context.Context, reviewID string, reviewValue productreview.Review) {
	if s.provenance == nil || reviewValue.RepositoryID == "" || reviewValue.BaseCommit == "" {
		return
	}
	if err := s.provenance.RecordRepositoryProvenance(ctx, productreview.RepositoryProvenance{
		ReviewID:       reviewID,
		RepositoryID:   reviewValue.RepositoryID,
		Commit:         reviewValue.BaseCommit,
		RepositoryHEAD: reviewValue.BaseCommit,
		BranchKind:     productreview.ProvenanceUnknown,
		InspectionMode: productreview.InspectionFeatureAreaWorkspace,
		CapturedAt:     time.Now().UTC(),
	}); err != nil {
		// Bounded factual diagnostic only; provenance stays unrecorded.
		observability.Error(ctx, "review", "review.provenance.record_failed", "Review repository provenance could not be recorded", map[string]any{"errorCode": "REVIEW_PROVENANCE_RECORD_FAILED", "errorSummary": err.Error()})
	}
}

// startReview resolves every authorization and runtime fact from the Review.
func (s *Service) startReview(ctx context.Context, input StartInput) (run.Run, error) {
	if input.AISessionID == "" || input.ReviewID == "" || s.reviews == nil || s.grants == nil {
		return run.Run{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return run.Run{}, ErrNotFound
	}
	v, err := s.reviews.Get(ctx, input.ReviewID)
	if err != nil || v.ProjectID != session.ProjectID || v.TaskID == "" {
		return run.Run{}, ErrNotFound
	}
	repositoryValue, err := s.repositories.Get(ctx, v.RepositoryID)
	if err != nil || repositoryValue.ProjectID != v.ProjectID {
		return run.Run{}, ErrNotFound
	}
	if v.RunID != "" || v.StartFailure != nil {
		return run.Run{}, ErrConflict
	}
	instruction := v.Instruction
	if ValidateInstruction(instruction) != nil {
		return run.Run{}, ErrInvalidArgument
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	initial := scope
	if v.Focus.Kind == productreview.FocusFocused && len(v.Focus.Paths) > 0 {
		initial, err = CanonicalizeScope(repositoryValue.Path, run.ReviewScope{Kind: run.ReviewScopePaths, Paths: v.Focus.Paths})
		if err != nil {
			return run.Run{}, err
		}
	}
	plan := run.ReviewPlan{Mode: run.ReviewMode(v.Focus.Kind), InitialScope: initial, SelectedModules: append([]run.ReviewModuleSelection(nil), v.Focus.Modules...)}
	if _, err = s.grants.Ensure(ctx, reviewgrant.Grant{ReviewID: v.ID, FeatureAreaID: v.FeatureAreaID, ProjectID: v.ProjectID, RepositoryID: v.RepositoryID, Mode: reviewgrant.ModeRepository, InitialScope: scope}); err != nil {
		return run.Run{}, err
	}
	return s.starter.StartReview(ctx, v.ID, func(reserveCtx context.Context) (run.Run, error) {
		created, err := s.runs.CreateFocusedScopeReviewForReview(reserveCtx, v.ID, instruction, scope, plan)
		if err == nil {
			s.recordProvenance(reserveCtx, v.ID, v)
		}
		return created, err
	})
}

// ValidateInstruction checks authored bytes without changing them.
func ValidateInstruction(instruction string) error {
	if strings.TrimSpace(instruction) == "" || len(instruction) > MaxInstructionBytes || strings.ContainsRune(instruction, '\x00') {
		return ErrInvalidArgument
	}
	return nil
}
