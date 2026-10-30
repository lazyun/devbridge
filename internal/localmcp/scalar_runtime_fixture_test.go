package localmcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// Layer B: canonical Run creation, without workspace authority or dispatch.
func prepareScalarExecutionForLocalMCP(t *testing.T, f *integrationFixture, sid string, admitted executionproposal.Proposal) (executionproposal.Proposal, run.Run) {
	t.Helper()
	p, r, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, admitted)
	if err != nil {
		t.Fatal(err)
	}
	assertScalarRunIdentity(t, f, p, r)
	if r.Status != run.StatusCreated {
		t.Fatalf("prepared Run status=%s", r.Status)
	}
	assertScalarLayer(t, f, p.ID, 1, 0)
	return p, r
}

// Layer C: the existing Coordinator owns all workspace and authority writes.
func authorizeScalarExecutionForLocalMCP(t *testing.T, f *integrationFixture, p executionproposal.Proposal, r run.Run) run.Run {
	t.Helper()
	prepared, err := f.plane.Coordinator.PrepareReservedExecution(f.ctx, r.ID, execution.Instruction{CodexInstruction: p.WorkSpec.CodexInstruction, ExecutionScope: p.WorkSpec.ExecutionScope}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertScalarRunIdentity(t, f, p, prepared.Run)
	if prepared.Run.Status != run.StatusPreparing {
		t.Fatalf("authorized status=%s", prepared.Run.Status)
	}
	if err := f.plane.Runs.ValidatePreparedStart(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	assertScalarLayer(t, f, p.ID, 1, 1)
	return prepared.Run
}

func assertScalarRunIdentity(t *testing.T, f *integrationFixture, p executionproposal.Proposal, r run.Run) {
	t.Helper()
	durable, err := f.plane.Runs.Get(f.ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "" || !r.SameOwner(durable) || durable.Purpose != run.PurposeImplementation || durable.ExecutionProposalID != p.ID || durable.OperationID != p.ID || durable.OperationKind != run.OperationKind("EXECUTION") || durable.TaskID != p.TaskID || durable.ProjectID != p.ProjectID || durable.FeatureAreaID == "" || durable.RuntimeEpoch == "" || durable.RuntimeEpoch != f.plane.DB.RuntimeEpoch() {
		t.Fatalf("invalid canonical Run identity: %+v", durable)
	}
}

func assertScalarActiveRun(t *testing.T, f *integrationFixture, p executionproposal.Proposal, id string) run.Run {
	t.Helper()
	r, err := f.plane.Runs.Get(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	assertScalarRunIdentity(t, f, p, r)
	// This fixture waits for its backend's accepted Turn, not an invented status.
	if r.Status != run.StatusRunning || r.ThreadID == "" || r.TurnID == "" {
		t.Fatalf("Run is not active: %+v", r)
	}
	assertScalarLayer(t, f, p.ID, 1, 1)
	return r
}

func assertScalarLayer(t *testing.T, f *integrationFixture, proposalID string, runs, authority int) {
	t.Helper()
	facts := scalarFixtureFacts(t, f, proposalID, run.Run{})
	for key, want := range map[string]int{"admission": 1, "plans": 1, "preparationAuthorities": 1, "runs": runs, "attempts": runs, "observations": authority, "bindings": authority, "startAuthorities": authority} {
		if got := len(facts[key].([]map[string]any)); got != want {
			t.Fatalf("%s=%d want %d; %+v", key, got, want, facts)
		}
	}
	if runs == 1 {
		r := facts["runs"].([]map[string]any)[0]
		if authority == 0 && (r["workspace_id"] != nil && r["workspace_id"] != "") {
			t.Fatalf("prepared fixture has workspace: %+v", r)
		}
		if authority == 1 {
			b := facts["bindings"].([]map[string]any)[0]
			a := facts["startAuthorities"].([]map[string]any)[0]
			if b["run_id"] != r["id"] || a["run_id"] != r["id"] || b["runtime_epoch"] != r["runtime_epoch"] || a["runtime_epoch"] != r["runtime_epoch"] || b["start_authority_id"] != a["authority_id"] || b["workset_content_hash"] != a["workset_content_hash"] || r["runtime_epoch"] != f.plane.DB.RuntimeEpoch() {
				t.Fatalf("authority identity mismatch: %+v", facts)
			}
			durable, err := f.plane.Runs.Get(f.ctx, r["id"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if durable.RuntimeWorkset == nil || durable.WorkspaceID == "" || len(durable.RuntimeWorkset.Members) != 1 {
				t.Fatalf("missing managed Workset: %+v", durable)
			}
			member := durable.RuntimeWorkset.Members[0]
			observation := facts["observations"].([]map[string]any)[0]
			if member.WorktreeID == "" || observation["worktree_id"] != member.WorktreeID || observation["workspace_id"] != durable.WorkspaceID {
				t.Fatal("managed Worktree does not match the observation")
			}
			if _, err := f.plane.Worktrees.Get(f.ctx, member.WorktreeID); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// The only fake is the backend protocol. Production owns Run/epoch/state,
// verification reservation, reports, completion and the dispatch receipt.
type scalarCompletionBackend struct {
	release chan struct{}
	entered chan struct{}
	spec    task.VerificationSpec
	next    atomic.Int32
}
type scalarCompletionSession struct {
	*stubCodexSession
	backend      *scalarCompletionBackend
	verification bool
}

func (b *scalarCompletionBackend) Start(ctx context.Context, _ codexconfig.Config) (execution.CodexSession, error) {
	if fence, ok := runtimeepoch.FromContext(ctx); !ok || fence.Epoch == "" || fence.RunID == "" {
		return nil, fmt.Errorf("backend lacks durable fence")
	}
	return &scalarCompletionSession{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, backend: b, verification: b.next.Add(1) > 1}, nil
}

func (s *scalarCompletionSession) StartTurnWithStarted(ctx context.Context, thread, prompt string, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.stubCodexSession.StartTurn(ctx, thread, prompt)
	if err != nil {
		return result, err
	}
	if err = started(result.TurnID); err != nil {
		return result, err
	}
	if !s.verification {
		close(s.backend.entered)
		select {
		case <-s.backend.release:
		case <-ctx.Done():
			return result, ctx.Err()
		}
		result.Message = "Fixture implementation completed."
	} else {
		result.Message = "Fixture verification completed."
		zero := 0
		for _, check := range s.backend.spec.Checks {
			if check.Command != "" {
				result.Commands = append(result.Commands, execution.ObservedCommand{Command: check.Command, ExitCode: &zero, Output: "fixture check passed"})
			}
		}
	}
	return result, nil
}
func (s *scalarCompletionSession) StartTurnWithSchemaAndStarted(ctx context.Context, thread, prompt string, schema json.RawMessage, started func(string) error) (execution.CodexTurnResult, error) {
	result, err := s.stubCodexSession.StartTurn(ctx, thread, prompt)
	if err != nil {
		return result, err
	}
	if err = started(result.TurnID); err != nil {
		return result, err
	}
	if string(schema) == execution.ImplementationReportJSONSchema {
		close(s.backend.entered)
		select {
		case <-s.backend.release:
		case <-ctx.Done():
			return result, ctx.Err()
		}
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: "fixture implementation completed"})
		result.Message = string(raw)
	} else if string(schema) == execution.VerificationReportV2JSONSchema {
		items := func(n int) []map[string]any {
			v := make([]map[string]any, n)
			for i := range v {
				v[i] = map[string]any{"index": i, "status": "PASSED", "summary": "fixture", "evidence": "", "reason": ""}
			}
			return v
		}
		raw, _ := json.Marshal(map[string]any{"version": 2, "summary": "fixture verification", "checks": items(len(s.backend.spec.Checks)), "acceptanceCriteria": items(len(s.backend.spec.AcceptanceCriteria))})
		result.Message = string(raw)
	} else {
		return result, fmt.Errorf("unexpected scalar fixture report schema")
	}
	return result, nil
}

func completeScalarExecutionForLocalMCP(t *testing.T, f *integrationFixture, sid string, admitted executionproposal.Proposal) executionproposal.Proposal {
	t.Helper()
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, admitted)
	// StartAuthorizedTurn claims the CREATED handoff before its Coordinator
	// performs layer C. A separately PREPARING fixture is not that handoff.
	backend := &scalarCompletionBackend{release: make(chan struct{}), entered: make(chan struct{}), spec: p.VerificationSpec}
	f.plane.Coordinator.Codex = backend
	started, err := f.plane.ExecutionProposals.StartAuthorizedTurn(f.ctx, sid, p, r)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := f.plane.Supervisor.GetSession(started.Run.ID)
	if !ok {
		t.Fatal("missing production supervisor session")
	}
	select {
	case <-backend.entered:
	case <-session.Done():
		result, cause := session.Wait(f.ctx)
		t.Fatalf("runtime stopped before accepted Turn: %+v %v", result, cause)
	}
	assertScalarActiveRun(t, f, p, started.Run.ID)
	close(backend.release)
	verified, err := session.Wait(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	verified, err = f.plane.Runs.Get(f.ctx, verified.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != run.StatusSucceeded || verified.VerificationReport != nil || verified.AnalysisState != "REQUIRED" {
		t.Fatalf("native result did not wait for GPT: %+v", verified)
	}
	submitLocalFixtureAnalysis(t, f, sid, verified.ID)
	verified, err = f.plane.Runs.Get(f.ctx, verified.ID)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Status != run.StatusSucceeded || verified.Purpose != run.PurposeVerification || verified.RuntimeEpoch != f.plane.DB.RuntimeEpoch() || verified.VerificationReport == nil {
		t.Fatalf("canonical verification incomplete: %+v", verified)
	}
	p, err = sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Outcome != executionproposal.OutcomeSucceeded {
		t.Fatalf("execution did not succeed: %+v", p)
	}
	return p
}
