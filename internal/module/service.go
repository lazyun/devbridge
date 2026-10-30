package module

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/project"
)

var (
	ErrNotFound        = errors.New("module not found")
	ErrConflict        = errors.New("module conflict")
	ErrInvalidArgument = errors.New("invalid module argument")
)

var validKey = regexp.MustCompile(`^[a-z][a-z0-9._-]*$`)

type Store interface {
	Create(context.Context, Module) error
	Get(context.Context, string) (Module, error)
	ListByProject(context.Context, string) ([]Module, error)
}

type contextMapStore interface {
	ReplaceContextMap(context.Context, string, []Module, time.Time) error
}

type topologyContextMapStore interface {
	ReplaceContextMapAtTopology(context.Context, string, []Module, time.Time, int64) error
}

type ProjectLookup interface {
	Get(context.Context, string) (project.Project, error)
}

type CreateInput struct {
	ProjectID   string
	Key         string
	Name        string
	Description string
	ParentID    *string
}

type Service struct {
	store    Store
	projects ProjectLookup
	newID    func() (string, error)
	now      func() time.Time
}

func NewService(store Store, projects ProjectLookup, newID func() (string, error), now func() time.Time) *Service {
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, projects: projects, newID: newID, now: now}
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Module, error) {
	return s.create(ctx, input, false)
}

func (s *Service) create(ctx context.Context, input CreateInput, systemOwned bool) (Module, error) {
	if s == nil || s.store == nil || s.projects == nil {
		return Module{}, errors.New("module store and project lookup are required")
	}
	if input.ProjectID == "" {
		return Module{}, fmt.Errorf("%w: module project id is required", ErrInvalidArgument)
	}
	key := strings.TrimSpace(input.Key)
	if len(key) > 128 || (!systemOwned && !validKey.MatchString(key)) || (systemOwned && key != UnclassifiedKey) {
		return Module{}, fmt.Errorf("%w: module key must start with a lowercase letter and contain only lowercase letters, digits, '.', '_' or '-'", ErrInvalidArgument)
	}
	if !systemOwned && key == UnclassifiedKey {
		return Module{}, fmt.Errorf("%w: module key is reserved for the system Unclassified module", ErrInvalidArgument)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Module{}, fmt.Errorf("%w: module name is required", ErrInvalidArgument)
	}
	description := strings.TrimSpace(input.Description)
	if len(description) > 4096 {
		return Module{}, fmt.Errorf("%w: module description exceeds 4096 bytes", ErrInvalidArgument)
	}
	if _, err := s.projects.Get(ctx, input.ProjectID); err != nil {
		return Module{}, fmt.Errorf("load module project: %w", err)
	}
	id, err := s.newID()
	if err != nil {
		return Module{}, fmt.Errorf("allocate module id: %w", err)
	}
	var parentID *string
	if input.ParentID != nil {
		trimmed := strings.TrimSpace(*input.ParentID)
		if trimmed == "" {
			return Module{}, fmt.Errorf("%w: module parent id cannot be empty", ErrInvalidArgument)
		}
		if trimmed == id {
			return Module{}, errors.New("module cannot be its own parent")
		}
		parent, err := s.store.Get(ctx, trimmed)
		if err != nil {
			return Module{}, fmt.Errorf("load module parent: %w", err)
		}
		if parent.ProjectID != input.ProjectID {
			return Module{}, errors.New("module parent must belong to the same project")
		}
		if err := s.validateAncestry(ctx, id, parent); err != nil {
			return Module{}, err
		}
		parentID = &trimmed
	}
	now := s.now().UTC()
	value := Module{ID: id, ProjectID: input.ProjectID, Key: key, Name: name, Description: description, SystemOwned: systemOwned, ParentID: parentID, CreatedAt: now, UpdatedAt: now}
	if err := s.store.Create(ctx, value); err != nil {
		return Module{}, fmt.Errorf("store module: %w", err)
	}
	return clone(value), nil
}

// EnsureUnclassified lazily creates the sole system-owned fallback Module for
// a Project. A concurrent creator is resolved by re-reading the stable key.
func (s *Service) EnsureUnclassified(ctx context.Context, projectID string) (Module, error) {
	if projectID == "" {
		return Module{}, errors.New("module project id is required")
	}
	values, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return Module{}, err
	}
	for _, value := range values {
		if value.Key == UnclassifiedKey && value.SystemOwned {
			return clone(value), nil
		}
	}
	value, err := s.create(ctx, CreateInput{ProjectID: projectID, Key: UnclassifiedKey, Name: UnclassifiedName, Description: "System fallback for Tasks whose semantic module is not yet known."}, true)
	if err == nil {
		return value, nil
	}
	// Stores expose their own conflict sentinel. Re-read the stable system key
	// for any failed concurrent insert before returning the original error.
	values, listErr := s.store.ListByProject(ctx, projectID)
	if listErr != nil {
		return Module{}, listErr
	}
	for _, existing := range values {
		if existing.Key == UnclassifiedKey && existing.SystemOwned {
			return clone(existing), nil
		}
	}
	return Module{}, err
}

func (s *Service) validateAncestry(ctx context.Context, id string, parent Module) error {
	seen := map[string]struct{}{id: {}}
	current := parent
	for {
		if _, exists := seen[current.ID]; exists {
			return errors.New("module hierarchy cycle detected")
		}
		seen[current.ID] = struct{}{}
		if current.ParentID == nil {
			return nil
		}
		next, err := s.store.Get(ctx, *current.ParentID)
		if err != nil {
			return fmt.Errorf("load module ancestor: %w", err)
		}
		if next.ProjectID != current.ProjectID {
			return errors.New("module ancestor must belong to the same project")
		}
		current = next
	}
}

func (s *Service) Get(ctx context.Context, id string) (Module, error) {
	return s.store.Get(ctx, id)
}

func (s *Service) ListByProject(ctx context.Context, projectID string) ([]Module, error) {
	if projectID == "" {
		return nil, errors.New("module project id is required")
	}
	return s.store.ListByProject(ctx, projectID)
}

// ReplaceContextMap atomically replaces only the repository facts that make
// up the Project Module Map. Ordinary classification Modules remain durable;
// when a discovered key already exists, the map reuses that Module identity.
func (s *Service) ReplaceContextMap(ctx context.Context, projectID string, inputs []ContextModuleInput) ([]Module, error) {
	values, err := s.contextMapValues(ctx, projectID, inputs)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(contextMapStore)
	if !ok {
		return nil, errors.New("module store does not support Project Module Maps")
	}
	if err := store.ReplaceContextMap(ctx, projectID, values, s.now().UTC()); err != nil {
		return nil, err
	}
	return s.ListContextMapByProject(ctx, projectID)
}

// ReplaceContextMapAtTopology publishes the current-only module projection
// only while the Repository inventory remains at the bootstrap's observed
// topology. It is used by production Project Context bootstrap; in-memory
// stores intentionally retain the ordinary method for focused domain tests.
func (s *Service) ReplaceContextMapAtTopology(ctx context.Context, projectID string, inputs []ContextModuleInput, topologyRevision int64) ([]Module, error) {
	if topologyRevision < 1 {
		return nil, ErrInvalidArgument
	}
	values, err := s.contextMapValues(ctx, projectID, inputs)
	if err != nil {
		return nil, err
	}
	store, ok := s.store.(topologyContextMapStore)
	if !ok {
		return nil, errors.New("module store does not support topology-fenced Project Module Maps")
	}
	if err := store.ReplaceContextMapAtTopology(ctx, projectID, values, s.now().UTC(), topologyRevision); err != nil {
		return nil, err
	}
	return s.ListContextMapByProject(ctx, projectID)
}

func (s *Service) contextMapValues(ctx context.Context, projectID string, inputs []ContextModuleInput) ([]Module, error) {
	if projectID == "" || len(inputs) == 0 || len(inputs) > 100 {
		return nil, ErrInvalidArgument
	}
	if _, err := s.projects.Get(ctx, projectID); err != nil {
		return nil, fmt.Errorf("load module project: %w", err)
	}
	existing, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]Module, len(existing))
	for _, value := range existing {
		byKey[value.Key] = value
	}
	seen := make(map[string]struct{}, len(inputs))
	values := make([]Module, 0, len(inputs))
	now := s.now().UTC()
	for _, input := range inputs {
		key, name, description := strings.TrimSpace(input.Key), strings.TrimSpace(input.Name), strings.TrimSpace(input.Description)
		if !validKey.MatchString(key) || key == UnclassifiedKey || name == "" || len(key) > 128 || len(name) > 256 || len(description) > 4096 {
			return nil, ErrInvalidArgument
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, ErrConflict
		}
		seen[key] = struct{}{}
		facts, err := validateProductionFacts(input.ProductionFacts)
		if err != nil {
			return nil, err
		}
		value, found := byKey[key]
		if !found {
			id, err := s.newID()
			if err != nil {
				return nil, fmt.Errorf("allocate module id: %w", err)
			}
			value = Module{ID: id, ProjectID: projectID, Key: key, CreatedAt: now}
		}
		value.Name, value.Description, value.ProductionFacts, value.UpdatedAt = name, description, facts, now
		values = append(values, value)
	}
	return values, nil
}

// ListContextMapByProject returns only canonical Modules with current
// repository production facts. The stable Key is the external selection
// handle; callers need not expose Module IDs.
func (s *Service) ListContextMapByProject(ctx context.Context, projectID string) ([]Module, error) {
	values, err := s.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	result := make([]Module, 0, len(values))
	for _, value := range values {
		if len(value.ProductionFacts) != 0 {
			result = append(result, value)
		}
	}
	return result, nil
}

func validateProductionFacts(values []ProductionFacts) ([]ProductionFacts, error) {
	if len(values) == 0 || len(values) > 16 {
		return nil, ErrInvalidArgument
	}
	result := cloneProductionFacts(values)
	seenRepositories := map[string]struct{}{}
	for i := range result {
		value := &result[i]
		value.RepositoryID = strings.TrimSpace(value.RepositoryID)
		value.RepositoryName = strings.TrimSpace(value.RepositoryName)
		value.Responsibility = strings.TrimSpace(value.Responsibility)
		if value.RepositoryID == "" || value.RepositoryName == "" || value.Responsibility == "" || len(value.RepositoryID) > 128 || len(value.RepositoryName) > 256 || len(value.Responsibility) > 4096 {
			return nil, ErrInvalidArgument
		}
		if _, duplicate := seenRepositories[value.RepositoryID]; duplicate {
			return nil, ErrConflict
		}
		seenRepositories[value.RepositoryID] = struct{}{}
		switch value.ResponsibilitySource {
		case ResponsibilityGuidance, ResponsibilityPackageDoc, ResponsibilityDerived:
		default:
			return nil, ErrInvalidArgument
		}
		if err := validateFactList(value.ProductionRoots, 128, true); err != nil {
			return nil, err
		}
		if err := validateFactList(value.Entrypoints, 128, false); err != nil {
			return nil, err
		}
		if err := validateFactList(value.RelatedModules, 128, false); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func validateFactList(values []string, maximum int, required bool) error {
	if (required && len(values) == 0) || len(values) > maximum {
		return ErrInvalidArgument
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return ErrInvalidArgument
		}
		if _, duplicate := seen[value]; duplicate {
			return ErrConflict
		}
		seen[value] = struct{}{}
	}
	return nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "module_" + hex.EncodeToString(raw[:]), nil
}
