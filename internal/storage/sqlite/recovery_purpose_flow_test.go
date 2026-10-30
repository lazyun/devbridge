package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/executionproposal"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestImplementationRevisionRequiredCreatesPurposeConfirmation(t *testing.T) {
	runExecutionPurposeFlow(t, "IMPLEMENTATION")
}

func TestVerificationRevisionRequiredCreatesPurposeConfirmation(t *testing.T) {
	runExecutionPurposeFlow(t, "VERIFICATION")
}

func runExecutionPurposeFlow(t *testing.T, owner string) {
	runExecutionPurposeFlowBeforeCommit(t, owner, nil)
}

type purposeBeforeCommit func(*testing.T, *DB, context.Context, *RecoveryStatusStore, rs.Spec, rs.Record, confirmation.Intent) confirmation.Intent

func runExecutionPurposeFlowBeforeCommit(t *testing.T, owner string, beforeCommit purposeBeforeCommit) {
	runExecutionPurposeFlowWithHooks(t, owner, executionPurposeHooks{beforeCommit: beforeCommit})
}

type executionPurposeHooks struct {
	preparePurpose func(*rs.PurposeProposal)
	beforeCommit   purposeBeforeCommit
	// A true return means the hook owns its rejection assertions and ends the flow.
	beforeConsume func(*testing.T, *DB, context.Context, confirmation.Intent, rs.PurposeConsumeInput) bool
	afterConsume  func(*testing.T, *DB, context.Context, confirmation.Intent, rs.PurposeConsumeInput, rs.PurposeConsumption)
}

func runExecutionPurposeFlowWithHooks(t *testing.T, owner string, hooks executionPurposeHooks) {
	suffix := "purpose-" + strings.ToLower(owner)
	f := newAgentStorageFixture(t, suffix)
	ctx := aisession.WithCallerScope(context.Background(), "agent-fixture-"+suffix)
	executionID := seedExecutionOwner(t, f.db, f.task, f.session, "execution-"+suffix)
	_, source, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, executionID, "run-"+suffix, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if owner == "VERIFICATION" {
		// The recovery fixture begins after an Implementation Run has completed.
		// ReserveVerificationRun is the production source admission path.
		if _, err = f.db.sql.ExecContext(ctx, `UPDATE runs SET status='SUCCEEDED',finished_at=?,updated_at=? WHERE id=?`, formatTime(testNow), formatTime(testNow), source.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.sql.ExecContext(ctx, `UPDATE tasks SET phase='VERIFICATION_READY' WHERE id=?`, source.TaskID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.sql.ExecContext(ctx, `DELETE FROM feature_area_agent_claims WHERE run_id=?`, source.ID); err != nil {
			t.Fatal(err)
		}
		_, source, err = NewExecutionProposalStore(f.db).ReserveVerificationRun(ctx, executionID, "verify-"+suffix, testNow.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	proposal, err := NewExecutionProposalStore(f.db).Get(ctx, executionID)
	if err != nil || proposal.BackendSelection == nil {
		t.Fatalf("source backend selection: %v %+v", err, proposal)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusPreparing, "", "")
	conversation, err := NewAgentConversationStore(f.db).BeginRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = NewAgentConversationStore(f.db).BindThreadWithRuntimeProvenance(ctx, source.ID, "thread-purpose", proposal.RuntimeConfig.Model, "source-selection", *proposal.BackendSelection); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusRunning, "thread-purpose", "turn-purpose")
	if err = NewAgentConversationStore(f.db).MarkTurnPending(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	source, err = NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, ExecutionID: executionID, RunID: source.ID, WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: f.task.RepositoryID, RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "wt", CanonicalRoot: "/managed/test", RootFileIdentity: "root", OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}, RuntimeRootIdentity: "identity"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "v1", CreatedAt: time.Now().UTC()}
	// A historical source Workset observes the configuration actually used by
	// its consumed Execution. It cannot invent a new backend or model to make a
	// later purpose proposal look authorized.
	w.BackendConfigID, w.BackendConfigFingerprint = proposal.BackendSelection.ConfigID, proposal.BackendSelection.ConfigFingerprint
	w.Model, w.ReasoningEffort, w.ServiceTier = proposal.RuntimeConfig.Model, proposal.RuntimeConfig.ReasoningEffort, proposal.RuntimeConfig.ServiceTier
	w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity = proposal.RuntimeAdapterIdentity, proposal.RuntimeProtocolIdentity
	if w.RuntimeAdapterIdentity == "" {
		w.RuntimeAdapterIdentity = executionproposal.RuntimeAdapterCodexAppServerV1
	}
	if w.RuntimeProtocolIdentity == "" {
		w.RuntimeProtocolIdentity = executionproposal.RuntimeProtocolCodex0149
	}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunStore(f.db).BindRuntimeWorkset(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, source.ID, appserver.Binding{ID: "instance", Epoch: 1, Endpoint: "unix:/fake", ProcessStartIdentity: "owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "instance"}, RequestID: "purpose-prepare", ActorID: "operator-fixture", RecoveryReason: "MISSING_RESULT", Instruction: "Inspect current state", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	store := NewRecoveryStatusStore(f.db)
	record, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentExecutionID != executionID {
		t.Fatalf("source Execution not current: %s", current.CurrentExecutionID)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, current.ID); err != nil {
		t.Fatal(err)
	}
	current.Phase = task.PhaseRecoveryRequired
	report := []byte(`{"summary":"purpose revision required"}`)
	assessment := []byte(`{"consistency":"CONSISTENT","engineeringDisposition":"REVISION_REQUIRED"}`)
	admission := rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	record.State, record.Report, record.After, record.Assessment, record.Admission = "ASSESSED", report, json.RawMessage(`[]`), assessment, admission
	record.ReportHash, record.AssessmentHash = rs.Hash(report), rs.Hash(assessment)
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveRecoveryTx(ctx, tx, record); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	revised := current.WorkSpec
	revised.Goal = "Revised goal"
	revised.CodexInstruction = "Implement revised goal"
	revised.ExecutionScope = task.ExecutionScope{Kind: task.ExecutionScopeTargets}
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "explicit test waiver"}}
	target := workset.Target{RepositoryID: f.task.RepositoryID, MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}}
	variant := rs.PurposeVariant{ID: "revised", Label: "Revised implementation", Summary: "Implement revised goal"}
	if owner == "VERIFICATION" {
		variant.Verification = &rs.VerificationPurposeVariant{SourceExecutionID: executionID, SourceRevisionID: current.CurrentDefinitionRevisionID, WorkSpec: &revised, VerificationSpec: verification, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: "member", ImplementationMode: revised.ImplementationMode}
	} else {
		variant.Implementation = &rs.ImplementationPurposeVariant{SourceExecutionID: executionID, SourceRevisionID: current.CurrentDefinitionRevisionID, WorkSpec: revised, VerificationSpec: verification, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: "member", ImplementationMode: revised.ImplementationMode}
	}
	purpose := rs.PurposeProposal{RecoveryID: record.ID, RecoveryGeneration: 1, OwnerKind: owner, OwnerID: executionID, TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, SourceExecutionID: executionID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, SourceOperationID: executionID, SourceRunID: source.ID, SourceTurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AssessmentHash: record.AssessmentHash, SourceAISessionID: f.session.ID, Variants: []rs.PurposeVariant{variant}}
	if hooks.preparePurpose != nil {
		hooks.preparePurpose(&purpose)
	}
	purpose.SubmittedHash = purpose.ComputeSubmittedHash()
	if err = purpose.Validate(spec.Source, record.ID, 1, record.AssessmentHash); err != nil {
		t.Fatalf("typed proposal invalid: %v work=%v verify=%v targets=%v", err, task.ValidateTargetWorkSpec(revised), task.ValidateTargetVerification(verification, []workset.Target{target}), workset.ValidateTargets([]workset.Target{target}))
	}
	var beforeExecutions, beforeRuns, beforeRevisions int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM execution_proposals WHERE task_id=?),(SELECT count(*) FROM runs WHERE task_id=?),(SELECT count(*) FROM task_definition_revisions WHERE task_id=?)`, current.ID, current.ID, current.ID).Scan(&beforeExecutions, &beforeRuns, &beforeRevisions); err != nil {
		t.Fatal(err)
	}
	for _, negative := range []struct {
		name, session string
		want          error
	}{
		{"missing", "", rs.ErrPurposeSourceSessionUnavailable},
		{"mismatched", "different-ai-session", rs.ErrPurposeSourceSessionMismatch},
	} {
		bad := purpose
		bad.SourceAISessionID = negative.session
		bad.SubmittedHash = bad.ComputeSubmittedHash()
		attempt := rs.ResolveInput{ID: record.ID, RequestID: "resolve-purpose-" + negative.name, ActorID: "operator-fixture", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &bad, Admission: admission}
		attempt.PayloadHash = attempt.ComputePayloadHash()
		if _, err = store.Resolve(ctx, attempt); !errors.Is(err, negative.want) {
			t.Fatalf("%s source AI session: got %v want %v", negative.name, err, negative.want)
		}
		still, readErr := store.Read(ctx, record.ID)
		if readErr != nil || still.State != "ASSESSED" {
			t.Fatalf("%s source AI session changed Recovery: %+v %v", negative.name, still, readErr)
		}
		var links int
		if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM recovery_purpose_confirmation_links WHERE recovery_id=?`, record.ID).Scan(&links); err != nil || links != 0 {
			t.Fatalf("%s source AI session wrote link: %d %v", negative.name, links, err)
		}
	}
	in := rs.ResolveInput{ID: record.ID, RequestID: "resolve-purpose", ActorID: "operator-fixture", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &purpose, Admission: admission}
	in.PayloadHash = in.ComputePayloadHash()
	resolved, err := store.Resolve(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != "RESOLVED" {
		t.Fatalf("state=%s", resolved.State)
	}
	replayedResolve, err := store.Resolve(ctx, in)
	if err != nil || replayedResolve.ID != resolved.ID || replayedResolve.State != "RESOLVED" {
		t.Fatalf("Recovery Resolve replay=%+v err=%v", replayedResolve, err)
	}
	changedResolve := in
	changedProposal := purpose
	changedProposal.Variants = append([]rs.PurposeVariant(nil), purpose.Variants...)
	changedProposal.Variants[0].Summary = "Different submitted purpose"
	changedProposal.SubmittedHash = changedProposal.ComputeSubmittedHash()
	changedResolve.Purpose = &changedProposal
	changedResolve.PayloadHash = changedResolve.ComputePayloadHash()
	if _, err = store.Resolve(ctx, changedResolve); !errors.Is(err, rs.ErrConflict) {
		t.Fatalf("same Resolve request with changed proposal error=%v", err)
	}
	projection, err := store.ReadPurposeConfirmation(ctx, record.ID)
	if err != nil || projection.State != "PENDING" || projection.IntentID == "" {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	intent, err := NewConfirmationStore(f.db).Get(ctx, projection.AISessionID, projection.IntentID)
	if err != nil || intent.Purpose != confirmation.RecoveryPurpose || intent.Content.RecoveryPurpose == nil || len(intent.Content.Variants) != 1 {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	current, err = NewTaskStore(f.db).Get(ctx, current.ID)
	if err != nil || current.Phase != task.PhaseRecoveryRequired || current.CurrentExecutionID != executionID || current.CurrentDefinitionRevisionID != purpose.TaskDefinitionRevisionID {
		t.Fatalf("Task changed %+v %v", current, err)
	}
	var executionCount, runCount, revisionCount, receiptCount int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM execution_proposals WHERE task_id=?),(SELECT count(*) FROM runs WHERE task_id=?),(SELECT count(*) FROM task_definition_revisions WHERE task_id=?),(SELECT count(*) FROM recovery_purpose_confirmation_receipts WHERE recovery_id=?)`, current.ID, current.ID, current.ID, record.ID).Scan(&executionCount, &runCount, &revisionCount, &receiptCount); err != nil {
		t.Fatal(err)
	}
	if executionCount != beforeExecutions || runCount != beforeRuns || revisionCount != beforeRevisions || receiptCount != 0 {
		t.Fatalf("Resolve created successors execution=%d/%d run=%d/%d revision=%d/%d receipt=%d", executionCount, beforeExecutions, runCount, beforeRuns, revisionCount, beforeRevisions, receiptCount)
	}
	if hooks.beforeCommit != nil {
		intent = hooks.beforeCommit(t, f.db, ctx, store, spec, resolved, intent)
		if intent.IntentID == "" {
			return
		}
	}
	commit := confirmation.CommitInput{AISessionID: f.session.ID, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "commit-purpose", ContentHash: intent.ContentHash, VariantID: "revised"}
	commit.SubmittedHash = confirmation.ValueHash(commit)
	step := confirmation.Step{Kind: "WRITE_PURPOSE_RECEIPT", RequestID: "purpose-step", State: "PENDING"}
	confirmations := NewConfirmationStore(f.db)
	confirming, fresh, err := confirmations.BeginCommit(ctx, commit, []confirmation.Step{step})
	if err != nil || !fresh || confirming.State != "CONFIRMING" {
		t.Fatalf("BeginCommit intent=%+v fresh=%v err=%v", confirming, fresh, err)
	}
	step.State = "UNKNOWN"
	if _, err = confirmations.RecordStep(ctx, f.session.ID, intent.IntentID, intent.Version, step, "CONFIRMING"); err != nil {
		t.Fatal(err)
	}
	if err = confirmations.WriteRecoveryPurposeReceipt(ctx, commit); err != nil {
		t.Fatal(err)
	}
	step.State, step.Effect = "COMPLETED", "COMMAND_COMPLETED"
	if _, err = confirmations.RecordStep(ctx, f.session.ID, intent.IntentID, intent.Version, step, "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.sql.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM execution_proposals WHERE task_id=?),(SELECT count(*) FROM runs WHERE task_id=?),(SELECT count(*) FROM task_definition_revisions WHERE task_id=?)`, current.ID, current.ID, current.ID).Scan(&executionCount, &runCount, &revisionCount); err != nil {
		t.Fatal(err)
	}
	if executionCount != beforeExecutions || runCount != beforeRuns || revisionCount != beforeRevisions {
		t.Fatal("Confirmation commit created successor authority")
	}
	action := rs.ApplyConfirmedImplementationPurpose
	if owner == "VERIFICATION" {
		action = rs.ApplyConfirmedVerificationPurpose
	}
	consume := rs.PurposeConsumeInput{AISessionID: f.session.ID, IntentID: intent.IntentID, Version: intent.Version, ContentHash: intent.ContentHash, Action: action, RequestID: "consume-purpose"}
	consume.PayloadHash = consume.ComputePayloadHash()
	if hooks.beforeConsume != nil && hooks.beforeConsume(t, f.db, ctx, intent, consume) {
		return
	}
	consumer := NewRecoveryPurposeStore(f.db)
	wrong := consume
	wrong.Action = rs.ApplyConfirmedReviewPurpose
	wrong.PayloadHash = wrong.ComputePayloadHash()
	if _, err = consumer.ApplyConfirmedReviewPurpose(ctx, wrong); !errors.Is(err, rs.ErrPurposeOwnerMismatch) {
		t.Fatalf("cross-owner consume error=%v", err)
	}
	badReceipt := consume
	badReceipt.ContentHash = strings.Repeat("0", 64)
	badReceipt.PayloadHash = badReceipt.ComputePayloadHash()
	if owner == "VERIFICATION" {
		_, err = consumer.ApplyConfirmedVerificationPurpose(ctx, badReceipt)
	} else {
		_, err = consumer.ApplyConfirmedImplementationPurpose(ctx, badReceipt)
	}
	if !errors.Is(err, rs.ErrPurposeReceiptMismatch) {
		t.Fatalf("mismatched receipt error=%v", err)
	}
	var consumed rs.PurposeConsumption
	if owner == "VERIFICATION" {
		consumed, err = consumer.ApplyConfirmedVerificationPurpose(ctx, consume)
	} else {
		consumed, err = consumer.ApplyConfirmedImplementationPurpose(ctx, consume)
	}
	if err != nil {
		t.Fatal(err)
	}
	if consumed.ExecutionID == "" || consumed.TaskDefinitionRevisionID == "" || consumed.RunID != "" || consumed.TaskPhase != "IMPLEMENTATION_READY" {
		t.Fatalf("consumption=%+v", consumed)
	}
	var planID, authorityOwner string
	if err = f.db.sql.QueryRowContext(ctx, `SELECT p.plan_id,a.owner_id FROM runtime_scope_plans p JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id WHERE p.owner_kind='EXECUTION' AND p.owner_id=?`, consumed.ExecutionID).Scan(&planID, &authorityOwner); err != nil || planID == "" || authorityOwner != consumed.ExecutionID {
		t.Fatalf("purpose consume did not freeze exact Plan/preparation authority: plan=%q owner=%q err=%v", planID, authorityOwner, err)
	}
	var successorRuns int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE execution_proposal_id=?`, consumed.ExecutionID).Scan(&successorRuns); err != nil || successorRuns != 0 {
		t.Fatalf("purpose consume created a Run: count=%d err=%v", successorRuns, err)
	}
	var again rs.PurposeConsumption
	if owner == "VERIFICATION" {
		again, err = consumer.ApplyConfirmedVerificationPurpose(ctx, consume)
	} else {
		again, err = consumer.ApplyConfirmedImplementationPurpose(ctx, consume)
	}
	if err != nil || !again.Replay || again.ExecutionID != consumed.ExecutionID {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	changed := consume
	changed.ContentHash = strings.Repeat("1", 64)
	changed.PayloadHash = changed.ComputePayloadHash()
	if owner == "VERIFICATION" {
		_, err = consumer.ApplyConfirmedVerificationPurpose(ctx, changed)
	} else {
		_, err = consumer.ApplyConfirmedImplementationPurpose(ctx, changed)
	}
	if !errors.Is(err, rs.ErrPurposeRequestConflict) {
		t.Fatalf("same request with changed payload error=%v", err)
	}
	late := consume
	late.RequestID = "another-consumer"
	late.PayloadHash = late.ComputePayloadHash()
	if owner == "VERIFICATION" {
		_, err = consumer.ApplyConfirmedVerificationPurpose(ctx, late)
	} else {
		_, err = consumer.ApplyConfirmedImplementationPurpose(ctx, late)
	}
	if !errors.Is(err, rs.ErrPurposeAlreadyConsumed) {
		t.Fatalf("second consume request error=%v", err)
	}
	if hooks.afterConsume != nil {
		hooks.afterConsume(t, f.db, ctx, intent, consume, consumed)
	}
}

func TestReviewRevisionRequiredCreatesPurposeConfirmation(t *testing.T) {
	runReviewPurposeFlowBeforeCommit(t, nil)
}

func runReviewPurposeFlowBeforeCommit(t *testing.T, beforeCommit purposeBeforeCommit) {
	f, _ := newTaskOwnedFixture(t)
	ctx := aisession.WithCallerScope(context.Background(), "owned-caller")
	started := f.start(t)
	source := started.Run
	setAgentRunState(t, f.db, source.ID, run.StatusPreparing, "", "")
	conversation, err := NewAgentConversationStore(f.db).BeginRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = NewAgentConversationStore(f.db).BindThreadWithRuntimeProvenance(ctx, source.ID, "thread-review-purpose", "model", "review-source", *f.candidate.BackendSelection); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusRunning, "thread-review-purpose", "turn-review-purpose")
	if err = NewAgentConversationStore(f.db).MarkTurnPending(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	source, err = NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, ReviewID: started.Review.ID, TaskDefinitionRevisionID: ownedTask(t, f).CurrentDefinitionRevisionID, RunID: source.ID, WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: f.candidate.RepositoryID, RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "wt", CanonicalRoot: "/managed/review", RootFileIdentity: "root", OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "identity"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "v1", CreatedAt: time.Now().UTC()}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunStore(f.db).BindRuntimeWorkset(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, source.ID, appserver.Binding{ID: "review-instance", Epoch: 1, Endpoint: "unix:/fake", ProcessStartIdentity: "owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE agent_operation_boundaries SET quiesced=1 WHERE run_id=?`, source.ID); err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: w.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "review-instance"}, RequestID: "review-purpose-prepare", ActorID: "operator-fixture", RecoveryReason: "MISSING_RESULT", Instruction: "Inspect current Review state", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	store := NewRecoveryStatusStore(f.db)
	record, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil {
		t.Fatal(err)
	}
	current, err := NewTaskStore(f.db).Get(ctx, source.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if current.CurrentReviewID != started.Review.ID {
		t.Fatalf("source Review not current: %s", current.CurrentReviewID)
	}
	if _, err = f.db.sql.ExecContext(ctx, `UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, current.ID); err != nil {
		t.Fatal(err)
	}
	report := []byte(`{"summary":"review purpose revision required"}`)
	assessment := []byte(`{"consistency":"CONSISTENT","engineeringDisposition":"REVISION_REQUIRED"}`)
	admission := rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	record.State, record.Report, record.After, record.Assessment, record.Admission = "ASSESSED", report, json.RawMessage(`[]`), assessment, admission
	record.ReportHash, record.AssessmentHash = rs.Hash(report), rs.Hash(assessment)
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = saveRecoveryTx(ctx, tx, record); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	target := workset.Target{RepositoryID: f.candidate.RepositoryID, MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	purpose := rs.PurposeProposal{RecoveryID: record.ID, RecoveryGeneration: 1, OwnerKind: "REVIEW", OwnerID: started.Review.ID, TaskID: current.ID, FeatureAreaID: current.FeatureAreaID, SourceReviewID: started.Review.ID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, SourceOperationID: started.Review.ID, SourceRunID: source.ID, SourceTurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AssessmentHash: record.AssessmentHash, SourceAISessionID: f.session, Variants: []rs.PurposeVariant{{ID: "revised", Label: "Revised Review", Summary: "Review revised scope", Review: &rs.ReviewPurposeVariant{SourceReviewID: started.Review.ID, ReviewInstruction: "Inspect the revised scope", ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope}, ReviewDepth: task.ReviewDepthDeep, SupplementalPurpose: "Reassess the Review", RepositoryTargets: []workset.Target{target}}}}}
	purpose.SubmittedHash = purpose.ComputeSubmittedHash()
	if err = purpose.Validate(spec.Source, record.ID, record.Spec.Generation, record.AssessmentHash); err != nil {
		t.Fatalf("Review typed purpose fixture invalid: purpose=%+v source=%+v err=%v", purpose, spec.Source, err)
	}
	for _, negative := range []struct {
		name, session string
		want          error
	}{
		{"missing", "", rs.ErrPurposeSourceSessionUnavailable},
		{"mismatched", "different-ai-session", rs.ErrPurposeSourceSessionMismatch},
	} {
		bad := purpose
		bad.SourceAISessionID = negative.session
		bad.SubmittedHash = bad.ComputeSubmittedHash()
		attempt := rs.ResolveInput{ID: record.ID, RequestID: "resolve-review-purpose-" + negative.name, ActorID: "operator-fixture", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &bad, Admission: admission}
		attempt.PayloadHash = attempt.ComputePayloadHash()
		if _, err = store.Resolve(ctx, attempt); !errors.Is(err, negative.want) {
			t.Fatalf("%s Review source AI session: got %v want %v", negative.name, err, negative.want)
		}
		still, readErr := store.Read(ctx, record.ID)
		if readErr != nil || still.State != "ASSESSED" {
			t.Fatalf("%s Review source AI session changed Recovery: %+v %v", negative.name, still, readErr)
		}
	}
	in := rs.ResolveInput{ID: record.ID, RequestID: "resolve-review-purpose", ActorID: "operator-fixture", RuntimeEpoch: record.RuntimeEpoch, AssessmentHash: record.AssessmentHash, ReportHash: record.ReportHash, Purpose: &purpose, Admission: admission}
	in.PayloadHash = in.ComputePayloadHash()
	resolved, err := store.Resolve(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != "RESOLVED" {
		t.Fatalf("state=%s", resolved.State)
	}
	projection, err := store.ReadPurposeConfirmation(ctx, record.ID)
	if err != nil || projection.IntentID == "" || projection.State != "PENDING" {
		t.Fatalf("projection=%+v err=%v", projection, err)
	}
	intent, err := NewConfirmationStore(f.db).Get(ctx, f.session, projection.IntentID)
	if err != nil || intent.Purpose != confirmation.RecoveryPurpose {
		t.Fatalf("intent=%+v err=%v", intent, err)
	}
	if beforeCommit != nil {
		intent = beforeCommit(t, f.db, ctx, store, spec, resolved, intent)
		if intent.IntentID == "" {
			return
		}
	}
	commit := confirmation.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "commit-review-purpose", ContentHash: intent.ContentHash, VariantID: "revised"}
	commit.SubmittedHash = confirmation.ValueHash(commit)
	step := confirmation.Step{Kind: "WRITE_PURPOSE_RECEIPT", RequestID: "review-purpose-step", State: "PENDING"}
	confirmations := NewConfirmationStore(f.db)
	confirming, fresh, err := confirmations.BeginCommit(ctx, commit, []confirmation.Step{step})
	if err != nil || !fresh || confirming.State != "CONFIRMING" {
		t.Fatalf("BeginCommit intent=%+v fresh=%v err=%v", confirming, fresh, err)
	}
	step.State = "UNKNOWN"
	if _, err = confirmations.RecordStep(ctx, f.session, intent.IntentID, intent.Version, step, "CONFIRMING"); err != nil {
		t.Fatal(err)
	}
	if err = confirmations.WriteRecoveryPurposeReceipt(ctx, commit); err != nil {
		t.Fatal(err)
	}
	step.State, step.Effect = "COMPLETED", "COMMAND_COMPLETED"
	if _, err = confirmations.RecordStep(ctx, f.session, intent.IntentID, intent.Version, step, "COMPLETED"); err != nil {
		t.Fatal(err)
	}
	consume := rs.PurposeConsumeInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, ContentHash: intent.ContentHash, Action: rs.ApplyConfirmedReviewPurpose, RequestID: "consume-review-purpose"}
	consume.PayloadHash = consume.ComputePayloadHash()
	consumer := NewRecoveryPurposeStore(f.db)
	wrong := consume
	wrong.Action = rs.ApplyConfirmedImplementationPurpose
	wrong.PayloadHash = wrong.ComputePayloadHash()
	if _, err = consumer.ApplyConfirmedImplementationPurpose(ctx, wrong); !errors.Is(err, rs.ErrPurposeOwnerMismatch) {
		t.Fatalf("cross-owner consume error=%v", err)
	}
	consumed, err := consumer.ApplyConfirmedReviewPurpose(ctx, consume)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.ReviewID == "" || consumed.RunID != "" || consumed.TaskPhase != "REVIEWING" {
		t.Fatalf("consumption=%+v", consumed)
	}
	var planID, authorityOwner string
	if err = f.db.sql.QueryRowContext(ctx, `SELECT p.plan_id,a.owner_id FROM runtime_scope_plans p JOIN runtime_preparation_authorities a ON a.plan_id=p.plan_id WHERE p.owner_kind='REVIEW' AND p.owner_id=?`, consumed.ReviewID).Scan(&planID, &authorityOwner); err != nil || planID == "" || authorityOwner != consumed.ReviewID {
		t.Fatalf("Review consume did not freeze exact Plan/preparation authority: plan=%q owner=%q err=%v", planID, authorityOwner, err)
	}
	var successorRuns int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE review_id=?`, consumed.ReviewID).Scan(&successorRuns); err != nil || successorRuns != 0 {
		t.Fatalf("Review consume created a Run: count=%d err=%v", successorRuns, err)
	}
	var prepRequest, prepPayload string
	if err = f.db.sql.QueryRowContext(ctx, `SELECT preparation_request_id,preparation_payload_hash FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&prepRequest, &prepPayload); err != nil {
		t.Fatal(err)
	}
	preparer := NewRunStore(f.db)
	preparedRun, err := preparer.CreatePreparationRun(ctx, planID, "run_review_purpose_prepared", prepRequest, prepPayload, time.Now().UTC())
	if err != nil || preparedRun.ID == "" || preparedRun.RuntimeWorkset != nil {
		t.Fatalf("prepare durable Run=%+v err=%v", preparedRun, err)
	}
	claimRequest, claimPayload := "workspace_review_purpose_prepared", strings.Repeat("c", 64)
	claimedAt := time.Now().UTC()
	if replay, e := preparer.ClaimWorkspacePreparation(ctx, planID, preparedRun.ID, claimRequest, claimPayload, claimedAt); e != nil || replay {
		t.Fatalf("workspace claim replay=%t err=%v", replay, e)
	}
	root := filepath.Join(t.TempDir(), "fresh-review-repository")
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", root}, {"-C", root, "config", "user.email", "test@example.invalid"}, {"-C", root, "config", "user.name", "Test"}, {"-C", root, "commit", "--allow-empty", "-m", "fresh"}} {
		if output, e := exec.Command("git", args...).CombinedOutput(); e != nil {
			t.Fatalf("git %v: %v %s", args, e, output)
		}
	}
	headBytes, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(string(headBytes))
	source, err = NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.RuntimeWorkset == nil {
		t.Fatal("source fixture lacks a Workset")
	}
	freshWorkset := *source.RuntimeWorkset
	freshWorkset.ReviewID, freshWorkset.RunID, freshWorkset.WorkspaceID, freshWorkset.CreatedAt = consumed.ReviewID, preparedRun.ID, source.RuntimeWorkset.WorkspaceID, time.Now().UTC()
	freshWorkset.Members = append([]workset.RuntimeMemberBinding(nil), source.RuntimeWorkset.Members...)
	if len(freshWorkset.Members) != 1 {
		t.Fatalf("fixture has %d members", len(freshWorkset.Members))
	}
	member := &freshWorkset.Members[0]
	member.WorktreeID, member.CanonicalRoot, member.RootFileIdentity, member.RuntimeRootIdentity = "fresh-review-worktree", root, "fresh-root-identity", "fresh-runtime-root"
	member.StartHEAD, member.StartSnapshot = head, head
	freshWorkset.ContentHash, err = freshWorkset.ComputeContentHash()
	if err != nil {
		t.Fatal(err)
	}
	if err = freshWorkset.Validate(); err != nil {
		t.Fatal(err)
	}
	observedAt := time.Now().UTC()
	observation := runtimepreparation.WorkspaceObservation{ID: "fresh-review-observation", PlanID: planID, RunID: preparedRun.ID, WorkspaceID: freshWorkset.WorkspaceID, RepositoryID: member.RepositoryID, MemberID: member.MemberID, WorktreeID: member.WorktreeID, CanonicalRoot: root, RootIdentity: member.RootFileIdentity, StartHEAD: head, StartSnapshot: head, Branch: "refs/heads/master", MetadataDigest: strings.Repeat("d", 64), Generation: 1, ObservedAt: observedAt}
	if _, err = preparer.RecordWorkspaceObservations(ctx, planID, preparedRun.ID, claimRequest, claimPayload, []runtimepreparation.WorkspaceObservation{observation}, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatalf("fresh HEAD mismatch must fail closed: %v", err)
	}
	var boundCount int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runtime_workset_bindings WHERE plan_id=?`, planID).Scan(&boundCount); err != nil || boundCount != 0 {
		t.Fatalf("stale observation created a binding: count=%d err=%v", boundCount, err)
	}
	var ordinal int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT ordinal FROM reviews WHERE id=?`, consumed.ReviewID).Scan(&ordinal); err != nil {
		t.Fatal(err)
	}
	if ordinal != started.Review.Ordinal+1 {
		t.Fatalf("successor ordinal=%d source=%d", ordinal, started.Review.Ordinal)
	}
	again, err := consumer.ApplyConfirmedReviewPurpose(ctx, consume)
	if err != nil || !again.Replay || again.ReviewID != consumed.ReviewID || again.RunID != consumed.RunID {
		t.Fatalf("Review exact replay=%+v err=%v", again, err)
	}
	changed := consume
	changed.Version++
	changed.PayloadHash = changed.ComputePayloadHash()
	if _, err = consumer.ApplyConfirmedReviewPurpose(ctx, changed); !errors.Is(err, rs.ErrPurposeRequestConflict) {
		t.Fatalf("Review request conflict=%v", err)
	}
	late := consume
	late.RequestID = "another-review-consumer"
	late.PayloadHash = late.ComputePayloadHash()
	if _, err = consumer.ApplyConfirmedReviewPurpose(ctx, late); !errors.Is(err, rs.ErrPurposeAlreadyConsumed) {
		t.Fatalf("Review already consumed=%v", err)
	}
}
