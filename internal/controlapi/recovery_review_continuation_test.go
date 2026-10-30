package controlapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// Only the interrupted source runtime and its diagnostic are observations.
// Source admissions and purpose confirmation use the public facade; successor
// consumption, preparation and dispatch use the actual restarted Core loop.
type observedReviewSourceStarter struct{ db *sqlite.DB }

func (observedReviewSourceStarter) StartReview(ctx context.Context, _ string, reserve func(context.Context) (run.Run, error)) (run.Run, error) {
	return reserve(ctx)
}
func (s observedReviewSourceStarter) StartReservedReview(ctx context.Context, _ string, r run.Run) (run.Run, error) {
	store := sqlite.NewExecutionProposalStore(s.db)
	if _, _, err := store.ClaimHandoff(ctx, r.ProjectID, r.ID, time.Now().UTC()); err != nil {
		return r, err
	}
	_, err := store.RecordHandoff(ctx, r.ProjectID, r.ID, executionproposal.HandoffAccepted, "SOURCE_OBSERVATION_FIXTURE", time.Now().UTC())
	return r, err
}

func recoveryReviewContinuationFixture(t *testing.T, executor review.ExecutorKind) (*upperLifecycleFixture, c.Intent) {
	t.Helper()
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	if executor == review.ExecutorCodingAgent {
		if _, err := f.facade.SubmitReviewResult(f.ctx, f.findingsOnly("release-source-upper", f.access.Target)); err != nil {
			t.Fatal(err)
		}
		scope, err := scopereview.NewService(f.plane.AISessions, f.plane.Repositories, f.plane.Runs, observedReviewSourceStarter{f.plane.DB}, sqlite.NewReviewStore(f.plane.DB))
		if err != nil {
			t.Fatal(err)
		}
		scope.SetReviews(f.plane.Reviews)
		scope.SetInitialGrantService(f.plane.ReviewGrants)
		scope.SetUpperStarter(f.plane.Coordinator)
		f.plane.ScopeReviews = scope
		f.facade, err = controlapi.NewFromControlPlane(f.plane)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "purpose-coding-task", Purpose: c.Task, Action: "CREATE_TASK", Task: &c.TaskBody{FeatureAreaID: f.area, Title: "Interrupted coding Review", ProblemStatement: "Read exact app/sdk", Constraints: []string{"Read only"}}, Variants: []c.Variant{{ID: "create", Summary: "Create task"}}})
		if err != nil {
			t.Fatal(err)
		}
		created, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "purpose-coding-task-commit", ContentHash: prepared.ContentHash, VariantID: "create"})
		if err != nil {
			t.Fatal(err)
		}
		f.task = created.Steps[0].TaskID
		prepared, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "purpose-coding-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: f.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "coding", Summary: "Read exact repositories", ExecutorKind: string(executor), ReviewInstruction: "Read the two exact member contracts", PrimaryMemberID: f.targets[0].MemberID}}})
		if err != nil {
			t.Fatal(err)
		}
		options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if err != nil || len(options.BackendCandidates) == 0 {
			t.Fatalf("source options: %+v %v", options, err)
		}
		b := options.BackendCandidates[0]
		started, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "purpose-coding-review-commit", ContentHash: prepared.ContentHash, VariantID: "coding", Selection: c.Selection{RuntimeConfig: &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, Backend: &codexconfig.BackendSelectionInput{ConfigID: b.ConfigID, ConfigFingerprint: b.ConfigFingerprint}}})
		if err != nil {
			t.Fatal(err)
		}
		f.review, f.run = started.Steps[0].ReviewID, started.Steps[0].RunID
	}
	source, err := f.plane.Runs.Get(f.ctx, f.run)
	if err != nil {
		t.Fatal(err)
	}
	value, err := f.plane.Reviews.Get(f.ctx, f.review)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE runs SET status='PREPARING' WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	agents := sqlite.NewAgentConversationStore(f.plane.DB)
	conversation, err := agents.BeginRun(f.ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if value.BackendSelection != nil {
		err = agents.BindThreadWithRuntimeProvenance(f.ctx, source.ID, "purpose-review-source-thread", value.RuntimeConfig.Model, "source-runtime-observation", *value.BackendSelection)
	} else {
		err = agents.BindThread(f.ctx, source.ID, "purpose-review-source-thread")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE runs SET status='RUNNING',thread_id='purpose-review-source-thread',turn_id='purpose-review-source-turn' WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err = agents.MarkTurnPending(f.ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: f.project, FeatureAreaID: f.area, ReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, RunID: source.ID, WorkspaceID: value.WorkspaceID, PrimaryMemberID: f.targets[0].MemberID, BackendConfigID: "upper-observed-source", BackendConfigFingerprint: strings.Repeat("a", 64), Model: "gpt-5.6-terra", ReasoningEffort: "high", RuntimeAdapterIdentity: executionproposal.RuntimeAdapterCodexAppServerV1, RuntimeProtocolIdentity: executionproposal.RuntimeProtocolCodex0149, CreatedAt: time.Now().UTC()}
	if value.BackendSelection != nil {
		w.BackendConfigID = value.BackendSelection.ConfigID
		w.BackendConfigFingerprint = value.BackendSelection.ConfigFingerprint
		w.Model = value.RuntimeConfig.Model
		w.ReasoningEffort = value.RuntimeConfig.ReasoningEffort
	}
	for i, target := range f.targets {
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryID: target.RepositoryID, RepositoryAlias: fmt.Sprintf("member-%d", i), MemberID: target.MemberID, MemberVersion: target.MemberVersion, WorktreeID: fmt.Sprintf("source-observed-%d", i), CanonicalRoot: filepath.Join(t.TempDir(), "source"), RootFileIdentity: fmt.Sprintf("source-root-%d", i), OriginCommit: target.OriginCommit, StartHEAD: target.OriginCommit, StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: fmt.Sprintf("source-runtime-%d", i)})
	}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	source, err = sqlite.NewRunStore(f.plane.DB).BindRuntimeWorkset(f.ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if err = sqlite.NewAgentRuntimeStore(f.plane.DB).BindAppServer(f.ctx, source.ID, appserver.Binding{ID: "purpose-review-observed", Epoch: 1, Endpoint: "unix:/source-observation", ProcessStartIdentity: "source-observation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: f.project, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "purpose-review-observed"}, RequestID: "review-purpose-recovery", ActorID: "operator-observation", RecoveryReason: "MISSING_RESULT", Instruction: "Diagnose interrupted review", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	recoveries := sqlite.NewRecoveryStatusStore(f.plane.DB)
	record, err := recoveries.Prepare(f.ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil {
		t.Fatal(err)
	}
	record.State = "ASSESSED"
	record.Report = json.RawMessage(`{"summary":"revised Review needed"}`)
	record.Assessment = json.RawMessage(`{"consistency":"CONSISTENT","engineeringDisposition":"REVISION_REQUIRED"}`)
	record.ReportHash = rs.Hash(record.Report)
	record.AssessmentHash = rs.Hash(record.Assessment)
	record.After = json.RawMessage(`[]`)
	record.Admission = rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	raw, _ := json.Marshal(record)
	if _, err = f.plane.DB.SQL().Exec(`UPDATE recovery_status_records SET state='ASSESSED',record_json=? WHERE id=?`, string(raw), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, f.task); err != nil {
		t.Fatal(err)
	}
	p := rs.PurposeProposal{RecoveryID: record.ID, RecoveryGeneration: 1, OwnerKind: "REVIEW", OwnerID: f.review, TaskID: f.task, FeatureAreaID: f.area, SourceReviewID: f.review, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, SourceOperationID: f.review, SourceRunID: source.ID, SourceTurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AssessmentHash: record.AssessmentHash, SourceAISessionID: f.session, Variants: []rs.PurposeVariant{{ID: "revised", Label: "Revise review", Summary: "Review app/sdk again", Review: &rs.ReviewPurposeVariant{SourceReviewID: f.review, ReviewInstruction: "Inspect both revised exact member contracts", ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}, ReviewDepth: task.ReviewDepthDeep, SupplementalPurpose: "Reassess exact scope", RepositoryTargets: f.targets}}}}
	p.SubmittedHash = p.ComputeSubmittedHash()
	resolve := rs.ResolveInput{ID: record.ID, RequestID: "review-purpose-resolve", ActorID: spec.ActorID, RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &p, Admission: record.Admission}
	resolve.PayloadHash = resolve.ComputePayloadHash()
	if _, err = recoveries.Resolve(f.ctx, resolve); err != nil {
		t.Fatal(err)
	}
	projection, err := recoveries.ReadPurposeConfirmation(f.ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := f.plane.Confirmations.Get(f.ctx, projection.AISessionID, projection.IntentID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err = f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "review-purpose-confirm", ContentHash: intent.ContentHash, VariantID: "revised"})
	if err != nil || intent.State != "COMPLETED" {
		for cause := err; cause != nil; cause = errors.Unwrap(cause) {
			t.Logf("confirm cause: %T %v", cause, cause)
		}
		t.Fatalf("public purpose confirm: state=%s %v", intent.State, err)
	}
	return f, intent
}

func TestRecoveryReviewPurposePublicCoreContinuation(t *testing.T) {
	for _, tc := range []struct {
		executor review.ExecutorKind
		cold     bool
	}{{review.ExecutorUpperLLM, false}, {review.ExecutorCodingAgent, false}, {review.ExecutorUpperLLM, true}, {review.ExecutorCodingAgent, true}} {
		executor := tc.executor
		t.Run(fmt.Sprintf("%s/prepared-cold-%t", executor, tc.cold), func(t *testing.T) {
			f, intent := recoveryReviewContinuationFixture(t, executor)
			helper := t.TempDir()
			launcher := filepath.Join(helper, "review-helper")
			t.Setenv("DEVBRIDGE_CODING_V1_HELPER", "1")
			t.Setenv("DEVBRIDGE_CODING_V1_WIRE_LOG", filepath.Join(helper, "wire.jsonl"))
			t.Setenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT", `{"version":2,"reviewDisposition":"FINDINGS_ONLY","summary":"Exact revised Review completed","findings":[]}`)
			executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
			if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"), 0700); err != nil {
				t.Fatal(err)
			}
			var originalRun, originalEpoch string
			if tc.cold {
				// Stop at the genuine Core continuation's durable boundary,
				// before dispatch. Neither a Run nor a consume is hand-authored.
				if _, err := f.plane.RecoveryPurposeConsumers.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
					t.Fatal(err)
				}
				if _, err := sqlite.NewReviewStore(f.plane.DB).ContinueRecoveryReviewPreparations(context.Background(), ""); err != nil {
					t.Fatal(err)
				}
				if err := f.plane.DB.SQL().QueryRow(`SELECT r.id,r.runtime_epoch FROM recovery_purpose_confirmation_consumptions c JOIN reviews v ON v.id=c.successor_review_id JOIN runs r ON r.id=v.linked_run_id WHERE c.intent_id=?`, intent.IntentID).Scan(&originalRun, &originalEpoch); err != nil {
					t.Fatal(err)
				}
			}
			reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot, Codex: codex.Config{CodexPath: launcher}})
			var successor, runID, status, grant string
			deadline := time.Now().Add(15 * time.Second)
			for time.Now().Before(deadline) {
				_ = f.plane.DB.SQL().QueryRow(`SELECT c.successor_review_id,COALESCE(v.linked_run_id,''),COALESCE(r.status,''),g.state FROM recovery_purpose_confirmation_consumptions c JOIN reviews v ON v.id=c.successor_review_id JOIN review_grants g ON g.review_id=v.id LEFT JOIN runs r ON r.id=v.linked_run_id WHERE c.intent_id=?`, intent.IntentID).Scan(&successor, &runID, &status, &grant)
				if executor == review.ExecutorUpperLLM && grant == "ACTIVE" && status == "RUNNING" {
					break
				}
				if executor == review.ExecutorCodingAgent && (status == "SUCCEEDED" || status == "FAILED" || status == "INFRASTRUCTURE_FAILED") {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			var attempts, bindings, receipts int
			var failure string
			_ = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM runtime_preparation_attempts WHERE run_id=?),(SELECT count(*) FROM runtime_workset_bindings WHERE run_id=?),(SELECT count(*) FROM feature_area_review_start_requests WHERE run_id=?),COALESCE((SELECT failure_code||':'||failure_message FROM runs WHERE id=?),'')`, runID, runID, runID, runID).Scan(&attempts, &bindings, &receipts, &failure)
			if successor == "" || runID == "" || attempts != 1 || bindings != 1 || receipts != 1 {

				t.Fatalf("Core continuation review=%s run=%s status=%s grant=%s attempts/bindings/receipts=%d/%d/%d failure=%s", successor, runID, status, grant, attempts, bindings, receipts, failure)
			}
			if executor == review.ExecutorUpperLLM {
				access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, successor)
				if err != nil || access.State != "ACTIVE" || len(access.RepositoryTargets) != 2 {
					t.Fatalf("successor Upper grant: %+v %v", access, err)
				}
				for _, member := range access.RepositoryTargets {
					got, readErr := f.facade.UpperReviewRead(f.ctx, directfile.Input{ReviewTarget: &access.Target, RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: member.MemberVersion, Path: "same.txt"})
					if readErr != nil || got.Content == "" {
						t.Fatalf("Upper exact member read %s: %+v %v", member.MemberID, got, readErr)
					}
				}
			} else {
				if status != "SUCCEEDED" {
					t.Fatalf("successor Coding runtime: %s failure=%s", status, failure)
				}
				var turn string
				if err := f.plane.DB.SQL().QueryRow(`SELECT turn_id FROM runs WHERE id=?`, runID).Scan(&turn); err != nil || turn == "" {
					t.Fatalf("successor never reached native protocol worker: %s %v", turn, err)
				}
			}
			if tc.cold {
				var count int
				if originalRun != runID {
					t.Fatalf("restart substituted Run %s -> %s", originalRun, runID)
				}
				if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM review_purpose_preparation_adoptions WHERE run_id=? AND previous_epoch=? AND current_epoch=?`, runID, originalEpoch, f.plane.DB.RuntimeEpoch()).Scan(&count); err != nil || count != 1 {
					t.Fatalf("exact cold adoption %d %v", count, err)
				}
			}
			var count int
			if _, err := sqlite.NewReviewStore(f.plane.DB).ContinueRecoveryReviewPreparations(f.ctx, ""); err != nil {
				t.Fatal(err)
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE review_id=?`, successor).Scan(&count); err != nil || count != 1 {
				t.Fatalf("duplicate preparation Run=%d %v", count, err)
			}
			t.Logf("public confirmation → Core consume → frozen preparation → %s: review=%s Run=%s two repository roots; exactly one admission", executor, successor, runID)
		})
	}
}

func TestRecoveryReviewPurposePreparationRejectsCorruptSourceAndActivePriorBinding(t *testing.T) {
	for _, mode := range []string{"corrupt-source", "other-active-binding"} {
		t.Run(mode, func(t *testing.T) {
			f, intent := recoveryReviewContinuationFixture(t, review.ExecutorCodingAgent)
			if _, err := f.plane.RecoveryPurposeConsumers.ContinueConfirmedPurposes(context.Background(), 0); err != nil {
				t.Fatal(err)
			}
			var successor string
			if err := f.plane.DB.SQL().QueryRow(`SELECT successor_review_id FROM recovery_purpose_confirmation_consumptions WHERE intent_id=?`, intent.IntentID).Scan(&successor); err != nil {
				t.Fatal(err)
			}
			if mode == "corrupt-source" {
				// First prove the normal writer cannot corrupt this identity.
				if _, err := f.plane.DB.SQL().Exec(`UPDATE recovery_status_records SET record_json=json_set(record_json,'$.spec.instruction','changed archived source') WHERE id=?`, intent.Content.RecoveryPurpose.Proposal.RecoveryID); err == nil {
					t.Fatal("immutable source accepted mutation")
				}
				// Simulate disk corruption beyond the normal writer boundary.
				if _, err := f.plane.DB.SQL().Exec(`DROP TRIGGER recovery_status_json_identity`); err != nil {
					t.Fatal(err)
				}
				// Corrupt one archived diagnostic field without authoring new authority.
				if _, err := f.plane.DB.SQL().Exec(`UPDATE recovery_status_records SET record_json=json_set(record_json,'$.spec.instruction','changed archived source') WHERE id=?`, intent.Content.RecoveryPurpose.Proposal.RecoveryID); err != nil {
					t.Fatal(err)
				}
				if _, err := sqlite.NewReviewStore(f.plane.DB).ContinueRecoveryReviewPreparations(f.ctx, ""); err == nil {
					t.Fatal("corrupt source archive created a preparation Run")
				}
				var count int
				if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE review_id=?`, successor).Scan(&count); err != nil || count != 0 {
					t.Fatalf("corrupt archive wrote Run %d %v", count, err)
				}
				return
			}
			if _, err := sqlite.NewReviewStore(f.plane.DB).ContinueRecoveryReviewPreparations(f.ctx, ""); err != nil {
				t.Fatal(err)
			}
			var runID, prior string
			if err := f.plane.DB.SQL().QueryRow(`SELECT linked_run_id FROM reviews WHERE id=?`, successor).Scan(&runID); err != nil {
				t.Fatal(err)
			}
			if err := f.plane.DB.SQL().QueryRow(`SELECT DISTINCT r.id FROM run_repository_bindings b JOIN runs r ON r.id=b.run_id WHERE r.feature_area_id=? AND r.task_id<>? AND r.status='SUCCEEDED' LIMIT 1`, f.area, f.task).Scan(&prior); err != nil {
				t.Fatal(err)
			}
			// Simulate another still-live owner of the inherited managed members. Its
			// repository binding must not be accepted merely because this purpose is valid.
			if _, err := f.plane.DB.SQL().Exec(`UPDATE runs SET status='RUNNING',finished_at=NULL WHERE id=?`, prior); err != nil {
				t.Fatal(err)
			}
			if _, err := f.plane.Coordinator.PrepareScopeReview(f.ctx, runID); err == nil {
				t.Fatal("active unrelated prior binding admitted a runtime")
			}
			var count int
			if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runtime_workset_bindings WHERE run_id=?`, runID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unsafe workspace wrote start binding %d %v", count, err)
			}
		})
	}
}
