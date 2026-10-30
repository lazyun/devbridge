package controlapi_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
)

// This is deliberately independent of the production-closure happy path. It
// checks that a started v2 grant remains a per-member capability even when a
// caller knows the other repository IDs and filesystem paths.
func TestMultiRepositoryDirectG2MemberIsolationAndCancellation(t *testing.T) {
	f, current := approveDirectFixture(t, true, true)
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %#v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.State != "ACTIVE" || len(state.RepositoryTargets) != 2 {
		t.Fatalf("active state: %#v %v", state, err)
	}

	var writable, readOnly = current.RepositoryTargets[0], current.RepositoryTargets[1]
	for _, target := range current.RepositoryTargets {
		if target.Write.Kind == "NONE" {
			readOnly = target
		} else {
			writable = target
		}
		if _, err := f.facade.FileRead(f.ctx, directInput(f, state, target)); err != nil {
			t.Fatalf("authorized member %s unreadable: %v", target.RepositoryID, err)
		}
	}
	readOnlyWrite := directInput(f, state, readOnly)
	readOnlyWrite.RequestID, readOnlyWrite.Content = "readonly-write", "forbidden"
	if _, err := f.facade.FileCreate(f.ctx, readOnlyWrite); err == nil {
		t.Fatal("read-only member accepted mutation")
	}
	third := directInput(f, state, writable)
	third.RepositoryID, third.MemberID, third.MemberVersion = f.repos["backend"], "foreign-member", 1
	if _, err := f.facade.FileRead(f.ctx, third); err == nil {
		t.Fatal("unselected repository accepted read")
	}
	expanded, err := f.plane.Workspaces.EnsureForFeatureArea(f.ctx, f.project, f.repos["backend"], f.area, f.heads["backend"])
	if err != nil {
		t.Fatal(err)
	}
	added, err := expanded.MemberForRepository(f.repos["backend"])
	if err != nil {
		t.Fatal(err)
	}
	third.MemberID, third.MemberVersion = added.ID, added.Version
	if _, err = f.facade.FileRead(f.ctx, third); err == nil {
		t.Fatal("new actual workspace member expanded existing grant")
	}
	wrong := directInput(f, state, writable)
	wrong.MemberVersion++
	if _, err = f.facade.FileRead(f.ctx, wrong); err == nil {
		t.Fatal("wrong member version read accepted")
	}
	for _, op := range []string{"replace", "delete", "move"} {
		in := directInput(f, state, readOnly)
		read, e := f.facade.FileRead(f.ctx, in)
		if e != nil {
			t.Fatal(e)
		}
		in.RequestID = "readonly-" + op
		in.ExpectedDigest = read.Digest
		in.Content = "forbidden"
		in.Destination = "new.txt"
		switch op {
		case "replace":
			_, err = f.facade.FileReplace(f.ctx, in)
		case "delete":
			_, err = f.facade.FileDelete(f.ctx, in)
		case "move":
			_, err = f.facade.FileMove(f.ctx, in)
		}
		if err == nil {
			t.Fatalf("readonly %s accepted", op)
		}
	}
	outside := directInput(f, state, writable)
	outside.Path = "outside.txt"
	outside.RequestID = "outside-create"
	outside.Content = "forbidden"
	if _, err = f.facade.FileCreate(f.ctx, outside); err == nil {
		t.Fatal("outside PATHS create accepted")
	}
	move := directInput(f, state, writable)
	source, e := f.facade.FileRead(f.ctx, move)
	if e != nil {
		t.Fatal(e)
	}
	move.ExpectedDigest = source.Digest
	move.RequestID = "outside-move"
	move.Destination = "outside.txt"
	if _, err = f.facade.FileMove(f.ctx, move); err == nil {
		t.Fatal("outside PATHS destination accepted")
	}
	move.RequestID = "cross-move"
	move.Destination = "new.txt"
	move.DestinationRepositoryID = readOnly.RepositoryID
	move.DestinationMemberID = readOnly.MemberID
	if _, err = f.facade.FileMove(f.ctx, move); err == nil {
		t.Fatal("cross repository move accepted")
	}
	pathEscape := directInput(f, state, writable)
	pathEscape.Path = "../outside"
	if _, err := f.facade.FileRead(f.ctx, pathEscape); err == nil {
		t.Fatal("path traversal accepted")
	}

	var root string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND repository_id=?`, state.WorkflowID, writable.RepositoryID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("same.txt", filepath.Join(root, "inside-link")); err != nil {
		t.Fatal(err)
	}
	symlinkRead := directInput(f, state, writable)
	symlinkRead.Path = "inside-link"
	if _, err := f.facade.FileRead(f.ctx, symlinkRead); err == nil {
		t.Fatal("symlink member path accepted")
	}

	write := directInput(f, state, writable)
	read, err := f.facade.FileRead(f.ctx, write)
	if err != nil {
		t.Fatal(err)
	}
	write.RequestID, write.ExpectedDigest, write.Content = "exact-replay", read.Digest, "changed once\n"
	first, err := f.facade.FileReplace(f.ctx, write)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := f.facade.FileReplace(f.ctx, write); err != nil || replay.Digest != first.Digest {
		t.Fatalf("exact write replay: %#v %v", replay, err)
	}
	write.Content = "conflicting replay\n"
	if _, err := f.facade.FileReplace(f.ctx, write); err == nil {
		t.Fatal("different payload reused operation request")
	}

	if err = os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = f.facade.FileRead(f.ctx, directInput(f, state, writable)); err == nil {
		t.Fatal("replacement root accepted")
	}
	if err = os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root+"-original", root); err != nil {
		t.Fatal(err)
	}
	canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state)
	if err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("cancel: %#v %v", canceled, err)
	}
	late := directInput(f, state, writable)
	if _, err := f.facade.FileRead(f.ctx, late); err == nil {
		t.Fatal("late call after explicit cancellation accepted")
	}
}

func TestMultiRepositoryDirectG3G7RestartRecovery(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("start: %#v %v", started, err)
	}
	old, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || old.State != "ACTIVE" {
		t.Fatalf("active: %#v %v", old, err)
	}
	app := current.RepositoryTargets[0]
	in := directInput(f, old, app)
	before, err := f.facade.FileRead(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.RequestID, in.ExpectedDigest, in.Content = "restart-write", before.Digest, "preserved after restart\n"
	if _, err = f.facade.FileReplace(f.ctx, in); err != nil {
		t.Fatal(err)
	}
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || observed.State != "RECOVERY_REQUIRED" {
		t.Fatalf("post-restart: %#v %v", observed, err)
	}
	if _, err = f.facade.FileRead(f.ctx, directInput(f, old, app)); err == nil {
		t.Fatal("old epoch read remained active")
	}
	recoveryInput := old
	// Confirmation binds the observed prior epoch; authority rotates it on recovery.
	recoveryInput.RuntimeEpoch = observed.RuntimeEpoch
	recovered, err := directConfirmation(t, f, current, "RECOVER_DIRECT_FILE", recoveryInput)
	if err != nil || recovered.State != "COMPLETED" {
		t.Fatalf("recover: %#v %v", recovered, err)
	}
	next, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || next.State != "ACTIVE" || next.RuntimeEpoch == old.RuntimeEpoch || next.GrantGeneration != old.GrantGeneration+1 {
		t.Fatalf("recovered state: %#v %v", next, err)
	}
	read := directInput(f, next, app)
	got, err := f.facade.FileRead(f.ctx, read)
	if err != nil || got.Content != "preserved after restart\n" {
		t.Fatalf("preserved app bytes: %#v %v", got, err)
	}
	sdk := current.RepositoryTargets[1]
	read = directInput(f, next, sdk)
	got, err = f.facade.FileRead(f.ctx, read)
	if err != nil || got.Content == "preserved after restart\n" {
		t.Fatalf("sdk bytes changed: %#v %v", got, err)
	}
	if _, err = f.facade.FileRead(f.ctx, directInput(f, old, app)); err == nil {
		t.Fatal("old grant generation resumed")
	}
	canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", next)
	if err != nil || canceled.State != "COMPLETED" {
		t.Fatalf("cancel: %#v %v", canceled, err)
	}
	if _, err = f.facade.FileRead(f.ctx, directInput(f, next, app)); err == nil {
		t.Fatal("late call after recovery cancellation accepted")
	}
}
