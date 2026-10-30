package sqlite

import (
	"context"
	"strings"
	"testing"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestReviewMaterializationRejectsV1VerificationAuthority(t *testing.T) {
	for _, field := range []string{`"workingDirectory":"other"`, `"RepositoryId":"other"`, `"RepositoryIds":["other"]`} {
		t.Run(field, func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			a := f.start(t)
			raw := strings.Replace(approvedResult(t), `"kind":"TEST_COMMAND"`, `"kind":"TEST_COMMAND",`+field, 1)
			f.complete(t, a, raw)
			before := scanMaterializationClaim(t, f.db, a.Review.ID)
			if before.state != "FINALIZED" || before.outcome != "INVALID" || before.rawSHA != workflowHash(raw) || before.decisionID != wp.InvalidResultDecisionID(a.Review.ID) {
				t.Fatalf("v1 target authority was materialized: %+v", before)
			}
			if err := f.store.Reconcile(context.Background()); err != nil {
				t.Fatal(err)
			}
			if after := scanMaterializationClaim(t, f.db, a.Review.ID); after != before {
				t.Fatalf("invalid claim changed: %+v", after)
			}
			if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE decision_id=?`, wp.DecisionID(a.Review.ID)); n != 0 {
				t.Fatalf("invalid result generated approval decision: %d", n)
			}
			if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=? AND outcome='INVALID' AND formal_result_json IS NULL`, a.Review.ID); n != 1 {
				t.Fatalf("invalid materializations=%d", n)
			}
		})
	}
}
