package controlapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/document"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// These fixtures use public Core services only. They do not insert database rows,
// create workflow attempts, attach workspaces, or run any execution/recovery path.
type documentProvenanceFixture struct {
	plane                *controlplane.ControlPlane
	facade               *controlapi.Facade
	ctx                  context.Context
	projectID, sessionID string
}

func newDocumentProvenanceFixture(t *testing.T) documentProvenanceFixture {
	t.Helper()
	ctx := aisession.WithCallerScope(context.Background(), "document-owner")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	project, err := plane.Projects.Create(ctx, "Document provenance")
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "document-owner"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "document-owner-init", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, project.ID, "document-owner-bind")
	if err != nil {
		t.Fatal(err)
	}
	if bound.PreparationStatus != "COMPLETED" {
		t.Fatalf("binding preparation: %+v", bound)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	return documentProvenanceFixture{plane, facade, ctx, project.ID, initialized.AISessionID}
}

func TestDocumentCreateRejectsForeignCallerProvenance(t *testing.T) {
	f := newDocumentProvenanceFixture(t)
	original, err := f.facade.CreateDocument(f.ctx, document.CreateInput{AISessionID: f.sessionID, Title: "Decision", Content: "Owner-authored immutable original"})
	if err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "DevBridge", ApplicationVersion: "document-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	foreign := aisession.WithCallerScope(context.Background(), "document-other-caller")
	ref := aiprojectselection.ClientBindingReference{CallerScope: "document-other-caller"}
	initialized, err := f.plane.AIContext.InitializeSession(foreign, ref, "document-foreign-init", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.AIContext.BindProject(foreign, ref, initialized.AISessionID, f.projectID, "document-foreign-bind"); err != nil {
		t.Fatal(err)
	}
	// Sharing the same Project does not grant authority to attribute text to the
	// other caller's exact Session. The ordinary read boundary proves the scope.
	if _, err := f.facade.GetProjectContextForSession(foreign, f.sessionID); err == nil {
		t.Fatal("foreign caller control unexpectedly authorized")
	}
	if _, err := f.facade.GetProjectContextForSession(context.Background(), f.sessionID); err == nil {
		t.Fatal("missing-scope read control unexpectedly authorized")
	}
	for _, test := range []struct {
		name string
		ctx  context.Context
	}{
		{"different_caller_same_project", foreign},
		{"missing_caller_scope", context.Background()},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := f.plane.Documents.ListByProject(f.ctx, f.projectID, document.MaxDocuments)
			if err != nil {
				t.Fatal(err)
			}
			got, err := mcpserver.DocumentCreateCore(test.ctx, service, mcpserver.ExternalDocumentCreateInput{AISessionID: f.sessionID, Title: "Decision", Content: "Unowned replacement narrative"})
			if err == nil {
				stored, readErr := f.plane.Documents.GetForProject(f.ctx, f.projectID, got.ID)
				t.Errorf("unowned document_create admitted durable owner provenance: id=%s project=%s storedSession=%s readErr=%v", got.ID, got.ProjectID, stored.CreatedByAISessionID, readErr)
			}
			after, listErr := f.plane.Documents.ListByProject(f.ctx, f.projectID, document.MaxDocuments)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(after) != len(before) {
				t.Errorf("rejected call changed durable documents: before=%d after=%d", len(before), len(after))
			}
		})
	}
	stored, err := f.plane.Documents.GetForProject(f.ctx, f.projectID, original.ID)
	if err != nil || stored != original {
		t.Fatalf("original snapshot changed: got=%+v err=%v", stored, err)
	}
	own, err := mcpserver.DocumentCreateCore(foreign, service, mcpserver.ExternalDocumentCreateInput{AISessionID: initialized.AISessionID, Title: "Own authorship", Content: "Other caller using its own Session"})
	if err != nil {
		t.Fatalf("same-Project own-session control: %v", err)
	}
	ownStored, err := f.plane.Documents.GetForProject(f.ctx, f.projectID, own.ID)
	if err != nil || ownStored.CreatedByAISessionID != initialized.AISessionID {
		t.Fatalf("own provenance: %+v err=%v", ownStored, err)
	}
}

func TestDocumentConcurrentCreatesPreserveAuthoredSnapshots(t *testing.T) {
	f := newDocumentProvenanceFixture(t)
	const writers = 12
	start := make(chan struct{})
	type result struct {
		index int
		value document.Document
		err   error
	}
	results := make(chan result, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			value, err := f.facade.CreateDocument(f.ctx, document.CreateInput{AISessionID: f.sessionID, Title: "  Shared title  ", Content: fmt.Sprintf("Snapshot %02d\n\n 保留原文与空格  ", index)})
			results <- result{index, value, err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	seen := map[string]document.Document{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("writer %d: %v", result.index, result.err)
		}
		value := result.value
		if _, duplicate := seen[value.ID]; duplicate {
			t.Fatalf("duplicate document id %s", value.ID)
		}
		seen[value.ID] = value
		if value.ProjectID != f.projectID || value.CreatedByAISessionID != f.sessionID || value.Title != "  Shared title  " || value.Content != fmt.Sprintf("Snapshot %02d\n\n 保留原文与空格  ", result.index) || !value.CreatedAt.Equal(value.UpdatedAt) {
			t.Fatalf("snapshot/provenance drift: %+v", value)
		}
	}
	listed, err := f.plane.Documents.ListByProject(f.ctx, f.projectID, document.MaxDocuments)
	if err != nil || len(listed) != writers {
		t.Fatalf("list count=%d err=%v", len(listed), err)
	}
	for _, value := range listed {
		if want, ok := seen[value.ID]; !ok || value != want {
			t.Fatalf("concurrent list drift: %+v", value)
		}
		stored, err := f.plane.Documents.GetForProject(f.ctx, f.projectID, value.ID)
		if err != nil || stored != value {
			t.Fatalf("read drift: %+v err=%v", stored, err)
		}
	}
	otherProject, err := f.plane.Projects.Create(f.ctx, "Other document Project")
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "document-owner"}
	if _, err := f.plane.AIContext.BindProject(f.ctx, ref, f.sessionID, otherProject.ID, "document-rebind-attempt"); err == nil {
		t.Fatal("immutable session Project unexpectedly rebound")
	}
	for id, original := range seen {
		stored, err := f.plane.Documents.GetForProject(f.ctx, f.projectID, id)
		if err != nil || stored != original {
			t.Fatalf("rebind attempt altered snapshot: %+v err=%v", stored, err)
		}
		if _, err := f.plane.Documents.GetForProject(f.ctx, otherProject.ID, id); !errors.Is(err, document.ErrNotFound) {
			t.Fatalf("cross-Project document read: %v", err)
		}
	}
	post, err := f.facade.CreateDocument(f.ctx, document.CreateInput{AISessionID: f.sessionID, Title: "After rejected rebind", Content: "Still original Project"})
	if err != nil || post.ProjectID != f.projectID {
		t.Fatalf("post-rebind write drift: %+v err=%v", post, err)
	}
	tasks, err := f.plane.Tasks.List(f.ctx)
	if err != nil || len(tasks) != 0 {
		t.Fatalf("documents started workflow: tasks=%d err=%v", len(tasks), err)
	}
}

// The supported transport already installs caller admission. This is a normal
// wire compatibility control, not evidence of an unauthenticated wire exploit.
func TestDocumentNormalLocalMCPPreservesCallerProvenance(t *testing.T) {
	f := newDocumentProvenanceFixture(t)
	localCtx := aisession.WithCallerScope(context.Background(), "data-instance-local")
	ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
	initialized, err := f.plane.AIContext.InitializeSession(localCtx, ref, "document-local-init", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.AIContext.BindProject(localCtx, ref, initialized.AISessionID, f.projectID, "document-local-bind"); err != nil {
		t.Fatal(err)
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "DevBridge", ApplicationVersion: "document-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(adapter.Handler())
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "document-provenance-control", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + localmcp.MCPPath, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	const content = "  Wire-authored durable knowledge\n保留原文  "
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "document_create", Arguments: map[string]any{"aiSessionId": initialized.AISessionID, "title": "Wire control", "content": content}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("normal document_create: result=%+v err=%v", result, err)
	}
	before, err := f.plane.Documents.ListByProject(f.ctx, f.projectID, document.MaxDocuments)
	if err != nil || len(before) != 1 || before[0].CreatedByAISessionID != initialized.AISessionID || before[0].Content != content {
		t.Fatalf("wire provenance/snapshot: %+v err=%v", before, err)
	}
	result, err = session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "document_create", Arguments: map[string]any{"aiSessionId": f.sessionID, "title": "Unowned wire attempt", "content": "Must not be persisted"}})
	if err == nil && (result == nil || !result.IsError) {
		t.Fatalf("wire foreign Session accepted: %+v", result)
	}
	after, err := f.plane.Documents.ListByProject(f.ctx, f.projectID, document.MaxDocuments)
	if err != nil || len(after) != 1 || after[0] != before[0] {
		t.Fatalf("wire denial changed durable documents: %+v err=%v", after, err)
	}
}
