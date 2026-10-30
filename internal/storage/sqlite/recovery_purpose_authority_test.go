package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

func consumeExecutionPurposeForTest(ctx context.Context, db *DB, in rs.PurposeConsumeInput) (rs.PurposeConsumption, error) {
	if in.Action == rs.ApplyConfirmedVerificationPurpose {
		return NewRecoveryPurposeStore(db).ApplyConfirmedVerificationPurpose(ctx, in)
	}
	return NewRecoveryPurposeStore(db).ApplyConfirmedImplementationPurpose(ctx, in)
}

func purposeExecutionPlanForTest(t *testing.T, ctx context.Context, db *DB, out rs.PurposeConsumption) runtimepreparation.Plan {
	t.Helper()
	var id, request, payload, authority string
	if err := db.sql.QueryRowContext(ctx, `SELECT runtime_scope_plan_id,admission_request_id,payload_sha256,runtime_preparation_authority_id FROM execution_proposal_admissions WHERE proposal_id=? AND task_id=? AND task_definition_revision_id=?`, out.ExecutionID, out.TaskID, out.TaskDefinitionRevisionID).Scan(&id, &request, &payload, &authority); err != nil {
		t.Fatalf("purpose successor lacks its exact durable admission: %v", err)
	}
	p, err := readArchivedRuntimeSelectionPlanTx(ctx, db.sql, id)
	if err != nil || p.OwnerKind != runtimepreparation.OwnerExecution || p.OwnerID != out.ExecutionID || p.TaskDefinitionRevisionID != out.TaskDefinitionRevisionID || p.PreparationRequestID != request || p.PreparationPayloadHash != payload || authority != id {
		t.Fatalf("purpose admission/plan mismatch: %+v err=%v", p, err)
	}
	return p
}

func purposeCurrentAuthorityForTest(ctx context.Context, db *DB, p runtimepreparation.Plan) (runtimeselection.CurrentAuthority, error) {
	return NewRuntimeSelectionStore(db).ReadExecutionAuthority(ctx, runtimeselection.Owner{ProjectID: p.ProjectID, TaskID: p.TaskID, TaskDefinitionRevisionID: p.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}, p.OwnerID)
}

func TestRecoveryPurposeAdmissionPreparesImplementationAfterConsumption(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, owner, executionPurposeHooks{afterConsume: func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, in rs.PurposeConsumeInput, out rs.PurposeConsumption) {
				p := purposeExecutionPlanForTest(t, ctx, db, out)
				conversationStore := NewAgentConversationStore(db)
				sourceBoundary, err := conversationStore.Boundary(ctx, intent.Content.RecoveryPurpose.Proposal.SourceRunID)
				if err != nil || sourceBoundary.Advance != agentconversation.Unknown {
					t.Fatalf("fixture must retain its unresolved source boundary: %+v %v", sourceBoundary, err)
				}
				x, err := NewExecutionProposalStore(db).Get(ctx, out.ExecutionID)
				if err != nil || x.Status != executionproposal.StatusAdmitted || x.RunID != "" || x.Outcome != executionproposal.OutcomePending {
					t.Fatalf("purpose did not admit a Run-free successor: %+v err=%v", x, err)
				}
				if p.SourceCommand != in.Action || p.SourceReceiptID != intent.IntentID || p.SelectedVariantID != "revised" || p.ProposalHash != intent.Content.RecoveryPurpose.Proposal.SubmittedHash {
					t.Fatalf("plan lost exact purpose provenance: %+v", p)
				}
				authority, err := purposeCurrentAuthorityForTest(ctx, db, p)
				if err != nil {
					t.Fatalf("read legitimate purpose execution authority: %v", err)
				}
				if authority.PurposeConsumptionID == "" || authority.BindingID != "" {
					t.Fatalf("purpose authority must identify its consumption without a formal binding: %+v", authority)
				}
				want := runtimeselection.AuthorityValues{BackendIdentity: p.BackendIdentity, BackendConfigFingerprint: p.BackendConfigFingerprint, BackendSource: p.BackendSource, BackendKind: "CODEX", Model: p.Model, ReasoningEffort: p.ReasoningEffort, CodexFastMode: p.CodexFastMode, ServiceTier: p.ServiceTier, RuntimeAdapterIdentity: p.RuntimeAdapterIdentity, RuntimeProtocolIdentity: p.RuntimeProtocolIdentity}
				if authority.Values != want || authority.AuthoritySnapshotHash != p.AuthoritySnapshotHash || p.AuthoritySnapshotHash != runtimeselection.HashValue(want) {
					t.Fatalf("purpose authority fields differ: got=%+v plan=%+v", authority, p)
				}
				if _, err = NewRuntimeSelectionStore(db).ReadCurrent(ctx, authority.Owner); !errors.Is(err, runtimeselection.ErrBindingMissing) {
					t.Fatalf("purpose consumption escaped through formal revision binding: %v", err)
				}
				wrongOwner := authority.Owner
				wrongOwner.TaskDefinitionRevisionID = intent.Content.RecoveryPurpose.Proposal.TaskDefinitionRevisionID
				if _, err = NewRuntimeSelectionStore(db).ReadExecutionAuthority(ctx, wrongOwner, out.ExecutionID); err == nil {
					t.Fatal("purpose successor granted authority to its predecessor revision")
				}
				var sourceCount, bindingCount, startCount, attemptCount int
				if err = db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM runtime_selection_sources WHERE revision_id=?),(SELECT count(*) FROM runtime_selection_bindings WHERE revision_id=?),(SELECT count(*) FROM runtime_start_authorities WHERE plan_id=?),(SELECT count(*) FROM runtime_preparation_attempts WHERE plan_id=?)`, out.TaskDefinitionRevisionID, out.TaskDefinitionRevisionID, p.ID, p.ID).Scan(&sourceCount, &bindingCount, &startCount, &attemptCount); err != nil || sourceCount != 0 || bindingCount != 0 || startCount != 0 || attemptCount != 0 {
					t.Fatalf("purpose consume minted formal/platform/runtime authority: source=%d binding=%d start=%d attempt=%d err=%v", sourceCount, bindingCount, startCount, attemptCount, err)
				}
				created, err := NewRunStore(db).CreatePreparationRun(ctx, p.ID, "run_purpose_successor", p.PreparationRequestID, p.PreparationPayloadHash, time.Now().UTC())
				if err != nil || created.Purpose != run.PurposeImplementation || created.ExecutionProposalID != out.ExecutionID || created.RuntimeWorkset != nil || created.TurnID != "" {
					t.Fatalf("separate preparation did not create an unbound Implementation Run: %+v err=%v", created, err)
				}
				replay, err := NewRunStore(db).CreatePreparationRun(ctx, p.ID, created.ID, p.PreparationRequestID, p.PreparationPayloadHash, time.Now().UTC())
				if err != nil || replay.ID != created.ID {
					t.Fatalf("preparation exact replay=%+v err=%v", replay, err)
				}
				readPlan, err := NewRunStore(db).ReadExecutionPreparationPlan(ctx, created.ID)
				if err != nil || readPlan.ID != p.ID {
					t.Fatalf("worker cannot read admitted purpose plan: %+v err=%v", readPlan, err)
				}
				if err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runtime_start_authorities WHERE plan_id=?`, p.ID).Scan(&startCount); err != nil || startCount != 0 {
					t.Fatalf("Run preparation prematurely authorized dispatch: %d %v", startCount, err)
				}
				if _, err = run.NewService(NewRunStore(db), nil, nil).Transition(ctx, created.ID, run.StatusPreparing); err != nil {
					t.Fatalf("prepare admitted purpose Run: %v", err)
				}
				nextConversation, err := conversationStore.BeginRun(ctx, created.ID)
				if err != nil || nextConversation.ID == sourceBoundary.AgentConversationID || nextConversation.PredecessorID != sourceBoundary.AgentConversationID || !nextConversation.ContinuationSafe || nextConversation.BackendThreadID != "" {
					t.Fatalf("successor reused an unsafe source conversation: %+v %v", nextConversation, err)
				}
				oldConversation, err := conversationStore.Get(ctx, sourceBoundary.AgentConversationID)
				if err != nil || oldConversation.EndedAt == nil || oldConversation.ContinuationSafe {
					t.Fatalf("source conversation was not preserved and rotated: %+v %v", oldConversation, err)
				}
				unchanged, err := conversationStore.Boundary(ctx, sourceBoundary.RunID)
				if err != nil || !reflect.DeepEqual(sourceBoundary, unchanged) {
					t.Fatalf("successor rewrote historical UNKNOWN evidence: before=%+v after=%+v err=%v", sourceBoundary, unchanged, err)
				}
			}})
		})
	}
}

func TestRecoveryPurposeConsumptionDoesNotClearOtherUnknownOperations(t *testing.T) {
	for _, kind := range []string{"earlier-operation", "later-conversation-boundary"} {
		t.Run(kind, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, "VERIFICATION", executionPurposeHooks{afterConsume: func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, _ rs.PurposeConsumeInput, out rs.PurposeConsumption) {
				p := purposeExecutionPlanForTest(t, ctx, db, out)
				source := intent.Content.RecoveryPurpose.Proposal
				var differentRun string
				if err := db.sql.QueryRowContext(ctx, `SELECT run_id FROM execution_proposals WHERE id=?`, source.SourceExecutionID).Scan(&differentRun); err != nil || differentRun == "" || differentRun == source.SourceRunID {
					t.Fatalf("missing independent old Implementation Run: %q %v", differentRun, err)
				}
				var err error
				if kind == "earlier-operation" {
					_, err = db.sql.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','unresolved independent operation',?)`, differentRun, db.epoch)
				} else {
					var conversation string
					if err = db.sql.QueryRowContext(ctx, `SELECT agent_conversation_id FROM agent_operation_boundaries WHERE run_id=?`, source.SourceRunID).Scan(&conversation); err != nil {
						t.Fatal(err)
					}
					// A newly recorded independent unresolved boundary must prevent
					// the consumed source from masking this conversation's later facts.
					_, err = db.sql.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,advance_state,quiesced) VALUES(?,?,'UNKNOWN',1)`, differentRun, conversation)
				}
				if err != nil {
					t.Fatal(err)
				}
				state, err := readRuntimeAuthority(ctx, db.sql, p.FeatureAreaID)
				if err != nil || !state.RecoveryRequired {
					t.Fatalf("purpose consumption hid independent recovery requirement: %+v %v", state, err)
				}
				if _, err = NewRunStore(db).CreatePreparationRun(ctx, p.ID, "run_unrelated_unknown", p.PreparationRequestID, p.PreparationPayloadHash, time.Now().UTC()); err == nil {
					t.Fatal("purpose consumption bypassed another unresolved operation")
				}
				var count int
				if err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE execution_proposal_id=?`, out.ExecutionID).Scan(&count); err != nil || count != 0 {
					t.Fatalf("blocked preparation wrote a successor Run: count=%d err=%v", count, err)
				}
			}})
		})
	}
}

func TestRecoveryPurposeAuthorityAndConsumptionSurviveReopen(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, owner, executionPurposeHooks{afterConsume: func(t *testing.T, db *DB, ctx context.Context, _ confirmation.Intent, in rs.PurposeConsumeInput, out rs.PurposeConsumption) {
				p := purposeExecutionPlanForTest(t, ctx, db, out)
				before, err := purposeCurrentAuthorityForTest(ctx, db, p)
				if err != nil {
					t.Fatal(err)
				}
				root := db.stateRoot
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, root)
				defer cold.Close()
				replayed, err := consumeExecutionPurposeForTest(ctx, cold, in)
				if err != nil || !replayed.Replay || replayed.ExecutionID != out.ExecutionID || replayed.TaskDefinitionRevisionID != out.TaskDefinitionRevisionID {
					t.Fatalf("cold consumption replay=%+v err=%v", replayed, err)
				}
				after, err := purposeCurrentAuthorityForTest(ctx, cold, p)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("cold authority changed: before=%+v after=%+v err=%v", before, after, err)
				}
			}})
		})
	}
}

// Snapshot every durable output and the existing owner facts that a failed
// consume must leave untouched, not only the successor row counts.
func purposeConsumptionSnapshot(t *testing.T, ctx context.Context, db *DB, taskID string) string {
	t.Helper()
	var out string
	err := db.sql.QueryRowContext(ctx, `SELECT json_object('task', (SELECT json_object('phase',phase,'revision',current_definition_revision_id,'execution',current_execution_id,'goal',goal) FROM tasks WHERE id=?), 'executions',(SELECT count(*) FROM execution_proposals),'revisions',(SELECT count(*) FROM task_definition_revisions),'plans',(SELECT count(*) FROM runtime_scope_plans),'authorities',(SELECT count(*) FROM runtime_preparation_authorities),'admissions',(SELECT count(*) FROM execution_proposal_admissions),'consumptions',(SELECT count(*) FROM recovery_purpose_confirmation_consumptions),'startEffects',(SELECT count(*) FROM execution_start_effects),'sourceRuns',(SELECT json_group_array(json_object('id',id,'status',status)) FROM runs),'claims',(SELECT count(*) FROM feature_area_agent_claims))`, taskID).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRecoveryPurposeAdmissionFailureRollsBackAllOutputs(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, owner, executionPurposeHooks{beforeConsume: func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, in rs.PurposeConsumeInput) bool {
				before := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID)
				if _, err := db.sql.ExecContext(ctx, `CREATE TRIGGER test_purpose_admission_failure BEFORE INSERT ON execution_proposal_admissions BEGIN SELECT RAISE(ABORT,'injected purpose admission failure'); END`); err != nil {
					t.Fatal(err)
				}
				if _, err := consumeExecutionPurposeForTest(ctx, db, in); err == nil {
					t.Fatal("injected admission failure committed")
				}
				after := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID)
				if before != after {
					t.Fatalf("failed consume leaked durable effects: before=%s after=%s", before, after)
				}
				if _, err := db.sql.ExecContext(ctx, `DROP TRIGGER test_purpose_admission_failure`); err != nil {
					t.Fatal(err)
				}
				return false // The exact original request must now succeed normally.
			}})
		})
	}
}

func TestRecoveryPurposeCannotReplaceLockedRuntimeConfiguration(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, owner, executionPurposeHooks{
				preparePurpose: func(p *rs.PurposeProposal) {
					p.RecommendedRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "unapproved-model", ReasoningEffort: "high", CodexFastMode: true, ServiceTier: "priority"}
				},
				beforeConsume: func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, in rs.PurposeConsumeInput) bool {
					before := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID)
					if _, err := consumeExecutionPurposeForTest(ctx, db, in); err == nil {
						t.Fatal("purpose recommendation silently replaced locked runtime selection")
					}
					if after := purposeConsumptionSnapshot(t, ctx, db, intent.Content.RecoveryPurpose.Proposal.TaskID); before != after {
						t.Fatalf("runtime mismatch wrote business effects: before=%s after=%s", before, after)
					}
					return true
				},
			})
		})
	}
}

func TestRecoveryPurposeAuthorityRejectsCorruptIndependentArchive(t *testing.T) {
	for _, corruption := range []string{"missing-consumption", "consumption-variant", "receipt-variant", "missing-admission", "confirmation-unknown", "unauthorized-runtime-selection", "preparation-superseded", "plan-runtime-rehashed", "source-runtime"} {
		t.Run(corruption, func(t *testing.T) {
			runExecutionPurposeFlowWithHooks(t, "IMPLEMENTATION", executionPurposeHooks{afterConsume: func(t *testing.T, db *DB, ctx context.Context, intent confirmation.Intent, in rs.PurposeConsumeInput, out rs.PurposeConsumption) {
				p := purposeExecutionPlanForTest(t, ctx, db, out)
				if _, err := purposeCurrentAuthorityForTest(ctx, db, p); err != nil {
					t.Fatalf("uncorrupted authority rejected: %v", err)
				}
				var err error
				switch corruption {
				case "missing-consumption":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "recovery_purpose_confirmation_consumptions")
					_, err = db.sql.ExecContext(ctx, `DELETE FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, in.IntentID)
				case "consumption-variant":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "recovery_purpose_confirmation_consumptions")
					_, err = db.sql.ExecContext(ctx, `UPDATE recovery_purpose_confirmation_consumptions SET selected_variant_id='forged-variant' WHERE intent_id=?`, in.IntentID)
				case "receipt-variant":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "recovery_purpose_confirmation_receipts")
					_, err = db.sql.ExecContext(ctx, `UPDATE recovery_purpose_confirmation_receipts SET selected_variant_id='forged-variant' WHERE intent_id=?`, in.IntentID)
				case "missing-admission":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "execution_proposal_admissions")
					_, err = db.sql.ExecContext(ctx, `DELETE FROM execution_proposal_admissions WHERE proposal_id=?`, out.ExecutionID)
				case "confirmation-unknown":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "confirmation_intents")
					_, err = db.sql.ExecContext(ctx, `UPDATE confirmation_intents SET intent_json=json_set(intent_json,'$.state','UNKNOWN') WHERE intent_id=?`, in.IntentID)
				case "unauthorized-runtime-selection":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "confirmation_intents")
					_, err = db.sql.ExecContext(ctx, `UPDATE confirmation_intents SET intent_json=json_set(intent_json,'$.commit.selection.runtimeConfig',json('{"model":"forged-model","reasoningEffort":"high"}')) WHERE intent_id=?`, in.IntentID)
				case "preparation-superseded":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runtime_preparation_authorities")
					_, err = db.sql.ExecContext(ctx, `UPDATE runtime_preparation_authorities SET disposition='SUPERSEDED' WHERE plan_id=?`, p.ID)
				case "plan-runtime-rehashed":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "runtime_scope_plans")
					p.Model = "forged-runtime-model"
					p.AuthoritySnapshotHash, err = p.ComputeAuthoritySnapshotHash()
					if err == nil {
						p.ContentHash, err = p.ComputeContentHash()
					}
					if err == nil {
						_, err = db.sql.ExecContext(ctx, `UPDATE runtime_scope_plans SET model=?,authority_snapshot_hash=?,content_hash=? WHERE plan_id=?`, p.Model, p.AuthoritySnapshotHash, p.ContentHash, p.ID)
					}
				case "source-runtime":
					purposeDropTableTriggersForCorruptionTest(t, ctx, db, "execution_proposals")
					_, err = db.sql.ExecContext(ctx, `UPDATE execution_proposals SET runtime_config_json=json_set(runtime_config_json,'$.model','forged-source-model') WHERE id=?`, intent.Content.RecoveryPurpose.Proposal.SourceExecutionID)
				}
				if err != nil {
					t.Fatalf("inject %s: %v", corruption, err)
				}
				if _, err = purposeCurrentAuthorityForTest(ctx, db, p); err == nil {
					t.Fatalf("%s granted purpose runtime authority", corruption)
				}
				if _, err = NewRunStore(db).CreatePreparationRun(ctx, p.ID, "run_corrupt_purpose", p.PreparationRequestID, p.PreparationPayloadHash, time.Now().UTC()); err == nil {
					t.Fatalf("%s created a Run", corruption)
				}
				var runs int
				if err = db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE execution_proposal_id=?`, out.ExecutionID).Scan(&runs); err != nil || runs != 0 {
					t.Fatalf("corrupt authority leaked a Run: %d %v", runs, err)
				}
			}})
		})
	}
}

// Deliberately bypass SQL immutability only to model corrupted disk bytes. The
// production reader must independently reject a rehashed or detached archive.
func purposeDropTableTriggersForCorruptionTest(t *testing.T, ctx context.Context, db *DB, table string) {
	t.Helper()
	rows, err := db.sql.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='trigger' AND tbl_name=?`, table)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		names = append(names, name)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if _, err = db.sql.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(name, `"`, `""`)+`"`); err != nil {
			t.Fatal(err)
		}
	}
}
