package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

type inventoryProjects struct{ ProjectService }

type ownedInventorySessions map[string]aisession.Session

func (s ownedInventorySessions) Get(ctx context.Context, id string) (aisession.Session, error) {
	return s.GetOwned(ctx, id)
}
func (s ownedInventorySessions) GetOwned(_ context.Context, id string) (aisession.Session, error) {
	value, ok := s[id]
	if !ok {
		return aisession.Session{}, aisession.ErrNotFound
	}
	return value, nil
}

func (inventoryProjects) Get(context.Context, string) (project.Project, error) {
	return project.Project{ID: "project", Name: "Project"}, nil
}

type inventoryAreas struct {
	FeatureAreaService
	values  []featurearea.FeatureArea
	details int
}

func (s *inventoryAreas) ListForSession(context.Context, string) ([]featurearea.FeatureArea, error) {
	return s.values, nil
}
func (s *inventoryAreas) GetForSession(context.Context, string, string) (featurearea.FeatureArea, error) {
	s.details++
	return featurearea.FeatureArea{}, errors.New("selected detail unavailable")
}

type inventoryProgression struct {
	wp.Store
	reads int
}

type currentLatestProgression struct{ wp.Store }

func (currentLatestProgression) Read(context.Context, string, string) (wp.Guidance, error) {
	return wp.Guidance{TaskPhase: task.PhaseVerifying, CurrentState: "VERIFICATION_RUNNING", CurrentTask: &wp.TaskFact{ID: "current", Status: string(task.StatusActive), Phase: task.PhaseVerifying}, LatestTask: &wp.TaskFact{ID: "latest", Status: string(task.StatusSucceeded), Phase: task.PhaseAcceptancePending}}, nil
}

func (s *inventoryProgression) Read(_ context.Context, _, area string) (wp.Guidance, error) {
	s.reads++
	if area == "bad" {
		return wp.Guidance{}, errors.New("one damaged read")
	}
	return wp.Guidance{LatestReviewStatus: "SUCCEEDED", NextAction: wp.Action{Code: "WAIT_FOR_USER_TASK"}}, nil
}
func (s *inventoryProgression) CommitInventoryCapability(_ context.Context, _, area string) (featurearea.CommitCapability, error) {
	c := featurearea.CommitCapability{CommitStatus: "UNCOMMITTED"}
	switch area {
	case "conflict":
		c.CanCommit = true
	case "retry":
		c.CanRetry = true
		c.RetryTaskID = "retry-task"
	case "merged":
		c.CommitStatus = "COMMITTED"
		c.MergedCommit = "dev-sha"
	}
	return c, nil
}
func (s *inventoryProgression) CommitInventoryFacts(_ context.Context, _, area string) (featurearea.CommitFacts, error) {
	switch area {
	case "conflict":
		return featurearea.CommitFacts{LastError: "MERGE_CONFLICT"}, nil
	case "retry":
		return featurearea.CommitFacts{Tasks: []featurearea.TaskCommitFact{{TaskID: "retry-task", Pending: true, LastError: "SNAPSHOT_DRIFT"}}}, nil
	}
	return featurearea.CommitFacts{}, nil
}
func TestWorkspaceInventoryLayeredFailuresAndBoundedReads(t *testing.T) {
	areas := &inventoryAreas{}
	for _, id := range []string{"empty", "bad", "failed", "conflict", "retry", "merged"} {
		areas.values = append(areas.values, featurearea.FeatureArea{ID: id, ProjectID: "project", Title: id, Status: featurearea.StatusActive})
	}
	tasks := scopeTasks{}
	for _, id := range []string{"failed", "conflict", "retry", "merged"} {
		v := task.Task{ID: id + "-task", ProjectID: "project", FeatureAreaID: id, Type: task.TypeDevelopment, Status: task.StatusSucceeded, ProblemStatement: id}
		switch id {
		case "failed":
			v.Status = task.StatusFailed
		case "conflict":
			v.CommitSHA = "task-sha"
		case "merged":
			v.CommitSHA = "task-sha"
			v.MergedCommit = "dev-sha"
		}
		tasks[v.ID] = v
	}
	tasks["foreign"] = task.Task{ID: "foreign", ProjectID: "other", FeatureAreaID: "empty"}
	progression := &inventoryProgression{}
	f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Projects: inventoryProjects{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: areas, Tasks: tasks, WorkflowProgression: progression}}
	out, err := f.DevelopmentWorkspaceForSession(context.Background(), "session", "empty", "")
	if err != nil || len(out.FeatureAreas) != 6 || len(out.Tasks) != 0 || out.ReadError != "" || out.SelectedWorkReadError == "" {
		t.Fatalf("%+v %v", out, err)
	}
	if progression.reads != 6 || areas.details != 1 {
		t.Fatalf("deep N+1: guidance=%d detail=%d", progression.reads, areas.details)
	}
	for _, a := range out.FeatureAreas {
		if a.Status != "ACTIVE" || a.Availability != "AVAILABLE" {
			t.Fatal("workflow failure leaked to lifecycle", a)
		}
		switch a.ID {
		case "bad":
			if a.ReadError == "" || a.CurrentAction.Code != "REFRESH" {
				t.Fatal(a)
			}
		case "failed":
			if a.CurrentTaskID != "" || a.LatestTaskStatus != "FAILED" || a.ReviewStatus != "SUCCEEDED" || a.CommitStatus != "UNCOMMITTED" {
				t.Fatal(a)
			}
		case "conflict":
			if a.CurrentTaskID != "" || a.CommitStatus != "UNCOMMITTED" || a.LatestTaskCommitStatus != "COMMITTED" || a.LatestTaskDevIntegrationStatus != "UNMERGED" || a.CurrentAction.Code != "COMMIT_FEATURE_AREA" {
				t.Fatal(a)
			}
		case "retry":
			if a.CurrentAction.Code != "RETRY_TASK_COMMIT" || a.CurrentTaskID != "retry-task" || a.CurrentTaskCommitStatus != "UNCOMMITTED" {
				t.Fatal(a)
			}
		case "merged":
			if a.CurrentTaskID != "" || a.CommitStatus != "COMMITTED" || a.MergedCommit != "dev-sha" || a.LatestTaskMergedCommit != "dev-sha" || a.LatestTaskDevIntegrationStatus != "MERGED" {
				t.Fatal(a)
			}
		}
	}
}

func TestWorkspaceAutomaticSelectionIsIndependentOfOtherEntries(t *testing.T) {
	areas := &inventoryAreas{values: []featurearea.FeatureArea{{ID: "area", ProjectID: "project", Title: "Area", Status: featurearea.StatusActive}}}
	tasks := scopeTasks{"done": {ID: "done", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusSucceeded}}
	f := &Facade{workspaceSelections: &workspaceSelections{values: make(map[string]workspaceSelection)}, deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Projects: inventoryProjects{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: areas, Tasks: tasks, WorkflowProgression: &inventoryProgression{}}}
	read := func(area, id, want string) {
		t.Helper()
		v, e := f.DevelopmentWorkspaceForSession(context.Background(), "session", area, id)
		if e != nil || v.SelectedTaskID != want {
			t.Fatalf("%+v %v", v, e)
		}
	}
	read("", "", "done")
	tasks["active"] = task.Task{ID: "active", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Status: task.StatusRunning}
	read("", "", "active")
	read("area", "done", "done")
	read("", "", "active")
}

func TestWorkspaceSeparatesCurrentAndLatestTaskPhase(t *testing.T) {
	areas := &inventoryAreas{values: []featurearea.FeatureArea{{ID: "area", ProjectID: "project", Title: "Area", Status: featurearea.StatusActive}}}
	tasks := scopeTasks{
		"current": {ID: "current", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Current", Status: task.StatusActive, Phase: task.PhaseVerifying},
		"latest":  {ID: "latest", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Title: "Latest", Status: task.StatusSucceeded, Phase: task.PhaseAcceptancePending},
	}
	f := &Facade{deps: Dependencies{AISessions: ownedInventorySessions{"session": {ID: "session", ProjectID: "project"}}, Projects: inventoryProjects{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: areas, Tasks: tasks, WorkflowProgression: currentLatestProgression{}}}
	out, err := f.DevelopmentWorkspaceForSession(context.Background(), "session", "area", "")
	if err != nil || len(out.FeatureAreas) != 1 {
		t.Fatalf("workspace=%+v err=%v", out, err)
	}
	area := out.FeatureAreas[0]
	if area.CurrentTaskID != "current" || area.CurrentTaskPhase != string(task.PhaseVerifying) || area.LatestTaskID != "latest" || area.LatestTaskPhase != string(task.PhaseAcceptancePending) {
		t.Fatalf("task ownership mixed: %+v", area)
	}
	if area.Product.DomainStatus != "ACTIVE" || area.Product.InternalStatus != "ACTIVE" || area.Product.CurrentTask == nil || area.Product.CurrentTask.ID != "current" || area.Product.LatestTask == nil || area.Product.LatestTask.ID != "latest" || area.Product.TaskPhase != string(task.PhaseVerifying) || area.Product.OperationActivity != nil {
		t.Fatalf("product projection mixed authority: %+v", area.Product)
	}
}

type workspaceCountingTasks struct {
	scopeTasks
	gets      int
	listError error
}

func (s *workspaceCountingTasks) Get(context.Context, string) (task.Task, error) {
	s.gets++
	return task.Task{}, errors.New("detail unavailable")
}
func (s *workspaceCountingTasks) List(ctx context.Context) ([]task.Task, error) {
	if s.listError != nil {
		return nil, s.listError
	}
	return s.scopeTasks.List(ctx)
}
func TestWorkspaceSelectedWorkRoutesAndLocalFailure(t *testing.T) {
	for _, kind := range []task.Type{task.Type("REVIEW"), task.TypeInspection, task.TypeDevelopment} {
		t.Run(string(kind), func(t *testing.T) {
			areas := &inventoryAreas{values: []featurearea.FeatureArea{{ID: "area", ProjectID: "project", Status: featurearea.StatusActive}}}
			tasks := &workspaceCountingTasks{scopeTasks: scopeTasks{"work": {ID: "work", ProjectID: "project", FeatureAreaID: "area", Type: kind, Status: task.StatusFailed, Work: task.Work{Type: kind}}}}
			f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Projects: inventoryProjects{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: areas, Tasks: tasks}}
			out, err := f.DevelopmentWorkspaceForSession(context.Background(), "session", "area", "")
			if err != nil || out.ReadError != "" || out.SelectedWorkReadError == "" || out.SelectedWork != nil || len(out.FeatureAreas) != 1 || len(out.Tasks) != 1 || out.RefreshedAt.IsZero() {
				t.Fatalf("%+v %v", out, err)
			}
			if kind == task.TypeDevelopment {
				if out.SelectedTaskID != "work" || tasks.gets != 1 {
					t.Fatalf("Task route: %+v gets=%d", out, tasks.gets)
				}
			} else if out.SelectedTaskID != "" || tasks.gets != 0 || areas.details != 1 {
				t.Fatalf("non-Task route: %+v gets=%d", out, tasks.gets)
			}
			tasks.listError = errors.New("inventory unavailable")
			if _, err := f.DevelopmentWorkspaceForSession(context.Background(), "session", "area", ""); err == nil {
				t.Fatal("authoritative failure swallowed")
			}
		})
	}
}

type recallProgression struct {
	wp.Store
	state string
}

func (s recallProgression) Read(context.Context, string, string) (wp.Guidance, error) {
	return wp.Guidance{CurrentState: s.state, TaskPhase: task.Phase(s.state), CurrentTask: &wp.TaskFact{ID: "running", Status: string(task.StatusActive), Phase: task.Phase(s.state)}}, nil
}
func TestWorkspaceRunningDevelopmentRecallFact(t *testing.T) {
	for _, state := range []string{"IMPLEMENTING", "VERIFYING", "REVIEW_RUNNING", "WAITING_APPROVAL", "WAITING_USER_INPUT", "WAITING_PERMISSION", "WAITING_FILE_APPROVAL", "WAITING_COMMAND_APPROVAL", "RECOVERY_REQUIRED", "TASK_DELIVERING", "TASK_RECOVERY_REQUIRED", "REVIEW_MATERIALIZING", "FEATURE_AREA_BUSY", "GIT_FINALIZATION_PENDING", "FAILED", "CANCELED", "DONE", "FEATURE_AREA_IDLE"} {
		for _, kind := range []task.Type{task.TypeDevelopment, task.Type("REVIEW"), task.TypeInspection} {
			t.Run(state+string(kind), func(t *testing.T) {
				areas := &inventoryAreas{values: []featurearea.FeatureArea{{ID: "a", ProjectID: "project", Status: featurearea.StatusActive}}}
				tasks := scopeTasks{"running": {ID: "running", ProjectID: "project", FeatureAreaID: "a", Type: kind}}
				f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Projects: inventoryProjects{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: areas, Tasks: tasks, WorkflowProgression: recallProgression{state: state}}}
				out, err := f.DevelopmentWorkspaceForSession(context.Background(), "session", "", "")
				if err != nil {
					t.Fatal(err)
				}
				want := kind == task.TypeDevelopment && (state == "IMPLEMENTING" || state == "VERIFYING")
				if (out.FeatureAreas[0].RunningDevelopmentTaskID != "") != want {
					t.Fatalf("unexpected recall fact: %+v", out.FeatureAreas[0])
				}
			})
		}
	}
}
