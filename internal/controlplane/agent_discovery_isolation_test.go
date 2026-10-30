package controlplane_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/probeexecution"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/reviewgrant"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func TestProjectDiscoveryDoesNotUseBusyFeatureAreaConversation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repoPath := filepath.Join(root, "repository")
	base := initializeContextFlowRepository(t, repoPath)
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	project, err := plane.Projects.Create(ctx, "Discovery isolation")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := plane.Repositories.RegisterInProject(ctx, project.ID, repoPath, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	session := bindFixtureSession(t, ctx, plane, "agent-discovery-isolation", project.ID)
	if _, err = plane.DevelopmentCycles.Select(ctx, developmentcycle.SelectInput{ProjectID: project.ID, RepositoryID: repo.ID, DevelopmentRef: "refs/heads/main", RequestID: "fixture-branch"}); err != nil {
		t.Fatal(err)
	}
	area, err := plane.FeatureAreas.Create(ctx, featurearea.CreateInput{AISessionID: session.Session.ID, Title: "Busy capability", ProblemStatement: "Preserve engineering context while project discovery runs."})
	if err != nil {
		t.Fatal(err)
	}
	m, err := plane.Modules.EnsureUnclassified(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := plane.Tasks.Create(ctx, task.CreateInput{AISessionID: session.Session.ID, RequestID: "agent-discovery-task", ProjectID: project.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "Busy capability", ProblemStatement: area.ProblemStatement, BaseRevision: base, Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	value, err := plane.Reviews.Create(ctx, frozenCodingReview(t, plane, review.Review{ReviewDepth: review.ReviewDepthDeep, TaskID: intent.ID, ProjectID: project.ID, FeatureAreaID: area.ID, CreatedByAISessionID: session.Session.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: base, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: area.ProblemStatement, Instruction: "Inspect the capability"}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.ReviewGrants.Ensure(ctx, reviewgrant.Grant{ReviewID: value.ID, FeatureAreaID: area.ID, ProjectID: project.ID, RepositoryID: repo.ID, Mode: reviewgrant.ModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}); err != nil {
		t.Fatal(err)
	}
	reviewRun, err := plane.Runs.CreateFocusedScopeReviewForReview(ctx, value.ID, value.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.Runs.Transition(ctx, reviewRun.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversation, err := plane.AgentConversations.BeginRun(ctx, reviewRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = plane.AgentConversations.BindThread(ctx, reviewRun.ID, "thread-feature-area"); err != nil {
		t.Fatal(err)
	}
	conversation, err = plane.AgentConversations.Get(ctx, conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := plane.AgentConversations.GetClaim(ctx, area.ID)
	if err != nil {
		t.Fatal(err)
	}
	probe, err := plane.ContextProbes.Create(ctx, contextprobe.CreateInput{AISessionID: session.Session.ID, RepositoryID: repo.ID, CodexConfigID: codexconfig.DefaultConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"AGENTS.md"}}, CodexInstruction: "Refresh Project guidance only."})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	runner, err := probeexecution.NewRunner(plane.ContextProbes, plane.Repositories, plane.CodexConfigs, &contextFlowProbeFactory{done: done, onStart: func(ctx context.Context) error {
		fence, ok := runtimeepoch.FromContext(ctx)
		if !ok {
			return fmt.Errorf("Inspection launch missing runtime fence")
		}
		r, err := plane.Runs.Get(ctx, fence.RunID)
		if err != nil {
			return err
		}
		v, err := plane.Tasks.Get(ctx, r.TaskID)
		if err != nil {
			return err
		}
		if v.Type != task.TypeInspection || v.Scope != task.ScopeProject || v.FeatureAreaID != "" || r.ContextProbeID != probe.ID || r.OperationKind != run.OperationInspection || v.Work.RunID != r.ID {
			return fmt.Errorf("Inspection launched without exact visible Task: %+v %+v", v, r)
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()
	if _, err = runner.Start(ctx, session.Session.ID, probe.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("synthetic discovery did not complete while FeatureArea was busy")
	}
	runner.Shutdown()
	completed, err := plane.ContextProbes.GetForSession(ctx, session.Session.ID, probe.ID)
	if err != nil || completed.Status != contextprobe.StatusSucceeded {
		t.Fatalf("probe=%+v err=%v", completed, err)
	}
	visible, err := plane.Tasks.Get(ctx, completed.TaskID)
	if err != nil || visible.Status != task.StatusSucceeded || visible.Work.RunID != completed.RunID {
		t.Fatalf("Inspection terminal envelope=%+v %v", visible, err)
	}
	after, err := plane.AgentConversations.Current(ctx, area.ID)
	if err != nil || !reflect.DeepEqual(after, conversation) {
		t.Fatalf("discovery changed FeatureArea context: %+v err=%v", after, err)
	}
	afterClaim, err := plane.AgentConversations.GetClaim(ctx, area.ID)
	if err != nil || !reflect.DeepEqual(afterClaim, claim) {
		t.Fatalf("discovery changed claim: %+v err=%v", afterClaim, err)
	}
	var conversations, claims, boundaries, tasks, runs int
	if err = plane.DB.SQL().QueryRow(`SELECT (SELECT COUNT(*) FROM agent_conversations),(SELECT COUNT(*) FROM feature_area_agent_claims),(SELECT COUNT(*) FROM agent_operation_boundaries),(SELECT COUNT(*) FROM tasks),(SELECT COUNT(*) FROM runs)`).Scan(&conversations, &claims, &boundaries, &tasks, &runs); err != nil {
		t.Fatal(err)
	}
	if conversations != 1 || claims != 1 || boundaries != 1 || tasks != 2 || runs != 2 {
		t.Fatalf("discovery allocated Agent context: conversations=%d claims=%d boundaries=%d tasks=%d runs=%d", conversations, claims, boundaries, tasks, runs)
	}
}
