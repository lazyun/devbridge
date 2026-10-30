package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestCompletedTaskDeliveryReceiptPrecedesLiveAdmissionAndAcceptanceReplay(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-completed-readback")
	execution = terminalExecutionWithReport(t, f, execution)
	attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	in := wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "completed-readback-accept"}
	accepted, err := f.store.ResolveTaskAcceptance(ctx, in)
	if err != nil || accepted.CommitError != "" || accepted.Task.CommitSHA == "" {
		t.Fatalf("initial acceptance=%+v err=%v", accepted, err)
	}
	completed, lastError := deliveryReceiptState(t, f, owner.ID)
	if !completed.Valid || lastError != "" {
		t.Fatalf("receipt not complete: %+v %q", completed, lastError)
	}
	// A later writer owns the area, but cannot revoke a completed receipt.
	if _, err = NewDirectFileAuthority(f.db).Enable(ctx, directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: "later-writer"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE workspaces SET runtime_quarantined=1 WHERE id='git-workspace'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE workspace_members SET preparation='FAILED' WHERE workspace_id='git-workspace'`); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"live-authority-changed", "cold-reopen"} {
		if phase == "cold-reopen" {
			root := f.db.stateRoot
			if err = f.db.Close(); err != nil {
				t.Fatal(err)
			}
			f.db = openTestDB(t, root)
			t.Cleanup(func() { f.db.Close() })
			f.store = NewWorkflowProgressionStore(f.db)
		}
		t.Run(phase, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "git-called")
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called >> \"$DELIVERY_REPLAY_GIT_MARKER\"\nexit 97\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DELIVERY_REPLAY_GIT_MARKER", marker)
			t.Setenv("PATH", bin)
			got, err := f.store.RetryTaskCommit(ctx, f.project, owner.ID)
			if err != nil || !reflect.DeepEqual(got, accepted.Task) {
				t.Errorf("completed retry changed result: status=%s commit=%s err=%v", got.Status, got.CommitSHA, err)
			}
			replayed, err := f.store.ResolveTaskAcceptance(ctx, in)
			if err != nil || !replayed.Replay || replayed.CommitError != "" || replayed.Receipt != accepted.Receipt || !reflect.DeepEqual(replayed.Task, accepted.Task) {
				t.Errorf("acceptance replay=%v commitError=%q err=%v", replayed.Replay, replayed.CommitError, err)
			}
			if got, last := deliveryReceiptState(t, f, owner.ID); got != completed || last != lastError {
				t.Errorf("completed receipt changed: %+v %q", got, last)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Errorf("receipt replay invoked Git: %v", err)
			}
			if _, err := f.store.RetryTaskCommit(ctx, "foreign-project", owner.ID); err == nil {
				t.Error("foreign project read the completed receipt")
			}
		})
	}
}

func TestIncompleteTaskDeliveryReceiptStillRejectsDirectFileGrant(t *testing.T) {
	f, owner, execution := acceptedExecutionFixture(t, "-incomplete-direct-file")
	execution = terminalExecutionWithReport(t, f, execution)
	repo, source, _ := attachCommitWorkspace(t, f, owner.ID, execution.VerificationRunID)
	ctx := context.Background()
	if _, err := f.db.sql.Exec(`CREATE TRIGGER replay_incomplete_checkpoint BEFORE UPDATE OF commit_sha ON tasks BEGIN SELECT RAISE(ABORT,'incomplete delivery checkpoint'); END`); err != nil {
		t.Fatal(err)
	}
	out, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(execution.ID), ExecutionID: execution.ID, Option: wp.AcceptanceAccept, RequestID: "incomplete-direct-file-accept"})
	if err != nil || !strings.Contains(out.CommitError, "incomplete delivery checkpoint") || out.Task.CommitSHA != "" {
		t.Fatalf("incomplete receipt fixture: commitError=%q err=%v", out.CommitError, err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER replay_incomplete_checkpoint`); err != nil {
		t.Fatal(err)
	}
	if completed, _ := deliveryReceiptState(t, f, owner.ID); completed.Valid {
		t.Fatal("fault unexpectedly completed the receipt")
	}
	beforeSource, beforeTarget := commitTestGit(t, source, "rev-parse", "HEAD"), commitTestGit(t, repo, "rev-parse", "HEAD")
	// Normal grant admission excludes pending delivery. Inject the retained
	// in-memory owner to independently verify RetryTaskCommit's own fence when
	// the receipt is incomplete, even though its Git effect already happened.
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	directFileGrants.Lock()
	if directFileGrants.byDB[f.db] == nil {
		directFileGrants.byDB[f.db] = make(map[string]*directFileGrant)
	}
	directFileGrants.byDB[f.db][f.area] = &directFileGrant{root: root}
	directFileGrants.Unlock()
	t.Cleanup(func() { invalidateDirectFile(f.db, f.area) })
	t.Run("blocked-before-Git", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "git-called")
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf called >> \"$DELIVERY_REPLAY_GIT_MARKER\"\nexit 97\n"), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("DELIVERY_REPLAY_GIT_MARKER", marker)
		t.Setenv("PATH", bin)
		if _, err := f.store.RetryTaskCommit(ctx, f.project, owner.ID); err == nil {
			t.Fatal("incomplete delivery bypassed Direct File grant")
		} else {
			assertDirectFileCode(t, err, directfile.CapabilityActive)
		}
		if completed, _ := deliveryReceiptState(t, f, owner.ID); completed.Valid {
			t.Fatal("blocked retry completed the receipt")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("blocked retry invoked Git: %v", err)
		}
	})
	if commitTestGit(t, source, "rev-parse", "HEAD") != beforeSource || commitTestGit(t, repo, "rev-parse", "HEAD") != beforeTarget {
		t.Fatal("blocked incomplete retry changed Git")
	}
	invalidateDirectFile(f.db, f.area)
	completed, err := f.store.RetryTaskCommit(ctx, f.project, owner.ID)
	if err != nil || completed.CommitSHA != beforeSource {
		t.Fatalf("released grant did not recover exact original commit: commit=%s err=%v", completed.CommitSHA, err)
	}
}
