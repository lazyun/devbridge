package simulation

import (
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestReviewPredecessorDoesNotAliasSuccessor(t *testing.T) {
	f := &fixture{review: "old-review", reviewInstruction: "exact authored bytes\n"}
	f.access = review.UpperAccess{RepositoryTargets: []workset.Target{{RepositoryID: "old-repo"}}, AllowedPaths: []string{"old-path"}}
	f.upperClaim = review.WorkerClaimInput{WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "old-claim"}}
	f.upperFirst = map[string]any{"receipt": map[string]any{"claimId": "old-id"}}
	f.report = review.SubmitInput{WorkerClaim: &review.WorkerClaimIdentity{RequestID: "old-claim"}, Report: review.ReviewReport{EvidenceRefs: []review.EvidenceRef{{EvidenceID: "old-evidence"}}, RecommendedNextSteps: []string{"old-step"}}}
	old := f.reviewPredecessor()
	before := encoded(old.access) + encoded(old.upperClaim) + encoded(old.upperFirst) + encoded(old.report)
	f.review = "new-review"
	f.reviewInstruction = "new instruction"
	f.access.RepositoryTargets[0].RepositoryID = "new-repo"
	f.access.AllowedPaths[0] = "new-path"
	f.upperClaim.RequestID = "new-claim"
	f.upperFirst["receipt"].(map[string]any)["claimId"] = "new-id"
	f.report.WorkerClaim.RequestID = "new-claim"
	f.report.Report.EvidenceRefs[0].EvidenceID = "new-evidence"
	f.report.Report.RecommendedNextSteps[0] = "new-step"
	after := encoded(old.access) + encoded(old.upperClaim) + encoded(old.upperFirst) + encoded(old.report)
	if before != after || old.review != "old-review" || old.reviewInstruction != "exact authored bytes\n" {
		t.Fatal("successor mutation rewrote immutable predecessor oracle")
	}
}
