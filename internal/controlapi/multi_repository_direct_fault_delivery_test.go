package controlapi_test

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

func exerciseDirectFileResult(t *testing.T, f *upperLifecycleFixture, current task.Task) (directfile.State, directfile.ImplementationResult) {
	t.Helper()
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: intent=%+v err=%v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("active state=%+v err=%v", state, err)
	}
	for _, target := range current.RepositoryTargets {
		in := directInput(f, state, target)
		before, readErr := f.facade.FileRead(f.ctx, in)
		if readErr != nil {
			t.Fatal(readErr)
		}
		in.RequestID = "fault-replace-" + target.RepositoryID
		in.ExpectedDigest = before.Digest
		in.Content = "fault delivery " + target.RepositoryID + "\n"
		if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
			t.Fatalf("replace %s: %v", target.RepositoryID, err)
		}
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{
		AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID,
		WorkflowID: state.WorkflowID, RequestID: "fault-submit-files", Summary: "Exact members changed",
		VerificationNotes: "Explicit fixture waiver; no command verification claimed", ProtocolVersion: 2,
		RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration,
	})
	if err != nil || len(result.Members) != 2 {
		t.Fatalf("submit result=%+v err=%v", result, err)
	}
	return state, result
}

func reopenDirectDeliveryFixture(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
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
}

func gitOutput(t *testing.T, path string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func logDeliveryErrorChain(t *testing.T, err error) {
	t.Helper()
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		t.Logf("delivery failure cause %T: %v", cause, cause)
	}
}

func createDirectAreaOnFixture(t *testing.T, source *upperLifecycleFixture, tag string) *upperLifecycleFixture {
	t.Helper()
	f := *source
	commit := func(in c.PrepareInput) c.Intent {
		t.Helper()
		in.AISessionID = f.session
		prepared, err := f.facade.PrepareConfirmation(f.ctx, in)
		if err != nil {
			t.Fatalf("prepare %s: %v", in.Action, err)
		}
		got, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: tag + "-commit-" + in.RequestID, ContentHash: prepared.ContentHash, VariantID: in.Variants[0].ID})
		if err != nil || got.State != "COMPLETED" {
			t.Fatalf("commit %s: intent=%+v err=%v", in.Action, got, err)
		}
		return got
	}
	area := commit(c.PrepareInput{RequestID: tag + "-area", Purpose: c.FeatureArea, Action: "CREATE_AREA", FeatureArea: &c.AreaBody{Title: "Competing delivery " + tag, ProblemStatement: "Change both exact repositories"}, Variants: []c.Variant{{ID: "create", Summary: "create area"}}})
	f.area = area.Steps[0].FeatureAreaID
	created := commit(c.PrepareInput{RequestID: tag + "-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Competing task " + tag, ProblemStatement: "Change exact repository files", Constraints: []string{"preserve unrelated bytes"}}, Variants: []c.Variant{{ID: "create", Summary: "create task"}}})
	f.task = created.Steps[0].TaskID
	targets := []workset.Target{}
	for _, name := range []string{"app", "sdk"} {
		targets = append(targets, workset.Target{RepositoryID: f.repos[name], OriginCommit: f.heads[name], OriginRef: "refs/heads/" + name + "-development", Read: true, Write: workset.WriteScope{Kind: "NONE"}})
	}
	started := commit(c.PrepareInput{RequestID: tag + "-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "review exact members", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Read same.txt from both repositories"}}})
	f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	var err error
	f.access, err = f.facade.UpperReviewAccessForSession(f.ctx, f.session, f.review)
	if err != nil || f.access.State != "ACTIVE" {
		t.Fatalf("second review access=%+v err=%v", f.access, err)
	}
	value, err := f.plane.Reviews.Get(f.ctx, f.review)
	if err != nil || len(value.RepositoryTargets) != 2 {
		t.Fatalf("second frozen targets=%+v err=%v", value.RepositoryTargets, err)
	}
	f.targets = workset.CloneTargets(value.RepositoryTargets)
	return &f
}

func approveDirectOnFixture(t *testing.T, f *upperLifecycleFixture, tag string) task.Task {
	t.Helper()
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	checks := []task.VerificationCheck{}
	for i, target := range targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got.Digest})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "Update both exact members", Constraints: []string{"preserve unrelated bytes"}, CodexInstruction: "Update same.txt", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both members", Checks: checks, AcceptanceCriteria: []string{"both pass"}, CodexInstruction: "Run both checks"}
	if _, err := f.facade.SubmitReviewResult(f.ctx, review.SubmitInput{UpperTarget: f.access.Target, RequestID: tag + "-proposal", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, Summary: "Direct File competing delivery", Disposition: review.DispositionApprovalReady, Coverage: []string{"both members"}, Findings: []review.Finding{}, Limitations: []string{"source only"}, RecommendedNextSteps: []string{"explicit approval"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}}); err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: tag + "-prepare-approval", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "direct", Summary: "Approve without start", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: targets, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: tag + "-confirm-approval", ContentHash: prepared.ContentHash, VariantID: "direct", Selection: c.Selection{VerificationWaiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "User explicitly waives command verification for competing delivery fixture"}}})
	if err != nil || confirmed.State != "COMPLETED" {
		t.Fatalf("approval intent=%+v err=%v", confirmed, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func directConfirmationTagged(t *testing.T, f *upperLifecycleFixture, current task.Task, action, tag string, state directfile.State) (c.Intent, error) {
	t.Helper()
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: tag + "-prepare-" + action, Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Summary: "Explicit " + action, ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: current.RepositoryTargets, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		return prepared, err
	}
	return f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: tag + "-confirm-" + action, ContentHash: prepared.ContentHash, VariantID: "exact"})
}

func acceptCompetingDirectTask(t *testing.T, f *upperLifecycleFixture, current task.Task, tag string) {
	t.Helper()
	started, err := directConfirmationTagged(t, f, current, "START_DIRECT_FILE", tag, directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start %s intent=%+v err=%v", tag, started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state %s=%+v err=%v", tag, state, err)
	}
	for _, target := range current.RepositoryTargets {
		in := directInput(f, state, target)
		before, readErr := f.facade.FileRead(f.ctx, in)
		if readErr != nil {
			t.Fatal(readErr)
		}
		in.RequestID, in.ExpectedDigest, in.Content = tag+"-replace-"+target.RepositoryID, before.Digest, tag+" "+target.RepositoryID+"\n"
		if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: tag + "-submit", Summary: "Both exact members changed", VerificationNotes: "Explicit user waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: tag + "-accept", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept %s result=%+v err=%v", tag, accepted, err)
	}
}

func TestMultiRepositoryDirectG5TaskCommitEffectBeforeReceiptRestartRecovery(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)

	effects := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
		if kind == "TASK_COMMIT_AFTER_GIT" {
			effects++
			if effects == 2 {
				return errors.New("injected task commit response loss")
			}
		}
		return nil
	})
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{
		AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID,
		WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "fault-accept-files", ProtocolVersion: 2,
		RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration,
	})
	if err != nil || !accepted.Accepted || !strings.Contains(accepted.CommitError, "injected task commit response loss") {
		logDeliveryErrorChain(t, err)
		t.Fatalf("accept result=%+v err=%v", accepted, err)
	}

	type member struct{ repositoryID, path, expectedHead, state, resultingCommit string }
	rows, err := f.plane.DB.SQL().Query(`SELECT repository_id,workspace_path,expected_head,state,resulting_commit FROM task_commit_members WHERE task_id=? ORDER BY repository_id`, f.task)
	if err != nil {
		t.Fatal(err)
	}
	var before []member
	for rows.Next() {
		var value member
		if err = rows.Scan(&value.repositoryID, &value.path, &value.expectedHead, &value.state, &value.resultingCommit); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		before = append(before, value)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(before) != 2 || before[0].state != "COMPLETED" || before[1].state != "PENDING" {
		t.Fatalf("partial task receipts: %+v", before)
	}
	preRecoveryHeads := map[string]string{}
	for _, value := range before {
		preRecoveryHeads[value.repositoryID] = gitOutput(t, value.path, "rev-parse", "HEAD")
		if got := gitOutput(t, value.path, "rev-list", "--count", value.expectedHead+"..HEAD"); got != "1" {
			t.Fatalf("repository %s effect count before recovery=%s", value.repositoryID, got)
		}
	}

	reopenDirectDeliveryFixture(t, f)
	if _, err = f.facade.RetryAcceptedTaskCommit(f.ctx, f.session, f.task); err != nil {
		t.Fatalf("restart task recovery: %v", err)
	}
	for _, value := range before {
		head := gitOutput(t, value.path, "rev-parse", "HEAD")
		if head != preRecoveryHeads[value.repositoryID] {
			t.Fatalf("repository %s was committed twice: before=%s after=%s", value.repositoryID, preRecoveryHeads[value.repositoryID], head)
		}
		if got := gitOutput(t, value.path, "rev-list", "--count", value.expectedHead+"..HEAD"); got != "1" {
			t.Fatalf("repository %s recovered effect count=%s", value.repositoryID, got)
		}
	}
	var completed int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM task_commit_members WHERE task_id=? AND state='COMPLETED'`, f.task).Scan(&completed); err != nil || completed != 2 {
		t.Fatalf("completed task members=%d err=%v", completed, err)
	}
	var parentDone bool
	if err = f.plane.DB.SQL().QueryRow(`SELECT completed_at IS NOT NULL FROM task_commit_receipts WHERE task_id=? AND protocol_version=2`, f.task).Scan(&parentDone); err != nil || !parentDone {
		t.Fatalf("task parent complete=%v err=%v", parentDone, err)
	}
}

func TestMultiRepositoryDirectG6AreaMergeEffectBeforeReceiptUsesFrozenPlan(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{
		AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID,
		WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "area-accept-files", ProtocolVersion: 2,
		RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration,
	})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept result=%+v err=%v", accepted, err)
	}

	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "prepare-fault-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA",
		FeatureArea: &c.AreaBody{FeatureAreaID: f.area},
		Variants:    []c.Variant{{ID: "merge", Summary: "Merge the frozen per-repository task commits"}},
	})
	if err != nil || prepared.Content.FeatureArea == nil || prepared.Content.FeatureArea.DeliveryPlan == nil || len(prepared.Content.FeatureArea.DeliveryPlan.Members) != 2 {
		t.Fatalf("prepare merge=%+v err=%v", prepared, err)
	}
	plan := *prepared.Content.FeatureArea.DeliveryPlan
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-fault-merge", ContentHash: prepared.ContentHash, VariantID: "merge"}

	effects := 0
	f.plane.WorkflowProgression.SetDeliveryFaultHookForTest(func(kind, _ string) error {
		if kind == "AREA_MERGE_AFTER_GIT" {
			effects++
			if effects == 2 {
				return errors.New("injected area merge response loss")
			}
		}
		return nil
	})
	partial, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err == nil || partial.State != "UNKNOWN" {
		logDeliveryErrorChain(t, err)
		t.Fatalf("partial merge intent=%+v err=%v", partial, err)
	}

	type mergeMember struct{ repositoryID, targetPath, expectedHead, state, resultingHead string }
	rows, err := f.plane.DB.SQL().Query(`SELECT repository_id,target_path,expected_dev_head,state,resulting_dev_head FROM feature_area_commit_members WHERE request_id=? ORDER BY repository_id`, partial.Steps[0].RequestID)
	if err != nil {
		t.Fatal(err)
	}
	var members []mergeMember
	for rows.Next() {
		var value mergeMember
		if err = rows.Scan(&value.repositoryID, &value.targetPath, &value.expectedHead, &value.state, &value.resultingHead); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		members = append(members, value)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].state != "COMPLETED" || members[1].state != "PENDING" {
		t.Fatalf("partial merge receipts: %+v", members)
	}
	preRecoveryHeads := map[string]string{}
	for _, value := range members {
		preRecoveryHeads[value.repositoryID] = gitOutput(t, value.targetPath, "rev-parse", "HEAD")
		if preRecoveryHeads[value.repositoryID] == value.expectedHead {
			t.Fatalf("repository %s Git effect did not precede receipt fault", value.repositoryID)
		}
	}

	reopenDirectDeliveryFixture(t, f)
	// A pending merge fences development reselection. Recovery must retain the
	// target path/ref and cycle frozen in the original confirmation.
	changed := plan.Members[1]
	repository, err := f.plane.Repositories.Get(f.ctx, changed.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	newRef := "refs/heads/reselected-after-partial"
	gitOutput(t, repository.Path, "branch", strings.TrimPrefix(newRef, "refs/heads/"), changed.ExpectedDevHead)
	if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{
		AISessionID: f.session, ProjectID: f.project, RepositoryID: changed.RepositoryID,
		DevelopmentRef: newRef, RequestID: "reselect-after-partial",
	}); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
		t.Fatalf("pending merge did not fence development mapping: %v", err)
	}

	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: commit.RequestID})
	if err != nil || !read.CanResume || read.State != "UNKNOWN" || read.Content.FeatureArea.DeliveryPlan.SHA256 != plan.SHA256 {
		t.Fatalf("restart read intent=%+v err=%v", read, err)
	}
	recovered, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || recovered.State != "COMPLETED" {
		logDeliveryErrorChain(t, err)
		t.Fatalf("recover frozen merge intent=%+v err=%v", recovered, err)
	}
	for _, value := range members {
		head := gitOutput(t, value.targetPath, "rev-parse", "HEAD")
		if head != preRecoveryHeads[value.repositoryID] {
			t.Fatalf("repository %s merge was replayed: before=%s after=%s", value.repositoryID, preRecoveryHeads[value.repositoryID], head)
		}
	}
	var completed int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_members WHERE request_id=? AND state='COMPLETED'`, partial.Steps[0].RequestID).Scan(&completed); err != nil || completed != 2 {
		t.Fatalf("completed merge members=%d err=%v", completed, err)
	}
	var storedPlan string
	if err = f.plane.DB.SQL().QueryRow(`SELECT plan_sha256 FROM feature_area_commit_receipts WHERE request_id=? AND completed_at IS NOT NULL`, partial.Steps[0].RequestID).Scan(&storedPlan); err != nil || storedPlan != plan.SHA256 {
		t.Fatalf("completed frozen plan=%q want=%q err=%v", storedPlan, plan.SHA256, err)
	}
}

func TestMultiRepositoryDirectG6PartialDirtyTargetRecoversOriginalMapping(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	state, result := exerciseDirectFileResult(t, f, current)
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{
		AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID,
		WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "dirty-accept-files", ProtocolVersion: 2,
		RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration,
	})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept result=%+v err=%v", accepted, err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "prepare-dirty-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA",
		FeatureArea: &c.AreaBody{FeatureAreaID: f.area},
		Variants:    []c.Variant{{ID: "merge", Summary: "Merge the frozen per-repository task commits"}},
	})
	if err != nil || prepared.Content.FeatureArea == nil || prepared.Content.FeatureArea.DeliveryPlan == nil || len(prepared.Content.FeatureArea.DeliveryPlan.Members) != 2 {
		t.Fatalf("prepare merge=%+v err=%v", prepared, err)
	}
	plan := *prepared.Content.FeatureArea.DeliveryPlan
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-dirty-merge", ContentHash: prepared.ContentHash, VariantID: "merge"}
	dirtyPath := plan.Members[1].TargetPath + "/devbridge-owned-dirty-fixture.txt"
	if err = os.WriteFile(dirtyPath, []byte("owned fault fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	partial, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err == nil || partial.State != "UNKNOWN" {
		logDeliveryErrorChain(t, err)
		t.Fatalf("dirty partial merge intent=%+v err=%v", partial, err)
	}
	var firstState, secondState string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, partial.Steps[0].RequestID, plan.Members[0].RepositoryID).Scan(&firstState); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, partial.Steps[0].RequestID, plan.Members[1].RepositoryID).Scan(&secondState); err != nil {
		t.Fatal(err)
	}
	if firstState != "COMPLETED" || secondState != "UNKNOWN" {
		t.Fatalf("dirty partial states first=%s second=%s", firstState, secondState)
	}
	firstHead := gitOutput(t, plan.Members[0].TargetPath, "rev-parse", "HEAD")
	if secondHead := gitOutput(t, plan.Members[1].TargetPath, "rev-parse", "HEAD"); secondHead != plan.Members[1].ExpectedDevHead {
		t.Fatalf("dirty target mutated: got=%s want=%s", secondHead, plan.Members[1].ExpectedDevHead)
	}

	reopenDirectDeliveryFixture(t, f)
	changed := plan.Members[1]
	repository, err := f.plane.Repositories.Get(f.ctx, changed.RepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	newRef := "refs/heads/reselected-after-dirty-partial"
	gitOutput(t, repository.Path, "branch", strings.TrimPrefix(newRef, "refs/heads/"), changed.ExpectedDevHead)
	if _, err = f.facade.SelectDevelopmentBranchForSession(f.ctx, developmentcycle.SelectInput{
		AISessionID: f.session, ProjectID: f.project, RepositoryID: changed.RepositoryID,
		DevelopmentRef: newRef, RequestID: "reselect-after-dirty-partial",
	}); !errors.Is(err, developmentcycle.ErrFinalizationPending) {
		t.Fatalf("pending merge did not fence development mapping: %v", err)
	}
	if err = os.Remove(dirtyPath); err != nil {
		t.Fatalf("remove owned fault fixture: %v", err)
	}
	read, err := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash, RequestID: commit.RequestID})
	if err != nil || !read.CanResume || read.Content.FeatureArea.DeliveryPlan.SHA256 != plan.SHA256 {
		t.Fatalf("read dirty recovery intent=%+v err=%v", read, err)
	}
	recovered, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err == nil || recovered.State == "COMPLETED" {
		t.Fatal("old rejected merge was silently redispatched")
	}
	candidates, err := f.facade.FeatureAreaMergeRetryCandidates(f.ctx, f.session, f.area)
	if err != nil || len(candidates) != 1 || candidates[0].RepositoryID != changed.RepositoryID {
		t.Fatalf("missing exact rejected member: %+v %v", candidates, err)
	}
	if _, err = f.facade.ReprepareFeatureAreaMerge(f.ctx, f.session, delivery.MergeRetryInput{FeatureAreaID: f.area, MergeRequestID: partial.Steps[0].RequestID, RepositoryID: changed.RepositoryID, PredecessorOperationID: candidates[0].OperationID, RequestID: "dirty-target-explicit-retry"}); err != nil {
		t.Fatalf("explicit retry of original frozen target: %v", err)
	}

	if got := gitOutput(t, plan.Members[0].TargetPath, "rev-parse", "HEAD"); got != firstHead {
		t.Fatalf("first repository merge replayed: before=%s after=%s", firstHead, got)
	}
	if got := gitOutput(t, plan.Members[1].TargetPath, "rev-parse", "HEAD"); got == plan.Members[1].ExpectedDevHead {
		t.Fatalf("original frozen target was not merged: %s", got)
	}
}

func TestMultiRepositoryDirectG6CompetingFeatureAreasFenceFrozenTargetHeads(t *testing.T) {
	first, firstTask := approveDirectFixture(t, false, true)
	second := createDirectAreaOnFixture(t, first, "competitor")
	secondTask := approveDirectOnFixture(t, second, "competitor")
	acceptCompetingDirectTask(t, first, firstTask, "first-area")
	acceptCompetingDirectTask(t, second, secondTask, "second-area")

	preparePlan := func(f *upperLifecycleFixture, tag string) (c.Intent, c.CommitInput) {
		t.Helper()
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: tag + "-prepare-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge frozen competing plan"}}})
		if err != nil || prepared.Content.FeatureArea == nil || prepared.Content.FeatureArea.DeliveryPlan == nil || len(prepared.Content.FeatureArea.DeliveryPlan.Members) != 2 {
			t.Fatalf("prepare %s intent=%+v err=%v", tag, prepared, err)
		}
		return prepared, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: tag + "-confirm-merge", ContentHash: prepared.ContentHash, VariantID: "merge"}
	}
	firstPrepared, firstCommit := preparePlan(first, "first-area")
	secondPrepared, secondCommit := preparePlan(second, "second-area")
	firstPlan, secondPlan := *firstPrepared.Content.FeatureArea.DeliveryPlan, *secondPrepared.Content.FeatureArea.DeliveryPlan
	for i := range firstPlan.Members {
		if firstPlan.Members[i].RepositoryID != secondPlan.Members[i].RepositoryID || firstPlan.Members[i].ExpectedDevHead != secondPlan.Members[i].ExpectedDevHead || firstPlan.Members[i].DevelopmentRef != secondPlan.Members[i].DevelopmentRef {
			t.Fatalf("plans did not compete for the same frozen targets: first=%+v second=%+v", firstPlan.Members, secondPlan.Members)
		}
	}

	firstMerged, err := first.facade.CommitConfirmation(first.ctx, firstCommit)
	if err != nil || firstMerged.State != "COMPLETED" {
		logDeliveryErrorChain(t, err)
		t.Fatalf("first merge intent=%+v err=%v", firstMerged, err)
	}
	winningHeads := map[string]string{}
	for _, member := range firstPlan.Members {
		winningHeads[member.RepositoryID] = gitOutput(t, member.TargetPath, "rev-parse", "HEAD")
	}

	loser, err := second.facade.CommitConfirmation(second.ctx, secondCommit)
	if err == nil || loser.State != "PENDING" {
		logDeliveryErrorChain(t, err)
		t.Fatalf("competing stale plan was not fenced: intent=%+v err=%v", loser, err)
	}
	for _, member := range secondPlan.Members {
		if got := gitOutput(t, member.TargetPath, "rev-parse", "HEAD"); got != winningHeads[member.RepositoryID] {
			t.Fatalf("losing plan overwrote repository %s: winner=%s got=%s", member.RepositoryID, winningHeads[member.RepositoryID], got)
		}
	}
	var loserReceipts int
	if err = second.plane.DB.SQL().QueryRow(`SELECT count(*) FROM feature_area_commit_receipts WHERE feature_area_id=?`, second.area).Scan(&loserReceipts); err != nil || loserReceipts != 0 {
		t.Fatalf("losing plan fake completion count=%d err=%v", loserReceipts, err)
	}

	// Exact replay of the consumed confirmation cannot refresh the stale target
	// heads or substitute the winning FeatureArea's source commits.
	replay, replayErr := second.facade.CommitConfirmation(second.ctx, secondCommit)
	if replayErr == nil && replay.State == "COMPLETED" {
		t.Fatalf("stale competing plan completed on replay: %+v", replay)
	}
	if replay.Content.FeatureArea == nil || replay.Content.FeatureArea.DeliveryPlan == nil || replay.Content.FeatureArea.DeliveryPlan.SHA256 != secondPlan.SHA256 {
		t.Fatalf("losing plan was substituted on replay: %+v", replay.Content.FeatureArea)
	}
	for _, member := range secondPlan.Members {
		if got := gitOutput(t, member.TargetPath, "rev-parse", "HEAD"); got != winningHeads[member.RepositoryID] {
			t.Fatalf("stale replay changed repository %s: winner=%s got=%s", member.RepositoryID, winningHeads[member.RepositoryID], got)
		}
	}
}
