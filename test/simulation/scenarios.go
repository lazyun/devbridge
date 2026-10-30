package simulation

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"reflect"
	"strings"
	"sync"
)

func (s *scenario) happy() {
	f := s.fixture
	f.setupFile()
	bad := f.claim
	bad.AISessionID = f.foreign
	s.agent.deny("file_worker_claim", bad, "")
	f.claimFile()
	lastDigest := map[string]any{}
	f.receipt("file_worker_claim", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	bad = f.claim
	bad.WorkerID = "foreign-worker"
	s.agent.deny("file_worker_claim", bad, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	for _, t := range f.targets {
		content := "SIMULATED actual Core bytes for " + t.RepositoryID + "\n"
		args := f.writeArgs(t, "write-"+t.RepositoryID, content)
		missing := object(args)
		delete(missing, "workerClaim")
		s.agent.deny("file_write", missing, "exact file worker identity or authority is unavailable")
		wrong := object(args)
		wrong["workerClaim"] = bad.WorkerClaimIdentity
		s.agent.deny("file_write", wrong, "exact file worker identity or authority is unavailable")
		stale := object(args)
		stale["requestId"] = "stale-" + t.RepositoryID
		stale["expectedDigest"] = strings.Repeat("0", 64)
		s.agent.deny("file_write", stale, "mutation source digest changed")
		untouched := s.agent.call("file_read", f.readArgs(t))
		s.check("denials leave actual bytes unchanged", untouched["digest"] == args["expectedDigest"], untouched)
		if t.MemberID == f.targets[0].MemberID {
			beforeScope := f.managedFileSnapshot()
			mismatch := object(args)
			mismatch["requestId"] = "mismatched-member"
			mismatch["repositoryId"] = f.targets[1].RepositoryID
			s.agent.deny("file_write", mismatch, "repository member is outside approved workset")
			outside := object(args)
			outside["requestId"] = "non-target"
			outside["repositoryId"] = f.repos["backend"]
			s.agent.deny("file_write", outside, "repository member is outside approved workset")
			escaped := object(args)
			escaped["requestId"] = "escape"
			escaped["path"] = "../outside.txt"
			s.agent.deny("file_write", escaped, "PATH_OUTSIDE_WORKTREE")
			s.check("scope denials preserve all managed file bytes", reflect.DeepEqual(beforeScope, f.managedFileSnapshot()), nil)
		}
		written := s.agent.call("file_write", args)
		lastDigest[t.MemberID] = written["digest"]
		replay := s.agent.call("file_write", args)
		s.check("write exact replay is stable", reflect.DeepEqual(written, replay), replay)
		conflict := object(args)
		conflict["content"] = "changed replay must not write"
		s.agent.deny("file_write", conflict, "FILE_OPERATION_FAILED")
		got := s.agent.call("file_read", f.readArgs(t))
		s.check("actual approved member changed", got["content"] == content, got)
	}
	f.checkPhase("first file effect", "ACTIVE", "IMPLEMENTING")
	f.checkSources()
	f.submit()
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerResultRecorded)
	late := f.readArgs(f.targets[0])
	late["action"] = "REPLACE"
	late["protocolVersion"] = 2
	late["requestId"] = "late-write"
	late["content"] = "forbidden"
	late["expectedDigest"] = lastDigest[f.targets[0].MemberID]
	late["workerClaim"] = f.claim.WorkerClaimIdentity
	beforeClosed := f.managedFileSnapshot()
	s.agent.deny("file_write", late, "exact v2 file workflow is not active")
	s.check("closed result rejects valid-digest write without file effects", reflect.DeepEqual(beforeClosed, f.managedFileSnapshot()), nil)
	changed := f.resultInput
	changed.Summary = "conflicting replay"
	s.agent.deny("file_implementation_result_submit", changed, "FILE_RESULT_CONFLICT")
	changed = f.resultInput
	changed.RequestID = "late-new-result"
	s.agent.deny("file_implementation_result_submit", changed, "")
	f.checkPhase("late effects cannot change acceptance state", "ACTIVE", "ACCEPTANCE_PENDING")
	s.stop(false)
	s.launch()
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerResultRecorded)
	f.receipt("file_worker_claim", f.claim, f.first, directfile.WorkerResultRecorded)
	f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerResultRecorded)
	replay := s.agent.call("file_implementation_result_submit", f.resultInput)
	s.check("cold result replay is not acceptance", replay["id"] == f.result["id"] && replay["accepted"] == false, replay)
	// Acceptance is a separate fixture-user action, not an Agent report or an
	// implied consequence of test success. NOT_RUN remains visibly NOT_RUN.
	accept := directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, ResultID: field(f.result, "id"), RequestID: "fixture-user-accept", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration}
	accepted := s.gpt.callAs("fixture_user", "file_implementation_accept", accept)
	s.check("explicit waived acceptance with real task commit", accepted["accepted"] == true && accepted["verificationStatus"] == "NOT_RUN" && accepted["executionId"] == nil && accepted["commitError"] == nil, accepted)
	f.checkPhase("accepted and committed, merge separate", "SUCCEEDED", "DELIVERING")
	f.checkSources()
	merged, mergeInput := f.complete(c.PrepareInput{RequestID: "merge-fixture", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{variant("merge", "Fixture user explicitly merges only app and sdk disposable branches")}}, c.Selection{})
	s.check("separate merge completed", merged.State == "COMPLETED", merged)
	for _, name := range []string{"app", "sdk"} {
		f.expectedSource[name] = "SIMULATED actual Core bytes for " + f.repos[name] + "\n"
		s.check("delivery advanced "+name, f.heads[name] != s.git(f.paths[name], "rev-parse", "HEAD"), nil)
		f.expectedHeads[name] = s.git(f.paths[name], "rev-parse", "HEAD")
	}
	var replayMerge c.Intent
	decode(s.gpt.callAs("fixture_user", "confirmation_commit", mergeInput)["intent"], &replayMerge)
	s.check("merge confirmation exact replay", reflect.DeepEqual(merged, replayMerge), replayMerge)
	f.checkSources()
}

func (s *scenario) claimRace() {
	f := s.fixture
	f.setupFile()
	const attempts = 8
	gate := make(chan struct{})
	results := make([]callOutcome, attempts)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			results[i] = s.agent.invoke("downstream_agent", "file_worker_claim", f.claim)
		}(i)
	}
	close(gate)
	wg.Wait()
	payloads := 0
	for _, got := range results {
		s.must(got.Err)
		s.check("concurrent exact claim succeeds", !got.Denied, got.ErrorText)
		if got.Value["executionPayload"] != nil {
			payloads++
			f.first = got.Value
		}
	}
	s.check("concurrent duplicate delivery has exactly one payload", payloads == 1, map[string]any{"calls": attempts, "payloads": payloads})
	for _, got := range results {
		s.check("concurrent claims share durable receipt", reflect.DeepEqual(got.Value["receipt"], f.first["receipt"]), got.Value["state"])
		expected := directfile.WorkerOutcomeUnknown
		if got.Value["executionPayload"] != nil {
			expected = directfile.WorkerDispatchIntent
		}
		s.check("concurrent receipt has exact delivery state", got.Value["state"] == expected, got.Value["state"])
	}
	f.complete(f.directPreparation("CANCEL_DIRECT_FILE", "cancel-concurrent-worker"), c.Selection{})
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	ws := f.checkPhase("cancel authority is not Task cancellation", "ACTIVE", "IMPLEMENTATION_READY")
	var state directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	s.check("canceled file authority is closed", !state.Enabled && state.State == "CLOSED", state)
	s.agent.deny("file_read", f.readArgs(f.targets[0]), "")
}

func (s *scenario) fileCrash() {
	f := s.fixture
	f.setupFile()
	f.claimFile()
	args := f.writeArgs(f.targets[0], "before-crash", "SIMULATED durable partial work\n")
	written := s.agent.call("file_write", args)
	f.checkPhase("partial work before crash", "ACTIVE", "IMPLEMENTING")
	s.record(map[string]any{"kind": "fault", "fault": "SIGKILL owned Core after acknowledged file effect; not a mid-journal fault"})
	s.stop(true)
	s.launch()
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	f.receipt("file_worker_claim", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	ws := f.checkPhase("Core crash does not invent Task completion", "ACTIVE", "IMPLEMENTING")
	var state directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	s.check("crashed active grant needs recovery without reassignment", state.State == "RECOVERY_REQUIRED" && !state.Enabled && state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration, state)
	other := f.claim
	other.WorkerID = "replacement-worker"
	other.RequestID = "replacement-delivery"
	s.agent.deny("file_worker_claim", other, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	s.agent.deny("file_read", f.readArgs(f.targets[0]), "")
	replay := s.agent.call("file_write", args)
	s.check("historical effect replay survives crash without redispatch", reflect.DeepEqual(written, replay), replay)
	// Public preparation must refuse continuation for an enrolled unknown worker.
	in := f.directPreparation("RECOVER_DIRECT_FILE", "unsupported-recover")
	in.AISessionID = f.session
	s.gpt.deny("execution_prepare", in, "")
	ws = f.workspace()
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	s.check("refused recovery cannot refresh authority", state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration, state)
	f.complete(f.directPreparation("CANCEL_DIRECT_FILE", "cancel-after-crash"), c.Selection{})
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	changed := object(args)
	changed["requestId"] = "late-after-cancel"
	changed["expectedDigest"] = written["digest"]
	changed["content"] = "forbidden post-cancel bytes"
	beforeCanceled := f.managedFileSnapshot()
	s.agent.deny("file_write", changed, "exact v2 file workflow is not active")
	s.check("canceled authority rejects valid-digest write without file effects", reflect.DeepEqual(beforeCanceled, f.managedFileSnapshot()), nil)
	ws = f.checkPhase("explicit cancellation closes authority not external worker", "ACTIVE", "IMPLEMENTING")
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	s.check("canceled stale grant closed", state.State == "CLOSED" && !state.Enabled, state)
}

func (s *scenario) reviewCancel() {
	f := s.fixture
	f.startReview()
	f.claimReview()
	target := f.access.RepositoryTargets[0]
	args := map[string]any{"reviewTarget": f.access.Target, "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"}
	s.agent.call("file_read", args)
	// Same-writer epoch is obtained from the real admitted grant, not fabricated.
	cancelArgs := map[string]any{"aiSessionId": f.session, "ownerType": "REVIEW", "ownerId": f.review, "runId": f.access.Target.RunID, "requestId": "cancel-review", "reason": "SIMULATED fixture user withdraws review", "expectedUpperVersion": map[string]any{"authorityEpoch": f.access.Target.RuntimeEpoch, "grantEpoch": f.access.Target.RuntimeEpoch, "grantGeneration": f.access.Target.GrantGeneration}}
	canceled := s.gpt.callAs("fixture_user", "operation_cancel", cancelArgs)
	s.check("review cancellation records no native backend", canceled["state"] == "CANCELED" && canceled["backendDisposition"] == "NO_BACKEND", canceled)
	f.checkPhase("review canceled, Task remains active", "ACTIVE", "REVIEW_REQUIRED")
	s.agent.deny("file_read", args, "")
	report := review.SubmitInput{UpperTarget: f.access.Target, WorkerClaim: &f.upperClaim.WorkerClaimIdentity, RequestID: "late-canceled-report", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: f.access.RepositoryTargets, Summary: "SIMULATED late report must not commit", Disposition: review.DispositionFurtherReviewRequired, Coverage: []string{"app/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"Simulated"}, RecommendedNextSteps: []string{"None"}, EvidenceRefs: []review.EvidenceRef{}}}
	s.agent.deny("review_result_submit", report, "REVIEW_TARGET_STALE")
	f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerOutcomeUnknown)
	s.stop(false)
	s.launch()
	f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerOutcomeUnknown)
	replay := s.gpt.callAs("fixture_user", "operation_cancel", cancelArgs)
	s.check("cancellation durable exact replay", reflect.DeepEqual(canceled, replay), replay)
	f.checkPhase("cold canceled review cannot complete Task", "ACTIVE", "REVIEW_REQUIRED")
}
