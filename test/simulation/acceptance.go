package simulation

import (
	"reflect"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
)

// acceptanceProof keeps independent fixture bytes and Git identities across the
// separate report, user acceptance, Task commit and FeatureArea merge boundaries.
type acceptanceProof struct {
	priorRevision string
	result        directfile.ImplementationResult
	input         directfile.ImplementationAcceptInput
	expected      map[string]string
	files         map[string]string
	heads         map[string]string
}

func (f *fixture) prepareAcceptance(changes int) *acceptanceProof {
	s := f.s
	p := &acceptanceProof{priorRevision: f.approveStartFixture(true), expected: map[string]string{}, heads: map[string]string{}}
	f.startFile()
	f.claimFile()
	for i, target := range f.targets {
		name := f.repositoryName(target.RepositoryID)
		content := f.expectedSource[name]
		if i < changes {
			content = "SIMULATED exact accepted bytes for " + target.RepositoryID + "\n"
			f.mutate(f.mutationInput(target, "REPLACE", "same.txt", "accepted-effect-"+target.MemberID, content, digestText(f.expectedSource[name]), ""), digestText(f.expectedSource[name]), digestText(content))
		}
		p.expected[target.MemberID] = content
	}
	f.resultInput = directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, RequestID: "report-before-acceptance", Summary: "SIMULATED bounded actual file facts, including unchanged members; awaits separate fixture-user acceptance", VerificationNotes: "Explicit fixture-only NONE waiver; no command or model verification performed", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration, WorkerClaim: &f.claim.WorkerClaimIdentity}
	decode(s.agent.call("file_implementation_result_submit", f.resultInput), &p.result)
	p.input = directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, ResultID: p.result.ID, RequestID: "explicit-fixture-user-acceptance", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration}
	p.files = f.managedFileSnapshot()
	var claimed directfile.WorkerClaimResult
	decode(f.first, &claimed)
	f.checkFrozenFileResult(p.result, claimed, p.expected)
	f.checkResultOnly(p.result, p.files)
	f.checkBeforeAcceptanceHeads(p)
	return p
}
func (f *fixture) repositoryName(id string) string {
	for name, repo := range f.repos {
		if repo == id {
			return name
		}
	}
	panic("unknown fixture repository " + id)
}
func (f *fixture) checkBeforeAcceptanceHeads(p *acceptanceProof) {
	for _, m := range p.result.Members {
		root := f.paths[f.repositoryName(m.RepositoryID)]
		f.s.check("unaccepted report creates no managed Task commit", m.Branch != "" && f.s.git(root, "rev-parse", "--verify", m.Branch+"^{commit}") == m.StartHead, m)
	}
}
func (s *scenario) fileAcceptanceGates(changes int) {
	f := s.fixture
	p := f.prepareAcceptance(changes)
	s.gpt.deny("task_commit_retry", map[string]any{"aiSessionId": f.session, "taskId": f.task}, "TASK_NOT_ACCEPTED")
	f.checkResultOnly(p.result, p.files)
	f.checkBeforeAcceptanceHeads(p)
	for _, kind := range []string{"foreign-session", "prior-revision", "wrong-workflow", "wrong-result", "epoch", "generation", "protocol"} {
		bad := p.input
		bad.RequestID = "invalid-accept-" + kind
		code := "FILE_RESULT_CONFLICT"
		switch kind {
		case "foreign-session":
			bad.AISessionID = f.foreign
			code = "NOT_FOUND"
		case "prior-revision":
			bad.TaskDefinitionRevisionID = p.priorRevision
			code = "NOT_FOUND"
		case "wrong-workflow":
			bad.WorkflowID = f.area
			code = "NOT_FOUND"
		case "wrong-result":
			bad.ResultID = f.review
			code = "NOT_FOUND"
		case "epoch":
			bad.RuntimeEpoch = "not-original-result-epoch"
		case "generation":
			bad.GrantGeneration++
		case "protocol":
			bad.ProtocolVersion = 1
		}
		s.gpt.deny("file_implementation_accept", bad, code)
		f.checkResultOnly(p.result, p.files)
		f.checkBeforeAcceptanceHeads(p)
	}
	// A process restart is not acceptance or permission to replace original IDs.
	s.stop(false)
	s.launch()
	f.checkResultOnly(p.result, p.files)
	f.checkBeforeAcceptanceHeads(p)
	f.acceptFiles(p)
	f.checkAcceptanceReplays(p)
	s.stop(false)
	s.launch()
	f.checkAcceptanceReplays(p)
	// Deliberately stop before FeatureArea integration. No new Task/continuation.
}
func (f *fixture) acceptFiles(p *acceptanceProof) {
	var accepted directfile.ImplementationResult
	decode(f.s.gpt.callAs("fixture_user", "file_implementation_accept", p.input), &accepted)
	expected := p.result
	expected.Accepted = true
	f.s.check("separate fixture-user acceptance preserves exact NOT_RUN file facts", !accepted.Replay && accepted.CommitError == "" && reflect.DeepEqual(accepted, expected), accepted)
	f.checkAcceptedFiles(p, false)
}
func (f *fixture) checkAcceptedFiles(p *acceptanceProof, integrated bool) {
	s := f.s
	ws := f.checkPhase("accepted Task commit is distinct from FeatureArea integration", "SUCCEEDED", "DELIVERING")
	f.checkFrozenStartDefinition(ws)
	view := ws["taskView"].(map[string]any)
	s.check("all Task member commits completed without fabricated scalar commit", view["commitStatus"] == "COMMITTED" && emptyField(view, "commitSha") && emptyField(view, "commitErrorSummary"), view)
	var fact featurearea.TaskCommitFact
	decode(view["details"].(map[string]any)["commitReceipt"], &fact)
	wantIntegration := "MERGED"
	if !integrated {
		for _, member := range p.result.Members {
			if member.State != "NO_CHANGE" {
				wantIntegration = "UNMERGED"
			}
		}
	}
	s.check("integration projection matches actual pending changes or no-change completion", view["devIntegrationStatus"] == wantIntegration && fact.IntegrationState == wantIntegration, fact)
	s.check("public Task receipt has exact complete member set", fact.TaskID == f.task && !fact.Pending && fact.LastError == "" && fact.Delivery.State == "COMPLETED" && len(fact.Delivery.Members) == len(p.result.Members), fact)
	seen := map[string]bool{}
	for _, m := range p.result.Members {
		root := f.paths[f.repositoryName(m.RepositoryID)]
		head := s.git(root, "rev-parse", "--verify", m.Branch+"^{commit}")
		var found bool
		for _, d := range fact.Delivery.Members {
			if d.MemberID != m.MemberID {
				continue
			}
			found = true
			s.check("committed member remains exact frozen result target", !seen[d.MemberID] && d.RepositoryID == m.RepositoryID && d.MemberVersion == m.MemberVersion && d.WorktreeID == m.WorktreeID, d)
			seen[d.MemberID] = true
			if m.State == "NO_CHANGE" {
				s.check("NO_CHANGE member has no invented Git commit", d.State == "NO_CHANGE" && d.Commit == "" && head == m.StartHead, d)
			} else {
				s.check("changed member commits exactly the frozen tree and one original parent", d.State == "COMPLETED" && d.Commit == head && head != m.StartHead && s.git(root, "rev-parse", head+"^{tree}") == m.EndSnapshot && s.git(root, "show", "-s", "--format=%P", head) == m.StartHead && s.git(root, "rev-list", "--count", m.StartHead+".."+head) == "1", d)
			}
		}
		s.check("every result member has exactly one public delivery receipt", found, nil)
		if old, ok := p.heads[m.MemberID]; ok {
			s.check("receipt reads/retries never create another Task commit", old == head, head)
		} else {
			p.heads[m.MemberID] = head
		}
	}
	expected := p.result
	expected.Accepted = true
	read := f.readFormalFileResult()
	s.check("result read projects separate acceptance but retains NOT_RUN and frozen evidence", reflect.DeepEqual(read, expected), read)
	var closed directfile.State
	decode(view["directFile"], &closed)
	s.check("acceptance cannot reopen closed file authority", closed.State == "CLOSED" && !closed.Enabled && closed.WorkflowID == f.state.WorkflowID && closed.TaskDefinitionRevisionID == f.revision && closed.WorkspaceID == f.state.WorkspaceID && closed.RuntimeEpoch == f.state.RuntimeEpoch && closed.GrantGeneration == f.state.GrantGeneration && reflect.DeepEqual(closed.RepositoryTargets, f.state.RepositoryTargets), closed)
	f.checkAdmissionObjects("acceptance and Git effects create no extra Run", 1, 1)
	s.check("acceptance preserves exact owned file bytes", reflect.DeepEqual(p.files, f.managedFileSnapshot()), nil)
	f.checkSources()
}
func (f *fixture) checkAcceptanceReplays(p *acceptanceProof) {
	s := f.s
	var replay directfile.ImplementationResult
	decode(s.gpt.callAs("fixture_user", "file_implementation_accept", p.input), &replay)
	expected := p.result
	expected.Accepted = true
	s.check("exact accepted receipt replays original identity without another commit", replay.Replay && reflect.DeepEqual(stableFileResult(replay), expected), replay)
	changed := p.input
	changed.GrantGeneration++
	s.gpt.deny("file_implementation_accept", changed, "FILE_RESULT_CONFLICT")
	changed = p.input
	changed.RequestID = "new-acceptance-after-completed-task"
	s.gpt.deny("file_implementation_accept", changed, "FILE_RESULT_CONFLICT")
	retry := s.gpt.callAs("fixture_user", "task_commit_retry", map[string]any{"aiSessionId": f.session, "taskId": f.task})
	s.check("completed multi-member retry invents no scalar commit", retry["commitSha"] == "" && retry["mergedCommit"] == "", retry)
	// Submission replay retains historical report facts. The result reader and
	// worker receipt reads project the separate durable acceptance as true.
	decode(s.agent.call("file_implementation_result_submit", f.resultInput), &replay)
	s.check("historical report replay is not another acceptance", replay.Replay && reflect.DeepEqual(stableFileResult(replay), p.result), replay)
	var first directfile.WorkerClaimResult
	decode(f.first, &first)
	for _, tool := range []string{"file_worker_get", "file_worker_claim"} {
		var worker directfile.WorkerClaimResult
		decode(s.agent.call(tool, f.claim), &worker)
		s.check("accepted Task cannot redispatch a worker or rewrite original report receipt", worker.State == directfile.WorkerResultRecorded && worker.ExecutionPayload == nil && worker.ResultReceipt != nil && reflect.DeepEqual(worker.Receipt, first.Receipt) && reflect.DeepEqual(stableFileResult(*worker.ResultReceipt), expected), worker)
	}
	f.checkAcceptedFiles(p, false)
}

func (s *scenario) featureDeliveryGates() {
	f := s.fixture
	p := f.prepareAcceptance(2)
	f.acceptFiles(p)
	prepared := f.prepare(c.PrepareInput{RequestID: "prepare-separate-delivery", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{variant("merge", "Fixture user separately integrates only the frozen accepted app/sdk commits")}})
	plan := prepared.Content.FeatureArea.DeliveryPlan
	s.check("prepared integration is valid exact two-member frozen plan", plan != nil && plan.Valid() && plan.ProjectID == f.project && plan.FeatureAreaID == f.area && plan.WorkspaceID == f.state.WorkspaceID && len(plan.Members) == 2, plan)
	seen := map[string]bool{}
	for _, target := range plan.Members {
		name := f.repositoryName(target.RepositoryID)
		var found bool
		for _, m := range p.result.Members {
			if m.MemberID == target.MemberID {
				found = true
				s.check("integration source is exact accepted member commit", target.MemberVersion == m.MemberVersion && target.WorktreeID == m.WorktreeID && target.SourceBranch == m.Branch && target.SourceHead == p.heads[m.MemberID] && reflect.DeepEqual(target.TaskCommits, []string{p.heads[m.MemberID]}), target)
			}
		}
		s.check("integration targets only original selected development branch", found && !seen[target.MemberID] && name != "backend" && target.TargetPath == f.paths[name] && target.ExpectedDevHead == f.heads[name] && target.DevelopmentRef == "refs/heads/"+name+"-development", target)
		seen[target.MemberID] = true
	}
	var options c.Options
	decode(s.gpt.call("confirmation_options_get", c.OptionsInput{ReadInput: admissionRead(prepared, "")})["options"], &options)
	s.check("integration options have no model or runtime selection", options.Eligibility.Available && len(options.BackendCandidates) == 0 && options.LockedBackend == nil, options)
	f.checkAcceptedFiles(p, false)
	input := admissionCommit(prepared, "confirm-separate-delivery", c.Selection{})
	bad := input
	bad.ContentHash = strings.Repeat("0", 64)
	bad.RequestID = "invalid-delivery-hash"
	f.refuseConfirmation(bad, "CONFIRMATION_READ_IDENTITY_MISMATCH")
	f.checkAcceptedFiles(p, false)
	bad = input
	bad.RequestID = "invalid-delivery-variant"
	bad.VariantID = "not-prepared"
	f.checkEditableRefusal(prepared, bad)
	f.checkAcceptedFiles(p, false)
	s.stop(false)
	s.launch()
	pending := f.readAdmission(prepared, "")
	s.check("cold pending integration does not consume or rediscover plan", pending.State == "PENDING" && pending.Commit == nil && len(pending.Steps) == 0 && reflect.DeepEqual(pending.Content, prepared.Content), pending)
	f.checkAcceptedFiles(p, false)
	merged := f.commitAdmission(input)
	s.check("separate delivery has only one exact integration step", len(merged.Steps) == 1 && merged.Steps[0].Kind == "MERGE_FEATURE_AREA" && merged.Steps[0].State == "COMPLETED" && merged.Steps[0].FeatureAreaID == f.area && merged.Steps[0].RunID == "" && merged.Steps[0].ExecutionID == "", merged.Steps)
	for _, target := range plan.Members {
		name := f.repositoryName(target.RepositoryID)
		// This fixture has no target-side divergence: exact fast-forward, not a
		// manufactured merge commit, is the expected real Git integration.
		head := s.git(f.paths[name], "rev-parse", "HEAD")
		s.check("explicit delivery fast-forwards exact frozen accepted commit", head == target.SourceHead && head != target.ExpectedDevHead, head)
		f.expectedHeads[name] = head
		f.expectedSource[name] = p.expected[target.MemberID]
	}
	f.checkAcceptedFiles(p, true)
	f.checkAdmissionReplay(merged, f.commitAdmission(input))
	bad = input
	bad.VariantID = "changed-consumed-delivery"
	f.refuseConfirmation(bad, "CONFIRMATION_REQUEST_CONFLICT")
	f.checkAcceptedFiles(p, true)
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(merged, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(merged, f.commitAdmission(input))
	var replay directfile.ImplementationResult
	decode(s.gpt.callAs("fixture_user", "file_implementation_accept", p.input), &replay)
	s.check("cold accepted result remains exact NOT_RUN after separate integration", replay.Replay && replay.Accepted && replay.ID == p.result.ID && replay.VerificationStatus == "NOT_RUN", replay)
	f.checkAcceptedFiles(p, true)
}
