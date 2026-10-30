package sqlite

import (
	"context"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitPresentationReadsExistingFactsWithoutAdvancingGit(t *testing.T) {
	f, owner, e := acceptedExecutionFixture(t, "-presentation")
	e = terminalExecutionWithReport(t, f, e)
	repo, source, base := attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
	commitTestWrite(t, filepath.Join(source, "drift.txt"), "later\n")
	_, err := f.store.ResolveTaskAcceptance(context.Background(), wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), ExecutionID: e.ID, Option: wp.AcceptanceAccept, RequestID: "presentation-accept"})
	if err != nil {
		t.Fatal(err)
	}
	facts, err := f.store.CommitPresentationFacts(context.Background(), f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	receipt, ok := facts.Task(owner.ID)
	if !ok || !receipt.Pending || receipt.LastError == "" || facts.WorkspaceHead != base || facts.DevHead != base {
		t.Fatalf("%+v", facts)
	}
	if commitTestGit(t, repo, "rev-parse", "HEAD") != base || commitTestGit(t, source, "rev-parse", "HEAD") != base {
		t.Fatal("read advanced Git")
	}
	cap, err := f.store.CommitInventoryCapability(context.Background(), f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	inventoryFacts, err := f.store.CommitInventoryFacts(context.Background(), f.project, f.area)
	if err != nil {
		t.Fatal(err)
	}
	projected := presentation.Projection{Commit: &cap}
	presentation.ApplyCommitFacts(&projected, inventoryFacts)
	if cap.CanRetry || cap.Reason != "RECOVERY_REQUIRED" || presentation.CurrentAction(projected).Code == "RETRY_TASK_COMMIT" {
		t.Fatalf("inventory hid accepted-task commit failure behind a blind retry: %+v", cap)
	}
	foreign, err := f.store.CommitPresentationFacts(context.Background(), "foreign", f.area)
	if err != nil || len(foreign.Tasks) != 0 || foreign.RequestID != "" || foreign.WorkspaceHead != "" {
		t.Fatalf("foreign facts: %+v %v", foreign, err)
	}
}

func TestInventoryCommitReadsNeverSpawnGit(t *testing.T) {
	f, owner, e := acceptedExecutionFixture(t, "-inventory")
	e = terminalExecutionWithReport(t, f, e)
	attachCommitWorkspace(t, f, owner.ID, e.VerificationRunID)
	ctx := context.Background()
	// Accept through Core so the checkpoint keeps its normal immutable provenance.
	if _, err := f.store.ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session, DecisionID: wp.TaskAcceptanceDecisionID(e.ID), ExecutionID: e.ID, Option: wp.AcceptanceAccept, RequestID: "inventory-accept"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "called")
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n/usr/bin/touch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	cap, err := f.store.CommitInventoryCapability(ctx, f.project, f.area)
	if err != nil || cap.CommitStatus != "UNCOMMITTED" || !cap.CanCommit {
		t.Fatalf("%+v %v", cap, err)
	}
	facts, err := f.store.CommitInventoryFacts(ctx, f.project, f.area)
	if err != nil || facts.DevHead != "" || facts.WorkspaceHead != "" {
		t.Fatalf("%+v %v", facts, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("inventory spawned Git")
	}
}
