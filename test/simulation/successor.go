package simulation

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/taskaction"
)

func (f *fixture) prepareReviewCheckpoint() (map[string]any, map[string]string) {
	f.startReview()
	f.claimReview()
	f.buildReviewReport()
	f.report.Report.RecommendedNextSteps = []string{"SIMULATED: request same-Task formal re-review or explicitly approve; no implementation or verification has run"}
	receipt := f.recordReviewReport()
	return receipt, f.managedFileSnapshot()
}
func (f *fixture) actionNode() taskaction.Node {
	var node taskaction.Node
	decode(f.workspace()["taskView"].(map[string]any)["actionNode"], &node)
	f.s.check("public action node has exact Task and observed positive generation", node.TaskID == f.task && node.Generation > 0, node)
	return node
}
func (f *fixture) checkUnchangedReviewCheckpoint(receipt map[string]any, files map[string]string) {
	f.checkRecordedReview(receipt, "REVIEW_APPROVAL_PENDING", f.revision, files)
	f.checkReviewResultReplays(receipt)
}
func (f *fixture) refusePreparation(a *actor, tool string, args any, code string) callOutcome {
	out := a.invoke("upstream_gpt", tool, args)
	f.s.must(out.Err)
	var result c.Result
	decode(out.Value, &result)
	f.s.check("exact preparation refusal: "+code, out.Denied && result.Intent == nil && result.Failure != nil && result.Failure.Code == code, result)
	return out
}
func (s *scenario) taskActionGates() {
	f := s.fixture
	receipt, files := f.prepareReviewCheckpoint()
	before := f.actionNode()
	proposal := f.approvalPreparation("APPROVE", "current-generation-proposal")
	proposal.TaskActionGeneration = before.Generation
	args := object(proposal)
	s.must(s.gpt.validateInput("execution_prepare", args))
	for _, kind := range []string{"nested", "zero", "fraction", "string"} {
		bad := object(args)
		switch kind {
		case "nested":
			delete(bad, "taskActionGeneration")
			bad["execution"].(map[string]any)["taskActionGeneration"] = before.Generation
		case "zero":
			bad["taskActionGeneration"] = 0
		case "fraction":
			bad["taskActionGeneration"] = 1.5
		case "string":
			bad["taskActionGeneration"] = "1"
		}
		err := s.gpt.validateInput("execution_prepare", bad)
		s.check("advertised schema rejects "+kind+" generation before dispatch (not a domain test)", err != nil, nil)
		s.record(map[string]any{"kind": "expected-schema-rejection", "tool": "execution_prepare", "case": kind, "arguments": bad, "error": err.Error()})
	}
	stale := proposal
	stale.RequestID = "stale-generation-proposal"
	stale.TaskActionGeneration++
	f.refusePreparation(s.gpt, "execution_prepare", stale, "TASK_ACTION_STALE")
	s.check("schema/stale refusal does not reserve another Task action", reflect.DeepEqual(f.actionNode(), before), nil)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	var prepared c.Intent
	decode(s.gpt.call("execution_prepare", proposal)["intent"], &prepared)
	s.check("top-level current generation freezes pending proposal only", prepared.State == "PENDING" && prepared.Content.TaskActionGeneration == before.Generation && prepared.Commit == nil && len(prepared.Steps) == 0, prepared)
	owned := f.actionNode()
	s.check("one pending action is owned by exact preparation", owned.Owned && owned.IntentID == prepared.IntentID && owned.Blocking, owned)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	// Local worker ingress intentionally does not advertise execution_prepare.
	// A competing author must be a second genuinely initialized/bound GPT Session.
	other := *f
	other.session = field(s.gpt.call("session_initialize", map[string]any{"requestId": "initialize-competing-upstream"}), "aiSessionId")
	other.complete(c.PrepareInput{RequestID: "bind-competing-upstream", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Explicit fixture user binds second simulated author to the same disposable project"}, Variants: []c.Variant{variant("select", "Select only this existing disposable project")}}, c.Selection{ProjectID: f.project})
	s.check("competing upstream uses distinct real bound Session", other.session != f.session, nil)
	foreignRead := s.gpt.invoke("upstream_gpt", "workspace_get", map[string]any{"aiSessionId": other.session, "taskId": f.task, "featureAreaId": f.area, "include": []string{"TASK"}})
	s.must(foreignRead.Err)
	s.check("foreign workspace envelope never leaks private pending intent", !foreignRead.Denied && !strings.Contains(encoded(foreignRead.Envelope), prepared.IntentID) && !strings.Contains(foreignRead.ErrorText, prepared.IntentID), foreignRead.Envelope)
	foreignView := foreignRead.Value
	var foreignNode taskaction.Node
	decode(foreignView["taskView"].(map[string]any)["actionNode"], &foreignNode)
	s.check("foreign session sees blocking action without private confirmation identity", foreignNode.TaskID == f.task && foreignNode.Generation == owned.Generation && foreignNode.Blocking && !foreignNode.Owned && foreignNode.IntentID == "", foreignNode)
	competitor := proposal
	competitor.AISessionID = other.session
	competitor.RequestID = "other-session-proposal"
	competitor.TaskActionGeneration = foreignNode.Generation
	refused := f.refusePreparation(s.gpt, "execution_prepare", competitor, "TASK_ACTION_PENDING")
	s.check("losing session is not handed another session's confirmation", !strings.Contains(encoded(refused.Envelope), prepared.IntentID) && !strings.Contains(refused.ErrorText, prepared.IntentID), refused.Envelope)
	s.check("competing preparation cannot replace pending owner", reflect.DeepEqual(f.actionNode(), owned), nil)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	var discarded c.Intent
	decode(s.gpt.callAs("fixture_user", "confirmation_discard", admissionRead(prepared, ""))["intent"], &discarded)
	s.check("explicit discard retires only exact unconsumed proposal", discarded.State == "SUPERSEDED" && discarded.IntentID == prepared.IntentID && discarded.Commit == nil && len(discarded.Steps) == 0 && reflect.DeepEqual(discarded.Content, prepared.Content), discarded)
	after := f.actionNode()
	s.check("discard advances generation without creating business successor", after.Generation > owned.Generation && after.IntentID == "", after)
	stale = proposal
	stale.RequestID = "late-preparation-from-old-generation"
	f.refusePreparation(s.gpt, "execution_prepare", stale, "TASK_ACTION_STALE")
	f.refuseConfirmation(admissionCommit(prepared, "late-old-card", c.Selection{}), "CONFIRMATION_INTENT_NOT_CURRENT")
	current := proposal
	current.RequestID = "replacement-observed-generation"
	current.TaskActionGeneration = after.Generation
	var replacement c.Intent
	decode(s.gpt.call("execution_prepare", current)["intent"], &replacement)
	s.check("fresh proposal uses newly read generation, never rewrites old intent", replacement.IntentID != prepared.IntentID && replacement.State == "PENDING" && replacement.Content.TaskActionGeneration == after.Generation, replacement)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	s.stop(false)
	s.launch()
	read := f.readAdmission(replacement, "")
	s.check("cold pending replacement is not user approval", read.State == "PENDING" && read.Commit == nil && len(read.Steps) == 0 && reflect.DeepEqual(read.Content, replacement.Content), read)
	f.refuseConfirmation(admissionCommit(prepared, "late-old-card", c.Selection{}), "CONFIRMATION_INTENT_NOT_CURRENT")
	s.check("late historical card cannot steal replacement authority", f.actionNode().IntentID == replacement.IntentID, nil)
	decode(s.gpt.callAs("fixture_user", "confirmation_discard", admissionRead(replacement, ""))["intent"], &discarded)
	s.check("fixture user closes replacement without approving work", discarded.State == "SUPERSEDED" && discarded.Commit == nil, discarded)
	f.checkUnchangedReviewCheckpoint(receipt, files)
}

// Review decision wire deliberately contains no replacement scope/runtime/plan.
func (f *fixture) revisePreparation(request, feedback string, generation int64) map[string]any {
	return map[string]any{"aiSessionId": f.session, "requestId": request, "purpose": "REVIEW", "action": "REVISE", "taskActionGeneration": generation, "review": map[string]any{"taskId": f.task, "featureAreaId": f.area, "reviewId": f.review, "decisionId": "REVIEW_APPROVAL:" + f.review, "reviewFeedback": feedback}, "variants": []any{map[string]any{"id": "revise", "label": "SIMULATED user re-review", "summary": "Request a formal same-Task UPPER Review successor without replacing frozen scope"}}}
}

// Snapshot only immutable predecessor fields used by historical receipt reads;
// decode into fresh values so later successor writes cannot alias pointers/slices.
func (f *fixture) reviewPredecessor() *fixture {
	old := &fixture{s: f.s, project: f.project, session: f.session, task: f.task, area: f.area, review: f.review, revision: f.revision, reviewInstruction: f.reviewInstruction}
	decode(f.access, &old.access)
	decode(f.upperClaim, &old.upperClaim)
	decode(f.upperFirst, &old.upperFirst)
	decode(f.report, &old.report)
	return old
}
func (f *fixture) checkHistoricalWorkerEnvelope(old *fixture, receipt map[string]any) {
	for _, tool := range []string{"review_worker_get", "review_worker_claim"} {
		out := f.s.agent.invoke("downstream_agent", tool, old.upperClaim)
		f.s.must(out.Err)
		f.s.check("old worker only reads its original completed receipt", !out.Denied && out.Value["state"] == review.WorkerResultRecorded && out.Value["executionPayload"] == nil && reflect.DeepEqual(out.Value["receipt"], old.upperFirst["receipt"]) && reflect.DeepEqual(out.Value["resultReceipt"], receipt), out.Value)
		scope, ok := out.Envelope["context"].(map[string]any)
		workflow, wok := out.Envelope["workflow"].(map[string]any)
		f.s.check("old receipt envelope cannot borrow successor identity or next action", ok && wok && scope["taskId"] == old.task && scope["reviewId"] == old.review && scope["runId"] == old.access.Target.RunID && out.Envelope["refreshRequired"] == true && workflow["stale"] == true && workflow["nextAction"] == nil, out.Envelope)
	}
}

func (s *scenario) reviewSuccessorGates() {
	f := s.fixture
	receipt, files := f.prepareReviewCheckpoint()
	old := f.reviewPredecessor()
	initialDefinition := object(f.workspace()["taskSources"].(map[string]any)["selectedDefinition"])
	node := f.actionNode()
	invalid := f.revisePreparation("invalid-nul-review-feedback", "bounded feedback\x00", node.Generation)
	s.gpt.deny("review_prepare", invalid, "REVISE requires non-empty bounded reviewFeedback")
	s.check("invalid successor feedback leaves current action unchanged", reflect.DeepEqual(f.actionNode(), node), nil)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	feedback := "SIMULATED fixture user: re-check both exact same.txt contracts.\nPreserve original scope; do not implement or claim verification."
	proposal := f.revisePreparation("prepare-same-task-successor", feedback, node.Generation)
	var prepared c.Intent
	decode(s.gpt.call("review_prepare", proposal)["intent"], &prepared)
	s.check("successor preparation freezes only exact predecessor and bounded feedback", prepared.State == "PENDING" && prepared.Commit == nil && len(prepared.Steps) == 0 && prepared.Content.TaskActionGeneration == node.Generation && prepared.Content.Review.ReviewID == old.review && prepared.Content.Review.ReviewFeedback == feedback, prepared)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	input := admissionCommit(prepared, "confirm-same-task-successor", c.Selection{})
	bad := input
	bad.RequestID = "invalid-successor-choice"
	bad.VariantID = "not-prepared"
	f.checkEditableRefusal(prepared, bad)
	f.checkUnchangedReviewCheckpoint(receipt, files)
	consumed := f.commitAdmission(input)
	s.check("one formal user decision creates exactly one successor Review/Run", len(consumed.Steps) == 1 && consumed.Steps[0].Kind == "RESOLVE_REVIEW_DECISION" && consumed.Steps[0].State == "COMPLETED" && consumed.Steps[0].TaskID == old.task && consumed.Steps[0].RevisionID == old.revision && consumed.Steps[0].ReviewID != "" && consumed.Steps[0].ReviewID != old.review && consumed.Steps[0].RunID != "" && consumed.Steps[0].RunID != old.access.Target.RunID && consumed.Steps[0].ExecutionID == "", consumed.Steps)
	f.review = consumed.Steps[0].ReviewID
	f.reviewInstruction = old.reviewInstruction + "\n\nRe-review feedback:\n" + feedback
	decode(s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &f.access)
	s.check("successor access retains exact original read-only workspace and members", (f.access.State == "PREPARING" || f.access.State == "ACTIVE") && f.access.ProtocolVersion == 2 && f.access.Target.ReviewID == f.review && f.access.Target.RunID == consumed.Steps[0].RunID && f.access.Target.TaskID == old.task && f.access.WorkspaceID == old.access.WorkspaceID && reflect.DeepEqual(f.access.RepositoryTargets, old.access.RepositoryTargets), f.access)
	target := f.access.Target
	decode(s.gpt.call("review_access_prepare", target), &f.access)
	s.check("explicit activation uses already admitted exact successor target", f.access.State == "ACTIVE" && f.access.Target == target && f.access.WorkspaceID == old.access.WorkspaceID && reflect.DeepEqual(f.access.RepositoryTargets, old.access.RepositoryTargets), f.access)
	f.upperClaim = review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "successor-upper-delivery", WorkerID: "SIMULATED-successor-review-worker"}}
	f.checkSameTaskSuccessor(old, receipt, nil, initialDefinition, files)
	old.checkReviewResultReplays(receipt)
	f.checkHistoricalWorkerEnvelope(old, receipt)
	f.claimReview()
	var claimed review.WorkerClaimResult
	decode(f.upperFirst, &claimed)
	s.check("new claim carries inherited authored instruction plus exact user feedback only", claimed.ExecutionPayload != nil && claimed.ExecutionPayload.Instruction == f.reviewInstruction && claimed.Receipt.UpperTarget == target && claimed.Receipt.InstructionSHA256 == digestText(f.reviewInstruction) && claimed.Receipt.PayloadSHA256 == reviewPayloadDigest(claimed.ExecutionPayload) && reflect.DeepEqual(claimed.ExecutionPayload.Access, f.access), claimed)
	f.buildReviewReport()
	f.report.RequestID = "successor-review-result"
	f.report.Report.RecommendedNextSteps = []string{"SIMULATED: separate approval only; successor review has not implemented or verified work"}
	nextReceipt := f.recordReviewReport()
	f.checkSameTaskSuccessor(old, receipt, nextReceipt, initialDefinition, files)
	f.checkReviewResultReplays(nextReceipt)
	old.checkReviewResultReplays(receipt)
	f.checkHistoricalWorkerEnvelope(old, receipt)
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	bad = input
	bad.VariantID = "altered-old-successor-choice"
	f.refuseConfirmation(bad, "CONFIRMATION_REQUEST_CONFLICT")
	var discard c.Intent
	decode(s.gpt.callAs("fixture_user", "confirmation_discard", admissionRead(prepared, ""))["intent"], &discard)
	s.check("discarding consumed predecessor reads receipt, never rolls back successor", discard.State == "COMPLETED" && reflect.DeepEqual(discard.Steps, consumed.Steps) && reflect.DeepEqual(discard.Commit, consumed.Commit), discard)
	f.checkSameTaskSuccessor(old, receipt, nextReceipt, initialDefinition, files)
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(consumed, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	f.checkReviewResultReplays(nextReceipt)
	old.checkReviewResultReplays(receipt)
	f.checkHistoricalWorkerEnvelope(old, receipt)
	f.checkSameTaskSuccessor(old, receipt, nextReceipt, initialDefinition, files)
	// This is a formal same-Task Review successor, not native CONTINUE_TASK,
	// recovery of a killed worker, a new implementation, or Task acceptance.
}
func (f *fixture) checkSameTaskSuccessor(old *fixture, receipt, nextReceipt, definition map[string]any, files map[string]string) {
	phase := "REVIEWING"
	if nextReceipt != nil {
		phase = "REVIEW_APPROVAL_PENDING"
		var recorded review.ReportReceipt
		decode(nextReceipt, &recorded)
		f.s.check("successor result receipt binds only new exact report and Run", recorded.Status == "COMPLETED" && recorded.ReportID != "" && recorded.ReportID != receipt["reportId"] && recorded.ReviewID == f.review && recorded.RunID == f.access.Target.RunID && recorded.RequestID == f.report.RequestID && recorded.PayloadSHA256 == reviewPayloadDigest(f.report) && recorded.Disposition == f.report.Report.Disposition, recorded)
	}
	var access review.UpperAccess
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &access)
	expectedAccess := "ACTIVE"
	if nextReceipt != nil {
		expectedAccess = "CLOSED"
	}
	f.s.check("successor report closes only its exact immutable read authority", access.State == expectedAccess && access.Target == f.access.Target && access.WorkspaceID == old.access.WorkspaceID && reflect.DeepEqual(access.RepositoryTargets, old.access.RepositoryTargets), access)
	ws := f.checkPhase("same Task retains history across explicit Review successor", "ACTIVE", phase)
	f.s.check("successor does not replace Task or frozen definition", ws["task"].(map[string]any)["id"] == old.task && ws["task"].(map[string]any)["currentReviewId"] == f.review && reflect.DeepEqual(ws["taskSources"].(map[string]any)["selectedDefinition"], definition), ws["task"])
	f.checkNoImplementation(ws)
	history, ok := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check("exactly predecessor and successor Review remain visible", ok && len(history) == 2, history)
	seen := map[string]bool{}
	for _, raw := range history {
		item := raw.(map[string]any)
		id := field(item, "id")
		f.s.check("Review history is same Task, UPPER and exact unique owner", !seen[id] && (id == old.review || id == f.review) && item["taskId"] == f.task && item["executorKind"] == "UPPER_LLM", item)
		seen[id] = true
		run := item["run"].(map[string]any)
		expectedRun, expectedStatus := f.access.Target.RunID, "RUNNING"
		if id == old.review {
			expectedRun, expectedStatus = old.access.Target.RunID, "SUCCEEDED"
		} else if nextReceipt != nil {
			expectedStatus = "SUCCEEDED"
		}
		f.s.check("each Review Run is genuinely running or report-completed, never native", run["id"] == expectedRun && run["transportStatus"] == expectedStatus && run["nativeResultState"] == "UNAVAILABLE", run)
		if id == old.review {
			f.s.check("predecessor formal report remains indexed", item["reportId"] == receipt["reportId"], item)
		} else if nextReceipt != nil {
			f.s.check("successor formal report has distinct indexed identity", item["reportId"] == nextReceipt["reportId"] && item["reportId"] != receipt["reportId"], item)
		}
	}
	page := f.s.http(http.MethodGet, "/ui/api/tasks/"+url.PathEscape(f.task)+"/runs?project_id="+url.QueryEscape(f.project)+"&limit=100&offset=0", nil)
	items, complete := page["items"].([]any)
	f.s.check("successor Task Run page is complete and contains exactly two Runs", complete && len(items) == 2 && page["total"] == float64(2) && page["offset"] == float64(0), page)
	seenRuns := map[string]bool{}
	for _, raw := range items {
		item := raw.(map[string]any)
		id := field(item, "id")
		owner, expectedStatus := f.review, "RUNNING"
		if id == old.access.Target.RunID {
			owner, expectedStatus = old.review, "SUCCEEDED"
		} else if nextReceipt != nil {
			expectedStatus = "SUCCEEDED"
		}
		f.s.check("each historical Run has exact Review owner and no Task Execution", !seenRuns[id] && (id == old.access.Target.RunID || id == f.access.Target.RunID) && item["reviewId"] == owner && item["transportStatus"] == expectedStatus && item["runPurpose"] == "SCOPE_REVIEW" && emptyField(item, "taskExecutionId") && item["agentExecutionId"] == "review:"+owner && item["operationKind"] == "REVIEW" && emptyField(item, "model") && emptyField(item, "threadId") && emptyField(item, "turnId"), item)
		seenRuns[id] = true
	}
	f.checkAdmissionObjects("one Task and exactly two semantic Review Runs", 1, 2)
	f.s.check("Review successor never changes managed file bytes", reflect.DeepEqual(files, f.managedFileSnapshot()), nil)
	f.checkSources()
}
