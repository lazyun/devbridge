package simulation

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

// taskAdmission ends at the first admitted Review. It deliberately does not
// claim a worker, approve a definition, or start implementation/continuation.
func (s *scenario) taskAdmission() {
	f := s.fixture
	f.checkAdmissionObjects("before Task preparation", 0, 0)
	prepared := f.prepare(f.taskPreparation("create-task"))
	s.check("Task preparation is pending without business steps", prepared.State == "PENDING" && prepared.Commit == nil && len(prepared.Steps) == 0, prepared)
	reprepared := f.prepare(f.taskPreparation("create-task"))
	s.check("exact Task preparation replay retains frozen identity", prepared.IntentID == reprepared.IntentID && prepared.Version == reprepared.Version && prepared.ContentHash == reprepared.ContentHash && reflect.DeepEqual(prepared.Content, reprepared.Content), reprepared)
	noReview := false
	input := admissionCommit(prepared, "create-task-confirmed", c.Selection{StartReview: &noReview})
	options := s.gpt.call("confirmation_options_get", c.OptionsInput{ReadInput: admissionRead(prepared, ""), StartReview: &noReview})
	var available c.Options
	decode(options["options"], &available)
	s.check("create-only options require no native configuration", available.Eligibility.Available && available.StartReview != nil && !*available.StartReview && len(available.BackendCandidates) == 0 && available.LockedBackend == nil, available)
	f.checkAdmissionObjects("prepare and options create no Task or Run", 0, 0)
	f.checkSources()

	for _, kind := range []string{"hash", "version"} {
		bad := input
		bad.RequestID = "invalid-create-" + kind
		if kind == "hash" {
			bad.ContentHash = strings.Repeat("0", 64)
		} else {
			bad.Version++
		}
		refused := f.refuseConfirmation(bad, "CONFIRMATION_READ_IDENTITY_MISMATCH")
		s.check("wrong "+kind+" discloses no confirmation facts", refused.Intent == nil && refused.Options == nil && refused.Failure.Stage == "INPUT_IDENTITY", refused)
		current := f.readAdmission(prepared, "")
		s.check("identity refusal leaves exact Task proposal unconsumed", current.State == "PENDING" && current.Commit == nil && len(current.Steps) == 0 && reflect.DeepEqual(current.Content, prepared.Content), current)
		f.checkAdmissionObjects("wrong "+kind+" has no business effects", 0, 0)
		f.checkSources()
	}

	// A definite editable rejection is different from missing/unknown evidence.
	// Only Core's explicit EDIT_SELECTION_AND_RESUBMIT permits the fresh choice.
	bad := input
	bad.RequestID, bad.VariantID = "invalid-create-variant", "not-prepared"
	f.checkEditableRefusal(prepared, bad)
	f.checkAdmissionObjects("invalid Task choice has no business effects", 0, 0)
	f.checkSources()
	created := f.commitAdmission(input)
	s.check("creation has exactly one completed Task step", len(created.Steps) == 1 && created.Steps[0].Kind == "CREATE_TASK" && created.Steps[0].State == "COMPLETED" && created.Steps[0].ReviewID == "" && created.Steps[0].RunID == "", created.Steps)
	f.task = created.Steps[0].TaskID
	f.checkUnstartedTask("explicit create-only selection")
	f.checkAdmissionObjects("one created Task and no Run", 1, 0)
	f.checkSources()
	f.checkAdmissionReplay(created, f.commitAdmission(input))

	withReview := true
	changed := input
	changed.Selection.StartReview = &withReview
	f.refuseConfirmation(changed, "CONFIRMATION_REQUEST_CONFLICT")
	changed.RequestID = "late-start-review-on-consumed-create"
	f.refuseConfirmation(changed, "CONFIRMATION_REQUEST_CONFLICT")
	f.checkUnstartedTask("consumed choice cannot retroactively start Review")
	f.checkAdmissionObjects("changed consumed choices create no duplicate", 1, 0)
	f.checkSources()

	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(created, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(created, f.commitAdmission(input))
	f.checkUnstartedTask("cold exact creation replay")
	f.checkAdmissionObjects("cold replay creates no duplicate Task or Run", 1, 0)
	f.checkSources()

	reviewPrepared := f.prepare(f.reviewPreparation("initial-review"))
	s.check("Review preparation is pending without admission", reviewPrepared.State == "PENDING" && reviewPrepared.Commit == nil && len(reviewPrepared.Steps) == 0, reviewPrepared)
	f.checkUnstartedTask("Review prepare is not Review admission")
	f.checkAdmissionObjects("Review prepare creates no Run", 1, 0)
	f.checkSources()
	reviewInput := admissionCommit(reviewPrepared, "initial-review-confirmed", c.Selection{})
	bad = reviewInput
	bad.RequestID, bad.VariantID = "invalid-review-variant", "not-prepared"
	f.checkEditableRefusal(reviewPrepared, bad)
	f.checkUnstartedTask("rejected Review choice does not advance Task")
	f.checkAdmissionObjects("rejected Review choice creates no Run", 1, 0)
	f.checkSources()
	admitted := f.commitAdmission(reviewInput)
	s.check("admission has exactly one Review step", len(admitted.Steps) == 1 && admitted.Steps[0].Kind == "START_REVIEW" && admitted.Steps[0].State == "COMPLETED" && admitted.Steps[0].ReviewID != "" && admitted.Steps[0].RunID != "", admitted.Steps)
	f.adoptReview(admitted)
	s.check("confirmation and access agree on admitted Review and Run", admitted.Steps[0].RunID == f.access.Target.RunID && f.access.Target.ReviewID == f.review, f.access.Target)
	seen := map[string]bool{}
	for _, target := range f.access.RepositoryTargets {
		name := ""
		for _, candidate := range []string{"app", "sdk"} {
			if target.RepositoryID == f.repos[candidate] {
				name = candidate
			}
		}
		s.check("initial Review target is exact read-only fixture member", name != "" && !seen[name] && target.OriginCommit == f.heads[name] && target.OriginRef == "refs/heads/"+name+"-development" && target.Read && target.Write.Kind == "NONE" && target.MemberID != "" && target.MemberVersion > 0, target)
		seen[name] = true
	}
	f.checkAdmissionReplay(admitted, f.commitAdmission(reviewInput))
	f.checkAdmissionReplay(admitted, f.readAdmission(reviewPrepared, reviewInput.RequestID))
	f.checkAdmissionObjects("exact Review replay admits only one Run", 1, 1)
	page := s.http(http.MethodGet, "/ui/api/tasks/"+url.PathEscape(f.task)+"/runs?project_id="+url.QueryEscape(f.project)+"&limit=100&offset=0", nil)
	items, ok := page["items"].([]any)
	s.check("Task Run history is complete and singular", ok && len(items) == 1 && page["total"] == float64(1) && page["offset"] == float64(0), page)
	owned := items[0].(map[string]any)
	s.check("only admitted scope Review owns the Run", owned["id"] == f.access.Target.RunID && owned["reviewId"] == f.review && owned["runPurpose"] == "SCOPE_REVIEW" && emptyField(owned, "taskExecutionId"), owned)
	ws := f.workspace()
	current := ws["task"].(map[string]any)
	s.check("Review admission is not implementation", current["currentReviewId"] == f.review && current["phase"] == "REVIEWING" && current["status"] == "ACTIVE" && emptyField(current, "currentExecutionId") && emptyField(current, "codexConfigId"), current)
	history, ok := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	s.check("Review history contains exactly one admitted Review", ok && len(history) == 1, history)
	reviewed := history[0].(map[string]any)
	run := reviewed["run"].(map[string]any)
	s.check("unclaimed Review has no invented result or native execution", reviewed["id"] == f.review && reviewed["executorKind"] == "UPPER_LLM" && reviewed["resultAvailable"] == false && run["id"] == f.access.Target.RunID && run["nativeResultState"] == "UNAVAILABLE", reviewed)
	f.checkNoImplementation(ws)
}

func admissionRead(intent c.Intent, request string) c.ReadInput {
	return c.ReadInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, ContentHash: intent.ContentHash, RequestID: request}
}
func admissionCommit(intent c.Intent, request string, selection c.Selection) c.CommitInput {
	return c.CommitInput{AISessionID: intent.AISessionID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, ContentHash: intent.ContentHash, RequestID: request, VariantID: intent.Content.Variants[0].ID, Selection: selection}
}
func (f *fixture) readAdmission(intent c.Intent, request string) c.Intent {
	var read c.Intent
	decode(f.s.gpt.call("confirmation_get", admissionRead(intent, request))["intent"], &read)
	return read
}
func (f *fixture) commitAdmission(input c.CommitInput) c.Intent {
	var committed c.Intent
	decode(f.s.gpt.callAs("fixture_user", "confirmation_commit", input)["intent"], &committed)
	sub := committed.Submission
	f.s.check("fixture choice consumed with exact recorded payload", committed.State == "COMPLETED" && sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.PayloadSHA256 == c.ValueHash(input) && sub.ReceiptRequestID == input.RequestID, committed)
	return committed
}
func (f *fixture) refuseConfirmation(input c.CommitInput, code string) c.Result {
	out := f.s.gpt.invoke("fixture_user", "confirmation_commit", input)
	f.s.must(out.Err)
	var result c.Result
	decode(out.Value, &result)
	f.s.check("typed confirmation refusal: "+code, out.Denied && result.Failure != nil && result.Failure.Code == code, result)
	return result
}
func (f *fixture) checkEditableRefusal(intent c.Intent, input c.CommitInput) {
	refused := f.refuseConfirmation(input, "CONFIRMATION_VARIANT_UNAVAILABLE")
	read := f.readAdmission(intent, input.RequestID)
	sub := read.Submission
	f.s.check("invalid variant leaves proposal unconsumed", read.State == "PENDING" && read.Commit == nil && len(read.Steps) == 0 && sub != nil && sub.Recorded && sub.Outcome == c.RejectedNotConsumed && sub.Failure != nil && sub.Failure.Code == "CONFIRMATION_VARIANT_UNAVAILABLE", read)
	f.s.check("rejected request has exact payload and no consumption receipt", sub.PayloadSHA256 == c.ValueHash(input) && reflect.DeepEqual(sub.Request, input) && sub.ReceiptRequestID == "" && refused.Intent != nil && reflect.DeepEqual(refused.Intent.Submission, sub), sub)
	f.s.check("Core explicitly permits editing the rejected choice", sub.Consumed != nil && !*sub.Consumed && sub.BusinessOutcome == "REJECTED_NOT_CONSUMED" && sub.ConsumptionDisposition == "NOT_CONSUMED" && sub.NextAction == c.EditSelection && sub.RetryDisposition == c.RetryEditSelectionAndResubmit, sub)
}
func (f *fixture) checkAdmissionReplay(original, replay c.Intent) {
	f.s.check("exact confirmation replay preserves frozen receipt and business identities", replay.State == original.State && replay.IntentID == original.IntentID && replay.Version == original.Version && replay.ContentHash == original.ContentHash && reflect.DeepEqual(replay.Content, original.Content) && reflect.DeepEqual(replay.Commit, original.Commit) && reflect.DeepEqual(replay.Steps, original.Steps) && reflect.DeepEqual(replay.Submission, original.Submission), replay)
}
func (f *fixture) checkAdmissionObjects(label string, taskCount, runCount int) {
	for _, probe := range []struct {
		path string
		want int
	}{{"tasks", taskCount}, {"runs", runCount}} {
		page := f.s.http(http.MethodGet, "/v1/"+probe.path+"?project_id="+url.QueryEscape(f.project)+"&limit=100&offset=0", nil)
		items, ok := page["items"].([]any)
		f.s.check(label+": complete public "+probe.path+" count", ok && len(items) == probe.want && page["has_more"] == false && page["offset"] == float64(0), page)
	}
}
func (f *fixture) checkUnstartedTask(label string) {
	ws := f.checkPhase(label, "ACTIVE", "REVIEW_REQUIRED")
	current := ws["task"].(map[string]any)
	f.s.check(label+": no Review, execution, or native configuration", emptyField(current, "currentReviewId") && emptyField(current, "currentExecutionId") && emptyField(current, "codexConfigId"), current)
	history, ok := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check(label+": no historical Review", ok && len(history) == 0, history)
	f.checkNoImplementation(ws)
}
func (f *fixture) checkNoImplementation(ws map[string]any) {
	view := ws["taskView"].(map[string]any)
	file := view["directFile"].(map[string]any)
	history, ok := view["executionHistory"].([]any)
	f.s.check("initial lifecycle has no implementation authority or execution history", file["enabled"] == false && file["state"] == "UNAVAILABLE" && ok && len(history) == 0, map[string]any{"directFile": file, "executionHistory": history})
}
func emptyField(value map[string]any, key string) bool { return value[key] == nil || value[key] == "" }
