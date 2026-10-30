package worktree

import (
	"context"
	"errors"
	"fmt"
)

type DurableManager struct {
	External interface {
		Create(context.Context, CreateInput) (Worktree, error)
	}
	Store Store
}

func (m *DurableManager) Create(ctx context.Context, input CreateInput) (Worktree, error) {
	if m == nil || m.External == nil || m.Store == nil {
		return Worktree{}, errors.New("durable worktree manager dependencies are required")
	}
	if input.WorkspaceID != "" {
		key := input.WorkspaceID
		if input.MemberID != "" {
			key = memberWorktreeKey(input)
		}
		v, err := m.Store.Get(ctx, "wt_"+key)
		if err == nil {
			if v.RepositoryID != input.RepositoryID || v.BaseCommit != input.BaseCommit || v.Status != StatusReady {
				return Worktree{}, ErrCollision
			}
			return v, m.Validate(ctx, v)
		}
		if !errors.Is(err, ErrNotFound) {
			return Worktree{}, err
		}
	}
	value, err := m.External.Create(ctx, input)
	if err != nil {
		return Worktree{}, err
	}
	if err := m.Store.Create(ctx, value); err != nil {
		return Worktree{}, fmt.Errorf("persist worktree metadata: %w", err)
	}
	return value, nil
}

func (m *DurableManager) Get(ctx context.Context, id string) (Worktree, error) {
	if m == nil || m.Store == nil {
		return Worktree{}, errors.New("durable worktree store is required")
	}
	return m.Store.Get(ctx, id)
}

func (m *DurableManager) Validate(ctx context.Context, v Worktree) error {
	checker, ok := m.External.(interface {
		Validate(context.Context, Worktree) error
	})
	if !ok {
		return errors.New("workspace validation unavailable")
	}
	return checker.Validate(ctx, v)
}
