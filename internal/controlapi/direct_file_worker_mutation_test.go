package controlapi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
)

func directMutationWorkerFixture(t *testing.T, enroll bool) (*upperLifecycleFixture, task.Task, directfile.State, directfile.WorkerClaimInput) {
	t.Helper()
	f, current := approveDirectFixture(t, false, true)
	if started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" {
		t.Fatalf("state %+v %v", state, err)
	}
	claim := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}}
	if enroll {
		if got, err := f.facade.ClaimFileImplementationWorker(f.ctx, claim); err != nil || got.ExecutionPayload == nil {
			t.Fatalf("claim: %+v %v", got, err)
		}
	}
	return f, current, state, claim
}

func invokeWorkerMutation(ctx context.Context, service *directfile.Service, kind string, in directfile.Input) (directfile.Result, error) {
	switch kind {
	case "CREATE":
		return service.Create(ctx, in)
	case "REPLACE":
		return service.Replace(ctx, in)
	case "DELETE":
		return service.Delete(ctx, in)
	case "MOVE":
		return service.Move(ctx, in)
	}
	panic("unexpected fixture operation")
}

func TestDirectFileWorkerMutationAllKindsFencedAndHistoricalReplay(t *testing.T) {
	for _, kind := range []string{"CREATE", "REPLACE", "DELETE", "MOVE"} {
		t.Run(kind, func(t *testing.T) {
			f, current, state, claim := directMutationWorkerFixture(t, true)
			in := directInput(f, state, current.RepositoryTargets[0])
			initial, err := f.facade.FileRead(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			in.RequestID = "mutation"
			in.ExpectedDigest = initial.Digest
			in.Content = "worker-owned exact bytes\n"
			if kind == "CREATE" {
				in.Path = "new.txt"
				in.ExpectedDigest = ""
			}
			if kind == "MOVE" {
				in.Destination = "new.txt"
			}
			for _, invalid := range []string{"missing", "worker", "request", "scope"} {
				t.Run(invalid, func(t *testing.T) {
					bad := in
					ctx := f.ctx
					if invalid != "missing" {
						identity := claim.WorkerClaimIdentity
						bad.WorkerClaim = &identity
					}
					if invalid == "worker" {
						bad.WorkerClaim.WorkerID = "other"
					}
					if invalid == "request" {
						bad.WorkerClaim.RequestID = "other"
					}
					if invalid == "scope" {
						ctx = aisession.WithCallerScope(context.Background(), "foreign-scope")
					}
					if _, err := invokeWorkerMutation(ctx, f.plane.DirectFiles, kind, bad); err == nil {
						t.Fatal("unauthorized worker mutation accepted")
					}
				})
			}
			var n int
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=?`, state.WorkflowID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("denial persisted intent: %d %v", n, err)
			}
			read, err := f.facade.FileRead(f.ctx, directInput(f, state, current.RepositoryTargets[0]))
			if err != nil || read.Digest != initial.Digest {
				t.Fatalf("denial changed source: %+v %v", read, err)
			}
			in.WorkerClaim = &claim.WorkerClaimIdentity
			got, err := invokeWorkerMutation(f.ctx, f.plane.DirectFiles, kind, in)
			if err != nil {
				t.Fatal(err)
			}
			if canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state); err != nil || canceled.State != "COMPLETED" {
				t.Fatalf("cancel %+v %v", canceled, err)
			}
			// Closed/restarted authority must not be reopened to return exact prior proof.
			reopenDirectDeliveryFixture(t, f)
			var root string
			if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, in.MemberID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(root, root+".offline"); err != nil {
				t.Fatal(err)
			}
			defer os.Rename(root+".offline", root)
			called := false
			f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { called = true; return errors.New("historical proof must not touch files") })
			stale := runtimeepoch.WithFence(f.ctx, state.RuntimeEpoch, "")
			replay, err := invokeWorkerMutation(stale, f.plane.DirectFiles, kind, in)
			if err != nil || !reflect.DeepEqual(got, replay) || called {
				t.Fatalf("historical proof %+v, want %+v: %v", replay, got, err)
			}
			foreign := aisession.WithCallerScope(context.Background(), "foreign-scope")
			if _, err := invokeWorkerMutation(foreign, f.plane.DirectFiles, kind, in); err == nil {
				t.Fatal("historical worker replay accepted foreign scope")
			}
			fresh := in
			fresh.RequestID = "fresh-after-close"
			if _, err := invokeWorkerMutation(f.ctx, f.plane.DirectFiles, kind, fresh); err == nil {
				t.Fatal("closed grant admitted new effect")
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=?`, state.WorkflowID).Scan(&n); err != nil || n != 1 {
				t.Fatalf("replay created intent: %d %v", n, err)
			}
		})
	}
}

func TestDirectFileWorkerMutationJournalProvesEffectWithoutRedispatch(t *testing.T) {
	f, current, state, claim := directMutationWorkerFixture(t, true)
	in := directInput(f, state, current.RepositoryTargets[0])
	before, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "journal-effect"
	in.ExpectedDigest = before.Digest
	in.Content = "effect recorded once\n"
	in.WorkerClaim = &claim.WorkerClaimIdentity
	calls := 0
	if !f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { calls++; return errors.New("injected after durable journal") }) {
		t.Fatal("missing fault hook")
	}
	if _, err := f.plane.DirectFiles.Replace(f.ctx, in); err == nil {
		t.Fatal("fault did not interrupt receipt")
	}
	var root, status string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, in.MemberID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, in.Path))
	if err != nil || string(bytes) != in.Content {
		t.Fatalf("effect evidence %q %v", bytes, err)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&status); err != nil || status != "PENDING" {
		t.Fatalf("receipt %s %v", status, err)
	}
	journalFiles, err := filepath.Glob(filepath.Join(f.dataRoot, "state", "direct-file-journal", "*.json"))
	if err != nil || len(journalFiles) != 1 {
		t.Fatalf("expected one durable journal: %v %v", journalFiles, err)
	}
	journal := journalFiles[0]
	// Hide (do not erase) this fixture's journal to prove unknown is not
	// upgraded merely because the caller supplies the original worker token.
	if err := os.Rename(journal, journal+".held"); err != nil {
		t.Fatal(err)
	}
	// A real Core restart must not force replay of the already-journaled effect
	// or require forbidden same-workflow worker recovery just to read its proof.
	reopenDirectDeliveryFixture(t, f)
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error {
		calls++
		return errors.New("historical journal lookup must never execute file effect")
	})
	bad := in
	bad.WorkerClaim = nil
	if _, err := f.plane.DirectFiles.Replace(f.ctx, bad); err == nil {
		t.Fatal("journal recovered without worker claim")
	}
	if calls != 1 {
		t.Fatalf("invalid replay reexecuted effect: %d", calls)
	}
	if _, err := f.plane.DirectFiles.Replace(f.ctx, in); err == nil || calls != 1 {
		t.Fatalf("unknown without proof reexecuted or completed: %v calls=%d", err, calls)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&status); err != nil || (status != "PENDING" && status != "UNKNOWN") {
		t.Fatalf("unproved effect became terminal: %s %v", status, err)
	}
	var beforeGrant, afterGrant string
	grantSQL := `SELECT json_array(state,runtime_epoch,generation,operation_count,started_at) FROM direct_file_task_authorizations WHERE workflow_id=?`
	if err := f.plane.DB.SQL().QueryRow(grantSQL, state.WorkflowID).Scan(&beforeGrant); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(journal+".held", journal); err != nil {
		t.Fatal(err)
	}
	stale := runtimeepoch.WithFence(f.ctx, state.RuntimeEpoch, "")
	if _, err := f.plane.DirectFiles.Replace(stale, in); err == nil || calls != 1 {
		t.Fatalf("stale writer promoted journal: %v calls=%d", err, calls)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&status); err != nil || (status != "PENDING" && status != "UNKNOWN") {
		t.Fatalf("stale writer changed receipt: %s %v", status, err)
	}
	got, err := f.plane.DirectFiles.Replace(f.ctx, in)
	if err != nil || got.Digest == "" || calls != 1 {
		t.Fatalf("journal proof: %+v %v effects=%d", got, err, calls)
	}
	if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&status); err != nil || status != "COMPLETED" {
		t.Fatalf("receipt %s %v", status, err)
	}
	if err := f.plane.DB.SQL().QueryRow(grantSQL, state.WorkflowID).Scan(&afterGrant); err != nil || afterGrant != beforeGrant {
		t.Fatalf("proof refreshed authority: before=%s after=%s err=%v", beforeGrant, afterGrant, err)
	}
	if canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state); err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("proved historical effect still blocked cancellation: %+v %v", canceled, err)
	}
}

func TestDirectFileWorkerClaimAndLegacyMutationSerialize(t *testing.T) {
	f, current, state, claim := directMutationWorkerFixture(t, false)
	in := directInput(f, state, current.RepositoryTargets[0])
	in.Path = "new.txt"
	in.RequestID = "legacy-write"
	in.Content = "legacy work before enrollment\n"
	unknown := in
	unknown.WorkerClaim = &claim.WorkerClaimIdentity
	if _, err := f.plane.DirectFiles.Create(f.ctx, unknown); err == nil {
		t.Fatal("supplied token invented worker enrollment")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var claimErr, writeErr error
	go func() { defer wg.Done(); <-start; _, claimErr = f.facade.ClaimFileImplementationWorker(f.ctx, claim) }()
	go func() { defer wg.Done(); <-start; _, writeErr = f.plane.DirectFiles.Create(f.ctx, in) }()
	close(start)
	wg.Wait()
	if (claimErr == nil) == (writeErr == nil) {
		t.Fatalf("exactly one authority path must win: claim=%v write=%v", claimErr, writeErr)
	}
	var mutations int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=?`, state.WorkflowID).Scan(&mutations); err != nil {
		t.Fatal(err)
	}
	if claimErr == nil && mutations != 0 || writeErr == nil && mutations != 1 {
		t.Fatalf("claim=%v write=%v mutation count=%d", claimErr, writeErr, mutations)
	}
}
