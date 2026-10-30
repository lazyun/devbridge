package simulation

import (
	"net/http"
	"net/url"
	"reflect"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
)

func (s *scenario) canceledReviewSuccessor() {
	f := s.fixture
	f.startReview()
	f.claimReview()
	f.buildReviewReport() // Ground an otherwise valid old report before cancellation.
	ws := f.workspace()
	f.revision = field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	definition := object(ws["taskSources"].(map[string]any)["selectedDefinition"])
	old := f.reviewPredecessor()
	files := f.managedFileSnapshot()
	cancel := map[string]any{"aiSessionId": f.session, "ownerType": "REVIEW", "ownerId": f.review, "runId": f.access.Target.RunID, "requestId": "cancel-before-fresh-review", "reason": "SIMULATED fixture user cancels old delivery; a new Review is not worker continuation", "expectedUpperVersion": map[string]any{"authorityEpoch": f.access.Target.RuntimeEpoch, "grantEpoch": f.access.Target.RuntimeEpoch, "grantGeneration": f.access.Target.GrantGeneration}}
	canceled := s.agent.callAs("fixture_user", "operation_cancel", cancel)
	s.check("explicit Review cancellation owns only old semantic Review Run", canceled["state"] == "CANCELED" && canceled["backendDisposition"] == "NO_BACKEND" && canceled["ownerId"] == old.review && canceled["runId"] == old.access.Target.RunID && canceled["executionId"] == "review:"+old.review, canceled)
	f.checkCanceledReviewBeforeSuccessor(old, definition, files)
	bad := object(cancel)
	bad["reason"] = "changed old cancellation payload"
	s.agent.deny("operation_cancel", bad, "REQUEST_CONFLICT")
	f.checkCanceledReviewBeforeSuccessor(old, definition, files)
	proposal := f.reviewPreparation("prepare-after-canceled-review")
	proposal.TaskActionGeneration = f.actionNode().Generation
	proposal.Variants[0].ReviewInstruction = "SIMULATED: Fresh read-only Review after explicit cancellation. Read exact app/sdk contracts; never resume the old worker or implement changes."
	prepared := f.prepare(proposal)
	s.check("fresh Review prepare does not resurrect canceled delivery", prepared.State == "PENDING" && prepared.Commit == nil && len(prepared.Steps) == 0, prepared)
	f.checkCanceledReviewBeforeSuccessor(old, definition, files)
	input := admissionCommit(prepared, "confirm-after-canceled-review", c.Selection{})
	invalid := input
	invalid.RequestID, invalid.VariantID = "invalid-rereview-choice", "not-prepared"
	f.checkEditableRefusal(prepared, invalid)
	f.checkCanceledReviewBeforeSuccessor(old, definition, files)
	consumed := f.commitAdmission(input)
	s.check("one new confirmed Review, no native successor or implementation", len(consumed.Steps) == 1 && consumed.Steps[0].Kind == "START_REVIEW" && consumed.Steps[0].ReviewID != "" && consumed.Steps[0].ReviewID != old.review && consumed.Steps[0].RunID != "" && consumed.Steps[0].RunID != old.access.Target.RunID && consumed.Steps[0].TaskID == old.task && consumed.Steps[0].ExecutionID == "", consumed.Steps)
	f.adoptReview(consumed)
	s.check("new read grant retains exact originally authorized workspace and members", f.access.Target.RunID == consumed.Steps[0].RunID && f.access.WorkspaceID == old.access.WorkspaceID && reflect.DeepEqual(f.access.RepositoryTargets, old.access.RepositoryTargets), f.access)
	f.upperClaim.RequestID, f.upperClaim.WorkerID = "after-cancel-new-delivery", "SIMULATED-fresh-review-worker"
	f.checkReReviewPair(old, "CANCELED", "", nil, definition, files)
	f.checkCanceledHistory(old, cancel, canceled)
	f.claimReview()
	f.checkFreshReviewDispatch(old, proposal.Variants[0].ReviewInstruction)
	f.buildReviewReport()
	f.report.RequestID = "after-cancel-new-report"
	f.report.Report.RecommendedNextSteps = []string{"SIMULATED: a separate user approval is still required; no implementation or verification ran"}
	receipt := f.recordReviewReport()
	f.checkReviewResultReplays(receipt)
	f.checkCanceledHistory(old, cancel, canceled)
	f.checkReReviewPair(old, "CANCELED", "", receipt, definition, files)
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	changed := input
	changed.VariantID = "changed-consumed-choice"
	f.refuseConfirmation(changed, "CONFIRMATION_REQUEST_CONFLICT")
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(consumed, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	f.checkCanceledHistory(old, cancel, canceled)
	f.checkReviewResultReplays(receipt)
	f.checkReReviewPair(old, "CANCELED", "", receipt, definition, files)
}

func (f *fixture) checkCanceledReviewBeforeSuccessor(old *fixture, definition map[string]any, files map[string]string) {
	ws := f.checkPhase("canceled Review is not canceled Task or completed work", "ACTIVE", "REVIEW_REQUIRED")
	f.s.check("cancellation preserves Task identity, current Review and definition", ws["task"].(map[string]any)["currentReviewId"] == old.review && reflect.DeepEqual(ws["taskSources"].(map[string]any)["selectedDefinition"], definition), ws["task"])
	f.checkNoImplementation(ws)
	history := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check("no replacement Review exists before separate confirmation", len(history) == 1 && history[0].(map[string]any)["id"] == old.review && history[0].(map[string]any)["resultAvailable"] == false, history)
	var access review.UpperAccess
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": old.review}), &access)
	f.s.check("cancellation revokes exact old grant without a report", access.State == "CANCELED" && access.Target == old.access.Target, access)
	old.receipt("review_worker_get", old.upperClaim, old.upperFirst, review.WorkerOutcomeUnknown)
	f.s.agent.deny("review_result_submit", old.report, "REVIEW_TARGET_STALE")
	f.s.agent.deny("file_read", old.reviewReadArgs(old.access.RepositoryTargets[0]), "REVIEW_TARGET_STALE")
	f.checkAdmissionObjects("canceled original Review only", 1, 1)
	f.s.check("canceled/prepared/rejected Review preserves every managed file", reflect.DeepEqual(files, f.managedFileSnapshot()), nil)
	f.checkSources()
}

func (f *fixture) checkCanceledHistory(old *fixture, cancel, canceled map[string]any) {
	s := f.s
	replay := s.agent.invoke("fixture_user", "operation_cancel", cancel)
	s.must(replay.Err)
	s.check("historical cancellation remains exact after new Review", !replay.Denied && reflect.DeepEqual(replay.Value, canceled), replay.Value)
	f.checkStaleReviewEnvelope(old, replay.Envelope)
	for _, tool := range []string{"review_worker_get", "review_worker_claim"} {
		out := s.agent.invoke("downstream_agent", tool, old.upperClaim)
		s.must(out.Err)
		s.check("canceled old worker remains outcome unknown with no new payload/result", !out.Denied && out.Value["state"] == review.WorkerOutcomeUnknown && out.Value["executionPayload"] == nil && out.Value["resultReceipt"] == nil && reflect.DeepEqual(out.Value["receipt"], old.upperFirst["receipt"]), out.Value)
		f.checkStaleReviewEnvelope(old, out.Envelope)
	}
	s.agent.deny("review_result_submit", old.report, "REVIEW_TARGET_STALE")
	s.agent.deny("file_read", old.reviewReadArgs(old.access.RepositoryTargets[0]), "REVIEW_TARGET_STALE")
}
func (f *fixture) checkStaleReviewEnvelope(old *fixture, envelope map[string]any) {
	scope, ok := envelope["context"].(map[string]any)
	workflow, wok := envelope["workflow"].(map[string]any)
	f.s.check("old canceled receipt cannot adopt successor context or next action", ok && wok && scope["taskId"] == old.task && scope["reviewId"] == old.review && scope["runId"] == old.access.Target.RunID && emptyField(scope, "executionId") && envelope["refreshRequired"] == true && workflow["stale"] == true && workflow["nextAction"] == nil, envelope)
}

// A new Review can be admitted after an explicitly canceled predecessor. Its
// report is not a new implementation, acceptance, or revival of the old worker.
func (f *fixture) checkReReviewPair(old *fixture, oldStatus, oldReport string, receipt, definition map[string]any, files map[string]string) {
	phase, active, currentStatus := "REVIEWING", "ACTIVE", "RUNNING"
	if receipt != nil {
		phase, active, currentStatus = "REVIEW_APPROVAL_PENDING", "CLOSED", "SUCCEEDED"
	}
	ws := f.checkPhase("explicit re-review preserves active same Task", "ACTIVE", phase)
	f.s.check("re-review retains original exact definition and no Task Execution", ws["task"].(map[string]any)["id"] == old.task && ws["task"].(map[string]any)["currentReviewId"] == f.review && emptyField(ws["task"].(map[string]any), "currentExecutionId") && reflect.DeepEqual(ws["taskSources"].(map[string]any)["selectedDefinition"], definition), ws["task"])
	if f.state.WorkflowID == "" {
		f.checkNoImplementation(ws)
	} else {
		f.checkClosedFileAuthority(ws)
	}
	var access review.UpperAccess
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &access)
	f.s.check("new Review has exact independent read authority and closure", access.State == active && access.Target == f.access.Target && access.WorkspaceID == f.access.WorkspaceID && reflect.DeepEqual(access.RepositoryTargets, f.access.RepositoryTargets), access)
	if receipt != nil {
		var result review.ReportReceipt
		decode(receipt, &result)
		f.s.check("new formal report binds only its own Task Review Run", result.Status == "COMPLETED" && result.ReviewID == f.review && result.RunID == f.access.Target.RunID && result.ReportID != "" && result.ReportID != oldReport && result.RequestID == f.report.RequestID && result.PayloadSHA256 == reviewPayloadDigest(f.report) && result.Disposition == f.report.Report.Disposition, result)
	}
	history, ok := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check("re-review history contains exactly old and new semantic Reviews", ok && len(history) == 2, history)
	seen := map[string]bool{}
	for _, raw := range history {
		item := raw.(map[string]any)
		id := field(item, "id")
		runID, status, reportID := f.access.Target.RunID, currentStatus, ""
		if receipt != nil {
			reportID = field(receipt, "reportId")
		}
		if id == old.review {
			runID, status, reportID = old.access.Target.RunID, oldStatus, oldReport
		}
		run := item["run"].(map[string]any)
		f.s.check("history preserves exact Review Run status, report absence and no native result", !seen[id] && (id == old.review || id == f.review) && item["taskId"] == old.task && item["executorKind"] == "UPPER_LLM" && item["resultAvailable"] == (reportID != "") && (item["reportId"] == reportID || (reportID == "" && emptyField(item, "reportId"))) && run["id"] == runID && run["transportStatus"] == status && run["nativeResultState"] == "UNAVAILABLE", item)
		seen[id] = true
	}
	page := f.s.http(http.MethodGet, "/ui/api/tasks/"+url.PathEscape(f.task)+"/runs?project_id="+url.QueryEscape(f.project)+"&limit=100&offset=0", nil)
	items, complete := page["items"].([]any)
	f.s.check("re-review Run page is complete with exactly two owners", complete && len(items) == 2 && page["total"] == float64(2) && page["offset"] == float64(0), page)
	seen = map[string]bool{}
	for _, raw := range items {
		item := raw.(map[string]any)
		id := field(item, "id")
		owner, status := f.review, currentStatus
		if id == old.access.Target.RunID {
			owner, status = old.review, oldStatus
		}
		f.s.check("each Run is exact semantic Review, never native Task continuation", !seen[id] && (id == old.access.Target.RunID || id == f.access.Target.RunID) && item["reviewId"] == owner && item["runPurpose"] == "SCOPE_REVIEW" && item["transportStatus"] == status && emptyField(item, "taskExecutionId") && item["agentExecutionId"] == "review:"+owner && emptyField(item, "model") && emptyField(item, "threadId") && emptyField(item, "turnId"), item)
		seen[id] = true
	}
	f.checkAdmissionObjects("one Task and exactly two Reviews after explicit re-review", 1, 2)
	f.s.check("new read-only Review preserves every managed file", reflect.DeepEqual(files, f.managedFileSnapshot()), nil)
	f.checkSources()
}

func (f *fixture) checkFreshReviewDispatch(old *fixture, instruction string) {
	var dispatch review.WorkerClaimResult
	decode(f.upperFirst, &dispatch)
	f.s.check("fresh dispatch has exact instruction and read authority, never old delivery", dispatch.ExecutionPayload != nil && dispatch.ExecutionPayload.Instruction == instruction && reflect.DeepEqual(dispatch.ExecutionPayload.Access, f.access) && dispatch.Receipt.ClaimID != old.upperFirst["receipt"].(map[string]any)["claimId"] && dispatch.Receipt.UpperTarget == f.access.Target && dispatch.Receipt.WorkerClaimIdentity == f.upperClaim.WorkerClaimIdentity && dispatch.Receipt.PayloadSHA256 == reviewPayloadDigest(dispatch.ExecutionPayload) && dispatch.Receipt.InstructionSHA256 == digestText(instruction), dispatch)
}
