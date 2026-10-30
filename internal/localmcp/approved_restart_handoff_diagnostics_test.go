package localmcp_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func readCompleteHandoffEntities(t *testing.T, f *integrationFixture, tx *sql.Tx) map[string]any {
	t.Helper()
	out := map[string]any{}
	targets := []struct {
		table, where string
		args         []any
	}{}
	for _, row := range evidenceQuery(t, f, tx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name") {
		targets = append(targets, struct {
			table, where string
			args         []any
		}{row["name"].(string), "1=1", nil})
	}
	for _, target := range targets {
		entity := target.table
		if target.table == "successorExecutionCandidates" {
			target.table = "execution_proposals"
		}
		name := evidenceIdent(target.table)
		info := evidenceQuery(t, f, tx, "PRAGMA table_info("+name+")")
		if len(info) == 0 {
			t.Fatalf("missing schema %s", target.table)
		}
		indexes := evidenceQuery(t, f, tx, "PRAGMA index_list("+name+")")
		indexInfo := map[string]any{}
		for _, idx := range indexes {
			n := idx["name"].(string)
			indexInfo[n] = evidenceQuery(t, f, tx, "PRAGMA index_info("+evidenceIdent(n)+")")
		}
		schema := map[string]any{"table": target.table, "tableSQL": evidenceQuery(t, f, tx, "SELECT sql FROM sqlite_master WHERE type='table' AND name=?", target.table), "columns": info, "foreignKeys": evidenceQuery(t, f, tx, "PRAGMA foreign_key_list("+name+")"), "indexes": indexes, "indexInfo": indexInfo, "triggers": evidenceQuery(t, f, tx, "SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? ORDER BY name", target.table)}
		schemaBytes := evidenceCanonicalBytes(t, schema)
		pk := []map[string]any{}
		for _, col := range info {
			if col["pk"].(int64) > 0 {
				pk = append(pk, col)
			}
		}
		sort.Slice(pk, func(i, j int) bool { return pk[i]["pk"].(int64) < pk[j]["pk"].(int64) })
		if len(pk) == 0 {
			// Tables without explicit primary keys retain deterministic rowid order.
		}
		order := []string{}
		types := []string{}
		for _, col := range pk {
			order = append(order, evidenceIdent(col["name"].(string)))
		}
		for _, col := range info {
			types = append(types, "typeof("+evidenceIdent(col["name"].(string))+")")
		}
		if len(order) == 0 {
			order = append(order, "rowid")
		}
		query := "SELECT *," + strings.Join(types, ",") + " FROM " + name + " WHERE " + target.where + " ORDER BY " + strings.Join(order, ",")
		rows, err := tx.QueryContext(f.ctx, query, target.args...)
		if err != nil {
			t.Fatal(err)
		}
		full := []map[string]any{}
		for rows.Next() {
			values := make([]any, len(info)*2)
			ptr := make([]any, len(values))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err = rows.Scan(ptr...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			columns := []map[string]any{}
			primary := map[string]any{}
			for i, col := range info {
				value := values[i]
				typ := strings.ToUpper(values[len(info)+i].(string))
				if typ == "BLOB" {
					b, ok := value.([]byte)
					if !ok {
						rows.Close()
						t.Fatalf("invalid blob %T", value)
					}
					value = hex.EncodeToString(b)
				} else if b, ok := value.([]byte); ok {
					value = string(b)
				}
				columns = append(columns, map[string]any{"cid": col["cid"], "name": col["name"], "sqliteType": col["type"], "valueType": typ, "value": value})
				if col["pk"].(int64) > 0 {
					primary[col["name"].(string)] = value
				}
			}
			full = append(full, map[string]any{"table": target.table, "primaryKey": primary, "columns": columns})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		rowBytes := evidenceCanonicalBytes(t, full)
		out[entity] = map[string]any{"schema": schema, "schemaHash": fmt.Sprintf("%x", sha256.Sum256(schemaBytes)), "query": query, "queryArgs": target.args, "present": len(full) > 0, "queryMatchedRows": full, "rowSetSha256": fmt.Sprintf("%x", sha256.Sum256(rowBytes))}
	}
	return out
}

// This observer reads every table of the isolated fixture database in one snapshot.
// No production fact is backfilled, normalized, or mutated by evidence collection.
func captureApprovedHandoff(t *testing.T, f *integrationFixture, checkpoint string) map[string]any {
	t.Helper()
	tx, err := f.plane.DB.SQL().BeginTx(f.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	facts := readCompleteHandoffEntities(t, f, tx)
	recordRestartFacts(t, checkpoint, map[string]any{"runtimeEpoch": f.plane.DB.RuntimeEpoch(), "tables": facts})
	return facts
}

func approvedRestartFixture(t *testing.T) (*integrationFixture, string, string) {
	t.Helper()
	f := newIntegrationFixture(t)
	backend := &closureFactory{entered: make(chan *closureSession, 2), message: reviewApprovalProposal(t)}
	f.plane.Coordinator.Codex = backend
	gpt := connectGPT(t, f.gptURL)
	sid := callToolMap(t, f.ctx, gpt, "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, gpt, sid, "Handoff durable restart witness")
	callToolMap(t, f.ctx, gpt, "task_review_start", closureTaskReviewArgs(t, f, sid, area))
	awaitClosure(t, backend).finish()
	g := awaitApprovalState(t, f, sid, area, "REVIEW_APPROVAL_PENDING")
	review, err := f.plane.Reviews.Get(f.ctx, g.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := review.RuntimeConfig
	out, err := f.plane.WorkflowProgression.Resolve(f.ctx, wp.ResolveInput{AISessionID: sid, TaskID: g.TaskID, DecisionID: g.DecisionID, Option: "APPROVE", RequestID: "approve-before-restart", BackendSelection: review.BackendSelection, NextRunRuntimeConfig: &runtime})
	if err != nil || out.NextAction.Code != "START_EXECUTION" {
		t.Fatalf("approval: %+v %v", out, err)
	}
	f.plane.Retirement.Close()
	beforeClose := captureApprovedHandoff(t, f, "before-close")
	root := f.plane.DataPaths.Root
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := controlplane.Open(f.ctx, controlplane.Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	// Join only this fixture's continuation before testing a foreground capability.
	reopened.Retirement.Close()
	reopened.Coordinator.Codex = unavailableFixtureBackend{}
	f.plane = reopened
	f.control, err = controlapi.NewFromControlPlane(reopened)
	if err != nil {
		t.Fatal(err)
	}
	afterOpen := captureApprovedHandoff(t, f, "after-open")
	for _, table := range []string{"runtime_handoffs", "tasks", "execution_proposals"} {
		if string(evidenceCanonicalBytes(t, beforeClose[table])) != string(evidenceCanonicalBytes(t, afterOpen[table])) {
			t.Fatalf("restart changed approved owner table %s", table)
		}
	}
	return f, sid, g.TaskID
}

// The barrier follows the real store's committed CAS; it never writes rows itself.
// Only the winning dispatcher pauses. A losing caller reads the real PREPARING row.
type committedHandoffBarrier struct {
	execution.RuntimeHandoffStore
	entered chan executionproposal.Handoff
	release chan struct{}
}

func (b *committedHandoffBarrier) ClaimHandoff(ctx context.Context, project, runID string, now time.Time) (executionproposal.Handoff, bool, error) {
	h, won, err := b.RuntimeHandoffStore.ClaimHandoff(ctx, project, runID, now)
	if won && err == nil {
		b.entered <- h
		select {
		case <-b.release:
		case <-ctx.Done():
			return h, won, ctx.Err()
		}
	}
	return h, won, err
}

func TestApprovedTaskRestartExplicitStartRejectsRealHandoffInProgress(t *testing.T) {
	f, sid, taskID := approvedRestartFixture(t)
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	proposal, reserved, err := store.ReserveTaskExecution(f.ctx, sid, taskID)
	if err != nil || reserved.ID != "" {
		t.Fatalf("admission: %+v %+v %v", proposal, reserved, err)
	}
	barrier := &committedHandoffBarrier{RuntimeHandoffStore: f.plane.Coordinator.Handoffs, entered: make(chan executionproposal.Handoff, 1), release: make(chan struct{})}
	f.plane.Coordinator.Handoffs = barrier
	done := make(chan struct{})
	go func() { defer close(done); f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx) }()
	h := <-barrier.entered
	defer func() { close(barrier.release); <-done }()
	if h.State != executionproposal.HandoffPreparing || h.Code != "HANDOFF_IN_PROGRESS" || h.ExecutionID != proposal.ID || h.RuntimeEpoch != f.plane.DB.RuntimeEpoch() {
		t.Fatalf("current handoff: %+v", h)
	}
	before := captureApprovedHandoff(t, f, "true-in-progress-before-start")
	result, err := f.control.StartTaskExecution(f.ctx, sid, taskID)
	after := captureApprovedHandoff(t, f, "true-in-progress-after-start")
	var handoffErr *executionproposal.HandoffError
	if !errors.As(err, &handoffErr) || handoffErr.Handoff.Code != "HANDOFF_IN_PROGRESS" || result.Run.ID != h.RunID {
		t.Fatalf("real in-progress guard: %+v %v", result, err)
	}
	if string(evidenceCanonicalBytes(t, before)) != string(evidenceCanonicalBytes(t, after)) {
		t.Fatal("in-progress public replay changed durable rows")
	}
}

func approvedNoStartPrecondition(t *testing.T, f *integrationFixture, taskID string) {
	t.Helper()
	queries := []string{
		"SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'",
		"SELECT count(*) FROM runtime_handoffs WHERE run_id IN (SELECT id FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION')",
		"SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id IN (SELECT plan_id FROM runtime_scope_plans WHERE owner_id IN (SELECT id FROM execution_proposals WHERE task_id=?))",
		"SELECT count(*) FROM runtime_start_authorities WHERE owner_id IN (SELECT id FROM execution_proposals WHERE task_id=?)",
		"SELECT count(*) FROM execution_start_effects WHERE task_id=? AND effect='START_AUTHORIZED'",
	}
	for _, q := range queries {
		var n int
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, q, taskID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("approved precondition %s: count=%d error=%v", q, n, err)
		}
	}
}
func TestApprovedTaskRestartExplicitStartPreconditionHasExpectedHandoffState(t *testing.T) {
	f, _, id := approvedRestartFixture(t)
	approvedNoStartPrecondition(t, f, id)
	captureApprovedHandoff(t, f, "before-explicit-start")
}
func TestApprovedTaskRestartDoesNotAutoStart(t *testing.T) {
	f, _, id := approvedRestartFixture(t)
	approvedNoStartPrecondition(t, f, id)
	// Exercise the same formal worker synchronously: APPROVE/NO_START is not queued.
	before := captureApprovedHandoff(t, f, "no-auto-start-before-worker")
	f.plane.ExecutionProposals.ApprovedContinuation()(f.ctx)
	after := captureApprovedHandoff(t, f, "no-auto-start-after-worker")
	if string(evidenceCanonicalBytes(t, before)) != string(evidenceCanonicalBytes(t, after)) {
		t.Fatal("approved task auto-started")
	}
}
func TestApprovedTaskRestartHandoffReadbackIsStable(t *testing.T) {
	f, _, id := approvedRestartFixture(t)
	approvedNoStartPrecondition(t, f, id)
	before := captureApprovedHandoff(t, f, "readback-first")
	after := captureApprovedHandoff(t, f, "readback-second")
	if string(evidenceCanonicalBytes(t, before)) != string(evidenceCanonicalBytes(t, after)) {
		t.Fatal("readback wrote durable rows")
	}
}
func waitApprovedInfrastructureFailure(t *testing.T, f *integrationFixture, runID string) {
	t.Helper()
	if session, ok := f.plane.Supervisor.GetSession(runID); ok {
		_, _ = session.Wait(f.ctx)
	}
	current, err := f.plane.Runs.Get(f.ctx, runID)
	if err != nil || current.Status != run.StatusInfrastructureFailed {
		t.Fatalf("exact terminal infrastructure failure: %+v %v", current, err)
	}
}
