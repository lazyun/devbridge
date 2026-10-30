package controlapi_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// These are enrolled workers, not the legacy no-token ordering fixture. The
// post-effect hook holds the actual authority mutex while the journal exists
// but the mutation receipt is still PENDING.
func TestDirectFileWorkerMutationSerializesResultAndCancellation(t *testing.T) {
	for _, terminal := range []string{"result", "cancel"} {
		t.Run(terminal, func(t *testing.T) {
			f, current, state, claim := directMutationWorkerFixture(t, true)
			in := directInput(f, state, current.RepositoryTargets[0])
			before, err := f.facade.FileRead(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			in.RequestID = "ordered-effect"
			in.ExpectedDigest = before.Digest
			in.Content = "ordered worker effect\n"
			in.WorkerClaim = &claim.WorkerClaimIdentity
			resultIn := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "ordered-result", Summary: "Actual file change after its durable effect", VerificationNotes: "Explicit fixture waiver; no commands claimed", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration, WorkerClaim: &claim.WorkerClaimIdentity}
			var cancel c.Intent
			if terminal == "cancel" {
				// Establish IMPLEMENTING before preparing exact cancellation.
				// Otherwise the first mutation legitimately invalidates an
				// IMPLEMENTATION_READY task-action snapshot during this race.
				// We must not weaken that confirmation fence to test the mutex.
				prior := in
				prior.RequestID = "establish-implementing"
				prior.Content = "prior authorized worker step\n"
				primed, primeErr := f.plane.DirectFiles.Replace(f.ctx, prior)
				if primeErr != nil {
					t.Fatal(primeErr)
				}
				in.ExpectedDigest = primed.Digest
				current, err = f.facade.GetTask(f.ctx, f.task)
				if err != nil || current.Phase != "IMPLEMENTING" {
					t.Fatalf("stable cancellation phase: %s %v", current.Phase, err)
				}
				cancel, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-worker-cancel-race", Purpose: c.Execution, Action: "CANCEL_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "CANCEL_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, Variants: []c.Variant{{ID: "exact", Summary: "Explicit cancellation after ordered worker effect", ImplementationMode: current.WorkSpec.ImplementationMode, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { close(entered); <-release; return nil })
			mutationDone := make(chan error, 1)
			go func() { _, e := f.plane.DirectFiles.Replace(f.ctx, in); mutationDone <- e }()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				t.Fatal("mutation did not reach post-effect boundary")
			}
			var receiptState string
			if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&receiptState); err != nil || receiptState != "PENDING" {
				t.Fatalf("expected pending effect receipt: %s %v", receiptState, err)
			}
			terminalStarted, terminalDone := make(chan struct{}), make(chan error, 1)
			go func() {
				close(terminalStarted)
				if terminal == "result" {
					_, e := f.facade.SubmitFileImplementationResult(f.ctx, resultIn)
					terminalDone <- e
					return
				}
				got, e := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: cancel.IntentID, Version: cancel.Version, Purpose: cancel.Purpose, Action: cancel.Action, RequestID: "confirm-worker-cancel-race", ContentHash: cancel.ContentHash, VariantID: "exact"})
				if e == nil && got.State != "COMPLETED" {
					e = fmt.Errorf("cancellation did not complete: %s", got.State)
				}
				terminalDone <- e
			}()
			<-terminalStarted
			select {
			case e := <-terminalDone:
				t.Fatalf("%s crossed active mutation mutex: %v", terminal, e)
			case <-time.After(50 * time.Millisecond):
			}
			var grantState string
			var results int
			if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&grantState); err != nil || grantState != "ACTIVE" {
				t.Fatalf("grant closed before effect receipt: %s %v", grantState, err)
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_implementation_results WHERE workflow_id=?`, state.WorkflowID).Scan(&results); err != nil || results != 0 {
				t.Fatalf("result preceded effect: %d %v", results, err)
			}
			unblock()
			select {
			case e := <-mutationDone:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("mutation did not finish")
			}
			select {
			case e := <-terminalDone:
				if e != nil {
					t.Fatalf("%s: %v", terminal, e)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("%s did not finish", terminal)
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, state.WorkflowID, in.RequestID).Scan(&receiptState); err != nil || receiptState != "COMPLETED" {
				t.Fatalf("effect proof: %s %v", receiptState, err)
			}
			var root string
			if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, state.WorkflowID, in.MemberID).Scan(&root); err != nil {
				t.Fatal(err)
			}
			actual, err := os.ReadFile(filepath.Join(root, in.Path))
			if err != nil || string(actual) != in.Content {
				t.Fatalf("terminal changed actual effect: %q %v", actual, err)
			}
			late := in
			late.Path = "new.txt"
			late.ExpectedDigest = ""
			late.RequestID = "late-worker-mutation"
			if _, err := f.plane.DirectFiles.Create(f.ctx, late); err == nil {
				t.Fatal("new claimed effect after terminal authority")
			}
			resultIn.RequestID = "late-worker-result"
			if _, err := f.facade.SubmitFileImplementationResult(f.ctx, resultIn); err == nil {
				t.Fatal("new claimed result after terminal authority")
			}
		})
	}
}
