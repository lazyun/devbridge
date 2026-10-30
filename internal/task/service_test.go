package task

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/module"
)

type resolver struct {
	commits map[string]string
	calls   int
}

func taskRegistries(t *testing.T, projectID string, enabled bool) (*module.MemoryStore, *codexconfig.MemoryStore) {
	t.Helper()
	modules := module.NewMemoryStore()
	configs := codexconfig.NewMemoryStore()
	now := time.Unix(1, 0).UTC()
	if err := modules.Create(context.Background(), module.Module{ID: "module", ProjectID: projectID, Key: "core", Name: "Core", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := configs.Create(context.Background(), codexconfig.Config{ID: "config", Name: "Config", Enabled: enabled, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return modules, configs
}

func (r *resolver) ResolveBase(_ context.Context, repositoryID, revision string) (string, error) {
	r.calls++
	value, ok := r.commits[repositoryID+":"+revision]
	if !ok {
		return "", errors.New("missing revision")
	}
	return value, nil
}

func explicitCreateInput(goal string, constraints []string) CreateInput {
	return CreateInput{RequestID: "test-request", FeatureAreaID: "feature", Goal: goal, Constraints: constraints, WorkSpec: WorkSpec{Goal: goal, Constraints: append([]string(nil), constraints...), CodexInstruction: goal, ExecutionScope: ExecutionScope{Kind: ExecutionScopeRepository}}, VerificationSpec: VerificationSpec{Mode: VerificationModeNone, Waiver: &VerificationWaiver{Source: VerificationWaiverUser, Reason: "test explicitly waives verification"}}}
}

func TestCreateCapturesImmutableBaseAndClones(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { return "task-1", nil }, func() time.Time { return clock })
	constraints := []string{"stay scoped"}
	input := explicitCreateInput("change it", constraints)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	constraints[0] = "mutated"
	if value.Status != StatusActive || value.BaseCommit != resolve.commits["repo:main"] || !reflect.DeepEqual(value.Constraints, []string{"stay scoped"}) {
		t.Fatalf("task=%+v", value)
	}
	resolve.commits["repo:main"] = "2222222222222222222222222222222222222222"
	got, _ := service.Get(context.Background(), value.ID)
	if got.BaseCommit != value.BaseCommit {
		t.Fatalf("base changed: %s", got.BaseCommit)
	}
	list, _ := service.List(context.Background())
	if len(list) != 1 {
		t.Fatalf("list=%v", list)
	}
}

func TestTaskIntentCannotAuthorizeImplementation(t *testing.T) {
	resolve := &resolver{commits: map[string]string{"repo:base": strings.Repeat("a", 40)}}
	modules, configs := taskRegistries(t, "project", true)
	svc := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { return "task", nil }, nil)
	in := CreateInput{RequestID: "intent-request", ProjectID: "project", FeatureAreaID: "feature", ModuleID: "module", RepositoryID: "repo", CodexConfigID: "config", BaseRevision: "base", Goal: "Task title", ProblemStatement: "Long-lived intent"}
	v, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if v.Status != StatusActive || v.Phase != PhaseReviewRequired || v.CurrentDefinitionRevisionID != "" || v.WorkSpec.Goal != "" || v.Title != in.Goal {
		t.Fatalf("intent acquired premature authority: %+v", v)
	}
	// Creation grants intent only. No generic lifecycle entry exists, and the
	// shared store fence rejects any ordinary Update callback that tries to
	// acquire Status or Phase authority.
	store := NewMemoryStore()
	svc = NewService(store, resolve, modules, configs, func() (string, error) { return "task2", nil }, nil)
	v2, err := svc.Create(context.Background(), CreateInput{RequestID: "intent-request-2", ProjectID: "project", FeatureAreaID: "feature", ModuleID: "module", RepositoryID: "repo", CodexConfigID: "config", BaseRevision: "base", Goal: "Second title", ProblemStatement: "Long-lived intent"})
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []func(value *Task) error{
		func(value *Task) error { value.Status = StatusReady; return nil },
		func(value *Task) error { value.Status = StatusRunning; return nil },
		func(value *Task) error { value.Status = StatusSucceeded; return nil },
		func(value *Task) error { value.Status = StatusFailed; return nil },
		func(value *Task) error { value.Status = StatusCanceled; return nil },
		func(value *Task) error { value.Phase = PhaseDelivering; return nil },
		func(value *Task) error { value.Phase = PhaseRecoveryRequired; return nil },
	} {
		if _, err = store.Update(context.Background(), v2.ID, attempt); !errors.Is(err, ErrConflict) {
			t.Fatalf("generic Update acquired Task gate authority: %v", err)
		}
		got, err := store.Get(context.Background(), v2.ID)
		if err != nil || got.Status != StatusActive || got.Phase != PhaseReviewRequired {
			t.Fatalf("rejected mutation changed Task: %+v err=%v", got, err)
		}
	}
}

func TestCreateValidationAndResolveFailure(t *testing.T) {
	modules, configs := taskRegistries(t, "project", true)
	service := NewService(NewMemoryStore(), &resolver{commits: map[string]string{}}, modules, configs, func() (string, error) { return "id", nil }, nil)
	if _, err := service.Create(context.Background(), CreateInput{}); err == nil {
		t.Fatal("invalid input accepted")
	}
	input := explicitCreateInput("goal", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "missing"
	if _, err := service.Create(context.Background(), input); err == nil {
		t.Fatal("invalid revision accepted")
	}
}

func TestCreateRequiresProjectModuleAndEnabledConfig(t *testing.T) {
	modules, enabled := taskRegistries(t, "project-one", true)
	_, disabled := taskRegistries(t, "project-one", false)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	input := explicitCreateInput("goal", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project-one", "module", "repo", "config", "main"
	service := NewService(NewMemoryStore(), resolve, modules, enabled, func() (string, error) { return "task", nil }, nil)
	if _, err := service.Create(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	crossProject := input
	crossProject.ProjectID = "project-two"
	if _, err := service.Create(context.Background(), crossProject); err == nil {
		t.Fatal("cross-project module accepted")
	}
	disabledService := NewService(NewMemoryStore(), resolve, modules, disabled, func() (string, error) { return "disabled-task", nil }, nil)
	if _, err := disabledService.Create(context.Background(), input); err == nil {
		t.Fatal("disabled config accepted")
	}
}

func TestCodexConfigAndResolvedBaseAreImmutable(t *testing.T) {
	store := NewMemoryStore()
	now := time.Unix(1, 0).UTC()
	value := Task{ID: "task", ProjectID: "project", ModuleID: "module", RepositoryID: "repo", CodexConfigID: "config", BaseCommit: "1111111111111111111111111111111111111111", Status: StatusReady, Type: TypeDevelopment, CreatedAt: now, UpdatedAt: now}
	if err := store.Create(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Update(context.Background(), value.ID, func(task *Task) error { task.CodexConfigID = "other"; return nil }); !errors.Is(err, ErrCodexConfigImmutable) {
		t.Fatalf("config mutation err=%v", err)
	}
	if _, err := store.Update(context.Background(), value.ID, func(task *Task) error { task.BaseCommit = "2222222222222222222222222222222222222222"; return nil }); !errors.Is(err, ErrBaseCommitImmutable) {
		t.Fatalf("base mutation err=%v", err)
	}
}

func TestCreateAcceptsNullAndClosedWorkKinds(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	sequence := 0
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { sequence++; return fmt.Sprintf("task-kind-%d", sequence), nil }, func() time.Time { return clock })
	for name, kind := range map[string]WorkKind{
		"null": "", "feature": WorkKindFeature, "bugFix": WorkKindBugFix, "refactor": WorkKindRefactor,
		"maintenance": WorkKindMaintenance, "testChange": WorkKindTestChange, "documentation": WorkKindDocumentation, "other": WorkKindOther,
	} {
		t.Run(name, func(t *testing.T) {
			input := explicitCreateInput("goal", nil)
			input.RequestID = "work-kind-" + name
			input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
			input.WorkKind = kind
			value, err := service.Create(context.Background(), input)
			if err != nil {
				t.Fatalf("create with work kind %q: %v", kind, err)
			}
			if value.WorkKind != kind {
				t.Fatalf("work kind=%q, want %q", value.WorkKind, kind)
			}
			persisted, err := service.Get(context.Background(), value.ID)
			if err != nil || persisted.WorkKind != kind {
				t.Fatalf("persisted work kind=%q err=%v", persisted.WorkKind, err)
			}
		})
	}
}

func TestCreateRejectsInvalidWorkKindWithoutInference(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { return "task-kind", nil }, func() time.Time { return clock })
	input := explicitCreateInput("Fix bug in checkout flow", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	input.WorkKind = WorkKind("TASK")
	if _, err := service.Create(context.Background(), input); err == nil {
		t.Fatal("invalid work kind accepted")
	}
	// Bridge never infers work kind from title, prompt, path, diff, or module.
	input.WorkKind = ""
	value, err := service.Create(context.Background(), input)
	if err != nil || value.WorkKind != "" {
		t.Fatalf("task=%+v err=%v, wanted null work kind", value, err)
	}
}

func TestClassifyWorkKindUpdatesOnlyClassificationMetadata(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { return "task-kind", nil }, func() time.Time { return clock })
	input := explicitCreateInput("goal", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.ClassifyWorkKind(context.Background(), value.ID, WorkKindRefactor)
	if err != nil {
		t.Fatal(err)
	}
	if updated.WorkKind != WorkKindRefactor || updated.Status != StatusActive || updated.BaseCommit != value.BaseCommit || !reflect.DeepEqual(updated.Constraints, value.Constraints) {
		t.Fatalf("classification changed non-metadata fields: %+v", updated)
	}
	if _, err := service.ClassifyWorkKind(context.Background(), value.ID, WorkKind("unknown")); err == nil {
		t.Fatal("invalid classification accepted")
	}
	if _, err := service.ClassifyWorkKind(context.Background(), "missing-task", WorkKindFeature); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing task classification err=%v", err)
	}
}

func TestCreateFreezesBoundedExecutionSelection(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	sequence := 0
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { sequence++; return fmt.Sprintf("task-selection-%d", sequence), nil }, func() time.Time { return clock })
	for name, model := range map[string]string{"luna": "gpt-5.6-luna", "terra": "gpt-5.6-terra", "sol": "gpt-5.6-sol", "custom": "gpt-6-codex-preview"} {
		t.Run(name, func(t *testing.T) {
			input := explicitCreateInput("goal", nil)
			input.RequestID = "selection-" + name
			input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
			input.Model, input.ReasoningEffort = model, "high"
			value, err := service.Create(context.Background(), input)
			if err != nil {
				t.Fatalf("create with model %q: %v", model, err)
			}
			persisted, err := service.Get(context.Background(), value.ID)
			if err != nil || persisted.Model != model || persisted.ReasoningEffort != "high" {
				t.Fatalf("persisted selection=%+v err=%v", persisted, err)
			}
		})
	}
}

func TestCreateRejectsInvalidSelectionAndClassifyFailsWithoutSourceMutation(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	service := NewService(NewMemoryStore(), resolve, modules, configs, func() (string, error) { return "task-selection", nil }, func() time.Time { return clock })
	for _, invalid := range []struct{ model, effort string }{
		{"bad model", "high"}, {"gpt-5.6-luna", "unsupported-effort"},
	} {
		input := explicitCreateInput("goal", nil)
		input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
		input.Model, input.ReasoningEffort = invalid.model, invalid.effort
		if _, err := service.Create(context.Background(), input); err == nil {
			t.Fatalf("invalid selection accepted: %+v", invalid)
		}
	}
	input := explicitCreateInput("goal", nil)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := service.ClassifyExecutionSelection(context.Background(), value.ID, "gpt-5.6-sol", "xhigh")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Model != "gpt-5.6-sol" || updated.ReasoningEffort != "xhigh" || updated.Status != StatusActive || !reflect.DeepEqual(updated.Constraints, value.Constraints) {
		t.Fatalf("selection classification changed non-selection fields: %+v", updated)
	}
	if _, err := service.ClassifyExecutionSelection(context.Background(), value.ID, "bad model", "xhigh"); err == nil {
		t.Fatal("invalid classifier model accepted")
	}
	if _, err := service.ClassifyExecutionSelection(context.Background(), "missing", "gpt-5.6-sol", "xhigh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing-task selection err=%v", err)
	}
}

func TestCreateSkipReviewAwaitsImplementationConfirmation(t *testing.T) {
	clock := time.Unix(100, 0)
	resolve := &resolver{commits: map[string]string{"repo:main": "1111111111111111111111111111111111111111"}}
	modules, configs := taskRegistries(t, "project", true)
	counter := 1
	newID := func() (string, error) {
		id := fmt.Sprintf("task-%d", counter)
		counter++
		return id, nil
	}
	service := NewService(NewMemoryStore(), resolve, modules, configs, newID, func() time.Time { return clock })
	constraints := []string{"no review needed"}
	input := explicitCreateInput("skip review task", constraints)
	input.ProjectID, input.ModuleID, input.RepositoryID, input.CodexConfigID, input.BaseRevision = "project", "module", "repo", "config", "main"
	input.SkipReview = true
	value, err := service.Create(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != StatusActive || value.Phase != PhaseImplementationReady || value.RequiredReviewDepth != nil {
		t.Fatalf("task with skipReview should have phase=IMPLEMENTATION_READY and null required_review_depth: %+v", value)
	}
	if value.CurrentReviewID != "" || value.CurrentExecutionID != "" || value.WorkSpec.ImplementationMode != "" {
		t.Fatalf("skip Review must await a separate implementation definition: %+v", value)
	}
	// Verify that non-skip review tasks still use default behavior
	input2 := input
	input2.RequestID = "intent-task-2"
	input2.SkipReview = false
	value2, err := service.Create(context.Background(), input2)
	if err != nil {
		t.Fatal(err)
	}
	if value2.Phase != PhaseReviewRequired || value2.RequiredReviewDepth == nil || *value2.RequiredReviewDepth != ReviewDepthFast {
		t.Fatalf("task without skipReview should use default REVIEW_REQUIRED phase: %+v", value2)
	}
}
