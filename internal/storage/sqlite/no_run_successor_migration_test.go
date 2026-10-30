package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunSuccessorSchemaMigration(t *testing.T) {
	db, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var phaseDDL string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&phaseDDL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(phaseDDL, "'NO_RUN_SUCCESSOR_REQUIRED'") {
		t.Fatal("missing durable successor-required phase")
	}
	var planDDL, effectDDL string
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='runtime_scope_plans'`).Scan(&planDDL); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(planDDL, "'EXECUTION'") || !strings.Contains(planDDL, "authority_snapshot_hash") {
		t.Fatal("v95 missing execution-owned Plan and current snapshot storage")
	}
	if err := db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='no_run_successor_effect_authorities'`).Scan(&effectDDL); err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"predecessor_runtime_scope_plan_id", "predecessor_plan_content_hash", "predecessor_authority_snapshot_hash", "verified_current_authority_snapshot_hash"} {
		if !strings.Contains(effectDDL, column) {
			t.Fatalf("v95 effect authority lacks %s", column)
		}
	}
	for _, table := range []string{"no_run_successor_effect_authorities", "no_run_successor_command_receipts"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s missing: %d %v", table, n, err)
		}
	}
	var fk int
	rows, err := db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		fk++
	}
	if err := rows.Err(); err != nil || fk != 0 {
		t.Fatalf("foreign keys: %d %v", fk, err)
	}
}

func TestNoRunSuccessorLegacyCanceledPlanConversion(t *testing.T) {
	ctx := context.Background()
	f, owner, predecessor := acceptedExecutionFixture(t, "-legacy-plan-conversion")
	if _, _, err := f.store.ReserveTaskExecution(ctx, f.session, owner.ID); err != nil {
		t.Fatal(err)
	}
	cancel := executioncancel.Input{AISessionID: f.session, ProjectID: owner.ProjectID, ExecutionID: predecessor.ID, OwnerType: "EXECUTION", OwnerID: predecessor.ID, RequestID: "cancel-legacy-plan-conversion", Reason: "cancel pre-Run"}
	if receipt, _, err := NewExecutionCancellationStore(f.db).Begin(ctx, cancel, false); err != nil || receipt.State != "CANCELED" {
		t.Fatalf("cancel: %+v %v", receipt, err)
	}
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	plan, err := readSuccessorSourcePlan(ctx, tx, predecessor.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan.OwnerKind = runtimepreparation.OwnerImplementation
	plan.BackendConfigFingerprint, plan.BackendSource, plan.BackendKind = "", "", ""
	plan.CodexFastMode, plan.ServiceTier = false, ""
	plan.RuntimeProtocolIdentity, plan.AuthoritySnapshotHash = "", ""
	plan.ContentHash, err = plan.ComputeContentHash()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime_scope_plans_immutable_UPDATE", "runtime_preparation_authorities_immutable_UPDATE"} {
		if _, err = tx.ExecContext(ctx, `DROP TRIGGER `+sqlIdentifier(name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_scope_plans SET owner_kind='IMPLEMENTATION',backend_config_fingerprint='',backend_source='',backend_kind='',codex_fast_mode=0,service_tier='',runtime_protocol_identity='',authority_snapshot_hash='',content_hash=? WHERE plan_id=?`, plan.ContentHash, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runtime_preparation_authorities SET owner_kind='IMPLEMENTATION' WHERE plan_id=?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"runtime_scope_plans_immutable_UPDATE", "runtime_preparation_authorities_immutable_UPDATE"} {
		table := "runtime_scope_plans"
		if name == "runtime_preparation_authorities_immutable_UPDATE" {
			table = "runtime_preparation_authorities"
		}
		if _, err = tx.ExecContext(ctx, `CREATE TRIGGER `+sqlIdentifier(name)+` BEFORE UPDATE ON `+table+` BEGIN SELECT RAISE(ABORT,'runtime preparation fact immutable'); END`); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgradeCanceledNoRunExecutionPlansV95(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
		t.Fatal(err)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "legacy-converted-successor", PayloadSHA256: strings.Repeat("a", 64), ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID, ActorAuthority: f.session}
	if receipt, err := f.store.PrepareSuccessorExecution(ctx, in); !errors.Is(err, runtimeselection.ErrBindingMissing) || receipt.SuccessorExecutionID != "" {
		t.Fatalf("legacy conversion must not invent a runtime binding: %+v %v", receipt, err)
	}
	for _, table := range []string{"no_run_successor_effect_authorities", "no_run_successor_command_receipts"} {
		if got := noRunCount(t, f, table); got != 0 {
			t.Fatalf("legacy rejection wrote %s: %d", table, got)
		}
	}
}

func TestNoRunSuccessorV94UpgradeAndRepeatedOpen(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "v94.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	all := DefaultMigrations()
	var v94, v95 int
	for i, migration := range all {
		if migration.Version == 94 {
			v94 = i + 1
		}
		if migration.Version == 95 {
			v95 = i + 1
		}
	}
	if v94 == 0 || v95 != v94+1 {
		t.Fatal("unexpected migration sequence")
	}
	if err := ApplyMigrations(ctx, raw, all[:v94]); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, raw, all[:v95]); err != nil {
		t.Fatalf("v94 to v95: %v", err)
	}
	if err := ApplyMigrations(ctx, raw, all[:v95]); err != nil {
		t.Fatalf("repeated open: %v", err)
	}
	var version int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 95 {
		t.Fatalf("version %d %v", version, err)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation after upgrade")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
