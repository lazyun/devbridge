package controlapi_test

// A real CONTINUE attempts to delete the real DEFER hold, but its canonical
// receipt insert fails. The normal transaction rollback must preserve that hold.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

func TestSimulatedContinuationContinueUnknown(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SIMULATED_CONTINUATION") != "1" {
		t.Skip("explicit trusted Agent gate; run scripts/test-simulated-continuation.sh --continue-unknown")
	}
	token := newJournalFaultWitness(t)
	runSimulatedContinuationGate(t, func(s *continuationGate) { s.exerciseContinueUnknown(token) })
}

type continueUnknownBusinessSnapshot struct {
	HoldsJSON, ReceiptsJSON, EventsJSON                                 string
	DeferIntentJSON, DeferRequestJSON, DeferSubmittedHash, DeferOutcome string
}

// Only immutable business facts belong here: preparing the later CONTINUE
// legitimately changes action ownership, which is separately frozen after fault.
func (s *continuationGate) continueUnknownBusinessSnapshot(deferred c.Intent, input c.CommitInput) continueUnknownBusinessSnapshot {
	s.t.Helper()
	tx, err := s.f.plane.DB.SQL().BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	var out continueUnknownBusinessSnapshot
	queries := []struct {
		sql  string
		args []any
		dest []any
	}{
		{`SELECT json_group_array(json_object('taskId',task_id,'executionId',execution_id,'taskDefinitionRevisionId',revision_id,'note',note,'heldAt',held_at)) FROM task_holds WHERE task_id=?`, []any{s.f.task}, []any{&out.HoldsJSON}},
		{`SELECT json_group_array(json_object('actor',actor,'requestId',request_id,'taskId',task_id,'executionId',execution_id,'revisionId',revision_id,'payloadSHA256',payload_sha256,'action',action,'createdAt',created_at)) FROM (SELECT * FROM task_hold_receipts WHERE task_id=? ORDER BY actor,request_id)`, []any{s.f.task}, []any{&out.ReceiptsJSON}},
		{`SELECT json_group_array(json_object('sequence',sequence,'id',id,'type',type,'projectId',project_id,'taskId',task_id,'runId',run_id,'entityType',entity_type,'entityId',entity_id,'payload',payload_json,'createdAt',created_at)) FROM (SELECT * FROM events WHERE task_id=? AND type='task.verification_choice' ORDER BY sequence)`, []any{s.f.task}, []any{&out.EventsJSON}},
		{`SELECT intent_json FROM confirmation_intents WHERE intent_id=? AND ai_session_id=?`, []any{input.IntentID, input.AISessionID}, []any{&out.DeferIntentJSON}},
		{`SELECT request_json,submitted_sha256,outcome FROM confirmation_commit_requests WHERE intent_id=? AND ai_session_id=? AND request_id=?`, []any{input.IntentID, input.AISessionID, input.RequestID}, []any{&out.DeferRequestJSON, &out.DeferSubmittedHash, &out.DeferOutcome}},
	}
	for _, q := range queries {
		if err = tx.QueryRowContext(s.ctx, q.sql, q.args...).Scan(q.dest...); err != nil {
			s.t.Fatal(err)
		}
	}
	var holds []task.Hold
	var receipts []struct{ Actor, RequestID, TaskID, ExecutionID, RevisionID, PayloadSHA256, Action, CreatedAt string }
	var events []json.RawMessage
	var durable c.Intent
	var request c.CommitInput
	for _, v := range []struct {
		raw  string
		dest any
	}{{out.HoldsJSON, &holds}, {out.ReceiptsJSON, &receipts}, {out.EventsJSON, &events}, {out.DeferIntentJSON, &durable}, {out.DeferRequestJSON, &request}} {
		if err = json.Unmarshal([]byte(v.raw), v.dest); err != nil {
			s.t.Fatal(err)
		}
	}
	s.check(len(holds) == 1 && reflect.DeepEqual(holds[0], s.held), "persisted original DEFER hold retains exact Task/Execution/revision/note/HeldAt", holds)
	body := deferred.Content.Execution
	s.check(body != nil && len(deferred.Steps) == 1, "original genuine DEFER retains frozen payload and one business step", deferred)
	raw, err := json.Marshal(task.HoldInput{ProjectID: deferred.ProjectID, TaskID: body.TaskID, FeatureAreaID: body.FeatureAreaID, ExecutionID: body.ExecutionID, RevisionID: body.TaskDefinitionRevisionID, Action: "DEFER_TASK", RequestID: deferred.Steps[0].RequestID, Note: body.Note})
	if err != nil {
		s.t.Fatal(err)
	}
	s.check(len(receipts) == 1 && receipts[0].Actor == "session:"+input.AISessionID && receipts[0].RequestID == deferred.Steps[0].RequestID && receipts[0].TaskID == s.f.task && receipts[0].ExecutionID == s.executionID && receipts[0].RevisionID == s.revision && receipts[0].Action == "DEFER_TASK" && receipts[0].PayloadSHA256 == fmt.Sprintf("%x", sha256.Sum256(raw)) && receipts[0].CreatedAt != "" && len(events) == 1, "only genuine exact DEFER canonical receipt/event exists; no CONTINUE receipt", out)
	s.check(durable.State == "COMPLETED" && reflect.DeepEqual(durable.Commit, &input) && reflect.DeepEqual(durable.Steps, deferred.Steps) && reflect.DeepEqual(request, input) && out.DeferSubmittedHash == c.ValueHash(input) && out.DeferOutcome == "CONSUMED", "original successful DEFER journal and consumed request remain exact", out)
	return out
}

func (s *continuationGate) exerciseContinueUnknown(token string) {
	view := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	prepareDefer := s.prepare(s.choice("DEFER_TASK", "continue-unknown-prepare-defer", note, int(view.ActionNode.Generation)))
	deferInput := s.commitInput(prepareDefer, "continue-unknown-commit-defer")
	deferred := s.commit(deferInput)
	s.check(deferred.Submission != nil && deferred.Submission.Recorded && deferred.Submission.Outcome == c.Consumed && reflect.DeepEqual(deferred.Submission.Request, deferInput), "real fixture-user DEFER succeeds before any fault is installed", deferred)
	view = s.checkCheckpoint(true, false)
	baseline := s.continueUnknownBusinessSnapshot(deferred, deferInput)
	s.save("continue-unknown-business-before.json", baseline)
	s.check(len(journalFaultCalls(token)) == 0, "successful DEFER and real pipeline never entered future CONTINUE fault", nil)
	pending := s.prepare(s.choice("CONTINUE_TASK", "continue-unknown-prepare-continue", "", int(view.ActionNode.Generation)))
	input := s.commitInput(pending, "continue-unknown-commit-once")
	s.checkCheckpoint(true, false)
	s.check(reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "CONTINUE preparation does not change original held business facts", nil)
	witness := s.injectChoiceReceiptRollback(token, pending, input)
	// The shared real canonical-receipt insert is after ChangeTaskHold's DELETE.
	// Its abort rolls back the entire transaction; no replacement hold is seeded.
	before := s.journalSnapshotWithCounts(input, 1, 1)
	s.save("continue-unknown-journal-before.json", before)
	s.check(reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "CONTINUE rollback preserves exact old hold/receipt/event bytes", nil)
	observed := s.confirmationRead(pending, input.RequestID)
	s.checkUnknownChoice(observed, pending, input)
	s.checkUnknownOptions(pending, input)
	s.checkUnknownChoice(s.discardChoice(observed), pending, input)
	old := s.confirmationRead(prepareDefer, deferInput.RequestID)
	s.check(reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Submission, deferred.Submission), "reading completed DEFER history cannot settle failed CONTINUE", old)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == input.IntentID && view.ActionNode.Generation == before.Generation, "consumed UNKNOWN CONTINUE blocks current Task despite surviving hold", view.ActionNode)
	s.call("gpt", "execution_prepare", s.choice("DEFER_TASK", "continue-unknown-blocked-defer", note, int(view.ActionNode.Generation)), "TASK_ACTION_PENDING")
	s.check(reflect.DeepEqual(s.journalSnapshotWithCounts(input, 1, 1), before) && reflect.DeepEqual(s.continueUnknownBusinessSnapshot(deferred, deferInput), baseline), "warm reads/discard/refused fresh choice neither settle UNKNOWN nor replace the hold", nil)
	s.cold()
	observed = s.confirmationRead(pending, input.RequestID)
	s.checkUnknownChoice(observed, pending, input)
	s.checkUnknownOptions(pending, input)
	old = s.confirmationRead(prepareDefer, deferInput.RequestID)
	s.check(reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Submission, deferred.Submission), "cold old DEFER receipt remains consumed and unchanged", old)
	view = s.checkCheckpoint(true, false)
	s.check(view.ActionNode.Blocking && view.ActionNode.IntentID == input.IntentID && view.ActionNode.Generation == before.Generation, "cold UNKNOWN CONTINUE retains exact action ownership and generation", view.ActionNode)
	after := s.journalSnapshotWithCounts(input, 1, 1)
	businessAfter := s.continueUnknownBusinessSnapshot(deferred, deferInput)
	s.check(reflect.DeepEqual(after, before) && reflect.DeepEqual(businessAfter, baseline) && reflect.DeepEqual(journalFaultCalls(token), witness), "cold recovery preserves held business facts and exact UNKNOWN without re-entering command", after)
	s.save("continue-unknown-journal-after.json", after)
	s.save("continue-unknown-business-after.json", businessAfter)
	s.save("final-workspace.json", s.workspace())
	// No DEFER or CONTINUE commit replay, no fresh command after UNKNOWN.
}
