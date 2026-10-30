package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type claimRowFacts struct {
	found            bool
	state            string
	claimToken       string
	generation       int
	rawSHA           string
	worksetSHA       string
	provenanceSHA    string
	outcome          string
	decisionID       string
	created, updated string
	finalizedAt      string
}

func scanMaterializationClaim(t *testing.T, db *DB, reviewID string) claimRowFacts {
	t.Helper()
	var v claimRowFacts
	err := db.sql.QueryRow(`SELECT state,claim_token,generation,raw_result_sha256,runtime_workset_hash,provenance_sha256,COALESCE(outcome,''),COALESCE(decision_id,''),COALESCE(finalized_at,''),created_at,updated_at FROM review_result_materialization_claims WHERE review_id=?`, reviewID).
		Scan(&v.state, &v.claimToken, &v.generation, &v.rawSHA, &v.worksetSHA, &v.provenanceSHA, &v.outcome, &v.decisionID, &v.finalizedAt, &v.created, &v.updated)
	if err != nil {
		return v
	}
	v.found = true
	return v
}

func countRows(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// G1: crash after the durable claim is committed. The claim must survive as
// MATERIALIZING with no formal result, then resume with the same token and
// terminalize exactly once together with the single Decision.
func TestReviewMaterializationDurableClaimSurvivesCrashAndResumes(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	raw := approvedResult(t)
	f.db.SetReviewMaterializationFaultHookForTest(func(reviewID, stage string) error {
		if stage == "AFTER_CLAIM" {
			return errors.New("injected crash after durable claim")
		}
		return nil
	})
	f.complete(t, a, raw)
	claim := scanMaterializationClaim(t, f.db, a.Review.ID)
	if !claim.found {
		t.Fatal("Run success did not leave a durable MATERIALIZING claim")
	}
	if claim.state != "MATERIALIZING" || claim.outcome != "" || claim.decisionID != "" || claim.finalizedAt != "" {
		t.Fatalf("claim did not survive the crash as open: %+v", claim)
	}
	if claim.rawSHA != workflowHash(raw) {
		t.Fatalf("claim raw hash fence=%q want=%q", claim.rawSHA, workflowHash(raw))
	}
	if len(claim.provenanceSHA) != 64 || len(claim.worksetSHA) != 64 || claim.generation != 1 || claim.claimToken == "" {
		t.Fatalf("claim identity=%+v", claim)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=?`, a.Review.ID); n != 0 {
		t.Fatalf("materialization written without finalize: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 0 {
		t.Fatalf("Decision written without finalize: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM execution_proposals`); n != 0 {
		t.Fatalf("materialization protocol created a hidden Execution: %d", n)
	}
	if g, e := f.store.Read(ctx, f.project, f.area); e != nil || g.TaskPhase != task.PhaseReviewing || g.CurrentState != "REVIEW_MATERIALIZING" || g.NextAction.Code != "WAIT_FOR_REVIEW_MATERIALIZATION" {
		t.Fatalf("open claim guidance=%+v err=%v", g, e)
	}
	f.db.SetReviewMaterializationFaultHookForTest(nil)
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	resumed := scanMaterializationClaim(t, f.db, a.Review.ID)
	if !resumed.found || resumed.state != "FINALIZED" || resumed.outcome != "VALID" || resumed.decisionID != wp.DecisionID(a.Review.ID) || resumed.finalizedAt == "" {
		t.Fatalf("resume did not terminalize the exact claim: %+v", resumed)
	}
	if resumed.claimToken != claim.claimToken || resumed.generation != claim.generation {
		t.Fatalf("resume re-claimed instead of resuming: before=%+v after=%+v", claim, resumed)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=? AND outcome='VALID'`, a.Review.ID); n != 1 {
		t.Fatalf("formal materializations=%d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 1 {
		t.Fatalf("Decisions=%d want exactly one", n)
	}
	f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	// duplicate finalize: further sweeps terminalize nothing a second time.
	for i := 0; i < 2; i++ {
		if err := f.store.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after := scanMaterializationClaim(t, f.db, a.Review.ID)
	if after.state != "FINALIZED" || after.claimToken != resumed.claimToken || after.finalizedAt != resumed.finalizedAt || after.updated != resumed.updated {
		t.Fatalf("duplicate finalize rewrote the terminal claim: before=%+v after=%+v", resumed, after)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 1 {
		t.Fatalf("duplicate finalize created a second Decision: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM execution_proposals`); n != 0 {
		t.Fatalf("materialization protocol created a hidden Execution: %d", n)
	}
}

// G2: the invalid formal result terminalizes the claim with the single
// invalid-result Decision and the retained raw artifact.
func TestReviewMaterializationInvalidResultTerminalizesClaimWithSingleDecision(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	raw := `{"version":1,"reviewDisposition":"APPROVAL_READY","summary":"missing required proposal"}`
	f.complete(t, a, raw)
	claim := scanMaterializationClaim(t, f.db, a.Review.ID)
	if !claim.found || claim.state != "FINALIZED" || claim.outcome != "INVALID" || claim.decisionID != wp.InvalidResultDecisionID(a.Review.ID) || claim.finalizedAt == "" {
		t.Fatalf("invalid result claim=%+v", claim)
	}
	if claim.rawSHA != workflowHash(raw) {
		t.Fatalf("invalid raw fence=%q want=%q", claim.rawSHA, workflowHash(raw))
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=? AND outcome='INVALID' AND reason_code='RESULT_INVALID' AND formal_result_json IS NULL`, a.Review.ID); n != 1 {
		t.Fatalf("invalid materializations=%d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 1 {
		t.Fatalf("invalid Decisions=%d want exactly one", n)
	}
	g, e := f.store.Read(context.Background(), f.project, f.area)
	if e != nil || g.TaskPhase != task.PhaseReviewing || g.FormalResultStatus != "INVALID" || g.DecisionID != wp.InvalidResultDecisionID(a.Review.ID) {
		t.Fatalf("invalid guidance=%+v err=%v", g, e)
	}
}

// G3: cancellation before any claim leaves no claim row at all; the protocol
// must not resurrect materialization for a terminalized Task.
func TestReviewMaterializationCancelBeforeClaimCreatesNoClaim(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	raw := approvedResult(t)
	f.db.SetReviewMaterializationFaultHookForTest(func(reviewID, stage string) error {
		if stage != "BEFORE_CLAIM" {
			return nil
		}
		f.db.SetReviewMaterializationFaultHookForTest(nil)
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "TASK", OwnerID: a.Review.TaskID, RequestID: "cancel-before-claim", Reason: "withdraw before materialization"}, false); err != nil {
			t.Errorf("official Task cancellation: %v", err)
		}
		return errors.New("stop this sweep; cancellation landed before the claim")
	})
	f.complete(t, a, raw)
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materialization_claims`); n != 0 {
		t.Fatalf("claims created after cancellation: %d", n)
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materialization_claims WHERE raw_result_sha256 IS NOT NULL`); n != 0 {
		t.Fatalf("post-cancellation claims: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations`); n != 0 {
		t.Fatalf("post-cancellation materializations: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions`); n != 0 {
		t.Fatalf("post-cancellation Decisions: %d", n)
	}
	var status string
	if err := f.db.sql.QueryRow(`SELECT status FROM tasks WHERE id=?`, a.Review.TaskID).Scan(&status); err != nil || status != string(task.StatusCanceled) {
		t.Fatalf("task status=%q err=%v", status, err)
	}
}

// G4: cancellation between the committed claim and finalize. The finalize
// transaction must re-check the durable facts, VOID the exact claim and write
// no formal materialization and no Decision.
func TestReviewMaterializationCancelAfterClaimVoidsWithoutMaterialization(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	raw := approvedResult(t)
	f.db.SetReviewMaterializationFaultHookForTest(func(reviewID, stage string) error {
		if stage != "AFTER_CLAIM" {
			return nil
		}
		f.db.SetReviewMaterializationFaultHookForTest(nil)
		if _, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, executioncancel.Input{ProjectID: f.project, AISessionID: f.session, OwnerType: "TASK", OwnerID: a.Review.TaskID, RequestID: "cancel-during-materialization", Reason: "withdraw after the durable claim"}, false); err != nil {
			t.Errorf("official Task cancellation: %v", err)
		}
		return nil
	})
	f.complete(t, a, raw)
	claim := scanMaterializationClaim(t, f.db, a.Review.ID)
	if !claim.found || claim.state != "VOIDED" || claim.outcome != "" || claim.decisionID != "" || claim.finalizedAt == "" {
		t.Fatalf("canceled review claim=%+v", claim)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations`); n != 0 {
		t.Fatalf("canceled review materialized: %d", n)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions`); n != 0 {
		t.Fatalf("canceled review created a Decision: %d", n)
	}
	// The VOID terminal state is durable: further sweeps change nothing.
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	after := scanMaterializationClaim(t, f.db, a.Review.ID)
	if after.state != "VOIDED" || after.finalizedAt != claim.finalizedAt {
		t.Fatalf("VOIDED claim rewritten: before=%+v after=%+v", claim, after)
	}
}

// G5: a finalizer holding a stale token or targeting an already terminal
// claim is a no-op, and the schema keeps the claim ledger append-only: no
// duplicate claim, no reopen, no delete.
func TestReviewMaterializationStaleFinalizerCannotRewriteTerminalClaim(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	raw := approvedResult(t)
	f.complete(t, a, raw)
	claim := scanMaterializationClaim(t, f.db, a.Review.ID)
	if !claim.found || claim.state != "FINALIZED" {
		t.Fatalf("fixture claim=%+v", claim)
	}
	stale := reviewMaterializationClaim{ReviewID: a.Review.ID, ProjectID: a.Review.ProjectID, TaskID: a.Review.TaskID, RunID: a.Review.RunID, ClaimToken: "rmc-stale-token", Generation: 99}
	if err := finalizeReviewMaterializationClaimTx(ctx, f.db, stale, reviewMaterializationOutcome{Terminal: "VALID", Kind: "VALID_APPROVAL"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after := scanMaterializationClaim(t, f.db, a.Review.ID)
	if after.state != "FINALIZED" || after.claimToken != claim.claimToken || after.decisionID != claim.decisionID || after.finalizedAt != claim.finalizedAt {
		t.Fatalf("stale finalizer rewrote the claim: %+v", after)
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 1 {
		t.Fatalf("stale finalizer Decisions=%d", n)
	}
	// duplicate claim for the same Review is structurally impossible.
	dupArgs := []any{a.Review.ID, a.Review.ProjectID, a.Review.TaskID, a.Review.RunID, "MATERIALIZING", strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), 1, "rmc-second", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339)}
	if _, err := f.db.sql.Exec(`INSERT INTO review_result_materialization_claims(review_id,project_id,task_id,run_id,state,raw_result_sha256,runtime_workset_hash,provenance_sha256,generation,claim_token,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, dupArgs...); err == nil {
		t.Fatal("duplicate MATERIALIZING claim accepted")
	}
	if _, err := f.db.sql.Exec(`UPDATE review_result_materialization_claims SET state='MATERIALIZING',finalized_at=NULL,outcome=NULL,decision_id=NULL WHERE review_id=?`, a.Review.ID); err == nil {
		t.Fatal("terminal claim reopened")
	}
	if _, err := f.db.sql.Exec(`DELETE FROM review_result_materialization_claims WHERE review_id=?`, a.Review.ID); err == nil {
		t.Fatal("durable claim deleted")
	}
	// a second Review for the same Run/Task keeps one claim per Review; the
	// existing formal materialization fences any new claim (source fence).
	if _, err := f.db.sql.Exec(`INSERT INTO review_result_materialization_claims(review_id,project_id,task_id,run_id,state,raw_result_sha256,runtime_workset_hash,provenance_sha256,generation,claim_token,created_at,updated_at) SELECT id,project_id,task_id,run_id,'MATERIALIZING',?,?,?,?,?,?,? FROM review_result_materializations WHERE review_id=?`, strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), 1, "rmc-deferred", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), a.Review.ID); err == nil {
		t.Fatal("claim accepted for a Review with a formal materialization")
	}
	if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materialization_claims WHERE review_id=?`, a.Review.ID); n != 1 {
		t.Fatalf("claim rows=%d want exactly one", n)
	}
}
