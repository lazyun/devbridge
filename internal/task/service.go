package task

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/observability"
)

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type Store interface {
	Create(context.Context, Task) error
	Get(context.Context, string) (Task, error)
	List(context.Context) ([]Task, error)
	Update(context.Context, string, func(*Task) error) (Task, error)
}

// BaseResolver maps a repository-owned revision to an immutable full commit.
type BaseResolver interface {
	ResolveBase(context.Context, string, string) (string, error)
}

type ModuleLookup interface {
	Get(context.Context, string) (module.Module, error)
}

type CodexConfigLookup interface {
	Get(context.Context, string) (codexconfig.Config, error)
}

type CreateInput struct {
	AISessionID      string
	RequestID        string
	ProjectID        string
	FeatureAreaID    string
	ModuleID         string
	RepositoryID     string
	CodexConfigID    string
	WorkKind         WorkKind
	Model            string
	ReasoningEffort  string
	Goal             string
	Constraints      []string
	WorkSpec         WorkSpec
	VerificationSpec VerificationSpec
	BaseRevision     string
	Draft            bool
	ProblemStatement string
	Planning         bool
	SkipReview       bool
}

type Service struct {
	store    Store
	resolver BaseResolver
	modules  ModuleLookup
	configs  CodexConfigLookup
	newID    func() (string, error)
	now      func() time.Time
}

func NewService(store Store, resolver BaseResolver, modules ModuleLookup, configs CodexConfigLookup, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, resolver: resolver, modules: modules, configs: configs, newID: newID, now: now}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Task, error) {
	if s.store == nil {
		return Task{}, errors.New("task store is required")
	}
	if !observability.ValidRequestID(input.RequestID) {
		return Task{}, errors.New("task create request ID is required and must be valid")
	}
	if input.ProjectID == "" || input.FeatureAreaID == "" || input.ModuleID == "" {
		return Task{}, errors.New("task project, feature area and module are required")
	}
	if !ValidWorkKind(input.WorkKind) {
		return Task{}, errors.New("task work kind must be null or one of the closed work-kind enum")
	}
	if input.Model != "" {
		if err := codexruntime.ValidateModelID(input.Model); err != nil {
			return Task{}, errors.New("task model is not a valid Codex model ID")
		}
	}
	if input.ReasoningEffort != "" && !containsString(codexruntime.AllReasoningEfforts, input.ReasoningEffort) {
		return Task{}, errors.New("task reasoning effort is not in the closed reasoning universe")
	}
	title := strings.TrimSpace(input.Goal)
	problem := strings.TrimSpace(input.ProblemStatement)
	if title == "" {
		title = strings.TrimSpace(input.WorkSpec.Goal)
	}
	if problem == "" {
		problem = title
	}
	if title == "" {
		title = problem
	}
	if err := ValidateIntentText(title, problem, input.Constraints); err != nil {
		return Task{}, err
	}
	if existing, e := s.ReplayCreate(ctx, input); e == nil {
		return existing, nil
	} else if !errors.Is(e, ErrNotFound) {
		return Task{}, e
	}
	if s.modules == nil {
		return Task{}, errors.New("task module lookup is required")
	}
	moduleValue, err := s.modules.Get(ctx, input.ModuleID)
	if err != nil {
		return Task{}, fmt.Errorf("load task module: %w", err)
	}
	if moduleValue.ProjectID != input.ProjectID {
		return Task{}, errors.New("task module must belong to the task project")
	}
	// Creation records intent, not execution authority. No Coding configuration
	// is applicable to create-only or Upper work. An explicit configuration
	// still must exist and be enabled; every Coding admission checks it again.
	if input.CodexConfigID != "" {
		if s.configs == nil {
			return Task{}, errors.New("task codex config lookup is required")
		}
		configValue, err := s.configs.Get(ctx, input.CodexConfigID)
		if err != nil {
			return Task{}, fmt.Errorf("load task codex config: %w", err)
		}
		if !configValue.Enabled {
			return Task{}, &codexruntime.SelectionError{Kind: codexruntime.BackendConfigurationUnavailable}
		}
	}
	// A planning intent has no repository authority. Legacy callers may still
	// supply an exact repository/base pair; neither is inferred from Project.
	var base string
	if input.RepositoryID != "" || input.BaseRevision != "" {
		if input.RepositoryID == "" || input.BaseRevision == "" || s.resolver == nil {
			return Task{}, errors.New("explicit task repository requires a base resolver and revision")
		}
		base, err = s.resolver.ResolveBase(ctx, input.RepositoryID, input.BaseRevision)
		if err != nil {
			return Task{}, fmt.Errorf("resolve task base commit: %w", err)
		}
		if !fullCommit(base) {
			return Task{}, errors.New("task base must be an immutable commit")
		}
	}
	id, err := s.newID()
	if err != nil {
		return Task{}, fmt.Errorf("allocate task id: %w", err)
	}
	now := s.now()
	var initialPhase Phase
	var requiredDepth *ReviewDepth
	if input.SkipReview {
		initialPhase = PhaseImplementationReady
		requiredDepth = nil
	} else {
		initialPhase = PhaseReviewRequired
		requiredDepth = ReviewDepthPointer(ReviewDepthFast)
	}
	value := Task{Title: title, Phase: initialPhase, RequiredReviewDepth: requiredDepth, Type: TypeDevelopment, Scope: ScopeFeatureArea, ID: id, ProjectID: input.ProjectID, FeatureAreaID: input.FeatureAreaID, ModuleID: input.ModuleID, RepositoryID: input.RepositoryID, CodexConfigID: input.CodexConfigID, WorkKind: input.WorkKind, Model: input.Model, ReasoningEffort: input.ReasoningEffort, ProblemStatement: problem, Goal: title, Constraints: append([]string(nil), input.Constraints...), BaseCommit: base, Status: StatusActive, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	receipted, ok := s.store.(interface {
		CreateIntent(context.Context, Task, string, string, string) (Task, error)
	})
	if !ok {
		return Task{}, errors.New("durable Task creation receipts unavailable")
	}
	created, err := receipted.CreateIntent(ctx, value, input.AISessionID, input.RequestID, intentPayloadHash(input))
	if err != nil {
		return Task{}, err
	}
	logCtx := observability.WithContext(ctx, observability.Correlation{ProjectID: created.ProjectID, FeatureAreaID: created.FeatureAreaID, TaskID: created.ID})
	observability.Info(logCtx, "task", "task.created", "durable Task created", map[string]any{"status": created.Status, "planning": input.Planning, "moduleId": created.ModuleID, "repositoryId": created.RepositoryID, "workKind": created.WorkKind, "model": created.Model, "reasoningEffort": created.ReasoningEffort})
	return created, nil
}

// ClassifyWorkKind updates the optional organization-only work kind on an
// existing Task. It rejects a value outside the closed enum and is a pure
// metadata update: it never changes WorkSpec, VerificationSpec, status,
// authorization, or execution routing.
func (s *Service) ClassifyWorkKind(ctx context.Context, id string, kind WorkKind) (Task, error) {
	if !ValidWorkKind(kind) {
		return Task{}, errors.New("task work kind must be null or one of the closed work-kind enum")
	}
	return s.store.Update(ctx, id, func(value *Task) error {
		if value.WorkKind != kind {
			value.WorkKind = kind
			value.UpdatedAt = s.now().UTC()
		}
		return nil
	})
}

// ClassifyExecutionSelection freezes the GPT-selected Codex model and
// reasoning effort on an existing Task. It is pure execution metadata and
// never changes prompt bytes, WorkSpec, or lifecycle.
func (s *Service) ClassifyExecutionSelection(ctx context.Context, id, model, reasoningEffort string) (Task, error) {
	if err := codexruntime.ValidateModelID(model); err != nil || !containsString(codexruntime.AllReasoningEfforts, reasoningEffort) {
		return Task{}, errors.New("task model and reasoning effort must be valid runtime values")
	}
	return s.store.Update(ctx, id, func(value *Task) error {
		if value.Model != model || value.ReasoningEffort != reasoningEffort {
			value.Model, value.ReasoningEffort = model, reasoningEffort
			value.UpdatedAt = s.now().UTC()
		}
		return nil
	})
}

func (s *Service) Get(ctx context.Context, id string) (Task, error) { return s.store.Get(ctx, id) }
func (s *Service) List(ctx context.Context) ([]Task, error)         { return s.store.List(ctx) }

func (s *Service) LatestForFeatureArea(ctx context.Context, featureAreaID string) (Task, error) {
	if store, ok := s.store.(interface {
		LatestByFeatureArea(context.Context, string) (Task, error)
	}); ok {
		return store.LatestByFeatureArea(ctx, featureAreaID)
	}
	values, err := s.store.List(ctx)
	if err != nil {
		return Task{}, err
	}
	var latest Task
	for _, value := range values {
		if value.FeatureAreaID != featureAreaID || (latest.ID != "" && (value.CreatedAt.Before(latest.CreatedAt) || value.CreatedAt.Equal(latest.CreatedAt) && value.ID >= latest.ID)) {
			continue
		}
		latest = value
	}
	if latest.ID == "" {
		return Task{}, ErrNotFound
	}
	return latest, nil
}

// ActiveFeatureAreaIDsForProject returns at most two distinct Topic identities with
// non-terminal Task lineage. Two results are sufficient to represent ambiguity
// without scanning or projecting historical work.
func (s *Service) ActiveFeatureAreaIDsForProject(ctx context.Context, projectID string) ([]string, error) {
	if store, ok := s.store.(interface {
		ActiveFeatureAreaIDsByProject(context.Context, string, int) ([]string, error)
	}); ok {
		return store.ActiveFeatureAreaIDsByProject(ctx, projectID, 2)
	}
	values, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	result := make([]string, 0, 2)
	for _, value := range values {
		if value.ProjectID != projectID || value.FeatureAreaID == "" || !isActive(value.Status) {
			continue
		}
		if _, exists := seen[value.FeatureAreaID]; exists {
			continue
		}
		seen[value.FeatureAreaID] = struct{}{}
		result = append(result, value.FeatureAreaID)
		if len(result) == 2 {
			break
		}
	}
	sort.Strings(result)
	return result, nil
}

func isActive(status Status) bool {
	return status == StatusActive
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "task_" + hex.EncodeToString(raw[:]), nil
}

func fullCommit(value string) bool {
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

// ReplayCreate reads only the durable intent receipt; changed environment or
// lifecycle prerequisites cannot turn a committed request into a new Task.
func (s *Service) ReplayCreate(ctx context.Context, input CreateInput) (Task, error) {
	reader, ok := s.store.(interface {
		GetIntent(context.Context, string, string, string) (Task, error)
	})
	if !ok || input.RequestID == "" {
		return Task{}, ErrNotFound
	}
	return reader.GetIntent(ctx, input.ProjectID, input.RequestID, intentPayloadHash(input))
}
func intentPayloadHash(input CreateInput) string {
	encoded, _ := json.Marshal(struct {
		Area, Repository, Goal, Problem string
		Constraints                     []string
		SkipReview                      bool `json:",omitempty"`
	}{input.FeatureAreaID, input.RepositoryID, input.Goal, input.ProblemStatement, input.Constraints, input.SkipReview})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

// ValidateIntentText is shared by direct Task creation and startup preflight.
func ValidateIntentText(title, problem string, constraints []string) error {
	if title == "" || len(title) > MaxGoalBytes || strings.ContainsRune(title, '\x00') || len(problem) > MaxGoalBytes || strings.ContainsRune(problem, '\x00') {
		return errors.New("Task intent title and problem statement are required and must be bounded")
	}
	if len(constraints) > 128 {
		return errors.New("too many Task constraints")
	}
	for _, constraint := range constraints {
		if len(constraint) > 4096 || strings.ContainsRune(constraint, '\x00') {
			return errors.New("Task constraint must be bounded")
		}
	}
	return nil
}
