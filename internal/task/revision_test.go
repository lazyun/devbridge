package task

import (
	"context"
	"testing"
	"time"
)

func TestDefinitionRevisionCanonicalPayloadAndHistoricalSelection(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	current := Task{ID: "task", ProjectID: "project", FeatureAreaID: "feature", Model: "gpt-5.6-luna", ReasoningEffort: "low", Type: TypeDevelopment, WorkSpec: WorkSpec{Goal: "intent", CodexInstruction: "exact\nbytes"}, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)}
	if err := store.Create(ctx, current); err != nil {
		t.Fatal(err)
	}
	initial, err := store.Get(ctx, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.CurrentDefinitionRevisionID == "" {
		t.Fatal("Task has no initial revision")
	}
	if _, err := store.Update(ctx, current.ID, func(value *Task) error { value.WorkKind = WorkKindBugFix; return nil }); err != nil {
		t.Fatal(err)
	}
	metadata, _ := store.Get(ctx, current.ID)
	if metadata.CurrentDefinitionRevisionID != initial.CurrentDefinitionRevisionID {
		t.Fatal("organization metadata changed definition revision")
	}
	if _, err := store.Update(ctx, current.ID, func(value *Task) error { value.Model = "gpt-5.6-sol"; return nil }); err != nil {
		t.Fatal(err)
	}
	later, _ := store.Get(ctx, current.ID)
	if later.CurrentDefinitionRevisionID == initial.CurrentDefinitionRevisionID {
		t.Fatal("model selection did not produce revision")
	}
	frozen, err := store.GetDefinitionRevision(ctx, initial.CurrentDefinitionRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Definition.ApplyTo(later).Model != "gpt-5.6-luna" {
		t.Fatal("historical definition read current selection")
	}
	raw, hash, err := EncodeDefinition(frozen.Definition)
	if err != nil {
		t.Fatal(err)
	}
	cloned := DefinitionFromTask(initial, nil)
	rawAgain, hashAgain, err := EncodeDefinition(cloned)
	if err != nil || raw != rawAgain || hash != hashAgain {
		t.Fatal("same semantic definition was not canonical")
	}
	if _, err := DecodeDefinition(raw+" ", hash); err == nil {
		t.Fatal("payload hash corruption accepted")
	}
}

func TestTaskServiceRejectsMissingFeatureAreaAtCreationBoundary(t *testing.T) {
	store := NewMemoryStore()
	service := NewService(store, nil, nil, nil, nil, nil)
	if _, err := service.Create(context.Background(), CreateInput{ProjectID: "project", ModuleID: "module", RepositoryID: "repo", CodexConfigID: "config"}); err == nil {
		t.Fatal("FeatureArea-less Task accepted")
	}
	values, _ := store.List(context.Background())
	if len(values) != 0 {
		t.Fatal("rejected Task persisted")
	}
}
