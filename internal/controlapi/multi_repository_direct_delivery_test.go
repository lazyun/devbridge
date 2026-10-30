package controlapi_test

import (
	"errors"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func approveDirectFixture(t *testing.T, readonly, waive bool) (*upperLifecycleFixture, task.Task) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	checks := []task.VerificationCheck{}
	for i, target := range targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got.Digest})
		if !readonly || target.RepositoryID == f.repos["app"] {
			targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt", "new.txt"}}
		}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "Update exact file members", Constraints: []string{"preserve unrelated bytes"}, CodexInstruction: "Update same.txt in writable members", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both members", Checks: checks, AcceptanceCriteria: []string{"both pass"}, CodexInstruction: "Run both checks"}
	primary := ""
	if !waive {
		primary = targets[0].MemberID
	}
	_, err := f.facade.SubmitReviewResult(f.ctx, review.SubmitInput{UpperTarget: f.access.Target, RequestID: "direct-proposal", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: primary, Summary: "Direct File exact members", Disposition: review.DispositionApprovalReady, Coverage: []string{"both members"}, Findings: []review.Finding{}, Limitations: []string{"source only"}, RecommendedNextSteps: []string{"explicit approval"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-direct-approval", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "direct", Summary: "Approve exact Direct File proposal without starting", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: targets, PrimaryMemberID: primary, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	selection := c.Selection{}
	if waive {
		selection.VerificationWaiver = &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Test user explicitly waives command verification for this isolated file-only fixture"}
	}
	confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-direct-approval", ContentHash: prepared.ContentHash, VariantID: "direct", Selection: selection})
	if err != nil {
		t.Fatalf("approval: %+v %v", confirmed, err)
	}
	if confirmed.State != "COMPLETED" {
		t.Fatalf("approval %+v", confirmed)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"SELECT count(*) FROM direct_file_task_authorizations", "SELECT count(*) FROM execution_proposals", "SELECT count(*) FROM task_commit_receipts"} {
		var n int
		if err := f.plane.DB.SQL().QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("NO_START effect: %s=%d %v", query, n, err)
		}
	}
	return f, current
}
func directConfirmation(t *testing.T, f *upperLifecycleFixture, current task.Task, action string, state directfile.State) (c.Intent, error) {
	t.Helper()
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-" + action, Purpose: c.Execution, Action: action, Execution: &c.ExecutionBody{Action: action, TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Summary: "Explicit " + action, ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		return prepared, err
	}
	return f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-" + action, ContentHash: prepared.ContentHash, VariantID: "exact"})
}
func directInput(f *upperLifecycleFixture, s directfile.State, target workset.Target) directfile.Input {
	return directfile.Input{Scope: directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: s.WorkflowID}, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, RuntimeEpoch: s.RuntimeEpoch, GrantGeneration: s.GrantGeneration, Path: "same.txt"}
}
func TestMultiRepositoryDirectG1G4G5G6ProductionClosure(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	// The unrelated third repository is unavailable throughout file implementation.
	backend, err := f.plane.Repositories.Get(f.ctx, f.repos["backend"])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(backend.Path, backend.Path+"-offline"); err != nil {
		t.Fatal(err)
	}
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("start cause %T: %v", cause, cause)
		}
		t.Fatalf("start %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" || len(state.RepositoryTargets) != 2 {
		t.Fatalf("state %+v %v", state, err)
	}
	for _, target := range current.RepositoryTargets {
		in := directInput(f, state, target)
		read, err := f.facade.FileRead(f.ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		in.RequestID = "replace-" + target.RepositoryID
		in.ExpectedDigest = read.Digest
		in.Content = "changed " + target.RepositoryID + "\n"
		result, err := f.facade.FileReplace(f.ctx, in)
		if err != nil {
			t.Fatalf("replace: %+v %v", result, err)
		}
		replay, err := f.facade.FileReplace(f.ctx, in)
		if err != nil || replay.Digest != result.Digest {
			t.Fatalf("mutation replay: %+v %v", replay, err)
		}
		conflict := in
		conflict.Content = "other"
		if _, err = f.facade.FileReplace(f.ctx, conflict); err == nil {
			t.Fatal("changed payload replay accepted")
		}
	}
	input := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "submit-files", Summary: "Both exact members updated", VerificationNotes: "Explicit fixture waiver; no commands claimed", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
	if err != nil || len(result.Members) != 2 {
		t.Fatalf("result %+v %v", result, err)
	}
	replay, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
	if err != nil || replay.ID != result.ID {
		t.Fatalf("result replay %+v %v", replay, err)
	}
	input.Summary = "changed payload"
	if _, err = f.facade.SubmitFileImplementationResult(f.ctx, input); err == nil {
		t.Fatal("result payload conflict accepted")
	}
	late := directInput(f, state, current.RepositoryTargets[0])
	late.Path = "new.txt"
	late.RequestID = "late"
	late.Content = "late"
	if _, err = f.facade.FileCreate(f.ctx, late); err == nil {
		t.Fatal("late write accepted")
	}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "accept-files", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil || !accepted.Accepted || accepted.CommitError != "" {
		t.Fatalf("accept %+v %v", accepted, err)
	}
	var complete int
	if err = f.plane.DB.SQL().QueryRow("SELECT count(*) FROM task_commit_members WHERE task_id=? AND state='COMPLETED'", f.task).Scan(&complete); err != nil || complete != 2 {
		t.Fatalf("commit members %d %v", complete, err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-merge", Purpose: c.FeatureArea, Action: "MERGE_FEATURE_AREA", FeatureArea: &c.AreaBody{FeatureAreaID: f.area}, Variants: []c.Variant{{ID: "merge", Summary: "Merge both exact Task commits into their selected development branches"}}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Content.FeatureArea.DeliveryPlan == nil || len(prepared.Content.FeatureArea.DeliveryPlan.Members) != 2 {
		t.Fatalf("plan %+v", prepared)
	}
	merged, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-merge", ContentHash: prepared.ContentHash, VariantID: "merge"})
	if err != nil || merged.State != "COMPLETED" {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("merge cause %T: %v", cause, cause)
		}
		t.Fatalf("merge %+v %v", merged, err)
	}
	for _, name := range []string{"app", "sdk"} {
		repo, err := f.plane.Repositories.Get(f.ctx, f.repos[name])
		if err != nil {
			t.Fatal(err)
		}
		content, err := os.ReadFile(filepath.Join(repo.Path, "same.txt"))
		if err != nil || !strings.Contains(string(content), "changed "+repo.ID) {
			t.Fatalf("merged %s %s %v", name, content, err)
		}
	}
	var fk string
	rows, err := f.plane.DB.SQL().Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatalf("foreign key failure %s", fk)
	}
}
func TestMultiRepositoryDirectG4RunBlockedBeforeStart(t *testing.T) {
	f, current := approveDirectFixture(t, false, false)
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err == nil && started.State == "COMPLETED" {
		t.Fatal("RUN started")
	}
	var n int
	if e := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM direct_file_task_authorizations").Scan(&n); e != nil || n != 0 {
		t.Fatalf("blocked start reserved grant %d %v", n, e)
	}
}
