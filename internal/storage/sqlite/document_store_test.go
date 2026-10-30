package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/document"
)

func TestDocumentStorePersistsExactProjectAndTopicDocumentAcrossReconnect(t *testing.T) {
	root := t.TempDir()
	db := openTestDB(t, root)
	f, top, session := reviewFixture(t, db, "document")
	value := document.Document{ID: "document_durable", ProjectID: f.project.ID, FeatureAreaID: top.ID, CreatedByAISessionID: session.ID, Title: "  Exact title  ", Content: "Line one.\n\n  Exact content.  ", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewDocumentStore(db).Create(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openTestDB(t, root)
	defer db.Close()
	got, err := NewDocumentStore(db).Get(context.Background(), value.ID)
	if err != nil || got != value {
		t.Fatalf("reopened Document=%+v err=%v", got, err)
	}
	listed, err := NewDocumentStore(db).ListByProject(context.Background(), f.project.ID, 100)
	if err != nil || len(listed) != 1 || listed[0] != value {
		t.Fatalf("listed Documents=%+v err=%v", listed, err)
	}
	var payload string
	if err := db.sql.QueryRow(`SELECT payload_json FROM events WHERE entity_type='document' AND entity_id=?`, value.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, value.Title) || strings.Contains(payload, value.Content) {
		t.Fatalf("Document text leaked to durable event: %s", payload)
	}
}
