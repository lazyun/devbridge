package project

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceCreateGetList(t *testing.T) {
	now := time.Date(2026, 9, 2, 1, 2, 3, 4, time.FixedZone("test", 3600))
	service := NewService(NewMemoryStore(), func() (string, error) { return "project-1", nil }, func() time.Time { return now })
	created, err := service.Create(context.Background(), "my_app")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "project-1" || created.Name != "my_app" || created.CreatedAt.Location() != time.UTC {
		t.Fatalf("project=%+v", created)
	}
	got, err := service.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	values, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != created || len(values) != 1 || values[0] != created {
		t.Fatalf("got=%+v list=%+v", got, values)
	}
	_, err = service.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
