// Package classification provides the single, narrow Task classification
// transition. It records AI-session provenance but Tasks remain Project-owned.
package classification

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

var (
	ErrNotFound        = errors.New("classification entity not found")
	ErrConflict        = errors.New("task classification conflict")
	ErrInvalidArgument = errors.New("invalid task classification argument")
)

type Classification struct {
	ID           string
	AISessionID  string
	ProjectID    string
	TaskID       string
	FromModuleID string
	ToModuleID   string
	CreatedAt    time.Time
}

type Store interface {
	Classify(context.Context, Classification) (task.Task, error)
}
type SessionLookup interface {
	Get(context.Context, string) (aisession.Session, error)
}
type ModuleLookup interface {
	Get(context.Context, string) (module.Module, error)
}
type TaskLookup interface {
	Get(context.Context, string) (task.Task, error)
}
type RunLookup interface {
	List(context.Context) ([]run.Run, error)
}

type Service struct {
	store    Store
	sessions SessionLookup
	modules  ModuleLookup
	tasks    TaskLookup
	runs     RunLookup
	newID    func() (string, error)
	now      func() time.Time
}

func NewService(store Store, sessions SessionLookup, modules ModuleLookup, tasks TaskLookup, runs RunLookup, newID func() (string, error), now func() time.Time) (*Service, error) {
	if store == nil || sessions == nil || modules == nil || tasks == nil || runs == nil {
		return nil, errors.New("task classification dependencies are incomplete")
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sessions: sessions, modules: modules, tasks: tasks, runs: runs, newID: newID, now: now}, nil
}

func (s *Service) Classify(ctx context.Context, sessionID, taskID, targetModuleID string) (task.Task, error) {
	if sessionID == "" || taskID == "" || targetModuleID == "" {
		return task.Task{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return task.Task{}, err
	}
	taskValue, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return task.Task{}, err
	}
	if taskValue.ProjectID != session.ProjectID {
		return task.Task{}, ErrNotFound
	}
	from, err := s.modules.Get(ctx, taskValue.ModuleID)
	if err != nil {
		return task.Task{}, err
	}
	target, err := s.modules.Get(ctx, targetModuleID)
	if err != nil {
		return task.Task{}, err
	}
	if target.ProjectID != session.ProjectID {
		return task.Task{}, ErrNotFound
	}
	if !from.SystemOwned || from.Key != module.UnclassifiedKey || target.SystemOwned || target.Key == module.UnclassifiedKey {
		return task.Task{}, ErrConflict
	}
	runs, err := s.runs.List(ctx)
	if err != nil {
		return task.Task{}, err
	}
	for _, value := range runs {
		if value.TaskID == taskID && active(value.Status) {
			return task.Task{}, ErrConflict
		}
	}
	id, err := s.newID()
	if err != nil {
		return task.Task{}, fmt.Errorf("allocate task classification id: %w", err)
	}
	return s.store.Classify(ctx, Classification{ID: id, AISessionID: session.ID, ProjectID: session.ProjectID, TaskID: taskID, FromModuleID: from.ID, ToModuleID: target.ID, CreatedAt: s.now().UTC()})
}

func active(status run.Status) bool {
	return status == run.StatusPreparing || status == run.StatusRunning || status == run.StatusWaitingApproval || status == run.StatusCollecting
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "task_classification_" + hex.EncodeToString(raw[:]), nil
}
