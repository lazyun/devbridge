package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MaxGoalCount bounds the ordered Task Goal list authored by GPT.
	MaxGoalCount = 16
	// MaxGoalTitleBytes bounds each Task Goal title.
	MaxGoalTitleBytes = 512
)

// Goal is one optional, ordered progress node authored with the Task. Goal
// definitions are immutable once stored; per-Execution progress lives in a
// separate projection keyed by Execution (execution proposal).
type Goal struct {
	ID        string
	TaskID    string
	Position  int
	Title     string
	CreatedAt time.Time
}

var ErrGoalsAlreadyDefined = errors.New("task goals are already defined")

// GoalInput is one validated Goal authoring entry.
type GoalInput struct {
	Title string
}

// GoalStore persists immutable Task Goal definitions.
type GoalStore interface {
	DefineGoals(ctx context.Context, taskID string, goals []Goal) error
	Goals(ctx context.Context, taskID string) ([]Goal, error)
}

// goalStore resolves the optional capability on the underlying Store.
func (s *Service) goalStore() (GoalStore, error) {
	if s.store == nil {
		return nil, errors.New("task store is required")
	}
	if store, ok := s.store.(GoalStore); ok {
		return store, nil
	}
	return nil, errors.New("task goal definitions require a goal-capable task store")
}

// DefineGoals stores the optional ordered Task Goals exactly once. Titles are
// non-empty, bounded, and free of NUL bytes; positions are implicit from list
// order. Definition never changes Task lifecycle.
func (s *Service) DefineGoals(ctx context.Context, id string, inputs []GoalInput) ([]Goal, error) {
	if len(inputs) == 0 {
		return nil, errors.New("task goal definitions must not be empty")
	}
	if len(inputs) > MaxGoalCount {
		return nil, fmt.Errorf("task goal count must not exceed %d", MaxGoalCount)
	}
	titles := make([]string, len(inputs))
	for index, input := range inputs {
		title := input.Title
		if title == "" || len(title) > MaxGoalTitleBytes || strings.ContainsRune(title, '\x00') {
			return nil, fmt.Errorf("task goal %d title is required and must be bounded", index)
		}
		for _, existing := range titles[:index] {
			if existing == title {
				return nil, fmt.Errorf("task goal %d title duplicates an earlier goal", index)
			}
		}
		titles[index] = title
	}
	store, err := s.goalStore()
	if err != nil {
		return nil, err
	}
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	existing, err := store.Goals(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, ErrGoalsAlreadyDefined
	}
	now := s.now().UTC()
	values := make([]Goal, 0, len(inputs))
	for index := range inputs {
		goalID, err := s.newID()
		if err != nil {
			return nil, fmt.Errorf("allocate task goal id: %w", err)
		}
		values = append(values, Goal{ID: "goal_" + goalID, TaskID: current.ID, Position: index, Title: titles[index], CreatedAt: now})
	}
	if err := store.DefineGoals(ctx, id, values); err != nil {
		return nil, err
	}
	return values, nil
}

// ListGoals returns the Task's Goal definitions ordered by position. An empty
// result means the Task has no Goal definitions.
func (s *Service) ListGoals(ctx context.Context, id string) ([]Goal, error) {
	store, err := s.goalStore()
	if err != nil {
		return nil, err
	}
	if _, err := s.store.Get(ctx, id); err != nil {
		return nil, err
	}
	return store.Goals(ctx, id)
}
