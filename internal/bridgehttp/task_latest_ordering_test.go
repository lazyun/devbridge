package bridgehttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Exercise the public read with real Core stores. The only injected facts are
// Task IDs and the service clock; every Task is created through CreateIntent
// and terminalized through ordinary pre-work cancellation. No Run is admitted.
func TestWorkflowGuidanceLatestTaskChronologicalOrdering(t *testing.T) {
	f := newConsoleFixtureWithBaseline(t, false)
	defer func() { _ = f.plane.Close() }()
	base := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	type scenario struct {
		name          string
		first, second time.Duration
		wantFirst     bool
		active        bool
	}
	cases := []scenario{
		{name: "whole-second-before-fraction", first: 0, second: 100 * time.Millisecond},
		{name: "fraction-prefix", first: 100 * time.Millisecond, second: 110 * time.Millisecond},
		{name: "nanosecond-prefix", first: 123456780, second: 123456789},
		{name: "nanosecond-same-width", first: 123456788, second: 123456789},
		{name: "across-seconds", first: 999999999, second: time.Second},
		{name: "equal-time-id-desc", first: 100 * time.Millisecond, second: 100 * time.Millisecond, wantFirst: true},
		{name: "insertion-order-is-not-time", first: 110 * time.Millisecond, second: 100 * time.Millisecond, wantFirst: true},
		{name: "active-owner-precedes-newer-terminal", first: time.Second, second: 0, active: true},
	}
	type expectation struct {
		name, area          string
		first, second, want task.Task
	}
	var checks []expectation
	create := func(id, session, project, area, mod string, at time.Time, cancel bool) task.Task {
		t.Helper()
		svc := task.NewService(sqlite.NewTaskStore(f.plane.DB), nil, f.plane.Modules, nil,
			func() (string, error) { return id, nil }, func() time.Time { return at })
		value, err := svc.Create(f.ctx, task.CreateInput{AISessionID: session, RequestID: "create-" + id, ProjectID: project, FeatureAreaID: area, ModuleID: mod, Goal: "Intent for " + id})
		if err != nil {
			t.Fatal(err)
		}
		if value.CurrentDefinitionRevisionID == "" || !value.CreatedAt.Equal(at) {
			t.Fatalf("created Task lacks exact definition/time: %+v", value)
		}
		if cancel {
			receipt, dispatch, err := sqlite.NewExecutionCancellationStore(f.plane.DB).Begin(f.ctx,
				executioncancel.Input{AISessionID: session, ProjectID: project, OwnerType: "TASK", OwnerID: value.ID, RequestID: "cancel-" + id}, false)
			if err != nil || dispatch || receipt.State != "CANCELED" || receipt.RunID != "" || receipt.BackendDisposition != "NO_BACKEND" {
				t.Fatalf("pre-work cancellation: %+v dispatch=%t err=%v", receipt, dispatch, err)
			}
			value, err = f.plane.Tasks.Get(f.ctx, value.ID)
			if err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	for i, tc := range cases {
		area, err := f.plane.FeatureAreas.Create(f.ctx, featurearea.CreateInput{AISessionID: f.sessionID, Title: tc.name, ProblemStatement: "Keep exact latest task history"})
		if err != nil {
			t.Fatal(err)
		}
		first := create(fmt.Sprintf("z-first-%02d", i), f.sessionID, f.projectID, area.ID, f.moduleID, base.Add(tc.first), true)
		second := create(fmt.Sprintf("a-second-%02d", i), f.sessionID, f.projectID, area.ID, f.moduleID, base.Add(tc.second), !tc.active)
		want := second
		if tc.wantFirst {
			want = first
		}
		if first.CurrentDefinitionRevisionID == second.CurrentDefinitionRevisionID {
			t.Fatal("distinct Tasks reused definition")
		}
		checks = append(checks, expectation{tc.name, area.ID, first, second, want})
	}
	// A newer Task in another Project must never become a candidate for any
	// selected area. Its real bound Session also tests the public scope fence.
	otherProject, err := f.plane.Projects.Create(f.ctx, "Unrelated latest Task")
	if err != nil {
		t.Fatal(err)
	}
	otherSession := f.bindSession(t, otherProject.ID, "latest-other")
	otherModule, err := f.plane.Modules.Create(f.ctx, module.CreateInput{ProjectID: otherProject.ID, Key: "other", Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	otherArea, err := f.plane.FeatureAreas.Create(f.ctx, featurearea.CreateInput{AISessionID: otherSession.ID, Title: "Other area", ProblemStatement: "Separate project"})
	if err != nil {
		t.Fatal(err)
	}
	other := create("other-project-task", otherSession.ID, otherProject.ID, otherArea.ID, otherModule.ID, base.Add(time.Hour), true)

	read := func(t *testing.T, session, area string) (wp.Guidance, int) {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/v1/workflow-guidance?ai_session_id="+session+"&feature_area_id="+area, nil).WithContext(f.ctx)
		setTestLoopbackTransport(request)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, request)
		var g wp.Guidance
		if err := json.Unmarshal(recorder.Body.Bytes(), &g); err != nil {
			t.Fatal(err)
		}
		return g, recorder.Code
	}
	check := func(t *testing.T) {
		t.Helper()
		before, err := f.plane.Tasks.List(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var eventBefore, eventAfter int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*) FROM events`).Scan(&eventBefore); err != nil {
			t.Fatal(err)
		}
		facade, err := controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			t.Run(c.name, func(t *testing.T) {
				g, status := read(t, f.sessionID, c.area)
				if status != http.StatusOK {
					t.Fatalf("HTTP status=%d guidance=%+v", status, g)
				}
				if g.TaskID != c.want.ID || g.LatestTask == nil || g.LatestTask.ID != c.want.ID || g.TaskDefinitionRevisionID != c.want.CurrentDefinitionRevisionID || g.TaskPhase != c.want.Phase {
					t.Errorf("selected Task=%s definition=%s latest=%+v; want Task=%s definition=%s; first=%s second=%s", g.TaskID, g.TaskDefinitionRevisionID, g.LatestTask, c.want.ID, c.want.CurrentDefinitionRevisionID, c.first.CreatedAt.Format(time.RFC3339Nano), c.second.CreatedAt.Format(time.RFC3339Nano))
				}
				if c.want.Status == task.StatusActive && (g.CurrentTask == nil || g.CurrentTask.ID != c.want.ID) {
					t.Errorf("lost active owner: %+v", g.CurrentTask)
				}
				if c.want.Status != task.StatusActive && g.CurrentTask != nil {
					t.Errorf("terminal Task became current: %+v", g.CurrentTask)
				}
				// The existing exact Task route must still read the requested
				// history, irrespective of the area's latest selection.
				exact, err := f.plane.WorkflowProgression.ReadTask(f.ctx, f.projectID, c.area, c.first.ID)
				if err != nil || exact.TaskID != c.first.ID || exact.TaskDefinitionRevisionID != c.first.CurrentDefinitionRevisionID {
					t.Errorf("exact Task read=%+v err=%v", exact, err)
				}
				current, err := facade.CurrentWorkflowForSession(f.ctx, f.sessionID, c.area)
				if err != nil || current.LatestTask == nil || current.LatestTask.ID != c.want.ID || current.LatestTask.CurrentDefinitionRevisionID != c.want.CurrentDefinitionRevisionID {
					t.Errorf("production current-workflow chose wrong Task or definition: want=%s definition=%s err=%v", c.want.ID, c.want.CurrentDefinitionRevisionID, err)
				}
			})
		}
		g, status := read(t, otherSession.ID, otherArea.ID)
		if status != http.StatusOK || g.TaskID != other.ID {
			t.Errorf("other Project own selection: status=%d Task=%s", status, g.TaskID)
		}
		g, status = read(t, otherSession.ID, checks[0].area)
		if status == http.StatusOK || g.TaskID != "" || g.LatestTask != nil {
			t.Errorf("cross-project area leaked: status=%d guidance=%+v", status, g)
		}
		after, err := f.plane.Tasks.List(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT count(*) FROM events`).Scan(&eventAfter); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || eventBefore != eventAfter {
			t.Error("latest/exact reads changed Task state or events")
		}
		var operations int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT (SELECT count(*) FROM runs)+(SELECT count(*) FROM reviews)+(SELECT count(*) FROM execution_proposals)`).Scan(&operations); err != nil || operations != 0 {
			t.Fatalf("unexpected operation admitted: %d err=%v", operations, err)
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
