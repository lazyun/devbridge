package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimepermission"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func authorityFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	statements := []string{
		`CREATE TABLE execution_proposals (id TEXT, task_id TEXT, project_id TEXT, task_definition_revision_id TEXT)`,
		`CREATE TABLE task_definition_revisions (id TEXT, task_id TEXT, payload_json TEXT, payload_sha256 TEXT)`,
		`CREATE TABLE runs (id TEXT PRIMARY KEY, runtime_workset_json TEXT)`,
		`CREATE TABLE runtime_workset_bindings (plan_id TEXT, run_id TEXT)`,
		projectAuthorizationV135SQL,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func insertAuthorityDefinition(t *testing.T, db *sql.DB, network bool) {
	t.Helper()
	d := task.Definition{Version: 1, ProjectID: "project", WorkSpec: task.WorkSpec{CommandPermissions: &task.CommandPermissions{NetworkAccess: network}}}
	raw, hash, err := task.EncodeDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO task_definition_revisions VALUES ('revision','task',?,?)`, raw, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_proposals VALUES ('execution','task','project','revision')`); err != nil {
		t.Fatal(err)
	}
}

func insertAuthorizationRevision(t *testing.T, db *sql.DB, revision int, trusted, network bool, head bool) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO project_execution_authorization_revisions VALUES (?,?,?,?,'2026-01-01T00:00:00Z')`, "project", revision, boolIntAuthorization(trusted), boolIntAuthorization(network)); err != nil {
		t.Fatal(err)
	}
	if head {
		if _, err := db.Exec(`INSERT INTO project_execution_authorizations VALUES ('project',?,?,?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, boolIntAuthorization(trusted), boolIntAuthorization(network), revision); err != nil {
			t.Fatal(err)
		}
	}
}

func authorityWorkset(p *runtimepermission.TaskPermissions) workset.RuntimeWorkset {
	return workset.RuntimeWorkset{ProjectID: "project", TaskID: "task", ExecutionID: "execution", TaskDefinitionRevisionID: "revision", RunID: "run-1", TaskPermissions: p}
}

func TestTaskPermissionAuthorityRequiresExactAdmittedDefinition(t *testing.T) {
	for _, granted := range []bool{false, true} {
		db := authorityFixture(t)
		insertAuthorityDefinition(t, db, granted)
		w := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "VERIFICATION", NetworkAccess: granted})
		if err := validateTaskPermissionAuthorityTx(context.Background(), db, w); err != nil {
			t.Fatal(err)
		}
		w.TaskPermissions.NetworkAccess = !granted
		if validateTaskPermissionAuthorityTx(context.Background(), db, w) == nil {
			t.Fatal("manifest changed frozen network grant")
		}
		w.TaskPermissions.NetworkAccess = granted
		w.TaskDefinitionRevisionID = "other"
		if validateTaskPermissionAuthorityTx(context.Background(), db, w) == nil {
			t.Fatal("foreign revision authorized")
		}
		w.TaskDefinitionRevisionID = "revision"
		if _, err := db.Exec(`UPDATE task_definition_revisions SET payload_json='{}'`); err != nil {
			t.Fatal(err)
		}
		if validateTaskPermissionAuthorityTx(context.Background(), db, w) == nil {
			t.Fatal("corrupt definition authorized")
		}
		db.Close()
	}
}

// A snapshot claiming trusted/network with no durable authorization history is
// rejected even when the network booleans already match the task definition —
// network equality must never bypass project-grant verification.
func TestTaskPermissionAuthorityForgedSnapshotRejected(t *testing.T) {
	db := authorityFixture(t)
	insertAuthorityDefinition(t, db, true)
	// definitionNetwork=true, p.NetworkAccess=true: old code returned early.
	w := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "IMPLEMENTATION", NetworkAccess: true, TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 7, Trusted: true}})
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, w); err == nil {
		t.Fatal("forged trusted snapshot passed because network already matched the definition")
	}
	// A forged network-only snapshot on a network-enabled definition too.
	w2 := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "IMPLEMENTATION", NetworkAccess: true, TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 7, Trusted: false, NetworkFromAuthorization: false}})
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, w2); err == nil {
		t.Fatal("forged snapshot without revision history passed")
	}
	// No snapshot at all with matching definition network stays valid.
	w3 := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "IMPLEMENTATION", NetworkAccess: true})
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, w3); err != nil {
		t.Fatalf("plain definition network rejected: %v", err)
	}
}

// Each toggle combination freezes an exact snapshot that matches its immutable
// revision row and is accepted; mismatched capability bits are rejected.
func TestTaskPermissionAuthorityIndependentToggles(t *testing.T) {
	cases := []struct {
		name                             string
		definitionNetwork, trusted, net  bool
		snapshotTrusted, snapshotNetwork bool
		manifestNetwork                  bool
		wantOK                           bool
	}{
		{"trusted only", false, true, false, true, false, false, true},
		{"network only", false, false, true, false, true, true, true},
		{"both", false, true, true, true, true, true, true},
		{"both network sources", true, true, true, true, true, true, true},
		{"task plus project network only", true, false, true, false, true, true, true},
		{"task network and project trust", true, true, false, true, false, true, true},
		{"none granted", false, false, false, false, false, false, true},
		{"trusted claimed but not granted", false, false, false, true, false, false, false},
		{"network claimed but not granted", false, false, false, false, true, true, false},
	}
	for _, tc := range cases {
		db := authorityFixture(t)
		insertAuthorityDefinition(t, db, tc.definitionNetwork)
		insertAuthorizationRevision(t, db, 1, tc.trusted, tc.net, true)
		w := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "IMPLEMENTATION", NetworkAccess: tc.manifestNetwork, TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 1, Trusted: tc.snapshotTrusted, NetworkFromAuthorization: tc.snapshotNetwork}})
		err := validateTaskPermissionAuthorityTx(context.Background(), db, w)
		if tc.wantOK && err != nil {
			t.Fatalf("%s: rejected: %v", tc.name, err)
		}
		if !tc.wantOK && err == nil {
			t.Fatalf("%s: accepted", tc.name)
		}
		db.Close()
	}
}

// Exact replay of an already-bound Run stays valid after a real flags=false
// revocation; a new first binding under the same frozen revision is rejected.
func TestTaskPermissionAuthorityReplayAfterRevocation(t *testing.T) {
	db := authorityFixture(t)
	insertAuthorityDefinition(t, db, false)
	insertAuthorizationRevision(t, db, 1, true, true, true)
	w := authorityWorkset(&runtimepermission.TaskPermissions{Purpose: "IMPLEMENTATION", NetworkAccess: true, TrustedExecution: &runtimepermission.TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 1, Trusted: true, NetworkFromAuthorization: true}})
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, w); err != nil {
		t.Fatalf("first binding rejected: %v", err)
	}
	// Durably bind the Run.
	if _, err := db.Exec(`INSERT INTO runtime_workset_bindings VALUES ('plan','run-1')`); err != nil {
		t.Fatal(err)
	}
	// Real revocation: a new revision with both flags false becomes head.
	insertAuthorizationRevision(t, db, 2, false, false, false)
	if _, err := db.Exec(`UPDATE project_execution_authorizations SET trusted_execution=0,network_access=0,head_revision=2`); err != nil {
		t.Fatal(err)
	}
	// Exact replay reads the historical revision row and stays valid.
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, w); err != nil {
		t.Fatalf("exact replay after revocation rejected: %v", err)
	}
	// A brand-new Run still presenting the superseded revision (the
	// prepare->revoke->first-bind race) is rejected.
	race := w
	race.RunID = "run-new"
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, race); err == nil {
		t.Fatal("new binding under superseded revision accepted")
	}
	// An already-bound Run cannot smuggle changed authority: a snapshot with
	// different capability bits under the same revision is rejected.
	forged := w
	forged.TaskPermissions.TrustedExecution = &runtimepermission.TrustedExecutionSnapshot{ProjectID: "project", AuthorizationRevision: 1, Trusted: true, NetworkFromAuthorization: false}
	forged.TaskPermissions.NetworkAccess = false
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, forged); err == nil {
		t.Fatal("bound Run upgraded its frozen capabilities")
	}
	// Foreign project snapshot is always rejected.
	foreign := w
	foreign.TaskPermissions.TrustedExecution = &runtimepermission.TrustedExecutionSnapshot{ProjectID: "other", AuthorizationRevision: 1, Trusted: true, NetworkFromAuthorization: true}
	if err := validateTaskPermissionAuthorityTx(context.Background(), db, foreign); err == nil {
		t.Fatal("foreign project snapshot authorized")
	}
	// SCOPE_REVIEW never carries project authority — the guard skips it and
	// policy validation rejects any snapshot on reviews separately.
	db.Close()
}

// The revision history is append-only at the SQL level: UPDATE and DELETE on
// revision rows abort.
func TestTaskPermissionAuthorityHistoryImmutable(t *testing.T) {
	db := authorityFixture(t)
	insertAuthorizationRevision(t, db, 1, true, true, true)
	if _, err := db.Exec(`UPDATE project_execution_authorization_revisions SET trusted_execution=0 WHERE project_id='project'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("history UPDATE must abort with append-only trigger, got %v", err)
	}
	if _, err := db.Exec(`DELETE FROM project_execution_authorization_revisions WHERE project_id='project'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("history DELETE must abort with append-only trigger, got %v", err)
	}
	db.Close()
}
