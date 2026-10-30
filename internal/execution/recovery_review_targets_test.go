package execution

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRecoveryReviewPreservesAdmittedTargetSet(t *testing.T) {
	for _, kind := range []string{"exact", "reordered", "proposed-write-scope", "missing-member", "origin-ref", "observed-commit", "development-cycle"} {
		t.Run(kind, func(t *testing.T) {
			verification, _, scope, _ := qualifiedVerificationFixture(t)
			w := scope.workset
			taskID := w.TaskID
			w.TaskID, w.ExecutionID, w.TaskDefinitionRevisionID, w.ReviewID = "", "", "", "review"
			var err error
			w, err = w.WithHash()
			if err != nil {
				t.Fatal(err)
			}
			var admitted []workset.Target
			for _, member := range w.Members {
				admitted = append(admitted, workset.Target{RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: member.MemberVersion, OriginCommit: member.OriginCommit, OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle", ObservedCommit: member.StartHEAD, Read: true, Write: workset.WriteScope{Kind: "NONE"}})
			}
			proposed := workset.CloneTargets(admitted)
			proposed[0].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"file.txt"}}
			switch kind {
			case "reordered":
				proposed[0], proposed[1] = proposed[1], proposed[0]
			case "proposed-write-scope":
				proposed[0].Write = workset.WriteScope{Kind: "REPOSITORY"}
			case "missing-member":
				proposed = proposed[:1]
				verification.Checks = verification.Checks[:1]
			case "origin-ref":
				proposed[0].OriginRef = "refs/heads/foreign"
			case "observed-commit":
				proposed[0].ObservedCommit = strings.Repeat("f", 40)
			case "development-cycle":
				proposed[0].DevelopmentCycleID = "foreign-cycle"
			}
			work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update file", Constraints: []string{"Keep existing behavior"}, CodexInstruction: "Update file.txt", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
			rr := &review.ReviewReport{SchemaVersion: 2, Summary: "Ready", Disposition: review.DispositionApprovalReady, RepositoryTargets: proposed, PrimaryMemberID: w.PrimaryMemberID, Coverage: []string{"source"}, Findings: []review.Finding{}, Limitations: []string{"No implementation yet"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}, WorkSpec: &work, VerificationSpec: &verification}
			if err := rr.Validate(); err != nil {
				t.Fatalf("structural fixture: %v", err)
			}
			source := run.Run{ID: w.RunID, TaskID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, ReviewID: "review", OperationKind: run.OperationReview, OperationID: "review", Purpose: run.PurposeScopeReview, TurnID: "turn", RuntimeWorkset: &w}
			c := &Coordinator{Tasks: &fakeTasks{value: task.Task{ID: taskID, ProjectID: w.ProjectID, FeatureAreaID: w.FeatureAreaID, CurrentReviewID: "review"}}, Reviews: &coordinatorReviewRuntime{value: review.Review{ID: "review", TaskID: taskID, RunID: source.ID, ExecutorKind: review.ExecutorCodingAgent, RepositoryTargets: admitted}}}
			facts := recoverystatus.FactualObservation{Source: recoverystatus.Source{RunID: source.ID, OperationID: source.OperationID, TurnID: source.TurnID, RuntimeWorksetHash: w.ContentHash}}
			if err := c.ValidateRecoveryOwnerFacts(context.Background(), source, recoverystatus.Report{ReviewReport: rr}, &facts); err != nil {
				t.Fatal(err)
			}
			valid := kind == "exact" || kind == "reordered" || kind == "proposed-write-scope"
			if facts.OwnerContractValidated != valid {
				t.Fatalf("target identity accepted=%v want=%v", facts.OwnerContractValidated, valid)
			}
			if !valid && (len(facts.OwnerResult) > 0 || facts.OwnerContractEvidenceHash != "") {
				t.Fatal("unbound targets acquired formal result evidence")
			}
		})
	}
}
