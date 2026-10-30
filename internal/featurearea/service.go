package featurearea

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
)

const (
	MaxIDBytes               = 128
	MaxTitleBytes            = 256
	MaxProblemStatementBytes = 16 << 10
	MaxWorkflowFeatureAreas  = 100
)

var (
	ErrRetiring        = errors.New("FEATURE_AREA_RETIRING")
	ErrNotFound        = errors.New("feature area not found")
	ErrUnavailable     = errors.New("feature area is unavailable for continuation")
	ErrConflict        = errors.New("feature area conflict")
	ErrInvalidArgument = errors.New("invalid feature area argument")
	validIdentifier    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
)

// SplitTaskCount is the storage probe used to enforce the "split before
// Tasks" invariant. Feature areas do not import task to avoid a cycle; the
// store adapter returns the durable count of Tasks owned by the feature area.
type SplitTaskCount func(ctx context.Context, featureAreaID string) (int, error)

type Store interface {
	Create(context.Context, FeatureArea) error
	ReplayCreation(context.Context, string, string, string, string, string) (FeatureArea, CreationReceipt, error)
	CreateReceipted(context.Context, FeatureArea, CreationReceipt) (FeatureArea, CreationReceipt, error)
	UpdateCreationReceipt(context.Context, CreationReceipt) error
	Get(context.Context, string) (FeatureArea, error)
	ListByProject(context.Context, string) ([]FeatureArea, error)
	Update(context.Context, string, func(*FeatureArea) error) (FeatureArea, error)
	ApplyLifecycle(context.Context, LifecycleMutation) (LifecycleResult, error)
	ListRelationsBySource(context.Context, string) ([]Relation, error)
	ListRelationsByTarget(context.Context, string) ([]Relation, error)
}

type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}

type CreateInput struct {
	AISessionID      string
	RequestID        string
	Title            string
	ProblemStatement string
}

type Service struct {
	onRetire func()
	store    Store
	sessions SessionLookup
	// taskCount probes durable Task ownership for split and Task gates.
	taskCount SplitTaskCount
	newID     func() (string, error)
	now       func() time.Time
}

func (s *Service) SetRetirementWake(wake func()) { s.onRetire = wake }

func NewService(store Store, sessions SessionLookup, taskCount SplitTaskCount, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || taskCount == nil {
		return nil, errors.New("feature area store, AI session lookup, and Task count are required")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	if memory, ok := store.(*MemoryStore); ok {
		memory.taskCount = taskCount
	}
	return &Service{store: store, sessions: sessions, taskCount: taskCount, newID: newID, now: now}, nil
}

// Create starts a Feature Area in DISCOVERING. Only the upper AI's explicit
// product-capability decision creates Feature Areas; Bridge never derives
// them automatically from repository scans or Project Discovery candidates.
func (s *Service) Create(ctx context.Context, input CreateInput) (FeatureArea, error) {
	if !validID(input.AISessionID) || !validText(input.Title, MaxTitleBytes) || !validText(input.ProblemStatement, MaxProblemStatementBytes) {
		return FeatureArea{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		if errors.Is(err, aisession.ErrNotFound) {
			return FeatureArea{}, ErrNotFound
		}
		return FeatureArea{}, err
	}
	id, err := s.newID()
	if err != nil {
		return FeatureArea{}, fmt.Errorf("allocate feature area id: %w", err)
	}
	now := s.now().UTC()
	value := FeatureArea{
		ID: id, ProjectID: session.ProjectID, CreatedByAISessionID: session.ID,
		Title: input.Title, ProblemStatement: input.ProblemStatement,
		Status: StatusDiscovering, CreatedAt: now, UpdatedAt: now,
	}
	if baseline, ok := s.store.(interface {
		CurrentDevelopmentHead(context.Context, string) (string, error)
	}); ok {
		value.MergedCommit, err = baseline.CurrentDevelopmentHead(ctx, value.ProjectID)
		if err != nil {
			return FeatureArea{}, err
		}
		if value.MergedCommit != "" {
			value.MergedAt = &now
		}
	}
	if err := s.store.Create(ctx, value); err != nil {
		return FeatureArea{}, fmt.Errorf("store feature area: %w", err)
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: input.AISessionID, FeatureAreaID: value.ID})
	observability.Info(logCtx, "featurearea", "feature_area.created", "durable Feature Area created", map[string]any{"creatingAISessionId": value.CreatedByAISessionID, "status": value.Status})
	return s.store.Get(ctx, value.ID)
}

// ReplayCreation returns a previously admitted root request. It intentionally
// authorizes only the session's Project and does not re-evaluate the prepared
// execution configuration.
func (s *Service) ReplayCreation(ctx context.Context, aiSessionID, command, requestID, payloadSHA256 string) (FeatureArea, CreationReceipt, error) {
	if !validID(aiSessionID) || !validID(command) || !validID(requestID) || !validSHA256(payloadSHA256) {
		return FeatureArea{}, CreationReceipt{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return FeatureArea{}, CreationReceipt{}, mapNotFound(err)
	}
	value, receipt, err := s.store.ReplayCreation(ctx, session.ID, session.ProjectID, command, requestID, payloadSHA256)
	if err != nil {
		return FeatureArea{}, CreationReceipt{}, mapNotFound(err)
	}
	return value, receipt, nil
}

// CreateReceipted atomically writes a newly allocated Feature Area and its
// root receipt. Callers must ReplayCreation before doing preflight; the store
// still makes a concurrent identical request return the original record.
func (s *Service) CreateReceipted(ctx context.Context, input CreateInput, receipt CreationReceipt) (FeatureArea, CreationReceipt, error) {
	if !validID(input.AISessionID) || !validText(input.Title, MaxTitleBytes) || !validText(input.ProblemStatement, MaxProblemStatementBytes) || !validCreationReceipt(receipt) {
		return FeatureArea{}, CreationReceipt{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, input.AISessionID)
	if err != nil {
		return FeatureArea{}, CreationReceipt{}, mapNotFound(err)
	}
	if input.RequestID != "" && input.RequestID != receipt.RequestID {
		return FeatureArea{}, CreationReceipt{}, ErrInvalidArgument
	}
	if receipt.AISessionID != session.ID || receipt.ProjectID != session.ProjectID || receipt.FeatureAreaID != "" || receipt.TaskID != "" || receipt.ReviewID != "" || receipt.RunID != "" || receipt.ReviewAdmitted || receipt.FailedStep != "" || receipt.ErrorCode != "" || receipt.ErrorMessage != "" {
		return FeatureArea{}, CreationReceipt{}, ErrInvalidArgument
	}
	// A racing retry must return the persisted root before reading the current
	// development head. This keeps the first admission's prepared baseline
	// authoritative even if the environment changes between attempts.
	if existing, existingReceipt, replayErr := s.store.ReplayCreation(ctx, session.ID, session.ProjectID, receipt.Command, receipt.RequestID, receipt.PayloadSHA256); replayErr == nil {
		return existing, existingReceipt, nil
	} else if !errors.Is(replayErr, ErrNotFound) {
		return FeatureArea{}, CreationReceipt{}, replayErr
	}
	id, err := s.newID()
	if err != nil {
		return FeatureArea{}, CreationReceipt{}, fmt.Errorf("allocate feature area id: %w", err)
	}
	now := s.now().UTC()
	value := FeatureArea{ID: id, ProjectID: session.ProjectID, CreatedByAISessionID: session.ID, Title: input.Title, ProblemStatement: input.ProblemStatement, Status: StatusDiscovering, CreatedAt: now, UpdatedAt: now}
	if baseline, ok := s.store.(interface {
		CurrentDevelopmentHead(context.Context, string) (string, error)
	}); ok {
		value.MergedCommit, err = baseline.CurrentDevelopmentHead(ctx, value.ProjectID)
		if err != nil {
			return FeatureArea{}, CreationReceipt{}, err
		}
		if value.MergedCommit != "" {
			value.MergedAt = &now
		}
	}
	receipt.FeatureAreaID = id
	created, saved, err := s.store.CreateReceipted(ctx, value, receipt)
	if err != nil {
		return FeatureArea{}, CreationReceipt{}, err
	}
	return created, saved, nil
}

// UpdateCreationReceipt records monotonic completed steps or a recoverable
// failure. The store rejects any replacement of the immutable root identity.
func (s *Service) UpdateCreationReceipt(ctx context.Context, receipt CreationReceipt) error {
	if !validCreationReceipt(receipt) || !validID(receipt.FeatureAreaID) {
		return ErrInvalidArgument
	}
	return s.store.UpdateCreationReceipt(ctx, receipt)
}

func (s *Service) GetForSession(ctx context.Context, aiSessionID, featureAreaID string) (FeatureArea, error) {
	value, err := s.getAuthorizedForSession(ctx, aiSessionID, featureAreaID)
	if err != nil {
		return FeatureArea{}, err
	}
	if value.Status == StatusRetiring {
		return FeatureArea{}, ErrRetiring
	}
	if !IsUsable(value) {
		logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: aiSessionID, FeatureAreaID: value.ID})
		observability.Info(logCtx, "featurearea", "feature_area.unavailable", "Feature Area is unavailable for continuation", map[string]any{"status": value.Status})
		return FeatureArea{}, ErrUnavailable
	}
	if value.CreatedByAISessionID != aiSessionID {
		logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: value.ProjectID, AISessionID: aiSessionID, FeatureAreaID: value.ID})
		observability.Info(logCtx, "featurearea", "feature_area.continued", "Project-owned Feature Area continued by another AI session", map[string]any{"creatingAISessionId": value.CreatedByAISessionID})
	}
	return value, nil
}

func (s *Service) getAuthorizedForSession(ctx context.Context, aiSessionID, featureAreaID string) (FeatureArea, error) {
	if !validID(aiSessionID) || !validID(featureAreaID) {
		return FeatureArea{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return FeatureArea{}, mapNotFound(err)
	}
	value, err := s.store.Get(ctx, featureAreaID)
	if err != nil {
		return FeatureArea{}, mapNotFound(err)
	}
	if value.ProjectID != session.ProjectID {
		return FeatureArea{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) ListForSession(ctx context.Context, aiSessionID string) ([]FeatureArea, error) {
	if !validID(aiSessionID) {
		return nil, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	values, err := s.listUsable(ctx, session.ProjectID, MaxWorkflowFeatureAreas)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].CreatedAt.After(values[j].CreatedAt)
	})
	return values, nil
}

// ListUsableByProject returns the bounded Project-owned Feature Area
// projection used by trusted local product surfaces. It deliberately excludes
// DECOMPOSED and RETIRED Feature Areas, and creation-session provenance is
// not part of the caller-facing contract.
func (s *Service) ListUsableByProject(ctx context.Context, projectID string, limit int) ([]FeatureArea, error) {
	if !validID(projectID) || limit < 1 || limit > MaxWorkflowFeatureAreas {
		return nil, ErrInvalidArgument
	}
	return s.listUsable(ctx, projectID, limit)
}

// ListHistoryByProject includes terminal and legacy ownership for trusted,
// Project-authorized read surfaces. It grants no continuation authority.
func (s *Service) ListHistoryByProject(ctx context.Context, projectID string, limit int) ([]FeatureArea, error) {
	if !validID(projectID) || limit < 1 || limit > MaxWorkflowFeatureAreas {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].ID < values[j].ID
		}
		return values[i].UpdatedAt.After(values[j].UpdatedAt)
	})
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
}

func (s *Service) listUsable(ctx context.Context, projectID string, limit int) ([]FeatureArea, error) {
	values, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	usable := make([]FeatureArea, 0, min(limit, len(values)))
	for _, value := range values {
		if IsUsable(value) {
			usable = append(usable, value)
		}
	}
	sort.Slice(usable, func(i, j int) bool {
		if usable[i].UpdatedAt.Equal(usable[j].UpdatedAt) {
			return usable[i].ID < usable[j].ID
		}
		return usable[i].UpdatedAt.After(usable[j].UpdatedAt)
	})
	if len(usable) > limit {
		usable = usable[:limit]
	}
	return usable, nil
}

// CurrentForSession returns a Feature Area only when the bound Project has
// exactly one usable Feature Area. Active workflow lineage is resolved by the
// control API; this fallback deliberately does not invent "newest Feature
// Area is current" semantics.
func (s *Service) CurrentForSession(ctx context.Context, aiSessionID string) (FeatureArea, error) {
	if !validID(aiSessionID) {
		return FeatureArea{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, aiSessionID)
	if err != nil {
		return FeatureArea{}, mapNotFound(err)
	}
	values, err := s.listUsable(ctx, session.ProjectID, 2)
	if err != nil {
		return FeatureArea{}, err
	}
	if len(values) != 1 {
		return FeatureArea{}, ErrNotFound
	}
	return values[0], nil
}

// SplitRelations returns the recorded SPLIT_INTO provenance for a source
// Feature Area, ordered by creation time then target ID.
func (s *Service) SplitRelations(ctx context.Context, featureAreaID string) ([]Relation, error) {
	if !validID(featureAreaID) {
		return nil, ErrInvalidArgument
	}
	values, err := s.store.ListRelationsBySource(ctx, featureAreaID)
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].TargetFeatureAreaID < values[j].TargetFeatureAreaID
		}
		return values[i].CreatedAt.Before(values[j].CreatedAt)
	})
	return values, nil
}

// IsTaskActionableFor implements the control-api capability probe. It reports
// whether durable Tasks may be created for the Feature Area (only ACTIVE).
func (s *Service) IsTaskActionableFor(value FeatureArea) bool { return IsTaskActionable(value) }

// IsUsable is the single normal-workflow capability boundary for Feature
// Areas: DISCOVERING and ACTIVE participate; DECOMPOSED and
// RETIRED do not.
func IsUsable(value FeatureArea) bool {
	return !value.LegacyReadOnly && (value.Status == StatusDiscovering || value.Status == StatusActive)
}

// IsTaskActionable reports whether durable Tasks may be created for the
// Feature Area. Only current ACTIVE capabilities qualify.
func IsTaskActionable(value FeatureArea) bool {
	return !value.LegacyReadOnly && value.Status == StatusActive
}

func validID(value string) bool {
	return value != "" && len(value) <= MaxIDBytes && validIdentifier.MatchString(value)
}

func validText(value string, max int) bool {
	trimmed := strings.TrimSpace(value)
	return trimmed != "" && len(trimmed) <= max && !strings.ContainsRune(value, '\x00')
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func validCreationReceipt(value CreationReceipt) bool {
	return validID(value.AISessionID) && validID(value.ProjectID) && validID(value.Command) && validID(value.RequestID) && validSHA256(value.PayloadSHA256) && value.ContractVersion != "" && len(value.ContractVersion) <= MaxIDBytes && len(value.Prepared) > 0 && json.Valid(value.Prepared)
}

func mapNotFound(err error) error {
	if errors.Is(err, aisession.ErrNotFound) || errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "feature_area_" + hex.EncodeToString(raw[:]), nil
}

// GetHistoryForSession authorizes a historical read without permitting new work.
func (s *Service) GetHistoryForSession(ctx context.Context, sessionID, id string) (FeatureArea, error) {
	return s.getAuthorizedForSession(ctx, sessionID, id)
}

// ListHistoryForSession is navigation only. It never makes closed areas usable.
func (s *Service) ListHistoryForSession(ctx context.Context, sessionID string) ([]FeatureArea, error) {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	return s.store.ListByProject(ctx, session.ProjectID)
}

// ListInventoryForSession returns every current area without the navigation
// limit. This read grants no engineering authority.
func (s *Service) ListInventoryForSession(ctx context.Context, sessionID string) ([]FeatureArea, error) {
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, mapNotFound(err)
	}
	values, err := s.store.ListByProject(ctx, session.ProjectID)
	if err != nil {
		return nil, err
	}
	out := make([]FeatureArea, 0, len(values))
	for _, v := range values {
		if v.Status != StatusRetired && v.Status != StatusDecomposed && !v.LegacyReadOnly {
			out = append(out, v)
		}
	}
	return out, nil
}

// GetHistoryForProject reads one exact Project-owned area after the caller has
// authorized the Project. Creation Session remains provenance only.
func (s *Service) GetHistoryForProject(ctx context.Context, projectID, areaID string) (FeatureArea, error) {
	if !validID(projectID) || !validID(areaID) {
		return FeatureArea{}, ErrInvalidArgument
	}
	value, err := s.store.Get(ctx, areaID)
	if err != nil {
		return FeatureArea{}, mapNotFound(err)
	}
	if value.ProjectID != projectID {
		return FeatureArea{}, ErrNotFound
	}
	return value, nil
}
