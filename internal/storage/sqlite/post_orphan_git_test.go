package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workspace"
)

func postOrphanGitAcceptance(f *reviewApprovalFixture, execution executionproposal.Proposal) wp.TaskAcceptanceInput {
	return wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "post-orphan-accept-" + execution.ID}
}

// The old Review really enters UNKNOWN, loses its runtime, and is explicitly
// orphaned before this fixture reaches normal current Task Acceptance.
func TestPostOrphanGitDeliveryAndReplay(t *testing.T) {
	for _, mode := range []string{"same-task", "cross-task", "commit-response-loss", "submission-response-loss"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f, oldRun, recovery := postOrphanReviewFixture(t)
			execution := terminalExecutionWithReport(t, f, completePostOrphanReviewExecution(t, f))
			repo, source, base := attachCommitWorkspace(t, f, execution.TaskID, execution.VerificationRunID)
			if mode == "commit-response-loss" {
				if _, err := f.db.sql.Exec(`CREATE TRIGGER post_orphan_commit_loss BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'commit checkpoint response lost'); END`); err != nil {
					t.Fatal(err)
				}
			}
			input := postOrphanGitAcceptance(f, execution)
			accepted, err := f.store.ResolveTaskAcceptance(ctx, input)
			if err != nil {
				t.Fatalf("post-orphan acceptance=%+v err=%v", accepted, err)
			}
			if mode == "commit-response-loss" {
				if accepted.Task.Status != task.StatusActive || accepted.Task.Phase != task.PhaseDelivering {
					t.Fatalf("durable delivery admission lost: %+v", accepted.Task)
				}
			} else if accepted.Task.Status != task.StatusSucceeded {
				t.Fatalf("post-orphan acceptance=%+v err=%v", accepted, err)
			}
			if mode == "commit-response-loss" {
				if accepted.CommitError == "" || accepted.Task.CommitSHA != "" {
					t.Fatalf("missing commit checkpoint failure: %+v", accepted)
				}
				committedHead := commitTestGit(t, source, "rev-parse", "HEAD")
				if committedHead == base {
					t.Fatal("response-loss fixture never reached Git commit")
				}
				if _, err = f.db.sql.Exec(`DROP TRIGGER post_orphan_commit_loss`); err != nil {
					t.Fatal(err)
				}
				reopenPostOrphanFixture(t, f)
				accepted.Task, err = f.store.RetryTaskCommit(ctx, f.project, execution.TaskID)
				if err != nil || accepted.Task.CommitSHA != committedHead {
					t.Fatalf("commit replay duplicated or lost commit: %+v %v", accepted.Task, err)
				}
			} else if accepted.CommitError != "" || accepted.Task.CommitSHA == "" {
				t.Fatalf("historical UNKNOWN blocked auto commit: %+v", accepted)
			}
			firstTask := accepted.Task
			if mode == "cross-task" {
				// Task A remains succeeded and committed. Task B owns a new Review
				// and Execution in the same canonical workspace and FeatureArea.
				f.newIntent(t, "post-orphan-independent-task")
				a := f.start(t)
				f.complete(t, a, approvedResult(t))
				approval, err := f.store.Resolve(ctx, f.input(a, "APPROVE"))
				if err != nil {
					t.Fatal(err)
				}
				execution, err = f.store.AuthorizeTaskExecution(ctx, f.session, approval.Task.ID)
				if err != nil {
					t.Fatal(err)
				}
				execution = terminalExecutionWithReport(t, f, execution)
				commitTestWrite(t, filepath.Join(source, "second-task.txt"), "independent Task after recovered history\n")
				snapshot, err := git.NewClient(git.Config{}).Snapshot(ctx, source)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.sql.Exec(`UPDATE runs SET end_snapshot=?,workspace_id='git-workspace' WHERE id=?`, snapshot, execution.VerificationRunID); err != nil {
					t.Fatal(err)
				}
				input = postOrphanGitAcceptance(f, execution)
				accepted, err = f.store.ResolveTaskAcceptance(ctx, input)
				if err != nil || accepted.CommitError != "" || accepted.Task.ID == firstTask.ID || accepted.Task.CommitSHA == "" || accepted.Task.CommitSHA == firstTask.CommitSHA {
					t.Fatalf("cross-Task automatic commit=%+v %v", accepted, err)
				}
			}
			// Read and write eligibility must agree after another cold restart.
			reopenPostOrphanFixture(t, f)
			for _, inspect := range []bool{false, true} {
				capability, err := f.store.commitCapability(ctx, f.project, f.area, inspect)
				if err != nil || !capability.CanCommit || capability.Reason != "" {
					t.Fatalf("post-orphan capability inspect=%v %+v %v", inspect, capability, err)
				}
			}
			if mode == "submission-response-loss" {
				if _, err = f.db.sql.Exec(`CREATE TRIGGER post_orphan_submission_loss BEFORE UPDATE OF merged_commit ON feature_areas BEGIN SELECT RAISE(ABORT,'submission checkpoint response lost'); END`); err != nil {
					t.Fatal(err)
				}
			}
			area, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "post-orphan-submit")
			if mode == "submission-response-loss" {
				if err == nil {
					t.Fatal("submission checkpoint fault did not fire")
				}
				mergedHead := commitTestGit(t, repo, "rev-parse", "HEAD")
				if mergedHead != accepted.Task.CommitSHA {
					t.Fatal("submission response loss did not follow actual Git delivery")
				}
				if _, err = f.db.sql.Exec(`DROP TRIGGER post_orphan_submission_loss`); err != nil {
					t.Fatal(err)
				}
				reopenPostOrphanFixture(t, f)
				area, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "post-orphan-submit")
				if err != nil || area.MergedCommit != mergedHead {
					t.Fatalf("submission replay=%+v %v", area, err)
				}
			}
			if err != nil || area.MergedCommit != accepted.Task.CommitSHA {
				t.Fatalf("historical orphan blocked submission=%+v %v", area, err)
			}
			assertPostOrphanGitCallbacks(t, f, oldRun, execution, source)
			assertPostOrphanEvidence(t, f, oldRun, recovery)
			// Concurrent response-loss retries must converge on the existing
			// authority/acceptance/Git receipts without creating another commit.
			results := make(chan error, 12)
			start := make(chan struct{})
			for i := 0; i < 3; i++ {
				go func() {
					<-start
					_, err := NewAgentConversationStore(f.db).Recover(ctx, recovery)
					results <- err
				}()
				go func() {
					<-start
					out, err := f.store.ResolveTaskAcceptance(ctx, input)
					if err == nil && (!out.Replay || out.Task.ID != accepted.Task.ID) {
						err = errors.New("acceptance replay changed target")
					}
					results <- err
				}()
				go func() {
					<-start
					out, err := f.store.RetryTaskCommit(ctx, f.project, accepted.Task.ID)
					if err == nil && out.CommitSHA != accepted.Task.CommitSHA {
						err = errors.New("Task commit replay changed result")
					}
					results <- err
				}()
				go func() {
					<-start
					out, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "post-orphan-submit")
					if err == nil && out.MergedCommit != area.MergedCommit {
						err = errors.New("submission replay changed result")
					}
					results <- err
				}()
			}
			close(start)
			for i := 0; i < 12; i++ {
				if err = <-results; err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{repo, source} {
				if head := commitTestGit(t, path, "rev-parse", "HEAD"); head != area.MergedCommit {
					t.Fatal("late callback/replay changed Git result")
				}
				if status := commitTestGit(t, path, "status", "--porcelain"); status != "" {
					t.Fatalf("late callback/replay changed workspace: %s", status)
				}
			}
			var recoveries, submissions, taskCommits int
			if err = f.db.sql.QueryRow(`SELECT (SELECT count(*) FROM agent_operation_recoveries),(SELECT count(*) FROM feature_area_commit_receipts),(SELECT count(*) FROM task_commit_receipts)`).Scan(&recoveries, &submissions, &taskCommits); err != nil {
				t.Fatal(err)
			}
			wantCommits := 1
			if mode == "cross-task" {
				wantCommits = 2
			}
			if recoveries != 1 || submissions != 1 || taskCommits != wantCommits {
				t.Fatalf("duplicate durable receipts: recovery=%d submission=%d Task=%d", recoveries, submissions, taskCommits)
			}
		})
	}
}

func assertPostOrphanGitCallbacks(t *testing.T, f *reviewApprovalFixture, oldRun run.Run, current executionproposal.Proposal, source string) {
	t.Helper()
	ctx := context.Background()
	before := loadTaskAcceptanceHistory(t, f, current.TaskID)
	beforeWorkspace, err := NewWorkspaceStore(f.db).Get(ctx, "git-workspace")
	if err != nil {
		t.Fatal(err)
	}
	beforeHead := commitTestGit(t, source, "rev-parse", "HEAD")
	stale := runtimeepoch.WithFence(ctx, oldRun.RuntimeEpoch, oldRun.ID)
	mutations := []func() error{
		func() error {
			_, err := NewRunStore(f.db).Update(stale, oldRun.ID, func(r *run.Run) error { r.Status = run.StatusSucceeded; return nil })
			return err
		},
		func() error {
			_, err := NewTaskStore(f.db).Update(stale, current.TaskID, func(v *task.Task) error { v.Status = task.StatusCanceled; return nil })
			return err
		},
		func() error {
			_, err := NewExecutionProposalStore(f.db).Complete(stale, current.ID, executionproposal.OutcomeFailed, "LATE", "late callback", testNow)
			return err
		},
		func() error {
			_, err := NewWorkspaceStore(f.db).Update(stale, "git-workspace", func(v *workspace.Workspace) error { v.Status = workspace.StatusContinuityUnavailable; return nil })
			return err
		},
		func() error {
			return NewAgentConversationStore(f.db).FinishRun(stale, oldRun.ID, agentconversation.Unknown, "", true)
		},
	}
	for i, mutate := range mutations {
		if err := mutate(); !errors.Is(err, runtimeepoch.ErrFenced) {
			t.Fatalf("late callback %d not fenced: %v", i, err)
		}
	}
	after := loadTaskAcceptanceHistory(t, f, current.TaskID)
	afterWorkspace, err := NewWorkspaceStore(f.db).Get(ctx, "git-workspace")
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(beforeWorkspace, afterWorkspace) || commitTestGit(t, source, "rev-parse", "HEAD") != beforeHead {
		t.Fatalf("late callback changed canonical state: %v", err)
	}
}

func TestPostOrphanGitDirectFileExclusion(t *testing.T) {
	ctx := context.Background()
	f, oldRun, recovery := postOrphanReviewFixture(t)
	execution := terminalExecutionWithReport(t, f, completePostOrphanReviewExecution(t, f))
	repo, source, _ := attachCommitWorkspace(t, f, execution.TaskID, execution.VerificationRunID)
	out, err := f.store.ResolveTaskAcceptance(ctx, postOrphanGitAcceptance(f, execution))
	if err != nil || out.CommitError != "" {
		t.Fatalf("accept %+v %v", out, err)
	}
	authority := NewDirectFileAuthority(f.db)
	scope := directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: "post-orphan-direct-file"}
	if _, err = authority.Enable(ctx, scope); err != nil {
		t.Fatalf("historical orphan blocked Direct File enable: %v", err)
	}
	for _, inspect := range []bool{false, true} {
		capability, err := f.store.commitCapability(ctx, f.project, f.area, inspect)
		if err != nil || capability.CanCommit || capability.CanRetry || !strings.Contains(capability.Reason, directfile.CapabilityActive) {
			t.Fatalf("Direct File capability inspect=%v %+v %v", inspect, capability, err)
		}
	}
	completed, lastError := deliveryReceiptState(t, f, execution.TaskID)
	beforeSource, beforeTarget := commitTestGit(t, source, "rev-parse", "HEAD"), commitTestGit(t, repo, "rev-parse", "HEAD")
	readback, err := f.store.RetryTaskCommit(ctx, f.project, execution.TaskID)
	if err != nil || !reflect.DeepEqual(readback, out.Task) {
		t.Fatalf("completed Task readback changed result: status=%s commit=%s err=%v", readback.Status, readback.CommitSHA, err)
	}
	if after, afterError := deliveryReceiptState(t, f, execution.TaskID); after != completed || afterError != lastError {
		t.Fatalf("completed Task readback changed receipt: before=%+v/%q after=%+v/%q", completed, lastError, after, afterError)
	}
	if commitTestGit(t, source, "rev-parse", "HEAD") != beforeSource || commitTestGit(t, repo, "rev-parse", "HEAD") != beforeTarget {
		t.Fatal("completed Task readback changed Git")
	}
	if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "direct-file-submit"); err == nil || !strings.Contains(err.Error(), directfile.CapabilityActive) {
		t.Fatalf("submission ignored Direct File grant: %v", err)
	}
	if _, err = authority.Disable(ctx, scope); err != nil {
		t.Fatal(err)
	}
	area, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "direct-file-submit")
	if err != nil || area.MergedCommit != out.Task.CommitSHA {
		t.Fatalf("grant release failed to restore submission: %+v %v", area, err)
	}
	assertPostOrphanGitCallbacks(t, f, oldRun, execution, source)
	assertPostOrphanEvidence(t, f, oldRun, recovery)
}

func TestUnresolvedAuthorityBlocksGitCommands(t *testing.T) {
	for _, classification := range []string{"UNKNOWN", "CONFLICT"} {
		t.Run(classification, func(t *testing.T) {
			ctx := context.Background()
			f, owner, execution := acceptedExecutionFixture(t, "-unresolved-git")
			execution = terminalExecutionWithReport(t, f, execution)
			repo, source, _ := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
			if _, err := f.db.sql.Exec(`CREATE TRIGGER unresolved_commit_checkpoint BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'pending Git receipt'); END`); err != nil {
				t.Fatal(err)
			}
			out, err := f.store.ResolveTaskAcceptance(ctx, postOrphanGitAcceptance(f, execution))
			if err != nil || out.CommitError == "" || out.Task.CommitSHA != "" {
				t.Fatalf("pending commit fixture %+v %v", out, err)
			}
			if _, err = f.db.sql.Exec(`DROP TRIGGER unresolved_commit_checkpoint`); err != nil {
				t.Fatal(err)
			}
			// A durable unsafe classification without a permanent disposition
			// remains blocking even if no claim happens to be present.
			if _, err = f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,?,'unresolved control',?)`, execution.VerificationRunID, classification, f.db.epoch); err != nil {
				t.Fatal(err)
			}
			beforeSource, beforeTarget := commitTestGit(t, source, "rev-parse", "HEAD"), commitTestGit(t, repo, "rev-parse", "HEAD")
			for _, inspect := range []bool{false, true} {
				capability, err := f.store.commitCapability(ctx, f.project, f.area, inspect)
				if err != nil || capability.Reason != errFeatureAreaBusy.Error() || capability.CanCommit || capability.CanRetry {
					t.Fatalf("unresolved capability=%+v %v", capability, err)
				}
			}
			if _, err = f.store.RetryTaskCommit(ctx, f.project, owner.ID); !errors.Is(err, errFeatureAreaBusy) {
				t.Fatalf("unresolved Task auto commit=%v", err)
			}
			if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "unsafe-submit"); err == nil || !strings.Contains(err.Error(), errFeatureAreaBusy.Error()) {
				t.Fatalf("unresolved submission=%v", err)
			}
			if commitTestGit(t, source, "rev-parse", "HEAD") != beforeSource || commitTestGit(t, repo, "rev-parse", "HEAD") != beforeTarget {
				t.Fatal("unresolved authority allowed Git mutation")
			}
			var submissions, completed int
			if err = f.db.sql.QueryRow(`SELECT (SELECT count(*) FROM feature_area_commit_receipts),(SELECT count(*) FROM task_commit_receipts WHERE completed_at IS NOT NULL)`).Scan(&submissions, &completed); err != nil || submissions != 0 || completed != 0 {
				t.Fatalf("unresolved authority admitted receipt: %d/%d %v", submissions, completed, err)
			}
		})
	}
}

// Task B has verification evidence but has not accepted yet when Task A starts
// its Review. This is a reachable state without defeating the pending Git
// receipt admission fence. The Review has no workspace binding, so revocation
// fences its exact context without quarantining Task B's canonical workspace.
func TestInitialOrphanRecoveryRacesAutomaticCommitAndSubmission(t *testing.T) {
	ctx := context.Background()
	f, taskB, executionB := acceptedExecutionFixture(t, "-recovery-delivery-race")
	executionB = terminalExecutionWithReport(t, f, executionB)
	repo, source, _ := attachCommitWorkspace(t, f, taskB.ID, executionB.VerificationRunID)
	acceptB := postOrphanGitAcceptance(f, executionB)
	f.newIntent(t, "unknown-task-a")
	a := f.start(t)
	if _, err := NewRunStore(f.db).Update(ctx, a.Run.ID, func(v *run.Run) error { v.Status = run.StatusPreparing; return nil }); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	conversation, err := conversations.BeginRun(ctx, a.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThread(ctx, a.Run.ID, "race-orphan-thread"); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, a.Run.ID, run.StatusRunning, "race-orphan-thread", "race-lost-turn")
	if err = conversations.MarkTurnPending(ctx, a.Run.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.RecordFinalization(ctx, a.Run.ID, agentconversation.Finalization{Advance: agentconversation.Unknown, Quiesced: false}); err != nil {
		t.Fatal(err)
	}
	oldRun, err := NewRunStore(f.db).Get(ctx, a.Run.ID)
	if err != nil || oldRun.WorkspaceID != "" {
		t.Fatalf("unexpected workspace binding: %+v %v", oldRun, err)
	}
	recovery := agentconversation.RecoverInput{FeatureAreaID: f.area, RunID: oldRun.ID, OperationKind: agentconversation.Review, OperationID: oldRun.OperationID, AgentConversationID: conversation.ID, RequestID: "initial-git-race-recovery"}
	reopenPostOrphanFixture(t, f)
	if err = NewRecoveryStore(f.db).RecoverAgentOperations(ctx, f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.ResolveTaskAcceptance(ctx, acceptB); err == nil {
		t.Fatal("unresolved Task A allowed Task B Acceptance/automatic commit")
	}
	if _, err = f.store.CommitFeatureArea(ctx, f.project, f.area, "initial-race-submit"); err == nil || !strings.Contains(err.Error(), errFeatureAreaBusy.Error()) {
		t.Fatalf("unresolved Task A submission=%v", err)
	}
	start := make(chan struct{})
	recovered, accepted, submitted := make(chan error, 1), make(chan error, 1), make(chan error, 1)
	go func() {
		<-start
		_, err := NewAgentConversationStore(f.db).Recover(ctx, recovery)
		recovered <- err
	}()
	go func() {
		<-start
		_, err := f.store.ResolveTaskAcceptance(ctx, acceptB)
		accepted <- err
	}()
	go func() {
		<-start
		_, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "initial-race-submit")
		submitted <- err
	}()
	close(start)
	if err = <-recovered; err != nil {
		t.Fatal(err)
	}
	if err = <-accepted; err != nil && !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("Acceptance/automatic commit race: %v", err)
	}
	if err = <-submitted; err == nil || (!strings.Contains(err.Error(), errFeatureAreaBusy.Error()) && !strings.Contains(err.Error(), "TASK_COMMIT_PENDING")) {
		t.Fatalf("submission must retain incomplete Task A guard: %v", err)
	}
	outB, err := f.store.ResolveTaskAcceptance(ctx, acceptB)
	if err != nil || outB.Task.CommitSHA == "" || outB.CommitError != "" {
		t.Fatalf("recovery did not unblock Task B automatic commit: %+v %v", outB, err)
	}
	var orphaned, unsafeEvidence, ended, noClaim bool
	if err = f.db.sql.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_orphan_dispositions WHERE run_id=? AND workspace_id IS NULL),EXISTS(SELECT 1 FROM agent_operation_boundaries WHERE run_id=? AND advance_state='UNKNOWN' AND quiesced=0),EXISTS(SELECT 1 FROM agent_conversations WHERE id=? AND ended_at IS NOT NULL),NOT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE run_id=?)`, oldRun.ID, oldRun.ID, conversation.ID, oldRun.ID).Scan(&orphaned, &unsafeEvidence, &ended, &noClaim); err != nil || !orphaned || !unsafeEvidence || !ended || !noClaim {
		t.Fatalf("incomplete/null-workspace revocation facts: %v %v %v %v %v", orphaned, unsafeEvidence, ended, noClaim, err)
	}
	// Cold resume must keep the same disposition while Task A legitimately
	// completes its own workflow; only then may the whole Area submit.
	reopenPostOrphanFixture(t, f)
	executionA := terminalExecutionWithReport(t, f, completePostOrphanReviewExecution(t, f))
	commitTestWrite(t, filepath.Join(source, "recovered-task-a.txt"), "Task A completed after explicit recovery\n")
	snapshot, err := git.NewClient(git.Config{}).Snapshot(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runs SET end_snapshot=?,workspace_id='git-workspace' WHERE id=?`, snapshot, executionA.VerificationRunID); err != nil {
		t.Fatal(err)
	}
	outA, err := f.store.ResolveTaskAcceptance(ctx, postOrphanGitAcceptance(f, executionA))
	if err != nil || outA.CommitError != "" || outA.Task.CommitSHA == "" {
		t.Fatalf("Task A continuation=%+v %v", outA, err)
	}
	area, err := f.store.CommitFeatureArea(ctx, f.project, f.area, "initial-race-submit")
	if err != nil || area.MergedCommit != outA.Task.CommitSHA || commitTestGit(t, repo, "rev-parse", "HEAD") != area.MergedCommit {
		t.Fatalf("post-race delivery=%+v %v", area, err)
	}
	assertPostOrphanGitCallbacks(t, f, oldRun, executionA, source)
}
