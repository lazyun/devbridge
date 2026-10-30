package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func assertProgressionAuthority(t *testing.T, db *DB, area string, busy, recovery bool) {
	t.Helper()
	state, err := readRuntimeAuthority(context.Background(), db.sql, area)
	if err != nil || state.Busy != busy || state.RecoveryRequired != recovery {
		t.Fatalf("authority=%+v err=%v want busy=%v recovery=%v", state, err, busy, recovery)
	}
}

func pendingPostOrphanReview(t *testing.T) (*reviewApprovalFixture, run.Run, agentconversation.RecoverInput) {
	t.Helper()
	f := newReviewApprovalFixture(t)
	a := f.start(t)
	old, input := beginPostOrphanUnknown(t, f, a.Run)
	reopenPostOrphanFixture(t, f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), f.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertProgressionAuthority(t, f.db, f.area, true, true)
	return f, old, input
}

func TestRuntimeAuthorityPartialOrphanRecoveryFailClosed(t *testing.T) {
	for _, point := range []struct{ name, trigger string }{
		{"quarantine", "BEFORE UPDATE OF runtime_quarantined ON workspaces WHEN NEW.runtime_quarantined=1"},
		{"context", "BEFORE UPDATE OF ended_at ON agent_conversations WHEN NEW.ended_at IS NOT NULL"},
		{"disposition", "BEFORE INSERT ON agent_orphan_dispositions"},
		{"claim", "BEFORE DELETE ON feature_area_agent_claims"},
		{"receipt", "BEFORE INSERT ON agent_operation_recoveries"},
	} {
		t.Run(point.name, func(t *testing.T) {
			ctx := context.Background()
			f, old, input := pendingPostOrphanReview(t)
			boundary, err := NewAgentConversationStore(f.db).Boundary(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.sql.Exec("CREATE TRIGGER closure_partial " + point.trigger + " BEGIN SELECT RAISE(ABORT,'injected partial recovery'); END"); err != nil {
				t.Fatal(err)
			}
			if _, err = NewAgentConversationStore(f.db).Recover(ctx, input); err == nil {
				t.Fatal("partial recovery committed")
			}
			assertProgressionAuthority(t, f.db, f.area, true, true)
			if err = commitBusy(ctx, f.db.sql, f.area); !errors.Is(err, errFeatureAreaBusy) {
				t.Fatalf("partial recovery Git gate=%v", err)
			}
			f.guidance(t, "RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION")
			if ownedCount(t, f, "agent_orphan_dispositions") != 0 || ownedCount(t, f, "agent_operation_recoveries") != 0 {
				t.Fatal("partial receipt escaped rollback")
			}
			if _, err = f.db.sql.Exec("DROP TRIGGER closure_partial"); err != nil {
				t.Fatal(err)
			}
			if _, err = NewAgentConversationStore(f.db).Recover(ctx, input); err != nil {
				t.Fatal(err)
			}
			assertProgressionAuthority(t, f.db, f.area, false, false)
			after, err := NewAgentConversationStore(f.db).Boundary(ctx, old.ID)
			if err != nil || !reflect.DeepEqual(after, boundary) {
				t.Fatal("recovery changed historical UNKNOWN boundary")
			}
			assertPostOrphanEvidence(t, f, old, input)
		})
	}
}

func TestRuntimeAuthoritySameEpochOrphanWithoutWorkspace(t *testing.T) {
	ctx := context.Background()
	f, old, input := unifiedOwnerFixture(t, agentconversation.Review)
	assertProgressionAuthority(t, f.db, f.area.ID, true, false)
	dispatchCancellation(t, f, input)
	if err := NewExecutionCancellationStore(f.db).FinishInterrupted(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	var sameEpoch, noWorkspace bool
	if err := f.db.sql.QueryRow(`SELECT original_runtime_epoch=observed_runtime_epoch,workspace_id IS NULL FROM agent_orphan_dispositions WHERE run_id=? AND source='CANCELLATION'`, old.ID).Scan(&sameEpoch, &noWorkspace); err != nil || !sameEpoch || !noWorkspace {
		t.Fatalf("same-epoch NULL workspace fixture: same=%v null=%v err=%v", sameEpoch, noWorkspace, err)
	}
	assertProgressionAuthority(t, f.db, f.area.ID, false, false)
	// Even a caller using the current epoch cannot reclaim an orphaned Run.
	if _, err := NewRunStore(f.db).Update(runtimeepoch.WithFence(ctx, f.db.epoch, old.ID), old.ID, func(r *run.Run) error { r.Result = &run.Result{}; return nil }); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("permanent orphan fence=%v", err)
	}
	reopenAgentFixtureDB(t, &f)
	assertProgressionAuthority(t, f.db, f.area.ID, false, false)
}

func TestPostOrphanRecoveryAdmissionAndLateCallbackRace(t *testing.T) {
	for attempt := range 4 {
		t.Run(string(rune('a'+attempt)), func(t *testing.T) {
			ctx := context.Background()
			f, old, input := pendingPostOrphanReview(t)
			var admission review.StartAdmission
			var admissionErr, recoveryErr, callbackErr error
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(3)
			admit := func(request string) (review.StartAdmission, error) {
				scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
				return f.reviews.AdmitStart(ctx, f.candidate, request, strings.Repeat("a", 64), f.candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
			}
			go func() { defer wg.Done(); <-start; _, recoveryErr = NewAgentConversationStore(f.db).Recover(ctx, input) }()
			go func() { defer wg.Done(); <-start; admission, admissionErr = admit("racing-review") }()
			go func() {
				defer wg.Done()
				<-start
				_, callbackErr = NewReviewStore(f.db).RecordPrompt(runtimeepoch.WithFence(ctx, old.RuntimeEpoch, old.ID), old.ReviewID, "stale prompt", f.now)
			}()
			close(start)
			wg.Wait()
			if recoveryErr != nil || !errors.Is(callbackErr, runtimeepoch.ErrFenced) {
				t.Fatalf("recover=%v callback=%v", recoveryErr, callbackErr)
			}
			if admissionErr != nil {
				if !errors.Is(admissionErr, agentconversation.ErrBusy) {
					t.Fatalf("admission=%v", admissionErr)
				}
				admission, admissionErr = admit("review-after-recovery")
			}
			if admissionErr != nil || admission.Run.ID == old.ID || admission.Review.Ordinal != 2 {
				t.Fatalf("new Review=%+v err=%v", admission, admissionErr)
			}
			claim, err := NewAgentConversationStore(f.db).GetClaim(ctx, f.area)
			if err != nil || claim.RunID != admission.Run.ID || ownedCount(t, f, "feature_area_agent_claims") != 1 {
				t.Fatalf("owner=%+v err=%v", claim, err)
			}
			assertProgressionAuthority(t, f.db, f.area, true, false)
			assertPostOrphanEvidence(t, f, old, input)
		})
	}
}

func TestPostOrphanRecoveryAcceptanceRace(t *testing.T) {
	for attempt := range 4 {
		t.Run(string(rune('a'+attempt)), func(t *testing.T) {
			ctx := context.Background()
			f, _, proposal := acceptedExecutionFixture(t, "recovery-race")
			_, value, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, proposal.ID, "unknown-execution", f.now.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			old, recovery := beginPostOrphanUnknown(t, f, value)
			reopenPostOrphanFixture(t, f)
			if err = NewRecoveryStore(f.db).RecoverAgentOperations(ctx, f.now.Add(2*time.Hour)); err != nil {
				t.Fatal(err)
			}
			assertProgressionAuthority(t, f.db, f.area, true, true)
			in := postOrphanAcceptanceInput(f, proposal, wp.AcceptanceRequestChanges)
			if _, err = f.store.ResolveTaskAcceptance(ctx, in); !errors.Is(err, executionproposal.ErrConflict) {
				t.Fatalf("unrecovered acceptance=%v", err)
			}
			// Approval may retain both the authored and approved revisions. The
			// race must append exactly one successor to that immutable history.
			beforeExecutions := ownedCount(t, f, "execution_proposals")
			beforeRevisions := ownedCount(t, f, "task_definition_revisions")
			beforeDispositions := ownedCount(t, f, "agent_orphan_dispositions")
			var recoveryErr, acceptanceErr error
			start := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, recoveryErr = NewAgentConversationStore(f.db).Recover(ctx, recovery)
			}()
			go func() { defer wg.Done(); <-start; _, acceptanceErr = f.store.ResolveTaskAcceptance(ctx, in) }()
			close(start)
			wg.Wait()
			if recoveryErr != nil || acceptanceErr != nil && !errors.Is(acceptanceErr, executionproposal.ErrConflict) {
				t.Fatalf("recovery=%v acceptance=%v", recoveryErr, acceptanceErr)
			}
			out, err := f.store.ResolveTaskAcceptance(ctx, in)
			if err != nil || out.SuccessorExecutionID == "" {
				t.Fatalf("post-recovery action=%+v err=%v", out, err)
			}
			if executions, revisions, dispositions := ownedCount(t, f, "execution_proposals"), ownedCount(t, f, "task_definition_revisions"), ownedCount(t, f, "agent_orphan_dispositions"); executions != beforeExecutions+1 || revisions != beforeRevisions+1 || dispositions != beforeDispositions+1 {
				t.Fatalf("race duplicated successor or revocation: executions=%d revisions=%d dispositions=%d", executions, revisions, dispositions)
			}
			successor, err := NewExecutionProposalStore(f.db).Get(ctx, out.SuccessorExecutionID)
			if err != nil || successor.TaskID != proposal.TaskID || successor.TaskDefinitionRevisionID == proposal.TaskDefinitionRevisionID || successor.TaskDefinitionRevisionID != out.Task.CurrentDefinitionRevisionID {
				t.Fatalf("race successor does not own the exact new revision: %+v %v", successor, err)
			}
			assertPostOrphanEvidence(t, f, old, recovery)
		})
	}
}

func TestRuntimeAuthorityPreservesSerialVerificationGuidance(t *testing.T) {
	ctx := context.Background()
	f, owner, proposal := acceptedExecutionFixture(t, "serial-authority")
	_, implementation, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, proposal.ID, "serial-implementation", f.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	service := run.NewService(NewRunStore(f.db), nil, acceptanceTestClock(f.now.Add(2*time.Hour)))
	for _, status := range []run.Status{run.StatusPreparing, run.StatusRunning, run.StatusCollecting} {
		if _, err = service.Transition(ctx, implementation.ID, status); err != nil {
			t.Fatal(err)
		}
		if status == run.StatusPreparing {
			if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = NewAgentConversationStore(f.db).FinishRun(ctx, implementation.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Succeed(ctx, implementation.ID, run.Result{}); err != nil {
		t.Fatal(err)
	}
	assertProgressionAuthority(t, f.db, f.area, true, false)
	for _, read := range []func() (wp.Guidance, error){
		func() (wp.Guidance, error) { return f.store.Read(ctx, f.project, f.area) },
		func() (wp.Guidance, error) { return f.store.ReadTask(ctx, f.project, f.area, owner.ID) },
	} {
		g, e := read()
		if e != nil || g.TaskPhase != task.PhaseVerificationReady || g.NextAction.Code != "START_VERIFICATION" {
			t.Fatalf("serial guidance=%+v err=%v", g, e)
		}
	}
	if err = commitBusy(ctx, f.db.sql, f.area); !errors.Is(err, errFeatureAreaBusy) {
		t.Fatalf("serial claim Git gate=%v", err)
	}
	if _, err = f.store.ResolveTaskAcceptance(ctx, postOrphanAcceptanceInput(f, proposal, wp.AcceptanceReject)); !errors.Is(err, executionproposal.ErrConflict) {
		t.Fatalf("serial owner Acceptance=%v", err)
	}
	if _, _, err = NewExecutionProposalStore(f.db).ReserveVerificationRun(ctx, proposal.ID, "serial-verification", f.now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeAuthorityRecoveryPrecedesPendingGitGuidance(t *testing.T) {
	ctx := context.Background()
	f, _, proposal := acceptedExecutionFixture(t, "pending-git-authority")
	proposal = terminalExecutionWithReport(t, f, proposal)
	out, err := f.store.ResolveTaskAcceptance(ctx, postOrphanAcceptanceInput(f, proposal, wp.AcceptanceAccept))
	if err != nil || out.Task.Status != task.StatusActive || out.Task.Phase != task.PhaseDelivering || out.CommitError == "" {
		t.Fatalf("pending Git setup=%+v err=%v", out, err)
	}
	f.guidance(t, "TASK_DELIVERING", "RETRY_GIT_FINALIZATION")
	// A contradictory durable recovery classification is fail-closed even
	// without a live claim; pending Git must not hide it behind a retry action.
	if _, err = f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'CONFLICT','conflicting durable observation',?)`, proposal.VerificationRunID, f.db.epoch); err != nil {
		t.Fatal(err)
	}
	g := f.guidance(t, "RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION")
	if g.DecisionID != "" || g.Approval != nil {
		t.Fatal("recovery retained an actionable decision")
	}
	if _, err = f.store.RetryTaskCommit(ctx, f.project, proposal.TaskID); !errors.Is(err, errFeatureAreaBusy) {
		t.Fatalf("unsafe retry=%v", err)
	}
}

func TestRuntimeAuthorityReviewDecisionProjectionAndWriteAgree(t *testing.T) {
	for _, blocker := range []string{"UNKNOWN", "DIRECT_FILE"} {
		for _, option := range []string{"APPROVE", "REVISE", "REJECT"} {
			t.Run(blocker+"/"+option, func(t *testing.T) {
				ctx := context.Background()
				f := newReviewApprovalFixture(t)
				a := f.start(t)
				f.complete(t, a, approvedResult(t))
				input := f.input(a, option)
				var disable func()
				state, action := "RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION"
				if blocker == "UNKNOWN" {
					if _, err := f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','unresolved historical authority',?)`, a.Run.ID, f.db.epoch); err != nil {
						t.Fatal(err)
					}
				} else {
					attachCommitWorkspace(t, f, a.Review.TaskID, a.Run.ID)
					authority := NewDirectFileAuthority(f.db)
					scope := directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: "approval-authority"}
					// Scope-only Direct File activation is retired. Its rejection is
					// the compatibility assertion; use the durable recovery fence to
					// retain the projection/write agreement exercised below.
					if _, err := authority.Enable(ctx, scope); err == nil {
						t.Fatal("legacy scope-only direct file grant succeeded")
					} else {
						assertDirectFileCode(t, err, directfile.CapabilityActive)
					}
					if _, err := f.db.sql.Exec(`INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','legacy direct-file activation is closed',?)`, a.Run.ID, f.db.epoch); err != nil {
						t.Fatal(err)
					}
				}
				g := f.guidance(t, state, action)
				if g.Approval != nil || g.DecisionID != "" {
					t.Fatal("blocked guidance can still create an Approval button")
				}
				before := ownedTask(t, f)
				if _, err := f.store.Resolve(ctx, input); err == nil {
					t.Fatal("blocked Review decision mutated")
				}
				if !reflect.DeepEqual(before, ownedTask(t, f)) || ownedCount(t, f, "decision_resolution_receipts") != 0 {
					t.Fatal("blocked decision left effects")
				}
				if disable != nil {
					disable()
					if _, err := f.store.Resolve(ctx, input); err != nil {
						t.Fatal(err)
					}
					if replay, err := f.store.Resolve(ctx, input); err != nil || !replay.Replay {
						t.Fatalf("Review replay=%+v err=%v", replay, err)
					}
				}
			})
		}
	}
}
