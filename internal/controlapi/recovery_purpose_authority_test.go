package controlapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalexecution"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

// This is a Core persistence integration test. Formal approval, qualification,
// binding, Run admission, purpose resolution, user confirmation and consumption
// use production writers. Only runtime observations and the diagnostic result
// are fixtures; no native worker or compatibility measurement is claimed.
func qualifiedRecoveryPurposeFixture(t *testing.T) (*upperLifecycleFixture, rs.PurposeConsumeInput, runtimeselection.CurrentAuthority) {
	t.Helper()
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	workflow := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	receipt, err := workflow.CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := f.plane.Confirmations.Get(f.ctx, id.AISessionID, id.IntentID)
	if err != nil || journal.Commit == nil {
		t.Fatalf("formal journal: %v", err)
	}
	commit := *journal.Commit
	commit.SubmittedHash = id.SubmittedHash
	completed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || completed.State != "COMPLETED" {
		t.Fatalf("formal completion: %+v %v", completed, err)
	}
	selection := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	owner := runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: receipt.Archive.RevisionID, RuntimeRole: "IMPLEMENTATION"}
	original, err := selection.ReadCurrent(f.ctx, owner)
	if err != nil || original.Platform != runtimeselection.PlatformReference(ref) {
		t.Fatalf("original qualified authority: %+v %v", original, err)
	}
	return f, prepareQualifiedRecoveryPurpose(t, f, receipt, original), original
}

// The original interrupted Run and diagnostic result are observations supplied
// by this fixture. All admission, purpose, receipt and consumption authority is
// produced by Core. Native tests supply a genuinely measured formal receipt.
func prepareQualifiedRecoveryPurpose(t *testing.T, f *upperLifecycleFixture, receipt formalexecution.Receipt, original runtimeselection.CurrentAuthority) rs.PurposeConsumeInput {
	t.Helper()
	workflow := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	owner := original.Owner
	proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, receipt.T2.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	expected := wp.TaskExecutionExpectation{FeatureAreaID: f.area, TaskDefinitionRevisionID: owner.TaskDefinitionRevisionID, ExecutionID: proposal.ID, Phase: task.PhaseImplementationReady, PrimaryMemberID: proposal.PrimaryMemberID, ApprovedWorksetHash: proposal.ApprovedWorksetHash}
	if _, _, err = workflow.ReserveTaskExecutionExpectedCommand(f.ctx, f.session, f.task, "START_EXECUTION", "qualified-purpose-source-start", &expected); err != nil {
		t.Fatal(err)
	}
	runs := sqlite.NewRunStore(f.plane.DB)
	source, err := runs.CreatePreparationRun(f.ctx, receipt.Plan.ID, "run-qualified-purpose-source", receipt.Plan.PreparationRequestID, receipt.Plan.PreparationPayloadHash, time.Now().UTC())
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
	if err = agents.BindThreadWithRuntimeProvenance(f.ctx, source.ID, "qualified-purpose-thread", original.Values.Model, "qualified-purpose-runtime-observation", *proposal.BackendSelection); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE runs SET status='RUNNING',thread_id='qualified-purpose-thread',turn_id='qualified-purpose-turn' WHERE id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err = agents.MarkTurnPending(f.ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	v := original.Values
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: f.project, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: owner.TaskDefinitionRevisionID, ExecutionID: proposal.ID, RunID: source.ID, WorkspaceID: "qualified-purpose-observed-workspace", PrimaryMemberID: receipt.Plan.PrimaryMemberID, BackendConfigID: v.BackendIdentity, BackendConfigFingerprint: v.BackendConfigFingerprint, Model: v.Model, ReasoningEffort: v.ReasoningEffort, ServiceTier: v.ServiceTier, RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity, CreatedAt: time.Now().UTC()}
	root := t.TempDir()
	for i, target := range receipt.Plan.RepositoryTargets {
		w.Members = append(w.Members, workset.RuntimeMemberBinding{RepositoryID: target.RepositoryID, RepositoryAlias: fmt.Sprint("member-", i), MemberID: target.MemberID, MemberVersion: target.MemberVersion, WorktreeID: fmt.Sprint("observed-worktree-", i), CanonicalRoot: filepath.Join(root, fmt.Sprint(i)), RootFileIdentity: fmt.Sprint("observed-root-", i), OriginCommit: target.OriginCommit, StartHEAD: target.OriginCommit, StartSnapshot: strings.Repeat("b", 64), Read: target.Read, Write: target.Write, RuntimeRootIdentity: fmt.Sprint("observed-runtime-root-", i)})
	}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if source, err = runs.BindRuntimeWorkset(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = sqlite.NewAgentRuntimeStore(f.plane.DB).BindAppServer(f.ctx, source.ID, appserver.Binding{ID: "qualified-purpose-instance", Epoch: 1, Endpoint: "unix:/qualified-purpose-fixture", ProcessStartIdentity: "qualified-purpose-observation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: f.project, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: owner.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "qualified-purpose-instance"}, RequestID: "qualified-purpose-recovery", ActorID: "qualified-purpose-operator", RecoveryReason: "MISSING_RESULT", Instruction: "Inspect the interrupted qualified execution", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	recoveries := sqlite.NewRecoveryStatusStore(f.plane.DB)
	record, err := recoveries.Prepare(f.ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil {
		t.Fatal(err)
	}
	// The diagnostic result is intentionally an injected observation fixture;
	// it cannot create approval, platform, revision or purpose authority.
	record.State = "ASSESSED"
	record.Report = json.RawMessage(`{"summary":"qualified source needs a revised purpose"}`)
	record.Assessment = json.RawMessage(`{"consistency":"CONSISTENT","engineeringDisposition":"REVISION_REQUIRED"}`)
	record.ReportHash, record.AssessmentHash = rs.Hash(record.Report), rs.Hash(record.Assessment)
	record.After = json.RawMessage(`[]`)
	record.Admission = rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE recovery_status_records SET state='ASSESSED',record_json=? WHERE id=?`, string(raw), record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, f.task); err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	work := task.CloneWorkSpec(current.WorkSpec)
	work.Goal = "Revised qualified implementation goal"
	work.CodexInstruction = "Implement the revised qualified goal"
	variant := rs.PurposeVariant{ID: "qualified-revision", Label: "Revise the approved purpose", Summary: "Retain the exact authorized runtime", Implementation: &rs.ImplementationPurposeVariant{SourceExecutionID: proposal.ID, SourceRevisionID: owner.TaskDefinitionRevisionID, WorkSpec: work, VerificationSpec: current.VerificationSpec, RepositoryTargets: receipt.Plan.RepositoryTargets, PrimaryMemberID: receipt.Plan.PrimaryMemberID, ImplementationMode: work.ImplementationMode}}
	purpose := rs.PurposeProposal{RecoveryID: record.ID, RecoveryGeneration: record.Spec.Generation, OwnerKind: "IMPLEMENTATION", OwnerID: proposal.ID, TaskID: f.task, FeatureAreaID: f.area, SourceExecutionID: proposal.ID, TaskDefinitionRevisionID: owner.TaskDefinitionRevisionID, SourceOperationID: proposal.ID, SourceRunID: source.ID, SourceTurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AssessmentHash: record.AssessmentHash, SourceAISessionID: f.session, Variants: []rs.PurposeVariant{variant}}
	purpose.SubmittedHash = purpose.ComputeSubmittedHash()
	resolve := rs.ResolveInput{ID: record.ID, RequestID: "qualified-purpose-resolve", ActorID: spec.ActorID, RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &purpose, Admission: record.Admission}
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
	confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "qualified-purpose-confirm", ContentHash: intent.ContentHash, VariantID: variant.ID})
	if err != nil || confirmed.State != "COMPLETED" {
		t.Fatalf("purpose confirmation: %+v %v", confirmed, err)
	}
	consume := rs.PurposeConsumeInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, ContentHash: intent.ContentHash, Action: rs.ApplyConfirmedImplementationPurpose, RequestID: "qualified-purpose-consume"}
	consume.PayloadHash = consume.ComputePayloadHash()
	return consume
}

func TestRecoveryPurposeQualifiedAuthorityRetainsOriginalPlatform(t *testing.T) {
	for _, mode := range []string{"superseded-start", "invalidated-before-consume", "invalidated-before-start"} {
		t.Run(mode, func(t *testing.T) {
			f, consume, original := qualifiedRecoveryPurposeFixture(t)
			platforms := sqlite.NewRuntimePlatformStore(f.plane.DB)
			ref := original.Platform
			lineage, err := platforms.GetLineage(f.ctx, ref.PlatformLineageID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = platforms.Supersede(f.ctx, rp.QualificationInput{QualificationID: "qualified-purpose-next-platform", PlatformLineageID: ref.PlatformLineageID, DescriptorID: ref.DescriptorID, CompatibilityPolicyVersion: ref.CompatibilityPolicyVersion, CompatibilityEvidenceHash: ref.CompatibilityEvidenceHash, IssuanceRequestID: "qualified-purpose-supersede", Generation: ref.QualificationGeneration + 1, ExpectedLineageVersion: lineage.Version, ExpectedQualificationID: ref.QualificationID, ExpectedGeneration: ref.QualificationGeneration}); err != nil {
				t.Fatal(err)
			}
			invalidate := func() {
				t.Helper()
				lineage, err := platforms.GetLineage(f.ctx, ref.PlatformLineageID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = platforms.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: ref.QualificationID, Generation: ref.QualificationGeneration, ExpectedLineageVersion: lineage.Version, RequestID: "qualified-purpose-invalidate", Reason: "original qualified platform invalidated"}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "invalidated-before-consume" {
				invalidate()
			}
			before := formalExecutionCounts(t, f)
			out, err := f.facade.ApplyConfirmedImplementationPurpose(f.ctx, consume)
			if mode == "invalidated-before-consume" {
				if !errors.Is(err, rp.ErrQualificationInvalidated) {
					t.Fatalf("invalidated source granted new purpose: %+v %v", out, err)
				}
				if after := formalExecutionCounts(t, f); !reflect.DeepEqual(before, after) {
					t.Fatalf("rejected invalidated consumption wrote business facts: before=%v after=%v", before, after)
				}
				return
			}
			if err != nil || out.ExecutionID == "" || out.RunID != "" {
				t.Fatalf("superseded historical source purpose: %+v %v", out, err)
			}
			selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
			owner := runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: out.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
			authority, err := selections.ReadExecutionAuthority(f.ctx, owner, out.ExecutionID)
			if err != nil || authority.Platform != original.Platform || authority.Values != original.Values || authority.AuthoritySnapshotHash != original.AuthoritySnapshotHash || authority.BindingID != "" || authority.PurposeConsumptionID == "" {
				t.Fatalf("successor replaced original qualification/runtime: %+v %v", authority, err)
			}
			if _, err = selections.ReadCurrent(f.ctx, owner); !errors.Is(err, runtimeselection.ErrBindingMissing) {
				t.Fatalf("purpose invented a formal revision binding: %v", err)
			}
			after := formalExecutionCounts(t, f)
			for _, table := range []string{"runtime_selection_sources", "runtime_selection_bindings", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "runs"} {
				if after[table] != before[table] {
					t.Fatalf("purpose wrote forbidden %s: %d -> %d", table, before[table], after[table])
				}
			}
			var planID, requestID, payload string
			if err = f.plane.DB.SQL().QueryRow(`SELECT runtime_scope_plan_id,admission_request_id,payload_sha256 FROM execution_proposal_admissions WHERE proposal_id=?`, out.ExecutionID).Scan(&planID, &requestID, &payload); err != nil {
				t.Fatal(err)
			}
			replay, err := f.facade.ApplyConfirmedImplementationPurpose(f.ctx, consume)
			if err != nil || !replay.Replay || replay.ExecutionID != out.ExecutionID {
				t.Fatalf("superseded source exact consumption replay: %+v %v", replay, err)
			}
			if mode == "superseded-start" {
				prepared, err := sqlite.NewRunStore(f.plane.DB).CreatePreparationRun(f.ctx, planID, "superseded-purpose-run", requestID, payload, time.Now().UTC())
				if err != nil || string(prepared.Purpose) != "IMPLEMENTATION" || prepared.ExecutionProposalID != out.ExecutionID {
					t.Fatalf("superseded qualified purpose did not prepare an Implementation Run: %+v %v", prepared, err)
				}
				after = formalExecutionCounts(t, f)
			}
			beforeInvalidation, err := f.facade.WorkflowGuidance(f.ctx, f.session, f.area)
			if err != nil {
				t.Fatalf("consumed purpose workflow readback: %v", err)
			}
			invalidate()
			afterInvalidation, err := f.facade.WorkflowGuidance(f.ctx, f.session, f.area)
			if err != nil {
				t.Fatalf("historical source resolution consulted revoked platform during workflow readback: %v", err)
			}
			if afterInvalidation.CurrentState != beforeInvalidation.CurrentState || afterInvalidation.NextAction.Code != beforeInvalidation.NextAction.Code || afterInvalidation.TaskPhase != beforeInvalidation.TaskPhase {
				t.Fatalf("platform revocation changed historical progression: before=%+v after=%+v", beforeInvalidation, afterInvalidation)
			}
			if _, err = selections.ReadExecutionAuthority(f.ctx, owner, out.ExecutionID); !errors.Is(err, rp.ErrQualificationInvalidated) {
				t.Fatalf("revoked original platform still grants launch authority: %v", err)
			}
			if mode == "invalidated-before-start" {
				if _, err = sqlite.NewRunStore(f.plane.DB).CreatePreparationRun(f.ctx, planID, "invalidated-purpose-run", requestID, payload, time.Now().UTC()); !errors.Is(err, rp.ErrQualificationInvalidated) {
					t.Fatalf("revoked original platform created successor Run: %v", err)
				}
			}
			replay, err = f.facade.ApplyConfirmedImplementationPurpose(f.ctx, consume)
			if err != nil || !replay.Replay || replay.ExecutionID != out.ExecutionID {
				t.Fatalf("historical exact consume replay consulted revoked platform: %+v %v", replay, err)
			}
			if final := formalExecutionCounts(t, f); !reflect.DeepEqual(after, final) {
				t.Fatalf("revoked authority/readback wrote business facts: before=%v after=%v", after, final)
			}
		})
	}
}
