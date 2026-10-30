package contextprobe

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
)

type canonicalStoreFixture struct {
	confirmed []ConfirmedInspection
	value     Probe
}

func (s *canonicalStoreFixture) Create(context.Context, Probe) error {
	return errors.New("legacy create must not be used")
}
func (s *canonicalStoreFixture) Get(context.Context, string) (Probe, error) {
	return Probe{}, ErrNotFound
}
func (s *canonicalStoreFixture) List(context.Context) ([]Probe, error) { return nil, nil }
func (s *canonicalStoreFixture) RecordPrompt(context.Context, string, string, time.Time) (Probe, error) {
	return Probe{}, ErrNotFound
}
func (s *canonicalStoreFixture) Update(context.Context, string, func(*Probe) error) (Probe, error) {
	return Probe{}, ErrNotFound
}
func (s *canonicalStoreFixture) RecoverRunning(context.Context, time.Time) error { return nil }
func (s *canonicalStoreFixture) ConfirmCanonicalInspection(_ context.Context, in ConfirmedInspection) (Probe, error) {
	s.confirmed = append(s.confirmed, in)
	s.value = in.Probe
	return clone(in.Probe), nil
}

type canonicalSessions map[string]aisession.Session

func (s canonicalSessions) Get(_ context.Context, id string) (aisession.Session, error) {
	v, ok := s[id]
	if !ok {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return v, nil
}

type canonicalRepositories struct {
	values map[string]repository.Repository
	states map[string]repository.State
}

func (r canonicalRepositories) Get(_ context.Context, id string) (repository.Repository, error) {
	v, ok := r.values[id]
	if !ok {
		return repository.Repository{}, errors.New("repository not found")
	}
	return v, nil
}
func (r canonicalRepositories) CurrentState(_ context.Context, id string) (repository.State, error) {
	v, ok := r.states[id]
	if !ok {
		return repository.State{}, errors.New("state not found")
	}
	return v, nil
}

type canonicalConfigs map[string]codexconfig.Config

func (c canonicalConfigs) Get(_ context.Context, id string) (codexconfig.Config, error) {
	v, ok := c[id]
	if !ok {
		return codexconfig.Config{}, errors.New("config not found")
	}
	return v, nil
}

type canonicalContexts struct{ ensures int }

func (c *canonicalContexts) EnsureRequired(_ context.Context, projectID string) (projectcontext.ProjectContext, error) {
	c.ensures++
	return projectcontext.ProjectContext{ProjectID: projectID}, nil
}
func (*canonicalContexts) MarkRunning(context.Context, string, string) (projectcontext.ProjectContext, error) {
	return projectcontext.ProjectContext{}, nil
}
func (*canonicalContexts) MarkFinished(context.Context, string, string, projectcontext.Status, string, string, string) (projectcontext.ProjectContext, error) {
	return projectcontext.ProjectContext{}, nil
}

func TestCanonicalInspectionPrepareIsPureAndConfirmUsesExactPlan(t *testing.T) {
	store := &canonicalStoreFixture{}
	contexts := &canonicalContexts{}
	config := codexconfig.Config{ID: "backend", Name: "Codex", CodexPath: "/codex", Enabled: true}
	repositories := canonicalRepositories{
		values: map[string]repository.Repository{
			"app": {ID: "app", ProjectID: "project", Name: "app", Path: t.TempDir()},
			"sdk": {ID: "sdk", ProjectID: "project", Name: "sdk", Path: t.TempDir()},
		},
		states: map[string]repository.State{
			"app": {HeadCommit: strings.Repeat("a", 40), BranchRef: "refs/heads/main"},
			"sdk": {HeadCommit: strings.Repeat("b", 40), BranchRef: "refs/heads/main"},
		},
	}
	service, err := NewService(store, canonicalSessions{"session": {ID: "session", ProjectID: "project"}}, repositories, canonicalConfigs{"backend": config}, contexts, func() (string, error) { return "inspection", nil }, func() time.Time { return time.Unix(100, 0) })
	if err != nil {
		t.Fatal(err)
	}
	in := PrepareInspectionInput{AISessionID: "session", RequestID: "request-1", CodexInstruction: "Inspect the app and SDK.", Backend: config.Selection("test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: "app", Targets: []TargetSelection{{RepositoryID: "sdk", Scope: Scope{Kind: ScopeRepository}}, {RepositoryID: "app", Scope: Scope{Kind: ScopeRepository}}}}
	first, err := service.PrepareInspection(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.PrepareInspection(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.PayloadSHA256 == "" || first.PayloadSHA256 != second.PayloadSHA256 {
		t.Fatalf("prepare replay changed payload: %#v %#v", first, second)
	}
	if contexts.ensures != 0 || len(store.confirmed) != 0 {
		t.Fatal("prepare produced durable side effects")
	}
	if len(first.Targets) != 2 || first.Targets[0].RepositoryID != "app" || first.Targets[1].RepositoryID != "sdk" || first.PrimaryMemberID != first.Targets[0].MemberID {
		t.Fatalf("unexpected exact target plan: %#v", first)
	}
	value, err := service.ConfirmInspection(context.Background(), ConfirmInspectionInput{Plan: first, PayloadSHA256: first.PayloadSHA256})
	if err != nil {
		t.Fatal(err)
	}
	if contexts.ensures != 1 || len(store.confirmed) != 1 {
		t.Fatal("confirm did not cross the single durable boundary")
	}
	if value.TaskID != first.TaskID || value.ID != first.InspectionID || value.Canonical == nil || value.CanonicalPayloadSHA256 != first.PayloadSHA256 || value.RepositoryID != "app" {
		t.Fatalf("unexpected confirmed probe: %#v", value)
	}
}

func TestCanonicalInspectionConfirmRejectsMutatedPlan(t *testing.T) {
	store := &canonicalStoreFixture{}
	contexts := &canonicalContexts{}
	config := codexconfig.Config{ID: "backend", Name: "Codex", CodexPath: "/codex", Enabled: true}
	repositories := canonicalRepositories{values: map[string]repository.Repository{"app": {ID: "app", ProjectID: "project", Path: t.TempDir()}}, states: map[string]repository.State{"app": {HeadCommit: strings.Repeat("a", 40)}}}
	service, _ := NewService(store, canonicalSessions{"session": {ID: "session", ProjectID: "project"}}, repositories, canonicalConfigs{"backend": config}, contexts, func() (string, error) { return "inspection", nil }, time.Now)
	plan, err := service.PrepareInspection(context.Background(), PrepareInspectionInput{AISessionID: "session", RequestID: "request-1", CodexInstruction: "Inspect.", Backend: config.Selection("test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: "app", Targets: []TargetSelection{{RepositoryID: "app", Scope: Scope{Kind: ScopeRepository}}}})
	if err != nil {
		t.Fatal(err)
	}
	plan.Model = "gpt-6-astra"
	if _, err = service.ConfirmInspection(context.Background(), ConfirmInspectionInput{Plan: plan, PayloadSHA256: plan.PayloadSHA256}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	if contexts.ensures != 0 || len(store.confirmed) != 0 {
		t.Fatal("mutated plan crossed durable boundary")
	}
}

func TestCanonicalInspectionConfirmRejectsSelfHashedForgedAuthority(t *testing.T) {
	store := &canonicalStoreFixture{}
	contexts := &canonicalContexts{}
	config := codexconfig.Config{ID: "backend", Name: "Codex", CodexPath: "/codex", Enabled: true}
	repositories := canonicalRepositories{values: map[string]repository.Repository{"app": {ID: "app", ProjectID: "project", Name: "app", Path: t.TempDir()}}, states: map[string]repository.State{"app": {HeadCommit: strings.Repeat("a", 40), BranchRef: "refs/heads/main"}}}
	service, _ := NewService(store, canonicalSessions{"session": {ID: "session", ProjectID: "project"}}, repositories, canonicalConfigs{"backend": config}, contexts, func() (string, error) { return "inspection", nil }, time.Now)
	prepare := func() InspectionConfirmationPlan {
		plan, err := service.PrepareInspection(context.Background(), PrepareInspectionInput{AISessionID: "session", RequestID: "request-1", CodexInstruction: "Inspect.", Backend: config.Selection("test").Input(), Model: "gpt-5.6-terra", ReasoningEffort: "high", PrimaryRepositoryID: "app", Targets: []TargetSelection{{RepositoryID: "app", Scope: Scope{Kind: ScopeRepository}}}})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	for name, mutate := range map[string]func(*InspectionConfirmationPlan){
		"backend provenance": func(plan *InspectionConfirmationPlan) { plan.Backend.Source = "CALLER_FORGED" },
		"repository name":    func(plan *InspectionConfirmationPlan) { plan.Targets[0].RepositoryName = "other" },
		"origin commit":      func(plan *InspectionConfirmationPlan) { plan.Targets[0].OriginCommit = strings.Repeat("b", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			plan := prepare()
			mutate(&plan)
			plan.PayloadSHA256, _ = inspectionPlanHash(plan)
			if _, err := service.ConfirmInspection(context.Background(), ConfirmInspectionInput{Plan: plan, PayloadSHA256: plan.PayloadSHA256}); !errors.Is(err, ErrConflict) {
				t.Fatalf("want conflict, got %v", err)
			}
		})
	}
	if contexts.ensures != 0 || len(store.confirmed) != 0 {
		t.Fatal("forged authority crossed durable boundary")
	}
}
