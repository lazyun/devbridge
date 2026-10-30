package simulation

import (
	"crypto/sha256"
	"fmt"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type fixture struct {
	s                                                       *scenario
	project, session, foreign, area, task, review, revision string
	paths, heads, repos, expectedSource, expectedHeads      map[string]string
	targets                                                 []workset.Target
	access                                                  review.UpperAccess
	upperClaim                                              review.WorkerClaimInput
	upperFirst                                              map[string]any
	report                                                  review.SubmitInput
	frozen                                                  task.Definition
	state                                                   directfile.State
	claim                                                   directfile.WorkerClaimInput
	first                                                   map[string]any
	resultInput                                             directfile.ImplementationResultInput
	result                                                  map[string]any
}

func variant(id, summary string) c.Variant {
	return c.Variant{ID: id, Label: summary, Summary: summary}
}
func (s *scenario) provision() *fixture {
	f := &fixture{s: s, paths: map[string]string{}, heads: map[string]string{}, repos: map[string]string{}, expectedSource: map[string]string{}, expectedHeads: map[string]string{}}
	inputs := []map[string]any{}
	for _, name := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(s.root, name)
		s.must(os.Mkdir(path, 0700))
		s.git(path, "init", "-b", name+"-development")
		s.must(os.WriteFile(filepath.Join(path, "same.txt"), []byte(name+" source\n"), 0600))
		s.git(path, "add", "same.txt")
		s.git(path, "commit", "-m", "Initial disposable simulation fixture")
		f.paths[name], f.heads[name], f.expectedSource[name] = path, s.git(path, "rev-parse", "HEAD"), name+" source\n"
		f.expectedHeads[name] = f.heads[name]
		inputs = append(inputs, map[string]any{"path": path, "alias": name})
	}
	registered := s.http(http.MethodPost, "/v1/projects", map[string]any{"request_id": "register-fixture", "name": "SIMULATED actors; disposable project", "repositories": inputs})
	f.project = field(registered, "project_id")
	for _, raw := range registered["repositories"].([]any) {
		r := raw.(map[string]any)
		f.repos[field(r, "name")] = field(r, "id")
	}
	f.session = field(s.gpt.call("session_initialize", map[string]any{"requestId": "initialize-upstream"}), "aiSessionId")
	f.foreign = field(s.agent.call("session_initialize", map[string]any{"requestId": "initialize-foreign"}), "aiSessionId")
	s.agent.callAs("fixture_user", "project_open", map[string]any{"aiSessionId": f.foreign, "projectId": f.project, "requestId": "bind-foreign"})
	for _, name := range []string{"app", "sdk", "backend"} {
		s.agent.callAs("fixture_user", "development_branch_select", map[string]any{"aiSessionId": f.foreign, "repositoryId": f.repos[name], "developmentRef": "refs/heads/" + name + "-development", "requestId": "select-" + name})
	}
	f.complete(c.PrepareInput{RequestID: "bind-upstream", Purpose: c.ProjectSetup, Action: "SELECT_PROJECT", ProjectSetup: &c.ProjectBody{Summary: "Open only this disposable project"}, Variants: []c.Variant{variant("select", "Select simulation fixture")}}, c.Selection{ProjectID: f.project})
	area, _ := f.complete(c.PrepareInput{RequestID: "create-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "SIMULATED contract update", ProblemStatement: "Exercise real Core state transitions without models"}, Variants: []c.Variant{variant("create", "Create disposable area")}}, c.Selection{})
	f.area = area.Steps[0].FeatureAreaID
	taskResult, _ := f.complete(c.PrepareInput{RequestID: "create-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "SIMULATED app and sdk update", ProblemStatement: "Update only app and sdk same.txt", Constraints: []string{"Preserve backend; no models or arbitrary commands"}}, Variants: []c.Variant{variant("create", "Create disposable Task")}}, c.Selection{})
	f.task = taskResult.Steps[0].TaskID
	f.checkPhase("new task", "ACTIVE", "REVIEW_REQUIRED")
	return f
}
func (f *fixture) prepare(in c.PrepareInput) c.Intent {
	in.AISessionID = f.session
	args := object(in)
	if in.Review != nil {
		body := args["review"].(map[string]any)
		body["reviewPlan"] = map[string]any{"mode": "REPOSITORY"}
		body["repositoryId"] = ""
		if targets, ok := body["repositoryTargets"].([]any); ok {
			for _, raw := range targets {
				t := raw.(map[string]any)
				if t["memberId"] == "" {
					delete(t, "memberId")
					delete(t, "memberVersion")
				}
			}
		}
	}
	if in.Action == "CREATE_TASK" {
		delete(args["task"].(map[string]any), "reviewPlan")
	}
	if in.Action == "MERGE_FEATURE_AREA" {
		delete(args["featureArea"].(map[string]any), "title")
		delete(args["featureArea"].(map[string]any), "problemStatement")
	}
	tool := map[c.Purpose]string{c.ProjectSetup: "project_setup_prepare", c.FeatureArea: "feature_area_prepare", c.Task: "task_prepare", c.Review: "review_prepare", c.Execution: "execution_prepare"}[in.Purpose]
	var prepared c.Intent
	decode(f.s.gpt.call(tool, args)["intent"], &prepared)
	f.s.check("prepared "+in.Action, prepared.IntentID != "", prepared.State)
	return prepared
}
func (f *fixture) complete(in c.PrepareInput, selection c.Selection) (c.Intent, c.CommitInput) {
	prepared := f.prepare(in)
	input := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID, Selection: selection}
	if in.Action == "APPROVE" {
		bad := input
		bad.ContentHash = strings.Repeat("0", 64)
		bad.RequestID = "invalid-hash-approval"
		denied := f.s.gpt.invoke("fixture_user", "confirmation_commit", bad)
		f.s.must(denied.Err)
		f.s.check("wrong confirmation hash is rejected", denied.Denied, denied.ErrorText)
		f.checkPhase("rejected confirmation cannot approve", "ACTIVE", "REVIEW_APPROVAL_PENDING")
	}
	var result c.Intent
	decode(f.s.gpt.callAs("fixture_user", "confirmation_commit", input)["intent"], &result)
	f.s.check("fixture-user confirmed "+in.Action, result.State == "COMPLETED", result)
	return result, input
}
func (f *fixture) workspace() map[string]any {
	return f.s.gpt.call("workspace_get", map[string]any{"aiSessionId": f.session, "taskId": f.task, "featureAreaId": f.area, "include": []string{"TASK"}})
}
func (f *fixture) checkPhase(label, status, phase string) map[string]any {
	ws := f.workspace()
	t := ws["task"].(map[string]any)
	f.s.check(label+" Task state", t["status"] == status && t["phase"] == phase, t)
	return ws
}
func (f *fixture) startReview() {
	targets := []workset.Target{}
	for _, name := range []string{"app", "sdk"} {
		targets = append(targets, workset.Target{RepositoryID: f.repos[name], OriginCommit: f.heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}
	v := variant("upper", "Admit deterministic simulated review worker")
	v.ExecutorKind = "UPPER_LLM"
	v.ReviewInstruction = "SIMULATED: Read both same.txt files and propose the exact fixture update."
	result, _ := f.complete(c.PrepareInput{RequestID: "start-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{v}}, c.Selection{})
	f.review = result.Steps[0].ReviewID
	decode(f.s.gpt.call("review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review}), &f.access)
	f.s.check("review is admitted by real Core", f.access.State == "ACTIVE" && f.access.ProtocolVersion == 2 && len(f.access.RepositoryTargets) == 2, f.access)
	f.upperClaim = review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "upper-delivery", WorkerID: "SIMULATED-review-worker"}}
	f.checkPhase("review admitted", "ACTIVE", "REVIEWING")
}
func (f *fixture) claimReview() {
	bad := f.upperClaim
	bad.AISessionID = f.foreign
	f.s.agent.deny("review_worker_claim", bad, "")
	f.upperFirst = f.s.agent.call("review_worker_claim", f.upperClaim)
	f.s.check("review payload delivered once", f.upperFirst["state"] == review.WorkerDispatchIntent && f.upperFirst["executionPayload"] != nil, f.upperFirst)
	f.receipt("review_worker_claim", f.upperClaim, f.upperFirst, review.WorkerOutcomeUnknown)
}
func (f *fixture) finishReview() {
	f.targets = workset.CloneTargets(f.access.RepositoryTargets)
	refs := []review.EvidenceRef{}
	for i, t := range f.targets {
		got := f.s.agent.call("file_read", map[string]any{"reviewTarget": f.access.Target, "repositoryId": t.RepositoryID, "memberId": t.MemberID, "memberVersion": t.MemberVersion, "path": "same.txt"})
		refs = append(refs, review.EvidenceRef{RepositoryID: t.RepositoryID, MemberID: t.MemberID, EvidenceID: field(got, "evidenceId"), Path: "same.txt", Digest: field(got, "digest")})
		f.targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "SIMULATED: Update exact app and sdk contracts", Constraints: []string{"Preserve backend"}, CodexInstruction: "SIMULATED deterministic worker: update same.txt in both exact members.\nPreserve unrelated bytes.\t", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verify := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both exact fixture members", AcceptanceCriteria: []string{"Both contracts have intended content"}, CodexInstruction: "Do not claim commands ran in the model-free simulator."}
	for _, name := range []string{"app", "sdk"} {
		verify.Checks = append(verify.Checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: f.repos[name], RepositoryIDs: []string{f.repos[name]}, WorkingDirectory: "."})
	}
	f.report = review.SubmitInput{UpperTarget: f.access.Target, WorkerClaim: &f.upperClaim.WorkerClaimIdentity, RequestID: "review-result", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: f.targets, PrimaryMemberID: f.targets[0].MemberID, Summary: "SIMULATED proposal grounded in actual Core file reads", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"Deterministic simulated reviewer; no model or command execution"}, RecommendedNextSteps: []string{"Explicit fixture-user approval, waiver and separate start"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verify}}
	f.s.agent.call("review_result_submit", f.report)
	f.receipt("review_worker_get", f.upperClaim, f.upperFirst, review.WorkerResultRecorded)
	ws := f.checkPhase("review report is not approval", "ACTIVE", "REVIEW_APPROVAL_PENDING")
	f.revision = field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	approve := variant("approve", "Approve fixture definition without starting")
	approve.ImplementationMode = task.ImplementationDirectFile
	approve.RepositoryTargets = f.targets
	approve.PrimaryMemberID = f.targets[0].MemberID
	approve.WorkSpec = &work
	approve.VerificationSpec = &verify
	premature := c.PrepareInput{AISessionID: f.session, RequestID: "start-before-approval", Purpose: c.Execution, Action: "START_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "START_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: "REVIEW_APPROVAL:" + f.review, TaskDefinitionRevisionID: f.revision}, Variants: []c.Variant{approve}}
	f.s.gpt.deny("execution_prepare", premature, "")
	f.checkPhase("start before approval cannot advance Task", "ACTIVE", "REVIEW_APPROVAL_PENDING")
	f.complete(c.PrepareInput{RequestID: "approve-no-start", Purpose: c.Execution, Action: "APPROVE", Execution: &c.ExecutionBody{Action: "APPROVE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: "REVIEW_APPROVAL:" + f.review, TaskDefinitionRevisionID: f.revision}, Variants: []c.Variant{approve}}, c.Selection{VerificationWaiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "SIMULATOR FIXTURE ONLY: user explicitly waives command verification; no real user Task or real model run"}})
	ws = f.checkPhase("approval is not execution", "ACTIVE", "IMPLEMENTATION_READY")
	f.revision = field(ws["taskSources"].(map[string]any), "currentDefinitionRevisionId")
	source := ws["taskSources"].(map[string]any)["selectedDefinition"].(map[string]any)
	decode(source["definition"], &f.frozen)
	f.s.check("explicit fixture waiver frozen", f.frozen.VerificationSpec.Mode == task.VerificationModeNone && f.frozen.WorkSpec.CodexInstruction == work.CodexInstruction, f.frozen)
}
func (f *fixture) directPreparation(action, request string) c.PrepareInput {
	work, verify := f.frozen.WorkSpec, f.frozen.VerificationSpec
	body := &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: "REVIEW_APPROVAL:" + f.review, TaskDefinitionRevisionID: f.revision}
	if action != "START_DIRECT_FILE" {
		body.WorkflowID = f.state.WorkflowID
		body.RuntimeEpoch = f.state.RuntimeEpoch
		body.GrantGeneration = f.state.GrantGeneration
	}
	v := variant("exact", "SIMULATED fixture user: "+action)
	v.ImplementationMode = task.ImplementationDirectFile
	v.RepositoryTargets = f.frozen.RepositoryTargets
	v.PrimaryMemberID = f.frozen.PrimaryMemberID
	v.WorkSpec = &work
	v.VerificationSpec = &verify
	return c.PrepareInput{RequestID: request, Purpose: c.Execution, Action: action, Execution: body, Variants: []c.Variant{v}}
}
func (f *fixture) startFile() {
	f.complete(f.directPreparation("START_DIRECT_FILE", "start-file"), c.Selection{})
	ws := f.workspace()
	decode(ws["taskView"].(map[string]any)["directFile"], &f.state)
	f.s.check("explicit file start grants v2 authority", f.state.Enabled && f.state.ProtocolVersion == 2, f.state)
	f.claim = directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: f.revision, DecisionID: f.state.DecisionID, WorkflowID: f.state.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "file-delivery", WorkerID: "SIMULATED-file-worker"}}
}
func (f *fixture) claimFile() {
	f.first = f.s.agent.call("file_worker_claim", f.claim)
	var got directfile.WorkerClaimResult
	decode(f.first, &got)
	f.s.check("file payload exactly matches approved definition", got.State == directfile.WorkerDispatchIntent && got.ExecutionPayload != nil && reflect.DeepEqual(got.ExecutionPayload.Definition, f.frozen) && got.ExecutionPayload.Instruction == f.frozen.WorkSpec.CodexInstruction, got)
}
func (f *fixture) receipt(tool string, args any, first map[string]any, state string) {
	got := f.s.agent.call(tool, args)
	f.s.check(tool+" stable receipt without redispatch", got["state"] == state && got["executionPayload"] == nil && reflect.DeepEqual(got["receipt"], first["receipt"]), got)
}
func (f *fixture) readArgs(t workset.Target) map[string]any {
	return map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "workflowId": f.state.WorkflowID, "repositoryId": t.RepositoryID, "memberId": t.MemberID, "memberVersion": t.MemberVersion, "runtimeEpoch": f.state.RuntimeEpoch, "grantGeneration": f.state.GrantGeneration, "path": "same.txt"}
}
func (f *fixture) writeArgs(t workset.Target, id, content string) map[string]any {
	args := f.readArgs(t)
	before := f.s.agent.call("file_read", args)
	args["action"] = "REPLACE"
	args["protocolVersion"] = 2
	args["requestId"] = id
	args["expectedDigest"] = before["digest"]
	args["content"] = content
	args["workerClaim"] = f.claim.WorkerClaimIdentity
	return args
}
func (f *fixture) submit() {
	f.resultInput = directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID, RequestID: "file-result", Summary: "SIMULATED worker made actual Core whole-file changes", VerificationNotes: "Explicit fixture-only waiver. No commands, native runtime, or model executed.", ProtocolVersion: 2, RuntimeEpoch: f.state.RuntimeEpoch, GrantGeneration: f.state.GrantGeneration, WorkerClaim: &f.claim.WorkerClaimIdentity}
	f.result = f.s.agent.call("file_implementation_result_submit", f.resultInput)
	f.s.check("report is neither verification nor acceptance", f.result["accepted"] == false && f.result["verificationStatus"] == "NOT_RUN" && f.result["executionId"] == nil, f.result)
	ws := f.checkPhase("report awaiting explicit acceptance", "ACTIVE", "ACCEPTANCE_PENDING")
	var closed directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &closed)
	f.s.check("result closes file authority", closed.State == "CLOSED" && !closed.Enabled, closed)
}
func (f *fixture) checkSources() {
	for _, name := range []string{"app", "sdk", "backend"} {
		b, err := os.ReadFile(filepath.Join(f.paths[name], "same.txt"))
		f.s.must(err)
		f.s.check("source bytes "+name, string(b) == f.expectedSource[name], string(b))
		f.s.check("source HEAD "+name, f.s.git(f.paths[name], "rev-parse", "HEAD") == f.expectedHeads[name], f.expectedHeads[name])
		f.s.check("source clean "+name, f.s.git(f.paths[name], "status", "--porcelain") == "", nil)
	}
}
func (f *fixture) setupFile() { f.startReview(); f.claimReview(); f.finishReview(); f.startFile() }

// Read-only independent evidence from this scenario's owned worktrees. Never
// opens SQLite or changes Core authority. Includes unapproved new files too.
func (f *fixture) managedFileSnapshot() map[string]string {
	root := filepath.Join(f.s.root, "data", "worktrees")
	out := map[string]string{}
	f.s.must(filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		digest := sha256.Sum256(raw)
		out[rel] = fmt.Sprintf("%x", digest)
		return nil
	}))
	return out
}
