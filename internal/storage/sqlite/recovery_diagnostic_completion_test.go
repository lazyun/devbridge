package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/worktree"
)

func prepareDiagnosticResolutionTest(t *testing.T, f agentStorageFixture, ctx context.Context, record rs.Record, source run.Run, disposition string) (rs.Record, rs.ResolveInput) {
	t.Helper()
	report := json.RawMessage(`{"summary":"independent diagnostic completed"}`)
	assessment, _ := json.Marshal(rs.Assessment{Consistency: "CONSISTENT", EngineeringDisposition: disposition})
	record.State, record.Report, record.After, record.Assessment = "ASSESSED", report, json.RawMessage(`[]`), assessment
	record.ReportHash, record.AssessmentHash = rs.Hash(report), rs.Hash(assessment)
	now := time.Now().UTC()
	record.CompletedAt = &now
	if disposition == "RECOVERED_COMPLETE" {
		seedLegacyDiagnosticSourceMembers(t, f, ctx, source)
		facts := rs.FactualObservation{OwnerContractValidated: true, OwnerContractEvidenceHash: rs.Hash([]byte("validated implementation result")), OwnerResult: json.RawMessage(`{"resultDisposition":"COMPLETED","summary":"implementation completed"}`), ObservedAt: now}
		for _, m := range source.RuntimeWorkset.Members {
			facts.After = append(facts.After, rs.WorktreeObservation{RepositoryID: m.RepositoryID, MemberID: m.MemberID, WorktreeID: m.WorktreeID, RootIdentity: m.RuntimeRootIdentity, State: "OBSERVED", HEAD: m.StartHEAD, Snapshot: m.StartSnapshot})
		}
		record.Facts, _ = json.Marshal(facts)
	}
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveRecoveryTx(ctx, tx, record); err == nil {
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, source.TaskID)
	}
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	in := rs.ResolveInput{ID: record.ID, RequestID: "resolve-isolated-diagnostic", ActorID: "caller", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Admission: record.Admission}
	in.PayloadHash = in.ComputePayloadHash()
	return record, in
}

func TestIndependentDiagnosticCompletionPreservesSourceAndReleasesExactFence(t *testing.T) {
	for _, disposition := range []string{"RECOVERED_PARTIAL", "RECOVERED_COMPLETE"} {
		t.Run(disposition, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				conversations := NewAgentConversationStore(f.db)
				before, err := conversations.Get(ctx, record.Spec.AgentConversationID)
				if err != nil {
					t.Fatal(err)
				}
				boundary, err := conversations.Boundary(ctx, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				record, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, disposition)
				resolved, err := store.Resolve(ctx, in)
				if err != nil || resolved.State != "RESOLVED" {
					t.Fatalf("resolve state=%s err=%v", resolved.State, err)
				}
				if replay, err := store.Resolve(ctx, in); err != nil || !reflect.DeepEqual(replay.Resolution, resolved.Resolution) {
					t.Fatalf("replay=%+v err=%v", replay, err)
				}
				after, err := conversations.Get(ctx, before.ID)
				if err != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("diagnostic rewrote source conversation: before=%+v after=%+v err=%v", before, after, err)
				}
				afterBoundary, err := conversations.Boundary(ctx, source.ID)
				if err != nil || !reflect.DeepEqual(boundary, afterBoundary) {
					t.Fatalf("diagnostic rewrote source UNKNOWN boundary: %+v %v", afterBoundary, err)
				}
				finished, err := NewRunStore(f.db).Get(ctx, child.ID)
				if err != nil || finished.Status != run.StatusSucceeded || finished.FinishedAt == nil || finished.ThreadID != child.ThreadID || finished.TurnID != child.TurnID {
					t.Fatalf("child not durably finished: %+v %v", finished, err)
				}
				state, err := readRuntimeAuthority(ctx, f.db.sql, source.FeatureAreaID)
				if err != nil || state.Busy || state.RecoveryRequired {
					t.Fatalf("resolved source still fences progression: %+v %v", state, err)
				}
				// Current process identity and epoch are irrelevant to historical resolution.
				root := f.db.stateRoot
				if err = f.db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, root)
				defer cold.Close()
				state, err = readRuntimeAuthority(ctx, cold.sql, source.FeatureAreaID)
				if err != nil || state.Busy || state.RecoveryRequired {
					t.Fatalf("cold resolved authority=%+v %v", state, err)
				}
				coldRecord, err := NewRecoveryStatusStore(cold).Read(ctx, record.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, independent, err := readRecoveryDiagnosticEvidenceTx(ctx, cold.sql, coldRecord, true); err != nil || !independent {
					t.Fatalf("cold diagnostic evidence: independent=%v err=%v", independent, err)
				}

				ctx = aisession.WithCallerScope(ctx, "agent-fixture-recovery-child-canonical")
				var successor run.Run
				if disposition == "RECOVERED_COMPLETE" {
					_, successor, err = NewExecutionProposalStore(cold).ReserveVerificationRun(ctx, source.ExecutionProposalID, "verification-after-independent-recovery", time.Now().UTC())
				} else {
					out, acceptErr := NewWorkflowProgressionStore(cold).ResolveTaskAcceptance(ctx, wp.TaskAcceptanceInput{AISessionID: f.session.ID, TaskID: source.TaskID, FeatureAreaID: source.FeatureAreaID, TaskDefinitionRevisionID: record.Spec.TaskDefinitionRevisionID, DecisionID: wp.TaskAcceptanceDecisionID(source.ExecutionProposalID), ExecutionID: source.ExecutionProposalID, Option: wp.AcceptanceRetryImplementation, RequestID: "partial-recovery-explicit-retry", RecoveryStatusID: record.ID, RecoveryContinuationInstruction: "Continue the remaining approved implementation"})
					if acceptErr != nil {
						t.Fatalf("confirm partial continuation: %v", acceptErr)
					}
					_, successor, err = NewExecutionProposalStore(cold).ConsumeAndCreateRun(ctx, out.SuccessorExecutionID, "implementation-after-independent-recovery", time.Now().UTC())
				}
				if err != nil {
					t.Fatalf("create independently recovered successor: %v", err)
				}
				if _, err = run.NewService(NewRunStore(cold), nil, nil).Transition(ctx, successor.ID, run.StatusPreparing); err != nil {
					t.Fatal(err)
				}
				rotated, err := NewAgentConversationStore(cold).BeginRun(ctx, successor.ID)
				if err != nil || rotated.ID == before.ID || rotated.PredecessorID != before.ID || rotated.BackendThreadID != "" || !rotated.ContinuationSafe {
					t.Fatalf("successor did not rotate unsafe source conversation: %+v %v", rotated, err)
				}
				if _, err = NewAgentRuntimeStore(cold).RecoveryContinuationBinding(ctx, successor.ID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("independent successor borrowed historical App Server or failed ordinary launch: %v", err)
				}
				old, err := NewAgentConversationStore(cold).Get(ctx, before.ID)
				if err != nil || old.ContinuationSafe || old.CurrentBoundaryTurnID != before.CurrentBoundaryTurnID || old.EndedAt == nil {
					t.Fatalf("rotation lost source unsafe evidence: %+v %v", old, err)
				}
			})
		})
	}
}

func TestIndependentDiagnosticResolutionRejectsIncompleteOrMismatchedChild(t *testing.T) {
	for _, disposition := range []string{"RECOVERED_PARTIAL", "RECOVERED_COMPLETE"} {
		for _, corruption := range []string{"turn", "thread", "child-not-running", "dispatch-receipt", "binding-json", "scope-plan"} {
			t.Run(disposition+"/"+corruption, func(t *testing.T) {
				runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
					_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, disposition)
					var err error
					switch corruption {
					case "turn", "thread", "child-not-running":
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
						column, value := "turn_id", "foreign-turn"
						if corruption == "thread" {
							column, value = "thread_id", source.ThreadID
						}
						if corruption == "child-not-running" {
							column, value = "status", "FAILED"
						}
						_, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET `+column+`=? WHERE id=?`, value, child.ID)
					case "dispatch-receipt":
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "recovery_child_turn_receipts")
						_, err = f.db.sql.ExecContext(ctx, `UPDATE recovery_child_turn_receipts SET payload_sha256=?`, rs.Hash([]byte("wrong dispatch")))
					case "binding-json":
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runtime_workset_bindings")
						_, err = f.db.sql.ExecContext(ctx, `UPDATE runtime_workset_bindings SET workset_json=json_set(workset_json,'$.runId','foreign-run') WHERE run_id=?`, child.ID)
					case "scope-plan":
						purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runtime_scope_plans")
						_, err = f.db.sql.ExecContext(ctx, `UPDATE runtime_scope_plans SET model='changed-model' WHERE owner_id=?`, record.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					before := purposeConsumptionSnapshot(t, ctx, f.db, source.TaskID)
					conversation, err := NewAgentConversationStore(f.db).Get(ctx, record.Spec.AgentConversationID)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = store.Resolve(ctx, in); err == nil {
						t.Fatal("inexact or unfinished diagnostic child authorized resolution")
					}
					if after := purposeConsumptionSnapshot(t, ctx, f.db, source.TaskID); after != before {
						t.Fatalf("rejected resolution changed business state: before=%s after=%s", before, after)
					}
					after, err := store.Read(ctx, record.ID)
					if err != nil || after.State != "ASSESSED" || len(after.Resolution) != 0 {
						t.Fatalf("resolution receipt escaped rollback: %+v %v", after, err)
					}
					unchanged, err := NewAgentConversationStore(f.db).Get(ctx, conversation.ID)
					if err != nil || !reflect.DeepEqual(conversation, unchanged) {
						t.Fatalf("rejected resolution rewrote conversation: %+v %v", unchanged, err)
					}
				})
			})
		}
	}
}

func TestIndependentDiagnosticResolutionCannotHideOtherUncertainty(t *testing.T) {
	for _, kind := range []string{"another-run", "later-conversation-boundary", "unfinished-child", "resolution-receipt"} {
		t.Run(kind, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, "RECOVERED_PARTIAL")
				if _, err := store.Resolve(ctx, in); err != nil {
					t.Fatal(err)
				}
				var err error
				switch kind {
				case "another-run":
					_, err = f.db.sql.ExecContext(ctx, `INSERT INTO agent_operation_recovery_states(run_id,classification,detail,classified_epoch) VALUES(?,'UNKNOWN','independent uncertain diagnostic observation',?)`, child.ID, f.db.epoch)
				case "later-conversation-boundary":
					_, err = f.db.sql.ExecContext(ctx, `INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,advance_state,quiesced) VALUES(?,?,'UNKNOWN',1)`, child.ID, record.Spec.AgentConversationID)
				case "resolution-receipt":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "recovery_status_records")
					_, err = f.db.sql.ExecContext(ctx, `UPDATE recovery_status_records SET record_json=json_set(record_json,'$.resolution.PayloadHash',?) WHERE id=?`, rs.Hash([]byte("foreign resolution")), record.ID)
				case "unfinished-child":
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
					_, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET status='RUNNING',finished_at=NULL WHERE id=?`, child.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				state, err := readRuntimeAuthority(ctx, f.db.sql, source.FeatureAreaID)
				if err == nil && (!state.Busy || !state.RecoveryRequired) {
					t.Fatalf("resolution concealed independent uncertainty: %+v", state)
				}
			})
		})
	}
}

// The shared fixture starts with a historical scalar source. Its member facts
// predate target-based admission, so restore those observed historical rows
// without manufacturing a current definition, platform binding or authority.
// The diagnostic child still uses the entire production preparation protocol.
func seedLegacyDiagnosticSourceMembers(t *testing.T, f agentStorageFixture, ctx context.Context, source run.Run) {
	t.Helper()
	var trigger string
	if err := f.db.sql.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='trigger' AND name='run_repository_binding_owner'`).Scan(&trigger); err != nil {
		t.Fatal(err)
	}
	for _, m := range source.RuntimeWorkset.Members {
		if err := NewWorktreeStore(f.db).Create(ctx, worktree.Worktree{ID: m.WorktreeID, RepositoryID: m.RepositoryID, RunID: source.ID, Path: m.CanonicalRoot, Branch: "historical-source", BaseCommit: m.OriginCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.sql.ExecContext(ctx, `DROP TRIGGER run_repository_binding_owner`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.db.sql.ExecContext(ctx, trigger); err != nil {
			t.Fatal(err)
		}
	}()
	for _, m := range source.RuntimeWorkset.Members {
		if err := NewRunStore(f.db).PutRepositoryBinding(ctx, run.RepositoryBinding{RunID: source.ID, ExecutionID: source.ExecutionProposalID, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, WorkspaceID: source.WorkspaceID, RepositoryID: m.RepositoryID, MemberID: m.MemberID, MemberVersion: m.MemberVersion, OriginCommit: m.OriginCommit, WorktreeID: m.WorktreeID, Preparation: "READY", RepositoryAlias: m.RepositoryAlias, StartHEAD: m.StartHEAD, StartSnapshot: m.StartSnapshot, IntendedRead: m.Read, IntendedWrite: m.Write, RuntimeRootIdentity: m.RuntimeRootIdentity}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIndependentDiagnosticResolveAfterAdoptionUsesOriginalPreparationEpoch(t *testing.T) {
	for _, disposition := range []string{"RECOVERED_COMPLETE", "RECOVERED_PARTIAL"} {
		t.Run(disposition, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				_, in := prepareDiagnosticResolutionTest(t, f, ctx, record, source, disposition)
				root := f.db.stateRoot
				if err := f.db.Close(); err != nil {
					t.Fatal(err)
				}
				cold := openTestDB(t, root)
				defer cold.Close()
				current, err := NewRecoveryStatusStore(cold).Adopt(ctx, record.ID)
				if err != nil || current.RuntimeEpoch == record.RuntimeEpoch {
					t.Fatalf("adopt diagnostic: epoch=%s err=%v", current.RuntimeEpoch, err)
				}
				tx, err := cold.sql.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				authorized, authErr := cold.recoveryChildAuthorityTx(ctx, tx, child.ID)
				tx.Rollback()
				if authErr != nil || !authorized {
					t.Fatalf("adopted exact child lost canonical authority: %v %v", authorized, authErr)
				}
				in.RuntimeEpoch = current.RuntimeEpoch
				in.PayloadHash = in.ComputePayloadHash()
				resolved, err := NewRecoveryStatusStore(cold).Resolve(ctx, in)
				if err != nil || resolved.State != "RESOLVED" {
					t.Fatalf("resolve adopted diagnostic: %s %v", resolved.State, err)
				}
				state, err := readRuntimeAuthority(ctx, cold.sql, source.FeatureAreaID)
				if err != nil || state.Busy || state.RecoveryRequired {
					t.Fatalf("adopted resolution fence: %+v %v", state, err)
				}
				purposeDropTableTriggersForCorruptionTest(t, ctx, cold, "recovery_status_adoptions")
				if _, err = cold.sql.ExecContext(ctx, `UPDATE recovery_status_adoptions SET recovery_id='foreign-recovery' WHERE recovery_id=?`, record.ID); err != nil {
					t.Fatal(err)
				}
				if state, err = readRuntimeAuthority(ctx, cold.sql, source.FeatureAreaID); err == nil && !state.RecoveryRequired {
					t.Fatal("foreign adoption bridged immutable preparation epoch")
				}
			})
		})
	}
}
