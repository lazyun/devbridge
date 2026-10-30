package sqlite

import (
	"context"
	"errors"
	"fmt"

	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"

	"github.com/lazyun/devbridge/internal/workspace"
)

func TestRecordFinalizationIntentRollsBackAsAUnit(t *testing.T) {
	f := newAgentStorageFixture(t, "intent-rollback")
	runValue, conversation := beginFinalizationRun(t, f, "intent-rollback")
	if _, err := f.db.SQL().Exec(`CREATE TRIGGER fail_intent_insert AFTER INSERT ON agent_finalization_intents BEGIN SELECT RAISE(ABORT,'injected intent failure'); END`); err != nil {
		t.Fatal(err)
	}
	err := NewAgentConversationStore(f.db).RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.NoAdvance, Quiesced: true})
	if err == nil {
		t.Fatal("RecordFinalization succeeded through injected failure")
	}
	assertFinalizationState(t, f.db, runValue.ID, conversation.ID, run.StatusPreparing, 0, agentconversation.NoAdvance, false, true)
}

func TestTerminalRunAndFinalizerAreAtomicAcrossCrashWindows(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{
			name:    "boundary application",
			trigger: `CREATE TRIGGER fail_boundary_apply BEFORE UPDATE ON agent_operation_boundaries WHEN NEW.quiesced=1 BEGIN SELECT RAISE(ABORT,'injected boundary failure'); END`,
		},
		{
			name:    "intent application marker",
			trigger: `CREATE TRIGGER fail_intent_apply BEFORE UPDATE ON agent_finalization_intents WHEN NEW.applied_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected intent apply failure'); END`,
		},
		{
			name:    "claim release",
			trigger: `CREATE TRIGGER fail_claim_release BEFORE DELETE ON feature_area_agent_claims BEGIN SELECT RAISE(ABORT,'injected claim release failure'); END`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentStorageFixture(t, "terminal-"+safeTestID(tc.name))
			runValue, conversation := beginFinalizationRun(t, f, "terminal-"+safeTestID(tc.name))
			store := NewAgentConversationStore(f.db)
			if err := store.RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.NoAdvance, Quiesced: true}); err != nil {
				t.Fatal(err)
			}
			assertFinalizationState(t, f.db, runValue.ID, conversation.ID, run.StatusPreparing, 1, agentconversation.NoAdvance, false, true)
			if _, err := f.db.SQL().Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}

			err := terminalizeRun(t, f.db, runValue.ID)
			if err == nil {
				t.Fatal("terminal update succeeded through injected failure")
			}
			// The external observation was committed before terminalization. The
			// failed terminal transaction preserves that unapplied intent and rolls
			// back every Run/boundary/claim mutation made by the finalizer.
			assertFinalizationState(t, f.db, runValue.ID, conversation.ID, run.StatusPreparing, 1, agentconversation.NoAdvance, false, true)
		})
	}
}

func TestPersistedFinalizationIntentConvergesAfterRestart(t *testing.T) {
	f := newAgentStorageFixture(t, "intent-restart")
	runValue, conversation := beginFinalizationRun(t, f, "intent-restart")
	if err := NewAgentConversationStore(f.db).RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.NoAdvance, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	assertFinalizationState(t, f.db, runValue.ID, conversation.ID, run.StatusPreparing, 1, agentconversation.NoAdvance, false, true)

	reopenAgentFixtureDB(t, &f)
	recovery := NewRecoveryStore(f.db)
	if err := recovery.RecoverAgentOperations(context.Background(), testNow.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertFinalizationState(t, f.db, runValue.ID, conversation.ID, run.StatusInfrastructureFailed, 1, agentconversation.NoAdvance, true, false)
}

func TestPersistedKnownBoundaryConvergesWithoutLosingHistory(t *testing.T) {
	f := newAgentStorageFixture(t, "known-restart")
	runValue, conversation := beginRecoveryRun(t, f, agentconversation.Execution, "known-restart", true)
	if err := NewAgentConversationStore(f.db).RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: "turn-known-restart", Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	reopenAgentFixtureDB(t, &f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	gotRun, err := NewRunStore(f.db).Get(context.Background(), runValue.ID)
	if err != nil || gotRun.Status != run.StatusInfrastructureFailed {
		t.Fatalf("Run=%+v err=%v", gotRun, err)
	}
	boundary, err := store.Boundary(context.Background(), runValue.ID)
	if err != nil || boundary.AgentConversationID != conversation.ID || boundary.Advance != agentconversation.KnownAdvance || boundary.ExitBoundaryTurnID != "turn-known-restart" || !boundary.Quiesced {
		t.Fatalf("known boundary=%+v err=%v", boundary, err)
	}
	current, err := store.Get(context.Background(), conversation.ID)
	if err != nil || current.EndedAt != nil || !current.ContinuationSafe || current.CurrentBoundaryTurnID != "turn-known-restart" {
		t.Fatalf("conversation=%+v err=%v", current, err)
	}
	if _, err := store.GetClaim(context.Background(), f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("known-safe claim err=%v", err)
	}
}

func TestUnknownRecoveryRequiresFencedExactOwnerAndIsReplayable(t *testing.T) {
	f, input, oldBoundary := prepareOldUnknownRecovery(t, "unknown-exact")
	store := NewAgentConversationStore(f.db)
	state, err := store.State(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "RECOVERY_REQUIRED" || state.Claim == nil || state.Claim.RuntimeEpoch != inputEpoch(t, f.db, input.RunID) {
		t.Fatalf("current epoch state=%+v", state)
	}
	if _, err := store.Recover(context.Background(), input); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("current epoch Recover err=%v, want restart-required busy", err)
	}

	reopenAgentFixtureDB(t, &f)
	if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(5*time.Hour)); err != nil {
		t.Fatal(err)
	}
	store = NewAgentConversationStore(f.db)
	state, err = store.State(context.Background(), f.area.ID)
	if err != nil || state.Status != "RECOVERY_REQUIRED" || state.Claim == nil || state.Claim.RunID != input.RunID || state.AgentConversationID != input.AgentConversationID {
		t.Fatalf("fenced UNKNOWN state=%+v err=%v", state, err)
	}

	wrong := []struct {
		name   string
		mutate func(*agentconversation.RecoverInput)
	}{
		{"feature area", func(v *agentconversation.RecoverInput) { v.FeatureAreaID = "wrong-area" }},
		{"Run", func(v *agentconversation.RecoverInput) { v.RunID = "wrong-run" }},
		{"operation kind", func(v *agentconversation.RecoverInput) { v.OperationKind = agentconversation.Review }},
		{"operation", func(v *agentconversation.RecoverInput) { v.OperationID = "wrong-operation" }},
		{"conversation", func(v *agentconversation.RecoverInput) { v.AgentConversationID = "wrong-conversation" }},
	}
	for _, tc := range wrong {
		t.Run("reject wrong "+tc.name, func(t *testing.T) {
			candidate := input
			candidate.RequestID = "wrong-" + safeTestID(tc.name)
			tc.mutate(&candidate)
			if _, err := store.Recover(context.Background(), candidate); !errors.Is(err, agentconversation.ErrConflict) && !errors.Is(err, agentconversation.ErrNotFound) {
				t.Fatalf("Recover err=%v, want exact-owner rejection", err)
			}
			assertUnknownRecoveryUnchanged(t, f.db, input, oldBoundary)
		})
	}

	result, err := store.Recover(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.OldConversationID != input.AgentConversationID || result.NewConversationID == "" || result.NewConversationID == input.AgentConversationID || result.Boundary != agentconversation.Unknown || !result.ContextAbandoned || !result.FreshWorkAllowed {
		t.Fatalf("recovery result=%+v", result)
	}
	replayed, err := store.Recover(context.Background(), input)
	if err != nil || replayed != result {
		t.Fatalf("replay=%+v err=%v, want %+v", replayed, err, result)
	}
	conflictingReplay := input
	conflictingReplay.RunID = "different-run"
	if _, err := store.Recover(context.Background(), conflictingReplay); !errors.Is(err, agentconversation.ErrConflict) {
		t.Fatalf("conflicting request replay err=%v", err)
	}

	boundary, err := store.Boundary(context.Background(), input.RunID)
	if err != nil || boundary != oldBoundary {
		t.Fatalf("old boundary changed: got=%+v want=%+v err=%v", boundary, oldBoundary, err)
	}
	old, err := store.Get(context.Background(), input.AgentConversationID)
	if err != nil || old.EndedAt == nil || old.RotationReason != agentconversation.UnsafeContextBoundary || old.ContinuationSafe {
		t.Fatalf("old conversation=%+v err=%v", old, err)
	}
	next, err := store.Get(context.Background(), result.NewConversationID)
	if err != nil || next.PredecessorID != old.ID || next.BackendThreadID != "" || next.CurrentBoundaryTurnID != "" || !next.ContinuationSafe {
		t.Fatalf("new conversation=%+v err=%v", next, err)
	}
	if _, err := store.GetClaim(context.Background(), f.area.ID); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("claim after recovery err=%v", err)
	}
	workspaceValue, err := NewWorkspaceStore(f.db).Get(context.Background(), "workspace-unknown-exact")
	if err != nil || !workspaceValue.RuntimeQuarantined || workspaceValue.Status != workspace.StatusContinuityUnavailable {
		t.Fatalf("workspace=%+v err=%v", workspaceValue, err)
	}
	state, err = store.State(context.Background(), f.area.ID)
	if err != nil || state.Status != "READY" || state.Claim != nil || state.AgentConversationID != next.ID {
		t.Fatalf("post-recovery state=%+v err=%v", state, err)
	}
}

func TestStateCurrentEpochActiveUnknownRemainsBusy(t *testing.T) {
	f := newAgentStorageFixture(t, "current-unknown-state")
	runValue, conversation := beginRecoveryRun(t, f, agentconversation.Execution, "current-unknown-state", true)
	store := NewAgentConversationStore(f.db)
	state, err := store.State(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "BUSY" || state.Claim == nil || state.Claim.RunID != runValue.ID || state.AgentConversationID != conversation.ID || !strings.Contains(state.Message, "Wait for that operation to finish") || strings.Contains(strings.ToLower(state.Message), "recover") || strings.Contains(strings.ToLower(state.Message), "restart") {
		t.Fatalf("current UNKNOWN state=%+v", state)
	}
	claim := *state.Claim
	input := agentconversation.RecoverInput{FeatureAreaID: claim.FeatureAreaID, RunID: claim.RunID, OperationKind: claim.Kind, OperationID: claim.OperationID, AgentConversationID: conversation.ID, RequestID: "recover-active-current"}
	if _, err := store.Recover(context.Background(), input); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("active current epoch Recover err=%v", err)
	}
}

func TestUnknownRecoveryRollsBackAcrossCrashWindows(t *testing.T) {
	tests := []struct {
		name    string
		trigger string
	}{
		{"conversation rotation", `CREATE TRIGGER fail_recovery_rotation BEFORE UPDATE OF ended_at ON agent_conversations WHEN NEW.ended_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected recovery rotation failure'); END`},
		{"workspace quarantine", `CREATE TRIGGER fail_recovery_workspace BEFORE UPDATE OF runtime_quarantined ON workspaces WHEN NEW.runtime_quarantined=1 BEGIN SELECT RAISE(ABORT,'injected workspace quarantine failure'); END`},
		{"claim release", `CREATE TRIGGER fail_recovery_claim BEFORE DELETE ON feature_area_agent_claims BEGIN SELECT RAISE(ABORT,'injected recovery claim failure'); END`},
		{"recovery receipt", `CREATE TRIGGER fail_recovery_receipt BEFORE INSERT ON agent_operation_recoveries BEGIN SELECT RAISE(ABORT,'injected recovery receipt failure'); END`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			label := "rollback-" + safeTestID(tc.name)
			f, input, oldBoundary := prepareOldUnknownRecovery(t, label)
			reopenAgentFixtureDB(t, &f)
			if err := NewRecoveryStore(f.db).RecoverAgentOperations(context.Background(), testNow.Add(5*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.SQL().Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := NewAgentConversationStore(f.db).Recover(context.Background(), input); err == nil {
				t.Fatal("Recover succeeded through injected crash window")
			}
			assertUnknownRecoveryUnchanged(t, f.db, input, oldBoundary)
		})
	}
}

func beginFinalizationRun(t *testing.T, f agentStorageFixture, label string) (run.Run, agentconversation.Conversation) {
	t.Helper()
	op := prepareAgentOperation(t, f, agentconversation.Review, label)
	runValue, err := op.make(context.Background(), "run-"+label)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
	conversation, err := NewAgentConversationStore(f.db).BeginRun(context.Background(), runValue.ID)
	if err != nil {
		t.Fatal(err)
	}
	return runValue, conversation
}

func prepareOldUnknownRecovery(t *testing.T, label string) (agentStorageFixture, agentconversation.RecoverInput, agentconversation.Boundary) {
	t.Helper()
	f := newAgentStorageFixture(t, label)
	runValue, conversation := beginRecoveryRun(t, f, agentconversation.Execution, label, true)
	workspaceID := "workspace-" + label
	if err := NewWorkspaceStore(f.db).Create(context.Background(), workspace.Workspace{ID: workspaceID, ProjectID: f.task.ProjectID, RepositoryID: f.task.RepositoryID, FeatureAreaID: f.task.FeatureAreaID, BaseCommit: f.task.BaseCommit, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.SQL().Exec(`UPDATE runs SET workspace_id=? WHERE id=?`, workspaceID, runValue.ID); err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	claim, err := store.GetClaim(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFinalization(context.Background(), runValue.ID, agentconversation.Finalization{Advance: agentconversation.Unknown, Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	if err := terminalizeRun(t, f.db, runValue.ID); err != nil {
		t.Fatal(err)
	}
	boundary, err := store.Boundary(context.Background(), runValue.ID)
	if err != nil || boundary.Advance != agentconversation.Unknown || boundary.Quiesced || boundary.ExitBoundaryTurnID != "" {
		t.Fatalf("UNKNOWN boundary=%+v err=%v", boundary, err)
	}
	return f, agentconversation.RecoverInput{FeatureAreaID: claim.FeatureAreaID, RunID: claim.RunID, OperationKind: claim.Kind, OperationID: claim.OperationID, AgentConversationID: conversation.ID, RequestID: "recover-" + label}, boundary
}

func assertUnknownRecoveryUnchanged(t *testing.T, db *DB, input agentconversation.RecoverInput, wantBoundary agentconversation.Boundary) {
	t.Helper()
	store := NewAgentConversationStore(db)
	claim, err := store.GetClaim(context.Background(), input.FeatureAreaID)
	if err != nil || claim.RunID != input.RunID || claim.OperationID != input.OperationID || claim.Kind != input.OperationKind {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	boundary, err := store.Boundary(context.Background(), input.RunID)
	if err != nil || boundary != wantBoundary {
		t.Fatalf("boundary=%+v want=%+v err=%v", boundary, wantBoundary, err)
	}
	old, err := store.Get(context.Background(), input.AgentConversationID)
	if err != nil || old.EndedAt != nil {
		t.Fatalf("old conversation=%+v err=%v", old, err)
	}
	var receipts int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM agent_operation_recoveries WHERE feature_area_id=?`, input.FeatureAreaID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("recovery receipts=%d after rollback", receipts)
	}
	workspaceValue, err := NewWorkspaceStore(db).Get(context.Background(), "workspace-"+input.RequestID[len("recover-"):])
	if err != nil || workspaceValue.RuntimeQuarantined || workspaceValue.Status != workspace.StatusReady {
		t.Fatalf("workspace after rollback=%+v err=%v", workspaceValue, err)
	}
}

func inputEpoch(t *testing.T, db *DB, runID string) string {
	t.Helper()
	var epoch string
	if err := db.SQL().QueryRow(`SELECT runtime_epoch FROM runs WHERE id=?`, runID).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	return epoch
}

func terminalizeRun(t *testing.T, db *DB, runID string) error {
	t.Helper()
	finished := testNow.Add(3 * time.Hour)
	_, err := NewRunStore(db).Update(context.Background(), runID, func(value *run.Run) error {
		value.Status = run.StatusSucceeded
		value.FinishedAt = &finished
		value.UpdatedAt = finished
		value.TokenUsage.Status = run.TokenUsageUnavailable
		return nil
	})
	return err
}

func assertFinalizationState(t *testing.T, db *DB, runID, conversationID string, wantRun run.Status, wantIntent int, wantAdvance agentconversation.Advance, wantQuiesced, wantClaim bool) {
	t.Helper()
	gotRun, err := NewRunStore(db).Get(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRun.Status != wantRun {
		t.Fatalf("Run status=%s, want %s", gotRun.Status, wantRun)
	}
	var intentCount, appliedCount int
	if err := db.SQL().QueryRow(`SELECT COUNT(*),COUNT(applied_at) FROM agent_finalization_intents WHERE run_id=?`, runID).Scan(&intentCount, &appliedCount); err != nil {
		t.Fatal(err)
	}
	if intentCount != wantIntent {
		t.Fatalf("intent count=%d applied=%d, want count=%d", intentCount, appliedCount, wantIntent)
	}
	if wantIntent == 1 && wantQuiesced && appliedCount != 1 {
		t.Fatalf("terminal intent applied=%d, want 1", appliedCount)
	}
	if wantIntent == 1 && !wantQuiesced && appliedCount != 0 {
		t.Fatalf("preterminal intent applied=%d, want 0", appliedCount)
	}
	boundary, err := NewAgentConversationStore(db).Boundary(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if boundary.AgentConversationID != conversationID || boundary.Advance != wantAdvance || boundary.Quiesced != wantQuiesced || boundary.ExitBoundaryTurnID != "" {
		t.Fatalf("boundary=%+v", boundary)
	}
	_, err = NewAgentConversationStore(db).GetClaim(context.Background(), boundaryFeatureArea(t, db, runID))
	if wantClaim && err != nil {
		t.Fatalf("claim missing: %v", err)
	}
	if !wantClaim && !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("claim retained after safe finalization: %v", err)
	}
}

func boundaryFeatureArea(t *testing.T, db *DB, runID string) string {
	t.Helper()
	var featureAreaID string
	if err := db.SQL().QueryRow(`SELECT feature_area_id FROM runs WHERE id=?`, runID).Scan(&featureAreaID); err != nil {
		t.Fatal(err)
	}
	return featureAreaID
}

func safeTestID(value string) string {
	return fmt.Sprintf("%x", value)
}
