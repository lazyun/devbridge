package controlapi_test

import (
	"github.com/lazyun/devbridge/internal/agentconversation"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"testing"
)

func TestReReviewScalarSuccessorExplicitPreparation(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		p, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		out, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "v1-commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || out.State != "COMPLETED" {
			t.Fatalf("commit %s: %+v %v", in.Action, out, err)
		}
		return out
	}
	area := commit(c.PrepareInput{RequestID: "v1-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Legacy compatibility", ProblemStatement: "Preserve the single-repository Upper Review protocol"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	areaID := area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "v1-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, RepositoryID: f.repos["app"], Title: "Legacy app review", ProblemStatement: "Review app source", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	taskID := created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "v1-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryID: f.repos["app"], ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt"}}})
	reviewID, _ := started.Steps[0].ReviewID, started.Steps[0].RunID
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, reviewID)
	if err != nil || access.State != "ACTIVE" {
		t.Fatalf("access=%+v %v", access, err)
	}
	read, err := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &access.Target, Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "legacy app update", Constraints: []string{"bounded"}, CodexInstruction: "update app", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "app repository", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true}}, AcceptanceCriteria: []string{"same.txt remains present"}, CodexInstruction: "Verify the app repository"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "v1-report", Report: review.ReviewReport{SchemaVersion: 1, Summary: "legacy report", Disposition: review.DispositionApprovalReady, Coverage: []string{"same.txt"}, Findings: []review.Finding{}, Limitations: []string{"read only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: "same.txt", Digest: read.Digest}}, WorkSpec: &work, VerificationSpec: &verification}}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, submission)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.SubmitReviewResult(f.ctx, submission); err != nil || replay.ReportID != receipt.ReportID {
		t.Fatalf("report replay=%+v %v", replay, err)
	}
	revised := commit(c.PrepareInput{RequestID: "v1-revise", Purpose: c.Review, Action: "REVISE", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, ReviewID: reviewID, DecisionID: wp.DecisionID(reviewID), ReviewFeedback: "Keep the exact scope and required command verification"}, Variants: []c.Variant{{ID: "revise", Summary: "Keep the review grant and request revision"}}})
	successor := revised.Steps[0].ReviewID
	next, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, successor)
	if err != nil || next.ProtocolVersion != 1 || next.ForbiddenPaths == nil {
		t.Fatalf("scalar successor %+v %v", next, err)
	}
	prepared, err := f.plane.Coordinator.PrepareUpperReview(f.ctx, next.Target)
	if err != nil || prepared.State != "ACTIVE" || prepared.Target != next.Target {
		t.Fatalf("scalar explicit prepare %+v %v", prepared, err)
	}
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{FeatureAreaID: areaID, RunID: next.Target.RunID, OperationKind: agentconversation.Review, OperationID: successor, RequestID: "scalar-recover", ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: next.Target.RuntimeEpoch, GrantGeneration: next.Target.GrantGeneration}})
	if err != nil {
		t.Fatal(err)
	}
	target := next.Target
	target.RuntimeEpoch = recovered.AuthorizedUpperVersion.GrantEpoch
	target.GrantGeneration = recovered.AuthorizedUpperVersion.GrantGeneration
	if _, err = f.facade.PrepareUpperReview(f.ctx, target); err != nil {
		t.Fatal(err)
	}
	read, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &target, Path: "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	submission.UpperTarget = target
	submission.RequestID = "scalar-recovered-report"
	submission.Report.EvidenceRefs = []review.EvidenceRef{{EvidenceID: read.EvidenceID, Path: read.Path, Digest: read.Digest}}
	if _, err = f.facade.SubmitReviewResult(f.ctx, submission); err != nil {
		t.Fatal(err)
	}
	g, err := f.facade.WorkflowGuidanceForResource(f.ctx, wp.ResourceScope{AISessionID: f.session, ProjectID: f.project, TaskID: taskID})
	if err != nil || g.Approval == nil || g.Approval.Status != "PENDING" {
		t.Fatalf("scalar recovered approval %+v %v", g, err)
	}
}
