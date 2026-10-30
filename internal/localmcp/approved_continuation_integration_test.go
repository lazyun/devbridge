package localmcp_test

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

type unavailableFixtureBackend struct{}

func (unavailableFixtureBackend) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	return nil, errors.New("synthetic backend launch failure")
}

func TestApprovedTaskRestartRequiresExplicitStartAndPreservesInfraPhase(t *testing.T) {
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 2), message: reviewApprovalProposal(t)}
	f.plane.Coordinator.Codex = backend
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Restart durable intent")
	callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
	awaitClosure(t, backend).finish()
	g := awaitApprovalState(t, f, sid, area, "REVIEW_APPROVAL_PENDING")
	reviewValue, err := f.plane.Reviews.Get(f.ctx, g.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := reviewValue.RuntimeConfig
	out, err := f.plane.WorkflowProgression.Resolve(f.ctx, wp.ResolveInput{AISessionID: sid, TaskID: g.TaskID, DecisionID: g.DecisionID, Option: "APPROVE", RequestID: "approve-before-restart", BackendSelection: reviewValue.BackendSelection, NextRunRuntimeConfig: &runtime})
	if err != nil || out.NextAction.Code != "START_EXECUTION" {
		t.Fatalf("approval: %+v %v", out, err)
	}
	f.plane.Retirement.Close()
	approvedNoStartPrecondition(t, f, g.TaskID)
	captureApprovedHandoff(t, f, "exact-before-close")
	root := f.plane.DataPaths.Root
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// This test owns foreground dispatch; join the fixture-only continuation.
	reopened.Retirement.Close()
	probe := integrationFixture{ctx: f.ctx, plane: reopened}
	approvedNoStartPrecondition(t, &probe, g.TaskID)
	captureApprovedHandoff(t, &probe, "exact-after-open")
	reopened.Coordinator.Codex = unavailableFixtureBackend{}
	facade, err := controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatal(err)
	}
	value, err := reopened.Tasks.Get(f.ctx, g.TaskID)
	var n int
	if err != nil || value.Phase != task.PhaseImplementationReady || value.CurrentExecutionID == "" {
		t.Fatalf("restart: %+v %v", value, err)
	}
	if err = reopened.DB.SQL().QueryRow(`SELECT count(*) FROM execution_proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("restart lost the approved Execution: %d %v", n, err)
	}
	started, err := facade.StartTaskExecution(f.ctx, sid, g.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	waitApprovedInfrastructureFailure(t, &probe, started.Run.ID)
	captureApprovedHandoff(t, &probe, "exact-after-explicit-start")
	value, err = reopened.Tasks.Get(f.ctx, g.TaskID)
	if err != nil || value.Status != task.StatusActive || value.Phase != task.PhaseImplementing {
		t.Fatalf("infra advanced Task: %+v %v", value, err)
	}
	_, _ = facade.StartTaskExecution(f.ctx, sid, g.TaskID)
	var executions, runs int
	_ = reopened.DB.SQL().QueryRow(`SELECT count(*) FROM execution_proposals WHERE task_id=?`, g.TaskID).Scan(&executions)
	_ = reopened.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, g.TaskID).Scan(&runs)
	if executions != 1 || runs != 2 {
		t.Fatalf("retry duplicated business attempts: %d %d", executions, runs)
	}
}
