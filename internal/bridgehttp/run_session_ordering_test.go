package bridgehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Exercise the actual bounded Console route, not a re-sorted copy of the SQL
// result. Only Run IDs and clocks are injected: Review and Run ownership use
// the ordinary stores, and no backend, workspace or Turn is started.
func TestRunSessionPageChronologicalOrdering(t *testing.T) {
	f := newConsoleFixture(t)
	defer func() { _ = f.plane.Close() }()
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		id     string
		offset time.Duration
	}{
		{"run-z-whole", 0},
		{"run-y-tenth", 100 * time.Millisecond},
		{"run-x-prefix", 110 * time.Millisecond},
		{"run-w-nano-prefix", 123456780 * time.Nanosecond},
		{"run-v-nano-neighbor", 123456788 * time.Nanosecond},
		{"run-u-nano", 123456789 * time.Nanosecond},
		{"run-t-second", time.Second},
		{"run-a-equal-time", 100 * time.Millisecond},
		// Insert an earlier instant last: neither insertion nor ID order is time.
		{"run-b-previous-second", -time.Nanosecond},
	}
	var expected []run.Run
	for _, tc := range cases {
		area, err := f.plane.FeatureAreas.Create(f.ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: tc.id, ProblemStatement: "Read exact chronological Run history"})
		if err != nil {
			t.Fatal(err)
		}
		owner := createPlanningTaskForArea(t, f, area.ID, tc.id)
		rev, err := f.plane.Reviews.Create(f.ctx, f.codingReview(t, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: area.ID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: f.configID, BaseCommit: f.baseCommit, Focus: review.Focus{Kind: review.FocusRepository}, Instruction: "Inspect only.", EngineeringGoal: "Read exact chronological Run history"}))
		if err != nil {
			t.Fatal(err)
		}
		service := run.NewService(sqlite.NewRunStore(f.plane.DB), func() (string, error) { return tc.id, nil }, func() time.Time { return base.Add(tc.offset) })
		scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
		value, err := service.CreateFocusedScopeReviewForReview(f.ctx, rev.ID, rev.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
		if err != nil {
			t.Fatal(err)
		}
		if !value.CreatedAt.Equal(base.Add(tc.offset)) || value.TaskID != owner.ID || value.ReviewID != rev.ID {
			t.Fatalf("incorrect fixture identity/time: %+v", value)
		}
		expected = append(expected, value)
	}
	sort.Slice(expected, func(i, j int) bool {
		if expected[i].CreatedAt.Equal(expected[j].CreatedAt) {
			return expected[i].ID < expected[j].ID
		}
		return expected[i].CreatedAt.After(expected[j].CreatedAt)
	})
	other, err := f.plane.Projects.Create(f.ctx, "Unrelated empty project")
	if err != nil {
		t.Fatal(err)
	}
	read := func(t *testing.T, project, scope, status, area string, limit, offset int) consoleapi.Page[consoleapi.RunSummary] {
		t.Helper()
		query := url.Values{"project_id": {project}, "scope": {scope}, "limit": {fmt.Sprint(limit)}, "offset": {fmt.Sprint(offset)}}
		if status != "" {
			query.Set("status", status)
		}
		if area != "" {
			query.Set("feature_area_id", area)
		}
		request := httptest.NewRequest(http.MethodGet, "/v1/runs?"+query.Encode(), nil).WithContext(f.ctx)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("read status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var page consoleapi.Page[consoleapi.RunSummary]
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	assertPage := func(t *testing.T, scope, status, area string, limit, offset int, want []run.Run) {
		t.Helper()
		page := read(t, f.projectID, scope, status, area, limit, offset)
		var gotIDs, wantIDs []string
		for _, v := range page.Items {
			gotIDs = append(gotIDs, v.ID)
		}
		for i := offset; i < len(want) && i-offset < limit; i++ {
			wantIDs = append(wantIDs, want[i].ID)
		}
		if !reflect.DeepEqual(gotIDs, wantIDs) || page.Limit != limit || page.Offset != offset || page.HasMore != (offset+limit < len(want)) {
			t.Errorf("scope=%s status=%s area=%s offset=%d limit=%d got=%v hasMore=%v want=%v hasMore=%v", scope, status, area, offset, limit, gotIDs, page.HasMore, wantIDs, offset+limit < len(want))
		}
	}
	// Reserved Runs without a native session are real current-list entries.
	t.Run("current-reservations", func(t *testing.T) {
		for _, scope := range []string{"all", "current"} {
			for offset := 0; offset <= len(expected); offset++ {
				assertPage(t, scope, "", "", 1, offset, expected)
			}
		}
		assertPage(t, "history", "", "", 20, 0, nil)
	})
	for i := range expected {
		// Record a synthetic terminal result before starting any backend. This
		// is history setup, not a cancellation or recovery acceptance test.
		v, err := f.plane.Runs.Fail(f.ctx, expected[i].ID, run.Failure{Kind: run.FailureTask, Code: "READ_FIXTURE", Message: "No backend started in history read fixture"})
		if err != nil {
			t.Fatal(err)
		}
		expected[i] = v
	}
	check := func(t *testing.T) {
		t.Helper()
		before, err := sqlite.NewRunStore(f.plane.DB).List(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var eventsBefore, eventsAfter int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*) FROM events`).Scan(&eventsBefore); err != nil {
			t.Fatal(err)
		}
		for _, scope := range []string{"all", "history"} {
			for _, limit := range []int{1, 3, 20} {
				for offset := 0; offset <= len(expected); offset++ {
					assertPage(t, scope, "", "", limit, offset, expected)
				}
			}
		}
		assertPage(t, "history", string(run.StatusFailed), "", 3, 0, expected)
		assertPage(t, "current", "", "", 20, 0, nil)
		assertPage(t, "all", string(run.StatusSucceeded), "", 20, 0, nil)
		for _, v := range expected {
			assertPage(t, "history", "", v.FeatureAreaID, 1, 0, []run.Run{v})
		}
		if page := read(t, other.ID, "all", "", "", 20, 0); len(page.Items) != 0 || page.HasMore {
			t.Errorf("cross-project history leaked: %+v", page)
		}
		after, err := sqlite.NewRunStore(f.plane.DB).List(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*) FROM events`).Scan(&eventsAfter); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || eventsBefore != eventsAfter {
			t.Error("history read changed Run records or workflow events")
		}
	}
	t.Run("warm", check)
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	f.handler = withTestLoopbackTransport(server.Handler())
	t.Run("cold-reopen", check)
}
