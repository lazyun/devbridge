// Package reviewgrant owns the immutable initial Feature Area Review authorization.
// It is deliberately separate from both mutable Run history and Task
// ExecutionScope.
package reviewgrant

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/run"
)

type Mode string

const (
	ModeFocused    Mode = "FOCUSED"
	ModeRepository Mode = "REPOSITORY"
)

type RelevanceRole string

const (
	RolePrimary    RelevanceRole = "PRIMARY"
	RoleSupporting RelevanceRole = "SUPPORTING"
)

var (
	ErrNotFound        = errors.New("initial Review grant not found")
	ErrConflict        = errors.New("initial Review grant conflict")
	ErrInvalidArgument = errors.New("invalid initial Review grant")
)

type ModuleSelection struct {
	Key  string        `json:"key"`
	Role RelevanceRole `json:"role"`
}

type Grant struct {
	ReviewID        string
	FeatureAreaID   string
	ProjectID       string
	RepositoryID    string
	Mode            Mode
	InitialScope    run.ReviewScope
	SelectedModules []ModuleSelection
	CreatedAt       time.Time
}

type Store interface {
	Create(context.Context, Grant) error
	Get(context.Context, string) (Grant, error)
}

type Service struct {
	store Store
	now   func() time.Time
}

func NewService(store Store, now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, errors.New("initial Review grant store is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}, nil
}

func (s *Service) Ensure(ctx context.Context, input Grant) (Grant, error) {
	value, err := validate(input)
	if err != nil {
		return Grant{}, err
	}
	value.CreatedAt = s.now().UTC()
	if err := s.store.Create(ctx, value); err == nil {
		return clone(value), nil
	} else if !errors.Is(err, ErrConflict) {
		return Grant{}, err
	}
	existing, err := s.store.Get(ctx, value.ReviewID)
	if err != nil {
		return Grant{}, err
	}
	if !equal(existing, value) {
		return Grant{}, ErrConflict
	}
	return clone(existing), nil
}

func (s *Service) Get(ctx context.Context, reviewID string) (Grant, error) {
	if strings.TrimSpace(reviewID) == "" {
		return Grant{}, ErrInvalidArgument
	}
	value, err := s.store.Get(ctx, reviewID)
	return clone(value), err
}

func validate(value Grant) (Grant, error) {
	if value.ReviewID == "" || value.FeatureAreaID == "" || value.ProjectID == "" || value.RepositoryID == "" {
		return Grant{}, ErrInvalidArgument
	}
	value.InitialScope.Paths = append([]string(nil), value.InitialScope.Paths...)
	value.SelectedModules = append([]ModuleSelection(nil), value.SelectedModules...)
	switch value.Mode {
	case ModeFocused:
		if value.InitialScope.Kind != run.ReviewScopePaths || len(value.InitialScope.Paths) == 0 || len(value.SelectedModules) == 0 || len(value.SelectedModules) > 128 {
			return Grant{}, ErrInvalidArgument
		}
		seen, primary := map[string]struct{}{}, false
		for _, selected := range value.SelectedModules {
			if selected.Key == "" || len(selected.Key) > 128 {
				return Grant{}, ErrInvalidArgument
			}
			if _, duplicate := seen[selected.Key]; duplicate {
				return Grant{}, ErrInvalidArgument
			}
			seen[selected.Key] = struct{}{}
			switch selected.Role {
			case RolePrimary:
				primary = true
			case RoleSupporting:
			default:
				return Grant{}, ErrInvalidArgument
			}
		}
		if !primary {
			return Grant{}, ErrInvalidArgument
		}
	case ModeRepository:
		if value.InitialScope.Kind != run.ReviewScopeRepository || len(value.InitialScope.Paths) != 0 || len(value.SelectedModules) != 0 {
			return Grant{}, ErrInvalidArgument
		}
		value.InitialScope.Paths = []string{}
		value.SelectedModules = []ModuleSelection{}
	default:
		return Grant{}, ErrInvalidArgument
	}
	return value, nil
}

func equal(left, right Grant) bool {
	if left.ReviewID != right.ReviewID || left.FeatureAreaID != right.FeatureAreaID || left.ProjectID != right.ProjectID || left.RepositoryID != right.RepositoryID || left.Mode != right.Mode || left.InitialScope.Kind != right.InitialScope.Kind || len(left.InitialScope.Paths) != len(right.InitialScope.Paths) || len(left.SelectedModules) != len(right.SelectedModules) {
		return false
	}
	lp, rp := append([]string(nil), left.InitialScope.Paths...), append([]string(nil), right.InitialScope.Paths...)
	sort.Strings(lp)
	sort.Strings(rp)
	for i := range lp {
		if lp[i] != rp[i] {
			return false
		}
	}
	ls, rs := append([]ModuleSelection(nil), left.SelectedModules...), append([]ModuleSelection(nil), right.SelectedModules...)
	sort.Slice(ls, func(i, j int) bool { return ls[i].Key < ls[j].Key })
	sort.Slice(rs, func(i, j int) bool { return rs[i].Key < rs[j].Key })
	for i := range ls {
		if ls[i] != rs[i] {
			return false
		}
	}
	return true
}

func clone(value Grant) Grant {
	value.InitialScope.Paths = append([]string{}, value.InitialScope.Paths...)
	value.SelectedModules = append([]ModuleSelection{}, value.SelectedModules...)
	return value
}
