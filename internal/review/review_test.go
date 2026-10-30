package review

import (
	"context"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"strings"
	"testing"
	"time"
)

type serviceStore struct{ created Review }

func (s *serviceStore) Create(_ context.Context, v Review) error                  { s.created = v; return nil }
func (*serviceStore) Get(context.Context, string) (Review, error)                 { return Review{}, ErrNotFound }
func (*serviceStore) ListByFeatureArea(context.Context, string) ([]Review, error) { return nil, nil }
func (*serviceStore) LinkRun(context.Context, string, string) error               { return nil }
func (*serviceStore) RecordPrompt(context.Context, string, string, time.Time) (Review, error) {
	return Review{}, nil
}
func (*serviceStore) RecordAccess(context.Context, AccessEvidence) error { return nil }
func (*serviceStore) SummarizeEvidence(context.Context, string) (EvidenceSummary, error) {
	return EvidenceSummary{}, nil
}
func (*serviceStore) RecordStartFailure(context.Context, string, StartFailure) error { return nil }
func (*serviceStore) ClaimContinuation(context.Context, string, time.Time) (bool, error) {
	return true, nil
}
func TestServiceCreatesOpaqueReviewID(t *testing.T) {
	store := &serviceStore{}
	at := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	svc, err := NewService(store, func() (string, error) { return "opaque", nil }, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	v, err := svc.Create(context.Background(), Review{ReviewDepth: "DEEP", TaskID: "semantic-task", ProjectID: "project", FeatureAreaID: "featureArea", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "goal", Instruction: "instruction", Model: "gpt-5.6-terra", ReasoningEffort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "review_opaque" || !v.CreatedAt.Equal(at) || store.created.ID != v.ID {
		t.Fatalf("review=%+v stored=%+v", v, store.created)
	}
}

func TestSelectionFreezesOnReviewAndRejectsInvalidValues(t *testing.T) {
	store := &serviceStore{}
	svc, err := NewService(store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := Review{ReviewDepth: "DEEP", TaskID: "semantic-task", ProjectID: "project", FeatureAreaID: "featureArea", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "goal", Instruction: "instruction", Model: "gpt-6-codex-preview", ReasoningEffort: "high"}
	created, err := svc.Create(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if created.Model != "gpt-6-codex-preview" || created.ReasoningEffort != "high" || store.created.Model != "gpt-6-codex-preview" || store.created.ReasoningEffort != "high" {
		t.Fatalf("selection not frozen: %+v", created)
	}
	for _, invalid := range []Review{
		{ProjectID: "p", FeatureAreaID: "t", CreatedByAISessionID: "s", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "g", Instruction: "i", Model: "bad model", ReasoningEffort: "high"},
		{ProjectID: "p", FeatureAreaID: "t", CreatedByAISessionID: "s", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "g", Instruction: "i", Model: "gpt-5.6-terra", ReasoningEffort: "ultra"},
	} {
		if _, err := svc.Create(context.Background(), invalid); err == nil {
			t.Fatalf("invalid selection accepted: %+v", invalid)
		}
	}
}

func TestReviewEmptyInstructionRejectsBeforePersistence(t *testing.T) {
	for _, instruction := range []string{"", " \n\t"} {
		store := &serviceStore{}
		svc, err := NewService(store, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = svc.Create(context.Background(), Review{ReviewDepth: "DEEP", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "must not become prompt", Instruction: instruction})
		if err != ErrInvalidArgument || store.created.ID != "" {
			t.Fatalf("empty Review persisted: %+v %v", store.created, err)
		}
	}
}
