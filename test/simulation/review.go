package simulation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// Review worker/result receipts hash the exact typed JSON bytes. Confirmation
// receipts instead canonicalize JSON; confirmation.ValueHash is not equivalent.
func reviewPayloadDigest(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(scenarioFailure{err.Error()})
	}
	return digestText(string(raw))
}

func (s *scenario) reviewGates() {
	f := s.fixture
	f.startReview()
	ws := f.workspace()
	initialRevision := field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	baseline := f.managedFileSnapshot()
	stale := f.upperClaim
	stale.RuntimeEpoch = "not-the-admitted-epoch"
	s.agent.deny("review_worker_claim", stale, "REVIEW_TARGET_STALE")
	f.checkPendingReview("stale claim", initialRevision, baseline)
	f.claimReview()
	var claimed review.WorkerClaimResult
	decode(f.upperFirst, &claimed)
	s.check("Review delivery preserves confirmed instruction and exact access", claimed.ExecutionPayload != nil && f.reviewInstruction != "" && claimed.ExecutionPayload.Instruction == f.reviewInstruction && reflect.DeepEqual(claimed.ExecutionPayload.Access, f.access), claimed)
	s.check("Review receipt hashes bind delivered instruction and payload", claimed.Receipt.InstructionSHA256 == digestText(f.reviewInstruction) && claimed.Receipt.PayloadSHA256 == reviewPayloadDigest(claimed.ExecutionPayload) && claimed.Receipt.UpperTarget == f.access.Target, claimed.Receipt)
	for _, identity := range []string{"worker", "request"} {
		changed := f.upperClaim
		if identity == "worker" {
			changed.WorkerID = "another-worker-label"
		} else {
			changed.RequestID = "another-delivery-request"
		}
		s.agent.deny("review_worker_claim", changed, "Review requestId was already used with a different payload")
		f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerOutcomeUnknown)
		f.checkPendingReview("changed claim "+identity, initialRevision, baseline)
	}
	first := f.access.RepositoryTargets[0]
	for _, kind := range []string{"member", "repository", "path"} {
		args := f.reviewReadArgs(first)
		code := "REVIEW_TARGET_STALE"
		switch kind {
		case "member":
			args["memberId"] = f.access.RepositoryTargets[1].MemberID
		case "repository":
			args["repositoryId"] = f.repos["backend"]
		case "path":
			args["path"], code = "../same.txt", "REVIEW_SCOPE_FORBIDDEN"
		}
		s.agent.deny("file_read", args, code)
		f.checkPendingReview("read fence "+kind, initialRevision, baseline)
	}
	// File reads use the admitted Review identity, not a pretend exclusive worker
	// identity. WorkerID is correlation only, never proof of a model/process.
	f.buildReviewReport()
	f.report.Report.RecommendedNextSteps = []string{"Explicit fixture-user approval only; preserve RUN verification and do not start implementation"}
	for _, kind := range []string{"missing-worker", "wrong-worker", "digest", "evidence-member", "target-origin"} {
		var invalid review.SubmitInput
		decode(f.report, &invalid)
		invalid.RequestID = "invalid-report-" + kind
		code := "REVIEW_TARGET_STALE"
		switch kind {
		case "missing-worker":
			invalid.WorkerClaim = nil
		case "wrong-worker":
			invalid.WorkerClaim.WorkerID = "wrong-worker-label"
		case "digest":
			invalid.Report.EvidenceRefs[0].Digest = strings.Repeat("0", 64)
			code = "REVIEW_EVIDENCE_MISMATCH"
		case "evidence-member":
			invalid.Report.EvidenceRefs[0].MemberID = invalid.Report.EvidenceRefs[1].MemberID
			code = "REVIEW_EVIDENCE_MISMATCH"
		case "target-origin":
			invalid.Report.RepositoryTargets[0].OriginCommit = strings.Repeat("0", 40)
		}
		s.agent.deny("review_result_submit", invalid, code)
		f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerOutcomeUnknown)
		f.checkPendingReview("rejected report "+kind, initialRevision, baseline)
	}
	receipt := f.recordReviewReport()
	f.checkRecordedReview(receipt, "REVIEW_APPROVAL_PENDING", initialRevision, baseline)
	f.checkReviewResultReplays(receipt)
	s.stop(false)
	s.launch()
	f.checkReviewResultReplays(receipt)
	f.checkRecordedReview(receipt, "REVIEW_APPROVAL_PENDING", initialRevision, baseline)

	prepared := f.prepare(f.approvalPreparation("APPROVE", "review-gates-approve"))
	f.checkPhase("approval preparation is not user approval", "ACTIVE", "REVIEW_APPROVAL_PENDING")
	f.checkNoImplementation(f.workspace())
	input := admissionCommit(prepared, "review-gates-approval-choice", c.Selection{})
	bad := input
	bad.ContentHash, bad.RequestID = strings.Repeat("0", 64), "bad-review-approval-hash"
	f.refuseConfirmation(bad, "CONFIRMATION_READ_IDENTITY_MISMATCH")
	f.checkRecordedReview(receipt, "REVIEW_APPROVAL_PENDING", initialRevision, baseline)
	approved := f.commitAdmission(input)
	s.check("approval commits only the approval step, not a Run", len(approved.Steps) == 1 && approved.Steps[0].Kind == "APPROVE" && approved.Steps[0].RunID == "" && approved.Steps[0].ExecutionID == "", approved.Steps)
	f.checkAdmissionReplay(approved, f.commitAdmission(input))
	ws = f.checkPhase("explicit approval stops before implementation", "ACTIVE", "IMPLEMENTATION_READY")
	f.checkNoImplementation(ws)
	sources := ws["taskSources"].(map[string]any)
	var frozen task.Definition
	decode(sources["selectedDefinition"].(map[string]any)["definition"], &frozen)
	s.check("approval freezes new definition with unexecuted RUN verification, not a waiver", field(sources, "currentDefinitionRevisionId") != initialRevision && frozen.VerificationSpec.Mode == task.VerificationModeRun && frozen.VerificationSpec.Waiver == nil && len(frozen.VerificationSpec.Checks) == 2 && frozen.WorkSpec.CodexInstruction == f.report.Report.WorkSpec.CodexInstruction && reflect.DeepEqual(frozen.RepositoryTargets, f.targets), frozen)
	approvedRevision := field(sources, "currentDefinitionRevisionId")
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(approved, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(approved, f.commitAdmission(input))
	ws = f.checkPhase("cold approval replay is still no-start", "ACTIVE", "IMPLEMENTATION_READY")
	currentSources := ws["taskSources"].(map[string]any)
	var cold task.Definition
	decode(currentSources["selectedDefinition"].(map[string]any)["definition"], &cold)
	s.check("cold replay preserves approved revision and exact definition", field(currentSources, "currentDefinitionRevisionId") == approvedRevision && reflect.DeepEqual(cold, frozen), cold)
	f.checkNoImplementation(ws)
	s.check("approval and report replay did not modify managed bytes", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkAdmissionObjects("approval starts no implementation or verification Run", 1, 1)
	f.checkSources()
}

// Findings-only outcomes each use a fresh fixture. A successful transport Run
// records the report, not a successful Task or permission to approve/start.
func (s *scenario) reviewNotReady(disposition review.Disposition) {
	f := s.fixture
	f.startReview()
	f.claimReview()
	initial := field(f.workspace()["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	baseline := f.managedFileSnapshot()
	f.buildReviewReport()
	f.report.Report.Disposition = disposition
	f.report.Report.Summary = "SIMULATED findings-only " + string(disposition) + "; not a native runtime failure"
	f.report.Report.Limitations = []string{"Deterministic fixture decision; not a model engineering conclusion"}
	f.report.Report.RecommendedNextSteps = []string{"Keep Task awaiting separate review; do not approve or start implementation"}
	f.report.Report.WorkSpec, f.report.Report.VerificationSpec = nil, nil
	f.report.Report.RepositoryTargets, f.report.Report.PrimaryMemberID = nil, ""
	receipt := s.agent.call("review_result_submit", f.report)
	f.checkRecordedReview(receipt, "REVIEW_REQUIRED", initial, baseline)
	f.checkReviewResultReplays(receipt)
	s.stop(false)
	s.launch()
	f.checkReviewResultReplays(receipt)
	ws := f.checkRecordedReview(receipt, "REVIEW_REQUIRED", initial, baseline)
	current := ws["task"].(map[string]any)
	s.check("findings require deep review, not implementation approval", current["requiredReviewDepth"] == "DEEP", current)
	view := ws["taskView"].(map[string]any)
	actions, ok := view["actions"].([]any)
	s.check("findings-only actions are authoritatively available", ok && view["actionsStatus"] == "AVAILABLE", view["actionsStatus"])
	for _, raw := range actions {
		action := raw.(map[string]any)
		forbidden := action["action"] == "APPROVE" || action["action"] == "APPROVE_AND_START" || action["action"] == "START_DIRECT_FILE" || action["action"] == "START_APPROVED"
		s.check("findings-only result offers no executable approval/start", !forbidden || action["available"] != true, action)
	}
	// No fabricated approval target or automatic successor is submitted.
}

func (f *fixture) reviewReadArgs(target workset.Target) map[string]any {
	return map[string]any{"reviewTarget": f.access.Target, "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"}
}
func (f *fixture) checkPendingReview(label, revision string, baseline map[string]string) {
	ws := f.checkPhase(label, "ACTIVE", "REVIEWING")
	f.s.check(label+": current definition unchanged", field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId") == revision, nil)
	history := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check(label+": exactly one Review without a result", len(history) == 1 && history[0].(map[string]any)["resultAvailable"] == false, history)
	var access review.UpperAccess
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &access)
	f.s.check(label+": same active read authority", access.State == "ACTIVE" && access.Target == f.access.Target && reflect.DeepEqual(access.RepositoryTargets, f.access.RepositoryTargets), access)
	f.s.check(label+": managed bytes unchanged", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkNoImplementation(ws)
	f.checkAdmissionObjects(label, 1, 1)
	f.checkSources()
}
func (f *fixture) checkRecordedReview(receipt map[string]any, phase, revision string, baseline map[string]string) map[string]any {
	ws := f.checkPhase("recorded report is not Task success", "ACTIVE", phase)
	f.s.check("report preserves current definition until approval", field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId") == revision, nil)
	var got review.ReportReceipt
	decode(receipt, &got)
	f.s.check("formal report receipt binds exact payload and disposition", got.Status == "COMPLETED" && got.ReportID != "" && got.ReviewID == f.review && got.RunID == f.access.Target.RunID && got.RequestID == f.report.RequestID && got.Disposition == f.report.Report.Disposition && got.PayloadSHA256 == reviewPayloadDigest(f.report), got)
	var access review.UpperAccess
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &access)
	f.s.check("formal result closes exact Review read grant", access.State == "CLOSED" && access.Target == f.access.Target, access)
	history := ws["taskView"].(map[string]any)["reviewHistory"].([]any)
	f.s.check("formal result has exactly one historical Review", len(history) == 1, history)
	item := history[0].(map[string]any)
	run := item["run"].(map[string]any)
	f.s.check("SUCCEEDED Review Run is only report completion", item["id"] == f.review && item["reviewDisposition"] == string(f.report.Report.Disposition) && item["resultAvailable"] == true && run["id"] == f.access.Target.RunID && run["transportStatus"] == "SUCCEEDED" && run["nativeResultState"] == "UNAVAILABLE", item)
	f.s.check("report processing preserves managed file bytes", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkNoImplementation(ws)
	f.checkAdmissionObjects("formal Review result", 1, 1)
	f.checkSources()
	return ws
}
func (f *fixture) checkReviewResultReplays(receipt map[string]any) {
	s := f.s
	replayed := s.agent.call("review_result_submit", f.report)
	s.check("exact report replay retains immutable receipt", reflect.DeepEqual(receipt, replayed), replayed)
	read := s.gpt.call("review_result_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review, "requestId": f.report.RequestID})
	s.check("public exact report receipt matches original", read["state"] == "RECORDED" && read["source"] == "UPPER_REVIEW_REPORT" && reflect.DeepEqual(read["receipt"], receipt), read)
	stored := s.gpt.call("review_result_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review})
	var report review.StoredReport
	decode(stored["report"], &report)
	s.check("public formal report remains exact simulated authorship", stored["source"] == "UPPER_REVIEW_REPORT" && report.ReportID == receipt["reportId"] && reflect.DeepEqual(report.Report, f.report.Report), stored)
	worker := s.agent.call("review_worker_get", f.upperClaim)
	s.check("recorded worker has only original receipt and report", worker["state"] == review.WorkerResultRecorded && worker["executionPayload"] == nil && reflect.DeepEqual(worker["receipt"], f.upperFirst["receipt"]) && reflect.DeepEqual(worker["resultReceipt"], receipt), worker)
	f.receipt("review_worker_claim", f.upperClaim, f.upperFirst, review.WorkerResultRecorded)
	var changed review.SubmitInput
	decode(f.report, &changed)
	changed.Report.Summary = "SIMULATED conflicting late payload must not replace the original"
	s.agent.deny("review_result_submit", changed, "Review requestId was already used with a different payload")
	changed = f.report
	changed.RequestID = "late-new-report"
	s.agent.deny("review_result_submit", changed, "REVIEW_TARGET_STALE")
	s.agent.deny("file_read", f.reviewReadArgs(f.access.RepositoryTargets[0]), "REVIEW_TARGET_STALE")
}
