package controlapi_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

type interleavedApprovedStartPrepare struct {
	c.Store
	before func()
}

func (s *interleavedApprovedStartPrepare) Prepare(ctx context.Context, project string, in c.PrepareInput) (c.Intent, error) {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	return s.Store.Prepare(ctx, project, in)
}

// The competing task uses real public Task/Review/approval producers in the
// same Session. Its different FeatureArea keeps runtime ownership independent.
func secondApprovedStartDefinition(t *testing.T, f *upperLifecycleFixture) *upperLifecycleFixture {
	t.Helper()
	g := *f
	g.targets = append(g.targets[:0:0], g.targets...)
	for i := range g.targets {
		g.targets[i].MemberID = ""
		g.targets[i].MemberVersion = 0
	}
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = g.session
		prepared, err := g.facade.PrepareConfirmation(g.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		value, err := g.facade.CommitConfirmation(g.ctx, c.CommitInput{AISessionID: g.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || value.State != "COMPLETED" {
			t.Fatalf("%s: %+v %v", in.Action, value, err)
		}
		return value
	}
	area := commit(c.PrepareInput{RequestID: "second-start-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Second start area", ProblemStatement: "Verify exact start ownership"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	g.area = area.Steps[0].FeatureAreaID
	owner := commit(c.PrepareInput{RequestID: "second-start-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: g.area, Title: "Second start task", ProblemStatement: "Inspect exact repositories", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	g.task = owner.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "second-start-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: g.task, FeatureAreaID: g.area, RepositoryTargets: g.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from app and sdk"}}})
	g.review, g.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	var err error
	g.access, err = g.facade.UpperReviewAccessForSession(g.ctx, g.session, g.review)
	if err != nil {
		t.Fatal(err)
	}
	value, err := g.plane.Reviews.Get(g.ctx, g.review)
	if err != nil {
		t.Fatal(err)
	}
	g.targets = value.RepositoryTargets
	_, approve := prepareDefinitionFormalApproval(t, &g, "second-start-approval")
	if value, err := g.facade.CommitConfirmation(g.ctx, approve); err != nil || value.State != "COMPLETED" {
		t.Fatalf("second approval: %+v %v", value, err)
	}
	return &g
}

func TestApprovedTaskStartRequestExclusiveAcrossAdmissionAndComposition(t *testing.T) {
	for _, admittedFirst := range []bool{true, false} {
		name := "composition-wins"
		if admittedFirst {
			name = "admitted-consumer-wins-after-composer-check"
		}
		t.Run(name, func(t *testing.T) {
			f, _, _, admitted := completedAdoptionStartFixture(t)
			g := secondApprovedStartDefinition(t, f)
			expected, err := g.facade.TaskExecutionTargetForSession(g.ctx, g.session, g.task)
			if err != nil {
				t.Fatal(err)
			}
			composed := admitted
			composed.TaskID = g.task
			composed.ExpectedTarget = expected
			if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER exclusive_start_run_fault BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'stop before Run'); END`); err != nil {
				t.Fatal(err)
			}
			var original executionproposal.StartResult
			var originalErr error
			var before map[string]int
			if admittedFirst {
				// The composer has already checked both replay paths and validated its
				// Task when the other caller commits the admitted-consumer request.
				wrapper := &interleavedApprovedStartPrepare{Store: f.plane.Confirmations}
				wrapper.before = func() {
					original, originalErr = f.facade.StartApprovedTaskExecution(f.ctx, admitted)
					before = formalExecutionCounts(t, f)
				}
				g.facade = controlapi.WithApprovedStartConfirmationStore(g.facade, wrapper)
				denied, deniedErr := g.facade.StartApprovedTaskExecution(g.ctx, composed)
				if originalErr == nil || original.Proposal.ID != admitted.ExpectedTarget.ExecutionID {
					t.Fatalf("original admitted consumer: %+v %v", original, originalErr)
				}
				if deniedErr == nil || !strings.Contains(deniedErr.Error(), "CONFIRMATION_REQUEST_CONFLICT") || denied.Receipt != nil || denied.Proposal.ID != "" || denied.Run.ID != "" {
					t.Fatalf("different Task reused admitted request: %+v %v", denied, deniedErr)
				}
			} else {
				original, originalErr = g.facade.StartApprovedTaskExecution(g.ctx, composed)
				if originalErr == nil || original.Proposal.ID == "" || original.Proposal.TaskID != g.task {
					t.Fatalf("original composed start: %+v %v", original, originalErr)
				}
				before = formalExecutionCounts(t, f)
				denied, deniedErr := f.facade.StartApprovedTaskExecution(f.ctx, admitted)
				if deniedErr == nil || !strings.Contains(deniedErr.Error(), "CONFIRMATION_REQUEST_CONFLICT") || denied.Receipt != nil || denied.Proposal.ID != "" || denied.Run.ID != "" {
					t.Fatalf("different Task reused composed request: %+v %v", denied, deniedErr)
				}
			}
			if after := formalExecutionCounts(t, f); !reflect.DeepEqual(before, after) {
				t.Fatalf("rejected competing request changed facts: before=%v after=%v", before, after)
			}
			var consumers, confirmations int
			key := c.ValueHash([]string{"devbridge.approved-task-start.v1", admitted.AISessionID, admitted.RequestID})
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM ai_admitted_execution_start_requests WHERE ai_session_id=? AND request_id=?`, admitted.AISessionID, admitted.RequestID).Scan(&consumers); err != nil {
				t.Fatal(err)
			}
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM confirmation_intents WHERE ai_session_id=? AND prepare_request_id=?`, admitted.AISessionID, "approved_task_start_prepare_"+key).Scan(&confirmations); err != nil {
				t.Fatal(err)
			}
			if consumers+confirmations != 1 || admittedFirst && consumers != 1 || !admittedFirst && confirmations != 1 {
				t.Fatalf("split request identity: admitted=%d composed=%d", consumers, confirmations)
			}
			input := composed
			if admittedFirst {
				input = admitted
			}
			replay, replayErr := f.facade.StartApprovedTaskExecution(f.ctx, input)
			if replayErr == nil || strings.Contains(replayErr.Error(), "CONFIRMATION_REQUEST_CONFLICT") || replay.Proposal.ID != original.Proposal.ID {
				t.Fatalf("competing request shadowed original replay: %+v %v", replay, replayErr)
			}
		})
	}
}

func TestApprovedTaskStartColdUnacceptedRunReportsHandoffFailure(t *testing.T) {
	f, _, _, input := completedAdoptionStartFixture(t)
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER cold_start_before_run BEFORE INSERT ON runs WHEN NEW.execution_proposal_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'pause before Run'); END`); err != nil {
		t.Fatal(err)
	}
	if got, err := f.facade.StartApprovedTaskExecution(f.ctx, input); err == nil || got.Proposal.ID == "" {
		t.Fatalf("explicit consumer before crash: %+v %v", got, err)
	}
	if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER cold_start_before_run`); err != nil {
		t.Fatal(err)
	}
	// Stop at the real durable boundary between Core Run creation and dispatch.
	_, created, err := sqlite.NewExecutionProposalStore(f.plane.DB).PrepareAdmittedExecution(f.ctx, input.ExpectedTarget.ExecutionID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	handoff, err := sqlite.NewExecutionProposalStore(f.plane.DB).ReadHandoff(f.ctx, f.project, created.ID)
	if err != nil || handoff.WasAccepted() || handoff.State != executionproposal.HandoffRegistered {
		t.Fatalf("before restart: %+v %v", handoff, err)
	}
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	f.plane.Retirement.Close()
	before := formalExecutionCounts(t, f)
	got, err := f.facade.StartApprovedTaskExecution(f.ctx, input)
	var failure *executionproposal.HandoffError
	if !errors.As(err, &failure) || got.Run.ID != created.ID || got.Handoff.WasAccepted() || got.Handoff.RunEpochCurrent || got.Handoff.Code != "HANDOFF_EPOCH_NOT_CURRENT" {
		t.Fatalf("unaccepted cold replay: %+v %v", got, err)
	}
	if got.Receipt == nil || got.Receipt.OriginalRunID != created.ID || got.Receipt.OriginalExecutionID != input.ExpectedTarget.ExecutionID {
		t.Fatalf("cold failure lost original receipt: %+v", got.Receipt)
	}
	if after := formalExecutionCounts(t, f); !reflect.DeepEqual(before, after) {
		t.Fatalf("cold failed start re-dispatched or changed facts: before=%v after=%v", before, after)
	}
}
