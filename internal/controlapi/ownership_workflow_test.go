package controlapi_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"

	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

type ownershipWorkflowFixture struct {
	plane                                                *controlplane.ControlPlane
	facade                                               *controlapi.Facade
	ctx                                                  context.Context
	projectID, sessionID, areaID, repositoryID, moduleID string
	now                                                  time.Time
}

func newOwnershipWorkflowFixture(t *testing.T, status featurearea.Status) ownershipWorkflowFixture {
	t.Helper()
	ctx := aisession.WithCallerScope(context.Background(), "controlapi-ownership-fixture")
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plane.Close() })
	p, err := plane.Projects.Create(ctx, "Ownership projection")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	repo := repository.Repository{ID: "ownership-repository", ProjectID: p.ID, Name: "Source", Path: filepath.Join(t.TempDir(), "source"), CreatedAt: now, UpdatedAt: now}
	if err := os.MkdirAll(repo.Path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := sqlite.NewRepositoryStore(plane.DB).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	mod, err := plane.Modules.EnsureUnclassified(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "controlapi-ownership-fixture"}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, "controlapi-ownership-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, p.ID, "controlapi-ownership-bind"); err != nil {
		t.Fatal(err)
	}
	session, err := plane.AIContext.GetSession(ctx, initialized.AISessionID)
	if err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "ownership-feature", ProjectID: p.ID, CreatedByAISessionID: session.Session.ID, Title: "Owned workflow", ProblemStatement: "Keep selected operation exact", Status: status, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(plane.DB).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	return ownershipWorkflowFixture{plane, facade, ctx, p.ID, session.Session.ID, area.ID, repo.ID, mod.ID, now}
}
func (f ownershipWorkflowFixture) createTask(t *testing.T) task.Task {
	t.Helper()
	v := task.Task{ID: "ownership-task", ProjectID: f.projectID, FeatureAreaID: f.areaID, RepositoryID: f.repositoryID, ModuleID: f.moduleID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Ownership intent", Goal: "Ownership intent", Status: task.StatusActive, Phase: task.PhaseReviewRequired, Model: "gpt-5.6-luna", ReasoningEffort: "low", CreatedAt: f.now, UpdatedAt: f.now}
	if err := sqlite.NewTaskStore(f.plane.DB).Create(f.ctx, v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f ownershipWorkflowFixture) codingReview(t *testing.T, value review.Review) review.Review {
	return codingReviewForPlane(t, f.ctx, f.plane, value)
}

func codingReviewForPlane(t *testing.T, ctx context.Context, plane *controlplane.ControlPlane, value review.Review) review.Review {
	t.Helper()
	config, err := plane.CodexConfigs.Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("TEST_SESSION_INVENTORY")
	value.ExecutorKind = review.ExecutorCodingAgent
	value.AuthorKind = string(review.AuthorAISession)
	value.AuthorID = value.CreatedByAISessionID
	value.CodexConfigID = config.ID
	value.BackendSelection = &selection
	if value.Model == "" {
		value.Model = "gpt-5.6-luna"
	}
	if value.ReasoningEffort == "" {
		value.ReasoningEffort = "low"
	}
	return value
}

func (f ownershipWorkflowFixture) bindProject(t *testing.T, projectID, suffix string) string {
	t.Helper()
	ref := aiprojectselection.ClientBindingReference{CallerScope: "controlapi-ownership-fixture"}
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, ref, "controlapi-ownership-"+suffix+"-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.AIContext.BindProject(f.ctx, ref, initialized.AISessionID, projectID, "controlapi-ownership-"+suffix+"-bind"); err != nil {
		t.Fatal(err)
	}
	return initialized.AISessionID
}

func bindControlAPIFixtureProject(t *testing.T, ctx context.Context, plane *controlplane.ControlPlane, callerScope, projectID, suffix string) (context.Context, string) {
	t.Helper()
	ctx = aisession.WithCallerScope(ctx, callerScope)
	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := plane.AIContext.InitializeSession(ctx, ref, suffix+"-initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plane.AIContext.BindProject(ctx, ref, initialized.AISessionID, projectID, suffix+"-bind"); err != nil {
		t.Fatal(err)
	}
	return ctx, initialized.AISessionID
}

func TestCurrentWorkflowProjectsReviewApprovalFromExactRun(t *testing.T) {
	for _, kind := range []string{"REVIEW"} {
		t.Run(kind, func(t *testing.T) {
			status := featurearea.StatusActive
			if kind == "REVIEW" {
				status = featurearea.StatusDiscovering
			}
			f := newOwnershipWorkflowFixture(t, status)
			ctx := f.ctx
			runs := f.plane.Runs
			store := sqlite.NewRunStore(f.plane.DB)
			var reserved run.Run
			var err error
			if kind == "REVIEW" {
				semantic := f.createTask(t)
				owner, createErr := f.plane.Reviews.Create(ctx, f.codingReview(t, review.Review{ReviewDepth: "DEEP", TaskID: semantic.ID, ProjectID: f.projectID, FeatureAreaID: f.areaID, CreatedByAISessionID: f.sessionID, RepositoryID: f.repositoryID, CodexConfigID: codexconfig.DefaultConfigID, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Review taskless capability", Instruction: "Inspect source"}))
				if createErr != nil {
					t.Fatal(createErr)
				}
				reserved, err = store.CreateProductScopeReviewAttempt(ctx, owner.ID, "approval-review-run", owner.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository}, f.now)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Transition(ctx, reserved.ID, run.StatusPreparing); err != nil {
				t.Fatal(err)
			}
			conversations := sqlite.NewAgentConversationStore(f.plane.DB)
			if _, err := conversations.BeginRun(ctx, reserved.ID); err != nil {
				t.Fatal(err)
			}
			if err := conversations.BindThread(ctx, reserved.ID, "approval-thread"); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.AttachThread(ctx, reserved.ID, "approval-thread"); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.Transition(ctx, reserved.ID, run.StatusRunning); err != nil {
				t.Fatal(err)
			}
			if err := conversations.MarkTurnPending(ctx, reserved.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := runs.AttachTurn(ctx, reserved.ID, "approval-turn"); err != nil {
				t.Fatal(err)
			}
			pending, err := sqlite.NewApprovalStore(f.plane.DB).CreatePending(ctx, approval.Approval{ID: "ownership-pending-approval", RunID: reserved.ID, ThreadID: "approval-thread", TurnID: "approval-turn", Kind: approval.KindCommandExecution, AllowedDecisions: []approval.DecisionType{approval.DecisionDecline}, RequestPayload: []byte(`{}`)}, f.now)
			if err != nil {
				t.Fatal(err)
			}
			v, err := f.facade.CurrentWorkflowForSession(ctx, f.sessionID, f.areaID)
			if err != nil || !v.ApprovalPending || v.PendingApproval == nil || v.PendingApproval.ID != pending.ID || v.PendingApproval.RunID != reserved.ID || len(v.Runs) != 1 || v.Runs[0].ID != reserved.ID {
				t.Fatalf("approval disappeared from owned workflow: %+v err=%v", v, err)
			}
			if kind == "REVIEW" {
				if v.Task == nil || v.Task.ID != reserved.TaskID {
					t.Fatalf("Review selected a Development workflow Task: %+v", v.Task)
				}
				envelope, e := f.plane.Tasks.Get(ctx, reserved.TaskID)
				if e != nil || envelope.Type != task.TypeDevelopment || envelope.CurrentDefinitionRevisionID != "" {
					t.Fatalf("Review lost visible envelope: %+v %v", envelope, e)
				}
			}
		})
	}
}
