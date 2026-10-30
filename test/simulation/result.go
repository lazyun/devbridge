package simulation

import (
	"crypto/sha1"
	"fmt"
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
)

// gitBlobSHA1 checks the bytes named by a real Git tree, not a Core digest claim.
// Disposable repositories explicitly use Git's SHA-1 object format.
func gitBlobSHA1(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return fmt.Sprintf("%x", sum)
}
func stableFileResult(v directfile.ImplementationResult) directfile.ImplementationResult {
	v.Replay = false
	return v
}

func (s *scenario) fileResultGates(changed bool) {
	f := s.fixture
	priorRevision := f.approveStartFixture(true)
	f.startFile()
	f.claimFile()
	var claimed directfile.WorkerClaimResult
	decode(f.first, &claimed)
	expected := map[string]string{}
	for _, target := range f.targets {
		for name, repo := range f.repos {
			if repo == target.RepositoryID {
				expected[target.MemberID] = f.expectedSource[name]
			}
		}
	}
	phase := "IMPLEMENTATION_READY"
	var mutation *mutationProof
	if changed {
		target := f.targets[0]
		content := "SIMULATED actual file result bytes for " + target.RepositoryID + "\n"
		proof := f.mutate(f.mutationInput(target, "REPLACE", "same.txt", "result-source-effect", content, digestText(expected[target.MemberID]), ""), digestText(expected[target.MemberID]), digestText(content))
		mutation = &proof
		expected[target.MemberID] = content
		phase = "IMPLEMENTING"
	}
	for _, target := range f.targets {
		got := s.agent.call("file_read", f.readArgs(target))
		s.check("pre-report actual member bytes are known", got["content"] == expected[target.MemberID] && got["digest"] == digestText(expected[target.MemberID]), got)
	}
	baseline := f.managedFileSnapshot()
	f.checkPartialFiles("before formal result, no invented report", phase, "ACTIVE")
	failed := f.mutationInput(f.targets[0], "REPLACE", "same.txt", "known-failed-before-result", "must never reach disk", strings.Repeat("0", 64), "")
	f.denyMutation(failed, "mutation source digest changed")
	f.checkPartialFiles("known FAILED mutation must not create result or poison report", phase, "ACTIVE")
	input := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, RequestID: "formal-result", Summary: "SIMULATED formal file report; Core must independently derive changed and unchanged members", VerificationNotes: "Explicit fixture-only waiver: no native commands, model verification or Task acceptance occurred.", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration, WorkerClaim: &f.claim.WorkerClaimIdentity}
	for _, kind := range []string{"blank-summary", "protocol", "epoch", "generation", "prior-revision", "foreign-session", "missing-worker", "wrong-worker"} {
		var invalid directfile.ImplementationResultInput
		decode(input, &invalid)
		invalid.RequestID = "invalid-result-" + kind
		code := "FILE_RESULT_CONFLICT"
		switch kind {
		case "blank-summary":
			invalid.Summary = " \t\n"
		case "protocol":
			invalid.ProtocolVersion = 1
		case "epoch":
			invalid.RuntimeEpoch = "not-the-admitted-epoch"
		case "generation":
			invalid.GrantGeneration++
		case "prior-revision":
			invalid.TaskDefinitionRevisionID = priorRevision
		case "foreign-session":
			invalid.AISessionID = f.foreign
		case "missing-worker":
			invalid.WorkerClaim = nil
			code = "exact file worker identity or authority is unavailable"
		case "wrong-worker":
			invalid.WorkerClaim.WorkerID = "wrong-worker-label"
			code = "exact file worker identity or authority is unavailable"
		}
		s.agent.deny("file_implementation_result_submit", invalid, code)
		f.checkPartialFiles("rejected formal result "+kind, phase, "ACTIVE")
		s.check("invalid result has no file effects or closure", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	}
	args := object(input)
	// Deliberately false, schema-valid advisory metadata is an adversarial input,
	// NOT fixture evidence. Core must ignore it and derive all member facts itself.
	args["members"] = []any{map[string]any{"repositoryId": f.targets[0].RepositoryID, "memberId": f.targets[0].MemberID, "memberVersion": f.targets[0].MemberVersion, "workspaceId": "SIMULATED_UNTRUSTED_ADVISORY", "worktreeId": "SIMULATED_UNTRUSTED_ADVISORY", "originCommit": strings.Repeat("0", 40), "startSnapshot": "SIMULATED_UNTRUSTED_ADVISORY", "endSnapshot": "SIMULATED_UNTRUSTED_ADVISORY", "changedPaths": []string{"outside.txt"}, "state": "COMPLETED"}}
	var result directfile.ImplementationResult
	decode(s.agent.call("file_implementation_result_submit", args), &result)
	s.check("first formal result records exact original submission identity", !result.Replay && result.ID != "" && result.AISessionID == f.session && result.TaskID == f.task && result.TaskDefinitionRevisionID == f.revision && result.WorkflowID == f.state.WorkflowID && result.RequestID == input.RequestID && result.Summary == input.Summary && result.VerificationNotes == input.VerificationNotes, result)
	f.checkFrozenFileResult(result, claimed, expected)
	f.checkResultOnly(result, baseline)
	f.checkFileResultReplays(args, result, baseline)
	for _, kind := range []string{"summary", "verification-notes", "advisory-members"} {
		conflicting := object(args)
		switch kind {
		case "summary":
			conflicting["summary"] = "SIMULATED changed historical report"
		case "verification-notes":
			conflicting["verificationNotes"] = "SIMULATED forbidden changed historical notes"
		case "advisory-members":
			conflicting["members"].([]any)[0].(map[string]any)["changedPaths"] = []string{"same.txt"}
		}
		s.agent.deny("file_implementation_result_submit", conflicting, "FILE_RESULT_CONFLICT")
		f.checkResultOnly(result, baseline)
	}
	fresh := object(args)
	fresh["requestId"] = "late-second-result"
	s.agent.deny("file_implementation_result_submit", fresh, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	wrongRead := directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: priorRevision, WorkflowID: f.state.WorkflowID}
	s.gpt.deny("file_implementation_result_get", wrongRead, "FILE_RESULT_CONFLICT")
	s.agent.deny("file_read", f.readArgs(f.targets[0]), "exact v2 file workflow is not active")
	late := f.mutationInput(f.targets[0], "REPLACE", "same.txt", "after-formal-result", "forbidden post-report bytes", digestText(expected[f.targets[0].MemberID]), "")
	f.denyMutation(late, "exact v2 file workflow is not active")
	if mutation != nil {
		f.replayMutation(*mutation)
	}
	f.checkResultOnly(result, baseline)
	s.stop(false)
	s.launch()
	f.checkFileResultReplays(args, result, baseline)
	s.agent.deny("file_implementation_result_submit", fresh, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	f.denyMutation(late, "exact v2 file workflow is not active")
	if mutation != nil {
		f.replayMutation(*mutation)
	}
	f.checkFrozenFileResult(result, claimed, expected)
	f.checkResultOnly(result, baseline)
	// Deliberately no file_implementation_accept, merge or verification dispatch.
}

func (f *fixture) checkFrozenFileResult(result directfile.ImplementationResult, claimed directfile.WorkerClaimResult, expected map[string]string) {
	s := f.s
	s.check("formal result is real member evidence, not aggregate/native/acceptance success", result.ProtocolVersion == 2 && result.RuntimeEpoch == f.state.RuntimeEpoch && result.GrantGeneration == f.state.GrantGeneration && result.Snapshot == "" && result.VerificationStatus == "NOT_RUN" && result.ExecutionID == "" && result.VerificationRunID == "" && result.VerificationStartError == "" && !result.Accepted && result.CommitError == "" && reflect.DeepEqual(result.RepositoryTargets, f.frozen.RepositoryTargets) && len(result.Members) == len(f.targets), result)
	seen := map[string]bool{}
	for _, member := range result.Members {
		var start directfile.MemberGrant
		for _, candidate := range claimed.ExecutionPayload.Access.RepositoryTargets {
			if candidate.MemberID == member.MemberID {
				start = candidate
			}
		}
		name := ""
		for candidate, repo := range f.repos {
			if repo == member.RepositoryID {
				name = candidate
			}
		}
		s.check("Core derives exact complete member identity, never supplied advisory facts", name != "" && start.MemberID != "" && !seen[member.MemberID] && reflect.DeepEqual(member.Target, start.Target) && member.WorkspaceID == start.WorkspaceID && member.WorktreeID == start.WorktreeID && member.StartHead == start.StartHead && member.StartSnapshot == start.StartSnapshot, member)
		seen[member.MemberID] = true
		wasChanged := expected[member.MemberID] != f.expectedSource[name]
		if wasChanged {
			s.check("changed member frozen with exact actual changed path", member.State == "COMPLETED" && reflect.DeepEqual(member.ChangedPaths, []string{"same.txt"}) && member.EndSnapshot != member.StartSnapshot, member)
		} else {
			s.check("unchanged member remains NO_CHANGE without invented work", member.State == "NO_CHANGE" && len(member.ChangedPaths) == 0 && member.EndSnapshot == member.StartSnapshot, member)
		}
		path := f.paths[name]
		s.check("result snapshot is an actual tree in the owned repository", s.git(path, "rev-parse", "--show-object-format") == "sha1" && s.git(path, "cat-file", "-t", member.EndSnapshot) == "tree", member.EndSnapshot)
		s.check("snapshot contains only the exact fixture file", s.git(path, "ls-tree", "-r", "--name-only", member.EndSnapshot) == "same.txt", member.EndSnapshot)
		s.check("frozen start and end Git objects bind independently expected bytes", s.git(path, "rev-parse", member.StartSnapshot+":same.txt") == gitBlobSHA1(f.expectedSource[name]) && s.git(path, "rev-parse", member.EndSnapshot+":same.txt") == gitBlobSHA1(expected[member.MemberID]), member)
	}
}
func (f *fixture) checkResultOnly(result directfile.ImplementationResult, baseline map[string]string) {
	ws := f.checkPhase("formal result awaits separate user acceptance", "ACTIVE", "ACCEPTANCE_PENDING")
	f.s.check("formal result does not commit Task worktrees", ws["taskView"].(map[string]any)["commitStatus"] == "UNCOMMITTED", ws["taskView"])
	f.checkFrozenStartDefinition(ws)
	var state directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	f.s.check("formal result closes only exact file authority", state.State == "CLOSED" && !state.Enabled && state.WorkflowID == f.state.WorkflowID && state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration && state.TaskDefinitionRevisionID == f.revision && state.WorkspaceID == f.state.WorkspaceID && reflect.DeepEqual(state.RepositoryTargets, f.state.RepositoryTargets), state)
	f.checkAdmissionObjects("formal result invents no implementation or verification Run", 1, 1)
	read := f.readFormalFileResult()
	f.s.check("pure exact result read preserves immutable unaccepted facts", !read.Replay && reflect.DeepEqual(read, stableFileResult(result)), read)
	f.s.check("formal report does not modify actual files", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkSources()
}
func (f *fixture) readFormalFileResult() directfile.ImplementationResult {
	var result directfile.ImplementationResult
	decode(f.s.gpt.call("file_implementation_result_get", directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID}), &result)
	return result
}
func (f *fixture) checkFileResultReplays(args map[string]any, result directfile.ImplementationResult, baseline map[string]string) {
	var replay directfile.ImplementationResult
	decode(f.s.agent.call("file_implementation_result_submit", args), &replay)
	f.s.check("exact result submission replay returns same immutable receipt", replay.Replay && reflect.DeepEqual(stableFileResult(replay), stableFileResult(result)), replay)
	for _, tool := range []string{"file_worker_get", "file_worker_claim"} {
		var worker directfile.WorkerClaimResult
		decode(f.s.agent.call(tool, f.claim), &worker)
		var first directfile.WorkerClaimResult
		decode(f.first, &first)
		f.s.check("completed worker receipt never redispatches executable input", worker.State == directfile.WorkerResultRecorded && worker.ExecutionPayload == nil && worker.ResultReceipt != nil && reflect.DeepEqual(worker.Receipt, first.Receipt) && reflect.DeepEqual(stableFileResult(*worker.ResultReceipt), stableFileResult(result)), worker)
	}
	f.checkResultOnly(result, baseline)
}

// Non-enrolled file use is legitimate. Explicit cancellation must fence fresh
// v2 reports independently of the optional worker-claim protection.
func (s *scenario) fileResultAfterCancel() {
	f := s.fixture
	f.approveStartFixture(true)
	f.startFile() // deliberately no file_worker_claim and no file effect
	baseline := f.managedFileSnapshot()
	prepared := f.prepare(f.directPreparation("CANCEL_DIRECT_FILE", "cancel-unclaimed-result"))
	input := admissionCommit(prepared, "confirm-cancel-unclaimed-result", c.Selection{})
	canceled := f.commitAdmission(input)
	result := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, RequestID: "unclaimed-result-after-cancel", Summary: "SIMULATED late result must not bypass explicit canceled authority", VerificationNotes: "No file mutations or verification; explicit fixture waiver", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration}
	check := func() {
		ws := f.checkPhase("canceled unclaimed workflow cannot become a formal result", "ACTIVE", "IMPLEMENTATION_READY")
		s.check("canceled workflow remains uncommitted", ws["taskView"].(map[string]any)["commitStatus"] == "UNCOMMITTED", ws["taskView"])
		f.checkFrozenStartDefinition(ws)
		var state directfile.State
		decode(ws["taskView"].(map[string]any)["directFile"], &state)
		s.check("explicit cancellation remains exact CLOSED authority", !state.Enabled && state.State == "CLOSED" && state.WorkflowID == f.state.WorkflowID && state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration, state)
		s.gpt.deny("file_implementation_result_get", directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID}, "NOT_FOUND")
		s.agent.deny("file_worker_get", f.claim, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
		f.checkAdmissionObjects("canceled new result has no Run", 1, 1)
		s.check("canceled new result preserves actual file bytes", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
		f.checkSources()
	}
	check()
	s.agent.deny("file_implementation_result_submit", result, "new v2 file result requires active authorization")
	check()
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(canceled, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(canceled, f.commitAdmission(input))
	s.agent.deny("file_implementation_result_submit", result, "new v2 file result requires active authorization")
	check()
}
