package controlapi_test

// Two real business Sessions share the same established caller and Project.
// This tests Session-scoped confirmation identity, not cross-principal security.

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aicontext"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
)

type crossSessionLedgerTable struct {
	Columns []string
	Rows    [][]any
}

// Capture all persisted columns, not just counts or the selected request. Each
// snapshot is one read-only transaction over both real Sessions and this Task;
// it is not a claim of a globally atomic workspace view. No command is in flight.
func (s *continuationGate) crossSessionLedger(other string) map[string]crossSessionLedgerTable {
	s.t.Helper()
	tx, err := s.f.plane.DB.SQL().BeginTx(s.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.t.Fatal(err)
	}
	defer tx.Rollback()
	out := make(map[string]crossSessionLedgerTable)
	for _, query := range []struct {
		table, where string
		args         []any
	}{
		{"confirmation_intents", "ai_session_id IN (?,?) ORDER BY ai_session_id,intent_id", []any{s.f.session, other}},
		{"confirmation_commit_requests", "ai_session_id IN (?,?) ORDER BY ai_session_id,request_id", []any{s.f.session, other}},
		{"confirmation_replacement_receipts", "ai_session_id IN (?,?) ORDER BY ai_session_id,request_id", []any{s.f.session, other}},
		{"confirmation_current_decisions", "ai_session_id IN (?,?) ORDER BY ai_session_id,decision_id", []any{s.f.session, other}},
		{"task_action_heads", "task_id=? ORDER BY task_id", []any{s.f.task}},
		{"task_action_confirmation_bindings", "task_id=? ORDER BY intent_id", []any{s.f.task}},
		{"task_action_conflict_resolutions", "task_id=? ORDER BY task_id,generation", []any{s.f.task}},
		{"task_action_releases", "task_id=? ORDER BY intent_id", []any{s.f.task}},
		{"task_holds", "task_id=? ORDER BY task_id", []any{s.f.task}},
		{"task_hold_receipts", "task_id=? ORDER BY actor,request_id", []any{s.f.task}},
		{"events", "task_id=? AND type='task.verification_choice' ORDER BY sequence", []any{s.f.task}},
	} {
		// Table/predicate strings are fixed test source, never caller input.
		rows, err := tx.QueryContext(s.ctx, "SELECT * FROM "+query.table+" WHERE "+query.where, query.args...)
		if err != nil {
			s.t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			s.t.Fatal(err)
		}
		table := crossSessionLedgerTable{Columns: columns, Rows: [][]any{}}
		for rows.Next() {
			values, targets := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				targets[i] = &values[i]
			}
			if err := rows.Scan(targets...); err != nil {
				rows.Close()
				s.t.Fatal(err)
			}
			for i, value := range values {
				if raw, ok := value.([]byte); ok {
					values[i] = string(raw)
				}
			}
			table.Rows = append(table.Rows, values)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			s.t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			s.t.Fatal(err)
		}
		out[query.table] = table
	}
	return out
}

func TestSimulatedContinuationCrossSession(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseCrossSession)
}

func (s *continuationGate) readSessionIntent(in c.Intent, request string) c.Intent {
	var out c.Result
	read := c.ReadInput{AISessionID: in.AISessionID, IntentID: in.IntentID, Version: in.Version, Purpose: in.Purpose, ContentHash: in.ContentHash, RequestID: request}
	gateDecode(s.t, s.call("gpt", "confirmation_get", read, ""), &out)
	s.check(out.Intent != nil && out.Intent.AISessionID == in.AISessionID && out.Intent.IntentID == in.IntentID && out.Intent.Version == in.Version && out.Intent.Purpose == in.Purpose && out.Intent.ContentHash == in.ContentHash, "own Session reads exact original intent identity", out)
	return *out.Intent
}

func (s *continuationGate) checkSessionBinding(session string) {
	var bound aicontext.InitializationResult
	gateDecode(s.t, s.call("gpt", "session_initialize", map[string]any{"aiSessionId": session}, ""), &bound)
	s.check(bound.AISessionID == session && bound.State == "PROJECT_BOUND" && bound.BoundProjectID != nil && *bound.BoundProjectID == s.f.project, "original persisted Session remains bound to same Project", bound)
}

func (s *continuationGate) secondBoundSession(request string) (string, c.Intent) {
	var initialized aicontext.InitializationResult
	gateDecode(s.t, s.call("gpt", "session_initialize", map[string]any{"requestId": "cross-initialize-b"}, ""), &initialized)
	other := initialized.AISessionID
	s.check(other != "" && other != s.f.session && initialized.State == "PROJECT_SELECTION_REQUIRED" && initialized.BoundProjectID == nil, "public initialization creates distinct real unbound Session B", initialized)
	var prepared c.Result
	gateDecode(s.t, s.call("gpt", "project_setup_prepare", map[string]any{"aiSessionId": other, "requestId": "cross-prepare-project-b", "purpose": "PROJECT_SETUP", "action": "SELECT_PROJECT", "projectSetup": map[string]any{"summary": "Explicit fixture-user selects the existing Project for distinct Session B."}, "variants": []any{map[string]any{"id": "bind", "label": "Bind Project", "summary": "Bind only Session B; do not authorize engineering work."}}}, ""), &prepared)
	s.check(prepared.Intent != nil && prepared.Intent.State == "PENDING" && prepared.Intent.AISessionID == other, "Session B project selection is a real pending confirmation", prepared)
	// A different Session may independently reuse this string. Its project
	// binding receipt must never masquerade as A's later DEFER receipt.
	input := s.commitInput(*prepared.Intent, request)
	input.AISessionID = other
	input.Selection.ProjectID = s.f.project
	bound := s.commit(input)
	// The prepared intent freezes its originally unbound ProjectID. The actual
	// new binding belongs to the BIND_PROJECT receipt and current Session read.
	s.check(bound.AISessionID == other && bound.ProjectID == prepared.Intent.ProjectID && bound.Submission != nil && bound.Submission.Recorded && bound.Submission.Outcome == c.Consumed && reflect.DeepEqual(bound.Submission.Request, input) && len(bound.Steps) == 1 && bound.Steps[0].Kind == "BIND_PROJECT" && bound.Steps[0].ProjectID == s.f.project && bound.Steps[0].State == "COMPLETED", "explicit fixture-user confirmation binds B without continuation authority", bound)
	s.checkSessionBinding(s.f.session)
	s.checkSessionBinding(other)
	s.checkOtherSessionTaskView(other)
	s.save("cross-sessions.json", map[string]any{"originalSession": s.f.session, "otherSession": other, "projectId": s.f.project, "callerScope": "data-instance-local", "scope": "same-caller business Session intent isolation, not cross-principal authentication"})
	return other, bound
}

func (s *continuationGate) checkOtherSessionTaskView(other string) {
	var view mcpserver.WorkspaceGetResult
	gateDecode(s.t, s.call("gpt", "workspace_get", map[string]any{"aiSessionId": other, "taskId": s.f.task, "featureAreaId": s.f.area, "include": []string{"TASK"}}, ""), &view)
	s.check(view.TaskView != nil && view.TaskView.ID == s.f.task && view.TaskView.CurrentExecution != nil && view.TaskView.CurrentExecution.ID == s.executionID && view.TaskView.CurrentExecution.TaskDefinitionRevisionID == s.revision, "legitimate Session B can read shared Project Task, not rejected as unbound or foreign Project", view.TaskView)
}

func (s *continuationGate) refuseForeignIntent(source c.Intent, commit c.CommitInput, other, label string) {
	s.t.Helper()
	before := s.crossSessionLedger(other)
	s.save("cross-ledger-"+label+"-before.json", before)
	read := c.ReadInput{AISessionID: other, IntentID: source.IntentID, Version: source.Version, Purpose: source.Purpose, ContentHash: source.ContentHash, RequestID: commit.RequestID}
	refuse := func(actor, tool string, input any) {
		wire := s.call(actor, tool, input, "CONFIRMATION_FACTS_UNAVAILABLE")
		var out c.Result
		gateDecode(s.t, wire, &out)
		s.check(out.Intent == nil && out.Options == nil && out.Display == nil && len(wire) == 1 && out.Failure != nil, "foreign Session gets only typed failure, no intent/options/submission/receipt: "+label+" "+tool, wire)
	}
	refuse("gpt", "confirmation_get", read)
	refuse("gpt", "confirmation_options_get", read)
	refuse("fixture_user", "confirmation_discard", read)
	copied := commit
	copied.AISessionID = other
	refuse("fixture_user", "confirmation_commit", copied)
	// Both original-request replay and a fresh request must fail on Session +
	// intent identity, not merely on a request-key collision in B's own ledger.
	copied.RequestID = "cross-foreign-fresh-" + label
	refuse("fixture_user", "confirmation_commit", copied)
	// Withdrawal can create durable negative authority without a hold or Run.
	// Test its own public entry with the copied request key (colliding with B's
	// binding for DEFER) and a fresh key: neither may seal A's request or leak
	// A/B's receipt through a failure.
	copied.RequestID = commit.RequestID
	refuse("fixture_user", "confirmation_submission_withdraw", copied)
	afterCollision := s.crossSessionLedger(other)
	s.save("cross-ledger-"+label+"-after-collision.json", afterCollision)
	s.check(reflect.DeepEqual(afterCollision, before), "foreign commands and colliding-key withdrawal preserve both complete Session ledgers, Task owner/generation and business facts: "+label, nil)
	copied.RequestID = "cross-foreign-fresh-" + label
	refuse("fixture_user", "confirmation_submission_withdraw", copied)
	afterFresh := s.crossSessionLedger(other)
	s.save("cross-ledger-"+label+"-after-fresh.json", afterFresh)
	s.check(reflect.DeepEqual(afterFresh, before), "foreign fresh-key withdrawal cannot create negative authority in either Session: "+label, nil)
}

func (s *continuationGate) exerciseCrossSession() {
	v := s.checkCheckpoint(false, false)
	request := "cross-shared-request-string"
	other, binding := s.secondBoundSession(request)
	afterBinding := s.checkCheckpoint(false, false)
	s.check(reflect.DeepEqual(afterBinding.ActionNode, v.ActionNode), "binding B does not change A Task action ownership or generation", afterBinding.ActionNode)
	v = afterBinding
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	pending := s.prepare(s.choice("DEFER_TASK", "cross-prepare-defer", note, int(v.ActionNode.Generation)))
	deferInput := s.commitInput(pending, request)
	node := s.checkCheckpoint(false, false).ActionNode
	s.refuseForeignIntent(pending, deferInput, other, "pending-defer")
	unchanged := s.readSessionIntent(pending, "")
	s.check(unchanged.State == "PENDING" && unchanged.Commit == nil && unchanged.Submission == nil && reflect.DeepEqual(unchanged.Content, pending.Content), "foreign probes leave A's exact pending DEFER unconsumed", unchanged)
	v = s.checkCheckpoint(false, false)
	s.check(reflect.DeepEqual(v.ActionNode, node), "foreign pending probes preserve Task action ownership and generation", v.ActionNode)
	deferred := s.commit(deferInput)
	s.check(deferred.AISessionID == s.f.session && deferred.Submission != nil && deferred.Submission.Recorded && deferred.Submission.Outcome == c.Consumed && reflect.DeepEqual(deferred.Submission.Request, deferInput) && deferred.IntentID != binding.IntentID && deferred.Submission.PayloadSHA256 != binding.Submission.PayloadSHA256, "same request string in distinct Sessions preserves distinct genuine consumed receipts", deferred)
	node = s.checkCheckpoint(true, false).ActionNode
	s.refuseForeignIntent(deferred, deferInput, other, "consumed-defer")
	after := s.readSessionIntent(deferred, request)
	s.check(reflect.DeepEqual(after.Commit, deferred.Commit) && reflect.DeepEqual(after.Steps, deferred.Steps) && reflect.DeepEqual(after.Submission, deferred.Submission), "foreign consumed probes preserve A's original DEFER receipt", after)
	v = s.checkCheckpoint(true, false)
	s.check(reflect.DeepEqual(v.ActionNode, node), "foreign consumed probes preserve Task action ownership and generation", v.ActionNode)
	// Reverse-direction read uses A with B's real consumed identity, including
	// the colliding request string, and cannot return either Session's receipt.
	var refused c.Result
	gateDecode(s.t, s.call("gpt", "confirmation_get", c.ReadInput{AISessionID: s.f.session, IntentID: binding.IntentID, Version: binding.Version, Purpose: binding.Purpose, ContentHash: binding.ContentHash, RequestID: request}, "CONFIRMATION_FACTS_UNAVAILABLE"), &refused)
	s.check(refused.Intent == nil && refused.Options == nil && refused.Display == nil, "A cannot borrow B's consumed project-binding identity", refused)
	ownBinding := s.readSessionIntent(binding, request)
	s.check(reflect.DeepEqual(ownBinding.Commit, binding.Commit) && reflect.DeepEqual(ownBinding.Steps, binding.Steps) && reflect.DeepEqual(ownBinding.Submission, binding.Submission), "B's own colliding-key binding receipt remains unchanged", ownBinding)
	s.cold()
	s.checkSessionBinding(s.f.session)
	s.checkSessionBinding(other)
	s.checkOtherSessionTaskView(other)
	s.refuseForeignIntent(deferred, deferInput, other, "cold-defer")
	after = s.readSessionIntent(deferred, request)
	s.check(reflect.DeepEqual(after.Commit, deferred.Commit) && reflect.DeepEqual(after.Steps, deferred.Steps) && reflect.DeepEqual(after.Submission, deferred.Submission), "cold original DEFER history survives cross-Session probes", after)
	v = s.checkCheckpoint(true, false)
	s.check(reflect.DeepEqual(v.ActionNode, node), "cold reopen and foreign probes preserve Task action generation", v.ActionNode)
	preparedContinue := s.prepare(s.choice("CONTINUE_TASK", "cross-prepare-continue", "", int(v.ActionNode.Generation)))
	continueInput := s.commitInput(preparedContinue, "cross-commit-continue")
	continued := s.commit(continueInput)
	node = s.checkCheckpoint(false, true).ActionNode
	s.refuseForeignIntent(continued, continueInput, other, "consumed-continue")
	after = s.readSessionIntent(continued, continueInput.RequestID)
	s.check(reflect.DeepEqual(after.Commit, continued.Commit) && reflect.DeepEqual(after.Steps, continued.Steps) && reflect.DeepEqual(after.Submission, continued.Submission), "foreign probes preserve original CONTINUE receipt", after)
	ownBinding = s.readSessionIntent(binding, request)
	s.check(reflect.DeepEqual(ownBinding.Commit, binding.Commit) && reflect.DeepEqual(ownBinding.Steps, binding.Steps) && reflect.DeepEqual(ownBinding.Submission, binding.Submission), "cold B binding receipt remains independent of A continuation", ownBinding)
	v = s.checkCheckpoint(false, true)
	s.check(reflect.DeepEqual(v.ActionNode, node), "foreign CONTINUE probes preserve Task action ownership and generation", v.ActionNode)
	s.save("final-workspace.json", s.workspace())
}
