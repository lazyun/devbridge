//go:build !windows

package controlapi_test

// Actual pre-commit process death leaves CONFIRMING, not the finalized UNKNOWN
// produced by a returned SQL-abort error. Neither explicit withdrawal nor discard
// may turn that consumed submission into a rejection, release or retry authority.

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func TestSimulatedContinuationCrashSubmissionWithdraw(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseCrashSubmissionWithdraw)
}

type crashSubmissionWithdrawalSnapshot struct {
	Ledger                                    submissionWithdrawalSnapshot
	IntentsJSON, BindingsJSON, ReleasesJSON   string
	OriginalReleaseCount, ReplacementReceipts int
}

func (s *continuationGate) crashWithdrawalSnapshot(in c.CommitInput) crashSubmissionWithdrawalSnapshot {
	s.t.Helper()
	out := crashSubmissionWithdrawalSnapshot{Ledger: s.submissionWithdrawalSnapshot(in)}
	tx, err := s.f.plane.DB.SQL().BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT json_group_array(json_object('id',intent_id,'prepareRequest',prepare_request_id,'prepareHash',prepare_hash,'worker',worker_epoch,'intent',intent_json)) FROM (SELECT * FROM confirmation_intents WHERE ai_session_id=? ORDER BY intent_id)`, []any{in.AISessionID}, []any{&out.IntentsJSON}},
		{`SELECT json_group_array(json_object('intent',intent_id,'task',task_id,'generation',generation)) FROM (SELECT * FROM task_action_confirmation_bindings WHERE task_id=? ORDER BY intent_id)`, []any{s.f.task}, []any{&out.BindingsJSON}},
		{`SELECT json_group_array(json_object('intent',intent_id,'task',task_id,'generation',generation,'reason',reason)) FROM (SELECT * FROM task_action_releases WHERE task_id=? ORDER BY intent_id)`, []any{s.f.task}, []any{&out.ReleasesJSON}},
		{`SELECT COUNT(*) FROM task_action_releases WHERE intent_id=? AND task_id=?`, []any{in.IntentID, s.f.task}, []any{&out.OriginalReleaseCount}},
		{`SELECT COUNT(*) FROM confirmation_replacement_receipts WHERE ai_session_id=?`, []any{in.AISessionID}, []any{&out.ReplacementReceipts}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	s.check(out.Ledger.IntentRequests == 1 && out.Ledger.Owner == in.IntentID && out.OriginalReleaseCount == 0 && out.ReplacementReceipts == 0, "consumed crashed intent has one submission, original ownership, no release or replacement receipt", out)
	return out
}

func (s *continuationGate) exerciseCrashSubmissionWithdraw() {
	// Runs the real implementation/verification/analysis, genuine DEFER, exact
	// CONTINUE SIGKILL, independent pre-Open rollback oracle and cold readback.
	// All subsequent commands target those SAME durable identities and data.
	cp := s.establishCrashedContinue()
	in := cp.Input
	before := s.crashWithdrawalSnapshot(in)
	s.check(before.Ledger.IntentJSON == cp.Journal.IntentJSON && before.Ledger.Generation == cp.Journal.Generation, "withdrawal baseline is the actual crashed journal and original action fence", before)
	s.save("crash-submission-withdraw-before.json", before)
	assertUnchanged := func(label string) {
		s.t.Helper()
		now := s.crashWithdrawalSnapshot(in)
		s.check(reflect.DeepEqual(now, before), label+": all Session intents/requests and Task bindings/releases remain byte-identical", now)
		s.check(reflect.DeepEqual(s.continueProcessCrashSnapshot(s.f.plane.DB.SQL(), in), cp.Journal) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(cp.Deferred, cp.DeferInput), cp.Business), label+": crashed journal/worker epoch and exact restored hold/receipt/event remain unchanged", nil)
	}
	checkNoop := func(got c.Intent, label string) {
		s.t.Helper()
		s.checkUncommittedCrashChoice(got, cp.Pending, in, cp.Journal)
		s.check(reflect.DeepEqual(got.Commit, cp.Observed.Commit) && reflect.DeepEqual(got.Steps, cp.Observed.Steps) && reflect.DeepEqual(got.Submission, cp.Observed.Submission), label+": success envelope is exact consumed readback, not withdrawal or discard success", got)
		assertUnchanged(label)
	}
	withdrawn := s.withdrawSubmission(in)
	checkNoop(withdrawn, "warm exact withdrawal")
	s.save("crash-submission-withdraw-after-withdraw.json", s.crashWithdrawalSnapshot(in))
	discarded := s.discardChoice(cp.Pending)
	checkNoop(discarded, "warm exact discard")
	s.save("crash-submission-withdraw-after-discard.json", s.crashWithdrawalSnapshot(in))
	// Negative requests are never alternate recovery identities or business retries.
	// The original request and selection remain fixed for every successful readback.
	changed := in
	changed.Selection.ProjectID = s.f.project
	s.call("fixture_user", "confirmation_submission_withdraw", changed, "CONFIRMATION_REQUEST_CONFLICT")
	assertUnchanged("changed payload under consumed original request")
	unobserved := in
	unobserved.RequestID = "crash-withdraw-never-submitted-negative-probe"
	s.call("fixture_user", "confirmation_submission_withdraw", unobserved, "CONFIRMATION_REQUEST_CONFLICT")
	assertUnchanged("different unobserved withdrawal request cannot obtain non-consumption authority")
	wrongHash := c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: c.ValueHash("test-only wrong crash discard hash")}
	s.call("fixture_user", "confirmation_discard", wrongHash, "CONFIRMATION_READ_IDENTITY_MISMATCH")
	assertUnchanged("wrong frozen discard hash")
	view := s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == cp.Journal.Generation, "withdrawal/discard cannot release blocked consumed CONFIRMING owner or advance generation", view.ActionNode)
	s.call("gpt", "execution_prepare", s.choice("CONTINUE_TASK", "crash-withdraw-blocked-fresh-choice", "", int(cp.Journal.Generation)), "TASK_ACTION_PENDING")
	assertUnchanged("new choice still refused after both explicit no-op commands")
	s.cold()
	// Reverse the serial command order in a new epoch. This is not concurrency
	// arbitration; it proves each command remains a no-op after prior readbacks.
	coldDiscard := s.discardChoice(cp.Pending)
	checkNoop(coldDiscard, "cold exact discard")
	coldWithdraw := s.withdrawSubmission(in)
	checkNoop(coldWithdraw, "cold exact withdrawal")
	original := s.confirmationRead(cp.Pending, in.RequestID)
	checkNoop(original, "cold original receipt")
	oldDefer := s.confirmationRead(cp.Original, cp.DeferInput.RequestID)
	s.check(reflect.DeepEqual(oldDefer.Commit, cp.Deferred.Commit) && reflect.DeepEqual(oldDefer.Steps, cp.Deferred.Steps) && reflect.DeepEqual(oldDefer.Submission, cp.Deferred.Submission), "original DEFER history stays completed without settling crashed CONTINUE", oldDefer)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == in.IntentID && view.ActionNode.Generation == cp.Journal.Generation, "cold withdrawal/discard preserve original blocked owner and action generation", view.ActionNode)
	assertUnchanged("final cold state")
	for name, want := range cp.Fault.AgentLogs {
		got, err := os.ReadFile(filepath.Join(s.evidence, name))
		s.check(err == nil && bytes.Equal(got, want), "post-crash withdrawal/discard dispatch no new Agent work: "+name, err)
	}
	s.save("crash-submission-withdraw-after-cold.json", s.crashWithdrawalSnapshot(in))
	s.save("final-workspace.json", s.workspace())
	// No confirmation_commit replay, replacement Session, new execution, manual
	// ownership release, canonical receipt injection or business-row repair.
}
