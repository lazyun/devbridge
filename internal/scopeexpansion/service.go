package scopeexpansion

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewaccess"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
)

const (
	DefaultListLimit = 50
	MaxListLimit     = 100
	MaxOffset        = 1_000_000
	MaxIDBytes       = 128
	MaxReasonBytes   = 16 << 10
)

var (
	ErrNotFound        = errors.New("scope expansion not found")
	ErrConflict        = errors.New("scope expansion conflict")
	ErrInvalidArgument = errors.New("invalid scope expansion argument")
	validIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return fmt.Sprintf("scope expansion cannot transition from %s to %s", e.From, e.To)
}

type Store interface {
	Create(context.Context, Expansion) error
	Get(context.Context, string) (Expansion, error)
	ListByProject(context.Context, string) ([]Expansion, error)
	Decide(context.Context, string, Status, time.Time) (Expansion, error)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type ReviewLookup interface {
	Get(context.Context, string) (review.Review, error)
}

type RepositoryLookup interface {
	Get(context.Context, string) (repository.Repository, error)
}

type ReviewGrantLookup interface {
	Get(context.Context, string) (reviewgrant.Grant, error)
}

type CreateInput struct {
	AISessionID     string
	ReviewID        string
	CurrentScope    run.ReviewScope
	AdditionalScope run.ReviewScope
	Reason          string
}

type ListOptions struct{ Limit, Offset int }

type Service struct {
	store        Store
	sessions     SessionLookup
	reviews      ReviewLookup
	repositories RepositoryLookup
	newID        func() (string, error)
	now          func() time.Time
	grants       ReviewGrantLookup
}

func (s *Service) SetReviewGrantLookup(grants ReviewGrantLookup) { s.grants = grants }

// CurrentAuthorizedScope returns the immutable initial Review grant plus the
// exact non-stale ALLOWED expansion chain. Run history is not authority when
// the production grant service is configured.
func (s *Service) CurrentAuthorizedScope(ctx context.Context, reviewID string) (run.ReviewScope, bool, error) {
	if !validID(reviewID) {
		return run.ReviewScope{}, false, ErrInvalidArgument
	}
	reviewValue, err := s.reviews.Get(ctx, reviewID)
	if err != nil {
		return run.ReviewScope{}, false, mapNotFound(err)
	}
	return s.currentAuthorizedScope(ctx, reviewValue)
}

func NewService(store Store, sessions SessionLookup, reviews ReviewLookup, repositories RepositoryLookup, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || reviews == nil || repositories == nil {
		return nil, errors.New("scope expansion dependencies are incomplete")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, reviews: reviews, repositories: repositories, newID: newID, now: now}, nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Expansion, error) {
	reason := strings.TrimSpace(input.Reason)
	if !validID(input.AISessionID) || !validID(input.ReviewID) || reason == "" || len(reason) > MaxReasonBytes || strings.ContainsRune(reason, '\x00') {
		return Expansion{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return Expansion{}, mapNotFound(err)
	}
	reviewValue, err := s.reviews.Get(ctx, input.ReviewID)
	if err != nil || reviewValue.ProjectID != session.ProjectID {
		return Expansion{}, ErrNotFound
	}
	if reviewValue.TaskID == "" || reviewValue.FeatureAreaID == "" || reviewValue.RepositoryID == "" {
		return Expansion{}, ErrConflict
	}
	repositoryValue, err := s.repositories.Get(ctx, reviewValue.RepositoryID)
	if err != nil || repositoryValue.ProjectID != session.ProjectID {
		return Expansion{}, ErrNotFound
	}
	current, err := scopereview.CanonicalizeScope(repositoryValue.Path, input.CurrentScope)
	if errors.Is(err, reviewaccess.ErrScopeForbidden) {
		return Expansion{}, err
	}
	if err != nil || current.Kind == run.ReviewScopeRepository {
		return Expansion{}, ErrInvalidArgument
	}
	additional, err := scopereview.CanonicalizeScope(repositoryValue.Path, input.AdditionalScope)
	if errors.Is(err, reviewaccess.ErrScopeForbidden) {
		return Expansion{}, err
	}
	if err != nil || !AddsScope(current, additional) {
		return Expansion{}, ErrInvalidArgument
	}
	currentAuthorized, ok, err := s.currentAuthorizedScope(ctx, reviewValue)
	if err != nil {
		return Expansion{}, err
	}
	if !ok || !EqualScope(currentAuthorized, current) {
		return Expansion{}, ErrConflict
	}
	id, err := s.newID()
	if err != nil {
		return Expansion{}, fmt.Errorf("allocate scope expansion id: %w", err)
	}
	now := s.now().UTC()
	value := Expansion{ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, ReviewID: reviewValue.ID, FeatureAreaID: reviewValue.FeatureAreaID, RepositoryID: reviewValue.RepositoryID, CurrentScope: current, AdditionalScope: additional, Reason: reason, Status: StatusPendingConfirmation, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Expansion{}, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: session.ProjectID, AISessionID: session.ID, FeatureAreaID: reviewValue.FeatureAreaID, ReviewID: reviewValue.ID})
	observability.Info(logCtx, "scope_expansion", "scope_expansion.proposed", "durable scope expansion proposed", expansionLogData(value, mergeScopes(current, additional)))
	observability.Info(logCtx, "scope_expansion", "scope_expansion.presented", "scope expansion decision card requested", map[string]any{"expansionId": value.ID, "addedPaths": append([]string(nil), additional.Paths...), "repositoryWide": additional.Kind == run.ReviewScopeRepository})
	return Clone(value), nil
}

// AddsScope reports whether every proposed path adds an authorization boundary.
func AddsScope(current, additional run.ReviewScope) bool {
	if current.Kind == run.ReviewScopeRepository {
		return false
	}
	if additional.Kind == run.ReviewScopeRepository {
		return len(additional.Paths) == 0
	}
	if additional.Kind != run.ReviewScopePaths {
		return false
	}
	for _, proposed := range additional.Paths {
		for _, existing := range current.Paths {
			if proposed == existing || strings.HasPrefix(proposed, existing+"/") {
				return false
			}
		}
	}
	return len(additional.Paths) > 0
}

func (s *Service) Get(ctx context.Context, aiSessionID, expansionID string) (Expansion, error) {
	if !validID(aiSessionID) || !validID(expansionID) {
		return Expansion{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return Expansion{}, mapNotFound(err)
	}
	value, err := s.store.Get(ctx, expansionID)
	if err != nil {
		return Expansion{}, mapNotFound(err)
	}
	if value.ProjectID != session.ProjectID {
		return Expansion{}, ErrNotFound
	}
	if value.Status == StatusPendingConfirmation {
		observability.Info(observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: session.ID, ReviewID: value.ReviewID}), "scope_expansion", "scope_expansion.presented", "pending scope expansion projected for presentation", expansionLogData(value, mergeScopes(value.CurrentScope, value.AdditionalScope)))
	}
	return Clone(value), nil
}

func (s *Service) List(ctx context.Context, aiSessionID string, options ListOptions) ([]Expansion, error) {
	if !validID(aiSessionID) {
		return nil, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	limit, offset, err := boundedList(options)
	if err != nil {
		return nil, err
	}
	values, err := s.store.ListByProject(ctx, session.ProjectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	if offset >= len(values) {
		return []Expansion{}, nil
	}
	end := offset + limit
	if end > len(values) {
		end = len(values)
	}
	result := make([]Expansion, 0, end-offset)
	for _, value := range values[offset:end] {
		result = append(result, Clone(value))
	}
	return result, nil
}

// ListByProject provides a bounded-domain read projection for trusted local
// operator surfaces. User-facing MCP calls remain scoped through List and an
// AI session so callers cannot choose a Project directly.
func (s *Service) ListByProject(ctx context.Context, projectID string) ([]Expansion, error) {
	if !validID(projectID) {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	result := make([]Expansion, 0, len(values))
	for _, value := range values {
		result = append(result, Clone(value))
	}
	return result, nil
}

func (s *Service) Allow(ctx context.Context, aiSessionID, expansionID string) (Expansion, error) {
	return s.decide(ctx, aiSessionID, expansionID, StatusAllowed)
}

func (s *Service) Reject(ctx context.Context, aiSessionID, expansionID string) (Expansion, error) {
	return s.decide(ctx, aiSessionID, expansionID, StatusRejected)
}

func (s *Service) decide(ctx context.Context, aiSessionID, expansionID string, status Status) (Expansion, error) {
	current, err := s.Get(ctx, aiSessionID, expansionID)
	if err != nil {
		return Expansion{}, err
	}
	if current.LegacyTaskID != "" || current.ReviewID == "" {
		return Expansion{}, ErrConflict
	}
	if current.Status != StatusPendingConfirmation {
		return Expansion{}, &TransitionError{From: current.Status, To: status}
	}
	if status == StatusAllowed {
		reviewValue, reviewErr := s.reviews.Get(ctx, current.ReviewID)
		if reviewErr != nil {
			return Expansion{}, reviewErr
		}
		latest, ok, latestErr := s.currentAuthorizedScope(ctx, reviewValue)
		if latestErr != nil {
			return Expansion{}, latestErr
		}
		if !ok || !EqualScope(latest, current.CurrentScope) {
			observability.Info(observability.WithContext(ctx, observability.Correlation{ProjectID: current.ProjectID, AISessionID: aiSessionID, ReviewID: current.ReviewID}), "scope_expansion", "scope_expansion.stale", "scope expansion no longer extends the current authorized scope", expansionLogData(current, latest))
			return Expansion{}, ErrConflict
		}
	}
	value, err := s.store.Decide(ctx, expansionID, status, s.now().UTC())
	if err == nil {
		logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: aiSessionID, ReviewID: value.ReviewID})
		event := "scope_expansion.rejected"
		if status == StatusAllowed {
			event = "scope_expansion.allowed"
		}
		observability.Info(logCtx, "scope_expansion", event, "scope expansion decision persisted", expansionLogData(value, mergeScopes(value.CurrentScope, value.AdditionalScope)))
		if status == StatusAllowed {
			observability.Info(logCtx, "scope_expansion", "scope_expansion.applied", "allowed scope expansion is part of the authorized Review scope", expansionLogData(value, mergeScopes(value.CurrentScope, value.AdditionalScope)))
		}
	}
	return Clone(value), err
}

func expansionLogData(value Expansion, resulting run.ReviewScope) map[string]any {
	return map[string]any{
		"expansionId":          value.ID,
		"addedPaths":           append([]string(nil), value.AdditionalScope.Paths...),
		"repositoryWide":       value.AdditionalScope.Kind == run.ReviewScopeRepository,
		"resultingReviewScope": scopeFingerprint(resulting),
		"status":               value.Status,
	}
}

func scopeFingerprint(value run.ReviewScope) map[string]any {
	paths := append([]string(nil), value.Paths...)
	sort.Strings(paths)
	sum := sha256.Sum256([]byte(string(value.Kind) + "\n" + strings.Join(paths, "\n")))
	return map[string]any{"kind": value.Kind, "pathCount": len(paths), "sha256": hex.EncodeToString(sum[:])}
}

// AuthorizeReviewScope allows the initial exact Review scope, later subsets
// of the latest scope, or the exact union granted by a non-stale ALLOWED
// ScopeExpansion. It never reads or mutates Task ExecutionScope.
func (s *Service) AuthorizeReviewScope(ctx context.Context, reviewID string, requested run.ReviewScope) error {
	if !validID(reviewID) {
		return ErrInvalidArgument
	}
	reviewValue, err := s.reviews.Get(ctx, reviewID)
	if err != nil {
		return mapNotFound(err)
	}
	current, ok, err := s.currentAuthorizedScope(ctx, reviewValue)
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	repositoryValue, err := s.repositories.Get(ctx, reviewValue.RepositoryID)
	if err != nil || repositoryValue.ProjectID != reviewValue.ProjectID {
		return ErrNotFound
	}
	requested, err = scopereview.CanonicalizeScope(repositoryValue.Path, requested)
	if err != nil {
		return err
	}
	if scopeContains(current, requested) {
		return nil
	}
	return ErrConflict
}

func (s *Service) currentAuthorizedScope(ctx context.Context, reviewValue review.Review) (run.ReviewScope, bool, error) {
	if reviewValue.TaskID == "" || reviewValue.FeatureAreaID == "" || reviewValue.RepositoryID == "" {
		return run.ReviewScope{}, false, ErrConflict
	}
	if s.grants == nil {
		return run.ReviewScope{}, false, nil
	}
	grant, err := s.grants.Get(ctx, reviewValue.ID)
	if errors.Is(err, reviewgrant.ErrNotFound) {
		return run.ReviewScope{}, false, nil
	}
	if err != nil {
		return run.ReviewScope{}, false, err
	}
	expansions, err := s.store.ListByProject(ctx, reviewValue.ProjectID)
	if err != nil {
		return run.ReviewScope{}, false, err
	}
	current, err := AuthorizedScope(reviewValue, grant, expansions)
	return current, err == nil, err
}

// AuthorizedScope derives authority from the exact Review grant and allowed
// expansion chain. Stores use it inside the same transaction as a decision.
func AuthorizedScope(reviewValue review.Review, grant reviewgrant.Grant, expansions []Expansion) (run.ReviewScope, error) {
	if reviewValue.ID == "" || reviewValue.ProjectID == "" || reviewValue.TaskID == "" || reviewValue.FeatureAreaID == "" || reviewValue.RepositoryID == "" || grant.ReviewID != reviewValue.ID || grant.FeatureAreaID != reviewValue.FeatureAreaID || grant.ProjectID != reviewValue.ProjectID || grant.RepositoryID != reviewValue.RepositoryID {
		return run.ReviewScope{}, ErrConflict
	}
	current := run.ReviewScope{Kind: grant.InitialScope.Kind, Paths: append([]string(nil), grant.InitialScope.Paths...)}
	expansions = append([]Expansion(nil), expansions...)
	sort.Slice(expansions, func(i, j int) bool {
		if expansions[i].CreatedAt.Equal(expansions[j].CreatedAt) {
			return expansions[i].ID < expansions[j].ID
		}
		return expansions[i].CreatedAt.Before(expansions[j].CreatedAt)
	})
	applied := make(map[string]struct{}, len(expansions))
	for {
		advanced := false
		for _, expansion := range expansions {
			if _, seen := applied[expansion.ID]; seen || expansion.ReviewID != reviewValue.ID || expansion.ProjectID != reviewValue.ProjectID || expansion.LegacyTaskID != "" || expansion.FeatureAreaID != reviewValue.FeatureAreaID || expansion.RepositoryID != reviewValue.RepositoryID || expansion.Status != StatusAllowed || !EqualScope(expansion.CurrentScope, current) {
				continue
			}
			current = mergeScopes(current, expansion.AdditionalScope)
			applied[expansion.ID] = struct{}{}
			advanced = true
			break
		}
		if !advanced {
			break
		}
	}
	return current, nil
}

func scopeContains(authorized, requested run.ReviewScope) bool {
	if authorized.Kind == run.ReviewScopeRepository {
		return true
	}
	if requested.Kind != run.ReviewScopePaths {
		return false
	}
	for _, path := range requested.Paths {
		covered := false
		for _, allowed := range authorized.Paths {
			if path == allowed || strings.HasPrefix(path, allowed+"/") {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// EqualScope compares exact scopes independent of path ordering.
func EqualScope(left, right run.ReviewScope) bool {
	if left.Kind != right.Kind || len(left.Paths) != len(right.Paths) {
		return false
	}
	l, r := append([]string(nil), left.Paths...), append([]string(nil), right.Paths...)
	sort.Strings(l)
	sort.Strings(r)
	for i := range l {
		if l[i] != r[i] {
			return false
		}
	}
	return true
}

func mergeScopes(current, additional run.ReviewScope) run.ReviewScope {
	if current.Kind == run.ReviewScopeRepository || additional.Kind == run.ReviewScopeRepository {
		return run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	}
	paths := append(append([]string(nil), current.Paths...), additional.Paths...)
	sort.Strings(paths)
	return run.ReviewScope{Kind: run.ReviewScopePaths, Paths: paths}
}

func boundedList(options ListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit || options.Offset < 0 || options.Offset > MaxOffset {
		return 0, 0, ErrInvalidArgument
	}
	return limit, options.Offset, nil
}

func validID(value string) bool {
	return value != "" && len(value) <= MaxIDBytes && validIdentifier.MatchString(value)
}

func mapNotFound(err error) error {
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, review.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "scope_expansion_" + hex.EncodeToString(raw[:]), nil
}
