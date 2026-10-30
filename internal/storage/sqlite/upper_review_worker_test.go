package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func upperWorkerFixture(t *testing.T) (*reviewApprovalFixture, review.SubmitInput, context.Context) {
	t.Helper()
	f := newReviewApprovalFixture(t)
	ctx := context.Background()
	f.candidate.ExecutorKind = review.ExecutorUpperLLM
	f.candidate.Instruction = "review\n  exact\ttext\n"
	// Upper Reviews carry neither Coding backend selection nor runtime config.
	f.candidate.Model = ""
	f.candidate.ReasoningEffort = ""
	f.candidate.CodexConfigID = ""
	f.candidate.BackendSelection = nil
	a := f.start(t)
	reviewID, runID := a.Review.ID, a.Run.ID
	r := a.Run
	attachCommitWorkspace(t, f, f.candidate.TaskID, runID)
	var err error
	r, err = NewRunStore(f.db).Update(ctx, runID, func(v *run.Run) error {
		v.WorkspaceID = "git-workspace"
		v.WorktreeID = "commit-worktree"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, r.ID, run.StatusRunning, "", "")
	in := review.SubmitInput{UpperTarget: review.UpperTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.candidate.TaskID, ReviewID: reviewID, RunID: runID, RuntimeEpoch: r.RuntimeEpoch, GrantGeneration: 1}, RequestID: "request", Report: review.ReviewReport{SchemaVersion: 1, Summary: "blocked", Disposition: review.DispositionBlocked, Coverage: []string{"repository"}, Findings: []review.Finding{}, Limitations: []string{"missing dependency"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}}
	if _, err := NewUpperReviewAccessStore(f.db).Prepare(ctx, in.UpperTarget); err != nil {
		t.Fatalf("prepare Upper Review access: %v", err)
	}

	var scope string
	if err := f.db.sql.QueryRow(`SELECT caller_scope FROM ai_sessions WHERE id=?`, f.session).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	return f, in, aisession.WithCallerScope(ctx, scope)
}
func TestUpperWorkerClaimDispatchReplayAndSubmission(t *testing.T) {
	f, in, ctx := upperWorkerFixture(t)
	store := NewUpperReviewWorkerStore(f.db)
	claim := review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	first, err := store.ClaimUpperReviewWorker(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	if first.ExecutionPayload == nil || first.ExecutionPayload.Instruction != f.candidate.Instruction || first.State != review.WorkerDispatchIntent {
		t.Fatalf("first=%+v", first)
	}
	replay, err := store.ClaimUpperReviewWorker(ctx, claim)
	if err != nil || replay.ExecutionPayload != nil || replay.State != review.WorkerOutcomeUnknown || !reflect.DeepEqual(first.Receipt, replay.Receipt) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	other := claim
	other.WorkerID = "other"
	if _, err = store.ClaimUpperReviewWorker(ctx, other); !errors.Is(err, review.ErrRequestConflict) {
		t.Fatalf("different worker: %v", err)
	}
	if _, err = store.ClaimUpperReviewWorker(context.Background(), claim); err == nil {
		t.Fatal("unauthenticated claim")
	}
	results := NewUpperReviewResultStore(f.db)
	if _, err = results.SubmitReviewResult(ctx, in); !errors.Is(err, review.ErrTargetStale) {
		t.Fatalf("unclaimed result: %v", err)
	}
	in.WorkerClaim = &claim.WorkerClaimIdentity
	if _, err = f.db.sql.Exec(`CREATE TRIGGER fail_worker_complete BEFORE UPDATE ON upper_review_worker_claims BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = results.SubmitReviewResult(ctx, in); err == nil {
		t.Fatal("missing rollback")
	}
	var n int
	if err = f.db.sql.QueryRow(`SELECT count(*) FROM review_reports WHERE run_id=?`, in.RunID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rollback reports=%d err=%v", n, err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER fail_worker_complete`); err != nil {
		t.Fatal(err)
	}
	receipt, err := results.SubmitReviewResult(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.GetUpperReviewWorkerClaim(ctx, claim)
	if err != nil || got.State != review.WorkerResultRecorded || got.ExecutionPayload != nil || got.ResultReceipt == nil || !reflect.DeepEqual(*got.ResultReceipt, receipt) {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if _, err = results.SubmitReviewResult(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = results.SubmitReviewResult(aisession.WithCallerScope(context.Background(), "wrong-scope"), in); err == nil {
		t.Fatal("completed claimed report replay crossed caller scope")
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	terminalReplay, err := NewUpperReviewResultStore(reopened).SubmitReviewResult(ctx, in)
	if err != nil || !reflect.DeepEqual(terminalReplay, receipt) {
		t.Fatalf("completed receipt restart replay=%+v err=%v", terminalReplay, err)
	}
}

func TestUpperWorkerConcurrentClaimReturnsPayloadOnce(t *testing.T) {
	f, in, ctx := upperWorkerFixture(t)
	store := NewUpperReviewWorkerStore(f.db)
	claim := review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	var wg sync.WaitGroup
	results := make(chan review.WorkerClaimResult, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); v, e := store.ClaimUpperReviewWorker(ctx, claim); results <- v; errs <- e }()
	}
	wg.Wait()
	close(results)
	close(errs)
	count := 0
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	for v := range results {
		if v.ExecutionPayload != nil {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("payload count=%d", count)
	}
}

func TestUpperWorkerRestartKeepsUnknownAndCannotRedispatch(t *testing.T) {
	f, in, ctx := upperWorkerFixture(t)
	claim := review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	first, err := NewUpperReviewWorkerStore(f.db).ClaimUpperReviewWorker(ctx, claim)
	if err != nil {
		t.Fatal(err)
	}
	root := f.db.stateRoot
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	store := NewUpperReviewWorkerStore(reopened)
	for _, read := range []bool{false, true} {
		var got review.WorkerClaimResult
		if read {
			got, err = store.GetUpperReviewWorkerClaim(ctx, claim)
		} else {
			got, err = store.ClaimUpperReviewWorker(ctx, claim)
		}
		if err != nil || got.State != review.WorkerOutcomeUnknown || got.ExecutionPayload != nil || !reflect.DeepEqual(got.Receipt, first.Receipt) {
			t.Fatalf("restart got=%+v err=%v", got, err)
		}
	}
	staleContext := runtimeepoch.WithFence(ctx, claim.RuntimeEpoch, claim.RunID)
	for _, read := range []bool{false, true} {
		var got review.WorkerClaimResult
		if read {
			got, err = store.GetUpperReviewWorkerClaim(staleContext, claim)
		} else {
			got, err = store.ClaimUpperReviewWorker(staleContext, claim)
		}
		if err != nil || got.ExecutionPayload != nil || !reflect.DeepEqual(got.Receipt, first.Receipt) {
			t.Fatalf("historical stale-context read=%+v err=%v", got, err)
		}
	}
	var beforeEpoch, afterEpoch string
	var beforeGeneration, afterGeneration int64
	if err = reopened.sql.QueryRow(`SELECT runtime_epoch,generation FROM review_grants WHERE review_id=?`, in.ReviewID).Scan(&beforeEpoch, &beforeGeneration); err != nil {
		t.Fatal(err)
	}
	_, err = NewAgentConversationStore(reopened).Recover(ctx, agentconversation.RecoverInput{FeatureAreaID: in.FeatureAreaID, RunID: in.RunID, OperationID: in.ReviewID, OperationKind: agentconversation.Review, RequestID: "recover-worker", ProjectID: f.project, ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: reopened.epoch, GrantEpoch: beforeEpoch, GrantGeneration: beforeGeneration}})
	if !errors.Is(err, agentconversation.ErrConflict) || !strings.Contains(err.Error(), "worker continuation is unsupported") {
		t.Fatalf("worker recovery=%v", err)
	}
	if err = reopened.sql.QueryRow(`SELECT runtime_epoch,generation FROM review_grants WHERE review_id=?`, in.ReviewID).Scan(&afterEpoch, &afterGeneration); err != nil || beforeEpoch != afterEpoch || beforeGeneration != afterGeneration {
		t.Fatalf("recovery changed grant: %s/%d -> %s/%d err=%v", beforeEpoch, beforeGeneration, afterEpoch, afterGeneration, err)
	}
	other := claim
	other.RequestID = "redispatch"
	if _, err = store.ClaimUpperReviewWorker(ctx, other); !errors.Is(err, review.ErrRequestConflict) {
		t.Fatalf("redispatch: %v", err)
	}
	in.WorkerClaim = &claim.WorkerClaimIdentity
	if _, err = NewUpperReviewResultStore(reopened).SubmitReviewResult(ctx, in); err == nil {
		t.Fatal("stale epoch accepted result")
	}
	if _, err = store.GetUpperReviewWorkerClaim(aisession.WithCallerScope(context.Background(), "wrong-scope"), claim); err == nil {
		t.Fatal("wrong caller scope read")
	}
}

func TestUpperWorkerClaimRejectsNoncurrentTargetWithoutIntent(t *testing.T) {
	f, in, ctx := upperWorkerFixture(t)
	store := NewUpperReviewWorkerStore(f.db)
	claim := review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	claim.GrantGeneration++
	if _, err := store.ClaimUpperReviewWorker(ctx, claim); err == nil {
		t.Fatal("wrong grant accepted")
	}
	claim.GrantGeneration--
	claim.RuntimeEpoch = "wrong-epoch"
	if _, err := store.ClaimUpperReviewWorker(ctx, claim); err == nil {
		t.Fatal("wrong epoch accepted")
	}
	var n int
	if err := f.db.sql.QueryRow(`SELECT count(*) FROM upper_review_worker_claims`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("claims=%d err=%v", n, err)
	}
}

func TestUpperWorkerClaimAndUnclaimedSubmissionSerialize(t *testing.T) {
	f, in, ctx := upperWorkerFixture(t)
	claim := review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	var wg sync.WaitGroup
	var claimed review.WorkerClaimResult
	var claimErr, submitErr error
	start := make(chan struct{})
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		claimed, claimErr = NewUpperReviewWorkerStore(f.db).ClaimUpperReviewWorker(ctx, claim)
	}()
	go func() {
		defer wg.Done()
		<-start
		_, submitErr = NewUpperReviewResultStore(f.db).SubmitReviewResult(ctx, in)
	}()
	close(start)
	wg.Wait()
	if claimErr == nil {
		if claimed.ExecutionPayload == nil || submitErr == nil {
			t.Fatalf("claim=%+v submit err=%v", claimed, submitErr)
		}
		var n int
		if err := f.db.sql.QueryRow(`SELECT count(*) FROM review_reports WHERE run_id=?`, in.RunID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("claim won but reports=%d err=%v", n, err)
		}
	} else if submitErr != nil {
		t.Fatalf("both failed: claim=%v submit=%v", claimErr, submitErr)
	}
}

func TestUpperWorkerOptionalClaimPreservesLegacyReportPayload(t *testing.T) {
	_, in, _ := upperWorkerFixture(t)
	legacy := struct {
		review.UpperTarget
		RequestID string              `json:"requestId"`
		Report    review.ReviewReport `json:"report"`
	}{in.UpperTarget, in.RequestID, in.Report}
	want, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	actual, hash, err := reportPayload(in)
	if err != nil || string(actual) != string(want) || hash != workerDigest(want) {
		t.Fatalf("legacy payload changed: %s err=%v", actual, err)
	}
	in.WorkerClaim = &review.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}
	_, next, err := reportPayload(in)
	if err != nil || next == hash {
		t.Fatalf("claim omitted from result identity: %v", err)
	}
}

func TestUpperWorkerRestartGuidanceIsReadOnlyAndDoesNotAdvertiseRecovery(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(fmt.Sprintf("enrolled-%t", enrolled), func(t *testing.T) {
			f, in, ctx := upperWorkerFixture(t)
			if enrolled {
				_, err := NewUpperReviewWorkerStore(f.db).ClaimUpperReviewWorker(ctx, review.WorkerClaimInput{UpperTarget: in.UpperTarget, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "dispatch-guidance", WorkerID: "worker"}})
				if err != nil {
					t.Fatal(err)
				}
				eligibility, err := NewAgentConversationStore(f.db).RecoveryEligibility(ctx, in.FeatureAreaID)
				if err != nil || eligibility.Available || eligibility.ReasonCode != agentconversation.ReasonUpperWorkerContinuationUnsupported || eligibility.Prerequisite != "" || !strings.Contains(eligibility.Reason, "worker continuation is unsupported") {
					t.Fatalf("current enrolled eligibility=%+v err=%v", eligibility, err)
				}
				live, err := NewWorkflowProgressionStore(f.db).ReadTask(ctx, f.project, in.FeatureAreaID, in.TaskID)
				if err != nil || live.NextAction.Code == "CANCEL_REVIEW" || live.NextAction.Code == "RECOVER_AGENT_OPERATION" {
					t.Fatalf("live guidance changed=%+v err=%v", live, err)
				}
			}
			root := f.db.stateRoot
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err = NewRecoveryStore(db).RecoverAgentOperations(ctx, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var value string
				if err := db.sql.QueryRow(`SELECT json_array((SELECT json_group_array(json_array(id,status,runtime_epoch)) FROM runs),(SELECT json_group_array(json_array(review_id,state,runtime_epoch,generation)) FROM review_grants),(SELECT json_group_array(json_array(feature_area_id,operation_id,run_id,runtime_epoch)) FROM feature_area_agent_claims),(SELECT count(*) FROM upper_review_worker_claims),(SELECT count(*) FROM agent_operation_recoveries))`).Scan(&value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			before := snapshot()
			for i := 0; i < 2; i++ {
				g, err := NewWorkflowProgressionStore(db).ReadTask(ctx, f.project, in.FeatureAreaID, in.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				eligibility, err := NewAgentConversationStore(db).RecoveryEligibility(ctx, in.FeatureAreaID)
				if err != nil {
					t.Fatal(err)
				}
				state, err := NewAgentConversationStore(db).State(ctx, in.FeatureAreaID)
				if err != nil {
					t.Fatal(err)
				}
				if enrolled {
					if g.NextAction.Code != "CANCEL_REVIEW" || !strings.Contains(g.NextAction.Message, "worker continuation is unsupported") || eligibility.Available || eligibility.Target != nil || eligibility.Prerequisite != "" || !strings.Contains(state.Message, "worker continuation is unsupported") {
						t.Fatalf("enrolled g=%+v eligibility=%+v state=%+v", g, eligibility, state)
					}
				} else if g.NextAction.Code != "RECOVER_AGENT_OPERATION" || !eligibility.Available {
					t.Fatalf("unclaimed recovery changed: g=%+v eligibility=%+v", g, eligibility)
				}
			}
			if after := snapshot(); before != after {
				t.Fatalf("guidance mutated authority: before=%s after=%s", before, after)
			}
		})
	}
}
