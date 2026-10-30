package sqlite_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeaudit"
)

func scalarArchitectureEntries(t *testing.T) []runtimeaudit.Entry {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	rows, err := runtimeaudit.Scan(filepath.Join(filepath.Dir(here), "../.."))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("empty runtime call inventory")
	}
	return rows
}

func TestExecutionScalarAdmissionArchitecture(t *testing.T) {
	for _, row := range scalarArchitectureEntries(t) {
		if row.Kind != "LegacyExecutionRun" {
			continue
		}
		// The historical wrapper is the sole allowed internal legacy call.
		if row.File == "internal/storage/sqlite/task_execution_handoff_store.go" && row.Symbol == "ExecutionProposalStore.AuthorizeAndCreateRun" && row.Callee == "authorizeAndCreateRunTx" {
			continue
		}
		t.Errorf("noncanonical production call %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
	}
}

func TestExecutionAdmissionTwoStepPattern(t *testing.T) {
	required := map[string]bool{"admitExecutionTx": false, "ExecutionProposalStore.PrepareAdmittedExecution": false, "continueAdmittedExecution": false}
	for _, row := range scalarArchitectureEntries(t) {
		if _, ok := required[row.Symbol]; ok && (row.Kind == "ExecutionAdmissionEntry" || row.Kind == "CanonicalPreparationRunEntry") {
			required[row.Symbol] = true
		}
		if row.Symbol == "admitExecutionTx" && (row.Kind == "RunWriter" || row.Kind == "CanonicalPreparationRun" || row.Kind == "CanonicalRuntimeWorkset") {
			t.Errorf("admission crosses post-commit boundary %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
	for symbol, found := range required {
		if !found {
			t.Errorf("missing canonical stage %s", symbol)
		}
	}
}

func TestScalarProductionPreparationUsesCanonicalAuthorityWriters(t *testing.T) {
	required := map[string]bool{"ClaimWorkspacePreparation": false, "RecordWorkspaceObservations": false, "BindPreparedRuntimeWorkset": false}
	for _, row := range scalarArchitectureEntries(t) {
		if row.Symbol != "Coordinator.prepareScalarRuntime" {
			continue
		}
		if _, ok := required[row.Callee]; ok {
			required[row.Callee] = true
		}
		if !scalarPreparationCallAllowed(row) {
			t.Errorf("scalar preparation bypass %s:%d %s -> %s (%s)", row.File, row.Line, row.Symbol, row.Callee, row.Kind)
		}
	}
	for callee, found := range required {
		if !found {
			t.Errorf("missing scalar production stage %s", callee)
		}
	}
}

// Only these inventoried effects belong to scalar post-Run preparation. New
// writer classifications are rejected automatically instead of falling through
// an incomplete denylist.
func scalarPreparationCallAllowed(row runtimeaudit.Entry) bool {
	allowed := map[string]string{
		"ClaimWorkspacePreparation":   "CanonicalWorkspaceClaim",
		"RecordWorkspaceObservations": "CanonicalWorkspaceObservation",
		"BindPreparedRuntimeWorkset":  "CanonicalRuntimeWorkset",
	}
	return allowed[row.Callee] != "" && allowed[row.Callee] == row.Kind
}

func TestScalarProductionPreparationArchitectureRejectsDirectAuthorityWrites(t *testing.T) {
	root := filepath.Join(t.TempDir(), "internal")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	source := `package fixture
func (c *Coordinator) prepareScalarRuntime() {
 store.ClaimWorkspacePreparation()
 store.RecordWorkspaceObservations()
 store.BindPreparedRuntimeWorkset()
 tx.Exec("INSERT INTO runs(id) VALUES(?)")
 tx.Exec("INSERT INTO runtime_scope_plans(plan_id) VALUES(?)")
 tx.Exec("INSERT INTO runtime_preparation_authorities(plan_id) VALUES(?)")
 tx.Exec("INSERT INTO runtime_workspace_observations(plan_id) VALUES(?)")
 tx.Exec("INSERT INTO runtime_workset_bindings(plan_id) VALUES(?)")
 tx.Exec("INSERT INTO runtime_start_authorities(plan_id) VALUES(?)")
 tx.Exec("INSERT INTO run_repository_bindings(run_id) VALUES(?)")
 tx.Exec("UPDATE runs SET runtime_workset_json=?")
 store.CreatePreparationRun()
 backend.StartTurn()
}`
	if err := os.WriteFile(filepath.Join(root, "injected.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := runtimeaudit.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	rejected := map[string]bool{}
	allowed := 0
	for _, row := range rows {
		if row.Symbol != "Coordinator.prepareScalarRuntime" {
			t.Fatalf("unexpected scanner row: %+v", row)
		}
		if scalarPreparationCallAllowed(row) {
			allowed++
		} else {
			rejected[row.Kind] = true
		}
	}
	if allowed != 3 {
		t.Fatalf("canonical calls=%d, want 3", allowed)
	}
	for _, kind := range []string{"RunWriter", "ScopePlanWriter", "PreparationAuthorityWriter", "WorkspaceObservationWriter", "WorksetBindingWriter", "StartAuthorityWriter", "RepositoryBindingWriter", "RuntimeWorksetWriter", "CanonicalPreparationRun", "RuntimeDispatch"} {
		if !rejected[kind] {
			t.Errorf("injected %s bypassed scalar architecture fence", kind)
		}
	}
	if scalarPreparationCallAllowed(runtimeaudit.Entry{Kind: "UnknownWriter", Callee: "Unknown"}) {
		t.Fatal("unknown inventoried effect accepted")
	}
}
