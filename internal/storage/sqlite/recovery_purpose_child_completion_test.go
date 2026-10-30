package sqlite

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRecoveryPurposeResolveFinishesExactDiagnosticChild(t *testing.T) {
	for _, scenario := range []string{"complete", "missing-turn", "mismatched-turn"} {
		t.Run(scenario, func(t *testing.T) {
			runCanonicalRecoveryChildFixture(t, func(t *testing.T, f agentStorageFixture, ctx context.Context, store *RecoveryStatusStore, record rs.Record, source, child run.Run) {
				ctx = aisession.WithCallerScope(ctx, "agent-fixture-recovery-child-canonical")
				if child.Status != run.StatusRunning || child.Purpose != run.PurposeRecoveryStatus || child.TurnID == "" || child.ThreadID == source.ThreadID || child.RuntimeWorkset == nil {
					t.Fatalf("fixture lacks a real isolated diagnostic child: %+v", child)
				}
				sourceBoundary, err := NewAgentConversationStore(f.db).Boundary(ctx, source.ID)
				if err != nil {
					t.Fatal(err)
				}
				current, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				// Model a completed diagnostic observation at the same evidence seam
				// used by the purpose flow fixture. Admission and child identity above
				// were produced by the real preparation protocol.
				report := json.RawMessage(`{"summary":"confirmed revision required"}`)
				assessment := json.RawMessage(`{"consistency":"CONSISTENT","engineeringDisposition":"REVISION_REQUIRED"}`)
				record.State, record.Report, record.After, record.Assessment = "ASSESSED", report, json.RawMessage(`[]`), assessment
				record.ReportHash, record.AssessmentHash = rs.Hash(report), rs.Hash(assessment)
				now := time.Now().UTC()
				record.CompletedAt = &now
				tx, err := f.db.beginWrite(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = saveRecoveryTx(ctx, tx, record); err == nil {
					_, err = tx.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, current.ID)
				}
				if err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err = tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if scenario != "complete" {
					// Inject mismatching observed Run bytes without rewriting the
					// immutable Recovery/turn-dispatch receipt that authorizes closure.
					purposeDropTableTriggersForCorruptionTest(t, ctx, f.db, "runs")
					var turn any
					if scenario == "mismatched-turn" {
						turn = "different-diagnostic-turn"
					}
					if _, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET turn_id=? WHERE id=?`, turn, child.ID); err != nil {
						t.Fatal(err)
					}
					child, err = NewRunStore(f.db).Get(ctx, child.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				work := current.WorkSpec
				work.Goal, work.CodexInstruction = "Revised implementation", "Implement the revised scope"
				work.ExecutionScope = task.ExecutionScope{Kind: task.ExecutionScopeTargets}
				member := source.RuntimeWorkset.Members[0]
				target := workset.Target{RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: member.MemberVersion, OriginCommit: member.OriginCommit, Read: true, Write: member.Write}
				verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "explicit purpose fixture waiver"}}
				p := rs.PurposeProposal{RecoveryID: record.ID, RecoveryGeneration: record.Spec.Generation, OwnerKind: "IMPLEMENTATION", OwnerID: source.ExecutionProposalID, TaskID: source.TaskID, FeatureAreaID: source.FeatureAreaID, SourceExecutionID: source.ExecutionProposalID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, SourceOperationID: source.OperationID, SourceRunID: source.ID, SourceTurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: source.RuntimeWorkset.ContentHash, AssessmentHash: record.AssessmentHash, SourceAISessionID: f.session.ID, Variants: []rs.PurposeVariant{{ID: "revised", Label: "Revised implementation", Summary: "Implement confirmed revised purpose", Implementation: &rs.ImplementationPurposeVariant{SourceExecutionID: source.ExecutionProposalID, SourceRevisionID: current.CurrentDefinitionRevisionID, WorkSpec: work, VerificationSpec: verification, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: member.MemberID, ImplementationMode: work.ImplementationMode}}}}
				p.SubmittedHash = p.ComputeSubmittedHash()
				if err = p.Validate(record.Spec.Source, record.ID, record.Spec.Generation, record.AssessmentHash); err != nil {
					t.Fatalf("purpose observation fixture invalid: %v work=%v verification=%v", err, task.ValidateTargetWorkSpec(work), task.ValidateTargetVerification(verification, []workset.Target{target}))
				}
				in := rs.ResolveInput{ID: record.ID, RequestID: "resolve-purpose-with-child", ActorID: "caller", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Admission: record.Admission, Purpose: &p}
				in.PayloadHash = in.ComputePayloadHash()
				var beforeIntents, beforeLinks int
				if err = f.db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM confirmation_intents),(SELECT count(*) FROM recovery_purpose_link_history)`).Scan(&beforeIntents, &beforeLinks); err != nil {
					t.Fatal(err)
				}
				resolved, resolveErr := store.Resolve(ctx, in)
				if scenario == "complete" {
					if resolveErr != nil || resolved.State != "RESOLVED" {
						t.Fatalf("valid diagnostic completion failed: state=%s error=%v", resolved.State, resolveErr)
					}
					projection, err := store.ReadPurposeConfirmation(ctx, record.ID)
					if err != nil || projection.State != "PENDING" || projection.IntentID == "" {
						t.Fatalf("diagnostic completion did not publish pending purpose: %+v %v", projection, err)
					}
					intent, err := NewConfirmationStore(f.db).Get(ctx, f.session.ID, projection.IntentID)
					if err != nil || intent.State != "PENDING" || intent.Purpose != confirmation.RecoveryPurpose {
						t.Fatalf("invalid new purpose: %+v %v", intent, err)
					}
					finished, err := NewRunStore(f.db).Get(ctx, child.ID)
					if err != nil || finished.Status != run.StatusSucceeded || finished.FinishedAt == nil || finished.ThreadID != child.ThreadID || finished.TurnID != child.TurnID || finished.RuntimeWorkset.ContentHash != child.RuntimeWorkset.ContentHash {
						t.Fatalf("exact child not finished durably: %+v %v", finished, err)
					}
					if replay, err := store.Resolve(ctx, in); err != nil || replay.State != "RESOLVED" {
						t.Fatalf("diagnostic completion replay=%+v %v", replay, err)
					}
				} else {
					if resolveErr == nil {
						t.Fatal("inexact child turn authorized purpose resolution")
					}
					unchanged, err := NewRunStore(f.db).Get(ctx, child.ID)
					if err != nil || !reflect.DeepEqual(child, unchanged) {
						t.Fatalf("rejected resolution changed child: before=%+v after=%+v err=%v", child, unchanged, err)
					}
					var intents, links int
					if err = f.db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM confirmation_intents),(SELECT count(*) FROM recovery_purpose_link_history)`).Scan(&intents, &links); err != nil || intents != beforeIntents || links != beforeLinks {
						t.Fatalf("failed completion leaked purpose: intents=%d/%d links=%d/%d err=%v", intents, beforeIntents, links, beforeLinks, err)
					}
					still, err := store.Read(ctx, record.ID)
					if err != nil || still.State != "ASSESSED" || len(still.Resolution) != 0 {
						t.Fatalf("failed completion consumed resolution: %+v %v", still, err)
					}
				}
				unchangedSource, err := NewRunStore(f.db).Get(ctx, source.ID)
				if err != nil || !reflect.DeepEqual(source, unchangedSource) {
					t.Fatalf("purpose resolution changed source Run: before=%+v after=%+v err=%v", source, unchangedSource, err)
				}
				unchangedBoundary, err := NewAgentConversationStore(f.db).Boundary(ctx, source.ID)
				if err != nil || !reflect.DeepEqual(sourceBoundary, unchangedBoundary) {
					t.Fatalf("purpose resolution rewrote source UNKNOWN boundary: before=%+v after=%+v err=%v", sourceBoundary, unchangedBoundary, err)
				}
			})
		})
	}
}
