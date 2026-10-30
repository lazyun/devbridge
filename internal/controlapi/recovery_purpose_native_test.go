package controlapi_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

// The source interruption and diagnostic report are observation fixtures. The
// qualification is measured against the installed native executable, and Core
// produces every formal/purpose authority. Native execution starts at the
// consumed successor and includes both Implementation and Verification.
func TestRecoveryPurposeNativeSuccessorAfterCoreConsumption(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; loopback provider only")
	}
	root, err := filepath.Abs("../../build/recovery-purpose-authority-2026-10-02/native")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "purpose-successor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("Recovery purpose native evidence:", root)
	t.Setenv("TMPDIR", root)
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	planeConfig := controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: native}, AppServerContinuity: true, AppServerSpec: func(codexconfig.Config) appserver.Spec {
		return appserver.Spec{ExecutablePath: native, CodexHome: filepath.Join(root, "runtime-home")}
	}}
	t.Cleanup(func() { stopContinuityFixtureServers(f) })
	runtime, _, original := prepareQualifiedContinuityExecution(t, f, native, root)
	var raw string
	if err = f.plane.DB.SQL().QueryRow(`SELECT receipt_json FROM formal_execution_receipts WHERE ai_session_id=? AND request_id=?`, f.session, "qualified-continuity-commit").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var archived formalexecution.Receipt
	if err = json.Unmarshal([]byte(raw), &archived); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.plane.WorkflowProgression.ReadApproveAndStart(f.ctx, archived.Archive.Identity)
	if err != nil || !reflect.DeepEqual(receipt, archived) {
		t.Fatalf("measured formal receipt readback: %v", err)
	}
	purpose := prepareQualifiedRecoveryPurpose(t, f, receipt, original)
	intent, err := f.plane.Confirmations.Get(f.ctx, purpose.AISessionID, purpose.IntentID)
	if err != nil || intent.State != "COMPLETED" || intent.Commit == nil || intent.Content.RecoveryPurpose == nil {
		t.Fatalf("public purpose confirmation: %+v %v", intent, err)
	}
	sourceID := intent.Content.RecoveryPurpose.Proposal.SourceRunID
	before := formalExecutionCounts(t, f)
	// Bootstrap the actual Core drain with the already committed receipt. No
	// test call to ApplyConfirmed* or ContinueConfirmedPurposes is permitted.
	reopenRecoveryFixture(t, f, planeConfig)
	var count int
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if err = f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, purpose.IntentID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if count != 1 {
		t.Fatal("Core bootstrap did not consume the publicly confirmed purpose")
	}
	f.plane.Retirement.Close()
	var consumeRequest, executionID, revisionID string
	if err = f.plane.DB.SQL().QueryRow(`SELECT consume_request_id,successor_execution_id,successor_task_definition_revision_id FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, purpose.IntentID).Scan(&consumeRequest, &executionID, &revisionID); err != nil || !strings.HasPrefix(consumeRequest, "core_recovery_purpose_") || executionID == "" || revisionID == "" {
		t.Fatalf("Core consume identity: request=%s execution=%s revision=%s err=%v", consumeRequest, executionID, revisionID, err)
	}
	after := formalExecutionCounts(t, f)
	for _, table := range []string{"formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "runtime_selection_sources", "runtime_selection_bindings", "runs"} {
		if after[table] != before[table] {
			t.Fatalf("Core purpose consumption wrote forbidden %s: %d -> %d", table, before[table], after[table])
		}
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	owner := runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: revisionID, RuntimeRole: "IMPLEMENTATION"}
	authority, err := selections.ReadExecutionAuthority(f.ctx, owner, executionID)
	if err != nil || authority.Platform != original.Platform || authority.Values != original.Values || authority.AuthoritySnapshotHash != original.AuthoritySnapshotHash || authority.BindingID != "" || authority.PurposeConsumptionID == "" {
		t.Fatalf("purpose did not retain exact original authority: %+v %v", authority, err)
	}
	if _, err = selections.ReadCurrent(f.ctx, owner); !errors.Is(err, runtimeselection.ErrBindingMissing) {
		t.Fatalf("purpose successor invented a formal revision binding: %v", err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil || expected.ExecutionID != executionID || expected.TaskDefinitionRevisionID != revisionID || expected.Phase != task.PhaseImplementationReady {
		t.Fatalf("purpose successor exact start target: %+v %v", expected, err)
	}
	start := func() (executionproposal.StartResult, error) {
		return f.facade.StartTaskExecutionExpectedCommand(f.ctx, f.session, f.task, "START_EXECUTION", "recovery-purpose-native-start", expected)
	}
	started := runQualifiedNativeImplementation(t, f, root, runtime, start)
	if started.Proposal.ID != executionID || started.Run.ExecutionProposalID != executionID {
		t.Fatalf("native started a different purpose successor: %+v", started)
	}
	proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, executionID)
	if err != nil || proposal.VerificationRunID == "" {
		t.Fatalf("native successor Verification: %+v %v", proposal, err)
	}
	implementation, err := f.plane.Runs.Get(f.ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := f.plane.Runs.Get(f.ctx, proposal.VerificationRunID)
	if err != nil || verification.VerificationReport == nil || len(verification.VerificationReport.Checks) != 2 {
		t.Fatalf("native Verification command report: %+v %v", verification.VerificationReport, err)
	}
	for _, check := range verification.VerificationReport.Checks {
		if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 || check.Observed.Status != "PASSED" || !strings.Contains(check.Observed.Command, "test -f same.txt") {
			t.Fatalf("Verification lacks native exit-0 command evidence: %+v", check)
		}
	}
	for _, ran := range []string{implementation.ID, verification.ID} {
		binding, e := sqlite.NewAgentRuntimeStore(f.plane.DB).AppServerBinding(f.ctx, ran)
		if e != nil || binding.PID <= 0 || binding.CodexVersion == "" || binding.ExecutablePath != native || binding.ExecutableSHA256 == "" || binding.BackendConfigID != original.Values.BackendIdentity || binding.BackendFingerprint != original.Values.BackendConfigFingerprint {
			t.Fatalf("successor lacks actual native App Server binding: %+v %v", binding, e)
		}
		var conversationID, threadID, turnID, instanceID string
		if e = f.plane.DB.SQL().QueryRow(`SELECT b.agent_conversation_id,r.thread_id,r.turn_id,json_extract(s.snapshot_json,'$.appServerInstanceId') FROM agent_operation_boundaries b JOIN runs r ON r.id=b.run_id JOIN agent_conversations a ON a.id=b.agent_conversation_id JOIN agent_runtime_states s ON s.run_id=b.run_id WHERE b.run_id=? AND r.thread_id=a.backend_thread_id AND s.thread_id=r.thread_id AND s.turn_id=r.turn_id AND b.exit_boundary_turn_id=r.turn_id AND b.advance_state='KNOWN_ADVANCE'`, ran).Scan(&conversationID, &threadID, &turnID, &instanceID); e != nil || conversationID == "" || threadID == "" || turnID == "" || instanceID != binding.ID {
			t.Fatalf("native exact conversation/thread/turn binding: run=%s conversation=%s thread=%s turn=%s instance=%s binding=%s err=%v", ran, conversationID, threadID, turnID, instanceID, binding.ID, e)
		}
		writeRecoveryPurposeNativeEvidence(t, root, "appserver-"+ran+".json", binding)
		writeRecoveryPurposeNativeEvidence(t, root, "boundary-"+ran+".json", map[string]string{"runId": ran, "agentConversationId": conversationID, "threadId": threadID, "turnId": turnID, "appServerInstanceId": instanceID})
	}
	if implementation.RuntimeWorkset == nil || verification.RuntimeWorkset == nil || implementation.RuntimeWorkset.ContentHash != verification.RuntimeWorkset.ContentHash || implementation.RuntimeWorkset.Model != original.Values.Model || implementation.RuntimeWorkset.BackendConfigFingerprint != original.Values.BackendConfigFingerprint {
		t.Fatal("native successor changed the frozen workset/runtime during Verification")
	}
	var sourceClassification string
	if err = f.plane.DB.SQL().QueryRow(`SELECT advance_state FROM agent_operation_boundaries WHERE run_id=?`, sourceID).Scan(&sourceClassification); err != nil || sourceClassification != "UNKNOWN" {
		t.Fatalf("native successor rewrote original UNKNOWN boundary: %s %v", sourceClassification, err)
	}
	for name, value := range map[string]any{"formal-receipt.json": receipt, "original-authority.json": original, "successor-authority.json": authority, "implementation-run.json": implementation, "verification-run.json": verification, "purpose-confirmation.json": intent} {
		writeRecoveryPurposeNativeEvidence(t, root, name, value)
	}
	baseline := definitionRuntimeSnapshot(t, f, true)
	reopenRecoveryFixture(t, f, planeConfig)
	f.plane.Retirement.Close()
	if replay, e := start(); e != nil || replay.Run.ID != started.Run.ID || replay.Proposal.ID != executionID {
		t.Fatalf("cold exact purpose start replay: %+v %v", replay, e)
	}
	if definitionRuntimeSnapshot(t, f, true) != baseline {
		t.Fatal("cold purpose start replay wrote another Run/Turn or other durable facts")
	}
	commit := *intent.Commit
	commit.SubmittedHash = intent.Submission.PayloadSHA256
	if replay, e := f.facade.CommitConfirmation(f.ctx, commit); e != nil || replay.State != "COMPLETED" {
		t.Fatalf("consumed purpose confirmation replay: %+v %v", replay, e)
	}
	if got := formalExecutionCounts(t, f); got["runs"] != before["runs"]+2 || got["runtime_selection_sources"] != before["runtime_selection_sources"] || got["runtime_selection_bindings"] != before["runtime_selection_bindings"] {
		t.Fatalf("native successor duplicated Run or formal authority: before=%v after=%v", before, got)
	}
	writeRecoveryPurposeNativeEvidence(t, root, "result.json", map[string]any{"status": "PASS", "sourceOwner": "IMPLEMENTATION", "sourceDiagnostic": "injected observation fixture", "nativeScope": "Core consumption -> successor Implementation -> Verification", "nativeBinary": native, "consumeRequestId": consumeRequest, "successorExecutionId": executionID, "implementationRunId": implementation.ID, "verificationRunId": verification.ID, "observedVerificationCommands": 2, "commandExitCodes": []int{0, 0}, "sameProcessAndColdStartReplay": true, "newFormalBindings": 0})
	t.Logf("Core consume=%s native Implementation=%s Verification=%s; original 10 fields retained, two commands exited 0, cold replay created no Run/Turn", consumeRequest, implementation.ID, verification.ID)
}

func writeRecoveryPurposeNativeEvidence(t *testing.T, root, name string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(root, name), raw, 0600)
	}
	if err != nil {
		t.Fatal(err)
	}
}
