package workflowpresentation

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceSelectionStableAndScoped(t *testing.T) {
	areas := []FeatureAreaNavigationItem{
		{ID: "recent", Status: "ACTIVE", Availability: "AVAILABLE", UpdatedAt: time.Unix(30, 0)},
		{ID: "action", Status: "ACTIVE", Availability: "AVAILABLE", CurrentAction: wp.Action{Code: "RESOLVE_DECISION", Required: true}},
		{ID: "running", Status: "ACTIVE", Availability: "AVAILABLE", CurrentTaskID: "run", CurrentTaskStatus: "RUNNING"},
	}
	tasks := []TaskNavigationItem{{ID: "run", FeatureAreaID: "running", Status: "RUNNING"}, {ID: "done", FeatureAreaID: "action", Status: "SUCCEEDED", UpdatedAt: time.Unix(50, 0)}, {ID: "active", FeatureAreaID: "action", Status: "RUNNING"}}
	for i := range tasks {
		tasks[i].Work.Type = task.TypeDevelopment
	}
	for _, tc := range []struct{ area, task, wantArea, wantTask string }{{"", "", "action", "active"}, {"recent", "", "recent", ""}, {"action", "done", "action", "done"}, {"gone", "run", "action", "active"}} {
		p := WorkspaceProjection{FeatureAreas: append([]FeatureAreaNavigationItem{}, areas...), Tasks: append([]TaskNavigationItem{}, tasks...)}
		SelectWorkspace(&p, tc.area, tc.task)
		if p.SelectedFeatureAreaID != tc.wantArea || p.SelectedTaskID != tc.wantTask {
			t.Fatalf("%+v: %+v", tc, p)
		}
		for _, v := range p.Tasks {
			if v.FeatureAreaID != tc.wantArea {
				t.Fatal("foreign task")
			}
		}
	}
	for _, closed := range []string{"RETIRED", "DECOMPOSED"} {
		p := WorkspaceProjection{FeatureAreas: []FeatureAreaNavigationItem{{ID: "old", Status: closed, Availability: Availability(closed)}, areas[0]}}
		SelectWorkspace(&p, "old", "")
		if p.SelectedFeatureAreaID != "old" {
			t.Fatal(p)
		}
	}
}

func TestFeatureAreaProductStatusIsCoreDerivedAndUnknownFailsOpenForDiagnosis(t *testing.T) {
	for _, tc := range []struct {
		internal string
		legacy   bool
		want     string
		ok       bool
	}{
		{"DISCOVERING", false, DomainStatusActive, true}, {"ACTIVE", false, DomainStatusActive, true}, {"RETIRING", false, DomainStatusActive, true},
		{"RETIRED", false, DomainStatusClosed, true}, {"DECOMPOSED", false, DomainStatusClosed, true}, {"ACTIVE", true, DomainStatusClosed, true},
		{"REVIEWING", false, "", false},
	} {
		got, ok := FeatureAreaDomainStatus(tc.internal, tc.legacy)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("%+v: got %q/%v", tc, got, ok)
		}
	}
	unknown := NewFeatureAreaProductProjection("area", "Area", "REVIEWING", false)
	if unknown.DomainStatus != "" || unknown.TechnicalDetails.Diagnostic != "UNKNOWN_FEATURE_AREA_INTERNAL_STATUS" || unknown.AvailableActions == nil {
		t.Fatalf("unknown projection = %+v", unknown)
	}
}

func TestWorkspaceLayeringActionPriorityAndTextJSON(t *testing.T) {
	p := Projection{LifecycleStatus: "ACTIVE", WorkflowStatus: "FAILED", NextAction: wp.Action{Code: "WAIT_FOR_USER_TASK"}, Commit: &featurearea.CommitCapability{CanCommit: true, CanRetry: true}}
	if got := CurrentAction(p).Code; got != "RETRY_TASK_COMMIT" {
		t.Fatal(got)
	}
	p.PendingDecision = &Decision{ID: "d"}
	if CurrentAction(p).Code != "RESOLVE_DECISION" {
		t.Fatal("decision priority")
	}
	p.Recovery = &Recovery{Required: true}
	if CurrentAction(p).Code != "RESOLVE_DECISION" {
		t.Fatal("pending decision priority")
	}
	p.PendingDecision = nil
	if CurrentAction(p).Code != "RECOVER" {
		t.Fatal("recovery priority")
	}
	p.Recovery = nil
	p.Commit.CanRetry = false
	if CurrentAction(p).Code != "COMMIT_FEATURE_AREA" {
		t.Fatal("commit priority")
	}
	item := FeatureAreaNavigationItem{ID: "a", Name: "Area", Status: p.LifecycleStatus, Product: NewFeatureAreaProductProjection("a", "Area", p.LifecycleStatus, false), Availability: Availability(p.LifecycleStatus), ReviewStatus: "FAILED", CommitStatus: "UNCOMMITTED", CurrentTaskStatus: "FAILED", CurrentAction: CurrentAction(p)}
	v := WorkspaceProjection{FeatureAreas: []FeatureAreaNavigationItem{item}, DevelopmentContext: DevelopmentContext{Project: Identity{Name: "Project"}}}
	if item.Availability != "AVAILABLE" || item.Status != "ACTIVE" {
		t.Fatal(item)
	}
	text := RenderWorkspaceText(v)
	if !strings.Contains(text, "活跃 · Review 失败") || !strings.Contains(text, "未提交") {
		t.Fatal(text)
	}
	raw, err := json.Marshal(v)
	if err != nil || !strings.Contains(string(raw), `"currentTaskStatus":"FAILED"`) {
		t.Fatalf("%s %v", raw, err)
	}
}

func TestWorkspaceSelectedWorkTypeContract(t *testing.T) {
	if ReviewLabel("INFRASTRUCTURE_FAILED") != "失败" {
		t.Fatal("Review infrastructure failure label")
	}
	for _, kind := range []task.Type{task.Type("REVIEW"), task.TypeInspection, task.TypeDevelopment} {
		for _, explicit := range []string{"", "work"} {
			p := WorkspaceProjection{FeatureAreas: []FeatureAreaNavigationItem{{ID: "area", Availability: "AVAILABLE", CurrentTaskID: "work", CurrentTaskStatus: "FAILED"}}, Tasks: []TaskNavigationItem{{ID: "work", FeatureAreaID: "area", Status: "FAILED", Work: task.Work{Type: kind}}}}
			SelectWorkspace(&p, "area", explicit)
			if p.SelectedWork != nil {
				t.Fatal(p)
			}
			want := ""
			if kind == task.TypeDevelopment {
				want = "work"
			}
			if p.SelectedTaskID != want {
				t.Fatalf("%s: %+v", kind, p)
			}
		}
	}
}

func TestCompactTaskProjectionOmitsEngineeringDefinition(t *testing.T) {
	value := task.Task{
		ID: "task", ProjectID: "project", FeatureAreaID: "area", RepositoryID: "repository",
		Title: strings.Repeat("界", 300), Phase: task.PhaseReviewRequired, Status: task.StatusActive,
		RequiredReviewDepth: task.ReviewDepthPointer(task.ReviewDepthDeep),
		ProblemStatement:    "private problem", Constraints: []string{"private constraint"},
		WorkSpec: task.WorkSpec{Goal: "private goal", CodexInstruction: "private implementation"},
	}
	compact := NewCompactTaskProjection(value)
	encoded, err := json.Marshal(compact)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) || len(compact.Title) > 512 || !strings.HasPrefix(compact.Title, "界") {
		t.Fatalf("invalid bounded compact title: %q", compact.Title)
	}
	for _, forbidden := range []string{"private problem", "private constraint", "private goal", "private implementation", "problemStatement", "constraints", "workSpec"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("compact projection leaked %q: %s", forbidden, encoded)
		}
	}
	if compact.RequiredReviewDepth == nil || *compact.RequiredReviewDepth != task.ReviewDepthDeep || len(compact.AllowedReviewDepths) != 1 || compact.AllowedReviewDepths[0] != task.ReviewDepthDeep {
		t.Fatal("compact Task lost the exact review depth requirement")
	}
	if !compact.DetailsAvailable || compact.ProjectID != "project" || compact.Phase != task.PhaseReviewRequired {
		t.Fatalf("compact identity facts = %+v", compact)
	}
}
