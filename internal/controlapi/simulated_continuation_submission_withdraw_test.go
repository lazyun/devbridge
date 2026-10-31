package controlapi_test

// Submission withdrawal is an explicit Core command, not an inference that an
// unobserved request was never consumed. A consumed choice cannot be withdrawn.

import (
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func TestSimulatedContinuationSubmissionWithdraw(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --submission-withdraw")
	}
	token := newJournalFaultWitness(t)
	runSimulatedContinuationGate(t, func(s *continuationGate) { s.exerciseSubmissionWithdraw(token) })
}

type submissionWithdrawalSnapshot struct {
	IntentJSON, RequestsJSON, HoldsJSON, ReceiptsJSON, EventsJSON, Owner string
	Generation                                                           int64
	IntentRequests                                                       int
}

// Read-only snapshot includes every request in this fixture Session, not only
// the latest one: a withdrawal cannot erase or replace older receipt history.
func (s *continuationGate) submissionWithdrawalSnapshot(in c.CommitInput) submissionWithdrawalSnapshot {
	return s.submissionWithdrawalSnapshotFromDB(s.f.plane.DB.SQL(), in)
}

func (s *continuationGate) submissionWithdrawalSnapshotFromDB(db *sql.DB, in c.CommitInput) submissionWithdrawalSnapshot {
	s.t.Helper()
	tx, err := db.BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	var out submissionWithdrawalSnapshot
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, []any{in.IntentID, in.AISessionID}, []any{&out.IntentJSON}},
		{`SELECT json_group_array(json_object('session',ai_session_id,'requestId',request_id,'intentId',intent_id,'payload',payload_sha256,'submitted',submitted_sha256,'request',request_json,'outcome',outcome,'failure',failure_json)) FROM (SELECT * FROM confirmation_commit_requests WHERE ai_session_id=? ORDER BY request_id)`, []any{in.AISessionID}, []any{&out.RequestsJSON}},
		{`SELECT COUNT(*) FROM confirmation_commit_requests WHERE intent_id=?`, []any{in.IntentID}, []any{&out.IntentRequests}},
		{`SELECT json_group_array(json_object('task',task_id,'execution',execution_id,'revision',revision_id,'note',note,'heldAt',held_at)) FROM task_holds WHERE task_id=?`, []any{s.f.task}, []any{&out.HoldsJSON}},
		{`SELECT json_group_array(json_object('actor',actor,'requestId',request_id,'task',task_id,'execution',execution_id,'revision',revision_id,'hash',payload_sha256,'action',action,'createdAt',created_at)) FROM (SELECT * FROM task_hold_receipts WHERE task_id=? ORDER BY actor,request_id)`, []any{s.f.task}, []any{&out.ReceiptsJSON}},
		{`SELECT json_group_array(json_object('sequence',sequence,'id',id,'type',type,'project',project_id,'task',task_id,'run',run_id,'entityType',entity_type,'entityId',entity_id,'payload',payload_json,'createdAt',created_at)) FROM (SELECT * FROM events WHERE task_id=? AND type='task.verification_choice' ORDER BY sequence)`, []any{s.f.task}, []any{&out.EventsJSON}},
		{`SELECT generation,COALESCE(owner_intent_id,'') FROM task_action_heads WHERE task_id=?`, []any{s.f.task}, []any{&out.Generation, &out.Owner}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	return out
}

func (s *continuationGate) withdrawSubmission(in c.CommitInput) c.Intent {
	var out c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_submission_withdraw", in, ""), &out)
	s.check(out.Intent != nil && out.Intent.IntentID == in.IntentID && out.Intent.AISessionID == in.AISessionID && out.Intent.Version == in.Version && out.Intent.ContentHash == in.ContentHash, "withdrawal addresses exact original intent and Session", out)
	return *out.Intent
}

func (s *continuationGate) checkWithdrawnPending(got, pending c.Intent, in c.CommitInput) {
	s.check(got.State == "PENDING" && got.Commit == nil && len(got.Steps) == 0 && got.Purpose == pending.Purpose && got.Action == pending.Action && reflect.DeepEqual(got.Content, pending.Content), "explicit submission withdrawal seals request without consuming or discarding pending intent", got)
	sub := got.Submission
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.RejectedNotConsumed && sub.BusinessOutcome == "REJECTED_NOT_CONSUMED" && sub.ConsumptionDisposition == "NOT_CONSUMED" && sub.Consumed != nil && !*sub.Consumed && !sub.RedispatchSameIntent && sub.RetryDisposition == c.RetryEditSelectionAndResubmit && sub.NextAction == c.EditSelection && sub.ReprepareAllowed && sub.ReceiptRequestID == "" && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in) && sub.Failure != nil && sub.Failure.Code == "CONFIRMATION_SUBMISSION_WITHDRAWN" && sub.Failure.Stage == "RECEIPT_READBACK", "only durable exact withdrawal proves non-consumption and allows a new explicit request", sub)
}

func (s *continuationGate) exerciseSubmissionWithdraw(token string) {
	view := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	pending := s.prepare(s.choice("DEFER_TASK", "withdraw-prepare-defer", note, int(view.ActionNode.Generation)))
	original := s.commitInput(pending, "withdraw-original-never-dispatched")
	untouched := s.submissionWithdrawalSnapshot(original)
	s.check(untouched.IntentRequests == 0 && untouched.HoldsJSON == "[]" && untouched.ReceiptsJSON == "[]" && untouched.EventsJSON == "[]", "prepared choice has no submission or business effect", untouched)
	// Deliberate never-submitted request probe, NOT simulated transport loss.
	unobserved := s.confirmationRead(pending, original.RequestID)
	sub := unobserved.Submission
	s.check(unobserved.State == "PENDING" && unobserved.Commit == nil && sub != nil && !sub.Recorded && sub.Outcome == c.Unknown && sub.Consumed == nil && sub.BusinessOutcome == "OUTCOME_UNKNOWN" && sub.ConsumptionDisposition == "UNKNOWN" && sub.NextAction == c.ReadOriginalReceipt && sub.RetryDisposition == c.RetryReadOriginalReceipt && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.PayloadSHA256 == "" && sub.ReceiptRequestID == "", "unobserved exact request is UNKNOWN, never non-consumption or retry authority", unobserved)
	s.check(reflect.DeepEqual(s.submissionWithdrawalSnapshot(original), untouched), "unobserved read creates no rejection receipt or permission", nil)
	withdrawn := s.withdrawSubmission(original)
	s.checkWithdrawnPending(withdrawn, pending, original)
	sealed := s.submissionWithdrawalSnapshot(original)
	s.check(sealed.IntentRequests == 1 && sealed.IntentJSON == untouched.IntentJSON && sealed.HoldsJSON == untouched.HoldsJSON && sealed.ReceiptsJSON == untouched.ReceiptsJSON && sealed.EventsJSON == untouched.EventsJSON && sealed.Owner == untouched.Owner && sealed.Generation == untouched.Generation, "withdrawal writes only the one durable rejection, not a hold or intent retirement", sealed)
	// The public closed stage vocabulary normalizes this internal stage to
	// RECEIPT_READBACK; retain its actual durable origin independently.
	var rows []struct {
		RequestID string
		Failure   *string
	}
	if err := json.Unmarshal([]byte(sealed.RequestsJSON), &rows); err != nil {
		s.t.Fatal(err)
	}
	var rawFailure c.Failure
	for _, row := range rows {
		if row.RequestID == original.RequestID && row.Failure != nil {
			if err := json.Unmarshal([]byte(*row.Failure), &rawFailure); err != nil {
				s.t.Fatal(err)
			}
		}
	}
	s.check(rawFailure.Code == "CONFIRMATION_SUBMISSION_WITHDRAWN" && rawFailure.Stage == "SUBMISSION_WITHDRAWAL", "public stage normalization does not rewrite durable withdrawal origin", rawFailure)
	s.save("submission-withdraw-sealed-before.json", sealed)
	repeated := s.withdrawSubmission(original)
	s.checkWithdrawnPending(repeated, pending, original)
	s.check(reflect.DeepEqual(repeated.Submission, withdrawn.Submission), "repeated exact withdrawal returns the original rejection", repeated)
	s.cold()
	// Intentional late delivery ONLY AFTER durable non-consumption was proven.
	var late c.Result
	gateDecode(s.t, s.call("fixture_user", "confirmation_commit", original, "CONFIRMATION_SUBMISSION_WITHDRAWN"), &late)
	s.check(late.Intent != nil, "late original commit retains its authoritative rejection", late)
	s.checkWithdrawnPending(*late.Intent, pending, original)
	changed := original
	changed.Selection.ProjectID = s.f.project
	s.call("fixture_user", "confirmation_submission_withdraw", changed, "CONFIRMATION_REQUEST_CONFLICT")
	read := s.confirmationRead(pending, original.RequestID)
	s.checkWithdrawnPending(read, pending, original)
	s.check(reflect.DeepEqual(read.Submission, withdrawn.Submission) && reflect.DeepEqual(s.submissionWithdrawalSnapshot(original), sealed), "cold late delivery and changed-payload conflict cannot alter sealed request or effects", nil)
	s.save("submission-withdraw-sealed-after.json", s.submissionWithdrawalSnapshot(original))
	s.checkCheckpoint(false, false)
	// Fresh explicit fixture-user confirmation is allowed by the durable fence,
	// not by the earlier UNKNOWN read. It must have a different request ID.
	fresh := s.commitInput(pending, "withdraw-new-explicit-defer")
	deferred := s.commit(fresh)
	view = s.checkCheckpoint(true, false)
	consumed := s.submissionWithdrawalSnapshot(fresh)
	s.check(consumed.IntentRequests == 2, "one withdrawn request plus one independently consumed fresh request", consumed)
	got := s.withdrawSubmission(fresh)
	s.check(got.State == "COMPLETED" && reflect.DeepEqual(got.Commit, deferred.Commit) && reflect.DeepEqual(got.Steps, deferred.Steps) && reflect.DeepEqual(got.Submission, deferred.Submission), "withdrawal cannot undo the genuinely consumed DEFER", got)
	history := s.withdrawSubmission(original)
	expected := *withdrawn.Submission
	expected.NextAction = c.ReadCurrentTarget
	expected.RetryDisposition = c.RetryReprepareRequired
	expected.ReprepareAllowed = false
	s.check(history.State == "COMPLETED" && reflect.DeepEqual(history.Commit, deferred.Commit) && reflect.DeepEqual(history.Submission, &expected), "historical withdrawn receipt remains NOT_CONSUMED but no longer re-arms consumed intent", history)
	s.check(reflect.DeepEqual(s.submissionWithdrawalSnapshot(fresh), consumed), "withdrawals preserve both immutable request histories and actual hold", nil)
	s.save("submission-withdraw-consumed.json", consumed)
	baseline := s.continueUnknownBusinessSnapshot(deferred, fresh)
	next := s.prepare(s.choice("CONTINUE_TASK", "withdraw-prepare-continue", "", int(view.ActionNode.Generation)))
	input := s.commitInput(next, "withdraw-consumed-unknown-continue")
	witness := s.injectChoiceReceiptRollback(token, next, input)
	journal := s.journalSnapshotWithCounts(input, 1, 1)
	unknown := s.submissionWithdrawalSnapshot(input)
	s.check(unknown.IntentRequests == 1, "failed CONTINUE has one consumed request, not a withdrawal rejection", unknown)
	s.save("submission-withdraw-unknown-before.json", unknown)
	got = s.withdrawSubmission(input)
	s.checkUnknownChoice(got, next, input)
	s.checkUnknownOptions(next, input)
	s.check(reflect.DeepEqual(s.submissionWithdrawalSnapshot(input), unknown) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, fresh), baseline), "withdrawal cannot relabel consumed UNKNOWN or clear its genuine old hold", nil)
	s.cold()
	got = s.withdrawSubmission(input)
	s.checkUnknownChoice(got, next, input)
	s.checkUnknownOptions(next, input)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == input.IntentID && view.ActionNode.Generation == journal.Generation, "cold withdrawal cannot release UNKNOWN action ownership", view.ActionNode)
	after := s.submissionWithdrawalSnapshot(input)
	s.check(reflect.DeepEqual(after, unknown) && reflect.DeepEqual(s.journalSnapshotWithCounts(input, 1, 1), journal) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, fresh), baseline) && reflect.DeepEqual(journalFaultCalls(token), witness), "cold withdrawal preserves exact UNKNOWN/consumed ledger, original hold and single fault", after)
	s.save("submission-withdraw-unknown-after.json", after)
	s.save("final-workspace.json", s.workspace())
	// No consumed or UNKNOWN confirmation_commit is ever retried.
}
