package controlapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type compactOnlyTasks struct {
	scopeTasks
	lists int
}

func (s *compactOnlyTasks) List(context.Context) ([]task.Task, error) {
	s.lists++
	return nil, errors.New("history enumeration forbidden")
}

type compactAreas struct{ FeatureAreaService }

type compactReviews struct{ ReviewService }

func (compactReviews) ListByFeatureArea(context.Context, string) ([]review.Review, error) {
	return []review.Review{}, nil
}

type compactProjectContexts struct{}

func (compactProjectContexts) Get(_ context.Context, projectID string) (projectcontext.ProjectContext, error) {
	return projectcontext.ProjectContext{ProjectID: projectID, Status: projectcontext.StatusNoGuidance}, nil
}

type compactConversations struct{}

func (compactConversations) Reset(context.Context, string, string) (agentconversation.Conversation, error) {
	return agentconversation.Conversation{}, nil
}
func (compactConversations) State(context.Context, string) (agentconversation.State, error) {
	return agentconversation.State{Status: "READY"}, nil
}
func (compactConversations) Recover(context.Context, agentconversation.RecoverInput) (agentconversation.RecoveryResult, error) {
	return agentconversation.RecoveryResult{}, nil
}

func (compactAreas) GetForSession(context.Context, string, string) (featurearea.FeatureArea, error) {
	return featurearea.FeatureArea{ID: "area", ProjectID: "project", Status: featurearea.StatusActive}, nil
}

type compactGuidance struct {
	wp.Store
	value wp.Guidance
}

func (s compactGuidance) Read(context.Context, string, string) (wp.Guidance, error) {
	return s.value, nil
}

type preWorkCancellationRead struct {
	executioncancel.Store
	project, taskID string
	available       bool
}

func (s *preWorkCancellationRead) PreWorkTaskCancellationEligible(_ context.Context, project, taskID string) (bool, error) {
	s.project, s.taskID = project, taskID
	return s.available, nil
}

func TestCompactWorkspacePreservesCanonicalPreWorkTaskCancellation(t *testing.T) {
	value := task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, Status: task.StatusActive}
	reader := &preWorkCancellationRead{available: true}
	g := wp.Guidance{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskPhase: value.Phase, WorkflowMode: "CURRENT", CurrentTask: &wp.TaskFact{ID: value.ID, Status: string(value.Status), Phase: value.Phase}, CurrentState: "REVIEW_REQUIRED", NextAction: wp.Action{Code: "START_TASK_REVIEW"}}
	f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Tasks: &compactOnlyTasks{scopeTasks: scopeTasks{"task": value}}, Projects: inventoryProjects{}, ProjectContexts: compactProjectContexts{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: compactAreas{}, Reviews: compactReviews{}, Runs: scopeRuns{}, AgentConversations: compactConversations{}, WorkflowProgression: compactGuidance{value: g}, ExecutionCancellations: reader}}
	out, err := f.CompactWorkspaceForSession(context.Background(), "session", "area", "task")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range out.Capabilities {
		if c.Action != "CANCEL_EXECUTION" {
			continue
		}
		found = c.Available && c.Target != nil && c.Target.OperationKind == "TASK" && c.Target.OperationID == value.ID && c.Target.TaskID == value.ID && c.Target.FeatureAreaID == value.FeatureAreaID && c.Target.RunID == "" && c.Target.ExecutionID == ""
	}
	if !found || reader.project != value.ProjectID || reader.taskID != value.ID {
		t.Fatalf("missing exact canonical pre-work cap: %+v", out.Capabilities)
	}
	reader.available = false
	if cap, err := f.preWorkTaskCancellationCapability(context.Background(), &value); err != nil || cap != nil {
		t.Fatalf("unavailable source gained capability: %+v %v", cap, err)
	}
	value.Type = task.TypeInspection
	reader.available = true
	if cap, err := f.preWorkTaskCancellationCapability(context.Background(), &value); err != nil || cap != nil {
		t.Fatalf("Inspection gained Task cancellation: %+v %v", cap, err)
	}
	value.Type, value.LegacyReadOnly = task.TypeDevelopment, true
	if cap, err := f.preWorkTaskCancellationCapability(context.Background(), &value); err != nil || cap != nil {
		t.Fatalf("legacy Task gained cancellation: %+v %v", cap, err)
	}
}

func (s compactGuidance) ReadTask(context.Context, string, string, string) (wp.Guidance, error) {
	return s.value, nil
}
func (s compactGuidance) TaskReviewRequestEligibility(context.Context, string, string) (wp.TaskReviewRequestEligibility, error) {
	return wp.TaskReviewRequestEligibility{Available: false, Reason: "No supplemental Review requested."}, nil
}
func (s compactGuidance) RequestTaskReview(context.Context, wp.TaskReviewRequestInput) (wp.TaskReviewRequestResolution, error) {
	return wp.TaskReviewRequestResolution{}, errors.New("fixture write unavailable")
}
func TestCompactWorkspaceCurrentPointersWithoutHistory(t *testing.T) {
	tasks := &compactOnlyTasks{scopeTasks: scopeTasks{"task": {ID: "task", ProjectID: "project", FeatureAreaID: "area", Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, Status: task.StatusActive}}}
	guidance := wp.Guidance{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskPhase: task.PhaseReviewRequired, WorkflowMode: "CURRENT", FeatureAreaStatus: "ACTIVE", CurrentTask: &wp.TaskFact{ID: "task", Status: "ACTIVE", Phase: task.PhaseReviewRequired}, CurrentState: "REVIEW_REQUIRED", NextAction: wp.Action{Code: "START_TASK_REVIEW"}, Generation: "g1", SourceSequence: 41}
	f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Tasks: tasks, Projects: inventoryProjects{}, ProjectContexts: compactProjectContexts{}, DevelopmentCycles: &developmentPort{}, FeatureAreas: compactAreas{}, Reviews: compactReviews{}, Runs: scopeRuns{}, AgentConversations: compactConversations{}, WorkflowProgression: compactGuidance{value: guidance}}}
	out, err := f.CompactWorkspaceForSession(context.Background(), "session", "area", "task")
	if err != nil || tasks.lists != 0 || out.TaskStages == nil || out.TaskStages.TaskID != "task" || out.Generation == "" || out.SourceSequence != 41 {
		t.Fatalf("compact=%+v lists=%d err=%v", out, tasks.lists, err)
	}
	changed := tasks.scopeTasks["task"]
	changed.Phase = task.PhaseReviewing
	tasks.scopeTasks["task"] = changed
	if _, err = f.CompactWorkspaceForSession(context.Background(), "session", "area", "task"); err == nil {
		t.Fatal("mixed generation Task phase authorized")
	}
	if _, err = f.CompactWorkspaceForSession(context.Background(), "session", "other", "task"); err == nil {
		t.Fatal("explicit FeatureArea mismatch accepted")
	}
}

func TestCompactTaskPresentationRetainsWorkflowAndOmitsExactViews(t *testing.T) {
	current := &wp.TaskFact{ID: "task", Status: string(task.StatusActive), Phase: task.PhaseReviewRequired}
	exact := p.Projection{
		TaskView:       &p.TaskView{ID: "task", OriginalRequirement: task.OriginalRequirement{Goal: "private problem"}},
		LatestTaskView: &p.TaskView{ID: "old", OriginalRequirement: task.OriginalRequirement{Goal: "old private problem"}},
		CurrentTask:    current,
		NextAction:     wp.Action{Code: "START_TASK_REVIEW", Required: true},
		Capabilities:   []p.Capability{{Action: "START_TASK_REVIEW", Available: true}},
		TaskStages:     &p.TaskStages{TaskID: "task", Phase: string(task.PhaseReviewRequired)},
	}
	decisions := &p.VerificationDecisions{TaskID: "task", ExecutionID: "execution", RevisionID: "revision"}
	exact.TaskView.VerificationDecisions = decisions
	exact.TaskView.Actions = []p.Capability{{Action: "CONTINUE_TASK", Available: true}}
	compact := compactTaskPresentation(exact)
	if compact.TaskView != nil || compact.LatestTaskView != nil {
		t.Fatalf("exact Task views leaked: %+v", compact)
	}
	if compact.CurrentTask != current || compact.TaskStages == nil || compact.NextAction.Code != "START_TASK_REVIEW" || len(compact.Capabilities) != 2 || compact.VerificationDecisions != decisions {
		t.Fatalf("compact workflow facts changed: %+v", compact)
	}
}
func TestReviewCancellationCapabilityRequiresExactCanonicalFacts(t *testing.T) {
	r := run.Run{ID: "run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Status: run.StatusRunning, RuntimeEpoch: "epoch", ThreadID: "thread", TurnID: "turn"}
	f := &Facade{deps: Dependencies{AISessions: scopeSessions{"session": {ID: "session", ProjectID: "project"}}, Runs: scopeRuns{"run": r}, Runner: cancellationLiveRunner{available: true}, ExecutionCancellations: &cancellationReadStub{}}}
	current := CurrentWorkflow{Task: &task.Task{ID: "task"}, CurrentReview: &review.Review{ID: "review", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", RunID: "run", ExecutorKind: review.ExecutorCodingAgent}}
	op := agentconversation.State{Status: "BUSY", AgentConversationID: "conversation", Claim: &agentconversation.Claim{FeatureAreaID: "area", Kind: agentconversation.Review, OperationID: "review", RunID: "run", RuntimeEpoch: "epoch"}}
	cap, err := f.reviewCancellationEligibility(context.Background(), "session", current, op)
	if err != nil || !cap.Available || cap.Target == nil || cap.Target.OperationID != "review" || cap.Target.RunID != "run" || cap.Target.ExecutionID != "" {
		t.Fatalf("Review cancel=%+v %v", cap, err)
	}
	op.Claim.OperationID = "another-review"
	cap, err = f.reviewCancellationEligibility(context.Background(), "session", current, op)
	if err != nil || cap.Available || cap.Target != nil {
		t.Fatalf("wrong Review granted cancel=%+v %v", cap, err)
	}
}
