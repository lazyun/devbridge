package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type surfaceReadFixture struct {
	BridgeService
	workflow                                      p.Projection
	workflowError                                 error
	task                                          task.Task
	review                                        review.Review
	run                                           run.Run
	tasks                                         []task.Task
	operations                                    []agentexecution.Projection
	taskReads, workflowReads, runReads, listReads int
}

func TestCompactWorkflowCarriesRunScopedResultProcessing(t *testing.T) {
	step := &p.TaskNextStep{Code: "RECOVER_RUN_RESULT", TaskID: "task", ExecutionID: "execution", TaskDefinitionRevisionID: "revision", RunID: "verify", Purpose: "VERIFICATION", Label: "待找回验证结果", Summary: "尚未收到本轮最终结果"}
	v := p.Projection{CurrentState: "RECOVERY_REQUIRED", NextStep: step, VerificationDecisions: &p.VerificationDecisions{TaskID: "task", ExecutionID: "execution", RevisionID: "revision"}}
	out := projectSurfaceWorkflow(v)
	if out.NextStep != step || out.NextStep.RunID != "verify" || out.VerificationDecisions != v.VerificationDecisions {
		t.Fatal("compact workflow lost exact result target")
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if err = resolveSchema(t, json.RawMessage(runtimeDTOSchema[SurfaceWorkflow]())).Validate(wire); err != nil {
		t.Fatal(err)
	}
}

func (f *surfaceReadFixture) ListSurfaceOperationsForSession(_ context.Context, _, _, _ string, o controlapi.ListOptions) ([]agentexecution.Projection, error) {
	if o.Offset >= len(f.operations) {
		return []agentexecution.Projection{}, nil
	}
	end := o.Offset + o.Limit
	if end > len(f.operations) {
		end = len(f.operations)
	}
	return f.operations[o.Offset:end], nil
}

func TestWorkListLegacyDiagnosticPreservesMixedPageAndCoreFacts(t *testing.T) {
	f := newSurfaceReadFixture()
	legacy := agentexecution.Projection{ExecutionID: "legacy-id", ProjectID: "project", Kind: agentexecution.Kind("UNKNOWN"), ProvenanceKind: agentexecution.ProvenanceKind("LEGACY_UNKNOWN"), ReadOnly: true, Status: "FAILED", BackendDisposition: "UNKNOWN", RunIDs: []string{"historic-run"}}
	native := agentexecution.Projection{ExecutionID: "execution", ProjectID: "project", TaskID: "task", FeatureAreaID: "area", Kind: agentexecution.KindDevelopment, ExecutionProposalID: "execution", SemanticOwnerKind: "EXECUTION", SemanticOwnerID: "execution", Status: "PENDING", RunIDs: []string{}}
	f.operations = []agentexecution.Projection{legacy, native, native}
	out, err := WorkListCore(context.Background(), f, WorkListInput{AISessionID: "session", Kind: "OPERATION", Limit: 2})
	if err != nil || len(out.Result.Operations) != 1 || len(out.Result.UnavailableOperations) != 1 || !out.Result.HasMore {
		t.Fatalf("mixed page lost rows: %+v %v", out, err)
	}
	diagnostic := out.Result.UnavailableOperations[0]
	if !diagnostic.ReadOnly || diagnostic.Position != 0 || diagnostic.CoreOperation.ExecutionID != legacy.ExecutionID || diagnostic.CoreOperation.RunIDs[0] != "historic-run" || diagnostic.Reason == "" || out.Result.Operations[0].Target.OperationID != "execution" {
		t.Fatalf("legacy facts or native target lost: %+v", out)
	}
	raw, _ := json.Marshal(diagnostic)
	if strings.Contains(string(raw), `"target"`) || strings.Contains(string(raw), `"capabilities"`) {
		t.Fatal("legacy diagnostic invented action authority", string(raw))
	}
	var document any
	encoded, _ := json.Marshal(out)
	_ = json.Unmarshal(encoded, &document)
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(WorkListOutputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(document); err != nil {
		t.Fatal(err)
	}
}

func (f *surfaceReadFixture) GetSession(context.Context, string) (aicontext.SessionContext, error) {
	return aicontext.SessionContext{Session: aisession.Session{ID: "session", ProjectID: "project"}, Project: project.Project{ID: "project", Name: "Example"}}, nil
}
func (f *surfaceReadFixture) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	projectID := "project"
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &projectID, AvailableProjects: []aicontext.ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true}, nil
}
func (f *surfaceReadFixture) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	return aicontext.ProjectBindingResult{}, errors.New("fixture binding is already established")
}
func (f *surfaceReadFixture) GetTask(_ context.Context, _, id string) (task.Task, error) {
	f.taskReads++
	if f.task.ID != id {
		return task.Task{}, errors.New("missing Task")
	}
	return f.task, nil
}
func (f *surfaceReadFixture) GetFeatureArea(context.Context, string, string) (featurearea.FeatureArea, error) {
	return featurearea.FeatureArea{ID: "area", ProjectID: "project", Title: "Area"}, nil
}
func (f *surfaceReadFixture) CompactWorkspaceForSession(context.Context, string, string, string) (p.Projection, error) {
	f.workflowReads++
	return f.workflow, f.workflowError
}
func (f *surfaceReadFixture) DevelopmentContextPresentationForSession(context.Context, string) (p.DevelopmentContext, error) {
	return p.DevelopmentContext{Project: p.Identity{ID: "project", Name: "Example"}, Repositories: []p.RepositoryContext{}}, nil
}
func (f *surfaceReadFixture) GetReview(context.Context, string, string) (review.Review, error) {
	return f.review, nil
}
func (f *surfaceReadFixture) GetRunForSession(context.Context, string, string) (run.Run, error) {
	f.runReads++
	return f.run, nil
}
func (f *surfaceReadFixture) ListSurfaceTasksForSession(_ context.Context, _, _, _ string, o controlapi.ListOptions) ([]task.Task, error) {
	f.listReads++
	if o.Offset >= len(f.tasks) {
		return []task.Task{}, nil
	}
	end := o.Offset + o.Limit
	if end > len(f.tasks) {
		end = len(f.tasks)
	}
	return f.tasks[o.Offset:end], nil
}
func newSurfaceReadFixture() *surfaceReadFixture {
	f := &surfaceReadFixture{task: task.Task{ID: "task", ProjectID: "project", FeatureAreaID: "area", Title: "Exact task", ProblemStatement: strings.Repeat("large spec", 1000), Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, WorkSpec: task.WorkSpec{Goal: strings.Repeat("large spec", 1000)}}}
	f.workflow = p.Projection{FeatureArea: p.Identity{ID: "area", Name: "Area"}, Details: p.Details{ProjectID: "project", FeatureAreaID: "area", TaskID: "task"}, CurrentState: "REVIEW_REQUIRED", Generation: "source-generation", SourceSequence: 42, NextAction: wp.Action{Code: "START_TASK_REVIEW"}, Capabilities: []p.Capability{{Action: "START_TASK_REVIEW", Available: true, Target: &p.ActionTarget{TaskID: "task", FeatureAreaID: "area"}}}, TaskStages: &p.TaskStages{TaskID: "task", Phase: "REVIEW_REQUIRED"}}
	return f
}
func TestWorkspaceGetCompactExactReadAndOptInTask(t *testing.T) {
	f := newSurfaceReadFixture()
	out, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Context.TaskID != "task" || out.Context.FeatureAreaID != "area" || out.Workflow.SourceSequence != 42 || out.Workflow.Generation != "source-generation" || out.Result.Atomic || out.Result.Task != nil || out.Result.Navigation != nil || f.taskReads != 1 || f.workflowReads != 1 {
		t.Fatalf("compact read=%+v calls=%+v", out, f)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "large spec") {
		t.Fatal("default leaked full Task requirements")
	}
	var compact map[string]any
	if err := json.Unmarshal(encoded, &compact); err != nil {
		t.Fatal(err)
	}
	result := compact["result"].(map[string]any)
	selected := result["selectedTask"].(map[string]any)
	for _, forbidden := range []string{"goal", "problemStatement", "constraints", "workSpec", "verificationSpec", "instruction"} {
		if _, exists := selected[forbidden]; exists {
			t.Fatalf("compact selectedTask structurally exposed %q: %s", forbidden, encoded)
		}
	}
	if selected["detailsAvailable"] != true || selected["id"] != "task" || selected["phase"] != "REVIEW_REQUIRED" {
		t.Fatalf("compact Task identity/phase/detail marker = %#v", selected)
	}
	if len(out.Workflow.Capabilities) != 1 || out.Workflow.Capabilities[0].Action != "START_TASK_REVIEW" {
		t.Fatalf("compact workflow actions = %#v", out.Workflow.Capabilities)
	}
	detailed, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", TaskID: "task", Include: []string{"TASK"}})
	if err != nil || detailed.Result.Task == nil || detailed.Result.Task.ProblemStatement != f.task.ProblemStatement {
		t.Fatalf("TASK include=%+v %v", detailed, err)
	}
	detailedJSON, _ := json.Marshal(detailed)
	if !strings.Contains(string(detailedJSON), `"problemStatement"`) || !strings.Contains(string(detailedJSON), "large spec") {
		t.Fatalf("exact TASK read omitted definition content: %s", detailedJSON)
	}
}
func TestWorkspaceGetMismatchAndPartialFailure(t *testing.T) {
	f := newSurfaceReadFixture()
	if _, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", FeatureAreaID: "different", TaskID: "task"}); err == nil {
		t.Fatal("mismatch accepted")
	}
	if f.workflowReads != 0 {
		t.Fatal("mismatch read another workflow")
	}
	f.workflowError = errors.New("source failed")
	out, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", TaskID: "task"})
	if err != nil || !out.Workflow.RefreshRequired || out.Result.SelectedTask == nil || out.Workflow.Generation != "" {
		t.Fatalf("partial failure=%+v %v", out, err)
	}
	if len(out.Workflow.Capabilities) != 0 || out.Result.Partitions[len(out.Result.Partitions)-1].ReadError == "" {
		t.Fatal("failed partition pretended to authorize actions")
	}
}

type surfaceTaskViewFixture struct {
	*surfaceReadFixture
	view p.Projection
}

func (f *surfaceTaskViewFixture) TaskPresentationForSession(context.Context, string, string, string) (p.Projection, error) {
	return f.view, nil
}
func TestWorkspaceTaskViewPartitionFailureRevokesActions(t *testing.T) {
	f := &surfaceTaskViewFixture{surfaceReadFixture: newSurfaceReadFixture()}
	f.workflowError = errors.New("guidance partition failed")
	f.view = p.Projection{Details: p.Details{FeatureAreaID: "area"}, TaskView: &p.TaskView{ID: "task", Phase: f.task.Phase, Status: string(f.task.Status), Actions: []p.Capability{{Action: "START_TASK_REVIEW", Available: true, Target: &p.ActionTarget{TaskID: "task"}}}, CurrentExecutionAcceptance: &p.ExecutionAcceptanceView{AllowedActions: []string{"ACCEPT"}}}}
	out, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", TaskID: "task", Include: []string{"TASK"}})
	if err != nil || !out.Workflow.RefreshRequired || out.Result.TaskView == nil || out.Result.TaskView.Actions[0].Available || len(out.Result.TaskView.CurrentExecutionAcceptance.AllowedActions) != 0 {
		t.Fatalf("partial Task view retained authority: %+v %v", out, err)
	}
}
func TestWorkListSemanticTaskScopeAndPagination(t *testing.T) {
	f := newSurfaceReadFixture()
	f.tasks = []task.Task{f.task, f.task}
	out, err := WorkListCore(context.Background(), f, WorkListInput{AISessionID: "session", Kind: "TASK", Limit: 1})
	if err != nil || !out.Result.HasMore || len(out.Result.Tasks) != 1 || f.listReads != 2 {
		t.Fatalf("page=%+v %v", out, err)
	}
	encoded, _ := json.Marshal(out.Result.Tasks[0])
	if strings.Contains(string(encoded), "large spec") {
		t.Fatalf("compact Task list leaked definition content: %s", encoded)
	}
	var compact map[string]any
	if err := json.Unmarshal(encoded, &compact); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"goal", "problemStatement", "constraints", "workSpec", "verificationSpec", "instruction"} {
		if _, exists := compact[forbidden]; exists {
			t.Fatalf("compact Task list structurally exposed %q: %s", forbidden, encoded)
		}
	}
	f.tasks[0].Type = task.Type("REVIEW")
	if _, err := WorkListCore(context.Background(), f, WorkListInput{AISessionID: "session", Kind: "TASK", Limit: 1}); err == nil {
		t.Fatal("Review pseudo Task accepted")
	}
	if _, err := WorkListCore(context.Background(), f, WorkListInput{AISessionID: "session", Kind: "TASK", Limit: 101}); err == nil {
		t.Fatal("unbounded page accepted")
	}
}
func TestOperationGetExactReviewAndRunAssertions(t *testing.T) {
	f := newSurfaceReadFixture()
	f.review = review.Review{ID: "review", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", RunID: "run"}
	f.run = run.Run{ID: "run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Status: run.StatusSucceeded, UpdatedAt: time.Unix(1, 0)}
	out, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: "review", OwnerTaskID: "task", RunID: "run"}})
	if err != nil || out.Result.Operation.Target.RunID != "run" || out.Result.Operation.Status != "SUCCEEDED" || len(out.Result.AgentClaims) != 0 || len(out.Result.Activity) != 0 {
		t.Fatalf("operation=%+v %v", out, err)
	}
	if _, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: "review", OwnerTaskID: "wrong"}}); err == nil {
		t.Fatal("wrong owner accepted")
	}
	if _, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: "review", RunID: "other"}}); err == nil {
		t.Fatal("wrong run accepted")
	}
}
func TestSurfaceReadSchemasAreClosedAndHandlersRejectUnknownIncludes(t *testing.T) {
	for _, raw := range []string{WorkspaceGetOutputSchema, WorkListOutputSchema, OperationGetOutputSchema} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(raw), &schema); err != nil {
			t.Fatal(err)
		}
		if schema["additionalProperties"] != false {
			t.Fatal("open envelope")
		}
		properties := schema["properties"].(map[string]any)
		for _, name := range []string{"result", "context", "workflow", "presentationHint"} {
			object := properties[name].(map[string]any)
			if object["additionalProperties"] != false || len(object["properties"].(map[string]any)) == 0 {
				t.Fatalf("open %s", name)
			}
		}
	}
	f := newSurfaceReadFixture()
	if _, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", Include: []string{"HISTORY"}}); err == nil {
		t.Fatal("unknown workspace include accepted")
	}
	if _, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Include: []string{"RESULT", "RESULT"}}); err == nil {
		t.Fatal("duplicate include accepted")
	}
	out, err := WorkspaceGetCore(context.Background(), f, WorkspaceGetInput{AISessionID: "session", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(out)
	var document any
	_ = json.Unmarshal(encoded, &document)
	var schema jsonschema.Schema
	if err = json.Unmarshal([]byte(WorkspaceGetOutputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(document); err != nil {
		t.Fatalf("output does not match tools/list contract: %v", err)
	}
}
