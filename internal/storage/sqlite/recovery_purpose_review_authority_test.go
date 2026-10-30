package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
)

func reviewPurposeEvidenceFixture(t *testing.T) (*DB, context.Context, confirmation.Intent) {
	t.Helper()
	var db *DB
	var ctx context.Context
	var intent confirmation.Intent
	runReviewPurposeFlowBeforeCommit(t, func(t *testing.T, database *DB, caller context.Context, _ *RecoveryStatusStore, _ rs.Spec, _ rs.Record, prepared confirmation.Intent) confirmation.Intent {
		db, ctx, intent = database, caller, prepared
		return prepared
	})
	return db, ctx, intent
}

func TestRecoveryReviewPurposePreservesUnknownAndPermitsSuccessorPreparation(t *testing.T) {
	db, ctx, intent := reviewPurposeEvidenceFixture(t)
	p := intent.Content.RecoveryPurpose.Proposal
	sources, err := recoveryReviewPurposeResolvedSourceRunsTx(ctx, db.sql, p.FeatureAreaID)
	if err != nil || !reflect.DeepEqual(sources, []string{p.SourceRunID}) {
		t.Fatalf("exact Review source evidence: %v %v", sources, err)
	}
	state, err := readRuntimeAuthority(ctx, db.sql, p.FeatureAreaID)
	if err != nil || state.RecoveryRequired {
		t.Fatalf("consumed Review still blocks successor preparation: %+v %v", state, err)
	}
	agents := NewAgentConversationStore(db)
	before, err := agents.Boundary(ctx, p.SourceRunID)
	if err != nil || before.Advance != agentconversation.Unknown {
		t.Fatalf("historical UNKNOWN was rewritten: %+v %v", before, err)
	}
	var successor, preparedRun string
	if err = db.sql.QueryRowContext(ctx, `SELECT c.successor_review_id,r.id FROM recovery_purpose_confirmation_consumptions c JOIN runs r ON r.review_id=c.successor_review_id WHERE c.intent_id=?`, intent.IntentID).Scan(&successor, &preparedRun); err != nil {
		t.Fatalf("successor did not prepare its separate Run: %v", err)
	}
	if _, err = run.NewService(NewRunStore(db), nil, nil).Transition(ctx, preparedRun, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	next, err := agents.BeginRun(ctx, preparedRun)
	if err != nil || next.ID == before.AgentConversationID || next.PredecessorID != before.AgentConversationID || !next.ContinuationSafe || next.BackendThreadID != "" {
		t.Fatalf("successor did not rotate the unsafe source conversation: %+v %v", next, err)
	}
	after, err := agents.Boundary(ctx, p.SourceRunID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("preparation rewrote source UNKNOWN: before=%+v after=%+v err=%v", before, after, err)
	}
	// Historical authority remains provable after preparation authority has been
	// superseded; it does not grant another current Review start.
	purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runtime_preparation_authorities")
	if _, err = db.sql.ExecContext(ctx, `UPDATE runtime_preparation_authorities SET disposition='SUPERSEDED' WHERE owner_kind='REVIEW' AND owner_id=?`, successor); err != nil {
		t.Fatal(err)
	}
	if sources, err = recoveryReviewPurposeResolvedSourceRunsTx(ctx, db.sql, p.FeatureAreaID); err != nil || !reflect.DeepEqual(sources, []string{p.SourceRunID}) {
		t.Fatalf("historical Review source requires live preparation authority: %v %v", sources, err)
	}
	root := db.stateRoot
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	cold := openTestDB(t, root)
	defer cold.Close()
	if sources, err = recoveryReviewPurposeResolvedSourceRunsTx(ctx, cold.sql, p.FeatureAreaID); err != nil || !reflect.DeepEqual(sources, []string{p.SourceRunID}) {
		t.Fatalf("cold historical Review source changed: %v %v", sources, err)
	}
}

func TestRecoveryReviewPurposeRejectsCorruptArchive(t *testing.T) {
	for _, corruption := range []string{"receipt", "typed-commit", "plan-rehashed", "preparation-authority", "successor-instruction", "source-turn", "missing-diagnostic-child"} {
		t.Run(corruption, func(t *testing.T) {
			db, ctx, intent := reviewPurposeEvidenceFixture(t)
			p := intent.Content.RecoveryPurpose.Proposal
			var err error
			switch corruption {
			case "receipt":
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "recovery_purpose_confirmation_receipts")
				_, err = db.sql.ExecContext(ctx, `UPDATE recovery_purpose_confirmation_receipts SET selected_variant_id='foreign' WHERE intent_id=?`, intent.IntentID)
			case "typed-commit":
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "confirmation_commit_requests")
				_, err = db.sql.ExecContext(ctx, `UPDATE confirmation_commit_requests SET payload_sha256=? WHERE intent_id=?`, strings.Repeat("f", 64), intent.IntentID)
			case "plan-rehashed":
				var planID string
				if err = db.sql.QueryRowContext(ctx, `SELECT plan_id FROM runtime_scope_plans WHERE source_receipt_id=?`, intent.IntentID).Scan(&planID); err != nil {
					t.Fatal(err)
				}
				plan, readErr := readArchivedRuntimeSelectionPlanTx(ctx, db.sql, planID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				plan.Model = "substituted-model"
				plan.ContentHash, err = plan.ComputeContentHash()
				if err != nil {
					t.Fatal(err)
				}
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runtime_scope_plans")
				_, err = db.sql.ExecContext(ctx, `UPDATE runtime_scope_plans SET model=?,content_hash=? WHERE plan_id=?`, plan.Model, plan.ContentHash, planID)
			case "preparation-authority":
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runtime_preparation_authorities")
				_, err = db.sql.ExecContext(ctx, `UPDATE runtime_preparation_authorities SET payload_hash=? WHERE plan_id IN(SELECT plan_id FROM runtime_scope_plans WHERE source_receipt_id=?)`, strings.Repeat("f", 64), intent.IntentID)
			case "successor-instruction":
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "reviews")
				_, err = db.sql.ExecContext(ctx, `UPDATE reviews SET instruction='unconfirmed instruction' WHERE id=(SELECT successor_review_id FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?)`, intent.IntentID)
			case "source-turn":
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runs")
				_, err = db.sql.ExecContext(ctx, `UPDATE runs SET turn_id='foreign-turn' WHERE id=?`, p.SourceRunID)
			case "missing-diagnostic-child":
				record, readErr := NewRecoveryStatusStore(db).Read(ctx, p.RecoveryID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				record.RecoveryRunID = "missing-child"
				raw, encodeErr := encodeRecovery(record)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				purposeDropTableTriggersForCorruptionTest(t, ctx, db, "recovery_status_records")
				_, err = db.sql.ExecContext(ctx, `UPDATE recovery_status_records SET record_json=? WHERE id=?`, string(raw), record.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if sources, err := recoveryReviewPurposeResolvedSourceRunsTx(ctx, db.sql, p.FeatureAreaID); err == nil {
				t.Fatalf("corrupt %s resolved a source: %v", corruption, sources)
			}
			if _, err = readRuntimeAuthority(ctx, db.sql, p.FeatureAreaID); err == nil {
				t.Fatalf("corrupt %s disappeared through progression", corruption)
			}
		})
	}
}

func TestRecoveryReviewPurposeDoesNotClearUnrelatedUnknown(t *testing.T) {
	db, ctx, intent := reviewPurposeEvidenceFixture(t)
	p := intent.Content.RecoveryPurpose.Proposal
	var successorRun string
	if err := db.sql.QueryRowContext(ctx, `SELECT r.id FROM runs r JOIN recovery_purpose_confirmation_consumptions c ON c.successor_review_id=r.review_id WHERE c.intent_id=?`, intent.IntentID).Scan(&successorRun); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','independent unresolved successor',?)`, successorRun, db.epoch); err != nil {
		t.Fatal(err)
	}
	state, err := readRuntimeAuthority(ctx, db.sql, p.FeatureAreaID)
	if err != nil || !state.RecoveryRequired {
		t.Fatalf("Review purpose hid an unrelated UNKNOWN: %+v %v", state, err)
	}
	encoded, err := recoveryPurposeResolvedSourceRunsTx(ctx, db.sql, p.FeatureAreaID)
	var sources []string
	if err != nil || json.Unmarshal([]byte(encoded), &sources) != nil || !reflect.DeepEqual(sources, []string{p.SourceRunID}) {
		t.Fatalf("source exemption expanded to another Run: %s %v", encoded, err)
	}
}

func TestRecoveryReviewPurposeRejectsFrozenConfigurationChanges(t *testing.T) {
	for _, corruption := range []struct {
		name, assignment string
	}{
		{"codex-config", `codex_config_id=NULL`},
		{"model", `model=COALESCE(model,'') || '-changed'`},
		{"reasoning-effort", `reasoning_effort=CASE WHEN reasoning_effort='low' THEN 'high' ELSE 'low' END`},
		{"runtime-model", `runtime_config_json=json_set(runtime_config_json,'$.model','changed-model')`},
		{"runtime-reasoning-effort", `runtime_config_json=json_set(runtime_config_json,'$.reasoningEffort',CASE WHEN json_extract(runtime_config_json,'$.reasoningEffort')='low' THEN 'high' ELSE 'low' END)`},
		{"runtime-fast-mode", `runtime_config_json=json_set(runtime_config_json,'$.codexFastMode',json(CASE WHEN json_extract(runtime_config_json,'$.codexFastMode') THEN 'false' ELSE 'true' END))`},
		{"runtime-service-tier", `runtime_config_json=json_set(runtime_config_json,'$.serviceTier',CASE WHEN json_extract(runtime_config_json,'$.serviceTier')='priority' THEN '' ELSE 'priority' END)`},
		{"runtime-confirmation", `runtime_confirmation_id=COALESCE(runtime_confirmation_id,'') || '-changed'`},
		{"backend-config", `backend_selection_json=json_set(backend_selection_json,'$.configId','changed-config')`},
		{"backend-fingerprint", `backend_selection_json=json_set(backend_selection_json,'$.configFingerprint','changed-fingerprint')`},
		{"backend-name", `backend_selection_json=json_set(backend_selection_json,'$.configName','changed-name')`},
		{"backend-source", `backend_selection_json=json_set(backend_selection_json,'$.source','changed-source')`},
		{"backend-removed", `backend_selection_json='null'`},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			db, ctx, intent := reviewPurposeEvidenceFixture(t)
			p := intent.Content.RecoveryPurpose.Proposal
			var successorID string
			if err := db.sql.QueryRowContext(ctx, `SELECT successor_review_id FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, intent.IntentID).Scan(&successorID); err != nil {
				t.Fatal(err)
			}
			if source, err := readRecoveryReviewPurposeEvidenceTx(ctx, db.sql, p.FeatureAreaID, successorID); err != nil || source != p.SourceRunID {
				t.Fatalf("uncorrupted Review authority: source=%s err=%v", source, err)
			}
			before, err := scanReview(db.sql.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, successorID))
			if err != nil || before.CodexConfigID == "" || before.BackendSelection == nil {
				t.Fatalf("fixture lacks frozen backend configuration: %+v %v", before, err)
			}
			purposeDropTableTriggersForCorruptionTest(t, ctx, db, "reviews")
			if _, err = db.sql.ExecContext(ctx, `UPDATE reviews SET `+corruption.assignment+` WHERE id=?`, successorID); err != nil {
				t.Fatal(err)
			}
			after, err := scanReview(db.sql.QueryRowContext(ctx, reviewSelect+` WHERE r.id=?`, successorID))
			if err != nil || reflect.DeepEqual(before, after) {
				t.Fatalf("corruption did not change the decoded configuration: %v", err)
			}
			if _, err = readRecoveryReviewPurposeEvidenceTx(ctx, db.sql, p.FeatureAreaID, successorID); !errors.Is(err, rs.ErrPurposeReceiptMismatch) {
				t.Fatalf("changed frozen %s accepted or failed for another reason: %v", corruption.name, err)
			}
			if _, err = readRuntimeAuthority(ctx, db.sql, p.FeatureAreaID); !errors.Is(err, rs.ErrPurposeReceiptMismatch) {
				t.Fatalf("changed frozen %s did not block progression: %v", corruption.name, err)
			}
		})
	}
}
