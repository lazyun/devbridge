package localmcp_test

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
)

// restartDurableFacts records the isolated test database without changing facts.
// SELECT * deliberately retains every persisted column, including schema fields
// added by migrations, instead of hiding absent or unexpected authority.
func restartDurableFacts(t *testing.T, f *integrationFixture, p executionproposal.Proposal, in executioncancel.Input) map[string]any {
	t.Helper()
	tx, err := f.plane.DB.SQL().BeginTx(f.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	out := map[string]any{"input": in, "runtimeEpoch": f.plane.DB.RuntimeEpoch()}
	queries := map[string]struct {
		q    string
		args []any
	}{
		"task":                 {"SELECT * FROM tasks WHERE id=?", []any{p.TaskID}},
		"allTaskExecutions":    {"SELECT * FROM execution_proposals WHERE task_id=? ORDER BY id", []any{p.TaskID}},
		"eligibilities":        {"SELECT * FROM no_run_successor_eligibilities WHERE task_id=? OR predecessor_execution_id=? ORDER BY predecessor_execution_id", []any{p.TaskID, p.ID}},
		"execution":            {"SELECT * FROM execution_proposals WHERE id=?", []any{p.ID}},
		"admission":            {"SELECT * FROM execution_proposal_admissions WHERE proposal_id=?", []any{p.ID}},
		"plans":                {"SELECT * FROM runtime_scope_plans WHERE owner_id=? ORDER BY plan_id", []any{p.ID}},
		"preparationAuthority": {"SELECT * FROM runtime_preparation_authorities WHERE owner_id=? ORDER BY plan_id", []any{p.ID}},
		"runs":                 {"SELECT * FROM runs WHERE task_id=? OR execution_proposal_id=? ORDER BY id", []any{p.TaskID, p.ID}},
		"cancellations":        {"SELECT * FROM execution_cancellations WHERE task_id=? OR owner_id=? ORDER BY request_id", []any{p.TaskID, p.ID}},
		"effects":              {"SELECT * FROM no_run_successor_effect_authorities WHERE predecessor_execution_id=? ORDER BY authority_id", []any{p.ID}},
		"receipts":             {"SELECT * FROM no_run_successor_command_receipts WHERE predecessor_execution_id=? ORDER BY request_id", []any{p.ID}},
		"recoveries":           {"SELECT * FROM recovery_status_records ORDER BY id", nil},
		"attempts":             {"SELECT * FROM runtime_preparation_attempts WHERE plan_id IN (SELECT plan_id FROM runtime_scope_plans WHERE owner_id=?) ORDER BY attempt_id", []any{p.ID}},
		"startAuthorities":     {"SELECT * FROM runtime_start_authorities WHERE owner_id=? ORDER BY authority_id", []any{p.ID}},
		"bindings":             {"SELECT * FROM runtime_workset_bindings WHERE owner_id=? ORDER BY plan_id", []any{p.ID}},
		"events":               {"SELECT * FROM events ORDER BY id", nil},
		"agentExecutions":      {"SELECT * FROM agent_executions ORDER BY id", nil},
	}
	for key, q := range queries {
		rows, e := tx.QueryContext(f.ctx, q.q, q.args...)
		if e != nil {
			t.Fatalf("durable facts %s: %v", key, e)
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			t.Fatal(e)
		}
		entries := []map[string]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			ptr := make([]any, len(columns))
			for i := range values {
				ptr[i] = &values[i]
			}
			if e = rows.Scan(ptr...); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			entry := map[string]any{}
			for i, c := range columns {
				if b, ok := values[i].([]byte); ok {
					entry[c] = string(b)
				} else {
					entry[c] = values[i]
				}
			}
			entries = append(entries, entry)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		out[key] = entries
	}
	out["completeEntities"] = readCompleteSuccessorEntities(t, f, tx, p)
	return out
}

// Complete rows and schema are read under the same snapshot transaction as all
// other durable facts. typeof() preserves SQLite storage types independently of
// declared affinity. Identifier quoting is confined to schema-derived names.
func evidenceCanonicalBytes(t *testing.T, value any) []byte {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func evidenceIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
func evidenceQuery(t *testing.T, f *integrationFixture, tx *sql.Tx, q string, args ...any) []map[string]any {
	t.Helper()
	rows, err := tx.QueryContext(f.ctx, q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range vals {
			ptr[i] = &vals[i]
		}
		if err = rows.Scan(ptr...); err != nil {
			t.Fatal(err)
		}
		row := map[string]any{}
		for i, c := range cols {
			if b, ok := vals[i].([]byte); ok {
				row[c] = string(b)
			} else {
				row[c] = vals[i]
			}
		}
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
func readCompleteSuccessorEntities(t *testing.T, f *integrationFixture, tx *sql.Tx, p executionproposal.Proposal) map[string]any {
	t.Helper()
	out := map[string]any{}
	targets := []struct {
		table, where string
		args         []any
	}{
		{"execution_proposals", "task_id=?", []any{p.TaskID}},
		{"successorExecutionCandidates", "task_id=? AND id<>?", []any{p.TaskID, p.ID}},
		{"no_run_successor_eligibilities", "task_id=? OR predecessor_execution_id=?", []any{p.TaskID, p.ID}},
		{"no_run_successor_effect_authorities", "task_id=? OR predecessor_execution_id=?", []any{p.TaskID, p.ID}},
		{"no_run_successor_command_receipts", "task_id=? OR predecessor_execution_id=?", []any{p.TaskID, p.ID}},
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
			t.Fatalf("complete row table has no primary key: %s", target.table)
		}
		order := []string{}
		types := []string{}
		for _, col := range pk {
			order = append(order, evidenceIdent(col["name"].(string)))
		}
		for _, col := range info {
			types = append(types, "typeof("+evidenceIdent(col["name"].(string))+")")
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

func recordRestartFacts(t *testing.T, name string, facts map[string]any) {
	t.Helper()
	data, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DEVBRIDGE_DURABLE_FACTS_JSON=%s", data)
	t.Logf("DEVBRIDGE_DURABLE_FACTS_SHA256=%x", sha256.Sum256(data))
	t.Logf("DEVBRIDGE_DURABLE_FACTS_NAME=%s", name)
	envelope := map[string]any{"testName": t.Name(), "checkpoint": name, "facts": facts}
	evidence, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("DEVBRIDGE_COMPLETE_ROW_EVIDENCE_JSON=%s", evidence)
	if root := os.Getenv("DEVBRIDGE_RESTART_EVIDENCE_DIR"); root != "" {
		file, e := os.CreateTemp(root, "complete-row-*.json")
		if e != nil {
			t.Fatal(e)
		}
		_, e = file.Write(append(evidence, '\n'))
		closeErr := file.Close()
		if e != nil {
			t.Fatal(e)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
	if root := os.Getenv("DEVBRIDGE_RESTART_EVIDENCE_DIR"); root != "" {
		// This opt-in writes audit artifacts only; it never changes the test DataRoot.
		if err = os.WriteFile(filepath.Join(root, name+".json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

// The admission-only fixture must hold the boundary between the two committed
// transactions. An active continuation queue is evidence it does not do so.
func TestAdmittedNoRunFixtureHasNoAutonomousPreparation(t *testing.T) {
	// Joining the owned continuation is the explicit fixture capability boundary.
	// START_AUTHORIZED remains real and may still appear in the production queue;
	// queue membership alone must never be interpreted as an absent-Run promise.
	file, err := parser.ParseFile(token.NewFileSet(), "scalar_fixture_contract_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "newManualPreparationScalarFixture" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Close" {
				recv, ok := sel.X.(*ast.SelectorExpr)
				found = ok && recv.Sel.Name == "Retirement"
			}
			return true
		})
	}
	if !found {
		t.Fatal("manual admission fixture does not join its owned preparation worker")
	}
	f, _, p := localMCPScalarAdmission(t)
	pending, err := sqlite.NewExecutionProposalStore(f.plane.DB).PendingApprovedExecutions(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	authorized := false
	for _, v := range pending {
		authorized = authorized || v.ExecutionID == p.ID
	}
	if !authorized {
		t.Fatal("fixture lost real START_AUTHORIZED authority")
	}
	assertScalarLayer(t, f, p.ID, 0, 0)
}

func deferNoRunFailureFacts(t *testing.T, f *integrationFixture, p executionproposal.Proposal, in executioncancel.Input) func() {
	return func() {
		if t.Failed() {
			recordRestartFacts(t, fmt.Sprintf("failure-%s", t.Name()), restartDurableFacts(t, f, p, in))
		}
	}
}

// Force the exact existing production continuation ordering, without sleeps or
// SQL mutation: preparation wins after admission and before an empty-Run cancel.
func TestAdmittedNoRunFixturePreparationWinsWitness(t *testing.T) {
	f, sid, p := localMCPScalarAdmission(t)
	in := admissionCancelInput(f, sid, p)
	recordRestartFacts(t, "restart-controlled-admission", restartDurableFacts(t, f, p, in))
	prepared, r, err := f.plane.ExecutionProposals.PrepareAdmittedExecution(f.ctx, sid, p)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID == "" || prepared.RunID != r.ID {
		t.Fatal("canonical preparation did not allocate exact Run")
	}
	before := restartDurableFacts(t, f, p, in)
	_, err = f.control.CancelExecutionForSession(f.ctx, in)
	recordRestartFacts(t, "restart-controlled-run-not-active-before", before)
	recordRestartFacts(t, "restart-controlled-run-not-active-after", restartDurableFacts(t, f, p, in))
	if err == nil || !strings.Contains(err.Error(), "RUN_NOT_ACTIVE") {
		t.Fatalf("guard witness: %v", err)
	}
}
