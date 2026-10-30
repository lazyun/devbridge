package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type normalOnlyAdapter struct{ DeferredAdapter }

func (normalOnlyAdapter) Name() string { return "normal-test" }

func (normalOnlyAdapter) NormalExecution(_ context.Context, request GateRequest) (NormalObservation, error) {
	path := filepath.Join(request.WorktreeRoot, "run-1")
	if err := os.MkdirAll(request.WorktreeRoot, 0o700); err != nil {
		return NormalObservation{}, err
	}
	command := exec.Command("git", "worktree", "add", "-b", "real-e2e/fake-run-1", path, request.BaseCommit)
	command.Dir = request.SourceRepo
	if output, err := command.CombinedOutput(); err != nil {
		return NormalObservation{}, &commandError{output: string(output), err: err}
	}
	if err := os.WriteFile(filepath.Join(path, request.ExpectedPath), []byte(request.ExpectedContent), 0o600); err != nil {
		return NormalObservation{}, err
	}
	diffCommand := exec.Command("git", "diff", "--no-ext-diff", "--binary", request.BaseCommit, "--")
	diffCommand.Dir = path
	diff, err := diffCommand.Output()
	if err != nil {
		return NormalObservation{}, err
	}
	return NormalObservation{
		RunID: "run-1", ThreadID: "thread-1", WorktreePath: path,
		Timeline: []Checkpoint{CheckpointCodexCompleted, CheckpointEvidenceCollecting, CheckpointRunSucceeded}, AgentClaim: "done",
		BaseCommit: request.BaseCommit, HeadCommit: request.BaseCommit, Dirty: true,
		ChangedFiles: []string{request.ExpectedPath}, Diff: string(diff), DiffArtifactID: "artifact-diff-1", TokenStatus: "UNAVAILABLE",
	}, nil
}

type commandError struct {
	output string
	err    error
}

func (e *commandError) Error() string { return e.err.Error() + ": " + e.output }

type claimOnlyAdapter struct{ normalOnlyAdapter }

func (claimOnlyAdapter) NormalExecution(_ context.Context, request GateRequest) (NormalObservation, error) {
	path := filepath.Join(request.WorktreeRoot, "claim-only")
	if err := os.MkdirAll(request.WorktreeRoot, 0o700); err != nil {
		return NormalObservation{}, err
	}
	command := exec.Command("git", "worktree", "add", "-b", "real-e2e/claim-only", path, request.BaseCommit)
	command.Dir = request.SourceRepo
	if output, err := command.CombinedOutput(); err != nil {
		return NormalObservation{}, &commandError{output: string(output), err: err}
	}
	return NormalObservation{RunID: "run-claim", WorktreePath: path, Timeline: []Checkpoint{CheckpointCodexCompleted, CheckpointEvidenceCollecting, CheckpointRunSucceeded}, AgentClaim: "I changed tracked.txt"}, nil
}

func boolPointer(value bool) *bool { return &value }

func TestSuitePassesNormalOnlyAfterIndependentEvidenceAndBlocksMissingCapabilities(t *testing.T) {
	root := t.TempDir()
	adapter := normalOnlyAdapter{DeferredAdapter{Reasons: map[GateID]string{
		GateRuntimeApproval:      "approval unavailable",
		GateActiveInterruption:   "service unavailable",
		GateApprovalInterruption: "approval recovery unavailable",
	}}}
	results, artifactRoot, err := (Suite{Adapter: adapter, Root: root, Keep: true}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if artifactRoot != root {
		t.Fatalf("artifact root=%q want=%q", artifactRoot, root)
	}
	if results[0].Status != StatusPass {
		t.Fatalf("normal gate=%+v", results[0])
	}
	for _, result := range results[1:] {
		if result.Status != StatusBlocked || result.Reason == "" {
			t.Fatalf("deferred gate did not report explicit block: %+v", result)
		}
	}
	fixtureSource := results[0].Evidence["fixture_repo"]
	snapshot, err := (&Fixture{SourceRepo: fixtureSource, BaseCommit: results[0].Evidence["base_commit"]}).Inspect(context.Background(), fixtureSource)
	if err != nil || snapshot.Status != "" {
		t.Fatalf("source repository was touched: %+v err=%v", snapshot, err)
	}
}

func TestDefaultSuiteNeverReportsDeferredCapabilitiesAsPass(t *testing.T) {
	results, _, err := (Suite{Root: t.TempDir(), Keep: true}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range results {
		if result.Status != StatusBlocked || result.Reason == "" {
			t.Fatalf("gate=%+v", result)
		}
	}
}

func TestSuiteDoesNotTreatAgentClaimAsGitEvidence(t *testing.T) {
	results, _, err := (Suite{Adapter: claimOnlyAdapter{}, Root: t.TempDir(), Keep: true}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Status != StatusFail {
		t.Fatalf("claim-only gate=%+v", results[0])
	}
}
