package consoleapi

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

func TestExecutionProjectionUsesExactPhaseInsteadOfRunUpdateOrder(t *testing.T) {
	now := time.Now().UTC()
	implementation := run.Run{ID: "implementation", TaskID: "task", ProjectID: "project", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, CreatedAt: now, UpdatedAt: now.Add(time.Hour)}
	verification := run.Run{ID: "verification", TaskID: "task", ProjectID: "project", ExecutionProposalID: "execution", Purpose: run.PurposeVerification, Status: run.StatusRunning, CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}
	proposal := executionproposal.Proposal{ID: "execution", TaskID: "task", ProjectID: "project", ImplementationRunID: implementation.ID, VerificationRunID: verification.ID, TaskDefinitionRevisionID: "revision-one", Status: executionproposal.StatusConsumed, Outcome: executionproposal.OutcomeRunning, CreatedAt: now}
	owners := map[string]productExecutionOwner{proposal.ID: {proposal: proposal, title: "Original prepared intent"}}
	reader := &Reader{}
	entries, err := reader.productTaskExecutions(context.Background(), "task", "Current task title", []run.Run{implementation, verification}, owners)
	if err != nil || len(entries) != 1 {
		t.Fatalf("Execution rows=%+v err=%v", entries, err)
	}
	got := entries[0]
	if got.run.ID != verification.ID || got.summary.State != "VERIFYING" || got.summary.ID != proposal.ID || got.summary.TaskTitle != "Original prepared intent" || got.summary.TaskDefinitionRevisionID != "revision-one" || got.summary.ProvenanceKind != "EXACT" {
		t.Fatalf("Execution phase or frozen provenance drifted: %+v", got)
	}
	proposal.VerificationRunID = "missing-phase"
	owners[proposal.ID] = productExecutionOwner{proposal: proposal}
	if _, err := reader.productTaskExecutions(context.Background(), "task", "", []run.Run{implementation, verification}, owners); err == nil {
		t.Fatal("missing exact phase silently fell back to another Run")
	}
}

func TestExecutionProjectionKeepsLegacyRowsAndAuthoritativeOutcome(t *testing.T) {
	now := time.Now().UTC()
	legacy := run.Run{ID: "legacy", TaskID: "task", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, CreatedAt: now.Add(-time.Hour)}
	current := run.Run{ID: "current", TaskID: "task", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation, Status: run.StatusSucceeded, CreatedAt: now}
	owner := executionproposal.Proposal{ID: "execution", TaskID: "task", RunID: current.ID, Outcome: executionproposal.OutcomeFailed, FailureMessage: "Verification could not start", CreatedAt: now}
	legacyReader := &Reader{}
	entries, err := legacyReader.productTaskExecutions(context.Background(), "task", "Immutable problem", []run.Run{legacy, current}, map[string]productExecutionOwner{owner.ID: {proposal: owner, title: "Frozen title"}})
	if err != nil || len(entries) != 2 || entries[0].summary.State != "FAILED" || entries[0].summary.Failure != owner.FailureMessage || entries[1].summary.ID != "" || entries[1].summary.ProvenanceKind != "LEGACY_UNKNOWN" || entries[1].run.ID != legacy.ID {
		t.Fatalf("historical or overall outcome projection=%+v err=%v", entries, err)
	}
}
