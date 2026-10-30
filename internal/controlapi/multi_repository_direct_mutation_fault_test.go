package controlapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
)

func reopenMutationFixture(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	if err := f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
}
func TestMultiRepositoryDirectG3FileEffectReceiptLossAndHistoricalReplay(t *testing.T) {
	for _, kind := range []string{"create", "replace", "delete", "move"} {
		t.Run(kind, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
			if err != nil || started.State != "COMPLETED" {
				t.Fatalf("start %v", err)
			}
			state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			target := current.RepositoryTargets[0]
			in := directInput(f, state, target)
			read, err := f.facade.FileRead(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			in.RequestID = "loss-" + kind
			in.ExpectedDigest = read.Digest
			in.Content = "first effect\n"
			in.Destination = "new.txt"
			if kind == "create" {
				in.Path = "new.txt"
				in.ExpectedDigest = ""
			}
			call := func(in directfile.Input) (directfile.Result, error) {
				switch kind {
				case "create":
					return f.facade.FileCreate(f.ctx, in)
				case "replace":
					return f.facade.FileReplace(f.ctx, in)
				case "delete":
					return f.facade.FileDelete(f.ctx, in)
				default:
					return f.facade.FileMove(f.ctx, in)
				}
			}
			if !f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { return errors.New("injected effect before DB receipt") }) {
				t.Fatal("fault seam unavailable")
			}
			first, err := call(in)
			if err == nil {
				t.Fatal("fault wasn't observed")
			}
			var pending int
			if e := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=? AND state IN ('PENDING','UNKNOWN')`, state.WorkflowID, in.RequestID).Scan(&pending); e != nil || pending != 1 {
				t.Fatalf("intent lost %d %v", pending, e)
			}
			reopenMutationFixture(t, f)
			recovery, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", state)
			if err != nil || recovery.State != "COMPLETED" {
				t.Fatalf("recover %v %+v", err, recovery.Steps)
			}
			next, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := call(in)
			if err != nil || !reflect.DeepEqual(first, replay) {
				t.Fatalf("proved exact historical replay first=%+v replay=%+v err=%v", first, replay, err)
			}
			later := directInput(f, next, target)
			later.RequestID = "later-" + kind
			later.Content = "later legal bytes\n"
			if kind == "create" {
				later.Path = "new.txt"
			}
			if kind == "delete" || kind == "move" {
				_, err = f.facade.FileCreate(f.ctx, later)
			} else {
				r, e := f.facade.FileRead(f.ctx, later)
				if e != nil {
					t.Fatal(e)
				}
				later.ExpectedDigest = r.Digest
				_, err = f.facade.FileReplace(f.ctx, later)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = call(in); err != nil {
				t.Fatal(err)
			}
			now, err := f.facade.FileRead(f.ctx, later)
			if err != nil || now.Content != later.Content {
				t.Fatalf("old replay destroyed later bytes %+v %v", now, err)
			}
			for _, change := range []string{"content", "digest", "epoch", "generation"} {
				conflict := in
				switch change {
				case "content":
					conflict.Content = "conflicting original payload"
				case "digest":
					conflict.ExpectedDigest = "different-precondition"
				case "epoch":
					conflict.RuntimeEpoch = next.RuntimeEpoch
				case "generation":
					conflict.GrantGeneration++
				}
				if _, err = call(conflict); err == nil {
					t.Fatalf("changed original %s accepted", change)
				}
			}
		})
	}
}
func TestMultiRepositoryDirectG3UnknownWithoutProofCannotFinalizeOrOverwrite(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	in := directInput(f, state, current.RepositoryTargets[0])
	r, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "unknown-no-proof"
	in.ExpectedDigest = r.Digest
	in.Content = "effect remains\n"
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error {
		// Simulate the earlier crash window after the rename but before a durable
		// proof journal. Only this test-owned DataRoot is touched.
		err := filepath.WalkDir(f.dataRoot, func(path string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if !d.IsDir() && filepath.Ext(path) == ".json" && filepath.Base(filepath.Dir(path)) != "" {
				if filepath.Base(filepath.Dir(path)) == "direct-file-journal" {
					return os.Remove(path)
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
		return errors.New("injected missing proof")
	})
	if _, err = f.facade.FileReplace(f.ctx, in); err == nil {
		t.Fatal("injection missing")
	}
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(nil)
	var root string
	if err = f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND repository_id=?`, state.WorkflowID, in.RepositoryID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "same.txt"), []byte("later external bytes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.FileReplace(f.ctx, in); err == nil {
		t.Fatal("unproved mutation replay accepted")
	}
	result := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "unknown-result", Summary: "must not finalize", VerificationNotes: "waived", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	if _, err = f.facade.SubmitFileImplementationResult(f.ctx, result); err == nil {
		t.Fatal("unknown operation finalized")
	}
	if _, err = f.facade.DirectFileDisable(f.ctx, directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: state.WorkflowID}); err == nil {
		t.Fatal("generic disable discarded unresolved v2 authority")
	}
	if _, err = directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state); err == nil {
		t.Fatal("unknown operation cancellation released evidence")
	}
	bytes, err := os.ReadFile(filepath.Join(root, "same.txt"))
	if err != nil || string(bytes) != "later external bytes\n" {
		t.Fatalf("unproved replay overwrote later bytes %q %v", bytes, err)
	}
}
func TestMultiRepositoryDirectG7ResultWaitsForMutationBoundary(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	if _, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil {
		t.Fatal(err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	in := directInput(f, state, current.RepositoryTargets[0])
	r, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID = "blocked-effect"
	in.ExpectedDigest = r.Digest
	in.Content = "ordered effect\n"
	entered, release := make(chan struct{}), make(chan struct{})
	f.plane.DirectFiles.SetAfterFileEffectHookForTest(func() error { close(entered); <-release; return nil })
	writes := make(chan error, 1)
	go func() { _, e := f.facade.FileReplace(f.ctx, in); writes <- e }()
	<-entered
	results := make(chan error, 1)
	go func() {
		_, e := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "ordered-result", Summary: "result after effect", VerificationNotes: "waived", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
		results <- e
	}()
	select {
	case e := <-results:
		close(release)
		t.Fatalf("result crossed active mutation boundary %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if e := <-writes; e != nil {
		t.Fatal(e)
	}
	if e := <-results; e != nil {
		t.Fatal(e)
	}
}
