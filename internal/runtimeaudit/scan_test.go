package runtimeaudit

import (
	"path/filepath"
	"runtime"
	"testing"
)

func productionEntries(t *testing.T) []Entry {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtimeaudit source location unavailable")
	}
	rows, err := Scan(filepath.Dir(filepath.Dir(here)))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func rejectCall(t *testing.T, rows []Entry, symbol, callee string) {
	t.Helper()
	for _, row := range rows {
		if row.Symbol == symbol && row.Callee == callee && row.Kind != "LegacyExecutionRunEntry" && row.Kind != "LegacyExecutionStartEntry" && row.Kind != "PotentialRunCreationEntry" && row.Kind != "LegacyRecoveryDispatchEntry" {
			t.Errorf("legacy production call %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestExecutionModernPathHasNoAdmissionResumeReserved(t *testing.T) {
	rows := productionEntries(t)
	for _, symbol := range []string{"Facade.StartTaskExecutionInstructions", "Facade.startTaskExecutionExpectedCommand", "Facade.StartTaskExecution", "Service.Start", "Service.AuthorizeAndStart"} {
		rejectCall(t, rows, symbol, "ResumeReserved")
		rejectCall(t, rows, symbol, "ResumeReservedForProject")
	}
}

func TestExecutionResidualCompatibilityHasNoDirectResumeReserved(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if (row.Symbol == "Service.ApprovedContinuation" || row.Symbol == "Service.ResumeReserved" || row.Symbol == "Service.ResumeReservedForProject") && row.Callee == "resumeReserved" {
			t.Errorf("continuation bypasses authorized start: %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestReserveExecutionAdmittedPathDoesNotPrepareRunInsideAdmission(t *testing.T) {
	rows := productionEntries(t)
	canonical := false
	for _, row := range rows {
		if row.Symbol == "ExecutionProposalStore.PrepareAdmittedExecution" && row.Callee == "continueAdmittedExecution" {
			canonical = true
		}
		if row.Symbol == "ExecutionProposalStore.ReserveExecution" && row.Callee == "continueAdmittedExecution" {
			t.Errorf("admission also prepared Run: %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
	if !canonical {
		t.Error("post-commit Core Run preparation caller is missing")
	}
}

func TestTaskStartAdmissionDoesNotPrepareRunInsideStore(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if row.Symbol == "WorkflowProgressionStore.ReserveTaskExecutionExpectedCommand" && row.Callee == "continueAdmittedExecution" {
			t.Errorf("Task admission also prepared Run: %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestExecutionModernPathHasNoAuthorizeAndCreateRunCaller(t *testing.T) {
	rows := productionEntries(t)
	for _, symbol := range []string{"WorkflowProgressionStore.ReserveTaskExecutionInstructions", "Service.Start", "Service.AuthorizeAndStart", "Facade.StartTaskExecutionInstructions"} {
		rejectCall(t, rows, symbol, "AuthorizeAndCreateRun")
		rejectCall(t, rows, symbol, "authorizeAndCreateRunTx")
	}
}

func TestExecutionModernPathHasNoConsumeAndCreateRunCaller(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if row.Callee == "ConsumeAndCreateRun" && row.Kind == "LegacyExecutionRun" {
			t.Errorf("legacy production call %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestRecoveryModernPathHasNoDirectCreateRunCaller(t *testing.T) {
	rows := productionEntries(t)
	rejectCall(t, rows, "RecoveryStatusService.Start", "CreateRun")
}

func TestRecoveryModernPathHasNoSameThreadStartCaller(t *testing.T) {
	rows := productionEntries(t)
	rejectCall(t, rows, "RecoveryStatusService.Start", "StartRecoveryStatusTurn")
}

func TestRecoveryStatusModernPathHasNoDirectCreateRunCaller(t *testing.T) {
	rows := productionEntries(t)
	canonical := false
	for _, row := range rows {
		if row.Kind == "RunWriter" && row.Symbol == "createRecoveryChildPreparationRunTx" {
			canonical = true
		}
		if row.Symbol == "RecoveryStatusStore.CreateRun" || row.Symbol == "RecoveryStatusService.Start" && row.Callee == "CreateRun" {
			t.Errorf("direct Recovery Run creation %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
	if !canonical {
		t.Error("canonical Recovery child Run writer is missing")
	}
}

func TestRecoveryStatusModernPathHasNoSourceWorksetCopy(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if row.File == "internal/storage/sqlite/recovery_status_store.go" && (row.Kind == "RunWriter" || row.Kind == "RuntimeWorksetWriter" || row.Kind == "RepositoryBindingWriter") {
			t.Errorf("Recovery source runtime copy %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestRecoveryStatusModernPathHasNoSameThreadStartCaller(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if row.Symbol == "RecoveryStatusService.Start" && row.Callee == "StartRecoveryStatusTurn" {
			t.Errorf("Recovery same Thread start %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}

func TestRecoveryStatusModernPathHasNoDirectDispatchCaller(t *testing.T) {
	rows := productionEntries(t)
	for _, row := range rows {
		if row.Symbol == "RecoveryStatusService.Start" && row.Callee == "StartTurn" {
			t.Errorf("Recovery direct dispatch %s:%d %s -> %s", row.File, row.Line, row.Symbol, row.Callee)
		}
	}
}
