package consoleapi_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

func TestGetFeatureAreaPresentationMatchesFacadeAndRejectsForeignProject(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "console-presentation-fixture")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	project, err := plane.Projects.Create(ctx, "Console presentation")
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "console-presentation-fixture"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "console-presentation-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, project.ID, "console-presentation-bind"); err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.GetSession(ctx, initialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "console-presentation-feature", ProjectID: project.ID, CreatedByAISessionID: session.Session.ID, Title: "Console parity", ProblemStatement: "Read the same projection", Status: featurearea.StatusActive, CreatedAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := consoleapi.New(plane, facade)
	if err != nil {
		t.Fatal(err)
	}

	facadeProjection, err := facade.FeatureAreaPresentationForSession(ctx, session.Session.ID, area.ID)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := localoperator.NewIngress("console-presentation-test")
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(context.WithValue(ctx, http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 19091}), http.MethodGet, "http://127.0.0.1:19091/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.RemoteAddr, request.Host = "127.0.0.1:39091", "127.0.0.1:19091"
	operatorContext, err := ingress.AdmitHTTP(request)
	if err != nil {
		t.Fatal(err)
	}
	operatorContext, err = localoperator.BindProject(operatorContext, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	consoleProjection, err := reader.GetFeatureAreaPresentation(operatorContext, project.ID, session.Session.ID, area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if facadeProjection.Product.FeatureAreaID != consoleProjection.Product.FeatureAreaID || facadeProjection.Product.FeatureAreaName != consoleProjection.Product.FeatureAreaName || facadeProjection.Product.DomainStatus != consoleProjection.Product.DomainStatus || facadeProjection.Product.InternalStatus != consoleProjection.Product.InternalStatus || !reflect.DeepEqual(facadeProjection.Product.CurrentTask, consoleProjection.Product.CurrentTask) || !reflect.DeepEqual(facadeProjection.Product.LatestTask, consoleProjection.Product.LatestTask) || facadeProjection.Product.TaskPhase != consoleProjection.Product.TaskPhase || !reflect.DeepEqual(facadeProjection.Product.OperationActivity, consoleProjection.Product.OperationActivity) || facadeProjection.FeatureArea != consoleProjection.FeatureArea || facadeProjection.WorkflowStatus != consoleProjection.WorkflowStatus || facadeProjection.CurrentPhase != consoleProjection.CurrentPhase {
		t.Fatalf("Console product projection differs from facade:\nfacade=%+v\nconsole=%+v", facadeProjection.Product, consoleProjection.Product)
	}
	if got := presentation.RenderText(consoleProjection); got == "" {
		t.Fatal("Console product text is empty")
	}
	// Workspace selection is projected by the same control facade. Console
	// keeps the semantic Task selection and selected operation DTO intact.
	facadeWorkspace, err := facade.DevelopmentWorkspaceForSession(ctx, session.Session.ID, area.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	consoleWorkspace, err := reader.GetDevelopmentWorkspace(operatorContext, project.ID, session.Session.ID, area.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if consoleWorkspace.SelectedFeatureAreaID != facadeWorkspace.SelectedFeatureAreaID || consoleWorkspace.SelectedTaskID != facadeWorkspace.SelectedTaskID || !reflect.DeepEqual(consoleWorkspace.SelectedWork, facadeWorkspace.SelectedWork) {
		t.Fatalf("Console workspace selection differs from facade:\nfacade=%+v\nconsole=%+v", facadeWorkspace, consoleWorkspace)
	}

	foreignProject, err := plane.Projects.Create(ctx, "Foreign console project")
	if err != nil {
		t.Fatal(err)
	}
	foreignInitialized, err := plane.AIContext.InitializeSession(ctx, ref, "console-presentation-foreign-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AIContext.BindProject(ctx, ref, foreignInitialized.AISessionID, foreignProject.ID, "console-presentation-foreign-bind"); err != nil {
		t.Fatal(err)
	}
	foreignSession, err := plane.AIContext.GetSession(ctx, foreignInitialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.GetFeatureAreaPresentation(operatorContext, foreignProject.ID, foreignSession.Session.ID, area.ID)
	var apiErr *controlapi.Error
	if !errors.As(err, &apiErr) || apiErr.Code != controlapi.CodePermissionDenied {
		t.Fatalf("foreign project error=%v, want permission denied", err)
	}
}
