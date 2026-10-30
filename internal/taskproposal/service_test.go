package taskproposal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskproposal"
)

type historySessions map[string]aisession.Session

func (s historySessions) Get(_ context.Context, id string) (aisession.Session, error) {
	v, ok := s[id]
	if !ok {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return v, nil
}

type historyStore struct {
	*sqlite.TaskProposalStore
	values []taskproposal.Proposal
}

func (s historyStore) Get(_ context.Context, id string) (taskproposal.Proposal, error) {
	for _, v := range s.values {
		if v.ID == id {
			return v, nil
		}
	}
	return taskproposal.Proposal{}, taskproposal.ErrNotFound
}
func (s historyStore) ListBySession(context.Context, string) ([]taskproposal.Proposal, error) {
	return s.values, nil
}
func historicalService(t *testing.T, values ...taskproposal.Proposal) *taskproposal.Service {
	t.Helper()
	service, err := taskproposal.NewService(historyStore{sqlite.NewTaskProposalStore(nil), values}, historySessions{"session-a": {ID: "session-a", ProjectID: "project-a"}, "session-b": {ID: "session-b", ProjectID: "project-b"}}, nil, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestLegacyTaskProposalMutationPathsAreExplicitlyRetired(t *testing.T) {
	ctx := context.Background()
	service := historicalService(t)
	store := sqlite.NewTaskProposalStore(nil)
	for _, kind := range []taskproposal.Kind{taskproposal.KindCreateTask, taskproposal.KindContinueTask} {
		if _, err := service.Create(ctx, taskproposal.CreateInput{AISessionID: "session-a", Kind: kind}); !errors.Is(err, taskproposal.ErrRetired) {
			t.Fatalf("%s authoring=%v", kind, err)
		}
	}
	for name, call := range map[string]func() error{
		"confirm":                        func() error { _, err := service.Confirm(ctx, "session-a", "historical"); return err },
		"reject":                         func() error { _, err := service.Reject(ctx, "session-a", "historical"); return err },
		"apply":                          func() error { _, err := service.Apply(ctx, "session-a", "historical"); return err },
		"store create":                   func() error { return store.Create(ctx, taskproposal.Proposal{}) },
		"store confirm":                  func() error { _, err := store.Confirm(ctx, "historical", time.Now()); return err },
		"store reject":                   func() error { _, err := store.Reject(ctx, "historical", time.Now()); return err },
		"store create consumption":       func() error { _, err := store.ConsumeCreate(ctx, "historical", task.Task{}, time.Now()); return err },
		"store continuation consumption": func() error { _, err := store.ConsumeContinuation(ctx, "historical", time.Now()); return err },
	} {
		if err := call(); !errors.Is(err, taskproposal.ErrRetired) {
			t.Fatalf("%s=%v", name, err)
		}
	}
}

func TestLegacyTaskProposalHistoryRemainsSessionAndProjectScoped(t *testing.T) {
	ctx := context.Background()
	work, verification := explicitSpecs()
	own := taskproposal.Proposal{ID: "historical", AISessionID: "session-a", ProjectID: "project-a", Kind: taskproposal.KindCreateTask, Status: taskproposal.StatusConfirmed, Goal: work.Goal, WorkSpec: work, VerificationSpec: verification}
	foreign := own
	foreign.ID = "foreign"
	foreign.ProjectID = "project-b"
	foreign.AISessionID = "session-b"
	service := historicalService(t, own, foreign)
	listed, err := service.List(ctx, "session-a", taskproposal.ListOptions{})
	if err != nil || len(listed) != 1 || listed[0].ID != own.ID {
		t.Fatalf("history=%+v err=%v", listed, err)
	}
	if _, err := service.Get(ctx, "session-a", foreign.ID); !errors.Is(err, taskproposal.ErrNotFound) {
		t.Fatalf("foreign history exposed=%v", err)
	}
	if _, err := service.Get(ctx, "session-b", own.ID); !errors.Is(err, taskproposal.ErrNotFound) {
		t.Fatalf("foreign session read=%v", err)
	}
}
