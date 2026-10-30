package document

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/featurearea"
)

type memoryStore struct{ values map[string]Document }

func (s *memoryStore) Create(_ context.Context, value Document) error {
	if s.values == nil {
		s.values = map[string]Document{}
	}
	if _, exists := s.values[value.ID]; exists {
		return ErrConflict
	}
	s.values[value.ID] = value
	return nil
}
func (s *memoryStore) Get(_ context.Context, id string) (Document, error) {
	value, ok := s.values[id]
	if !ok {
		return Document{}, ErrNotFound
	}
	return value, nil
}
func (s *memoryStore) ListByProject(_ context.Context, projectID string, limit int) ([]Document, error) {
	values := make([]Document, 0)
	for _, value := range s.values {
		if value.ProjectID == projectID && len(values) < limit {
			values = append(values, value)
		}
	}
	return values, nil
}

type sessionLookup map[string]aisession.Session

func (s sessionLookup) Get(_ context.Context, id string) (aisession.Session, error) {
	value, ok := s[id]
	if !ok {
		return aisession.Session{}, errors.New("not found")
	}
	return value, nil
}

type featureAreaLookup map[string]featurearea.FeatureArea

func (s featureAreaLookup) GetForSession(_ context.Context, sessionID, featureAreaID string) (featurearea.FeatureArea, error) {
	value, ok := s[sessionID+"/"+featureAreaID]
	if !ok {
		return featurearea.FeatureArea{}, errors.New("not found")
	}
	return value, nil
}

func TestCreatePreservesExactAuthoredTextWithoutStartingWorkflow(t *testing.T) {
	store := &memoryStore{}
	createdAt := time.Date(2026, 9, 5, 1, 2, 3, 0, time.UTC)
	service, err := NewService(store, sessionLookup{"session-a": {ID: "session-a", ProjectID: "project-a"}}, featureAreaLookup{"session-a/featureArea-a": {ID: "featureArea-a", ProjectID: "project-a"}}, func() (string, error) { return "opaque", nil }, func() time.Time { return createdAt })
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &logs}))
	title := "  Decision record  "
	content := "Line one.\n\n  Keep exact spacing.  "
	value, err := service.Create(ctx, CreateInput{AISessionID: "session-a", FeatureAreaID: "featureArea-a", Title: title, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	if value.ProjectID != "project-a" || value.FeatureAreaID != "featureArea-a" || value.Title != title || value.Content != content || value.CreatedAt != createdAt || value.UpdatedAt != createdAt {
		t.Fatalf("created Document changed authored data: %+v", value)
	}
	if strings.Contains(logs.String(), title) || strings.Contains(logs.String(), content) {
		t.Fatalf("Document body leaked to logs: %s", logs.String())
	}
	if got, err := service.GetForProject(ctx, "project-a", value.ID); err != nil || got.Content != content {
		t.Fatalf("project read=%+v err=%v", got, err)
	}
	if _, err := service.GetForProject(ctx, "project-b", value.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-Project read error=%v", err)
	}
}

func TestCreateRejectsCrossProjectTopicAndInvalidText(t *testing.T) {
	service, err := NewService(&memoryStore{}, sessionLookup{"session-a": {ID: "session-a", ProjectID: "project-a"}}, featureAreaLookup{"session-a/featureArea-b": {ID: "featureArea-b", ProjectID: "project-b"}}, func() (string, error) { return "opaque", nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), CreateInput{AISessionID: "session-a", FeatureAreaID: "featureArea-b", Title: "title", Content: "content"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-Project Topic error=%v", err)
	}
	for _, input := range []CreateInput{
		{AISessionID: "session-a", Title: "", Content: "content"},
		{AISessionID: "session-a", Title: "title", Content: " \n"},
		{AISessionID: "session-a", Title: "title", Content: "bad\x00content"},
		{AISessionID: "session-a", Title: "title", Content: strings.Repeat("x", MaxContentBytes+1)},
	} {
		if _, err := service.Create(context.Background(), input); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("invalid input accepted: %#v err=%v", input, err)
		}
	}
}

func TestListByProjectIsBounded(t *testing.T) {
	service, err := NewService(&memoryStore{values: map[string]Document{"document-one": {ID: "document-one", ProjectID: "project-a", Title: "one", Content: "one"}}}, sessionLookup{"session-a": {ID: "session-a", ProjectID: "project-a"}}, featureAreaLookup{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if values, err := service.ListByProject(context.Background(), "project-a", 1); err != nil || len(values) != 1 {
		t.Fatalf("bounded list=%+v err=%v", values, err)
	}
	for _, limit := range []int{0, MaxDocuments + 1} {
		if _, err := service.ListByProject(context.Background(), "project-a", limit); !errors.Is(err, ErrInvalidArgument) {
			t.Fatalf("limit %d error=%v", limit, err)
		}
	}
}
