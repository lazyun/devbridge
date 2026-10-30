package controlapi

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

type projectionExecutionStore map[string]agentexecution.Execution

func (s projectionExecutionStore) Get(_ context.Context, id string) (agentexecution.Execution, error) {
	v, ok := s[id]
	if !ok {
		return v, agentexecution.ErrNotFound
	}
	return v, nil
}
func (s projectionExecutionStore) ListByProject(_ context.Context, id string) ([]agentexecution.Execution, error) {
	out := []agentexecution.Execution{}
	for _, v := range s {
		if v.ProjectID == id {
			out = append(out, v)
		}
	}
	return out, nil
}

func TestLegacyUnknownInventoryDoesNotRequireFabricatedTask(t *testing.T) {
	e := agentexecution.Execution{ID: "legacy", ProjectID: "project", Kind: agentexecution.KindInspection, ProvenanceKind: agentexecution.ProvenanceLegacyUnknown}
	facade := &Facade{deps: Dependencies{AgentExecutions: projectionExecutionStore{e.ID: e}, Runs: scopeRuns{}}}
	values, err := facade.ListAgentExecutions(context.Background(), e.ProjectID)
	if err != nil || len(values) != 1 || !values[0].ReadOnly || values[0].TaskID != "" || values[0].SemanticOwnerID != "" {
		t.Fatalf("legacy not readable: %+v %v", values, err)
	}
}
func TestUniversalCancellationResolvesExactSemanticOwner(t *testing.T) {
	for _, kind := range []agentexecution.Kind{agentexecution.KindReview, agentexecution.KindDevelopment} {
		t.Run(string(kind), func(t *testing.T) {
			ownerKind := string(kind)
			if kind == agentexecution.KindDevelopment {
				ownerKind = "EXECUTION"
			}
			e := agentexecution.Execution{ID: "envelope", ProjectID: "project", TaskID: "task", FeatureAreaID: "area", Kind: kind, SemanticOwnerKind: ownerKind, SemanticOwnerID: "owner", ProvenanceKind: agentexecution.ProvenanceNative}
			if kind == agentexecution.KindDevelopment {
				e.ID = "owner"
				e.ExecutionProposalID = e.ID
			}
			r := run.Run{ID: "run", AgentExecutionID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, FeatureAreaID: e.FeatureAreaID, OperationKind: run.OperationKind(ownerKind), OperationID: e.SemanticOwnerID}
			runner := &cancellationRunnerStub{}
			facade := &Facade{deps: Dependencies{AgentExecutions: projectionExecutionStore{e.ID: e}, Runs: scopeRuns{r.ID: r}, AISessions: scopeSessions{"session": {ID: "session", ProjectID: e.ProjectID}}, Runner: runner}}
			in := executioncancel.Input{AISessionID: "session", ExecutionID: e.ID, RunID: r.ID, RequestID: "request"}
			if _, err := facade.CancelExecutionForSession(context.Background(), in); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 || runner.calls[0].OwnerType != ownerKind || runner.calls[0].OwnerID != e.SemanticOwnerID || runner.calls[0].RunID != r.ID {
				t.Fatalf("changed canonical target: %+v", runner.calls)
			}
			if kind != agentexecution.KindDevelopment && runner.calls[0].ExecutionID != "" {
				t.Fatalf("universal ID misrepresented as proposal: %+v", runner.calls)
			}
			e.ProvenanceKind = agentexecution.ProvenanceLegacyUnknown
			facade.deps.AgentExecutions = projectionExecutionStore{e.ID: e}
			if _, err := facade.CancelExecutionForSession(context.Background(), in); err == nil || len(runner.calls) != 1 {
				t.Fatal("legacy acquired authority")
			}
		})
	}
}

func TestUniversalCancellationHistoryResolvesImmutableOwner(t *testing.T) {
	e := agentexecution.Execution{ID: "review:owner", ProjectID: "project", TaskID: "task", Kind: agentexecution.KindReview, SemanticOwnerKind: "REVIEW", SemanticOwnerID: "owner", ProvenanceKind: agentexecution.ProvenanceNative}
	receipts := &cancellationReadStub{receipts: []executioncancel.Receipt{{ProjectID: e.ProjectID, OwnerType: "REVIEW", OwnerID: "owner", RunID: "run"}}}
	facade := &Facade{deps: Dependencies{AgentExecutions: projectionExecutionStore{e.ID: e}, AISessions: scopeSessions{"session": {ID: "session", ProjectID: e.ProjectID}}, ExecutionCancellations: receipts}}
	values, err := facade.ListExecutionCancellationsForSession(context.Background(), "session", e.ID)
	if err != nil || len(values) != 1 || values[0].ExecutionID != e.ID || values[0].OwnerID != "owner" || receipts.execution != "owner" {
		t.Fatalf("history confused envelope and owner: %+v %v", values, err)
	}
}
