package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestFeatureAreaStartupRealSQLiteSDKWire(t *testing.T) {
	f := newStartupWireFixture(t)
	service, e := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true}, f.plane.AIContext, f.facade)
	if e != nil {
		t.Fatal(e)
	}
	server, e := localmcp.New(localmcp.Config{ListenAddress: "127.0.0.1:0"}, service)
	if e != nil {
		t.Fatal(e)
	}
	transport := httptest.NewServer(server.Handler())
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "startup-wire", Version: "1"}, nil)
	session, e := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: transport.URL + mcpserver.MCPPath, HTTPClient: transport.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	list, e := session.ListTools(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	var descriptor *sdkmcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "feature_area_create" {
			descriptor = tool
		}
	}
	if descriptor == nil || descriptor.Annotations.ReadOnlyHint {
		t.Fatal("composition tool missing or marked read-only")
	}
	raw, _ := json.Marshal(descriptor.OutputSchema)
	var schema jsonschema.Schema
	if e = json.Unmarshal(raw, &schema); e != nil {
		t.Fatal(e)
	}
	validator, e := schema.Resolve(nil)
	if e != nil {
		t.Fatal(e)
	}
	evidence := os.Getenv("DEVBRIDGE_STARTUP_EVIDENCE_DIR")
	save := func(name string, value any) {
		if evidence == "" {
			return
		}
		if e := os.MkdirAll(evidence, 0700); e != nil {
			t.Fatal(e)
		}
		raw, e := json.MarshalIndent(value, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(evidence, name+".json"), append(raw, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
	save("feature-area-create-schema", descriptor)
	call := func(name string, args any) map[string]any {
		t.Helper()
		result, e := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_create", Arguments: args})
		if e != nil || result.IsError {
			detail, _ := json.Marshal(result)
			t.Fatalf("%s: %v %s", name, e, detail)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var wire map[string]any
		if e = json.Unmarshal(raw, &wire); e != nil {
			t.Fatal(e)
		}
		if e = validator.Validate(wire); e != nil {
			t.Fatalf("%s schema: %v", name, e)
		}
		if result.Meta["openai/outputTemplate"] != nil {
			t.Fatal("silent creation returned UI template")
		}
		save(name, map[string]any{"arguments": args, "response": result})
		return wire
	}
	old := map[string]any{"aiSessionId": f.sessionID, "title": "Only record", "problemStatement": "Do not investigate yet."}
	ba, bt, br, bn, _, _ := startupWireCounts(t, f)
	only := call("create-only", old)
	if only["result"].(map[string]any)["startup"] != nil {
		t.Fatal("old caller silently upgraded")
	}
	a, ts, rs, ns, _, _ := startupWireCounts(t, f)
	if a != ba+1 || ts != bt || rs != br || ns != bn {
		t.Fatal("CREATE_ONLY created agent state")
	}
	in := f.input("sdk-normal")
	in.InitialReview.ReviewInstruction = "  Inspect exact source.\nPreserve authored whitespace.\n"
	normal := call("normal", in)
	start := normal["result"].(map[string]any)["startup"].(map[string]any)
	if start["reviewAdmitted"] != true || start["taskId"] == "" || normal["context"].(map[string]any)["taskId"] != start["taskId"] {
		t.Fatalf("normal startup %#v", normal)
	}
	owner, e := f.plane.Tasks.Get(ctx, start["taskId"].(string))
	if e != nil || owner.CurrentReviewID != start["reviewId"] {
		t.Fatal("current Review pointer mismatch", e)
	}
	review, e := f.plane.Reviews.Get(ctx, start["reviewId"].(string))
	if e != nil || review.Instruction != in.InitialReview.ReviewInstruction {
		t.Fatal("prompt bytes changed", e)
	}
	beforeCalls := f.scope.starts
	repeated := call("response-loss-retry", in)["result"].(map[string]any)["startup"].(map[string]any)
	if repeated["runId"] != start["runId"] || f.scope.starts != beforeCalls {
		t.Fatal("response retry repeated Review handoff")
	}
	f.tasks.failNext = startupWireLostResponse
	partialIn := f.input("sdk-partial")
	partial := call("partial-task-failure", partialIn)["result"].(map[string]any)["startup"].(map[string]any)
	if partial["failedStep"] != "TASK_CREATE" || partial["taskId"] != nil {
		t.Fatalf("partial %#v", partial)
	}
	resumed := call("partial-task-resumed", partialIn)["result"].(map[string]any)["startup"].(map[string]any)
	if resumed["featureAreaId"] != partial["featureAreaId"] || resumed["reviewAdmitted"] != true {
		t.Fatal("partial retry replaced area")
	}
}

var startupWireLostResponse = errors.New("fixture: task response lost")

type startupWireContext struct{ *aicontext.Service }

func (s startupWireContext) GetSession(ctx context.Context, id string) (aicontext.SessionContext, error) {
	v, err := s.Service.GetSession(ctx, id)
	if err == nil {
		v.ProjectContext = projectcontext.ProjectContext{ProjectID: v.Project.ID, Status: projectcontext.StatusNoGuidance}
	}
	return v, err
}

type startupWireRunner struct{}

func (startupWireRunner) StartReview(ctx context.Context, _ string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	return reserve(ctx)
}
func (startupWireRunner) StartReservedReview(_ context.Context, _ string, value run.Run) (run.Run, error) {
	return value, nil
}

type startupWireScope struct {
	*scopereview.Service
	starts int
}

func (s *startupWireScope) StartAdmitted(ctx context.Context, input scopereview.AdmittedStartInput) (review.StartAdmission, error) {
	s.starts++
	return s.Service.StartAdmitted(ctx, input)
}

type startupWireTasks struct {
	*task.Service
	failNext error
}

func (s *startupWireTasks) Create(ctx context.Context, input task.CreateInput) (task.Task, error) {
	if s.failNext != nil {
		err := s.failNext
		s.failNext = nil
		return task.Task{}, err
	}
	return s.Service.Create(ctx, input)
}

type startupWireFixture struct {
	plane        *controlplane.ControlPlane
	facade       *controlapi.Facade
	sessionID    string
	repositoryID string
	scope        *startupWireScope
	tasks        *startupWireTasks
}

func newStartupWireFixture(t *testing.T) startupWireFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	p, err := plane.Projects.Create(ctx, "startup wire")
	if err != nil {
		t.Fatal(err)
	}
	repoPath := createExternalWorkflowRepository(t, root, "repository")
	repo := repository.Repository{ID: "startup-wire-repository", ProjectID: p.ID, Name: "Source", Path: repoPath, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, _, err = sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Modules.EnsureUnclassified(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
	session, err := plane.AIContext.InitializeSession(ctx, ref, "startup-wire-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AIContext.BindProject(ctx, ref, session.AISessionID, p.ID, "startup-wire-bind"); err != nil {
		t.Fatal(err)
	}
	development, err := plane.DevelopmentCycles.Context(ctx, p.ID)
	if err != nil || len(development.Repositories) != 1 || len(development.Repositories[0].Branches) == 0 {
		t.Fatalf("development inventory=%+v err=%v", development, err)
	}
	if _, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: p.ID, RepositoryID: repo.ID, DevelopmentRef: development.Repositories[0].Branches[0].FullRef, RequestID: "startup-wire-branch"}); err != nil {
		t.Fatal(err)
	}
	selectCommitTestBaseline(t, plane, p.ID, repo.ID)
	scopeService, err := scopereview.NewService(plane.AISessions, plane.Repositories, plane.Runs, startupWireRunner{}, sqlite.NewReviewStore(plane.DB))
	if err != nil {
		t.Fatal(err)
	}
	scopeService.SetReviews(plane.Reviews)
	scopeService.SetInitialGrantService(plane.ReviewGrants)
	scope := &startupWireScope{Service: scopeService}
	tasks := &startupWireTasks{Service: plane.Tasks}
	facade, err := controlapi.New(controlapi.Dependencies{StartupContext: startupWireContext{plane.AIContext}, DevelopmentCycles: plane.DevelopmentCycles, Projects: plane.Projects, ProjectRegistration: plane.ProjectRegistration, ProjectSelection: plane.ProjectSelection, AISessions: plane.AISessions, Repositories: plane.Repositories, Modules: plane.Modules, ProjectContexts: plane.ProjectContexts, ContextProbes: plane.ContextProbes, ContextProbeRunner: plane.ContextProbeRunner, Classifications: plane.Classifications, FeatureAreas: plane.FeatureAreas, AgentConversations: plane.AgentConversations, Documents: plane.Documents, DecisionResolutions: plane.DecisionResolutions, Reviews: plane.Reviews, Tasks: tasks, TaskPlans: plane.TaskPlans, ScopeReviews: scope, ScopeExpansions: plane.ScopeExpansions, ExecutionProposals: plane.ExecutionProposals, ExecutionCancellations: plane.ExecutionCancellations, ExecutionGoals: plane.ExecutionGoals, Runs: plane.Runs, Workspaces: plane.Workspaces, Evidence: plane.Evidence, Artifacts: plane.Artifacts, Events: plane.Events, Approvals: plane.Approvals, Runner: plane.Supervisor, ExecutionAllowlist: plane.CodexRuntime})
	if err != nil {
		t.Fatal(err)
	}
	return startupWireFixture{plane: plane, facade: facade, sessionID: session.AISessionID, repositoryID: repo.ID, scope: scope, tasks: tasks}
}

func (f startupWireFixture) input(requestID string) controlapi.FeatureAreaCreateInput {
	return controlapi.FeatureAreaCreateInput{AISessionID: f.sessionID, Title: "SDK startup", ProblemStatement: "Inspect startup without modification.", Mode: controlapi.CreateAndReview, RequestID: requestID, InitialTask: &controlapi.InitialTaskInput{Title: "Inspect startup", ProblemStatement: "Inspect startup without modification.", Constraints: []string{"Read-only"}, RepositoryID: f.repositoryID}, InitialReview: &controlapi.InitialReviewInput{ReviewDepth: "DEEP", ReviewPlan: controlapi.InitialReviewPlan{Mode: "REPOSITORY"}, ReviewInstruction: "Inspect exact source.", Model: "gpt-5.6-luna"}}
}

func startupWireCounts(t *testing.T, f startupWireFixture) (areas, tasks, reviews, runs, executions, revisions int) {
	t.Helper()
	if err := f.plane.DB.SQL().QueryRowContext(context.Background(), `SELECT (SELECT COUNT(*) FROM feature_areas), (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM reviews), (SELECT COUNT(*) FROM runs), (SELECT COUNT(*) FROM execution_proposals), (SELECT COUNT(*) FROM task_definition_revisions)`).Scan(&areas, &tasks, &reviews, &runs, &executions, &revisions); err != nil {
		t.Fatal(err)
	}
	return
}
