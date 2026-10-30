package controlapi_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

var startupLostResponse = errors.New("fixture: committed response lost")

type faultStartupAreas struct {
	*featurearea.Service
	point string
	fired bool
}

func (s *faultStartupAreas) CreateReceipted(ctx context.Context, in featurearea.CreateInput, r featurearea.CreationReceipt) (featurearea.FeatureArea, featurearea.CreationReceipt, error) {
	area, receipt, e := s.Service.CreateReceipted(ctx, in, r)
	if e == nil && s.point == "area-response" && !s.fired {
		s.fired = true
		return featurearea.FeatureArea{}, featurearea.CreationReceipt{}, startupLostResponse
	}
	return area, receipt, e
}
func (s *faultStartupAreas) UpdateCreationReceipt(ctx context.Context, r featurearea.CreationReceipt) error {
	if !s.fired && (s.point == "task-receipt" && r.TaskID != "" && !r.ReviewAdmitted || s.point == "review-receipt" && r.ReviewAdmitted) {
		s.fired = true
		return startupLostResponse
	}
	return s.Service.UpdateCreationReceipt(ctx, r)
}

type faultStartupTasks struct {
	*task.Service
	fired bool
}

func (s *faultStartupTasks) Create(ctx context.Context, in task.CreateInput) (task.Task, error) {
	v, e := s.Service.Create(ctx, in)
	if e == nil && !s.fired {
		s.fired = true
		return task.Task{}, startupLostResponse
	}
	return v, e
}

type faultStartupScope struct {
	*scopereview.Service
	fired bool
}

func (s *faultStartupScope) StartAdmitted(ctx context.Context, in scopereview.AdmittedStartInput) (review.StartAdmission, error) {
	v, e := s.Service.StartAdmitted(ctx, in)
	if e == nil && !s.fired {
		s.fired = true
		return review.StartAdmission{}, startupLostResponse
	}
	return v, e
}

type faultStartupRunner struct {
	point              string
	attempts, accepted int
}

func (s *faultStartupRunner) StartReview(ctx context.Context, _ string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	return reserve(ctx)
}
func (s *faultStartupRunner) StartReservedReview(_ context.Context, _ string, v run.Run) (run.Run, error) {
	s.attempts++
	if s.point == "dispatch-before" {
		return run.Run{}, startupLostResponse
	}
	s.accepted++
	if s.point == "dispatch-after" {
		return run.Run{}, startupLostResponse
	}
	return v, nil
}

type faultStartupRuns struct {
	controlapi.RunService
	fail bool
}

func (s *faultStartupRuns) Get(ctx context.Context, id string) (run.Run, error) {
	if s.fail {
		return run.Run{}, startupLostResponse
	}
	return s.RunService.Get(ctx, id)
}

func newFaultStartupFacade(t *testing.T, f startupFixture, areas controlapi.FeatureAreaService, tasks controlapi.TaskService, scope controlapi.ScopeReviewService, runs controlapi.RunService) *controlapi.Facade {
	t.Helper()
	p := f.plane
	facade, e := controlapi.New(controlapi.Dependencies{StartupContext: startupContext{p.AIContext}, DevelopmentCycles: p.DevelopmentCycles, Projects: p.Projects, ProjectRegistration: p.ProjectRegistration, ProjectSelection: p.ProjectSelection, AISessions: p.AISessions, Repositories: p.Repositories, Modules: p.Modules, ProjectContexts: p.ProjectContexts, ContextProbes: p.ContextProbes, ContextProbeRunner: p.ContextProbeRunner, Classifications: p.Classifications, FeatureAreas: areas, AgentConversations: p.AgentConversations, Documents: p.Documents, DecisionResolutions: p.DecisionResolutions, Reviews: p.Reviews, Tasks: tasks, TaskPlans: p.TaskPlans, ScopeReviews: scope, ScopeExpansions: p.ScopeExpansions, ExecutionProposals: p.ExecutionProposals, ExecutionCancellations: p.ExecutionCancellations, ExecutionGoals: p.ExecutionGoals, Runs: runs, Workspaces: p.Workspaces, Evidence: p.Evidence, Artifacts: p.Artifacts, Events: p.Events, Approvals: p.Approvals, Runner: p.Supervisor, ExecutionAllowlist: p.CodexRuntime})
	if e != nil {
		t.Fatal(e)
	}
	return facade
}
func TestFeatureAreaStartupFaultBoundariesKeepOneDurableAdmission(t *testing.T) {
	for _, point := range []string{"area-response", "task-response", "task-receipt", "review-receipt", "admission-response", "dispatch-before", "dispatch-after", "result-response", "readback"} {
		t.Run(point, func(t *testing.T) {
			f := newStartupFixture(t)
			ctx := f.ctx
			ba, bt, br, bn, be, bv := startupCounts(t, f)
			areas := &faultStartupAreas{Service: f.plane.FeatureAreas, point: point}
			var tasks controlapi.TaskService = f.plane.Tasks
			if point == "task-response" {
				tasks = &faultStartupTasks{Service: f.plane.Tasks}
			}
			runner := &faultStartupRunner{point: point}
			scope, e := scopereview.NewService(f.plane.AISessions, f.plane.Repositories, f.plane.Runs, runner, sqlite.NewReviewStore(f.plane.DB))
			if e != nil {
				t.Fatal(e)
			}
			scope.SetReviews(f.plane.Reviews)
			scope.SetInitialGrantService(f.plane.ReviewGrants)
			var admitted controlapi.ScopeReviewService = scope
			if point == "admission-response" {
				admitted = &faultStartupScope{Service: scope}
			}
			runs := &faultStartupRuns{RunService: f.plane.Runs, fail: point == "readback"}
			facade := newFaultStartupFacade(t, f, areas, tasks, admitted, runs)
			in := f.input("fault-" + point)
			first, e := facade.CreateFeatureAreaWithInitialReview(ctx, in)
			if e != nil || first.FeatureArea.ID == "" {
				t.Fatalf("first=%+v err=%v", first, e)
			}
			if point == "readback" && (!first.Startup.ReviewAdmitted || !first.Startup.RefreshRequired || first.Startup.FailedStep != "READBACK") {
				t.Fatalf("lost committed identity: %+v", first.Startup)
			}
			if point == "review-receipt" && (!first.Startup.ReviewAdmitted || first.Startup.RunID == "") {
				t.Fatalf("admission receipt loss hid committed IDs: %+v", first.Startup)
			}
			attempts := runner.attempts
			runs.fail = false
			// Reconstruct the application service: no process-local root state survives.
			restarted := newFaultStartupFacade(t, f, areas, tasks, admitted, runs)
			replay, e := restarted.CreateFeatureAreaWithInitialReview(ctx, in)
			if e != nil || !replay.Startup.ReviewAdmitted || replay.FeatureArea.ID != first.FeatureArea.ID {
				t.Fatalf("replay=%+v err=%v", replay, e)
			}
			if first.Startup.TaskID != "" && first.Startup.TaskID != replay.Startup.TaskID || first.Startup.ReviewID != "" && first.Startup.ReviewID != replay.Startup.ReviewID {
				t.Fatal("replay replaced committed identity")
			}
			if point != "task-receipt" && runner.attempts != attempts {
				t.Fatalf("replayed external handoff %d -> %d", attempts, runner.attempts)
			}
			a, ts, rs, ns, es, vs := startupCounts(t, f)
			if a != ba+1 || ts != bt+1 || rs != br+1 || ns != bn+1 || es != be || vs != bv+1 {
				t.Fatalf("durable counts %d/%d/%d/%d/%d/%d", a, ts, rs, ns, es, vs)
			}
			assertInitialAuthoredDraft(t, ctx, f.facade, f.sessionID, replay.Startup.TaskID)
			stored, e := f.plane.Reviews.Get(ctx, replay.Startup.ReviewID)
			if e != nil || stored.TaskID != replay.Startup.TaskID || stored.RunID != replay.Startup.RunID || stored.Instruction != in.InitialReview.ReviewInstruction {
				t.Fatalf("lost authored/owner identity %+v %v", stored, e)
			}
		})
	}
}

func TestFeatureAreaStartupRecoveryHonorsLifecycleAndFrozenModel(t *testing.T) {
	t.Run("retired-area", func(t *testing.T) {
		f := newStartupFixture(t)
		ctx := f.ctx
		f.tasks.failNext = startupLostResponse
		in := f.input("retired-root")
		partial, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, in)
		if e != nil || partial.Startup.TaskID != "" {
			t.Fatalf("partial %+v %v", partial, e)
		}
		if _, e = f.facade.RetireFeatureArea(ctx, f.sessionID, partial.FeatureArea.ID, "retire-fixture"); e != nil {
			t.Fatal(e)
		}
		replay, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, in)
		if e != nil || replay.FeatureArea.ID != partial.FeatureArea.ID || replay.Startup.TaskID != "" || replay.Startup.Retryable || replay.Startup.Error == nil {
			t.Fatalf("retired replay %+v %v", replay, e)
		}
	})
	t.Run("canceled-task", func(t *testing.T) {
		f := newStartupFixture(t)
		ctx := f.ctx
		f.scope.failNext = startupLostResponse
		in := f.input("canceled-root")
		partial, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, in)
		if e != nil || partial.Startup.TaskID == "" {
			t.Fatalf("partial %+v %v", partial, e)
		}
		if _, e = f.plane.DB.SQL().ExecContext(ctx, `UPDATE tasks SET status='CANCELED' WHERE id=?`, partial.Startup.TaskID); e != nil {
			t.Fatal(e)
		}
		replay, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, in)
		if e != nil || replay.Startup.TaskID != partial.Startup.TaskID || replay.Startup.ReviewAdmitted || replay.Startup.Retryable || replay.Startup.Error == nil {
			t.Fatalf("canceled replay %+v %v", replay, e)
		}
	})
	t.Run("disabled-model", func(t *testing.T) {
		f := newStartupFixture(t)
		ctx := f.ctx
		completeIn := f.input("complete-model")
		complete, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, completeIn)
		if e != nil || !complete.Startup.ReviewAdmitted {
			t.Fatalf("complete %+v %v", complete, e)
		}
		f.tasks.failNext = startupLostResponse
		partialIn := f.input("partial-model")
		partial, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, partialIn)
		if e != nil || partial.Startup.TaskID != "" {
			t.Fatalf("partial %+v %v", partial, e)
		}
		current := f.plane.CodexRuntime.Snapshot()
		next := current.Allowlist
		for i := range next.ModelConfigs {
			if next.ModelConfigs[i].ModelID == completeIn.InitialReview.Model {
				next.ModelConfigs[i].Enabled = false
			}
		}
		if _, e = f.plane.CodexRuntime.Apply(current.Revision, next); e != nil {
			t.Fatal(e)
		}
		replay, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, completeIn)
		if e != nil || replay.Startup.RunID != complete.Startup.RunID || !replay.Startup.ReviewAdmitted {
			t.Fatalf("complete replay %+v %v", replay, e)
		}
		blocked, e := f.facade.CreateFeatureAreaWithInitialReview(ctx, partialIn)
		if e != nil || blocked.FeatureArea.ID != partial.FeatureArea.ID || blocked.Startup.TaskID != "" || blocked.Startup.Error == nil {
			t.Fatalf("partial replay %+v %v", blocked, e)
		}
	})
}

func TestFeatureAreaStartupRejectedAdmissionDoesNotAdvertiseSameRequestRetry(t *testing.T) {
	f := newStartupFixture(t)
	// Canonical admission persists a busy rejection for its exact request key.
	// Retrying the root cannot change that immutable child request outcome.
	f.scope.failNext = &review.AdmissionBusyError{}
	got, err := f.facade.CreateFeatureAreaWithInitialReview(f.ctx, f.input("busy-admission"))
	if err != nil || got.Startup == nil || got.Startup.TaskID == "" || got.Startup.FailedStep != "REVIEW_ADMISSION" || got.Startup.ReviewAdmitted || got.Startup.Retryable || got.Startup.Retry != nil {
		t.Fatalf("incorrect immutable rejection recovery: %+v %v", got.Startup, err)
	}
}
