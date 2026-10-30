package sqlite

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workspace"
)

func reopenPostOrphanFixture(t *testing.T, f *reviewApprovalFixture) {
	t.Helper()
	root := f.db.StateRoot()
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	f.db, f.store = db, NewWorkflowProgressionStore(db)
	f.store.clock = acceptanceTestClock(f.now.Add(time.Hour))
	f.reviews, err = review.NewService(NewReviewStore(db), nil, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
}

// The fixture uses the real Task-owned admission, dispatch markers, startup
// recovery and explicit recovery transaction. It never rewrites UNKNOWN.
func postOrphanReviewFixture(t *testing.T) (*reviewApprovalFixture, run.Run, agentconversation.RecoverInput) {
	t.Helper()
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	old, input := beginPostOrphanUnknown(t, f, a.Run)
	recoverPostOrphanFixture(t, f, input)
	assertPostOrphanEvidence(t, f, old, input)
	return f, old, input
}

func beginPostOrphanUnknown(t *testing.T, f *reviewApprovalFixture, value run.Run) (run.Run, agentconversation.RecoverInput) {
	t.Helper()
	ctx := context.Background()
	owner := ownedTask(t, f)
	oldWorkspace := workspace.Workspace{ID: "post-orphan-old-workspace", ProjectID: f.project, RepositoryID: owner.RepositoryID, FeatureAreaID: f.area, BaseCommit: owner.BaseCommit, Status: workspace.StatusReady, CreatedAt: f.now, UpdatedAt: f.now}
	if err := NewWorkspaceStore(f.db).Create(ctx, oldWorkspace); err != nil {
		t.Fatal(err)
	}
	var err error
	value, err = NewRunStore(f.db).Update(ctx, value.ID, func(r *run.Run) error {
		r.WorkspaceID, r.Status, r.UpdatedAt = oldWorkspace.ID, run.StatusPreparing, f.now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	conversation, err := conversations.BeginRun(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThread(ctx, value.ID, "post-orphan-old-thread"); err != nil {
		t.Fatal(err)
	}
	// Dispatch is durable, but a lost response leaves the turn identity unknown.
	setAgentRunState(t, f.db, value.ID, run.StatusRunning, "post-orphan-old-thread", "")
	if err = conversations.MarkTurnPending(ctx, value.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.RecordFinalization(ctx, value.ID, agentconversation.Finalization{Advance: agentconversation.Unknown, Quiesced: false}); err != nil {
		t.Fatal(err)
	}
	value, err = NewRunStore(f.db).Get(ctx, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value, agentconversation.RecoverInput{FeatureAreaID: f.area, RunID: value.ID, OperationKind: agentconversation.OperationKind(value.OperationKind), OperationID: value.OperationID, AgentConversationID: conversation.ID, RequestID: "post-orphan-recovery"}
}

func recoverPostOrphanFixture(t *testing.T, f *reviewApprovalFixture, input agentconversation.RecoverInput) {
	t.Helper()
	ctx := context.Background()
	reopenPostOrphanFixture(t, f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(ctx, f.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	result, err := conversations.Recover(ctx, input)
	if err != nil || result.BackendDisposition != "UNKNOWN_ORPHANED" || !result.ContextAbandoned || !result.FreshWorkAllowed {
		t.Fatalf("recovery=%+v err=%v", result, err)
	}
	// Simulate response loss, then another complete process restart.
	replayed, err := conversations.Recover(ctx, input)
	if err != nil || replayed != result {
		t.Fatalf("recovery replay=%+v err=%v", replayed, err)
	}
	reopenPostOrphanFixture(t, f)
	replayed, err = NewAgentConversationStore(f.db).Recover(ctx, input)
	if err != nil || replayed != result {
		t.Fatalf("cold recovery replay=%+v err=%v", replayed, err)
	}
}

func assertPostOrphanEvidence(t *testing.T, f *reviewApprovalFixture, old run.Run, input agentconversation.RecoverInput) {
	t.Helper()
	var exact, oldContextEnded, quarantined, detached, noClaim bool
	err := f.db.sql.QueryRow(`SELECT
 EXISTS(SELECT 1 FROM agent_orphan_dispositions o JOIN agent_operation_recovery_states r ON r.run_id=o.run_id JOIN agent_operation_boundaries b ON b.run_id=o.run_id JOIN agent_finalization_intents i ON i.run_id=o.run_id WHERE o.run_id=? AND o.task_id=? AND o.owner_type=? AND o.owner_id=? AND r.classification='UNKNOWN' AND b.advance_state='UNKNOWN' AND b.quiesced=0 AND i.advance_state='UNKNOWN' AND i.quiesced=0),
 EXISTS(SELECT 1 FROM agent_conversations WHERE id=? AND ended_at IS NOT NULL AND continuation_safe=0),
 EXISTS(SELECT 1 FROM workspaces WHERE id=? AND runtime_quarantined=1 AND status='CONTINUITY_UNAVAILABLE'),
 NOT EXISTS(SELECT 1 FROM feature_areas WHERE id=? AND current_workspace_id=?),
 NOT EXISTS(SELECT 1 FROM feature_area_agent_claims WHERE run_id=?)`, old.ID, old.TaskID, old.OperationKind, old.OperationID, input.AgentConversationID, old.WorkspaceID, f.area, old.WorkspaceID, old.ID).Scan(&exact, &oldContextEnded, &quarantined, &detached, &noClaim)
	if err != nil || !exact || !oldContextEnded || !quarantined || !detached || !noClaim {
		t.Fatalf("orphan facts exact=%v ended=%v quarantined=%v detached=%v noClaim=%v err=%v", exact, oldContextEnded, quarantined, detached, noClaim, err)
	}
}

func completePostOrphanReviewExecution(t *testing.T, f *reviewApprovalFixture) executionproposal.Proposal {
	t.Helper()
	a := f.start(t)
	if a.Review.Ordinal != 2 || a.Review.TaskID != f.candidate.TaskID {
		t.Fatalf("post-orphan Review=%+v", a.Review)
	}
	f.complete(t, a, approvedResult(t))
	in := f.input(a, "APPROVE")
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true}
	out, err := f.store.Resolve(context.Background(), in)
	if err != nil || out.Task.ID != f.candidate.TaskID {
		t.Fatalf("post-orphan Approval=%+v err=%v", out, err)
	}
	proposal, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, out.Task.ID)
	if err != nil || proposal.RuntimeConfig.Model != "gpt-5.6-terra" || proposal.RuntimeConfig.ReasoningEffort != "high" || !proposal.RuntimeConfig.CodexFastMode || proposal.RuntimeConfig.ServiceTier != "priority" {
		t.Fatalf("post-orphan execution=%+v err=%v", proposal, err)
	}
	return proposal
}

func postOrphanAcceptanceInput(f *reviewApprovalFixture, proposal executionproposal.Proposal, option string) wp.TaskAcceptanceInput {
	in := wp.TaskAcceptanceInput{AISessionID: f.session, TaskID: proposal.TaskID, FeatureAreaID: f.area, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(proposal.ID), ExecutionID: proposal.ID, Option: option, RequestID: "post-orphan-" + option}
	if option == wp.AcceptanceRequestChanges {
		work, verification := task.CloneWorkSpec(proposal.WorkSpec), task.CloneVerificationSpec(proposal.VerificationSpec)
		work.CodexInstruction = "Exact post-orphan correction.\nKeep approved scope."
		verification.CodexInstruction = "Exact post-orphan verification.\nPreserve instruction bytes."
		in.NextWorkSpec, in.NextVerificationSpec = &work, &verification
	}
	if option == wp.AcceptanceRequestChanges {
		in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-sol", ReasoningEffort: "xhigh", CodexFastMode: option == wp.AcceptanceRequestChanges}
	}
	return in
}

func TestPostOrphanReviewAcceptanceFourActionsAndRuntimeConfig(t *testing.T) {
	for _, option := range []string{wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestChanges, wp.AcceptanceRequestReverification} {
		t.Run(option, func(t *testing.T) {
			f, old, recoveryInput := postOrphanReviewFixture(t)
			proposal := completePostOrphanReviewExecution(t, f)
			proposal = terminalExecutionEvidence(t, f, proposal, run.VerificationCheckPassed, "")
			f.guidance(t, string(task.PhaseAcceptancePending), "RESOLVE_TASK_ACCEPTANCE")
			in := postOrphanAcceptanceInput(f, proposal, option)
			out, err := f.store.ResolveTaskAcceptance(context.Background(), in)
			if err != nil || out.Task.ID != proposal.TaskID || out.Receipt.Option != option {
				t.Fatalf("post-orphan %s=%+v err=%v", option, out, err)
			}
			if option == wp.AcceptanceAccept && (out.Task.Status != task.StatusActive || out.Task.Phase != task.PhaseDelivering) || option == wp.AcceptanceReject && out.Task.Status != task.StatusCanceled {
				t.Fatalf("terminal Task=%+v", out.Task)
			}
			if in.NextRunRuntimeConfig != nil {
				successor, e := NewExecutionProposalStore(f.db).Get(context.Background(), out.SuccessorExecutionID)
				want := *in.NextRunRuntimeConfig
				if want.CodexFastMode {
					want.ServiceTier = "priority"
				}
				if e != nil || successor.RuntimeConfig != want || successor.TaskID != proposal.TaskID || out.Task.Status != task.StatusActive {
					t.Fatalf("frozen successor=%+v err=%v", successor, e)
				}
				if option == wp.AcceptanceRequestChanges {
					if successor.TaskDefinitionRevisionID == proposal.TaskDefinitionRevisionID || successor.WorkSpec.CodexInstruction != in.NextWorkSpec.CodexInstruction || successor.VerificationSpec.CodexInstruction != in.NextVerificationSpec.CodexInstruction {
						t.Fatalf("new immutable definition/prompt=%+v", successor)
					}
				} else if successor.TaskDefinitionRevisionID != proposal.TaskDefinitionRevisionID || successor.ImplementationRunID != "" || !reflect.DeepEqual(successor.WorkSpec, proposal.WorkSpec) || !reflect.DeepEqual(successor.VerificationSpec, proposal.VerificationSpec) {
					t.Fatalf("verification-only authorization=%+v", successor)
				}
			} else if option == wp.AcceptanceRequestReverification {
				successor, e := NewExecutionProposalStore(f.db).Get(context.Background(), out.SuccessorExecutionID)
				if e != nil || successor.RuntimeConfig != proposal.RuntimeConfig {
					t.Fatalf("reverification must inherit frozen runtime config successor=%+v err=%v", successor, e)
				}
			}
			reopenPostOrphanFixture(t, f)
			// Replay must use the persisted selection without consulting changed
			// Desktop defaults or the model allowlist after restart.
			f.store.SetRuntimeConfigProvider(func() codexruntime.Allowlist {
				t.Error("receipt replay reread runtime defaults")
				return codexruntime.Allowlist{}
			})
			var wg sync.WaitGroup
			for range 6 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					replayed, e := f.store.ResolveTaskAcceptance(context.Background(), in)
					if e != nil || !replayed.Replay || replayed.Receipt != out.Receipt || replayed.SuccessorExecutionID != out.SuccessorExecutionID {
						t.Errorf("cold concurrent acceptance replay=%+v err=%v", replayed, e)
					}
				}()
			}
			wg.Wait()
			if in.NextRunRuntimeConfig != nil {
				changed := *in.NextRunRuntimeConfig
				changed.CodexFastMode = !changed.CodexFastMode
				conflict := in
				conflict.NextRunRuntimeConfig = &changed
				if _, e := f.store.ResolveTaskAcceptance(context.Background(), conflict); !errors.Is(e, executionproposal.ErrConflict) {
					t.Fatalf("changed runtime replay err=%v", e)
				}
				wantRevisions := 2
				if option == wp.AcceptanceRequestChanges {
					wantRevisions = 3
				}
				if ownedCount(t, f, "execution_proposals") != 2 || ownedCount(t, f, "task_definition_revisions") != wantRevisions {
					t.Fatal("response-loss replay duplicated successor")
				}
				if option == wp.AcceptanceRequestReverification {
					successor, verification, e := NewExecutionProposalStore(f.db).ConsumeAndCreateVerificationRun(context.Background(), out.SuccessorExecutionID, "post-orphan-verification-only", f.now.Add(4*time.Hour))
					if e != nil || successor.ImplementationRunID != "" || successor.VerificationRunID != verification.ID || verification.Purpose != run.PurposeVerification || successor.RuntimeConfig != *in.NextRunRuntimeConfig {
						t.Fatalf("post-orphan verification-only Run=%+v successor=%+v err=%v", verification, successor, e)
					}
				}
			}
			before := ownedTask(t, f)
			late := runtimeepoch.WithFence(context.Background(), old.RuntimeEpoch, old.ID)
			if _, e := NewReviewStore(f.db).RecordPrompt(late, old.ReviewID, "late rewritten prompt", f.now); !errors.Is(e, runtimeepoch.ErrFenced) {
				t.Fatalf("old backend callback=%v", e)
			}
			if after := ownedTask(t, f); !reflect.DeepEqual(after, before) {
				t.Fatal("late callback changed current Task")
			}
			assertPostOrphanEvidence(t, f, old, recoveryInput)
		})
	}
}

func TestPostOrphanDirectFileStillBlocksFreshAcceptance(t *testing.T) {
	f, old, recovery := postOrphanReviewFixture(t)
	proposal := terminalExecutionWithReport(t, f, completePostOrphanReviewExecution(t, f))
	attachCommitWorkspace(t, f, proposal.TaskID, proposal.VerificationRunID)
	authority := NewDirectFileAuthority(f.db)
	scope := directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: "post-orphan-direct-file"}
	// Scope-only Direct File grants have no approved definition identity and
	// cannot be activated for a DEVELOPMENT Task. They therefore cannot create
	// a synthetic Acceptance blocker; v2 task authorization owns that fence.
	if _, err := authority.Enable(context.Background(), scope); err == nil {
		t.Fatal("legacy scope-only Direct File grant succeeded")
	} else {
		assertDirectFileCode(t, err, directfile.CapabilityActive)
	}
	f.guidance(t, string(task.PhaseAcceptancePending), "RESOLVE_TASK_ACCEPTANCE")
	if _, err := f.store.ResolveTaskAcceptance(context.Background(), postOrphanAcceptanceInput(f, proposal, wp.AcceptanceReject)); err != nil {
		t.Fatal(err)
	}
	assertPostOrphanEvidence(t, f, old, recovery)
}

func TestPostOrphanReviewReviseRejectPreserveTaskOwnedDepth(t *testing.T) {
	for _, option := range []string{"REVISE", "REJECT"} {
		t.Run(option, func(t *testing.T) {
			f, old, recovery := postOrphanReviewFixture(t)
			a := f.start(t)
			f.complete(t, a, approvedResult(t))
			out, err := f.store.Resolve(context.Background(), f.input(a, option))
			if err != nil || out.Task.ID != old.TaskID {
				t.Fatalf("post-orphan Review %s=%+v err=%v", option, out, err)
			}
			if option == "REVISE" {
				if out.Task.Status != task.StatusActive || out.Task.Phase != task.PhaseReviewing || out.Task.CurrentReviewID != out.SuccessorReviewID || out.SuccessorReviewID == "" || out.SuccessorRunID == "" || out.Task.RequiredReviewDepth == nil || *out.Task.RequiredReviewDepth != task.ReviewDepthFast {
					t.Fatalf("REVISE task/depth=%+v", out.Task)
				}
			} else if out.Task.Status != task.StatusCanceled {
				t.Fatalf("REJECT task=%+v", out.Task)
			}
			if ownedCount(t, f, "task_definition_revisions") != 1 || ownedCount(t, f, "execution_proposals") != 0 {
				t.Fatal("non-Approval action created runtime/spec authority")
			}
			assertPostOrphanEvidence(t, f, old, recovery)
		})
	}
}

func TestPostOrphanFailedExecutionGuidanceActionsExecute(t *testing.T) {
	for _, option := range []string{wp.AcceptanceReject, wp.AcceptanceRequestChanges} {
		t.Run(option, func(t *testing.T) {
			f, _, proposal := acceptedExecutionFixture(t, "post-orphan-failed")
			_, implementation, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(context.Background(), proposal.ID, "post-orphan-failed-implementation", f.now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			old, input := beginPostOrphanUnknown(t, f, implementation)
			recoverPostOrphanFixture(t, f, input)
			proposal, err = NewExecutionProposalStore(f.db).Get(context.Background(), proposal.ID)
			if err != nil || proposal.Outcome != executionproposal.OutcomeFailed {
				t.Fatalf("recovered terminal Execution=%+v err=%v", proposal, err)
			}
			f.guidance(t, string(task.PhaseImplementing), "RESOLVE_TASK_ACCEPTANCE")
			if _, err = f.store.ResolveTaskAcceptance(context.Background(), postOrphanAcceptanceInput(f, proposal, wp.AcceptanceAccept)); !errors.Is(err, executionproposal.ErrConflict) {
				t.Fatalf("unverified failed execution ACCEPT err=%v", err)
			}
			out, err := f.store.ResolveTaskAcceptance(context.Background(), postOrphanAcceptanceInput(f, proposal, option))
			if err != nil || out.Receipt.Option != option {
				t.Fatalf("guidance-authorized action=%+v err=%v", out, err)
			}
			assertPostOrphanEvidence(t, f, old, input)
		})
	}
}

func TestPostOrphanUnresolvedHistoryStillBlocksAcceptance(t *testing.T) {
	for _, classification := range []string{"UNKNOWN", "CONFLICT"} {
		t.Run(classification, func(t *testing.T) {
			f, _, proposal := acceptedExecutionFixture(t, "post-orphan-unresolved")
			proposal = terminalExecutionWithReport(t, f, proposal)
			var reviewRun string
			if err := f.db.sql.QueryRow(`SELECT linked_run_id FROM reviews WHERE id=(SELECT current_review_id FROM tasks WHERE id=?)`, proposal.TaskID).Scan(&reviewRun); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,?,'unresolved history without orphan disposition',?)`, reviewRun, classification, f.db.epoch); err != nil {
				t.Fatal(err)
			}
			f.guidance(t, "RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION")
			for _, option := range []string{wp.AcceptanceAccept, wp.AcceptanceReject, wp.AcceptanceRequestChanges, wp.AcceptanceRequestReverification} {
				if _, err := f.store.ResolveTaskAcceptance(context.Background(), postOrphanAcceptanceInput(f, proposal, option)); !errors.Is(err, executionproposal.ErrConflict) {
					t.Fatalf("unresolved %s permitted %s: %v", classification, option, err)
				}
			}
			if ownedCount(t, f, "agent_orphan_dispositions") != 0 || ownedCount(t, f, "execution_proposals") != 1 {
				t.Fatal("blocked action changed durable history")
			}
		})
	}
}
