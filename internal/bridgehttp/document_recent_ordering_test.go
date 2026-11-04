package bridgehttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// The Console lists at most MaxDocuments. Service-authored UTC timestamps must
// select the newest knowledge before that limit, without changing stored text
// or provenance. This fixture admits no engineering workflow or SQL mutations.
func TestProductDocumentsSelectNewestBeforeLimit(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "document-order-owner")
	root := filepath.Join(t.TempDir(), "state")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = plane.Close() }()
	var nextID string
	var nextTime time.Time
	plane.Documents, err = document.NewService(sqlite.NewDocumentStore(plane.DB), plane.AISessions, plane.FeatureAreas,
		func() (string, error) { return nextID, nil }, func() time.Time { return nextTime })
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	bind := func(projectID, requestID string) string {
		t.Helper()
		ref := aiprojectselection.ClientBindingReference{CallerScope: "document-order-owner"}
		initialized, err := plane.AIContext.InitializeSession(ctx, ref, requestID+"-init", "")
		if err != nil {
			t.Fatal(err)
		}
		bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectID, requestID+"-bind")
		if err != nil || bound.PreparationStatus != "COMPLETED" {
			t.Fatalf("bind: %+v err=%v", bound, err)
		}
		return initialized.AISessionID
	}
	create := func(id, session, area string, at time.Time) document.Document {
		t.Helper()
		nextID, nextTime = id, at
		value, err := facade.CreateDocument(ctx, document.CreateInput{AISessionID: session, FeatureAreaID: area, Title: "  Shared decision  ", Content: "  Authored snapshot " + id + "\n保留原文与空格  "})
		if err != nil {
			t.Fatal(err)
		}
		if value.CreatedByAISessionID != session || value.FeatureAreaID != area || !value.CreatedAt.Equal(at) || !value.UpdatedAt.Equal(at) {
			t.Fatalf("authorship/time drift: %+v", value)
		}
		return value
	}
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name         string
		older, newer time.Duration
		newestFirst  bool
	}{
		{name: "whole-second-before-fraction", older: 0, newer: 100 * time.Millisecond},
		{name: "fraction-prefix", older: 100 * time.Millisecond, newer: 110 * time.Millisecond},
		{name: "nanosecond-prefix", older: 123456780, newer: 123456789},
		{name: "nanosecond-same-width", older: 123456788, newer: 123456789},
		{name: "across-seconds", older: 999999999, newer: time.Second},
		{name: "equal-time-id-ascending", older: 100 * time.Millisecond, newer: 100 * time.Millisecond},
		{name: "insertion-order-is-not-time", older: 100 * time.Millisecond, newer: 110 * time.Millisecond, newestFirst: true},
	}
	type expectation struct {
		name, project string
		all           []document.Document
		latest        document.Document
	}
	var checks []expectation
	for i, tc := range cases {
		project, err := plane.Projects.Create(ctx, tc.name)
		if err != nil {
			t.Fatal(err)
		}
		firstSession := bind(project.ID, tc.name+"-first")
		secondSession := bind(project.ID, tc.name+"-second")
		area, err := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: secondSession, Title: tc.name, ProblemStatement: "Keep durable authored knowledge visible"})
		if err != nil {
			t.Fatal(err)
		}
		c := expectation{name: tc.name, project: project.ID}
		newest := func() {
			c.latest = create(fmt.Sprintf("z-new-%02d", i), secondSession, area.ID, base.Add(tc.newer))
			c.all = append(c.all, c.latest)
		}
		if tc.newestFirst {
			newest()
		}
		// One full page of older documents makes the ordering error drop the
		// newest record entirely, not merely place it lower on the page.
		for j := 0; j < document.MaxDocuments; j++ {
			c.all = append(c.all, create(fmt.Sprintf("a-old-%02d-%03d", i, j), firstSession, "", base.Add(tc.older)))
		}
		if !tc.newestFirst {
			newest()
		}
		sort.Slice(c.all, func(i, j int) bool {
			if c.all[i].CreatedAt.Equal(c.all[j].CreatedAt) {
				return c.all[i].ID < c.all[j].ID
			}
			return c.all[i].CreatedAt.After(c.all[j].CreatedAt)
		})
		checks = append(checks, c)
	}
	foreignProject, err := plane.Projects.Create(ctx, "Unrelated future document")
	if err != nil {
		t.Fatal(err)
	}
	foreign := create("foreign-future", bind(foreignProject.ID, "foreign"), "", base.Add(time.Hour))

	check := func(t *testing.T) {
		t.Helper()
		server, err := New(plane, false)
		if err != nil {
			t.Fatal(err)
		}
		handler := withTestLoopbackTransport(server.Handler())
		var before, after int
		if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			t.Run(c.name, func(t *testing.T) {
				for _, limit := range []int{1, 2, document.MaxDocuments} {
					listed, err := plane.Documents.ListByProject(ctx, c.project, limit)
					if err != nil || !reflect.DeepEqual(listed, c.all[:limit]) {
						var gotID string
						if len(listed) > 0 {
							gotID = listed[0].ID
						}
						t.Errorf("limit=%d selected first=%s count=%d; want first=%s count=%d err=%v", limit, gotID, len(listed), c.all[0].ID, limit, err)
					}
				}
				status, _, body := getRaw(t, handler, "/ui/api/project-workspace?project_id="+c.project)
				var workspace consoleapi.ProductProjectWorkspace
				if err := json.Unmarshal([]byte(body), &workspace); err != nil || status != http.StatusOK {
					t.Fatalf("public workspace status=%d err=%v body=%s", status, err, body)
				}
				if len(workspace.Documents) != document.MaxDocuments {
					t.Fatalf("public limit=%d want=%d", len(workspace.Documents), document.MaxDocuments)
				}
				for i, got := range workspace.Documents {
					want := c.all[i]
					if got.ID != want.ID || got.Title != strings.TrimSpace(want.Title) || got.ContentPreview != strings.TrimSpace(want.Content) || got.FeatureAreaID != want.FeatureAreaID || !got.CreatedAt.Equal(want.CreatedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
						t.Errorf("public listing[%d]=%s; want=%s exact preview/time/area; newest=%s visible=%t", i, got.ID, want.ID, c.latest.ID, strings.Contains(body, c.latest.ID))
						break
					}
				}
				// Every original body and its Session/Project provenance remains
				// exact, including the record beyond the bounded listing.
				for _, want := range c.all {
					got, err := plane.Documents.GetForProject(ctx, c.project, want.ID)
					if err != nil || got != want {
						t.Fatalf("immutable snapshot=%+v want=%+v err=%v", got, want, err)
					}
					var storedTime string
					if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT created_at FROM documents WHERE id=?`, want.ID).Scan(&storedTime); err != nil || storedTime != want.CreatedAt.Format(time.RFC3339Nano) {
						t.Fatalf("stored timestamp changed: %q err=%v", storedTime, err)
					}
				}
				status, _, body = getRaw(t, handler, "/ui/api/documents/"+c.latest.ID+"?project_id="+c.project)
				var detail consoleapi.ProductDocumentDetail
				if err := json.Unmarshal([]byte(body), &detail); err != nil || status != http.StatusOK || detail.ID != c.latest.ID || detail.Title != c.latest.Title || detail.Content != c.latest.Content || detail.TopicTitle != c.name {
					t.Errorf("exact public body=%+v status=%d err=%v", detail, status, err)
				}
				if _, err := plane.Documents.GetForProject(ctx, foreignProject.ID, c.latest.ID); !errors.Is(err, document.ErrNotFound) {
					t.Errorf("cross-project service read: %v", err)
				}
				status, _, _ = getRaw(t, handler, "/ui/api/documents/"+c.latest.ID+"?project_id="+foreignProject.ID)
				if status != http.StatusNotFound {
					t.Errorf("cross-project public read status=%d", status)
				}
			})
		}
		listed, err := plane.Documents.ListByProject(ctx, foreignProject.ID, document.MaxDocuments)
		if err != nil || len(listed) != 1 || listed[0] != foreign {
			t.Errorf("foreign project own listing=%+v err=%v", listed, err)
		}
		for _, limit := range []int{0, -1, document.MaxDocuments + 1} {
			if _, err := plane.Documents.ListByProject(ctx, checks[0].project, limit); !errors.Is(err, document.ErrInvalidArgument) {
				t.Errorf("invalid limit=%d err=%v", limit, err)
			}
		}
		if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT count(*) FROM events`).Scan(&after); err != nil || before != after {
			t.Errorf("reads changed events: before=%d after=%d err=%v", before, after, err)
		}
		var operations int
		if err := plane.DB.SQL().QueryRowContext(ctx, `SELECT (SELECT count(*) FROM tasks)+(SELECT count(*) FROM runs)+(SELECT count(*) FROM reviews)+(SELECT count(*) FROM execution_proposals)`).Scan(&operations); err != nil || operations != 0 {
			t.Fatalf("documents admitted engineering work: count=%d err=%v", operations, err)
		}
	}
	t.Run("warm", check)
	if err := plane.Close(); err != nil {
		t.Fatal(err)
	}
	plane, err = controlplane.Open(ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("cold-reopen", check)
}
