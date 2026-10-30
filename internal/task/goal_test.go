package task

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type memoryGoalStore struct {
	store *MemoryStore
	goals map[string][]Goal
}

func newMemoryGoalStore() *memoryGoalStore {
	return &memoryGoalStore{store: NewMemoryStore(), goals: make(map[string][]Goal)}
}

func (s *memoryGoalStore) Create(ctx context.Context, value Task) error {
	return s.store.Create(ctx, value)
}
func (s *memoryGoalStore) CreateIntent(ctx context.Context, value Task, session, request, hash string) (Task, error) {
	return s.store.CreateIntent(ctx, value, session, request, hash)
}
func (s *memoryGoalStore) GetIntent(ctx context.Context, project, request, hash string) (Task, error) {
	return s.store.GetIntent(ctx, project, request, hash)
}
func (s *memoryGoalStore) Get(ctx context.Context, id string) (Task, error) {
	return s.store.Get(ctx, id)
}
func (s *memoryGoalStore) List(ctx context.Context) ([]Task, error) { return s.store.List(ctx) }
func (s *memoryGoalStore) Update(ctx context.Context, id string, update func(*Task) error) (Task, error) {
	return s.store.Update(ctx, id, update)
}

func (s *memoryGoalStore) DefineGoals(_ context.Context, taskID string, goals []Goal) error {
	if _, ok := s.goals[taskID]; ok {
		return ErrGoalsAlreadyDefined
	}
	s.goals[taskID] = append([]Goal(nil), goals...)
	return nil
}

func (s *memoryGoalStore) Goals(_ context.Context, taskID string) ([]Goal, error) {
	return append([]Goal(nil), s.goals[taskID]...), nil
}

func goalService(t *testing.T, store *memoryGoalStore) *Service {
	t.Helper()
	modules, configs := taskRegistries(t, "project", true)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	sequence := 0
	return NewService(store, resolve, modules, configs, func() (string, error) { sequence++; return "id-" + strconv.Itoa(sequence), nil }, func() time.Time { return time.Unix(5, 0).UTC() })
}

func createGoalTask(t *testing.T, service *Service) Task {
	t.Helper()
	input := explicitCreateInput("goal", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDefineGoalsStoresOrderedListOnce(t *testing.T) {
	store := newMemoryGoalStore()
	service := goalService(t, store)
	ctx := context.Background()
	value := createGoalTask(t, service)
	defined, err := service.DefineGoals(ctx, value.ID, []GoalInput{{Title: "first"}, {Title: "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(defined) != 2 || defined[0].Position != 0 || defined[1].Position != 1 || defined[0].ID == "" || defined[0].ID == defined[1].ID {
		t.Fatalf("defined=%+v", defined)
	}
	if _, err := service.DefineGoals(ctx, value.ID, []GoalInput{{Title: "again"}}); !errors.Is(err, ErrGoalsAlreadyDefined) {
		t.Fatalf("set-once err=%v", err)
	}
	listed, err := service.ListGoals(ctx, value.ID)
	if err != nil || !reflect.DeepEqual(listed, defined) {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func TestDefineGoalsValidation(t *testing.T) {
	store := newMemoryGoalStore()
	service := goalService(t, store)
	ctx := context.Background()
	value := createGoalTask(t, service)
	cases := []struct {
		name   string
		titles []string
	}{
		{"empty", []string{}},
		{"blank title", []string{"first", ""}},
		{"duplicate", []string{"same", "same"}},
		{"nul", []string{"bad\x00title"}},
		{"too long", []string{strings.Repeat("x", MaxGoalTitleBytes+1)}},
	}
	for _, testCase := range cases {
		if _, err := service.DefineGoals(ctx, value.ID, titlesToInputs(testCase.titles)); err == nil {
			t.Fatalf("%s: expected error", testCase.name)
		}
	}
	if _, err := service.DefineGoals(ctx, value.ID, titlesToInputs(makeTitles(MaxGoalCount+1))); err == nil {
		t.Fatal("expected count error")
	}
	if _, err := service.DefineGoals(ctx, "missing", []GoalInput{{Title: "first"}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task err=%v", err)
	}
}

func titlesToInputs(titles []string) []GoalInput {
	inputs := make([]GoalInput, 0, len(titles))
	for _, title := range titles {
		inputs = append(inputs, GoalInput{Title: title})
	}
	return inputs
}

func makeTitles(count int) []string {
	titles := make([]string, count)
	for index := range titles {
		titles[index] = strings.Repeat("t", 4) + string(rune('a'+index%26)) + string(rune('a'+(index/26)%26))
	}
	return titles
}
