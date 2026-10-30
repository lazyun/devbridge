package controlapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

type upperPurposeSnapshotFailure struct {
	execution.EvidenceCollector
	fail  bool
	calls int
}

func (s *upperPurposeSnapshotFailure) CaptureSnapshot(ctx context.Context, root string) (string, error) {
	s.calls++
	if s.fail {
		return "", errors.New("definite snapshot failure")
	}
	return s.EvidenceCollector.CaptureSnapshot(ctx, root)
}
func upperPurposePreparedFixture(t *testing.T) (*upperLifecycleFixture, review.Review, run.Run) {
	t.Helper()
	f, intent := recoveryReviewContinuationFixture(t, review.ExecutorUpperLLM)
	if _, err := f.plane.RecoveryPurposeConsumers.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlite.NewReviewStore(f.plane.DB).ContinueRecoveryReviewPreparations(f.ctx, ""); err != nil {
		t.Fatal(err)
	}
	var id, runID string
	if err := f.plane.DB.SQL().QueryRow(`SELECT c.successor_review_id,v.linked_run_id FROM recovery_purpose_confirmation_consumptions c JOIN reviews v ON v.id=c.successor_review_id WHERE c.intent_id=?`, intent.IntentID).Scan(&id, &runID); err != nil {
		t.Fatal(err)
	}
	v, err := f.plane.Reviews.Get(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	return f, v, r
}
func TestUpperPurposeActiveColdRecovery(t *testing.T) {
	f, v, r := upperPurposePreparedFixture(t)
	if _, err := f.plane.Coordinator.StartUpperReview(f.ctx, v, r); err != nil {
		t.Fatal(err)
	}
	var archive string
	if err := f.plane.DB.SQL().QueryRow(`SELECT json_object('binding',b.workset_json,'epoch',b.runtime_epoch,'startEpoch',a.runtime_epoch) FROM runtime_workset_bindings b JOIN runtime_start_authorities a ON a.authority_id=b.start_authority_id WHERE b.run_id=?`, r.ID).Scan(&archive); err != nil {
		t.Fatal(err)
	}
	for _, request := range []string{"recover-once", "recover-twice"} {
		before, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
		if err != nil || before.State != "ACTIVE" {
			t.Fatalf("before=%+v err=%v", before, err)
		}
		reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
		observed, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
		if err != nil || observed.State != "RECOVERY_REQUIRED" {
			t.Fatalf("observed=%+v err=%v", observed, err)
		}
		target := f.targets[0]
		if _, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &before.Target, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "same.txt"}); err == nil {
			t.Fatal("old target retained file permission")
		}
		expected := &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: before.Target.RuntimeEpoch, GrantGeneration: before.Target.GrantGeneration}
		in := agentconversation.RecoverInput{ExpectedUpperVersion: expected, FeatureAreaID: f.area, RunID: r.ID, OperationKind: agentconversation.Review, OperationID: v.ID, RequestID: request}
		bad := in
		wrong := *expected
		wrong.GrantGeneration++
		bad.ExpectedUpperVersion = &wrong
		bad.RequestID = "stale-" + request
		if _, err = f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, bad); err == nil {
			t.Fatal("unobserved grant generation recovered")
		}
		recovered, err := f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, in)
		if err != nil || recovered.GrantState != "PREPARING" {
			t.Fatalf("recover=%+v err=%v", recovered, err)
		}
		current := before.Target
		current.RuntimeEpoch = recovered.AuthorizedUpperVersion.GrantEpoch
		current.GrantGeneration = recovered.AuthorizedUpperVersion.GrantGeneration
		if _, err = f.facade.PrepareUpperReview(f.ctx, before.Target); err == nil {
			t.Fatal("old target prepared recovered grant")
		}
		if _, err = f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &current, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "same.txt"}); err == nil {
			t.Fatal("recovery receipt itself granted files")
		}
		prepared, err := f.facade.PrepareUpperReview(f.ctx, current)
		if err != nil || prepared.State != "ACTIVE" {
			t.Fatalf("prepare=%+v err=%v", prepared, err)
		}
		for _, target := range f.targets {
			got, err := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &current, RepositoryID: target.RepositoryID, MemberID: target.MemberID, MemberVersion: target.MemberVersion, Path: "same.txt"})
			if err != nil || got.Content == "" {
				t.Fatalf("read=%+v err=%v", got, err)
			}
		}
		replay, err := f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, in)
		if err != nil || replay.GrantState != "PREPARING" || *replay.AuthorizedUpperVersion != *recovered.AuthorizedUpperVersion {
			t.Fatalf("replay=%+v err=%v", replay, err)
		}
		var after string
		if err = f.plane.DB.SQL().QueryRow(`SELECT json_object('binding',b.workset_json,'epoch',b.runtime_epoch,'startEpoch',a.runtime_epoch) FROM runtime_workset_bindings b JOIN runtime_start_authorities a ON a.authority_id=b.start_authority_id WHERE b.run_id=?`, r.ID).Scan(&after); err != nil || after != archive {
			t.Fatalf("immutable archive rewritten err=%v", err)
		}
	}
}
func TestUpperPurposeSettledFailureTaskAbandonment(t *testing.T) {
	for _, cold := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-epoch", true: "after-restart"}[cold], func(t *testing.T) {
			f, v, r := upperPurposePreparedFixture(t)
			failure := &upperPurposeSnapshotFailure{EvidenceCollector: f.plane.Coordinator.Evidence, fail: true}
			f.plane.Coordinator.Evidence = failure
			if _, err := f.plane.Coordinator.StartUpperReview(f.ctx, v, r); err == nil {
				t.Fatal("fault not injected")
			}
			access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
			if err != nil || !strings.Contains(access.Blocker, "definite snapshot failure") {
				t.Fatalf("missing blocker=%+v err=%v", access, err)
			}
			var count int
			if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_preparation_failures WHERE run_id=?`, r.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("failure receipt=%d err=%v", count, err)
			}
			failure.fail = false
			if _, err = f.facade.PrepareUpperReview(f.ctx, access.Target); err == nil {
				t.Fatal("settled failure repeated preparation")
			}
			if failure.calls != 1 {
				t.Fatal("repeated external observation")
			}
			if cold {
				reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
			}
			projection, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
			if err != nil {
				t.Fatal(err)
			}
			var in executioncancel.Input
			for _, c := range projection.Capabilities {
				if c.Action == "CANCEL_EXECUTION" && c.Available && c.Target != nil && c.Target.OperationKind == "TASK" {
					in = executioncancel.Input{AISessionID: f.session, OwnerType: c.Target.OperationKind, OwnerID: c.Target.OperationID, RunID: c.Target.RunID, ExpectedUpperVersion: c.Target.ExpectedUpperVersion, RequestID: "abandon-settled-task", Reason: "abandon retained failed preparation"}
				}
			}
			if in.OwnerID != f.task || in.RunID != r.ID || in.ExpectedUpperVersion == nil {
				t.Fatalf("no exact public Task cancellation target: %+v", projection.Capabilities)
			}
			receipt, err := f.facade.CancelExecutionForSession(f.ctx, in)
			if err != nil || receipt.State != "CANCELED" {
				t.Fatalf("cancel=%+v err=%v", receipt, err)
			}
			current, err := f.plane.Tasks.Get(f.ctx, f.task)
			if err != nil || current.Status != "CANCELED" || current.CurrentReviewID != v.ID {
				t.Fatalf("Task=%+v err=%v", current, err)
			}
			for _, table := range []string{"runtime_preparation_failures", "runtime_preparation_revocations", "runtime_preparation_abandonments"} {
				if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, r.ID).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			replay, err := f.facade.CancelExecutionForSession(f.ctx, in)
			if err != nil || replay.State != "CANCELED" {
				t.Fatalf("replay=%+v err=%v", replay, err)
			}
			in.Reason = "changed"
			if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err == nil {
				t.Fatal("changed abandonment payload replay accepted")
			}
		})
	}
}

// A timeout does not prove the returned external operation is quiescent. It
// keeps the claim and exposes no settled-abandonment or second preparation.
func TestUpperPurposeUnknownPreparationCannotAbandonOrRedispatch(t *testing.T) {
	f, v, r := upperPurposePreparedFixture(t)
	uncertain := &upperPurposeUnknownSnapshot{EvidenceCollector: f.plane.Coordinator.Evidence}
	f.plane.Coordinator.Evidence = uncertain
	if _, err := f.plane.Coordinator.StartUpperReview(f.ctx, v, r); err == nil {
		t.Fatal("uncertain observation accepted")
	}
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
	if err != nil || access.Blocker == "" {
		t.Fatalf("unknown blocker=%+v err=%v", access, err)
	}
	var count int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_preparation_failures WHERE run_id=?`, r.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("UNKNOWN settled=%d err=%v", count, err)
	}
	if _, err = f.facade.PrepareUpperReview(f.ctx, access.Target); err == nil || uncertain.calls != 1 {
		t.Fatalf("unknown redispatched calls=%d err=%v", uncertain.calls, err)
	}
	in := executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RunID: r.ID, ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: access.Target.RuntimeEpoch, GrantGeneration: access.Target.GrantGeneration}, RequestID: "unknown-abandon"}
	if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err == nil {
		t.Fatal("unknown Task preparation abandoned without settlement")
	}
	in.OwnerType, in.OwnerID, in.RequestID = "REVIEW", v.ID, "unknown-review-cancel"
	if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err == nil {
		t.Fatal("generic Review cancellation bypassed unsettled preparation")
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM execution_cancellations WHERE run_id=?`, r.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("unknown rejection consumed request count=%d err=%v", count, err)
	}
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	observed, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.RecoverFeatureAreaAgentOperation(f.ctx, f.session, agentconversation.RecoverInput{FeatureAreaID: f.area, RunID: r.ID, OperationKind: agentconversation.Review, OperationID: v.ID, RequestID: "unknown-recover", ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: observed.Target.RuntimeEpoch, GrantGeneration: observed.Target.GrantGeneration}}); err == nil {
		t.Fatal("unknown workspace claim reauthorized for new preparation")
	}
}

type upperPurposeUnknownSnapshot struct {
	execution.EvidenceCollector
	calls int
}

func (s *upperPurposeUnknownSnapshot) CaptureSnapshot(context.Context, string) (string, error) {
	s.calls++
	return "", context.DeadlineExceeded
}

func TestUpperPurposeFailureAbandonmentExactAuthority(t *testing.T) {
	f, v, r := upperPurposePreparedFixture(t)
	fail := &upperPurposeSnapshotFailure{EvidenceCollector: f.plane.Coordinator.Evidence, fail: true}
	f.plane.Coordinator.Evidence = fail
	if _, err := f.plane.Coordinator.StartUpperReview(f.ctx, v, r); err == nil {
		t.Fatal("fault not injected")
	}
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	base := executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RunID: r.ID, ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: access.Target.RuntimeEpoch, GrantGeneration: access.Target.GrantGeneration}, RequestID: "negative"}
	for _, kind := range []string{"missing-version", "wrong-generation", "wrong-epoch", "wrong-task", "wrong-run"} {
		in := base
		version := *base.ExpectedUpperVersion
		in.ExpectedUpperVersion = &version
		in.RequestID = kind
		switch kind {
		case "missing-version":
			in.ExpectedUpperVersion = nil
		case "wrong-generation":
			in.ExpectedUpperVersion.GrantGeneration++
		case "wrong-epoch":
			in.ExpectedUpperVersion.AuthorityEpoch = "foreign"
		case "wrong-task":
			in.OwnerID = "foreign-task"
		case "wrong-run":
			in.RunID = "foreign-run"
		}
		if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err == nil {
			t.Fatalf("%s accepted", kind)
		}
	}
	for _, table := range []string{"runtime_preparation_revocations", "runtime_preparation_abandonments", "execution_cancellations"} {
		var count int
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM `+table+` WHERE run_id=?`, r.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("negative wrote %s count=%d err=%v", table, count, err)
		}
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE runtime_preparation_failures SET message='forged' WHERE run_id=?`, r.ID); err == nil {
		t.Fatal("failure receipt mutable")
	}
	if _, err = f.plane.DB.SQL().Exec(`DELETE FROM runtime_preparation_failures WHERE run_id=?`, r.ID); err == nil {
		t.Fatal("failure receipt deletable")
	}
	var plan, request, payload string
	if err = f.plane.DB.SQL().QueryRow(`SELECT plan_id,request_id,payload_hash FROM runtime_workspace_preparation_claims WHERE run_id=?`, r.ID).Scan(&plan, &request, &payload); err != nil {
		t.Fatal(err)
	}
	if _, err = sqlite.NewRunStore(f.plane.DB).ClaimWorkspacePreparation(f.ctx, plan, r.ID, "changed-claim", payload, time.Now().UTC()); err == nil {
		t.Fatal("second preparation claim accepted")
	}
	if _, err = f.facade.CancelExecutionForSession(f.ctx, base); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runtime_preparation_revocations", "runtime_preparation_abandonments"} {
		if _, err = f.plane.DB.SQL().Exec(`DELETE FROM `+table+` WHERE run_id=?`, r.ID); err == nil {
			t.Fatalf("%s deletable", table)
		}
	}
}

func TestUpperPurposeDefiniteBindingFailureRetainsObservationsForAbandonment(t *testing.T) {
	f, v, r := upperPurposePreparedFixture(t)
	if _, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER upper_binding_fault BEFORE INSERT ON runtime_workset_bindings BEGIN SELECT RAISE(ABORT,'definite binding transaction fault');END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.plane.Coordinator.StartUpperReview(f.ctx, v, r); err == nil {
		t.Fatal("binding fault not injected")
	}
	var observations, failures, bindings int
	if err := f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM runtime_workspace_observations WHERE run_id=?),(SELECT count(*) FROM runtime_preparation_failures WHERE run_id=? AND failed_stage='WORKSET_BINDING'),(SELECT count(*) FROM runtime_workset_bindings WHERE run_id=?)`, r.ID, r.ID, r.ID).Scan(&observations, &failures, &bindings); err != nil || observations != 2 || failures != 1 || bindings != 0 {
		t.Fatalf("observations/failures/bindings=%d/%d/%d err=%v", observations, failures, bindings, err)
	}
	if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER upper_binding_fault`); err != nil {
		t.Fatal(err)
	}
	access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.PrepareUpperReview(f.ctx, access.Target); err == nil {
		t.Fatal("failed binding attempt reused start authority")
	}
	in := executioncancel.Input{AISessionID: f.session, OwnerType: "TASK", OwnerID: f.task, RunID: r.ID, RequestID: "abandon-binding", ExpectedUpperVersion: &agentconversation.UpperVersion{AuthorityEpoch: f.plane.DB.RuntimeEpoch(), GrantEpoch: access.Target.RuntimeEpoch, GrantGeneration: access.Target.GrantGeneration}}
	if _, err = f.facade.CancelExecutionForSession(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_workspace_observations WHERE run_id=?`, r.ID).Scan(&retained); err != nil || retained != observations {
		t.Fatalf("retained observations=%d err=%v", retained, err)
	}
}
