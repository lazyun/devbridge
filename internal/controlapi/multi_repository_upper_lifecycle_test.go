package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// upperLifecycleFixture deliberately enters through the same project/session,
// confirmation, coordinator, workspace and SQLite services as production.  It
// keeps the third repository registered but outside the frozen Review set.
type upperLifecycleFixture struct {
	recoveryReport json.RawMessage
	t              *testing.T
	ctx            context.Context
	dataRoot       string
	plane          *controlplane.ControlPlane
	facade         *controlapi.Facade
	session        string
	project        string
	area           string
	task           string
	review         string
	run            string
	access         review.UpperAccess
	targets        []workset.Target
	repos          map[string]string
	heads          map[string]string
}

func newUpperLifecycleFixture(t *testing.T) *upperLifecycleFixture {
	t.Helper()
	return newUpperLifecycleFixtureScoped(t, "multi-upper-lifecycle-"+strings.ReplaceAll(t.Name(), "/", "-"))
}

// newUpperLifecycleFixtureScoped lets a test that enters through the MCP wire bind
// the Session to the caller scope the adapter resolves per request. The ChatGPT
// adapter owns one canonical data-instance scope, so a Session it must operate on
// is created against that scope rather than a test-only one.
func newUpperLifecycleFixtureScoped(t *testing.T, callerScope string) *upperLifecycleFixture {
	t.Helper()
	f := &upperLifecycleFixture{t: t, ctx: aisession.WithCallerScope(context.Background(), callerScope), repos: map[string]string{}, heads: map[string]string{}}
	root := t.TempDir()
	f.dataRoot = filepath.Join(root, "data")
	workflowtest.SeedEmptyDatabase(t, filepath.Join(f.dataRoot, "state"))
	var err error
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.plane != nil {
			_ = f.plane.Close()
		}
	})

	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture git %v: %s: %v", args, out, e)
		}
		return strings.TrimSpace(string(out))
	}
	inputs := make([]projectregistration.RepositoryInput, 0, 3)
	for _, name := range []string{"app", "sdk", "backend"} {
		path := filepath.Join(root, name)
		if err = os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		git(path, "init", "-b", name+"-development")
		if err = os.WriteFile(filepath.Join(path, "same.txt"), []byte(name+" source\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("DEVBRIDGE_CODING_V1_ASK_USER") != "" {
			writeDecisionFixtureInstructions(t, path)
			git(path, "add", "AGENTS.md")
		}
		git(path, "add", "same.txt")
		git(path, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
		f.heads[name] = git(path, "rev-parse", "HEAD")
		inputs = append(inputs, projectregistration.RepositoryInput{Path: path, Alias: name})
	}
	registered, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "register", Name: "Upper lifecycle", Repositories: inputs})
	if err != nil {
		t.Fatal(err)
	}
	f.project = registered.Project.ID
	ref := aiprojectselection.ClientBindingReference{CallerScope: callerScope}
	initialized, err := f.plane.AIContext.InitializeSession(f.ctx, ref, "initialize", "")
	if err != nil {
		t.Fatal(err)
	}
	f.session = initialized.AISessionID
	if _, err = f.plane.AIContext.BindProject(f.ctx, ref, f.session, f.project, "bind"); err != nil {
		t.Fatal(err)
	}
	values, err := f.plane.Repositories.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range values {
		f.repos[value.Name] = value.ID
	}
	for _, name := range []string{"app", "sdk", "backend"} {
		if f.repos[name] == "" {
			t.Fatalf("missing registered repository %q: %+v", name, values)
		}
		if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{AISessionID: f.session, ProjectID: f.project, RepositoryID: f.repos[name], DevelopmentRef: "refs/heads/" + name + "-development", RequestID: "select-" + name}); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"app", "sdk"} {
		f.targets = append(f.targets, workset.Target{RepositoryID: f.repos[name], OriginCommit: f.heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}

	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		prepared, e := f.facade.PrepareConfirmation(f.ctx, in)
		if e != nil {
			t.Fatalf("prepare %s: %v", in.Action, e)
		}
		got, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID})
		if e != nil || got.State != "COMPLETED" {
			t.Fatalf("commit %s: intent=%+v err=%v", in.Action, got, e)
		}
		return got
	}
	area := commit(c.PrepareInput{RequestID: "area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Lifecycle area", ProblemStatement: "Inspect app and sdk"}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	f.area = area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: "task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Lifecycle intent", ProblemStatement: "Inspect exact repositories", Constraints: []string{"read only"}}, Variants: []c.Variant{{ID: "create", Summary: "create"}}})
	f.task = created.Steps[0].TaskID
	started := commit(c.PrepareInput{RequestID: "review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: f.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from app and sdk"}}})
	f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil || f.access.State != "ACTIVE" {
		t.Fatalf("active access=%+v err=%v", f.access, err)
	}
	// Read the canonical frozen identities from admission, not the unfrozen
	// caller input used above.
	var raw string
	if err = f.plane.DB.SQL().QueryRow(`SELECT repository_targets_json FROM reviews WHERE id=?`, f.review).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	value, err := f.plane.Reviews.Get(f.ctx, f.review)
	if err != nil || len(value.RepositoryTargets) != 2 {
		t.Fatalf("frozen targets=%+v raw=%s err=%v", value.RepositoryTargets, raw, err)
	}
	f.targets = workset.CloneTargets(value.RepositoryTargets)
	return f
}

func (f *upperLifecycleFixture) upperVersion() *agentconversation.UpperVersion {
	return &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: f.access.Target.RuntimeEpoch, GrantGeneration: f.access.Target.GrantGeneration}
}

func (f *upperLifecycleFixture) inputFor(target workset.Target, path string) directfile.Input {
	return directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: path}
}

func (f *upperLifecycleFixture) findingsOnly(requestID string, target review.UpperTarget) review.SubmitInput {
	return review.SubmitInput{UpperTarget: target, RequestID: requestID, Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: workset.CloneTargets(f.targets), Summary: "Lifecycle finding", Disposition: review.DispositionFurtherReviewRequired, Coverage: []string{"app and sdk"}, Findings: []review.Finding{}, Limitations: []string{"read-only review"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}}
}

func TestMultiRepositoryUpperLifecycleCancelRevokesEveryMember(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	old := f.access.Target
	receipt, err := f.facade.CancelExecutionForSession(f.ctx, executioncancel.Input{AISessionID: f.session, OwnerType: "REVIEW", OwnerID: f.review, RunID: f.run, RequestID: "cancel-upper", Reason: "withdraw", ExpectedUpperVersion: f.upperVersion()})
	if err != nil || receipt.State != "CANCELED" || receipt.BackendDisposition != "NO_BACKEND" {
		t.Fatalf("cancel receipt=%+v err=%v", receipt, err)
	}
	for _, target := range f.targets {
		if _, err = f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt")); err == nil {
			t.Fatalf("late read authorized after cancellation for %+v", target)
		}
	}
	if _, err = f.facade.SubmitReviewResult(f.ctx, f.findingsOnly("late-report", old)); err == nil {
		t.Fatal("late report authorized after cancellation")
	}
	if _, err = f.facade.FileReplace(f.ctx, directfile.Input{ReviewTarget: &old, RepositoryID: f.targets[0].RepositoryID, MemberID: f.targets[0].MemberID, MemberVersion: f.targets[0].MemberVersion, Path: "same.txt", Content: "write"}); err == nil {
		t.Fatal("generic Direct File write accepted an Upper Review target")
	}
}

func TestMultiRepositoryUpperLifecycleRestartRecoveryPreservesExactSet(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	oldAccess := f.access
	oldEpoch := f.plane.DB.RuntimeEpoch()
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	var err error
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	if f.plane.DB.RuntimeEpoch() == oldEpoch {
		t.Fatal("restart reused runtime epoch")
	}
	for _, target := range f.targets {
		if _, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &oldAccess.Target, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "same.txt"}); err == nil {
			t.Fatalf("old epoch retained file authority for %+v", target)
		}
	}
	observed, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil || observed.State != "RECOVERY_REQUIRED" {
		t.Fatalf("post-restart access=%+v err=%v", observed, err)
	}
	expected := &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: oldAccess.Target.RuntimeEpoch, GrantGeneration: oldAccess.Target.GrantGeneration}
	recovered, err := f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{ExpectedUpperVersion: expected, FeatureAreaID: f.area, RunID: f.run, OperationKind: agentconversation.Review, OperationID: f.review, RequestID: "recover-upper"})
	if err != nil || recovered.GrantState != "PREPARING" || recovered.AuthorizedUpperVersion == nil || recovered.AuthorizedUpperVersion.GrantGeneration != oldAccess.Target.GrantGeneration+1 {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	newTarget := oldAccess.Target
	newTarget.RuntimeEpoch = recovered.AuthorizedUpperVersion.GrantEpoch
	newTarget.GrantGeneration = recovered.AuthorizedUpperVersion.GrantGeneration
	if _, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &newTarget, RepositoryID: f.targets[0].RepositoryID, MemberID: f.targets[0].MemberID, MemberVersion: f.targets[0].MemberVersion, Path: "same.txt"}); err == nil {
		t.Fatal("PREPARING recovery receipt granted file access before explicit prepare")
	}
	prepared, err := f.facade.PrepareUpperReview(f.ctx, newTarget)
	if err != nil || prepared.State != "ACTIVE" {
		t.Fatalf("prepare recovered access=%+v err=%v", prepared, err)
	}
	f.access = prepared
	for _, target := range f.targets {
		got, e := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if e != nil || got.Content == "" {
			t.Fatalf("recovered member read target=%+v result=%+v err=%v", target, got, e)
		}
	}
	if _, err = f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{ExpectedUpperVersion: expected, FeatureAreaID: f.area, RunID: f.run, OperationKind: agentconversation.Review, OperationID: f.review, RequestID: "recover-upper-conflict"}); err == nil {
		t.Fatal("stale recovery version reauthorized a second generation")
	}
}

func TestMultiRepositoryUpperLifecycleFrozenMembersAndRootIdentity(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	workspace, err := f.plane.Workspaces.EnsureForFeatureArea(f.ctx, f.project, f.repos["backend"], f.area, f.heads["backend"])
	if err != nil {
		t.Fatal(err)
	}
	third, err := workspace.MemberForRepository(f.repos["backend"])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &f.access.Target, RepositoryID: third.RepositoryID, MemberID: third.ID, MemberVersion: third.Version, Path: "same.txt"}); err == nil {
		t.Fatal("member appended after grant expanded the frozen read set")
	}

	first := f.targets[0]
	var worktreePath string
	if err = f.plane.DB.SQL().QueryRow(`SELECT wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=? AND b.repository_id=? AND b.member_id=?`, f.run, first.RepositoryID, first.MemberID).Scan(&worktreePath); err != nil {
		t.Fatal(err)
	}
	saved := worktreePath + ".saved"
	if err = os.Rename(worktreePath, saved); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(saved, worktreePath); err != nil {
		_ = os.Rename(saved, worktreePath)
		t.Fatal(err)
	}
	if _, err = f.facade.UpperReviewRead(f.ctx, f.inputFor(first, "same.txt")); err == nil {
		t.Fatal("symlinked worktree root retained Upper authority")
	}
	if err = os.Remove(worktreePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(saved, worktreePath); err != nil {
		t.Fatal(err)
	}

	// A different directory at the same pathname must also fail the pinned root
	// identity check, even when its visible file bytes are identical.
	saved = worktreePath + ".original"
	if err = os.Rename(worktreePath, saved); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(worktreePath, 0o700); err != nil {
		_ = os.Rename(saved, worktreePath)
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(worktreePath, "same.txt"), []byte(strings.TrimSpace(first.RepositoryID)+" source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.UpperReviewRead(f.ctx, f.inputFor(first, "same.txt")); err == nil {
		t.Fatal("root inode replacement retained Upper authority")
	}
	if err = os.RemoveAll(worktreePath); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(saved, worktreePath); err != nil {
		t.Fatal(err)
	}

	if _, err = f.facade.SubmitReviewResult(f.ctx, f.findingsOnly("final-report", f.access.Target)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{ExpectedUpperVersion: f.upperVersion(), FeatureAreaID: f.area, RunID: f.run, OperationKind: agentconversation.Review, OperationID: f.review, RequestID: "recover-finalized"}); err == nil {
		t.Fatal("finalized grant admitted recovery")
	}
}

func mcpObject(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func callUpperMCP(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("%s transport error: %v", name, err)
	}
	if result.IsError {
		raw, _ := json.Marshal(result.Content)
		t.Fatalf("%s returned protocol error: %s structured=%+v", name, raw, result.StructuredContent)
	}
	return mcpObject(t, result.StructuredContent)
}

// This test crosses the actual MCP JSON-schema and typed-handler boundary. It
// protects fields that an in-process facade call cannot detect as missing:
// frozen repository targets, member/source identity on file results, the v2
// structured proposal, and the final approval confirmation envelope.
func TestMultiRepositoryUpperMCPProtocolRoundTrip(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "multi-upper-protocol-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "multi-upper-protocol", Version: "1"}, nil)
	mcpserver.RegisterUpperReviewTools(server, service)
	mcpserver.RegisterDirectFileTools(server, service)
	mcpserver.RegisterConfirmationTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	callCtx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	serverSession, err := server.Connect(callCtx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "multi-upper-protocol-client", Version: "1"}, nil)
	session, err := client.Connect(callCtx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(callCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	for _, name := range []string{"review_access_get", "file_read", "review_result_submit", "execution_prepare", "confirmation_commit"} {
		if tools[name] == nil {
			t.Fatalf("missing production MCP tool %s", name)
		}
	}

	accessWire := callUpperMCP(t, callCtx, session, "review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review})
	if accessWire["protocolVersion"] != float64(2) {
		t.Fatalf("review_access_get lost v2 identity: %+v", accessWire)
	}
	wireTargets, ok := accessWire["repositoryTargets"].([]any)
	if !ok || len(wireTargets) != 2 {
		t.Fatalf("review_access_get targets=%+v", accessWire["repositoryTargets"])
	}
	var access review.UpperAccess
	raw, _ := json.Marshal(accessWire)
	if err = json.Unmarshal(raw, &access); err != nil || access.State != "ACTIVE" || len(access.RepositoryTargets) != 2 {
		t.Fatalf("decode access=%+v err=%v", access, err)
	}

	refs := make([]review.EvidenceRef, 0, 2)
	for _, target := range access.RepositoryTargets {
		fileWire := callUpperMCP(t, callCtx, session, "file_read", map[string]any{"reviewTarget": mcpObject(t, access.Target), "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": target.MemberVersion, "path": "same.txt"})
		if fileWire["repositoryId"] != target.RepositoryID || fileWire["memberId"] != target.MemberID || fileWire["memberVersion"] != float64(target.MemberVersion) || fileWire["sourceKind"] != "MANAGED_WORKTREE" || fileWire["workspaceId"] != access.WorkspaceID || fileWire["worktreeId"] == "" || fileWire["originCommit"] != target.OriginCommit || fileWire["startSnapshot"] == "" {
			t.Fatalf("file_read lost exact source identity: target=%+v wire=%+v", target, fileWire)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: fileWire["evidenceId"].(string), RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: fileWire["digest"].(string)})
	}

	implementation := workset.CloneTargets(access.RepositoryTargets)
	checks := make([]task.VerificationCheck, 0, len(implementation))
	for i := range implementation {
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: implementation[i].RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{implementation[i].RepositoryID}})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both repository contracts", Constraints: []string{"Preserve public API"}, CodexInstruction: "Update exact files", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both repositories", Checks: checks, AcceptanceCriteria: []string{"Both checks pass"}, CodexInstruction: "Run exact checks"}
	submission := review.SubmitInput{UpperTarget: access.Target, RequestID: "mcp-report", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Both repositories reviewed", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"source inspection only"}, RecommendedNextSteps: []string{"approve without start"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	reportArguments := mcpObject(t, submission)
	// Model a committed result whose first response is lost: deliberately do not
	// inspect any content from the first call, then resend the byte-equivalent
	// request identity and payload through the transport.
	if _, err = session.CallTool(callCtx, &sdkmcp.CallToolParams{Name: "review_result_submit", Arguments: reportArguments}); err != nil {
		t.Fatalf("first review_result_submit transport failed: %v", err)
	}
	reportWire := callUpperMCP(t, callCtx, session, "review_result_submit", reportArguments)
	if reportWire["reviewId"] != f.review || reportWire["runId"] != f.run || reportWire["status"] != "COMPLETED" || reportWire["payloadSha256"] == "" {
		t.Fatalf("review_result_submit receipt=%+v", reportWire)
	}
	var reportRows, receiptRows int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_reports WHERE review_id=?`, f.review).Scan(&reportRows); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_result_receipts WHERE review_id=? AND request_id=?`, f.review, submission.RequestID).Scan(&receiptRows); err != nil || reportRows != 1 || receiptRows != 1 {
		t.Fatalf("response-loss replay duplicated state reports=%d receipts=%d err=%v", reportRows, receiptRows, err)
	}

	prepare := c.PrepareInput{AISessionID: f.session, RequestID: "mcp-approve", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "approve", Label: "Approve", Summary: "Approve exact v2 definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}}
	preparedWire := callUpperMCP(t, callCtx, session, "execution_prepare", mcpObject(t, prepare))
	preparedRaw, _ := json.Marshal(preparedWire)
	var preparedResult c.Result
	if err = json.Unmarshal(preparedRaw, &preparedResult); err != nil || preparedResult.Intent == nil {
		t.Fatalf("execution_prepare envelope=%+v err=%v", preparedWire, err)
	}
	intent := preparedResult.Intent
	commit := c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "mcp-approve-commit", ContentHash: intent.ContentHash, VariantID: "approve"}
	committedWire := callUpperMCP(t, callCtx, session, "confirmation_commit", mcpObject(t, commit))
	committedRaw, _ := json.Marshal(committedWire)
	var committed c.Result
	if err = json.Unmarshal(committedRaw, &committed); err != nil || committed.Intent == nil || committed.Intent.State != "COMPLETED" || len(committed.Intent.Steps) != 1 || committed.Intent.Steps[0].RevisionID == "" {
		t.Fatalf("confirmation_commit envelope=%+v decoded=%+v err=%v", committedWire, committed, err)
	}
	var version int
	var targetJSON string
	if err = f.plane.DB.SQL().QueryRow(`SELECT json_extract(payload_json,'$.version'),json_extract(payload_json,'$.repositoryTargets') FROM task_definition_revisions WHERE id=?`, committed.Intent.Steps[0].RevisionID).Scan(&version, &targetJSON); err != nil || version != 2 || targetJSON == "" {
		t.Fatalf("persisted MCP approval version=%d targets=%q err=%v", version, targetJSON, err)
	}
}

func submitLifecycleApprovalReady(t *testing.T, f *upperLifecycleFixture, requestID string) ([]workset.Target, task.WorkSpec, task.VerificationSpec, review.ReportReceipt) {
	t.Helper()
	implementation := workset.CloneTargets(f.targets)
	refs := make([]review.EvidenceRef, 0, len(f.targets))
	checks := make([]task.VerificationCheck, 0, len(f.targets))
	for i, target := range f.targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: got.Path, Digest: got.Digest})
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{target.RepositoryID}})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update both contracts", Constraints: []string{"preserve compatibility"}, CodexInstruction: "Change both exact files", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both repositories", Checks: checks, AcceptanceCriteria: []string{"Both repositories pass"}, CodexInstruction: "Run both checks"}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, review.SubmitInput{UpperTarget: f.access.Target, RequestID: requestID, Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Approval-ready exact multi-repository proposal", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"source review only"}, RecommendedNextSteps: []string{"resolve the review decision"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}})
	if err != nil {
		t.Fatal(err)
	}
	return implementation, work, verification, receipt
}

func prepareLifecycleReviewDecision(t *testing.T, f *upperLifecycleFixture, action, requestID string) (c.Intent, c.CommitInput) {
	t.Helper()
	body := &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review)}
	if action == "REVISE" {
		body.ReviewFeedback = "Re-review the exact formal result and address its unresolved findings."
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: requestID, Purpose: c.Review, Action: action, Review: body, Variants: []c.Variant{{ID: strings.ToLower(action), Label: action, Summary: action + " exact current Review"}}})
	if err != nil {
		t.Fatal(err)
	}
	return prepared, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-" + requestID, ContentHash: prepared.ContentHash, VariantID: strings.ToLower(action)}
}

func TestMultiRepositoryUpperFindingsOnlyFurtherIsTerminalAndReplayable(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	in := f.findingsOnly("further-result", f.access.Target)
	// Findings-only v2 reports do not propose an implementation target set.
	in.Report.RepositoryTargets = nil
	first, err := f.facade.SubmitReviewResult(f.ctx, in)
	if err != nil || first.Status != "COMPLETED" || first.Disposition != review.DispositionFurtherReviewRequired {
		t.Fatalf("findings-only receipt=%+v err=%v", first, err)
	}
	replay, err := f.facade.SubmitReviewResult(f.ctx, in)
	if err != nil || replay.ReportID != first.ReportID || replay.PayloadSHA256 != first.PayloadSHA256 {
		t.Fatalf("findings-only replay=%+v err=%v", replay, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.Status != task.StatusActive || current.Phase != task.PhaseReviewRequired || current.RequiredReviewDepth == nil || *current.RequiredReviewDepth != task.ReviewDepthDeep {
		t.Fatalf("task after FURTHER=%+v err=%v", current, err)
	}
	var approvals, claims int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_approval_decisions WHERE decision_id=?`, wp.DecisionID(f.review)).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_agent_claims WHERE run_id=?`, f.run).Scan(&claims); err != nil || approvals != 0 || claims != 0 {
		t.Fatalf("FURTHER left approval/claim approvals=%d claims=%d err=%v", approvals, claims, err)
	}
	late := in
	late.RequestID = "late-further-result"
	if _, err = f.facade.SubmitReviewResult(f.ctx, late); err == nil {
		t.Fatal("a second result request replaced the completed FURTHER report")
	}
	changed := in
	changed.Report.Summary = "changed payload after response loss"
	if _, err = f.facade.SubmitReviewResult(f.ctx, changed); err == nil {
		t.Fatal("same result requestId accepted a changed payload")
	}
	if _, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "stale-approve-after-further", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "approve", Label: "Approve", Summary: "must fail", ImplementationMode: task.ImplementationCodingAgent}}}); err == nil {
		t.Fatal("FURTHER report exposed an approval decision")
	}
}

func TestMultiRepositoryUpperBlockedWithoutEvidenceSurvivesUnavailableMember(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	var worktreePath string
	if err := f.plane.DB.SQL().QueryRow(`SELECT wt.path FROM run_repository_bindings b JOIN worktrees wt ON wt.id=b.worktree_id WHERE b.run_id=? ORDER BY b.repository_id LIMIT 1`, f.run).Scan(&worktreePath); err != nil {
		t.Fatal(err)
	}
	saved := worktreePath + ".blocked-unavailable"
	if err := os.Rename(worktreePath, saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(saved); err == nil {
			_ = os.Rename(saved, worktreePath)
		}
	})
	in := review.SubmitInput{UpperTarget: f.access.Target, RequestID: "blocked-unavailable", Report: review.ReviewReport{SchemaVersion: 2, Summary: "A frozen member became unavailable", Disposition: review.DispositionBlocked, Coverage: []string{"frozen app and sdk target set"}, Findings: []review.Finding{}, Limitations: []string{"one admitted member root is unavailable"}, RecommendedNextSteps: []string{"restore the exact member and start a new Review"}, EvidenceRefs: []review.EvidenceRef{}}}
	receipt, err := f.facade.SubmitReviewResult(f.ctx, in)
	if err != nil || receipt.Status != "COMPLETED" || receipt.Disposition != review.DispositionBlocked {
		t.Fatalf("BLOCKED result=%+v err=%v", receipt, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseReviewRequired || current.RequiredReviewDepth == nil || *current.RequiredReviewDepth != task.ReviewDepthDeep {
		t.Fatalf("task after BLOCKED=%+v err=%v", current, err)
	}
}

func TestMultiRepositoryUpperReviewDecisionReviseAndStaleDecision(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-revise")
	prepared, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-revise")
	completed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || completed.State != "COMPLETED" || completed.IntentID != prepared.IntentID {
		t.Fatalf("REVISE result=%+v err=%v", completed, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.Status != task.StatusActive || current.Phase != task.PhaseReviewing || current.CurrentReviewID == f.review || current.RequiredReviewDepth == nil {
		t.Fatalf("task after REVISE=%+v err=%v", current, err)
	}
	var taskCount, reviewCount, revisionCount, executionCount int
	for query, target := range map[string]*int{
		`SELECT count(*) FROM tasks WHERE id=?`:                          &taskCount,
		`SELECT count(*) FROM reviews WHERE task_id=?`:                   &reviewCount,
		`SELECT count(*) FROM task_definition_revisions WHERE task_id=?`: &revisionCount,
		`SELECT count(*) FROM execution_proposals WHERE task_id=?`:       &executionCount,
	} {
		if err = f.plane.DB.SQL().QueryRow(query, f.task).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if taskCount != 1 || reviewCount != 2 || revisionCount != 1 || executionCount != 0 {
		t.Fatalf("successor entities task=%d reviews=%d revisions=%d executions=%d", taskCount, reviewCount, revisionCount, executionCount)
	}
	if _, err = f.facade.ResolveReviewApproval(f.ctx, wp.ResolveInput{AISessionID: f.session, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), Option: "REJECT", RequestID: "stale-after-revise"}); err == nil {
		t.Fatal("resolved Review decision remained current after REVISE")
	}
}

func TestMultiRepositoryUpperReviewDecisionRejectFencesPreparedRival(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-reject")
	rival, staleRevise := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-rival-revise")
	if _, err := f.facade.DiscardConfirmationProposal(f.ctx, nodeRead(rival)); err != nil {
		t.Fatal(err)
	}
	_, reject := prepareLifecycleReviewDecision(t, f, "REJECT", "prepare-reject")
	completed, err := f.facade.CommitConfirmation(f.ctx, reject)
	if err != nil || completed.State != "COMPLETED" {
		t.Fatalf("REJECT result=%+v err=%v", completed, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.Status != task.StatusCanceled {
		t.Fatalf("task after REJECT=%+v err=%v", current, err)
	}
	if _, err = f.facade.CommitConfirmation(f.ctx, staleRevise); err == nil {
		t.Fatal("prepared rival decision committed after REJECT")
	}
	if _, err = f.facade.SubmitReviewResult(f.ctx, f.findingsOnly("late-after-reject", f.access.Target)); err == nil {
		t.Fatal("late result committed after terminal Review decision")
	}
}

func TestMultiRepositoryReviewDecisionDisplayAndTopologyStaleReadback(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	submitLifecycleApprovalReady(t, f, "approval-for-topology-stale")
	prepared, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "prepare-topology-stale")
	if prepared.Content.Review == nil || prepared.Content.Review.RepositoryID != "" || prepared.Content.Review.PreparedTopologyRevision < 1 || prepared.Content.Review.PreparedDevelopmentContextHash == "" {
		t.Fatalf("project-scope frozen content=%+v", prepared.Content.Review)
	}
	display := f.facade.ConfirmationDisplay(f.ctx, prepared)
	if display == nil || display.ProjectName == "" || display.TaskName == "" || display.FeatureAreaName == "" || display.RepositoryID != "" || display.RepositoryCount != 3 || display.ReviewID != f.review || display.ReviewOrdinal != 1 || display.DecisionID != wp.DecisionID(f.review) {
		t.Fatalf("project-scope display=%+v", display)
	}

	path := filepath.Join(filepath.Dir(f.dataRoot), "new-topology-member")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, output, err)
		}
	}
	git("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("new member\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
	if _, err := f.plane.ProjectRegistration.Register(f.ctx, projectregistration.Input{RequestID: "append-after-confirmation", ProjectID: f.project, Repositories: []projectregistration.RepositoryInput{{Path: path, Alias: "new-member"}}}); err != nil {
		t.Fatal(err)
	}

	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || options.Eligibility.Available || options.Eligibility.ReasonCode != "PROJECT_TOPOLOGY_CHANGED" || !options.Eligibility.ReprepareAllowed || options.Eligibility.CurrentTopologyRevision == options.Eligibility.PreparedTopologyRevision {
		t.Fatalf("stale options=%+v err=%v", options.Eligibility, err)
	}
	// Bypass the facade preflight to prove the durable BeginCommit transaction
	// repeats the same generation fences before it records consumption.
	commit.SubmittedHash = c.ValueHash(commit)
	atomic, fresh, atomicErr := f.plane.Confirmations.BeginCommit(f.ctx, commit, nil)
	var atomicFailure *c.Failure
	if fresh || !errors.As(atomicErr, &atomicFailure) || atomicFailure.Code != "PROJECT_TOPOLOGY_CHANGED" || atomic.Submission == nil || atomic.Submission.Outcome != c.RejectedNotConsumed {
		t.Fatalf("atomic stale fence result=%+v fresh=%v failure=%+v err=%v", atomic, fresh, atomicFailure, atomicErr)
	}
	result, err := f.facade.CommitConfirmation(f.ctx, commit)
	var failure *c.Failure
	if !errors.As(err, &failure) || failure.Code != "PROJECT_TOPOLOGY_CHANGED" || result.Submission == nil || result.Submission.Outcome != c.RejectedNotConsumed || result.Submission.Consumed == nil || *result.Submission.Consumed || result.Submission.Failure == nil || result.Submission.Failure.Message == "" {
		t.Fatalf("typed stale commit result=%+v failure=%+v err=%v", result, failure, err)
	}
	read, readErr := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: commit.RequestID})
	if readErr != nil || read.Submission == nil || read.Submission.PayloadSHA256 == "" || read.Submission.BusinessOutcome != "REJECTED_NOT_CONSUMED" || read.Submission.Failure == nil || read.Submission.Failure.Code != "PROJECT_TOPOLOGY_CHANGED" {
		t.Fatalf("typed stale readback=%+v err=%v", read.Submission, readErr)
	}
	var reviews, runs int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM reviews WHERE task_id=?`, f.task).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 || runs != 1 {
		t.Fatalf("stale rejection created effects reviews=%d runs=%d", reviews, runs)
	}
}

// Approval input echoes the actual reviewed parent, including the initial
// authored draft. It never invents a revision or reconstructs approved scope.
func exactReviewApprovalBody(t *testing.T, ctx context.Context, facade *controlapi.Facade, taskID, areaID, reviewID, action string) *c.ExecutionBody {
	t.Helper()
	current, err := facade.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentDefinitionRevisionID == "" {
		t.Fatal("Review approval has no actual authored parent revision")
	}
	return &c.ExecutionBody{Action: action, TaskID: taskID, FeatureAreaID: areaID, ReviewID: reviewID, DecisionID: wp.DecisionID(reviewID), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID}
}

func assertInitialAuthoredDraft(t *testing.T, ctx context.Context, facade *controlapi.Facade, session, taskID string) {
	t.Helper()
	current, err := facade.GetTask(ctx, taskID)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := facade.TaskDefinitionRevisionForSession(ctx, session, current.CurrentDefinitionRevisionID)
	if err != nil || revision.Definition.Version != 1 || revision.Definition.ReviewApprovalID != "" || len(revision.Definition.RepositoryTargets) != 0 || revision.Definition.PrimaryMemberID != "" || revision.Definition.WorkSpec.ImplementationMode != "" {
		t.Fatalf("initial draft acquired approval/scope: %+v %v", revision, err)
	}
}
