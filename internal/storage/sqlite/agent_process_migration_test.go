package sqlite

import (
	"context"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
)

func TestProcessV41MigrationPreservesExistingRunAuthority(t *testing.T) {
	f := newAgentStorageFixture(t, "migration-process")
	ctx := context.Background()
	op := prepareAgentOperation(t, f, agentconversation.Review, "before-process-telemetry")
	r, err := op.make(ctx, "run-before-v41")
	if err != nil {
		t.Fatal(err)
	}
	raw := f.db.SQL()
	// Restore the v40 schema: these are the only objects added by v41 and are
	// still empty. Existing Run/Task/claim data is produced by the real stores.
	// Later schema objects remain installed; limit replay to the migration under test.
	for _, q := range []string{`DROP TABLE run_agent_processes`, `DROP TABLE agent_process_launches`, `DELETE FROM schema_migrations WHERE version>44`, `UPDATE schema_migrations SET version=40 WHERE version=44`} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() []string {
		t.Helper()
		var run, task string
		if err := raw.QueryRow(`SELECT id||'|'||status||'|'||created_at||'|'||coalesce(runtime_epoch,'') FROM runs WHERE id=?`, r.ID).Scan(&run); err != nil {
			t.Fatal(err)
		}
		if err := raw.QueryRow(`SELECT id||'|'||status||'|'||created_at FROM tasks WHERE id=?`, r.TaskID).Scan(&task); err != nil {
			t.Fatal(err)
		}
		return []string{run, task}
	}
	before := snapshot()
	for i := 0; i < 2; i++ {
		if err := ApplyMigrations(ctx, raw, historicalMigrations()[:41]); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("v41 rewrote existing authority")
	}
	for _, table := range []string{"agent_process_launches", "run_agent_processes"} {
		var n int
		if err := raw.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("invented process history %s %d %v", table, n, err)
		}
	}
	telemetry, err := NewAgentRuntimeStore(f.db).Read(ctx, r.ID)
	if err != nil || telemetry.Agent.ProcessAvailability != "UNAVAILABLE" || telemetry.Agent.PID != 0 {
		t.Fatalf("guessed historical process %+v %v", telemetry.Agent, err)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("v41 foreign key violation")
	}
}
