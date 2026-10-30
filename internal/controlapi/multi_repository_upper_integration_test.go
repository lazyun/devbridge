package controlapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/projectregistration"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMultiRepositoryUpperApprovalProductionClosure(t *testing.T) {
	ctx := aisession.WithCallerScope(context.Background(), "multi-upper-v2")
	root := t.TempDir()
	plane, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	facade, err := controlapi.NewFromControlPlane(plane)
	if err != nil {
		t.Fatal(err)
	}
	git := func(path string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = path
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("fixture git %v: %s %v", args, out, e)
		}
		return strings.TrimSpace(string(out))
	}
	inputs := []projectregistration.RepositoryInput{}
	heads := []string{}
	paths := []string{}
	for _, name := range []string{"app", "sdk", "backend"} {
		p := filepath.Join(root, name)
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
		git(p, "init", "-b", name+"-development")
		if e := os.WriteFile(filepath.Join(p, "same.txt"), []byte(name+" source\n"), 0600); e != nil {
			t.Fatal(e)
		}
		git(p, "add", "same.txt")
		git(p, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "initial")
		heads = append(heads, git(p, "rev-parse", "HEAD"))
		paths = append(paths, p)
		inputs = append(inputs, projectregistration.RepositoryInput{Path: p, Alias: name})
	}
	registered, err := plane.ProjectRegistration.Register(ctx, projectregistration.Input{RequestID: "register", Name: "Multi upper", Repositories: inputs})
	if err != nil {
		t.Fatal(err)
	}
	ref := aiprojectselection.ClientBindingReference{CallerScope: "multi-upper-v2"}
	session, err := plane.AIContext.InitializeSession(ctx, ref, "init", "")
	if err != nil {
		t.Fatal(err)
	}
	sid := session.AISessionID
	if _, err = plane.AIContext.BindProject(ctx, ref, sid, registered.Project.ID, "bind"); err != nil {
		t.Fatal(err)
	}
	repos, err := plane.Repositories.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targets := []workset.Target{}
	for _, name := range []string{"app", "sdk"} {
		for _, repo := range repos {
			if repo.Name == name {
				n := 0
				if name == "sdk" {
					n = 1
				}
				if _, e := facade.SelectDevelopmentBranchForSession(ctx, developmentcycle.SelectInput{AISessionID: sid, ProjectID: registered.Project.ID, RepositoryID: repo.ID, DevelopmentRef: "refs/heads/" + name + "-development", RequestID: "select-" + name}); e != nil {
					t.Fatal(e)
				}
				targets = append(targets, workset.Target{RepositoryID: repo.ID, OriginCommit: heads[n], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
			}
		}
	}
	if len(targets) != 2 {
		t.Fatalf("targets=%+v repos=%+v", targets, repos)
	}
	if err = os.Rename(paths[2], paths[2]+"-offline"); err != nil {
		t.Fatal(err)
	}
	commit := func(in c.PrepareInput, loseReceipt bool) (c.Intent, c.CommitInput) {
		t.Helper()
		in.AISessionID = sid
		p, e := facade.PrepareConfirmation(ctx, in)
		if e != nil {
			t.Fatalf("prepare %s: %v", in.Action, e)
		}
		ci := c.CommitInput{AISessionID: sid, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, Action: p.Action, RequestID: "commit-" + in.RequestID, ContentHash: p.ContentHash, VariantID: in.Variants[0].ID}
		if loseReceipt && in.Action != "APPROVE" {
			if _, e = plane.DB.SQL().Exec(`CREATE TRIGGER multi_upper_receipt_fault BEFORE UPDATE OF intent_json ON confirmation_intents WHEN json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'injected final upper receipt loss'); END`); e != nil {
				t.Fatal(e)
			}
		}
		got, e := facade.CommitConfirmation(ctx, ci)
		if loseReceipt && in.Action == "APPROVE" {
			// Formal approval seals completion atomically. Discard its successful
			// response, then reconstruct the exact receipt with a pure read.
			if e != nil || got.State != "COMPLETED" || len(got.Steps) != 1 || got.Steps[0].RevisionID == "" {
				t.Fatalf("formal approval: state=%s err=%v", got.State, e)
			}
			original := got
			var before string
			if e = plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, p.IntentID).Scan(&before); e != nil {
				t.Fatal(e)
			}
			got, e = facade.ReadConfirmation(ctx, c.ReadInput{AISessionID: sid, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash, RequestID: ci.RequestID})
			if e != nil || got.State != "COMPLETED" || !reflect.DeepEqual(got.Steps, original.Steps) {
				t.Fatalf("lost approval response readback: state=%s err=%v", got.State, e)
			}
			var after string
			if e = plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, p.IntentID).Scan(&after); e != nil || after != before {
				t.Fatalf("approval readback wrote journal: %v", e)
			}
			return got, ci
		}

		if loseReceipt {
			if e == nil || got.State != "CONFIRMING" || len(got.Steps) != 1 || got.Steps[0].State != "UNKNOWN" {
				t.Fatalf("receipt fault %s: %+v %v", in.Action, got, e)
			}
			if _, e = plane.DB.SQL().Exec(`DROP TRIGGER multi_upper_receipt_fault`); e != nil {
				t.Fatal(e)
			}
			var before string
			if e = plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, p.IntentID).Scan(&before); e != nil {
				t.Fatal(e)
			}
			var lost c.Intent
			if e = json.Unmarshal([]byte(before), &lost); e != nil || lost.State != "CONFIRMING" || len(lost.Steps) != 1 || lost.Steps[0].State != "UNKNOWN" {
				t.Fatalf("durable interrupted %s: %+v %v", in.Action, lost, e)
			}
			var canonicalReview, canonicalRun, canonicalRevision string
			switch in.Action {
			case "START_REVIEW":
				e = plane.DB.SQL().QueryRow(`SELECT review_id,run_id FROM feature_area_review_start_requests WHERE feature_area_id=? AND request_id=? AND outcome='ACCEPTED'`, in.Review.FeatureAreaID, lost.Steps[0].RequestID).Scan(&canonicalReview, &canonicalRun)
			case "APPROVE":
				e = plane.DB.SQL().QueryRow(`SELECT m.initial_revision_id FROM decision_resolution_receipts r JOIN review_approval_materializations m ON m.decision_id=r.decision_id JOIN confirmation_approval_sources source ON source.decision_id=r.decision_id WHERE r.project_id=? AND r.request_id=? AND r.result_id=? AND r.option='APPROVE' AND source.intent_id=?`, registered.Project.ID, lost.Steps[0].RequestID, in.Execution.TaskID, p.IntentID).Scan(&canonicalRevision)
			default:
				t.Fatalf("unhandled receipt fault action %s", in.Action)
			}
			if e != nil {
				t.Fatalf("%s canonical effect did not commit: %v", in.Action, e)
			}
			got, e = facade.ReadConfirmation(ctx, c.ReadInput{AISessionID: sid, IntentID: p.IntentID, Version: p.Version, Purpose: p.Purpose, ContentHash: p.ContentHash})
			if e != nil || got.State != "COMPLETED" || len(got.Steps) != 1 || got.Steps[0].RequestID != lost.Steps[0].RequestID || got.Steps[0].ReviewID != canonicalReview || got.Steps[0].RunID != canonicalRun || got.Steps[0].RevisionID != canonicalRevision {
				t.Fatalf("%s exact receipt recovery: %+v %v", in.Action, got, e)
			}
			var after string
			if e = plane.DB.SQL().QueryRow(`SELECT intent_json FROM confirmation_intents WHERE intent_id=?`, p.IntentID).Scan(&after); e != nil || after != before {
				t.Fatalf("%s read recovery wrote confirmation: %v", in.Action, e)
			}
		}
		if e != nil || got.State != "COMPLETED" {
			for cause := e; cause != nil; cause = errors.Unwrap(cause) {
				t.Logf("failure cause: %T %v", cause, cause)
			}
			var prep string
			_ = plane.DB.SQL().QueryRow(`SELECT COALESCE(preparation_error,'') FROM review_grants LIMIT 1`).Scan(&prep)
			t.Fatalf("commit %s: state=%s steps=%+v error=%v prep=%s", in.Action, got.State, got.Steps, e, prep)
		}
		return got, ci
	}
	area, _ := commit(c.PrepareInput{RequestID: "area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Cross repo change", ProblemStatement: "Inspect app and sdk together"}, Variants: []c.Variant{{ID: "create", Summary: "Create area"}}}, false)
	areaID := area.Steps[0].FeatureAreaID
	created, _ := commit(c.PrepareInput{RequestID: "task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: areaID, Title: "Review cross repo intent", ProblemStatement: "Implement app sdk change", Constraints: []string{"Keep both contracts"}}, Variants: []c.Variant{{ID: "create", Summary: "Create intent"}}}, false)
	taskID := created.Steps[0].TaskID
	owner, err := plane.Tasks.Get(ctx, taskID)
	if err != nil || owner.RepositoryID != "" || owner.BaseCommit != "" {
		t.Fatalf("intent=%+v %v", owner, err)
	}
	proposal := c.PrepareInput{AISessionID: sid, RequestID: "review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: taskID, FeatureAreaID: areaID, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "Read app and sdk", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt in each repository and propose the exact change"}}}
	prepared, err := facade.PrepareConfirmation(ctx, proposal)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"reviews", "runs", "review_grants", "workspaces", "worktrees", "feature_area_agent_claims"} {
		var count int
		if e := plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); e != nil || count != 0 {
			t.Fatalf("prepare side effect %s=%d %v", table, count, e)
		}
	}
	started, startInput := commit(proposal, true)
	reviewID, runID := started.Steps[0].ReviewID, started.Steps[0].RunID
	replay, err := facade.CommitConfirmation(ctx, startInput)
	if err != nil || replay.Steps[0].ReviewID != reviewID || replay.Steps[0].RunID != runID {
		t.Fatalf("admission replay=%+v %v", replay, err)
	}
	access, err := facade.UpperReviewAccessForSession(ctx, sid, reviewID)
	if err != nil || access.State != "ACTIVE" {
		t.Fatalf("access=%+v %v", access, err)
	}
	frozen := prepared.Content.Review.RepositoryTargets
	refs := []review.EvidenceRef{}
	for _, target := range frozen {
		read, e := facade.UpperReviewRead(ctx, directfile.Input{ReviewTarget: &access.Target, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "same.txt"})
		if e != nil {
			t.Fatal(e)
		}
		repo, _ := plane.Repositories.Get(ctx, target.RepositoryID)
		want := repo.Name + " source\n"
		if read.RepositoryID != target.RepositoryID || read.MemberID != target.MemberID || read.MemberVersion != target.MemberVersion || read.SourceKind != "MANAGED_WORKTREE" || read.OriginCommit != target.OriginCommit || read.StartSnapshot == "" {
			t.Fatalf("wrong source evidence %+v", read)
		}
		if read.Content != want {
			t.Fatalf("wrong content=%+v", read)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: read.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: read.Digest})
	}
	for _, in := range []directfile.Input{{ReviewTarget: &access.Target, Path: "same.txt"}, {ReviewTarget: &access.Target, RepositoryID: frozen[0].RepositoryID, MemberID: frozen[1].MemberID, MemberVersion: 1, Path: "same.txt"}, {ReviewTarget: &access.Target, RepositoryID: frozen[0].RepositoryID, MemberID: frozen[0].MemberID, MemberVersion: 1, Path: "../same.txt"}} {
		if _, e := facade.UpperReviewRead(ctx, in); e == nil {
			t.Fatalf("unauthorized read %+v", in)
		}
	}
	implementation := workset.CloneTargets(frozen)
	checks := []task.VerificationCheck{}
	for i := range implementation {
		implementation[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: implementation[i].RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{implementation[i].RepositoryID}})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Update app sdk contract", Constraints: []string{"Preserve public API"}, CodexInstruction: "Update exact files", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "Both repositories", Checks: checks, AcceptanceCriteria: []string{"Both checks pass"}, CodexInstruction: "Run both checks"}
	submitted := review.SubmitInput{UpperTarget: access.Target, RequestID: "report", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, Summary: "Both repository contracts reviewed", Disposition: review.DispositionApprovalReady, Coverage: []string{"app/same.txt", "sdk/same.txt"}, Findings: []review.Finding{}, Limitations: []string{"Source inspection only"}, RecommendedNextSteps: []string{"Approve without starting"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}
	receipt, err := facade.SubmitReviewResult(ctx, submitted)
	if err != nil {
		t.Fatal(err)
	}
	again, err := facade.SubmitReviewResult(ctx, submitted)
	if err != nil || again.ReportID != receipt.ReportID {
		t.Fatalf("result replay=%+v %v", again, err)
	}
	approved, approveInput := commit(c.PrepareInput{RequestID: "approve", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, ctx, facade, taskID, areaID, reviewID, "APPROVE"), Variants: []c.Variant{{ID: "approve", Summary: "Approve app and sdk without execution", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: implementation, PrimaryMemberID: implementation[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}}, true)
	replay, err = facade.CommitConfirmation(ctx, approveInput)
	if err != nil || replay.Steps[0].RevisionID != approved.Steps[0].RevisionID {
		t.Fatalf("approval replay=%+v %v", replay, err)
	}
	publicRevision, e := facade.TaskDefinitionRevisionForSession(ctx, sid, approved.Steps[0].RevisionID)
	if e != nil {
		t.Fatal(e)
	}
	if publicRevision.Definition.Version != 2 || !reflect.DeepEqual(publicRevision.Definition.RepositoryTargets, implementation) || !reflect.DeepEqual(publicRevision.Definition.VerificationSpec, verification) {
		t.Fatalf("public revision mismatch %+v", publicRevision)
	}
	var raw, hash string
	if err = plane.DB.SQL().QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=?`, approved.Steps[0].RevisionID).Scan(&raw, &hash); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(raw))
	if hash != hex.EncodeToString(sum[:]) || publicRevision.PayloadSHA256 != hash {
		t.Fatal("revision hash mismatch")
	}
	for table, want := range map[string]int{"tasks": 1, "reviews": 1, "runs": 1, "worktrees": 2, "run_repository_bindings": 2, "task_definition_revisions": 2, "execution_proposals": 0, "runtime_handoffs": 0, "feature_area_agent_claims": 0} {
		var count int
		if e := plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); e != nil || count != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, count, want, e)
		}
	}
	mutatedReport := submitted
	mutatedReport.Report.Summary = "changed"
	if _, e := facade.SubmitReviewResult(ctx, mutatedReport); e == nil {
		t.Fatal("changed result replay accepted")
	}
	changedApproval := approveInput
	changedApproval.VariantID = "changed"
	if _, e := facade.CommitConfirmation(ctx, changedApproval); e == nil {
		t.Fatal("changed approval replay accepted")
	}
	changedAdmission := startInput
	changedAdmission.VariantID = "changed"
	if _, e := facade.CommitConfirmation(ctx, changedAdmission); e == nil {
		t.Fatal("changed admission replay accepted")
	}
	expectedStart := wp.TaskExecutionExpectation{FeatureAreaID: areaID, Phase: task.PhaseImplementationReady, TaskDefinitionRevisionID: approved.Steps[0].RevisionID}
	if _, e := facade.StartTaskExecutionExpectedCommand(ctx, sid, taskID, "START_EXECUTION", "blocked-v2-start", expectedStart); e == nil {
		t.Fatal("v2 multi-repository definition entered the single-root Coding runtime")
	}
	if _, e := facade.DirectFileReconcileTaskAuthorization(ctx, directfile.Registration{AISessionID: sid, TaskID: taskID, TaskDefinitionRevisionID: approved.Steps[0].RevisionID, DecisionID: wp.DecisionID(reviewID), ImplementationMode: string(task.ImplementationDirectFile), Confirmed: true}); e == nil {
		t.Fatal("v2 multi-repository definition acquired a Direct File write workflow")
	}
	if _, e := facade.RetryAcceptedTaskCommit(ctx, sid, taskID); e == nil {
		t.Fatal("v2 approved definition entered legacy single-repository Task delivery")
	}
	if _, e := facade.CommitFeatureArea(ctx, sid, areaID, "blocked-v2-area-delivery"); e == nil {
		t.Fatal("v2 approved definition entered legacy single-repository FeatureArea delivery")
	}
	for table, want := range map[string]int{"execution_proposals": 0, "runtime_handoffs": 0, "task_commit_receipts": 0, "feature_area_commit_receipts": 0} {
		var count int
		if e := plane.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); e != nil || count != want {
			t.Fatalf("blocked v2 path changed %s count=%d want=%d err=%v", table, count, want, e)
		}
	}
	if err = plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(ctx, controlplane.Config{DataRoot: filepath.Join(root, "data")})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedFacade, err := controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatal(err)
	}
	coldRead, err := reopenedFacade.ReadConfirmation(ctx, c.ReadInput{AISessionID: sid, IntentID: approved.IntentID, Version: approved.Version, Purpose: approved.Purpose, ContentHash: approved.ContentHash})
	if err != nil || coldRead.State != "COMPLETED" || !reflect.DeepEqual(coldRead.Steps, approved.Steps) {
		t.Fatalf("cold approval receipt recovery=%+v err=%v", coldRead, err)
	}
	coldReplay, err := reopenedFacade.CommitConfirmation(ctx, approveInput)
	if err != nil || coldReplay.State != "COMPLETED" || !reflect.DeepEqual(coldReplay.Steps, approved.Steps) {
		t.Fatalf("cold approval receipt replay=%+v err=%v", coldReplay, err)
	}
	reopenedTask, err := reopened.Tasks.Get(ctx, taskID)
	if err != nil || reopenedTask.Phase != task.PhaseImplementationReady || reopenedTask.CurrentDefinitionRevisionID != approved.Steps[0].RevisionID || reopenedTask.RepositoryID != "" || reopenedTask.BaseCommit != "" || !reflect.DeepEqual(reopenedTask.RepositoryTargets, implementation) {
		t.Fatalf("reopened task=%+v err=%v", reopenedTask, err)
	}
	reopenedRevision, err := reopenedFacade.TaskDefinitionRevisionForSession(ctx, sid, approved.Steps[0].RevisionID)
	if err != nil || reopenedRevision.PayloadSHA256 != hash || reopenedRevision.Definition.Version != 2 || !reflect.DeepEqual(reopenedRevision.Definition.RepositoryTargets, implementation) || !reflect.DeepEqual(reopenedRevision.Definition.WorkSpec, work) || !reflect.DeepEqual(reopenedRevision.Definition.VerificationSpec, verification) {
		t.Fatalf("reopened revision=%+v err=%v", reopenedRevision, err)
	}
	var reopenedRaw, reopenedHash string
	if err = reopened.DB.SQL().QueryRow(`SELECT payload_json,payload_sha256 FROM task_definition_revisions WHERE id=?`, approved.Steps[0].RevisionID).Scan(&reopenedRaw, &reopenedHash); err != nil || reopenedRaw != raw || reopenedHash != hash {
		t.Fatalf("reopened immutable revision raw/hash changed: hash=%q err=%v", reopenedHash, err)
	}
	for table, want := range map[string]int{"tasks": 1, "reviews": 1, "runs": 1, "worktrees": 2, "run_repository_bindings": 2, "task_definition_revisions": 2, "execution_proposals": 0, "runtime_handoffs": 0, "feature_area_agent_claims": 0, "task_commit_receipts": 0, "feature_area_commit_receipts": 0} {
		var count int
		if e := reopened.DB.SQL().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); e != nil || count != want {
			t.Fatalf("reopened %s count=%d want=%d err=%v", table, count, want, e)
		}
	}
	bindings, err := reopened.Runs.ListRepositoryBindings(ctx, runID)
	if err != nil || len(bindings) != len(implementation) {
		t.Fatalf("reopened bindings=%+v err=%v", bindings, err)
	}
	for _, binding := range bindings {
		matched := false
		for _, target := range implementation {
			if target.RepositoryID == binding.RepositoryID && target.MemberID == binding.MemberID && target.MemberVersion == binding.MemberVersion && target.OriginCommit == binding.OriginCommit {
				matched = true
			}
		}
		if !matched || binding.RunID != runID || binding.ReviewID != reviewID || binding.WorkspaceID != access.WorkspaceID || binding.WorktreeID == "" || binding.Preparation != "READY" {
			t.Fatalf("reopened exact binding mismatch: %+v", binding)
		}
		t.Logf("persisted_binding=%+v", binding)
	}
	t.Logf("task=%s review=%s run=%s workspace=%s revision=%s hash=%s targets=%+v", taskID, reviewID, runID, access.WorkspaceID, approved.Steps[0].RevisionID, hash, implementation)
}
