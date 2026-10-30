package simulation

import (
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (f *fixture) supplementalCapability() map[string]any {
	ws := f.workspace()
	view := ws["taskView"].(map[string]any)
	actions, ok := view["actions"].([]any)
	f.s.check("supplemental eligibility is authoritative public action data", ok && view["actionsStatus"] == "AVAILABLE", view["actionsStatus"])
	var found map[string]any
	for _, raw := range actions {
		item := raw.(map[string]any)
		if item["action"] == "REQUEST_TASK_REVIEW" {
			f.s.check("one exact supplemental capability", found == nil, item)
			found = item
		}
	}
	f.s.check("same Task exposes supplemental eligibility, not invented authority", found != nil, actions)
	return found
}
func (f *fixture) supplementalPreparation(id string, cap map[string]any) map[string]any {
	target := cap["target"].(map[string]any)
	observed := target["expectedReviewRequest"].(map[string]any)
	f.s.check("supplemental target contains all five explicit identity strings", len(observed) == 5 && observed["phase"] == "IMPLEMENTING" && observed["taskDefinitionRevisionId"] == f.revision && observed["currentReviewId"] == f.review && observed["currentExecutionId"] == "" && observed["pendingDecisionId"] == "", observed)
	var exact wp.TaskReviewRequestTarget
	decode(observed, &exact)
	in := f.reviewPreparation(id)
	in.AISessionID, in.Action = f.session, "REQUEST_SUPPLEMENTAL_REVIEW"
	in.TaskActionGeneration = f.actionNode().Generation
	in.Review.RepositoryTargets = f.access.RepositoryTargets
	in.Review.WorkspaceID = f.access.WorkspaceID
	in.Review.SupplementalPurpose = "SIMULATED fixture user requests a new review after canceling partial file work; preserve acknowledged bytes and the old approved definition until separately approved"
	in.Review.ExpectedTarget = &exact
	in.Variants[0].ReviewInstruction = "SIMULATED: Review exact app and sdk members after explicit file cancellation. Do not resume the old file worker, mutate files, accept work, or claim verification."
	args := object(in)
	args["review"].(map[string]any)["repositoryId"] = ""
	args["review"].(map[string]any)["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
	return args
}
func (f *fixture) checkClosedFileAuthority(ws map[string]any) {
	f.checkFrozenStartDefinition(ws)
	var state directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	f.s.check("supplemental Review never reopens canceled partial file authority", state.State == "CLOSED" && !state.Enabled && state.WorkflowID == f.state.WorkflowID && state.TaskDefinitionRevisionID == f.revision && state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration && state.WorkspaceID == f.state.WorkspaceID && reflect.DeepEqual(state.RepositoryTargets, f.state.RepositoryTargets), state)
	view := ws["taskView"].(map[string]any)
	f.s.check("partial file work remains uncommitted, not verified or accepted", view["commitStatus"] == "UNCOMMITTED" && reflect.DeepEqual(view["currentVerificationSpec"], object(f.frozen.VerificationSpec)) && f.frozen.VerificationSpec.Mode == task.VerificationModeNone, view["commitStatus"])
	for _, tool := range []string{"file_worker_get", "file_worker_claim"} {
		out := f.s.agent.invoke("downstream_agent", tool, f.claim)
		f.s.must(out.Err)
		worker := out.Value
		f.s.check("old file worker remains unknown without a manufactured result or redispatch", !out.Denied && worker["state"] == directfile.WorkerOutcomeUnknown && worker["executionPayload"] == nil && worker["resultReceipt"] == nil && reflect.DeepEqual(worker["receipt"], f.first["receipt"]), worker)
		scope, ok := out.Envelope["context"].(map[string]any)
		workflow, wok := out.Envelope["workflow"].(map[string]any)
		f.s.check("historical file receipt keeps exact old file scope and no successor actions", ok && wok && reflect.DeepEqual(scope, object(f.claim.WorkerTarget)) && len(workflow) == 0 && out.Envelope["refreshRequired"] != true && !strings.Contains(encoded(out.Envelope), f.review), out.Envelope)
	}
	f.s.gpt.deny("file_implementation_result_get", directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID}, "NOT_FOUND")
}
func (s *scenario) canceledFileSupplementalReview() {
	f := s.fixture
	expected := f.mutationSetup()
	old := f.reviewPredecessor()
	oldReceipt := s.gpt.call("review_result_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review, "requestId": f.report.RequestID})["receipt"].(map[string]any)
	definition := object(f.workspace()["taskSources"].(map[string]any)["selectedDefinition"])
	target := f.targets[0]
	content := "SIMULATED acknowledged partial effect retained across supplemental review\n"
	proof := f.mutate(f.mutationInput(target, "REPLACE", "same.txt", "partial-before-supplemental", content, digestText(expected[target.MemberID]["same.txt"]), ""), digestText(expected[target.MemberID]["same.txt"]), digestText(content))
	expected[target.MemberID]["same.txt"] = content
	f.checkMemberFiles(expected)
	files := f.managedFileSnapshot()
	unavailable := f.supplementalCapability()
	reason, _ := unavailable["reason"].(string)
	s.check("live v2 file grant must be explicitly canceled before supplemental Review", unavailable["available"] == false && reason == "DIRECT_FILE_EXPLICIT_CANCEL_REQUIRED", unavailable)
	blocked := f.supplementalPreparation("forbidden-active-supplemental", unavailable)
	out := f.refusePreparation(s.gpt, "review_prepare", blocked, "INVALID_ARGUMENT")
	s.check("preparation refusal is specifically active-file authority, not schema", strings.Contains(out.ErrorText, reason), out.Envelope)
	f.checkPartialFiles("supplemental denial does not cancel active file work", "IMPLEMENTING", "ACTIVE")
	s.check("denied supplemental preserves all partial bytes", reflect.DeepEqual(files, f.managedFileSnapshot()), nil)
	cancelPrepared := f.prepare(f.directPreparation("CANCEL_DIRECT_FILE", "prepare-supplemental-file-cancel"))
	cancelInput := admissionCommit(cancelPrepared, "confirm-supplemental-file-cancel", c.Selection{})
	canceled := f.commitAdmission(cancelInput)
	f.checkPartialFiles("explicit cancel preserves partial implementation", "IMPLEMENTING", "CLOSED")
	f.checkAdmissionReplay(canceled, f.commitAdmission(cancelInput))
	cap := f.supplementalCapability()
	s.check("only after explicit cancellation can same-Task supplemental Review proceed", cap["available"] == true && cap["requiredReviewDepth"] == "DEEP" && reflect.DeepEqual(cap["allowedReviewDepths"], []any{"DEEP"}), cap)
	proposal := f.supplementalPreparation("prepare-after-partial-file-cancel", cap)
	bad := object(proposal)
	bad["requestId"] = "stale-supplemental-target"
	bad["review"].(map[string]any)["expectedTarget"].(map[string]any)["phase"] = "IMPLEMENTATION_READY"
	denied := f.refusePreparation(s.gpt, "review_prepare", bad, "INVALID_ARGUMENT")
	s.check("changed complete observed target is refused before effects", strings.Contains(denied.ErrorText, "supplemental target changed"), denied.Envelope)
	bad = object(proposal)
	bad["requestId"] = "shallow-supplemental-review"
	bad["review"].(map[string]any)["reviewDepth"] = "FAST"
	f.refusePreparation(s.gpt, "review_prepare", bad, "REVIEW_DEPTH_TOO_SHALLOW")
	f.checkPartialFiles("invalid supplemental input cannot reopen canceled work", "IMPLEMENTING", "CLOSED")
	var prepared c.Intent
	decode(s.gpt.call("review_prepare", proposal)["intent"], &prepared)
	s.check("supplemental prepare freezes exact observed target without creating successor", prepared.State == "PENDING" && prepared.Commit == nil && len(prepared.Steps) == 0 && prepared.Content.Review.ExpectedTarget != nil && reflect.DeepEqual(object(prepared.Content.Review.ExpectedTarget), proposal["review"].(map[string]any)["expectedTarget"]), prepared)
	f.checkPartialFiles("pending supplemental is not Review admission", "IMPLEMENTING", "CLOSED")
	s.check("pending supplemental leaves all acknowledged effects intact", reflect.DeepEqual(files, f.managedFileSnapshot()), nil)
	s.stop(false)
	s.launch()
	read := f.readAdmission(prepared, "")
	s.check("cold pending supplemental retains exact observed target and no business steps", read.State == "PENDING" && read.Commit == nil && len(read.Steps) == 0 && reflect.DeepEqual(read.Content, prepared.Content), read)
	f.checkPartialFiles("cold pending supplemental still has no successor", "IMPLEMENTING", "CLOSED")
	input := admissionCommit(prepared, "confirm-partial-file-supplemental", c.Selection{})
	badChoice := input
	badChoice.RequestID, badChoice.VariantID = "invalid-supplemental-choice", "not-prepared"
	f.checkEditableRefusal(prepared, badChoice)
	f.checkPartialFiles("invalid supplemental choice cannot admit Review", "IMPLEMENTING", "CLOSED")
	consumed := f.commitAdmission(input)
	s.check("explicit supplemental decision requests and starts exactly one same-Task Review", len(consumed.Steps) == 2 && consumed.Steps[0].Kind == "REQUEST_REVIEW" && consumed.Steps[1].Kind == "START_REVIEW" && consumed.Steps[0].State == "COMPLETED" && consumed.Steps[1].State == "COMPLETED" && consumed.Steps[0].TaskID == f.task && consumed.Steps[1].TaskID == f.task && consumed.Steps[1].ReviewID != "" && consumed.Steps[1].ReviewID != old.review && consumed.Steps[1].RunID != "" && consumed.Steps[1].RunID != old.access.Target.RunID && consumed.Steps[0].RunID == "" && consumed.Steps[1].ExecutionID == "", consumed.Steps)
	admission := consumed
	admission.Steps = []c.Step{consumed.Steps[1]}
	f.adoptReview(admission)
	f.upperClaim.RequestID, f.upperClaim.WorkerID = "supplemental-review-delivery", "SIMULATED-supplemental-review-worker"
	s.check("supplemental Review is exact previously authorized read-only member set", f.access.Target.RunID == consumed.Steps[1].RunID && f.access.WorkspaceID == old.access.WorkspaceID && reflect.DeepEqual(f.access.RepositoryTargets, old.access.RepositoryTargets), f.access)
	f.checkReReviewPair(old, "SUCCEEDED", field(oldReceipt, "reportId"), nil, definition, files)
	f.checkSupplementalProvenance(proposal, consumed, old)
	f.replayMutation(proof)
	f.claimReview()
	f.checkFreshReviewDispatch(old, prepared.Content.Variants[0].ReviewInstruction)
	for _, member := range f.access.RepositoryTargets {
		read := s.agent.call("file_read", f.reviewReadArgs(member))
		want := expected[member.MemberID]["same.txt"]
		s.check("supplemental Review actually reads preserved partial or untouched member bytes", read["content"] == want && read["digest"] == digestText(want), read)
	}
	f.buildReviewReport()
	f.report.RequestID = "supplemental-review-report"
	f.report.Report.RecommendedNextSteps = []string{"SIMULATED: only separate user approval could replace the old definition; no implementation result or verification has run"}
	receipt := f.recordReviewReport()
	f.checkReviewResultReplays(receipt)
	old.checkReviewResultReplays(oldReceipt)
	f.checkHistoricalWorkerEnvelope(old, oldReceipt)
	f.checkReReviewPair(old, "SUCCEEDED", field(oldReceipt, "reportId"), receipt, definition, files)
	f.checkSupplementalProvenance(proposal, consumed, old)
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	f.checkAdmissionReplay(canceled, f.commitAdmission(cancelInput))
	f.replayMutation(proof)
	late := object(proof.Input)
	late["requestId"], late["content"], late["expectedDigest"] = "late-old-file-after-supplemental", "forbidden", proof.Result["digest"]
	f.denyMutation(late, "exact v2 file workflow is not active")
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(consumed, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(consumed, f.commitAdmission(input))
	f.checkAdmissionReplay(canceled, f.commitAdmission(cancelInput))
	f.replayMutation(proof)
	f.denyMutation(late, "exact v2 file workflow is not active")
	f.checkReviewResultReplays(receipt)
	old.checkReviewResultReplays(oldReceipt)
	f.checkHistoricalWorkerEnvelope(old, oldReceipt)
	f.checkReReviewPair(old, "SUCCEEDED", field(oldReceipt, "reportId"), receipt, definition, files)
	f.checkSupplementalProvenance(proposal, consumed, old)
}
func (f *fixture) checkSupplementalProvenance(proposal map[string]any, consumed c.Intent, old *fixture) {
	ws := f.workspace()
	raw := ws["taskView"].(map[string]any)["currentReviewRequest"]
	var request wp.TaskReviewRequestResolution
	decode(raw, &request)
	body := proposal["review"].(map[string]any)
	f.s.check("supplemental provenance retains exact old definition/Review and explicit purpose", request.ID != "" && request.TaskID == old.task && request.FeatureAreaID == old.area && request.ProjectID == f.project && request.SourceRevisionID == old.revision && request.SourceReviewID == old.review && request.SourceExecutionID == "" && request.SupersededDecisionID == "" && request.RequestID == consumed.Steps[0].RequestID && request.Purpose == body["supplementalPurpose"] && reflect.DeepEqual(object(request.ExpectedTarget), body["expectedTarget"]) && request.Phase == task.PhaseReviewRequired, raw)
}
