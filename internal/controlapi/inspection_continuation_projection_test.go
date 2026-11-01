package controlapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

type inspectionProjectionProbes struct {
	ContextProbeService
	probe contextprobe.Probe
}

func (s inspectionProjectionProbes) Get(context.Context, string) (contextprobe.Probe, error) {
	return s.probe, nil
}

type inspectionProjectionSnapshot struct {
	Execution agentexecution.Execution
	Probe     contextprobe.Probe
	Task      task.Task
	Runs      []run.Run
}

func inspectionProjectionFixture() inspectionProjectionSnapshot {
	e := agentexecution.Execution{ID: "inspection-envelope", ProjectID: "project", TaskID: "inspection-task", Kind: agentexecution.KindInspection, SemanticOwnerKind: "INSPECTION", SemanticOwnerID: "inspection", ProvenanceKind: agentexecution.ProvenanceNative}
	old := run.Run{ID: "source-run", AgentExecutionID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, OperationKind: run.OperationInspection, OperationID: e.SemanticOwnerID, ContextProbeID: e.SemanticOwnerID, Purpose: run.PurposeInspection, Status: run.StatusFailed, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0)}
	next := old
	next.ID = "continuation-run"
	next.ContextProbeID = ""
	next.Status = run.StatusRunning
	next.CreatedAt = time.Unix(3, 0)
	next.UpdatedAt = time.Unix(4, 0)
	return inspectionProjectionSnapshot{Execution: e, Probe: contextprobe.Probe{ID: e.SemanticOwnerID, ProjectID: e.ProjectID, TaskID: e.TaskID, RunID: old.ID, RecoverySuccessorRunID: next.ID, Status: contextprobe.StatusRunning}, Task: task.Task{ID: e.TaskID, ProjectID: e.ProjectID}, Runs: []run.Run{old, next}}
}
func (s inspectionProjectionSnapshot) facade() *Facade {
	runs := scopeRuns{}
	for _, r := range s.Runs {
		runs[r.ID] = r
	}
	return &Facade{deps: Dependencies{AgentExecutions: projectionExecutionStore{s.Execution.ID: s.Execution}, AISessions: scopeSessions{"session": aisession.Session{ID: "session", ProjectID: s.Execution.ProjectID}}, Tasks: scopeTasks{s.Task.ID: s.Task}, Runs: runs, ContextProbes: inspectionProjectionProbes{probe: s.Probe}}}
}

func TestInspectionContinuationProjectionSelectsExactCurrent(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status run.Status
		domain contextprobe.Status
		want   string
	}{
		{"running", run.StatusRunning, contextprobe.StatusRunning, "RUNNING"},
		{"completed", run.StatusSucceeded, contextprobe.StatusSucceeded, "COMPLETED"},
		{"failed", run.StatusInfrastructureFailed, contextprobe.StatusFailed, "FAILED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := inspectionProjectionFixture()
			snapshot.Runs[1].Status, snapshot.Probe.Status = tc.status, tc.domain
			// Reload the persisted-shaped read facts into a new Facade; no in-memory
			// prior projection or runtime epoch is allowed to supply current ownership.
			// This is a read-model restart check, not a native Core/SQLite lifecycle test.
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "read-facts.json")
			if err = os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for _, phase := range []string{"initial", "reloaded"} {
				t.Run(phase, func(t *testing.T) {
					if phase == "reloaded" {
						raw, err = os.ReadFile(path)
						if err != nil {
							t.Fatal(err)
						}
						snapshot = inspectionProjectionSnapshot{}
						if err = json.Unmarshal(raw, &snapshot); err != nil {
							t.Fatal(err)
						}
					}
					facade := snapshot.facade()
					direct, err := facade.GetAgentExecutionForSession(context.Background(), "session", snapshot.Execution.ID)
					if err != nil {
						t.Fatal(err)
					}
					listed, err := facade.ListSurfaceOperationsForSession(context.Background(), "session", "", snapshot.Task.ID, ListOptions{Limit: 10})
					if err != nil || len(listed) != 1 {
						t.Fatalf("inventory: %+v %v", listed, err)
					}
					op := p.SelectedOperationFromAgentExecution(listed[0])
					if op == nil {
						t.Fatal("missing operation")
					}
					if direct.RunID != "continuation-run" || direct.Status != tc.want || op.Target.RunID != direct.RunID || op.Status != direct.Status || len(direct.RunIDs) != 2 {
						t.Fatalf("wrong current Inspection: direct=%+v inventory=%+v", direct, op)
					}
					original, err := facade.GetRunForSession(context.Background(), "session", "source-run")
					if err != nil || original.Status != run.StatusFailed {
						t.Fatalf("source history rewritten: %+v %v", original, err)
					}
				})
			}
		})
	}
}

func TestInspectionContinuationProjectionRejectsInvalidLink(t *testing.T) {
	for _, field := range []string{"missing", "probe-id", "source-missing", "source-is-successor", "run-id", "project", "task", "feature-area", "execution", "kind", "missing-kind", "operation", "missing-operation", "purpose", "context-probe"} {
		t.Run(field, func(t *testing.T) {
			s := inspectionProjectionFixture()
			next := &s.Runs[1]
			switch field {
			case "missing":
				s.Runs = s.Runs[:1]
			case "probe-id":
				s.Probe.ID = "other-inspection"
			case "source-missing":
				s.Probe.RunID = ""
			case "source-is-successor":
				s.Probe.RunID = next.ID
			case "run-id":
				next.ID = "other-run"
			case "project":
				next.ProjectID = "other-project"
			case "task":
				next.TaskID = "other-task"
			case "feature-area":
				next.FeatureAreaID = "other-area"
			case "execution":
				next.AgentExecutionID = "other-envelope"
			case "kind":
				next.OperationKind = run.OperationReview
			case "missing-kind":
				next.OperationKind = ""
			case "operation":
				next.OperationID = "other-inspection"
			case "missing-operation":
				next.OperationID = ""
			case "purpose":
				next.Purpose = run.PurposeRecoveryStatus
			case "context-probe":
				next.ContextProbeID = s.Probe.ID
			}
			facade := s.facade()
			if out, err := facade.GetAgentExecutionForSession(context.Background(), "session", s.Execution.ID); err == nil {
				t.Fatalf("invalid %s returned projection: %+v", field, out)
			}
			if out, err := facade.ListSurfaceOperationsForSession(context.Background(), "session", "", s.Task.ID, ListOptions{Limit: 10}); err == nil {
				t.Fatalf("invalid %s listed projection: %+v", field, out)
			}
		})
	}
}

func TestInspectionProjectionWithoutReceiptDoesNotSelectNewestRun(t *testing.T) {
	s := inspectionProjectionFixture()
	s.Probe.RecoverySuccessorRunID = ""
	s.Probe.Status = contextprobe.StatusFailed
	out, err := s.facade().GetAgentExecutionForSession(context.Background(), "session", s.Execution.ID)
	if err != nil || out.RunID != "source-run" || out.Status != "FAILED" {
		t.Fatalf("unlinked newer Run acquired current ownership: %+v %v", out, err)
	}
}

type inspectionProjectionRuntimeStore struct {
	projectionExecutionStore
	runtime         map[string]agentexecution.RuntimeFacts
	cancellationRun string
}

func (s *inspectionProjectionRuntimeStore) RuntimeFacts(context.Context, string) (map[string]agentexecution.RuntimeFacts, error) {
	return s.runtime, nil
}
func (s *inspectionProjectionRuntimeStore) CancellationFacts(_ context.Context, _, runID string) (agentexecution.RuntimeFacts, error) {
	s.cancellationRun = runID
	return agentexecution.RuntimeFacts{}, nil
}

func TestInspectionContinuationProjectionUsesOnlyCurrentRuntimeFacts(t *testing.T) {
	for _, currentUnknown := range []bool{false, true} {
		s := inspectionProjectionFixture()
		s.Runs[1].Status = run.StatusSucceeded
		s.Probe.Status = contextprobe.StatusSucceeded
		store := &inspectionProjectionRuntimeStore{projectionExecutionStore: projectionExecutionStore{s.Execution.ID: s.Execution}, runtime: map[string]agentexecution.RuntimeFacts{"source-run": {BackendDisposition: "UNKNOWN", CancellationState: "OUTCOME_UNKNOWN"}}}
		if currentUnknown {
			store.runtime["continuation-run"] = agentexecution.RuntimeFacts{BackendDisposition: "UNKNOWN"}
		}
		facade := s.facade()
		facade.deps.AgentExecutions = store
		out, err := facade.GetAgentExecutionForSession(context.Background(), "session", s.Execution.ID)
		want := "COMPLETED"
		if currentUnknown {
			want = "UNKNOWN"
		}
		if err != nil || out.RunID != "continuation-run" || out.Status != want || out.RecoveryRequired != currentUnknown || out.CancellationState != "" || store.cancellationRun != "continuation-run" {
			t.Fatalf("runtime facts borrowed/hidden (currentUnknown=%v): %+v target=%s err=%v", currentUnknown, out, store.cancellationRun, err)
		}
	}
}
