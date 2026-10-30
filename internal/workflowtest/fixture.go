// Package workflowtest builds bounded synthetic workflow facts through the
// public durable stores. It is imported only by tests, never production wiring.
package workflowtest

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func ReviewGate(t testing.TB, db *sqlite.DB, value task.Task, session string, work task.WorkSpec, verification task.VerificationSpec) review.Review {
	t.Helper()
	if work.ImplementationMode == "" {
		work.ImplementationMode = task.ImplementationCodingAgent
	}
	ctx := context.Background()
	if verification.Mode == task.VerificationModeNone {
		// Review proposes observable verification; it cannot grant a user waiver.
		verification = task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "fixture", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"result matches intent"}, CodexInstruction: "Run the fixture tests read-only"}
	}
	now := time.Now().UTC()
	reviews, _ := review.NewService(sqlite.NewReviewStore(db), nil, func() time.Time { return now })
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	config, err := sqlite.NewCodexConfigStore(db).Get(ctx, value.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("workflow-test")
	model, effort := value.Model, value.ReasoningEffort
	if model == "" {
		model = "gpt-5.6-terra"
	}
	if effort == "" {
		effort = "high"
	}
	v := review.Review{ReviewDepth: task.ReviewDepthDeep, TaskID: value.ID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, CreatedByAISessionID: session, AuthorKind: string(review.AuthorAISession), AuthorID: session, RepositoryID: value.RepositoryID, ExecutorKind: review.ExecutorCodingAgent, CodexConfigID: value.CodexConfigID, BackendSelection: &selection, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: model, ReasoningEffort: effort}, BaseCommit: value.BaseCommit, Model: model, ReasoningEffort: effort, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: value.ProblemStatement, Instruction: "Synthetic read-only review", RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: value.RepositoryID, Commit: value.BaseCommit, RepositoryHEAD: value.BaseCommit, BranchKind: review.ProvenanceBranch, BranchRef: "refs/heads/dev", InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: now}}
	if v.EngineeringGoal == "" {
		v.EngineeringGoal = value.Goal
	}
	admission, err := reviews.AdmitStart(ctx, v, "review-"+value.ID, strings.Repeat("a", 64), v.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: "Synthetic approved intent", WorkSpec: work, VerificationSpec: verification})
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqlite.NewRunStore(db).Update(ctx, admission.Run.ID, func(r *run.Run) error {
		r.Status = run.StatusSucceeded
		r.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: string(raw)}}
		r.UpdatedAt = now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return admission.Review
}

func Approve(t testing.TB, db *sqlite.DB, value task.Task, session string, work task.WorkSpec, verification task.VerificationSpec) task.Task {
	t.Helper()
	source := ReviewGate(t, db, value, session, work, verification)
	runtimeConfig := source.RuntimeConfig
	result, err := sqlite.NewWorkflowProgressionStore(db).Resolve(context.Background(), wp.ResolveInput{AISessionID: session, TaskID: value.ID, DecisionID: wp.DecisionID(source.ID), RequestID: "approve-" + value.ID, Option: "APPROVE", BackendSelection: source.BackendSelection, NextRunRuntimeConfig: &runtimeConfig})
	if err != nil {
		t.Fatal(err)
	}
	return *result.Task
}

func ApproveAndReserve(t testing.TB, db *sqlite.DB, value task.Task, session string, work task.WorkSpec, verification task.VerificationSpec) (task.Task, executionproposal.Proposal, run.Run) {
	t.Helper()
	value = Approve(t, db, value, session, work, verification)
	proposal, reserved, err := sqlite.NewWorkflowProgressionStore(db).ReserveTaskExecution(context.Background(), session, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Status == executionproposal.StatusAdmitted && reserved.ID == "" {
		proposal, reserved, err = sqlite.NewExecutionProposalStore(db).PrepareAdmittedExecution(context.Background(), proposal.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	value, err = sqlite.NewTaskStore(db).Get(context.Background(), value.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value, proposal, reserved
}
