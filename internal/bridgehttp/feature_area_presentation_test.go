package bridgehttp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestFeatureAreaPresentationHTTPReadAndTextParity(t *testing.T) {
	ctx := context.Background()
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	project, err := plane.Projects.Create(ctx, "Presentation HTTP")
	if err != nil {
		t.Fatal(err)
	}
	callerScope := "bridgehttp-feature-area-presentation"
	ctx = aisession.WithCallerScope(ctx, callerScope)
	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "presentation-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, project.ID, "presentation-bind")
	if err != nil {
		t.Fatal(err)
	}
	session, err := plane.AISessions.Get(ctx, bound.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "http-presentation-area", ProjectID: project.ID, CreatedByAISessionID: session.ID, Title: "Persistent area", ProblemStatement: "Pure authoritative read", Status: featurearea.StatusActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := localoperator.NewIngress(plane.DataPaths.Root)
	if err != nil {
		t.Fatal(err)
	}
	operatorRequest := httptest.NewRequest(http.MethodGet, "/ui/api/featureAreas/"+area.ID+"/presentation", nil)
	setTestLoopbackTransport(operatorRequest)
	operatorCtx, err := ingress.AdmitHTTP(operatorRequest)
	if err != nil {
		t.Fatal(err)
	}
	operatorCtx, err = localoperator.BindProject(operatorCtx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := facade.FeatureAreaPresentationForOperator(operatorCtx, project.ID, area.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/api/featureAreas/" + area.ID + "/presentation?project_id=" + project.ID + "&ai_session_id=" + session.ID
	status, _, body := getRaw(t, server.Handler(), path)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var got workflowpresentation.Projection
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP changed projection: %+v", got)
	}
	status, headers, body := getRaw(t, server.Handler(), path+"&format=text")
	if status != http.StatusOK || headers.Get("Cache-Control") != "no-store" || string(body) != workflowpresentation.RenderText(want) {
		t.Fatalf("text mismatch: status=%d body=%s", status, body)
	}

	workspacePath := "/ui/api/development-workspace?project_id=" + project.ID + "&ai_session_id=" + session.ID
	status, _, body = getRaw(t, server.Handler(), workspacePath)
	var workspace workflowpresentation.WorkspaceProjection
	if status != http.StatusOK || json.Unmarshal([]byte(body), &workspace) != nil || len(workspace.FeatureAreas) != 1 || workspace.SelectedFeatureAreaID != area.ID || workspace.PresentationID != workflowpresentation.WorkspaceIdentity("", project.ID) {
		t.Fatalf("workspace parity: %d %s", status, body)
	}
	if workspace.FeatureAreas[0].Status != "ACTIVE" || workspace.FeatureAreas[0].Availability != "AVAILABLE" || workspace.RefreshedAt.IsZero() {
		t.Fatalf("workspace facts: %+v", workspace)
	}
	status, headers, body = getRaw(t, server.Handler(), workspacePath+"&format=text")
	if status != http.StatusOK || headers.Get("Cache-Control") != "no-store" || !strings.Contains(body, "Feature Areas:\n- Persistent area · 活跃") || !strings.Contains(body, "Current Action:") {
		t.Fatalf("workspace text: %d %s", status, body)
	}
	for _, query := range []string{"&format=html", "&request_id=must-not-write"} {
		status, _, _ := getRaw(t, server.Handler(), path+query)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid query accepted: %s status=%d", query, status)
		}
	}
	request := httptest.NewRequest(http.MethodPost, path, nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("presentation accepted POST")
	}
}
