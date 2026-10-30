package bridgehttp

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
)

func TestProductReviewFrozenFocusAndIndependentEvidenceSurviveReopen(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	ctx := f.ctx
	area, err := f.plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: "Frozen Review evidence", ProblemStatement: "Separate repository authorization from observations."})
	if err != nil {
		t.Fatal(err)
	}
	// These module keys intentionally do not exist in the current catalog. The
	// Review's durable admission focus is the only display authority.
	focus := review.Focus{Kind: review.FocusFocused, Summary: "Original review focus", Paths: []string{"internal/original"}, Modules: []run.ReviewModuleSelection{{Key: "original-primary", Role: run.ReviewModulePrimary}, {Key: "original-support", Role: run.ReviewModuleSupporting}}}
	cases := []struct {
		name     string
		granted  bool
		outcomes []review.AccessOutcome
		want     consoleapi.ProductReviewObservedEvidence
	}{
		{name: "missing grant", want: consoleapi.ProductReviewObservedEvidence{AuthorizationStatus: "UNKNOWN", ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY"}},
		{name: "authorized without observation", granted: true, want: consoleapi.ProductReviewObservedEvidence{AuthorizationStatus: "GRANTED", ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY"}},
		{name: "failed and denied only", granted: true, outcomes: []review.AccessOutcome{review.AccessForbidden, review.AccessForbidden, review.AccessFailure}, want: consoleapi.ProductReviewObservedEvidence{AuthorizationStatus: "GRANTED", ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY", ForbiddenHostToolOperations: 2, FailedHostToolOperations: 1}},
		{name: "mixed observations", granted: true, outcomes: []review.AccessOutcome{review.AccessSuccess, review.AccessSuccess, review.AccessForbidden, review.AccessFailure, review.AccessFailure, review.AccessFailure}, want: consoleapi.ProductReviewObservedEvidence{AuthorizationStatus: "GRANTED", RepositoryInspectionObserved: true, ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY", SuccessfulHostToolOperations: 2, ForbiddenHostToolOperations: 1, FailedHostToolOperations: 3}},
	}
	wants := map[string]consoleapi.ProductReviewObservedEvidence{}
	for _, tc := range cases {
		owner := createPlanningTaskForArea(t, f, area.ID, tc.name)
		value, err := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: focus, Instruction: "Inspect only.", EngineeringGoal: tc.name}))
		if err != nil {
			t.Fatal(err)
		}
		operation, err := f.plane.Runs.CreateFocusedScopeReviewForReview(ctx, value.ID, value.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeFocused, InitialScope: run.ReviewScope{Kind: run.ReviewScopePaths, Paths: focus.Paths}, SelectedModules: focus.Modules})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.plane.Reviews.LinkRun(ctx, value.ID, operation.ID); err != nil {
			t.Fatal(err)
		}
		if tc.granted {
			if _, err := f.plane.ReviewGrants.Ensure(ctx, reviewgrant.Grant{ReviewID: value.ID, FeatureAreaID: area.ID, ProjectID: f.projectID, RepositoryID: f.repositoryID, Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}); err != nil {
				t.Fatal(err)
			}
		}
		for _, outcome := range tc.outcomes {
			if err := f.plane.Reviews.RecordAccess(ctx, value.ID, "repository_read", outcome, ""); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := f.plane.Runs.Fail(ctx, operation.ID, run.Failure{Kind: run.FailureInfrastructure, Stage: "test", Code: "TEST_COMPLETE", Message: "Frozen observations recorded."}); err != nil {
			t.Fatal(err)
		}
		wants[value.ID] = tc.want
	}
	// Reopen the durable database and obtain a new HTTP projection. A previous
	// response or in-memory Review value must not supply evidence or focus.
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(ctx, controlplane.Config{DataRoot: f.stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server, err := New(reopened, false)
	if err != nil {
		t.Fatal(err)
	}
	status, _, body := getRaw(t, server.Handler(), "/ui/api/project-workspace?project_id="+f.projectID)
	if status != http.StatusOK {
		t.Fatalf("workspace status=%d body=%s", status, body)
	}
	var workspace consoleapi.ProductProjectWorkspace
	if err := json.Unmarshal([]byte(body), &workspace); err != nil {
		t.Fatal(err)
	}
	if len(workspace.FeatureAreas) != 1 || len(workspace.FeatureAreas[0].Reviews) != len(wants) {
		t.Fatalf("review history missing: %s", body)
	}
	wantFocus := consoleapi.ReviewFocusSummary{Mode: "FOCUSED", Summary: focus.Summary, Paths: focus.Paths, PrimaryModules: []string{"original-primary"}, SupportingModules: []string{"original-support"}}
	for _, projected := range workspace.FeatureAreas[0].Reviews {
		want, ok := wants[projected.ID]
		if !ok || projected.ObservedEvidence != want {
			t.Errorf("%s evidence=%+v, want %+v", projected.ID, projected.ObservedEvidence, want)
		}
		if !reflect.DeepEqual(projected.Focus, wantFocus) {
			t.Errorf("%s frozen focus=%+v, want %+v", projected.ID, projected.Focus, wantFocus)
		}
	}
	if tasks, err := reopened.Tasks.List(ctx); err != nil || len(tasks) != 4 {
		t.Fatalf("Review projection fabricated Task ownership: %+v %v", tasks, err)
	}
}
