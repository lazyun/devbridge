package localmcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/run"
)

// scalarFixtureFacts reads only this isolated fixture's durable authority.
func scalarFixtureFacts(t *testing.T, f *integrationFixture, proposalID string, supplied run.Run) map[string]any {
	t.Helper()
	out := map[string]any{"proposalID": proposalID, "suppliedRunID": supplied.ID, "suppliedRuntimeEpoch": supplied.RuntimeEpoch, "currentRuntimeEpoch": f.plane.DB.RuntimeEpoch()}
	queries := map[string]string{
		"admission":              "SELECT * FROM execution_proposal_admissions WHERE proposal_id=?",
		"proposal":               "SELECT id,status,outcome,run_id FROM execution_proposals WHERE id=?",
		"plans":                  "SELECT * FROM runtime_scope_plans WHERE owner_execution_id=?",
		"preparationAuthorities": "SELECT * FROM runtime_preparation_authorities WHERE owner_id=?",
		"runs":                   "SELECT id,status,purpose,runtime_epoch,workspace_id,worktree_id,thread_id,turn_id,task_id,operation_id,execution_proposal_id FROM runs WHERE execution_proposal_id=?",
		"attempts":               "SELECT * FROM runtime_preparation_attempts WHERE run_id IN (SELECT id FROM runs WHERE execution_proposal_id=?)",
		"observations":           "SELECT * FROM runtime_workspace_observations WHERE run_id IN (SELECT id FROM runs WHERE execution_proposal_id=?)",
		"bindings":               "SELECT * FROM runtime_workset_bindings WHERE owner_id=?",
		"startAuthorities":       "SELECT * FROM runtime_start_authorities WHERE owner_id=?",
	}
	for name, query := range queries {
		rows, err := f.plane.DB.SQL().QueryContext(f.ctx, query, proposalID)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		values := []map[string]any{}
		for rows.Next() {
			items := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range items {
				pointers[i] = &items[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			value := map[string]any{}
			for i, column := range columns {
				value[column] = items[i]
			}
			values = append(values, value)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[name] = values
	}
	return out
}

func logScalarFixtureFacts(t *testing.T, f *integrationFixture, proposalID string, supplied run.Run) {
	t.Helper()
	b, err := json.Marshal(scalarFixtureFacts(t, f, proposalID, supplied))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("SCALAR_DURABLE_FACTS %s", b)
}

// newManualPreparationScalarFixture owns and joins only its private test
// reconciliation loop before creating facts. Production workers are unchanged;
// every subsequent preparation call still enters the canonical guarded store.
func newManualPreparationScalarFixture(t *testing.T) *integrationFixture {
	t.Helper()
	f := newIntegrationFixture(t)
	f.plane.Retirement.Close()
	return f
}

func localMCPScalarAdmission(t *testing.T) (*integrationFixture, string, executionproposal.Proposal) {
	t.Helper()
	f := newManualPreparationScalarFixture(t)
	sid := callToolMap(t, f.ctx, connectGPT(t, f.gptURL), "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	_, p := f.admitScalarExecution(t, sid)
	return f, sid, p
}

func TestLocalMCPScalarAdmissionHasNoRun(t *testing.T) {
	f, _, p := localMCPScalarAdmission(t)
	assertScalarLayer(t, f, p.ID, 0, 0)
	logScalarFixtureFacts(t, f, p.ID, run.Run{})
}

func TestLocalMCPScalarPreparedFixtureHasRealRun(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, p)
	logScalarFixtureFacts(t, f, p.ID, r)
	t.Run("CurrentEpochAccepted", func(t *testing.T) {
		_, replay, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(runtimeepoch.WithFence(f.ctx, r.RuntimeEpoch, r.ID), sid, p)
		if err != nil || replay.ID != r.ID || !replay.SameOwner(r) {
			t.Fatalf("current canonical replay: %+v %v", replay, err)
		}
	})
	t.Run("CreatesNoDuplicateRun", func(t *testing.T) { assertScalarLayer(t, f, p.ID, 1, 0) })
}

func TestLocalMCPScalarAuthorizedFixtureHasCurrentEpoch(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, p)
	r = authorizeScalarExecutionForLocalMCP(t, f, p, r)
	before := scalarFixtureFacts(t, f, p.ID, r)
	replay := authorizeScalarExecutionForLocalMCP(t, f, p, r)
	after := scalarFixtureFacts(t, f, p.ID, replay)
	for _, key := range []string{"runs", "observations", "bindings", "startAuthorities"} {
		t.Run("NoDuplicate-"+key, func(t *testing.T) {
			if !reflect.DeepEqual(before[key], after[key]) {
				t.Fatalf("canonical replay replaced %s", key)
			}
		})
	}
	if replay.ThreadID != "" || replay.TurnID != "" {
		t.Fatalf("authorized fixture dispatched: %+v", replay)
	}
	logScalarFixtureFacts(t, f, p.ID, replay)
}

func TestLocalMCPScalarStaleEpochRejected(t *testing.T) {
	// Obtain a retired writer's real epoch; never manufacture or patch a Run epoch.
	retired, err := sqlite.Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stale := retired.RuntimeEpoch()
	if err = retired.Close(); err != nil {
		t.Fatal(err)
	}
	f, sid, p := localMCPScalarAdmission(t)
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, p)
	if stale == "" || stale == r.RuntimeEpoch {
		t.Fatal("retired/current epochs are not distinct")
	}
	before := scalarFixtureFacts(t, f, p.ID, r)
	_, _, err = f.plane.ExecutionProposals.PrepareAdmittedExecution(runtimeepoch.WithFence(f.ctx, stale, r.ID), sid, p)
	if !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("stale epoch error=%v want RUNTIME_EPOCH_FENCED", err)
	}
	if after := scalarFixtureFacts(t, f, p.ID, r); !reflect.DeepEqual(before, after) {
		t.Fatal("stale callback changed durable facts")
	}
	// The exact same operation succeeds with the epoch from the durable Run.
	_, current, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(runtimeepoch.WithFence(f.ctx, r.RuntimeEpoch, r.ID), sid, p)
	if err != nil || !current.SameOwner(r) {
		t.Fatalf("current epoch replay: %+v %v", current, err)
	}
}

func TestLocalMCPScalarCurrentPreparedRunAvoidsRunNotActive(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	backend := &unifiedBackend{stubCodexSession: &stubCodexSession{id: fmt.Sprint(stubCodexSequence.Add(1))}, fixture: f, entered: make(chan struct{}), finish: make(chan struct{})}
	f.plane.Coordinator.Codex = backend
	p, r := prepareScalarExecutionForLocalMCP(t, f, sid, p)
	started, err := f.plane.ExecutionProposals.StartAuthorizedTurn(f.ctx, sid, p, r)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := f.plane.Supervisor.GetSession(started.Run.ID)
	if !ok {
		t.Fatal("missing runtime session")
	}
	select {
	case <-backend.entered:
	case <-session.Done():
		t.Fatal("runtime stopped before active Turn")
	}
	active := assertScalarActiveRun(t, f, p, started.Run.ID)
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("AdmissionRunIdentityRejectedForActiveOwner", func(t *testing.T) {
		before := scalarFixtureFacts(t, f, p.ID, active)
		body, _ := json.Marshal(map[string]string{"runId": "", "requestId": "stale-admission-run"})
		req := httptest.NewRequest(http.MethodPost, "/ui/api/executions/"+p.ID+"/cancel?project_id="+f.projectID, bytes.NewReader(body))
		setLocalOperatorLoopback(req)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		web.Handler().ServeHTTP(rr, req)
		var problem struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &problem); err != nil {
			t.Fatal(err)
		}
		if rr.Code != http.StatusInternalServerError || problem.Code != "RUN_NOT_ACTIVE" {
			t.Fatalf("admission identity guard: HTTP %d %s", rr.Code, rr.Body.String())
		}
		if !reflect.DeepEqual(before, scalarFixtureFacts(t, f, p.ID, active)) {
			t.Fatal("rejected cancellation changed authority")
		}
	})
	cancelHTTP(t, web.Handler(), f.projectID, p.ID, active.ID, "current-run")
	canceled, cause := session.Wait(f.ctx)
	if canceled.Status != run.StatusCanceled {
		t.Fatalf("current Run cancellation failed: %+v %v", canceled, cause)
	}
	receipt := cancelHTTP(t, web.Handler(), f.projectID, p.ID, active.ID, "current-run")
	if receipt.State != "CANCELED" || receipt.BackendDisposition != "TERMINATED" {
		t.Fatalf("cancellation receipt: %+v", receipt)
	}
}
