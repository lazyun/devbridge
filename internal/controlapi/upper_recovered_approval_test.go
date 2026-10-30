package controlapi_test

import (
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"strings"
	"testing"
)

func TestRecoveredUpperReviewCanReachApprovalWithoutErasingUncertainty(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	reopen := func() {
		t.Helper()
		if err := f.plane.Close(); err != nil {
			t.Fatal(err)
		}
		f.plane = nil
		var err error
		f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
		if err != nil {
			t.Fatal(err)
		}
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
	}
	old := f.access.Target
	reopen()
	projection, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Recovery == nil || projection.Recovery.Reason != "GPT_REVIEW_AUTHORIZATION_REQUIRED" || projection.TaskView == nil || projection.TaskView.RunRecovery == nil || projection.TaskView.RunRecovery.Label != "GPT 审查待重新授权" || projection.TaskView.RunRecovery.Required || projection.TaskView.NextStep != nil || strings.Contains(projection.TaskView.Activity.Summary, "Codex") {
		t.Fatalf("GPT authorization mislabeled as lost result: %+v", projection)
	}
	reader, err := consoleapi.New(f.plane, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	operatorCtx := operatorActionContext(t, f.project, f.task, "upper-auth-read", "VIEW_DETAILS")
	detail, err := reader.GetRun(operatorCtx, f.project, f.run)
	if err != nil || !detail.Run.ReviewAuthorizationRequired {
		t.Fatalf("run did not reflect interrupted authorization: %+v %v", detail.Run, err)
	}
	page, err := reader.ListRuns(operatorCtx, f.project, consoleapi.RunListFilter{Scope: "current"})
	if err != nil || len(page.Items) != 1 || !page.Items[0].ReviewAuthorizationRequired {
		t.Fatalf("list disagrees with detail: %+v %v", page, err)
	}
	expected := &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: old.RuntimeEpoch, GrantGeneration: old.GrantGeneration}
	recovered, err := f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{ExpectedUpperVersion: expected, FeatureAreaID: f.area, RunID: f.run, OperationKind: agentconversation.Review, OperationID: f.review, RequestID: "recover-before-approval"})
	if err != nil {
		t.Fatal(err)
	}
	target := old
	target.RuntimeEpoch = recovered.AuthorizedUpperVersion.GrantEpoch
	target.GrantGeneration = recovered.AuthorizedUpperVersion.GrantGeneration
	f.access, err = f.facade.PrepareUpperReview(f.ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	guidance := func() wp.Guidance {
		t.Helper()
		g, e := f.facade.WorkflowGuidanceForResource(f.ctx, wp.ResourceScope{AISessionID: f.session, ProjectID: f.project, TaskID: f.task})
		if e != nil {
			t.Fatal(e)
		}
		return g
	}
	detail, err = reader.GetRun(operatorCtx, f.project, f.run)
	if err != nil || detail.Run.ReviewAuthorizationRequired {
		t.Fatalf("renewed authorization remains stale: %+v %v", detail.Run, err)
	}
	if g := guidance(); g.CurrentState == "RECOVERY_REQUIRED" {
		t.Fatalf("active exact recovery remains blocked: %+v", g)
	}
	assertOperation := func(want string) {
		t.Helper()
		values, e := f.facade.ListAgentExecutionsForSession(f.ctx, f.session)
		if e != nil {
			t.Fatal(e)
		}
		for _, value := range values {
			if value.SemanticOwnerID == f.review {
				if value.Status != want || (want != "UNKNOWN" && value.RecoveryRequired) {
					t.Fatalf("Core operation disagrees with current Upper Review: %+v", value)
				}
				return
			}
		}
		t.Fatal("exact Review operation missing")
	}
	assertOperation("RUNNING")
	submitLifecycleApprovalReady(t, f, "recovered-approval-report")
	assertOperation("COMPLETED")
	assertPending := func() {
		t.Helper()
		g := guidance()
		if g.Approval == nil || g.Approval.Status != "PENDING" || g.Approval.DecisionID != wp.DecisionID(f.review) {
			t.Fatalf("completed report cannot reach approval: %+v", g)
		}
	}
	assertPending()
	var classification string
	if err = f.plane.DB.SQL().QueryRow(`SELECT classification FROM agent_operation_recovery_states WHERE run_id=?`, f.run).Scan(&classification); err != nil || classification != "UNKNOWN" {
		t.Fatalf("historical classification rewritten: %s %v", classification, err)
	}
	reopen()
	assertPending()
	assertOperation("COMPLETED")
	for _, classification := range []string{"CONFLICT", "UNKNOWN"} {
		if _, err = f.plane.DB.SQL().Exec(`UPDATE agent_operation_recovery_states SET classification=?,detail='independent unresolved evidence' WHERE run_id=?`, classification, f.run); err != nil {
			t.Fatal(err)
		}
		assertOperation("UNKNOWN")
		g := guidance()
		if g.CurrentState != "RECOVERY_REQUIRED" || g.Approval != nil {
			t.Fatalf("independent %s uncertainty bypassed: %+v", classification, g)
		}
	}
}
