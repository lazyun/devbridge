package controlapi_test

import (
	"testing"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Exercise the same operator read/command pair used by WebUI, rather than the
// Session facade (which previously advertised the action while WebUI hid it).
func TestWebContinuationExposesAndConsumesExactOperatorReviewRequest(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		owner := awaitAcceptanceRecoveryFixture(t, f, executionID)
		reader, err := consoleapi.New(f.plane, f.facade)
		if err != nil {
			t.Fatal(err)
		}
		ctx := operatorActionContext(t, f.project, f.task, "web-continuation", "CONTINUE_TASK")
		page, err := reader.GetProductTask(ctx, f.project, f.task)
		if err != nil {
			t.Fatal(err)
		}
		var target *p.ActionTarget
		for _, a := range page.OperatorActions {
			if a.Action == "CONTINUE_TASK" {
				target = a.Target
			}
		}
		if target == nil {
			t.Fatal("web continuation target missing")
		}
		if _, err = reader.ResolveTaskAction(ctx, f.project, f.task, consoleapi.TaskActionInput{Action: "CONTINUE_TASK", RequestID: "web-continue", Target: target}); err != nil {
			t.Fatal(err)
		}
		ctx = operatorActionContext(t, f.project, f.task, "web-continuation", wp.ActionRequestTaskReview)
		page, err = reader.GetProductTask(ctx, f.project, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if page.Presentation.TaskView.VerificationDecisions == nil || !page.Presentation.TaskView.VerificationDecisions.ContinuationConfirmed {
			t.Fatal("continuation readback missing")
		}
		target = nil
		for _, a := range page.OperatorActions {
			if a.Action == wp.ActionRequestTaskReview {
				if !a.ChannelAvailable || !a.DomainAvailable {
					rows, _ := f.plane.DB.SQL().Query(`SELECT r.id,r.purpose,r.status,r.runtime_epoch,r.workspace_id,b.advance_state,i.applied_at FROM runs r LEFT JOIN agent_operation_boundaries b ON b.run_id=r.id LEFT JOIN agent_finalization_intents i ON i.run_id=r.id WHERE r.execution_proposal_id=?`, executionID)
					if rows != nil {
						defer rows.Close()
						for rows.Next() {
							var id, purpose, status string
							var epoch, workspace, advance, applied any
							_ = rows.Scan(&id, &purpose, &status, &epoch, &workspace, &advance, &applied)
							t.Log(id, purpose, status, epoch, workspace, advance, applied)
						}
					}
					t.Fatalf("web supplemental Review blocked: %+v", a)
				}
				target = a.Target
			}
		}
		if target == nil {
			t.Fatal("web supplemental Review target missing")
		}
		var runsBefore int
		if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runsBefore); err != nil {
			t.Fatal(err)
		}
		input := consoleapi.TaskActionInput{Action: wp.ActionRequestTaskReview, RequestID: "web-supplemental", Target: target, ReviewPurpose: "Revise the implementation instructions in the same Task."}
		stale := *target.ExpectedReviewRequest
		stale.CurrentExecutionID = "wrong-execution"
		wrongTarget := *target
		wrongTarget.ExpectedReviewRequest = &stale
		wrong := input
		wrong.Target = &wrongTarget
		wrong.RequestID = "web-stale"
		if _, err = reader.ResolveTaskAction(ctx, f.project, f.task, wrong); err == nil {
			t.Fatal("stale request accepted")
		}
		for i := 0; i < 2; i++ {
			if _, err = reader.ResolveTaskAction(ctx, f.project, f.task, input); err != nil {
				t.Fatalf("submit/replay %d: %v", i, err)
			}
		}
		current, err := f.plane.Tasks.Get(f.ctx, f.task)
		if err != nil || current.ID != owner.ID || current.Status != task.StatusActive || current.Phase != task.PhaseReviewRequired {
			t.Fatalf("same Task did not advance: %+v %v", current, err)
		}
		var requests, runsAfter int
		if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM task_supplemental_review_requests WHERE task_id=? AND actor_kind='LOCAL_OPERATOR'`, f.task).Scan(&requests); err != nil {
			t.Fatal(err)
		}
		if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&runsAfter); err != nil {
			t.Fatal(err)
		}
		if requests != 1 || runsAfter != runsBefore {
			t.Fatalf("request replay created extra work: requests=%d runs=%d/%d", requests, runsBefore, runsAfter)
		}
		page, err = reader.GetProductTask(ctx, f.project, f.task)
		if err != nil {
			t.Fatal(err)
		}
		var start bool
		for _, a := range page.OperatorActions {
			if a.Action == "START_TASK_REVIEW" && a.ChannelAvailable && a.DomainAvailable {
				start = true
			}
		}
		if !start {
			t.Fatal("web cannot proceed to supplemental Review configuration")
		}
		return false
	})
}
