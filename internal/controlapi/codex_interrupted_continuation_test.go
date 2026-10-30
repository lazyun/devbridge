package controlapi_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Real Core/SQLite and separate protocol processes exercise interruption,
// preparation, explicit confirmation, dispatch and durable replay together.
func TestInterruptedCodexContinuationPreservesTaskWorkAndFrozenAuthority(t *testing.T) {
	t.Setenv("DEVBRIDGE_CODING_V1_INTERRUPT_ONCE", filepath.Join(t.TempDir(), "interrupted"))
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, oldExecution string) bool {
		var old run.Run
		deadline := time.Now().Add(15 * time.Second)
		for {
			proposal, err := f.facade.GetExecutionProposal(f.ctx, f.session, oldExecution)
			if err != nil {
				t.Fatal(err)
			}
			old, err = f.plane.Runs.Get(f.ctx, proposal.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if old.Status == run.StatusInfrastructureFailed {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("interruption not finalized: %+v", old)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if old.Failure == nil || old.Failure.Code != run.CodexTurnInterruptedCode {
			t.Fatalf("untyped interruption: %+v", old)
		}
		workspace, err := f.facade.DevelopmentWorkspaceForSession(f.ctx, f.session, f.area, f.task)
		if err != nil {
			t.Fatal(err)
		}
		continuation := workspace.Presentation.TaskView.CodexContinuation
		if continuation == nil || continuation.State != "AVAILABLE" || continuation.RunID != old.ID || continuation.Action.Action != "RETRY_IMPLEMENTATION" {
			t.Fatalf("missing exact continuation: %+v task=%+v", continuation, workspace.Presentation.TaskView)
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil {
			t.Fatal(err)
		}
		if old.RuntimeWorkset == nil {
			t.Fatal("missing frozen workset")
		}
		originalContents := map[string]string{}
		for _, member := range old.RuntimeWorkset.Members {
			content, e := os.ReadFile(filepath.Join(member.CanonicalRoot, "same.txt"))
			if e != nil || len(content) == 0 {
				t.Fatalf("partial files lost: %q %v", content, e)
			}
			originalContents[member.MemberID] = string(content)
		}
		var beforePrepare int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&beforePrepare); err != nil {
			t.Fatal(err)
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "interrupted-codex-prepare", Purpose: c.Execution, Action: "RETRY_IMPLEMENTATION", Execution: &c.ExecutionBody{Action: "RETRY_IMPLEMENTATION", TaskID: f.task, FeatureAreaID: f.area, ReviewID: current.CurrentReviewID, ExecutionID: oldExecution, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(oldExecution)}, Variants: []c.Variant{{ID: "continue", Summary: "Continue exact interrupted implementation", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
		if err != nil {
			t.Fatal(err)
		}
		options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if err != nil || options.LockedBackend == nil || options.LockedRuntimeConfig == nil {
			t.Fatalf("missing frozen authority: %+v %v", options, err)
		}
		var before int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if before != beforePrepare {
			t.Fatal("prepare dispatched work before confirmation")
		}
		commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, ContentHash: prepared.ContentHash, RequestID: "interrupted-codex-confirm", VariantID: "continue"}
		commit.Selection = c.Selection{RuntimeConfig: options.LockedRuntimeConfig, Backend: &codexconfig.BackendSelectionInput{ConfigID: options.LockedBackend.ConfigID, ConfigFingerprint: options.LockedBackend.ConfigFingerprint}}
		t.Setenv("DEVBRIDGE_CODING_V1_TURN_SUFFIX", "-continued")
		confirmed, err := f.facade.CommitConfirmation(f.ctx, commit)
		if err != nil || confirmed.State != "COMPLETED" {
			t.Fatalf("confirmation: %+v %v", confirmed, err)
		}
		nextTask, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || nextTask.CurrentExecutionID == oldExecution || nextTask.CurrentDefinitionRevisionID != current.CurrentDefinitionRevisionID {
			t.Fatalf("task replaced: %+v %v", nextTask, err)
		}
		deadline = time.Now().Add(15 * time.Second)
		var successor run.Run
		for {
			proposal, e := f.facade.GetExecutionProposal(f.ctx, f.session, nextTask.CurrentExecutionID)
			if e != nil {
				t.Fatal(e)
			}
			if proposal.RunID != "" {
				successor, e = f.plane.Runs.Get(f.ctx, proposal.RunID)
			}
			if e != nil {
				t.Fatal(e)
			}
			if successor.Status == run.StatusSucceeded {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("continuation did not run: %+v failure=%+v", successor, successor.Failure)
			}
			time.Sleep(10 * time.Millisecond)
		}
		if successor.ID == old.ID || successor.ThreadID != old.ThreadID || successor.RuntimeWorkset.WorkspaceID != old.RuntimeWorkset.WorkspaceID {
			t.Fatalf("lost original workspace/thread: old=%+v successor=%+v", old, successor)
		}
		for i, member := range successor.RuntimeWorkset.Members {
			if member.CanonicalRoot != old.RuntimeWorkset.Members[i].CanonicalRoot {
				t.Fatal("continuation replaced managed worktree")
			}
			content, e := os.ReadFile(filepath.Join(member.CanonicalRoot, "same.txt"))
			if e != nil || string(content) != originalContents[member.MemberID] {
				t.Fatalf("continuation lost partial work: %q %v", content, e)
			}
		}
		replayed, err := f.facade.CommitConfirmation(f.ctx, commit)
		if err != nil || replayed.IntentID != confirmed.IntentID {
			t.Fatalf("replay: %+v %v", replayed, err)
		}
		var count int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&count); err != nil || count != 2 {
			t.Fatalf("duplicate dispatch: %d %v (all runs before confirm %d)", count, err, before)
		}
		unchanged, err := f.plane.Runs.Get(f.ctx, old.ID)
		if err != nil || unchanged.Status != old.Status || unchanged.Failure.Code != old.Failure.Code {
			t.Fatal("original interruption overwritten")
		}
		return false
	})
}
