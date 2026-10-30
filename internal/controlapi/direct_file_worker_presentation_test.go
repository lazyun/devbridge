package controlapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
)

// Read the production rich and compact projections after a real control-plane
// restart. Synthetic projections alone cannot expose later generic overrides.
func TestDirectFileWorkerRestartProjectionPreservesUnsupportedContinuation(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		enrolled, write, lostReceipt bool
	}{{"legacy-unclaimed", false, false, false}, {"enrolled-ready", true, false, false}, {"enrolled-implementing", true, true, false}, {"enrolled-unknown-file-effect", true, true, true}} {
		enrolled := tc.enrolled
		t.Run(tc.name, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
				t.Fatal(err)
			}
			state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			in := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: state.TaskDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: state.ProtocolVersion, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "projection-claim", WorkerID: "projection-worker"}}
			if enrolled {
				if _, err = f.facade.ClaimFileImplementationWorker(f.ctx, in); err != nil {
					t.Fatal(err)
				}
			}
			if tc.write {
				op := directInput(f, state, current.RepositoryTargets[0])
				read, e := f.facade.FileRead(f.ctx, op)
				if e != nil {
					t.Fatal(e)
				}
				op.RequestID, op.ExpectedDigest, op.Content = "projection-effect", read.Digest, "Preserve this completed worker effect after restart.\n"
				op.WorkerClaim = &in.WorkerClaimIdentity
				if tc.lostReceipt && !f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { return errors.New("effect occurred; receipt lost") }) {
					t.Fatal("fault seam unavailable")
				}
				_, e = f.facade.FileReplace(f.ctx, op)
				if (e != nil) != tc.lostReceipt {
					t.Fatalf("unexpected write result: %v", e)
				}
			}

			prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "projection-other", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Another selected task", ProblemStatement: "Planning only; never borrow worker continuation"}, Variants: []c.Variant{{ID: "create", Summary: "Create unrelated planning task"}}})
			if err != nil {
				t.Fatal(err)
			}
			created, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "projection-other-confirm", ContentHash: prepared.ContentHash, VariantID: "create"})
			if err != nil || created.State != "COMPLETED" {
				t.Fatalf("other Task %+v %v", created, err)
			}
			otherID := created.Steps[0].TaskID

			if err = f.plane.Close(); err != nil {
				t.Fatal(err)
			}
			f.plane = nil
			f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
			if err != nil {
				t.Fatal(err)
			}
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := func() []any {
				t.Helper()
				var epoch, status string
				var generation, receipts int64
				if err := f.plane.DB.SQL().QueryRow(`SELECT runtime_epoch,generation,state FROM direct_file_task_authorizations WHERE workflow_id=?`, state.WorkflowID).Scan(&epoch, &generation, &status); err != nil {
					t.Fatal(err)
				}
				if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_start_receipts WHERE workflow_id=?`, state.WorkflowID).Scan(&receipts); err != nil {
					t.Fatal(err)
				}
				return []any{epoch, generation, status, receipts}
			}
			before := snapshot()
			for i := 0; i < 2; i++ {
				rich, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
				if err != nil {
					t.Fatal(err)
				}
				compact, err := f.facade.CompactWorkspaceForSession(f.ctx, f.session, f.area, f.task)
				if err != nil {
					t.Fatal(err)
				}
				if enrolled {
					want := "CANCEL_DIRECT_FILE"
					if tc.lostReceipt {
						want = "REFRESH"
					}
					if rich.NextAction.Code != want || compact.NextAction.Code != want {
						t.Fatalf("worker projection offers wrong action: rich=%+v compact=%+v", rich.NextAction, compact.NextAction)
					}
					if rich.DirectFile == nil || rich.DirectFile.State != "RECOVERY_REQUIRED" || rich.DirectFile.FailureCode != directfile.WorkerContinuationUnsupported || rich.DirectFile.Enabled {
						t.Fatalf("changed worker state %+v", rich.DirectFile)
					}
					if rich.TaskView == nil || !strings.Contains(strings.ToLower(rich.TaskView.Activity.Summary), "unsupported") {
						t.Fatalf("generic activity overrode worker limitation: %+v", rich.TaskView)
					}
				} else if rich.Guidance == nil || rich.Guidance.NextAction.Code != "RECOVER_DIRECT_FILE" {
					t.Fatalf("legacy recovery changed: %+v", rich.Guidance)
				}
				different, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, otherID)
				if err != nil {
					t.Fatal(err)
				}
				if different.NextAction.Code == "RECOVER_DIRECT_FILE" || different.NextAction.Code == "CANCEL_DIRECT_FILE" {
					t.Fatalf("other task inherited worker action %+v", different.NextAction)
				}
			}
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("projection reads changed grant or receipts")
			}
			if tc.write {
				var root string
				if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND repository_id=?`, state.WorkflowID, current.RepositoryTargets[0].RepositoryID).Scan(&root); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(filepath.Join(root, "same.txt"))
				if err != nil || string(b) != "Preserve this completed worker effect after restart.\n" {
					t.Fatalf("worker bytes changed %q %v", b, err)
				}
			}
		})
	}
}
