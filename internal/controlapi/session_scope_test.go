package controlapi

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type scopeSessions map[string]aisession.Session

func (s scopeSessions) Get(_ context.Context, id string) (aisession.Session, error) {
	value, ok := s[id]
	if !ok {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return value, nil
}

// GetOwned models the production caller-scoped Session store after the fixture
// has explicitly supplied the Session inventory. Tests that need caller-scope
// rejection use the real SQLite store instead of this bounded unit double.
func (s scopeSessions) GetOwned(ctx context.Context, id string) (aisession.Session, error) {
	return s.Get(ctx, id)
}

type scopeTasks map[string]task.Task

func (s scopeTasks) Create(context.Context, task.CreateInput) (task.Task, error) {
	return task.Task{}, nil
}
func (s scopeTasks) Get(_ context.Context, id string) (task.Task, error) {
	value, ok := s[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return value, nil
}
func (s scopeTasks) List(context.Context) ([]task.Task, error) {
	values := make([]task.Task, 0, len(s))
	for _, value := range s {
		values = append(values, value)
	}
	return values, nil
}

type scopeRuns map[string]run.Run

func (s scopeRuns) Get(_ context.Context, id string) (run.Run, error) {
	value, ok := s[id]
	if !ok {
		return run.Run{}, run.ErrNotFound
	}
	return value, nil
}
func (s scopeRuns) List(context.Context) ([]run.Run, error) {
	values := make([]run.Run, 0, len(s))
	for _, value := range s {
		values = append(values, value)
	}
	return values, nil
}
func (s scopeRuns) LatestForTask(_ context.Context, taskID string, purpose run.Purpose, executionID string) (run.Run, error) {
	var latest run.Run
	for _, value := range s {
		if value.TaskID != taskID || value.Purpose != purpose || executionID != "" && value.ExecutionProposalID != executionID {
			continue
		}
		if latest.ID == "" || value.CreatedAt.After(latest.CreatedAt) {
			latest = value
		}
	}
	if latest.ID == "" {
		return run.Run{}, run.ErrNotFound
	}
	return latest, nil
}

type scopeRunner struct{ cancelCalls int }

func (*scopeRunner) StartRun(context.Context, string) (run.Run, error) { return run.Run{}, nil }
func (s *scopeRunner) CancelRun(_ context.Context, id string) (run.Run, error) {
	s.cancelCalls++
	return run.Run{ID: id, Status: run.StatusCanceled}, nil
}

func TestSessionScopedRunAndApprovalOperationsHideForeignProjects(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	runs := scopeRuns{
		"run-a": {ID: "run-a", TaskID: "task-a", Status: run.StatusWaitingApproval, CreatedAt: now},
		"run-b": {ID: "run-b", TaskID: "task-b", Status: run.StatusWaitingApproval, CreatedAt: now},
	}
	approvals := &approvalServiceStub{values: []approval.Approval{
		{ID: "approval-a", RunID: "run-a", Status: approval.StatusPending, CreatedAt: now},
		{ID: "approval-b", RunID: "run-b", Status: approval.StatusPending, CreatedAt: now},
	}}
	runner := &scopeRunner{}
	facade := &Facade{deps: Dependencies{
		AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		Tasks:      scopeTasks{"task-a": {ID: "task-a", ProjectID: "project-a"}, "task-b": {ID: "task-b", ProjectID: "project-b"}},
		Runs:       runs,
		Approvals:  approvals,
		Runner:     runner,
	}}

	if value, err := facade.GetRunForSession(context.Background(), "session-a", "run-a"); err != nil || value.ID != "run-a" {
		t.Fatalf("own run=%+v err=%v", value, err)
	}
	assertNotFound := func(name string, err error) {
		t.Helper()
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != CodeNotFound {
			t.Fatalf("%s error=%v", name, err)
		}
	}
	_, err := facade.GetRunForSession(context.Background(), "session-a", "run-b")
	assertNotFound("foreign run", err)
	_, err = facade.CancelRunForSession(context.Background(), "session-a", "run-b")
	assertNotFound("foreign cancel", err)
	if runner.cancelCalls != 0 {
		t.Fatalf("foreign cancel reached supervisor %d times", runner.cancelCalls)
	}
	listedRuns, err := facade.ListRunsForSession(context.Background(), "session-a", RunFilter{})
	if err != nil || len(listedRuns) != 1 || listedRuns[0].ID != "run-a" {
		t.Fatalf("runs=%+v err=%v", listedRuns, err)
	}
	listedApprovals, err := facade.ListApprovalsForSession(context.Background(), "session-a", ApprovalFilter{PendingOnly: true})
	if err != nil || len(listedApprovals) != 1 || listedApprovals[0].ID != "approval-a" {
		t.Fatalf("approvals=%+v err=%v", listedApprovals, err)
	}
	_, err = facade.GetApprovalForSession(context.Background(), "session-a", "approval-b")
	assertNotFound("foreign approval", err)
	_, err = facade.DecideApprovalForSession(context.Background(), "session-a", "approval-b", approval.Decision{Type: approval.DecisionDecline})
	assertNotFound("foreign approval decision", err)
}

func TestClassifyTaskExecutionSelectionValidatesAgainstRuntimeAllowlist(t *testing.T) {
	tasks := scopeTasks{
		"task-a": {ID: "task-a", ProjectID: "project-a"},
	}
	facade := &Facade{
		deps: Dependencies{
			AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
			Tasks:      tasks,
		},
		allowlist: codexruntime.Allowlist{Models: []string{"gpt-5.6-sol"}, ReasoningEfforts: []string{"xhigh"}},
	}
	// Use a minimal updater to prove project-boundary validation still rejects
	// foreign Tasks before runtime selection validation reaches the store.
	updater := &selectionTaskStub{values: tasks}
	facade.deps.Tasks = updater
	if _, err := facade.ClassifyTaskExecutionSelection(context.Background(), "session-a", "task-b", "gpt-5.6-sol", "xhigh"); err == nil {
		t.Fatal("foreign Task selection accepted")
	}
}

func TestFacadeExecutionAllowlistReadsCurrentAuthoritySnapshot(t *testing.T) {
	authority, err := codexruntime.NewAuthority(codexruntime.Allowlist{Models: []string{"gpt-5.6-luna"}, ReasoningEfforts: []string{"low"}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := &selectionTaskStub{values: scopeTasks{
		"task-a": {ID: "task-a", ProjectID: "project-a"},
		"task-b": {ID: "task-b", ProjectID: "project-a"},
	}}
	facade := &Facade{allowlistProvider: authority, deps: Dependencies{
		AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		Tasks:      tasks,
	}}
	before := facade.ExecutionAllowlist()
	before.Models[0] = "mutated"
	if _, err := facade.ClassifyTaskExecutionSelection(context.Background(), "session-a", "task-a", "gpt-5.6-luna", "low"); err != nil {
		t.Fatalf("initial Task admission: %v", err)
	}
	if _, err := authority.Apply(1, codexruntime.Allowlist{Models: []string{"gpt-6-astra"}, ReasoningEfforts: []string{"xhigh"}}); err != nil {
		t.Fatal(err)
	}
	after := facade.ExecutionAllowlist()
	config, ok := after.ModelConfig("gpt-6-astra")
	if len(after.Models) != 1 || after.Models[0] != "gpt-6-astra" || !ok || config.DefaultReasoningEffort != "xhigh" {
		t.Fatalf("Facade retained startup inventory: %+v", after)
	}
	if _, err := facade.ClassifyTaskExecutionSelection(context.Background(), "session-a", "task-b", "gpt-5.6-luna", "xhigh"); err == nil || !strings.Contains(err.Error(), "MODEL_NOT_ALLOWED") {
		t.Fatalf("removed model admitted for new Task: %v", err)
	}
	if _, err := facade.ClassifyTaskExecutionSelection(context.Background(), "session-a", "task-b", "gpt-6-astra", "low"); err != nil {
		t.Fatalf("model capability effort rejected after legacy inventory migration: %v", err)
	}
	if _, err := facade.ClassifyTaskExecutionSelection(context.Background(), "session-a", "task-b", "gpt-6-astra", "xhigh"); err != nil {
		t.Fatalf("hot Task admission: %v", err)
	}
}

func TestClassifyTaskExecutionSelectionRejectsValuesOutsideUniverse(t *testing.T) {
	facade := &Facade{
		allowlist: codexruntime.Allowlist{Models: []string{"gpt-5.6-sol"}, ReasoningEfforts: []string{"xhigh"}},
	}
	err := facade.allowlist.ValidateModel("gpt-5.6-luna")
	if err == nil || !strings.Contains(err.Error(), "MODEL_NOT_ALLOWED") {
		t.Fatalf("ValidateModel() = %v", err)
	}
	err = facade.allowlist.ValidateReasoningEffort("low")
	if err == nil || !strings.Contains(err.Error(), "REASONING_EFFORT_NOT_ALLOWED") {
		t.Fatalf("ValidateReasoningEffort() = %v", err)
	}
}

type selectionTaskStub struct {
	values scopeTasks
}

func (s *selectionTaskStub) Get(ctx context.Context, id string) (task.Task, error) {
	return s.values.Get(ctx, id)
}
func (s *selectionTaskStub) Create(context.Context, task.CreateInput) (task.Task, error) {
	return task.Task{}, nil
}
func (s *selectionTaskStub) List(context.Context) ([]task.Task, error) {
	return s.values.List(context.Background())
}
func (s *selectionTaskStub) ClassifyExecutionSelection(_ context.Context, id, model, reasoningEffort string) (task.Task, error) {
	value, ok := s.values[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	value.Model, value.ReasoningEffort = model, reasoningEffort
	s.values[id] = value
	return value, nil
}

func TestSessionApprovalListUsesDirectReviewRunProject(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	facade := &Facade{deps: Dependencies{
		AISessions: scopeSessions{"session-a": {ID: "session-a", ProjectID: "project-a"}},
		Tasks:      scopeTasks{"legacy-task": {ID: "legacy-task", ProjectID: "project-a"}},
		Runs: scopeRuns{
			"review-run":         {ID: "review-run", ProjectID: "project-a", ReviewID: "review-a", OperationKind: run.OperationReview, OperationID: "review-a"},
			"foreign-review-run": {ID: "foreign-review-run", ProjectID: "project-b", ReviewID: "review-b", OperationKind: run.OperationReview, OperationID: "review-b"},
			"foreign-owner-run":  {ID: "foreign-owner-run", ProjectID: "project-b", TaskID: "legacy-task"},
			"legacy-run":         {ID: "legacy-run", TaskID: "legacy-task"},
		},
		Approvals: &approvalServiceStub{values: []approval.Approval{
			{ID: "review-approval", RunID: "review-run", Status: approval.StatusPending, CreatedAt: now},
			{ID: "foreign-review-approval", RunID: "foreign-review-run", Status: approval.StatusPending, CreatedAt: now},
			{ID: "foreign-owner-approval", RunID: "foreign-owner-run", Status: approval.StatusPending, CreatedAt: now},
			{ID: "legacy-approval", RunID: "legacy-run", Status: approval.StatusPending, CreatedAt: now},
		}},
	}}
	values, err := facade.ListApprovalsForSession(ctx, "session-a", ApprovalFilter{PendingOnly: true})
	if err != nil || len(values) != 2 {
		t.Fatalf("approvals=%+v err=%v", values, err)
	}
	seen := map[string]bool{}
	for _, value := range values {
		seen[value.ID] = true
	}
	if !seen["review-approval"] || !seen["legacy-approval"] {
		t.Fatalf("wrong scoped approvals: %+v", values)
	}
}
