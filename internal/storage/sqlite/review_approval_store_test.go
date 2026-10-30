package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type reviewApprovalFixture struct {
	db                     *DB
	store                  *WorkflowProgressionStore
	project, area, session string
	candidate              review.Review
	reviews                *review.Service
	now                    time.Time
	clockMu                sync.Mutex
}

func newReviewApprovalFixture(t *testing.T) *reviewApprovalFixture {
	f, _ := newTaskOwnedFixture(t)
	return f
}
func (f *reviewApprovalFixture) start(t *testing.T) review.StartAdmission {
	t.Helper()
	f.now = f.now.Add(time.Minute)
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository, Paths: []string{}}
	a, err := f.reviews.AdmitStart(context.Background(), f.candidate, "request-"+f.now.Format("150405"), strings.Repeat("a", 64), f.candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func approvedResult(t *testing.T) string {
	t.Helper()
	b, e := json.Marshal(review.ImplementationResult{ReviewDisposition: review.DispositionApprovalReady, Version: 1, Summary: "Reload model settings for future Runs; preserve active Run settings.", WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Hot-reload model configuration", Constraints: []string{"Keep active Runs unchanged"}, CodexInstruction: "Reload validated model settings only for subsequent Runs", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopePaths, Paths: []string{"internal/config"}}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./internal/config", Required: true}}, AcceptanceCriteria: []string{"A running Run retains its model; the next Run uses the new model"}, CodexInstruction: "Verify runtime reload and active Run isolation", Scope: "internal/config"}})
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func (f *reviewApprovalFixture) complete(t *testing.T, a review.StartAdmission, raw string) {
	t.Helper()
	_, err := NewRunStore(f.db).Update(context.Background(), a.Run.ID, func(r *run.Run) error {
		r.Status = run.StatusSucceeded
		r.UpdatedAt = f.now
		r.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: raw}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func (f *reviewApprovalFixture) input(a review.StartAdmission, option string) wp.ResolveInput {
	in := wp.ResolveInput{AISessionID: f.session, FeatureAreaID: f.area, ReviewID: a.Review.ID, DecisionID: wp.DecisionID(a.Review.ID), Option: option, RequestID: "approve-" + a.Review.ID}
	if option == "APPROVE" {
		in.BackendSelection = f.candidate.BackendSelection
		in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	}
	if option == "REVISE" {
		in.ReviewFeedback = "Please revisit the formal result and address the unresolved evidence."
	}
	return in
}
func (f *reviewApprovalFixture) guidance(t *testing.T, state, action string) wp.Guidance {
	t.Helper()
	g, e := f.store.Read(context.Background(), f.project, f.area)
	if e != nil || g.CurrentState != state || g.NextAction.Code != action {
		t.Fatalf("guidance=%+v err=%v want=%s/%s", g, e, state, action)
	}
	return g
}
func (f *reviewApprovalFixture) developmentCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM tasks WHERE feature_area_id=? AND task_type='DEVELOPMENT'`, f.area).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestReviewApprovalAtomicMaterializationAndFreshReplay(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	f.guidance(t, "REVIEW_REQUIRED", "START_TASK_REVIEW")
	a := f.start(t)
	f.guidance(t, "REVIEWING", "WAIT_FOR_REVIEW_START")
	if _, err := f.store.Resolve(ctx, f.input(a, "APPROVE")); err == nil {
		t.Fatal("approved running Review")
	}
	f.complete(t, a, approvedResult(t))
	before := f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	var materializedTask string
	var ordinal int
	var rawHash, formalHash, targetsHash, workHash, verificationHash string
	if err := f.db.sql.QueryRow(`SELECT task_id,review_ordinal,raw_result_sha256,formal_result_sha256,repository_target_set_sha256,work_spec_sha256,verification_spec_sha256 FROM review_result_materializations WHERE review_id=?`, a.Review.ID).Scan(&materializedTask, &ordinal, &rawHash, &formalHash, &targetsHash, &workHash, &verificationHash); err != nil {
		t.Fatal(err)
	}
	if materializedTask != a.Review.TaskID || ordinal != a.Review.Ordinal || rawHash == "" || formalHash == "" || targetsHash == "" || workHash == "" || verificationHash == "" {
		t.Fatalf("materialization identity task=%s ordinal=%d hashes=%q/%q/%q/%q/%q", materializedTask, ordinal, rawHash, formalHash, targetsHash, workHash, verificationHash)
	}
	if f.developmentCount(t) != 1 {
		t.Fatal("Task before Approval")
	}
	out, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Task == nil || out.CurrentState != "IMPLEMENTATION_READY" || out.Task.ReviewApprovalID != out.Receipt.DecisionID || out.Task.CurrentDefinitionRevisionID == "" {
		t.Fatalf("handoff=%+v", out)
	}
	if out.Receipt.SubjectKind != "TASK_REVIEW_GATE" || out.Receipt.SubjectID != out.Task.ID {
		t.Fatalf("receipt must name existing Task gate: %+v", out.Receipt)
	}
	if out.Generation == before.Generation {
		t.Fatal("generation did not change")
	}
	revision, err := NewTaskStore(f.db).GetDefinitionRevision(ctx, out.Task.CurrentDefinitionRevisionID)
	if err != nil || revision.Definition.ReviewApprovalID != out.Receipt.DecisionID {
		t.Fatalf("revision=%+v err=%v", revision, err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			replay, e := f.store.Resolve(ctx, f.input(a, "APPROVE"))
			if e == nil && (!replay.Replay || replay.Task.ID != out.Task.ID || replay.Receipt.RequestID != out.Receipt.RequestID) {
				e = errors.New("unstable replay")
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if f.developmentCount(t) != 1 {
		t.Fatal("duplicate Task")
	}
	if err := f.store.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := NewTaskStore(f.db).Update(ctx, out.Task.ID, func(v *task.Task) error { v.WorkSpec.Goal = "Expanded goal"; v.Goal = v.WorkSpec.Goal; return nil }); err == nil {
		t.Fatal("approved semantics mutable")
	}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	if _, err := f.reviews.AdmitStart(ctx, f.candidate, "post-approve", strings.Repeat("a", 64), f.candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}); err == nil {
		t.Fatal("approved Task re-entered Review without a Task decision")
	}

}

func TestReviewResultMaterializationRetainsInvalidRawResultWithResolution(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	raw := `{"version":1,"reviewDisposition":"APPROVAL_READY","summary":"missing required proposal"}`
	f.complete(t, a, raw)
	var outcome, rawHash, code string
	var formal any
	if err := f.db.sql.QueryRow(`SELECT outcome,raw_result_sha256,reason_code,formal_result_json FROM review_result_materializations WHERE review_id=? AND run_id=?`, a.Review.ID, a.Run.ID).Scan(&outcome, &rawHash, &code, &formal); err != nil {
		t.Fatal(err)
	}
	if outcome != "INVALID" || rawHash != workflowHash(raw) || code != "RESULT_INVALID" || formal != nil {
		t.Fatalf("materialization outcome=%s hash=%s code=%s formal=%v", outcome, rawHash, code, formal)
	}
	if ownedCount(t, f, "review_approval_decisions") != 1 {
		t.Fatal("invalid result did not create its dedicated resolution authority")
	}
	g := f.guidance(t, "REVIEWING", "REVIEW_RESULT_RESOLUTION_REQUIRED")
	if !g.RawResultAvailable || g.FormalResultStatus != "INVALID" || g.MaterializationFailure == nil || g.MaterializationFailure.Code != "RESULT_INVALID" || g.DecisionID != wp.InvalidResultDecisionID(a.Review.ID) || !slices.Equal(g.AvailableActions, []string{"REQUEST_RE_REVIEW", "OPEN_PROJECT_REPOSITORIES", "ABANDON_TASK"}) {
		t.Fatalf("invalid result guidance=%+v", g)
	}
	in := f.input(a, "REVISE")
	in.DecisionID, in.RequestID = wp.InvalidResultDecisionID(a.Review.ID), "resolve-invalid-by-rereview"
	out, err := f.store.Resolve(context.Background(), in)
	if err != nil || out.SuccessorReviewID == "" || out.SuccessorRunID == "" || out.Task == nil || out.Task.ID != a.Review.TaskID || out.Task.Phase != task.PhaseReviewing {
		t.Fatalf("invalid result resolution=%+v err=%v", out, err)
	}
	var retainedOutcome, retainedHash string
	if err = f.db.sql.QueryRow(`SELECT outcome,raw_result_sha256 FROM review_result_materializations WHERE review_id=?`, a.Review.ID).Scan(&retainedOutcome, &retainedHash); err != nil || retainedOutcome != "INVALID" || retainedHash != workflowHash(raw) {
		t.Fatalf("invalid history changed: outcome=%s hash=%s err=%v", retainedOutcome, retainedHash, err)
	}
	replay, err := f.store.Resolve(context.Background(), in)
	if err != nil || !replay.Replay || replay.SuccessorReviewID != out.SuccessorReviewID || replay.SuccessorRunID != out.SuccessorRunID {
		t.Fatalf("invalid resolution replay=%+v err=%v", replay, err)
	}
	changed := in
	changed.ReviewFeedback = "different immutable feedback"
	if _, err = f.store.Resolve(context.Background(), changed); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("changed invalid resolution replay err=%v", err)
	}
	if f.developmentCount(t) != 1 || ownedCount(t, f, "reviews") != 2 || ownedCount(t, f, "runs") != 2 || ownedCount(t, f, "execution_proposals") != 0 {
		t.Fatal("invalid resolution created a duplicate Task or execution graph")
	}
}

func TestReviewReviseAtomicallyReservesSameTaskSuccessorAndExactReplay(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	in := f.input(a, "REVISE")
	in.RequestID = "re-review-once"
	first, err := f.store.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if first.SuccessorReviewID == "" || first.SuccessorRunID == "" || first.ReviewStartRequestID == "" || first.Task == nil || first.Task.ID != a.Review.TaskID || first.Task.Phase != task.PhaseReviewing {
		t.Fatalf("re-review result=%+v", first)
	}
	g := f.guidance(t, "REVIEWING", "WAIT_FOR_REVIEW_START")
	if g.ReviewID != first.SuccessorReviewID || g.RunID != first.SuccessorRunID {
		t.Fatalf("successor projection=%+v", g)
	}
	successor, err := f.reviews.Get(context.Background(), first.SuccessorReviewID)
	if err != nil || successor.Ordinal != a.Review.Ordinal+1 || successor.RunID != first.SuccessorRunID || successor.TaskID != a.Review.TaskID {
		t.Fatalf("successor=%+v err=%v", successor, err)
	}
	var feedback, hash, formal string
	if err := f.db.sql.QueryRow(`SELECT feedback,feedback_sha256,previous_formal_result_sha256 FROM review_re_review_requests WHERE decision_id=?`, wp.DecisionID(a.Review.ID)).Scan(&feedback, &hash, &formal); err != nil {
		t.Fatal(err)
	}
	if feedback != in.ReviewFeedback || hash != workflowHash(in.ReviewFeedback) || formal == "" {
		t.Fatalf("immutable feedback=%q hash=%q formal=%q", feedback, hash, formal)
	}
	replay, err := f.store.Resolve(context.Background(), in)
	if err != nil || !replay.Replay || replay.SuccessorReviewID != first.SuccessorReviewID || replay.SuccessorRunID != first.SuccessorRunID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	conflict := in
	conflict.ReviewFeedback = "A different request payload must not create another Review."
	if _, err = f.store.Resolve(context.Background(), conflict); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	if f.developmentCount(t) != 1 || ownedCount(t, f, "reviews") != 2 || ownedCount(t, f, "runs") != 2 || ownedCount(t, f, "execution_proposals") != 0 {
		t.Fatalf("unexpected re-review graph tasks=%d reviews=%d runs=%d executions=%d", f.developmentCount(t), ownedCount(t, f, "reviews"), ownedCount(t, f, "runs"), ownedCount(t, f, "execution_proposals"))
	}
}

func TestReviewDecisionApproveReReviewAbandonSerialize(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	start := make(chan struct{})
	type result struct {
		option string
		out    wp.Resolution
		err    error
	}
	results := make(chan result, 3)
	var wg sync.WaitGroup
	for _, option := range []string{"APPROVE", "REVISE", "REJECT"} {
		wg.Add(1)
		go func(option string) {
			defer wg.Done()
			<-start
			in := f.input(a, option)
			in.RequestID = "race-" + strings.ToLower(option)
			out, err := f.store.Resolve(context.Background(), in)
			results <- result{option: option, out: out, err: err}
		}(option)
	}
	close(start)
	wg.Wait()
	close(results)
	winners := []result{}
	for value := range results {
		if value.err == nil {
			winners = append(winners, value)
		}
	}
	if len(winners) != 1 {
		t.Fatalf("decision winners=%+v", winners)
	}
	var revisions, executions, successors int
	for query, target := range map[string]*int{
		`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`: &revisions,
		`SELECT count(*) FROM execution_proposals WHERE task_id=?`:       &executions,
		`SELECT count(*) FROM review_re_review_requests WHERE task_id=?`: &successors,
	} {
		if err := f.db.sql.QueryRow(query, a.Review.TaskID).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	switch winners[0].option {
	case "APPROVE":
		if revisions != 2 || executions != 1 || successors != 0 {
			t.Fatalf("approve graph revisions=%d executions=%d successors=%d", revisions, executions, successors)
		}
	case "REVISE":
		if revisions != 1 || executions != 0 || successors != 1 || ownedCount(t, f, "reviews") != 2 {
			t.Fatalf("re-review graph revisions=%d executions=%d successors=%d", revisions, executions, successors)
		}
	case "REJECT":
		if revisions != 1 || executions != 0 || successors != 0 || ownedTask(t, f).Status != task.StatusCanceled {
			t.Fatalf("abandon graph revisions=%d executions=%d successors=%d", revisions, executions, successors)
		}
	}
}

func TestReviewApprovalRuntimeConfigMaterializesAndSurvivesRestart(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	in := f.input(a, "APPROVE")
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true}
	out, err := f.store.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, out.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.Resolve(context.Background(), in)
	if err != nil || !replay.Replay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := *in.NextRunRuntimeConfig
	changed.CodexFastMode = false
	in.NextRunRuntimeConfig = &changed
	if _, err = f.store.Resolve(context.Background(), in); err == nil {
		t.Fatal("review replay accepted a changed runtime config")
	}
	if got := execution.RuntimeConfig; got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "high" || !got.CodexFastMode || got.ServiceTier != "priority" {
		t.Fatalf("runtime=%+v", got)
	}
	root := f.db.StateRoot()
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewExecutionProposalStore(reopened).Get(context.Background(), execution.ID)
	if err != nil || restored.RuntimeConfig != execution.RuntimeConfig {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestReviewApprovalRejectReviseOwnershipAndRetirement(t *testing.T) {
	for _, option := range []string{"REJECT", "REVISE"} {
		t.Run(option, func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			a := f.start(t)
			f.complete(t, a, approvedResult(t))
			in := f.input(a, option)
			bad := in
			bad.FeatureAreaID += "-wrong"
			if _, e := f.store.Resolve(context.Background(), bad); e == nil {
				t.Fatal("wrong area accepted")
			}
			other := seed(t, f.db, "foreign-approval")
			bad = in
			bad.AISessionID = "foreign-session"
			seedBoundSession(t, f.db, aisession.Session{ID: bad.AISessionID, ProjectID: other.project.ID, CreatedAt: testNow, UpdatedAt: testNow})
			if _, e := f.store.Resolve(context.Background(), bad); e == nil {
				t.Fatal("wrong Project accepted")
			}
			out, e := f.store.Resolve(context.Background(), in)
			if e != nil {
				t.Fatal(e)
			}
			var actor string
			if err := f.db.sql.QueryRow(`SELECT ai_session_id FROM review_approval_receipt_context WHERE decision_id=?`, in.DecisionID).Scan(&actor); err != nil || actor != f.session {
				t.Fatalf("receipt actor=%s err=%v", actor, err)
			}
			if out.Task == nil || out.Task.ID != f.candidate.TaskID || f.developmentCount(t) != 1 {
				t.Fatal("rejection created Task")
			}
			if _, e = f.store.Resolve(context.Background(), f.input(a, "APPROVE")); e == nil {
				t.Fatal("resolved Decision reapproved")
			}
			if option == "REJECT" {
				if out.Task.Status != task.StatusCanceled {
					t.Fatal("rejected Task active")
				}
				return
			}
			if out.Task.Phase != task.PhaseReviewing || out.SuccessorReviewID == "" || out.SuccessorRunID == "" || out.Task.CurrentReviewID != out.SuccessorReviewID {
				t.Fatal("revise did not create and authorize the successor Review")
			}
			nextReview, err := f.reviews.Get(context.Background(), out.SuccessorReviewID)
			if err != nil {
				t.Fatal(err)
			}
			next := review.StartAdmission{Review: nextReview, Run: run.Run{ID: out.SuccessorRunID}}
			f.complete(t, next, approvedResult(t))
			if _, e = NewFeatureAreaStore(f.db).ApplyLifecycle(context.Background(), featurearea.LifecycleMutation{ProjectID: f.project, FeatureAreaID: f.area, AISessionID: f.session, RequestID: "retire", PayloadSHA256: strings.Repeat("c", 64), Action: featurearea.ActionRetire}); e != nil {
				t.Fatal(e)
			}
			_, e = f.store.Resolve(context.Background(), f.input(next, "APPROVE"))
			var rejected *wp.Error
			if !errors.As(e, &rejected) || rejected.Code != "STALE_REVIEW_APPROVAL" {
				t.Fatalf("late Approval=%v", e)
			}
			if f.developmentCount(t) != 1 {
				t.Fatal("retired materialization")
			}
		})
	}
}

func TestReviewApprovalCompletionRecoveryAndAtomicFault(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	// Simulate an older writer committing a terminal Run before the completion
	// coordinator. A pure read must not create the pending Decision.
	if _, e := f.db.sql.Exec(`UPDATE runs SET status='SUCCEEDED',result_text=? WHERE id=?`, approvedResult(t), a.Run.ID); e != nil {
		t.Fatal(e)
	}
	var before, after int
	_ = f.db.sql.QueryRow(`SELECT count(*) FROM review_approval_decisions`).Scan(&before)
	_, _ = f.store.Read(ctx, f.project, f.area)
	_ = f.db.sql.QueryRow(`SELECT count(*) FROM review_approval_decisions`).Scan(&after)
	if before != 0 || after != 0 {
		t.Fatal("read repaired Decision")
	}
	if e := f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if e := f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	// The raw historical terminal fact deliberately bypassed operation
	// finalization, so its durable claim remains busy.  Exercise approval's
	// materialization rollback on a separately, canonically completed Review;
	// this keeps the test from fabricating a safe boundary or deleting a claim.
	f = newReviewApprovalFixture(t)
	a = f.start(t)
	f.complete(t, a, approvedResult(t))
	f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	if _, e := f.db.sql.Exec(`CREATE TRIGGER approval_fault BEFORE INSERT ON review_approval_materializations BEGIN SELECT RAISE(ABORT,'injected materialization failure'); END`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.store.Resolve(ctx, f.input(a, "APPROVE")); e == nil {
		t.Fatal("fault not reached")
	}
	var receipts int
	_ = f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.DecisionID(a.Review.ID)).Scan(&receipts)
	if receipts != 0 || f.developmentCount(t) != 1 {
		t.Fatal("partial Approval committed")
	}
	if _, e := f.db.sql.Exec(`DROP TRIGGER approval_fault`); e != nil {
		t.Fatal(e)
	}
	out, e := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if e != nil {
		t.Fatal(e)
	}
	replay, e := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if e != nil || !replay.Replay || replay.Task.ID != out.Task.ID || replay.Task.Status != task.StatusActive {
		t.Fatalf("fresh replay=%+v err=%v", replay, e)
	}
	if _, e := f.db.sql.Exec(`UPDATE runs SET result_text='mutated' WHERE id=?`, a.Run.ID); e == nil {
		t.Fatal("approved result mutable")
	}
}

func TestReviewApprovalFailedCanceledAndMalformedResults(t *testing.T) {
	for _, status := range []run.Status{run.StatusFailed, run.StatusCanceled, run.StatusSucceeded} {
		t.Run(string(status), func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			a := f.start(t)
			_, e := NewRunStore(f.db).Update(context.Background(), a.Run.ID, func(v *run.Run) error {
				v.Status = status
				v.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: "unstructured result"}}
				return nil
			})
			if status == run.StatusCanceled && errors.Is(e, agentconversation.ErrUnsafe) {
				return
			} // canonical cancellation is covered by adapter regression tests
			if e != nil {
				t.Fatal(e)
			}
			g, e := f.store.Read(context.Background(), f.project, f.area)
			if e != nil {
				t.Fatal(e)
			}
			if status != run.StatusSucceeded && g.Approval != nil && len(g.Approval.AllowedActions) > 0 {
				t.Fatalf("unusable result actionable=%+v", g)
			}
			if status == run.StatusSucceeded && (g.Approval == nil || !slices.Equal(g.AvailableActions, []string{"REQUEST_RE_REVIEW", "OPEN_PROJECT_REPOSITORIES", "ABANDON_TASK"})) {
				t.Fatalf("invalid result resolution unavailable=%+v", g)
			}
			if _, e = f.store.Resolve(context.Background(), f.input(a, "APPROVE")); e == nil {
				t.Fatal("unusable approved")
			}
			if f.developmentCount(t) != 1 {
				t.Fatal("unusable materialized")
			}
		})
	}
}

func TestReviewApprovalRestartPreservesReceiptTaskAndInitialRevision(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	in := f.input(a, "APPROVE")
	pending := f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	root := f.db.stateRoot
	if e := f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db = openTestDB(t, root)
	t.Cleanup(func() { f.db.Close() })
	f.store = NewWorkflowProgressionStore(f.db)
	if e := f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	recovered := f.guidance(t, "REVIEW_APPROVAL_PENDING", "REVIEW_CONFIRMATION_REQUIRED")
	if recovered.DecisionID != pending.DecisionID || recovered.Approval.ResultSHA256 != pending.Approval.ResultSHA256 {
		t.Fatal("restart changed Approval")
	}
	out, e := f.store.Resolve(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.db.Close(); e != nil {
		t.Fatal(e)
	}
	f.db = openTestDB(t, root)
	f.store = NewWorkflowProgressionStore(f.db)
	if e = f.store.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	replay, e := f.store.Resolve(ctx, in)
	if e != nil || !replay.Replay || replay.Task.ID != out.Task.ID || replay.Task.CurrentDefinitionRevisionID != out.Task.CurrentDefinitionRevisionID || replay.NextAction.Code != "START_EXECUTION" {
		t.Fatalf("restarted replay=%+v err=%v", replay, e)
	}
}

func TestReviewApprovalRetirementRaceSerializesMaterialization(t *testing.T) {
	for attempt := 0; attempt < 5; attempt++ {
		f := newReviewApprovalFixture(t)
		a := f.start(t)
		f.complete(t, a, approvedResult(t))
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-start; _, _ = f.store.Resolve(context.Background(), f.input(a, "APPROVE")) }()
		go func() {
			defer wg.Done()
			<-start
			_, _ = NewFeatureAreaStore(f.db).ApplyLifecycle(context.Background(), featurearea.LifecycleMutation{ProjectID: f.project, FeatureAreaID: f.area, AISessionID: f.session, RequestID: "retirement-race", PayloadSHA256: strings.Repeat("d", 64), Action: featurearea.ActionRetire})
		}()
		close(start)
		wg.Wait()
		f.guidance(t, "FEATURE_AREA_RETIRING", "WAIT_FOR_RETIREMENT")
		var receipts int
		_ = f.db.sql.QueryRow(`SELECT count(*) FROM decision_resolution_receipts WHERE decision_id=?`, wp.DecisionID(a.Review.ID)).Scan(&receipts)
		if receipts+1 != ownedCount(t, f, "task_definition_revisions") || receipts > 1 {
			t.Fatal("partial race commit")
		}
		replay, e := f.store.Resolve(context.Background(), f.input(a, "APPROVE"))
		if receipts == 1 {
			if e != nil || !replay.Replay {
				t.Fatal("lost committed receipt", e)
			}
		} else if e == nil {
			t.Fatal("late approval accepted")
		}

	}
}

func TestReviewApprovalEmptySuccessfulResultIsNotActionable(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, "")
	g := f.guidance(t, "REVIEWING", "RETRY_REVIEW_OR_ABANDON")
	if g.RawResultAvailable || g.FormalResultAvailable || len(g.AvailableActions) != 2 {
		t.Fatalf("empty Review guidance=%+v", g)
	}
	if _, err := f.store.Resolve(context.Background(), f.input(a, "APPROVE")); err == nil {
		t.Fatal("empty Review approved")
	}
}

func TestApprovedTaskWorkProvenanceBeforeImplementation(t *testing.T) {
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	out, err := f.store.Resolve(context.Background(), f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	values, err := NewTaskStore(f.db).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range values {
		if v.ID != out.Task.ID && v.ID != a.Review.TaskID {
			continue
		}
		seen[v.ID] = true
		if len(v.Work.DevelopmentBranches) != 1 || v.Work.DevelopmentBranches[0] != "refs/heads/dev" {
			t.Fatalf("lost exact admission branch %+v", v.Work)
		}
		if v.ID == out.Task.ID && v.Work.DevelopmentBranchProvenance != "REVIEW_APPROVAL_INITIAL_REVISION" {
			t.Fatalf("legacy-only provenance %+v", v.Work)
		}
	}
	if !seen[out.Task.ID] || !seen[a.Review.TaskID] {
		t.Fatal("authoritative inventory missing Task", seen)
	}
	g := f.guidance(t, "IMPLEMENTATION_READY", "START_EXECUTION")
	if g.CurrentWorkflowReview == nil || g.CurrentWorkflowReview.ID != a.Review.ID || g.Approval != nil || g.TaskID != out.Task.ID {
		t.Fatalf("lineage %+v", g)
	}
}

func TestNewTaskReviewDoesNotSupersedeApprovedTask(t *testing.T) {
	f := newReviewApprovalFixture(t)
	first := f.start(t)
	f.complete(t, first, approvedResult(t))
	ctx := context.Background()
	approved, err := f.store.Resolve(ctx, f.input(first, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	next := *approved.Task
	next.ID = "next-semantic-task"
	next.Title = "Another intent"
	next.Goal = "Another intent"
	next.ProblemStatement = "Another intent"
	next.Status = task.StatusActive
	next.Phase = task.PhaseReviewRequired
	next.CurrentReviewID = ""
	next.CurrentExecutionID = ""
	next.CurrentDefinitionRevisionID = ""
	next.ConfirmedTaskPlanID = ""
	next.ReviewApprovalID = ""
	next.WorkSpec = task.WorkSpec{}
	next.VerificationSpec = task.VerificationSpec{}
	if err = NewTaskStore(f.db).Create(ctx, next); err != nil {
		t.Fatal(err)
	}
	f.candidate.TaskID = next.ID
	second := f.start(t)
	f.complete(t, second, approvedResult(t))
	g, err := f.store.ReadTask(ctx, f.project, f.area, approved.Task.ID)
	if err != nil || g.TaskPhase != task.PhaseImplementationReady || g.ReviewID != first.Review.ID {
		t.Fatalf("other Task superseded approval: %+v %v", g, err)
	}
}

func TestReviewApprovalScopeConflictPreservesImmutableAuthority(t *testing.T) {
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	out, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := NewTaskStore(f.db).GetDefinitionRevision(ctx, out.Task.CurrentDefinitionRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	executions := NewExecutionProposalStore(f.db)
	candidate, r, err := f.store.ReserveTaskExecution(ctx, f.session, out.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Status != executionproposal.StatusAdmitted || r.ID != "" {
		t.Fatalf("expected admission before Run preparation: %+v %+v", candidate, r)
	}
	candidate, r, err = executions.PrepareAdmittedExecution(ctx, candidate.ID, f.now)
	if err != nil || r.ID == "" {
		t.Fatalf("canonical preparation: %+v %v", r, err)
	}
	if _, err = run.NewService(NewRunStore(f.db), nil, nil).Fail(ctx, r.ID, run.Failure{Kind: run.FailureTask, Code: "SCOPE_CONFLICT", Message: "Approved scope cannot satisfy the goal"}); err != nil {
		t.Fatal(err)
	}
	if _, err = executions.Complete(ctx, candidate.ID, executionproposal.OutcomeFailed, "SCOPE_CONFLICT", "Approved scope cannot satisfy the goal", f.now); err != nil {
		t.Fatal(err)
	}
	after, err := NewTaskStore(f.db).GetDefinitionRevision(ctx, before.ID)
	if err != nil || after.PayloadSHA256 != before.PayloadSHA256 {
		t.Fatalf("definition changed: %+v %v", after, err)
	}
	// A different request cannot reuse this terminal execution's immutable
	// admission. Exact preparation replay is readback, not a new execution.
	if _, err = executions.AdmitExecution(ctx, candidate.ID, "must-not-retry", strings.Repeat("f", 64), f.now); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("conflicting authority reused: %v", err)
	}
	var count int
	if err = f.db.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=?`, candidate.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("terminal authority created another Run: count=%d err=%v", count, err)
	}
}

func (f *reviewApprovalFixture) newIntent(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	original, err := NewTaskStore(f.db).Get(ctx, f.candidate.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	original.ID = id
	original.FeatureAreaID = f.area
	original.Status, original.Phase = task.StatusActive, task.PhaseReviewRequired
	original.CurrentReviewID, original.CurrentExecutionID, original.CurrentDefinitionRevisionID, original.ReviewApprovalID, original.ConfirmedTaskPlanID = "", "", "", "", ""
	original.Title = f.candidate.EngineeringGoal
	if err := NewTaskStore(f.db).Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	f.candidate.TaskID = id
}

func TestReviewApprovalModelOnlyFreezesDefaultBeforeReplayAndMaterialization(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(map[bool]string{false: "standard", true: "fast"}[fast], func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			// Review semantic depth does not select a runtime speed or effort.
			if fast {
				f.candidate.ReviewDepth = task.ReviewDepthDeep
			} else {
				f.candidate.ReviewDepth = task.ReviewDepthFast
			}
			selected := codexruntime.Default()
			for i := range selected.ModelConfigs {
				if selected.ModelConfigs[i].ModelID == "gpt-5.6-terra" {
					selected.ModelConfigs[i].DefaultReasoningEffort = "xhigh"
				}
			}
			f.store.SetRuntimeConfigProvider(func() codexruntime.Allowlist { return selected.Clone() })
			admission := f.start(t)
			f.complete(t, admission, approvedResult(t))
			in := f.input(admission, "APPROVE")
			in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", CodexFastMode: fast}
			out, err := f.store.Resolve(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			// Defaults may change or become unavailable after approval. Neither replay
			// nor deferred Execution materialization may consult them again.
			f.store.SetRuntimeConfigProvider(func() codexruntime.Allowlist { panic("approved config reread Desktop defaults") })
			replay, err := f.store.Resolve(context.Background(), in)
			if err != nil || !replay.Replay {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			execution, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, out.Task.ID)
			if err != nil {
				t.Fatal(err)
			}
			got := execution.RuntimeConfig
			if got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "xhigh" || got.CodexFastMode != fast {
				t.Fatalf("frozen config=%+v", got)
			}
			if (got.ServiceTier == "priority") != fast {
				t.Fatalf("tier=%q fast=%t", got.ServiceTier, fast)
			}
			for _, changed := range []codexruntime.NextRunRuntimeConfig{{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh", CodexFastMode: fast}, {Model: got.Model, ReasoningEffort: "medium", CodexFastMode: fast}, {Model: got.Model, ReasoningEffort: got.ReasoningEffort, CodexFastMode: !fast}} {
				conflict := in
				conflict.NextRunRuntimeConfig = &changed
				if _, err := f.store.Resolve(context.Background(), conflict); err == nil {
					t.Fatalf("replay accepted changed selection %+v", changed)
				}
			}
		})
	}
}
