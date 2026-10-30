package mcpserver

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/projectcontext"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowstate"
)

func TestProductWorkflowStateVocabularyAndCapabilities(t *testing.T) {
	for _, entry := range []struct {
		phase           task.Phase
		action          string
		review, execute bool
	}{{task.PhaseReviewRequired, "START_TASK_REVIEW", true, false}, {task.PhaseReviewing, "WAIT", false, false}, {task.PhaseReviewApprovalPending, "RESOLVE_REVIEW_APPROVAL", false, false}, {task.PhaseImplementationReady, "START_EXECUTION", false, true}, {task.PhaseImplementing, "WAIT", false, false}, {task.PhaseVerificationReady, "START_VERIFICATION", false, true}, {task.PhaseVerifying, "WAIT", false, false}, {task.PhaseAcceptancePending, "RESOLVE_TASK_ACCEPTANCE", false, false}} {
		g := wp.Guidance{TaskID: "task", TaskPhase: entry.phase, CurrentState: string(entry.phase), NextAction: wp.Action{Code: entry.action}, CurrentTask: &wp.TaskFact{ID: "task", Status: "ACTIVE", Phase: entry.phase}}
		current := controlapi.CurrentWorkflow{Guidance: &g, AgentExecution: &agentexecution.Projection{Status: "FAILED"}}
		p := workflowProjection{featureArea: &featurearea.FeatureArea{ID: "area", Status: featurearea.StatusActive}, proposal: &executionproposal.Proposal{Status: executionproposal.StatusPendingConfirmation}, review: &run.Run{Status: run.StatusSucceeded}}
		applyProductWorkflow(&p, current, workflowstate.Input{}, usableContext())
		if p.output.State != string(entry.phase) || p.output.Capabilities.CanStartReview != entry.review || p.output.Capabilities.CanExecute != entry.execute {
			t.Fatalf("child-derived workflow: %+v", p.output)
		}
		applyProductWorkflow(&p, current, workflowstate.Input{}, projectcontext.ProjectContext{Status: projectcontext.StatusRunning})
		if p.output.Capabilities.CanStartReview || p.output.Capabilities.CanExecute {
			t.Fatal("unusable context granted operation")
		}
	}
}

func TestReviewEvidenceSummaryDerivesOnlyFromDurableFacts(t *testing.T) {
	now := time.Now().UTC()
	dirty := true
	value := productreview.Review{ReviewDepth: "DEEP", ID: "review_evidence", StartRequestID: "request-evidence", ProjectID: "project-a", FeatureAreaID: "feature-area-a", RepositoryID: "repository-a", RunID: "run-a", Instruction: "Inspect read-only.", CreatedAt: now, Focus: productreview.Focus{Kind: productreview.FocusRepository}, RepositoryProvenance: &productreview.RepositoryProvenance{RepositoryID: "repository-a", Commit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", RepositoryHEAD: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", BranchKind: productreview.ProvenanceBranch, BranchRef: "refs/heads/topic", WorkingTreeDirtyAtAdmission: &dirty, InspectionMode: productreview.InspectionFeatureAreaWorkspace, CapturedAt: now}}
	featureAreaValue := featurearea.FeatureArea{ID: value.FeatureAreaID, Title: "Evidence"}
	runValue := run.Run{ID: value.RunID, Status: run.StatusSucceeded, FinishedAt: &now, Result: &run.Result{AgentClaim: evidence.AgentClaim{Message: "Engineering findings exist."}}, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsageUnavailable}}

	withoutObservedInspection := externalReviewFor(value, featureAreaValue, &runValue, productreview.EvidenceSummary{AuthorizationGranted: true})
	if !withoutObservedInspection.Review.ResultAvailable || withoutObservedInspection.Result == nil || withoutObservedInspection.ObservedEvidence.AuthorizationStatus != "GRANTED" || withoutObservedInspection.ObservedEvidence.RepositoryInspectionObserved || withoutObservedInspection.ObservedEvidence.SuccessfulHostToolOperations != 0 {
		t.Fatalf("zero Host Tool evidence boundary = %+v", withoutObservedInspection)
	}
	if strings.Contains(withoutObservedInspection.Review.Summary, "inspection evidence") || strings.Contains(withoutObservedInspection.Review.Summary, "inspection was observed") {
		t.Fatalf("summary fabricated observed inspection: %q", withoutObservedInspection.Review.Summary)
	}
	if withoutObservedInspection.RepositoryProvenance == nil || withoutObservedInspection.RepositoryProvenance.BranchRef != "refs/heads/topic" || withoutObservedInspection.RepositoryProvenance.WorkingTreeDirtyAtAdmission == nil || !*withoutObservedInspection.RepositoryProvenance.WorkingTreeDirtyAtAdmission {
		t.Fatalf("immutable provenance = %+v", withoutObservedInspection.RepositoryProvenance)
	}
	withoutGrant := externalReviewFor(value, featureAreaValue, &runValue, productreview.EvidenceSummary{})
	if withoutGrant.ObservedEvidence.AuthorizationStatus != "UNKNOWN" || withoutGrant.ObservedEvidence.RepositoryInspectionObserved {
		t.Fatalf("missing durable grant fabricated authorization/evidence: %+v", withoutGrant.ObservedEvidence)
	}

	observed := externalReviewFor(value, featureAreaValue, &runValue, productreview.EvidenceSummary{AuthorizationGranted: true, Successes: 2})
	if !observed.ObservedEvidence.RepositoryInspectionObserved || observed.ObservedEvidence.SuccessfulHostToolOperations != 2 || !strings.Contains(observed.Review.Summary, "Repository inspection was observed") {
		t.Fatalf("observed Host Tool evidence = %+v", observed)
	}
}

func usableContext() projectcontext.ProjectContext {
	return projectcontext.ProjectContext{Status: projectcontext.StatusReady, ContextFingerprint: "sha256:fixture"}
}
