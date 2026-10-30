package simulation

import (
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

// Approval remains a separate fixture-user choice. The runtime-gate fixture
// preserves RUN; only the independent neutral start fixture explicitly waives it.
func (f *fixture) approveStartFixture(waive bool) string {
	f.startReview()
	f.claimReview()
	f.buildReviewReport()
	f.report.Report.RecommendedNextSteps = []string{"Separate explicit fixture-user approval and start; stop before any implementation effect"}
	f.recordReviewReport()
	prior := f.revision
	selection := c.Selection{}
	if waive {
		selection.VerificationWaiver = &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "SIMULATOR FIXTURE ONLY: explicit user waiver for pre-effect neutral-worker admission; no verification performed"}
	}
	f.complete(f.approvalPreparation("APPROVE", "approve-start-fixture"), selection)
	ws := f.checkPhase("approved definition is not start authority", "ACTIVE", "IMPLEMENTATION_READY")
	sources := ws["taskSources"].(map[string]any)
	f.revision = field(sources, "currentDefinitionRevisionId")
	decode(sources["selectedDefinition"].(map[string]any)["definition"], &f.frozen)
	if waive {
		f.s.check("neutral start uses explicit fixture-only waiver", f.frozen.VerificationSpec.Mode == task.VerificationModeNone && f.frozen.VerificationSpec.Waiver != nil && f.frozen.VerificationSpec.Waiver.Reason == selection.VerificationWaiver.Reason, f.frozen.VerificationSpec)
	} else {
		f.s.check("runtime gate retains required verification", f.frozen.VerificationSpec.Mode == task.VerificationModeRun && f.frozen.VerificationSpec.Waiver == nil && len(f.frozen.VerificationSpec.Checks) == 2, f.frozen.VerificationSpec)
	}
	f.checkNoImplementation(ws)
	return prior
}

func (s *scenario) executionRuntimeGate() {
	f := s.fixture
	f.approveStartFixture(false)
	baseline := f.managedFileSnapshot()
	prepared := f.prepare(f.directPreparation("START_DIRECT_FILE", "required-verification-start"))
	options := f.startOptions(prepared)
	s.check("runtime inventory is only selectable metadata, not a confirmed runtime", options.Eligibility.Available && len(options.BackendCandidates) > 0 && len(options.RuntimeOptions) > 0 && options.LockedBackend == nil && options.LockedRuntimeConfig == nil, options)
	f.checkBeforeStart("runtime options do not authorize files", baseline)
	input := admissionCommit(prepared, "no-native-selection", c.Selection{})
	f.refuseConfirmation(input, "BACKEND_SELECTION_REQUIRED")
	read := f.readAdmission(prepared, input.RequestID)
	sub := read.Submission
	s.check("runtime refusal is durably unconsumed, never implicit waiver", read.State == "PENDING" && read.Commit == nil && len(read.Steps) == 0 && sub != nil && sub.Recorded && sub.Outcome == c.RejectedNotConsumed && sub.Consumed != nil && !*sub.Consumed && sub.ReceiptRequestID == "" && reflect.DeepEqual(sub.Request, input) && sub.PayloadSHA256 == c.ValueHash(input) && sub.Failure != nil && sub.Failure.Code == "BACKEND_SELECTION_REQUIRED" && sub.RetryDisposition == c.RetryEditSelectionAndResubmit, read)
	f.checkBeforeStart("missing native selection cannot start", baseline)
	// Choose only a real advertised configuration identity, never a fabricated
	// runtime/platform qualification. The absent model selection must still fence.
	backend := options.BackendCandidates[0]
	input.RequestID = "backend-without-runtime-selection"
	input.Selection.Backend = &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}
	f.refuseConfirmation(input, "RUNTIME_CONFIGURATION_REQUIRED")
	read = f.readAdmission(prepared, input.RequestID)
	sub = read.Submission
	s.check("backend choice alone remains unconsumed and cannot waive RUN", read.State == "PENDING" && read.Commit == nil && len(read.Steps) == 0 && sub != nil && sub.Recorded && sub.Outcome == c.RejectedNotConsumed && sub.Consumed != nil && !*sub.Consumed && reflect.DeepEqual(sub.Request, input) && sub.PayloadSHA256 == c.ValueHash(input) && sub.ReceiptRequestID == "" && sub.Failure != nil && sub.Failure.Code == "RUNTIME_CONFIGURATION_REQUIRED", read)
	f.checkBeforeStart("backend metadata is not complete runtime authorization", baseline)
	s.stop(false)
	s.launch()
	cold := f.readAdmission(prepared, input.RequestID)
	s.check("cold runtime rejection preserves exact recorded choice and non-consumption", reflect.DeepEqual(cold.Submission, sub) && cold.State == "PENDING" && cold.Commit == nil && len(cold.Steps) == 0, cold)
	f.checkBeforeStart("cold runtime refusal preserves RUN and no authority", baseline)
	// No complete native selection, qualification, report, acceptance, or continuation.
}

func (s *scenario) directStartGates() {
	f := s.fixture
	priorRevision := f.approveStartFixture(true)
	baseline := f.managedFileSnapshot()
	for _, kind := range []string{"prior-revision", "instruction", "workset"} {
		var invalid c.PrepareInput
		decode(f.directPreparation("START_DIRECT_FILE", "invalid-start-"+kind), &invalid)
		invalid.AISessionID = f.session
		message := "START_APPROVED confirms the frozen candidate content"
		switch kind {
		case "prior-revision":
			invalid.Execution.TaskDefinitionRevisionID = priorRevision
			message = "NOT_FOUND"
		case "instruction":
			invalid.Variants[0].WorkSpec.CodexInstruction = "SIMULATED unauthorized replacement instruction"
		case "workset":
			invalid.Variants[0].RepositoryTargets[0].Write.Paths = []string{"outside.txt"}
			message = "exact approved Direct File workset required"
		}
		out := s.gpt.invoke("upstream_gpt", "execution_prepare", invalid)
		s.must(out.Err)
		var refused c.Result
		decode(out.Value, &refused)
		s.check("invalid start proposal is rejected before preparation: "+kind, out.Denied && strings.Contains(out.ErrorText, message) && refused.Intent == nil && refused.BusinessOutcome == c.RejectedNotPrepared && refused.Failure != nil, refused)
		f.checkBeforeStart("rejected start proposal "+kind, baseline)
	}
	prepared := f.prepare(f.directPreparation("START_DIRECT_FILE", "explicit-neutral-start"))
	s.check("file start preparation is pending with no business step", prepared.State == "PENDING" && prepared.Commit == nil && len(prepared.Steps) == 0, prepared)
	options := f.startOptions(prepared)
	s.check("explicit waiver start needs no native selection", options.Eligibility.Available && len(options.BackendCandidates) == 0 && options.LockedBackend == nil && options.LockedRuntimeConfig == nil, options)
	f.checkBeforeStart("prepare and options are not start", baseline)
	input := admissionCommit(prepared, "explicit-neutral-start-choice", c.Selection{})
	for _, kind := range []string{"hash", "version"} {
		bad := input
		bad.RequestID = "bad-start-" + kind
		if kind == "hash" {
			bad.ContentHash = strings.Repeat("0", 64)
		} else {
			bad.Version++
		}
		f.refuseConfirmation(bad, "CONFIRMATION_READ_IDENTITY_MISMATCH")
		f.checkBeforeStart("invalid start confirmation "+kind, baseline)
	}
	bad := input
	bad.RequestID, bad.VariantID = "bad-start-variant", "not-prepared"
	f.checkEditableRefusal(prepared, bad)
	f.checkBeforeStart("editable start rejection is unconsumed", baseline)
	s.stop(false)
	s.launch()
	pending := f.readAdmission(prepared, "")
	s.check("cold pending start does not silently consume choice", pending.State == "PENDING" && pending.Commit == nil && len(pending.Steps) == 0 && pending.ContentHash == prepared.ContentHash, pending)
	f.checkBeforeStart("cold pending start remains unauthorized", baseline)
	started := f.commitAdmission(input)
	f.adoptFile()
	s.check("explicit choice records only one exact Direct File start", len(started.Steps) == 1 && started.Steps[0].Kind == "START_DIRECT_FILE" && started.Steps[0].State == "COMPLETED" && started.Steps[0].WorkflowID == f.state.WorkflowID && started.Steps[0].RevisionID == f.revision && started.Steps[0].RunID == "" && started.Steps[0].ExecutionID == "", started.Steps)
	s.check("first start grants exactly two ready approved members", f.state.State == "ACTIVE" && f.state.Enabled && f.state.GrantGeneration == 1 && f.state.TaskDefinitionRevisionID == f.revision && len(f.state.RepositoryTargets) == 2, f.state)
	for _, member := range f.state.RepositoryTargets {
		found := false
		for _, target := range f.frozen.RepositoryTargets {
			found = found || reflect.DeepEqual(member.Target, target)
		}
		s.check("file start projection preserves exact frozen target", found, member)
		got := s.agent.call("file_read", f.readArgs(member.Target))
		expected := ""
		for name, repository := range f.repos {
			if repository == member.RepositoryID {
				expected = f.expectedSource[name]
			}
		}
		s.check("newly authorized member contains unchanged actual fixture bytes", expected != "" && got["content"] == expected && got["digest"] == digestText(expected), got)
	}
	afterStart := f.managedFileSnapshot()
	for path, digest := range baseline {
		s.check("start preserves preexisting managed file: "+path, afterStart[path] == digest, afterStart[path])
	}
	added := 0
	for path, digest := range afterStart {
		if _, exists := baseline[path]; !exists {
			added++
			s.check("start adds only unchanged fixture files", filepath.Base(path) == "same.txt" && (digest == digestText(f.expectedSource["app"]) || digest == digestText(f.expectedSource["sdk"])), path)
		}
	}
	s.check("start does not create unrelated managed file copies", added <= len(f.frozen.RepositoryTargets), added)
	baseline = afterStart // new worktrees were byte-checked above, not blindly blessed
	f.checkAdmissionReplay(started, f.commitAdmission(input))
	f.checkAdmissionReplay(started, f.readAdmission(prepared, input.RequestID))
	changed := input
	changed.VariantID = "changed-consumed-choice"
	f.refuseConfirmation(changed, "CONFIRMATION_REQUEST_CONFLICT")
	changed.RequestID = "new-choice-after-consumption"
	f.refuseConfirmation(changed, "CONFIRMATION_REQUEST_CONFLICT")
	f.checkStartedWithoutEffects("start replay does not duplicate or execute", baseline)
	for _, kind := range []string{"epoch", "generation", "revision", "session"} {
		stale := f.claim
		switch kind {
		case "epoch":
			stale.RuntimeEpoch = "not-the-started-epoch"
		case "generation":
			stale.GrantGeneration++
		case "revision":
			stale.TaskDefinitionRevisionID = priorRevision
		case "session":
			stale.AISessionID = f.foreign
		}
		s.agent.deny("file_worker_claim", stale, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
		f.checkStartedWithoutEffects("rejected pre-effect claim "+kind, baseline)
	}
	s.agent.deny("file_worker_get", f.claim, "DIRECT_FILE_CAPABILITY_NOT_ENABLED")
	f.claimFile()
	var claimed directfile.WorkerClaimResult
	decode(f.first, &claimed)
	selected := f.workspace()["taskSources"].(map[string]any)["selectedDefinition"].(map[string]any)
	s.check("file worker receipt binds exact public definition and payload", claimed.Receipt.WorkerTarget == f.claim.WorkerTarget && claimed.Receipt.WorkerClaimIdentity == f.claim.WorkerClaimIdentity && claimed.Receipt.InstructionSHA256 == digestText(f.frozen.WorkSpec.CodexInstruction) && claimed.Receipt.DefinitionSHA256 == field(selected, "payloadSha256") && claimed.Receipt.PayloadSHA256 == reviewPayloadDigest(claimed.ExecutionPayload) && len(claimed.Receipt.WorksetSHA256) == 64 && reflect.DeepEqual(claimed.ExecutionPayload.Verification, f.frozen.VerificationSpec), claimed)
	access := claimed.ExecutionPayload.Access
	s.check("dispatch access binds exact started authority", access.State == "ACTIVE" && access.Enabled && access.WorkflowID == f.state.WorkflowID && access.TaskDefinitionRevisionID == f.revision && access.RuntimeEpoch == f.state.RuntimeEpoch && access.GrantGeneration == f.state.GrantGeneration && access.WorkspaceID == f.state.WorkspaceID && len(access.RepositoryTargets) == len(f.frozen.RepositoryTargets), access)
	seen := map[string]bool{}
	for _, member := range access.RepositoryTargets {
		found := false
		for _, target := range f.frozen.RepositoryTargets {
			found = found || reflect.DeepEqual(member.Target, target)
		}
		s.check("claim has each exact frozen member once", found && !seen[member.MemberID], member)
		seen[member.MemberID] = true
		s.check("claimed member has real ready worktree identity", member.State == "READY" && member.StartHead == member.OriginCommit && member.StartSnapshot != "" && member.WorkspaceID == f.state.WorkspaceID && member.WorktreeID != "", member)
	}
	f.receipt("file_worker_claim", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	f.checkStartedWithoutEffects("claim delivery is not implementation progress", baseline)
	args := f.writeArgs(f.targets[0], "never-successful-write", "SIMULATED forbidden bytes")
	for _, kind := range []string{"epoch", "generation", "member-version", "missing-worker", "wrong-worker", "outside-paths"} {
		invalid := object(args)
		invalid["requestId"] = "denied-pre-effect-" + kind
		message := "exact v2 repository member and runtime fence are required"
		switch kind {
		case "epoch":
			invalid["runtimeEpoch"] = "not-the-started-epoch"
		case "generation":
			invalid["grantGeneration"] = f.state.GrantGeneration + 1
		case "member-version":
			invalid["memberVersion"] = f.targets[0].MemberVersion + 1
			message = "repository member is outside approved workset"
		case "missing-worker":
			delete(invalid, "workerClaim")
			message = "exact file worker identity or authority is unavailable"
		case "wrong-worker":
			invalid["workerClaim"] = directfile.WorkerClaimIdentity{RequestID: f.claim.RequestID, WorkerID: "wrong-worker-label"}
			message = "exact file worker identity or authority is unavailable"
		case "outside-paths":
			invalid["action"], invalid["path"] = "CREATE", "outside.txt"
			delete(invalid, "expectedDigest")
			message = "outside approved"
		}
		s.agent.deny("file_write", invalid, message)
		f.checkStartedWithoutEffects("pre-effect mutation fence "+kind, baseline)
	}
	f.receipt("file_worker_get", f.claim, f.first, directfile.WorkerOutcomeUnknown)
	// Stop here: no successful file mutation, result, acceptance, cancel, or recovery.
}

func (f *fixture) startOptions(intent c.Intent) c.Options {
	var options c.Options
	decode(f.s.gpt.call("confirmation_options_get", c.OptionsInput{ReadInput: admissionRead(intent, "")})["options"], &options)
	return options
}
func (f *fixture) checkFrozenStartDefinition(ws map[string]any) {
	sources := ws["taskSources"].(map[string]any)
	var current task.Definition
	decode(sources["selectedDefinition"].(map[string]any)["definition"], &current)
	f.s.check("start guards preserve exact approved revision and definition", field(sources, "currentDefinitionRevisionId") == f.revision && reflect.DeepEqual(current, f.frozen), current)
	view := ws["taskView"].(map[string]any)
	history, ok := view["executionHistory"].([]any)
	f.s.check("neutral start has no invented native Execution or history", ok && len(history) == 0 && emptyField(ws["task"].(map[string]any), "currentExecutionId"), history)
}
func (f *fixture) checkBeforeStart(label string, baseline map[string]string) {
	ws := f.checkPhase(label, "ACTIVE", "IMPLEMENTATION_READY")
	f.checkFrozenStartDefinition(ws)
	f.checkNoImplementation(ws)
	f.checkAdmissionObjects(label, 1, 1)
	f.s.check(label+": managed bytes unchanged", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkSources()
}
func (f *fixture) checkStartedWithoutEffects(label string, baseline map[string]string) {
	ws := f.checkPhase(label, "ACTIVE", "IMPLEMENTATION_READY")
	f.checkFrozenStartDefinition(ws)
	var current directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &current)
	f.s.check(label+": same active start authority", current.State == "ACTIVE" && current.Enabled && current.WorkflowID == f.state.WorkflowID && current.RuntimeEpoch == f.state.RuntimeEpoch && current.GrantGeneration == f.state.GrantGeneration && current.TaskDefinitionRevisionID == f.revision && current.WorkspaceID == f.state.WorkspaceID && reflect.DeepEqual(current.RepositoryTargets, f.state.RepositoryTargets), current)
	f.checkAdmissionObjects(label, 1, 1)
	f.s.check(label+": no managed file effect", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	f.checkSources()
}
