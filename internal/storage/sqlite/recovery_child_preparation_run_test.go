package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

func TestRecoveryChildAdmissionAndCanonicalRunDoNotCopySourceRuntime(t *testing.T) {
	runCanonicalRecoveryChildFixture(t, nil)
}

// The callback starts after the real child admission, isolated workspace,
// binding, thread and turn receipts are all durable. Tests supply later
// diagnostic observations without fabricating preparation authority.
func runCanonicalRecoveryChildFixture(t *testing.T, afterBound func(*testing.T, agentStorageFixture, context.Context, *RecoveryStatusStore, rs.Record, run.Run, run.Run)) {
	runCanonicalRecoveryChildFixtureStages(t, afterBound, nil)
}

func runCanonicalRecoveryChildFixtureStages(t *testing.T, afterBound func(*testing.T, agentStorageFixture, context.Context, *RecoveryStatusStore, rs.Record, run.Run, run.Run), checkpoint func(string, *agentStorageFixture, *rs.Record, *run.Run)) {
	f := newAgentStorageFixture(t, "recovery-child-canonical")
	ctx := context.Background()
	executionID := seedExecutionOwner(t, f.db, f.task, f.session, "execution-recovery-child-canonical")
	_, source, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, executionID, "source-recovery-child-canonical", testNow)
	if err != nil {
		t.Fatal(err)
	}
	const workspaceID = "source-workspace-recovery-child-canonical"
	if err = NewWorkspaceStore(f.db).Create(ctx, workspace.Workspace{ID: workspaceID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, FeatureAreaID: f.task.FeatureAreaID, OwnerKind: workspace.OwnerFeatureArea, OwnerID: f.task.FeatureAreaID, BaseCommit: strings.Repeat("a", 40), Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusPreparing, "", "")
	if _, err = NewRunStore(f.db).Update(ctx, source.ID, func(v *run.Run) error {
		v.WorkspaceID = workspaceID
		v.UpdatedAt = v.UpdatedAt.Add(time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	conversation, err := conversations.BeginRun(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := NewExecutionProposalStore(f.db).Get(ctx, executionID)
	if err != nil || proposal.BackendSelection == nil {
		t.Fatalf("source selection unavailable: %v", err)
	}
	if err = conversations.BindThreadWithRuntimeProvenance(ctx, source.ID, "source-thread-recovery-child", proposal.RuntimeConfig.Model, "source selection", *proposal.BackendSelection); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusRunning, "source-thread-recovery-child", "source-turn-recovery-child")
	if err = conversations.MarkTurnPending(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	source, err = NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, ExecutionID: executionID, RunID: source.ID, WorkspaceID: workspaceID, PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: f.task.RepositoryID, RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "source-worktree", CanonicalRoot: "/managed/source", RootFileIdentity: "source-root", OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}, RuntimeRootIdentity: "source-runtime-root"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "v1", CreatedAt: time.Now().UTC()}
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
	source, err = NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "instance"}, RequestID: "recovery-child-prepare", ActorID: "caller", RecoveryReason: "MISSING_RESULT", Instruction: "diagnose source status", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	store := NewRecoveryStatusStore(f.db)
	record, err := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil {
		t.Fatal(err)
	}
	admission := rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	requestID := "recovery-child-admit"
	payload := strings.Repeat("d", 64)
	planID, err := store.AdmitRecoveryChild(ctx, record.ID, requestID, payload, admission, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		checkpoint("admitted", &f, &record, nil)
		store = NewRecoveryStatusStore(f.db)
	}
	var targetJSON, primaryMemberID string
	if err = f.db.sql.QueryRowContext(ctx, `SELECT repository_targets_json,primary_member_id FROM runtime_scope_plans WHERE plan_id=?`, planID).Scan(&targetJSON, &primaryMemberID); err != nil {
		t.Fatal(err)
	}
	var childTargets []workset.Target
	if err = json.Unmarshal([]byte(targetJSON), &childTargets); err != nil {
		t.Fatal(err)
	}
	if len(childTargets) != 1 || childTargets[0].MemberID == w.PrimaryMemberID || childTargets[0].MemberID != primaryMemberID || childTargets[0].Write.Kind != "NONE" {
		t.Fatalf("child plan reused source member identity or write scope: %+v primary=%s", childTargets, primaryMemberID)
	}
	if replay, err := store.AdmitRecoveryChild(ctx, record.ID, requestID, payload, admission, time.Now().UTC()); err != nil || replay != planID {
		t.Fatalf("admission replay %s %v", replay, err)
	}
	var count int
	if err = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE purpose='RECOVERY_STATUS' AND task_id=?`, source.TaskID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("admission created Run count=%d err=%v", count, err)
	}
	child, err := NewRunStore(f.db).CreatePreparationRun(ctx, planID, "child-run-recovery-canonical", requestID, payload, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if replay, replayErr := store.AdmitRecoveryChild(ctx, record.ID, requestID, payload, admission, time.Now().Add(2*time.Hour)); replayErr != nil || replay != planID {
		t.Fatalf("admission replay after child Run: %s %v", replay, replayErr)
	}
	changedAdmission := admission
	changedAdmission.Before = json.RawMessage(`{"changed":true}`)
	if _, replayErr := store.AdmitRecoveryChild(ctx, record.ID, requestID, payload, changedAdmission, time.Now().UTC()); replayErr == nil {
		t.Fatal("changed admission replay accepted")
	}
	if child.ID == source.ID || child.Purpose != run.PurposeRecoveryStatus || child.WorkspaceID != "" || child.WorktreeID != "" || child.ThreadID != "" || child.RuntimeWorkset != nil {
		t.Fatalf("child copied source runtime: %+v", child)
	}
	if checkpoint != nil {
		checkpoint("run-created", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
	}
	workspaceService := workspace.NewService(NewWorkspaceStore(f.db), nil, nil)
	childWorkspace, err := workspaceService.EnsureRecoveryMembers(ctx, source.ProjectID, source.FeatureAreaID, record.ID, childTargets)
	if err != nil {
		t.Fatal(err)
	}
	if childWorkspace.ID == workspaceID || childWorkspace.OwnerKind != workspace.OwnerRecoveryStatus || len(childWorkspace.Members) != 1 || childWorkspace.Members[0].ID != primaryMemberID || childWorkspace.Members[0].WorktreeID != "" {
		t.Fatalf("Recovery Workspace copied source identity: %+v", childWorkspace)
	}
	if _, err = workspaceService.EnsureRecoveryMembers(ctx, source.ProjectID, source.FeatureAreaID, record.ID, childTargets); err != nil {
		t.Fatalf("exact Workspace readback failed: %v", err)
	}
	const childWorktreeID = "child-worktree-recovery-canonical"
	childCtx := runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	if err = NewWorktreeStore(f.db).Create(childCtx, worktree.Worktree{ID: childWorktreeID, RepositoryID: f.task.RepositoryID, RunID: child.ID, Path: "/managed/child", Branch: "recovery-child", BaseCommit: childTargets[0].OriginCommit, Status: worktree.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	childWorkspace, err = workspaceService.AttachMemberWorktree(ctx, childWorkspace.ID, primaryMemberID, childWorktreeID)
	if err != nil || childWorkspace.Members[0].WorktreeID != childWorktreeID {
		t.Fatalf("child member worktree binding failed: %+v %v", childWorkspace, err)
	}
	preparer := NewRunStore(f.db)
	claimRequest := "recovery-child-workspace-claim"
	claimPayload := strings.Repeat("e", 64)
	if replay, err := preparer.ClaimWorkspacePreparation(childCtx, planID, child.ID, claimRequest, claimPayload, time.Now().UTC()); err != nil || replay {
		t.Fatalf("workspace claim replay=%v err=%v", replay, err)
	}
	observation := runtimepreparation.WorkspaceObservation{ID: "recovery-child-observation", PlanID: planID, RunID: child.ID, WorkspaceID: childWorkspace.ID, RepositoryID: f.task.RepositoryID, MemberID: primaryMemberID, WorktreeID: childWorktreeID, CanonicalRoot: "/managed/child", RootIdentity: "child-root-identity", StartHEAD: childTargets[0].OriginCommit, StartSnapshot: strings.Repeat("f", 64), Branch: "recovery-child", MetadataDigest: strings.Repeat("a", 64), Generation: 1, ObservedAt: time.Now().UTC()}
	if _, err = preparer.RecordWorkspaceObservations(childCtx, planID, child.ID, claimRequest, claimPayload, []runtimepreparation.WorkspaceObservation{observation}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		checkpoint("observed", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		preparer = NewRunStore(f.db)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	}
	member := workset.RuntimeMemberBinding{RepositoryID: observation.RepositoryID, RepositoryAlias: "child", MemberID: primaryMemberID, MemberVersion: 1, WorktreeID: childWorktreeID, CanonicalRoot: observation.CanonicalRoot, RootFileIdentity: observation.RootIdentity, OriginCommit: childTargets[0].OriginCommit, StartHEAD: observation.StartHEAD, StartSnapshot: observation.StartSnapshot, Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: observation.RootIdentity}
	childWorkset := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersionRecoveryStatus, OwnerKind: workset.RuntimeOwnerRecoveryStatus, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, RecoveryStatusID: record.ID, RecoverySourceRunID: source.ID, RunID: child.ID, WorkspaceID: childWorkspace.ID, PrimaryMemberID: primaryMemberID, Members: []workset.RuntimeMemberBinding{member}, BackendConfigID: w.BackendConfigID, BackendConfigFingerprint: w.BackendConfigFingerprint, Model: w.Model, ReasoningEffort: w.ReasoningEffort, RuntimeAdapterIdentity: w.RuntimeAdapterIdentity, RuntimeProtocolIdentity: w.RuntimeProtocolIdentity, CreatedAt: time.Now().UTC()}
	childWorkset, err = childWorkset.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = preparer.BindPreparedRuntimeWorkset(childCtx, planID, "recovery-child-binding", strings.Repeat("1", 64), "recovery-child-start-authority", childWorkset, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		checkpoint("bound", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		preparer = NewRunStore(f.db)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	}
	if err = preparer.ValidatePreparedStart(childCtx, child.ID); err != nil {
		t.Fatalf("child start authority invalid: %v", err)
	}
	runs := run.NewService(preparer, nil, nil)
	if _, err = runs.Transition(childCtx, child.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	threadRequest, threadPayload := "recovery-child-thread", strings.Repeat("2", 64)
	threadClaim := rs.ChildThreadClaim{RecoveryID: record.ID, ChildRunID: child.ID, WorksetHash: childWorkset.ContentHash, WorkspaceID: childWorkspace.ID, RuntimeEpoch: child.RuntimeEpoch, RequestID: threadRequest, PayloadSHA256: threadPayload}
	if replay, err := store.ClaimRecoveryChildThread(childCtx, threadClaim, time.Now().UTC()); err != nil || replay {
		t.Fatalf("thread claim replay=%v err=%v", replay, err)
	}
	if checkpoint != nil {
		checkpoint("thread-claimed", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		runs = run.NewService(NewRunStore(f.db), nil, nil)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
		threadClaim.RuntimeEpoch = child.RuntimeEpoch
	}
	if replay, err := store.ClaimRecoveryChildThread(childCtx, threadClaim, time.Now().UTC()); err != nil || !replay {
		t.Fatalf("thread claim readback replay=%v err=%v", replay, err)
	}
	if err = store.RecordRecoveryChildThread(childCtx, rs.ChildThreadReceipt{RequestID: threadRequest, PayloadSHA256: threadPayload, Outcome: "CREATED", ChildThreadID: "independent-child-thread", AppServerInstanceID: "instance", BindingHash: strings.Repeat("3", 64)}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		checkpoint("thread-receipted", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		runs = run.NewService(NewRunStore(f.db), nil, nil)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	}
	if _, err = runs.AttachThread(childCtx, child.ID, "independent-child-thread"); err != nil {
		t.Fatal(err)
	}
	turnRequest, turnPayload := "recovery-child-turn", strings.Repeat("4", 64)
	turnClaim := rs.ChildTurnClaim{RecoveryID: record.ID, ThreadRequestID: threadRequest, ChildThreadID: "independent-child-thread", RequestID: turnRequest, PayloadSHA256: turnPayload}
	if replay, err := store.ClaimRecoveryChildTurn(childCtx, turnClaim, time.Now().UTC()); err != nil || replay {
		t.Fatalf("turn claim replay=%v err=%v", replay, err)
	}
	if checkpoint != nil {
		checkpoint("turn-claimed", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	}
	if _, err = store.BeforeStart(childCtx, record.ID, record.RuntimeEpoch); err != nil {
		t.Fatal(err)
	}
	if err = store.RecordRecoveryChildTurn(childCtx, rs.ChildTurnReceipt{RequestID: turnRequest, PayloadSHA256: turnPayload, Outcome: "DISPATCHED", ChildThreadID: "independent-child-thread", ChildTurnID: "independent-child-turn", DispatchAttemptID: turnRequest}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if checkpoint != nil {
		checkpoint("turn-receipted", &f, &record, &child)
		store = NewRecoveryStatusStore(f.db)
		childCtx = runtimeepoch.WithFence(ctx, child.RuntimeEpoch, child.ID)
	}
	if _, err = store.BindTurn(childCtx, record.ID, record.RuntimeEpoch, "independent-child-turn"); err != nil {
		t.Fatal(err)
	}
	if threadReceipt, found, err := store.ReadRecoveryChildThread(ctx, threadRequest); err != nil || !found || threadReceipt.ChildThreadID == source.ThreadID {
		t.Fatalf("thread receipt=%+v found=%v err=%v", threadReceipt, found, err)
	}
	if turnReceipt, found, err := store.ReadRecoveryChildTurn(ctx, turnRequest); err != nil || !found || turnReceipt.ChildTurnID != "independent-child-turn" {
		t.Fatalf("turn receipt=%+v found=%v err=%v", turnReceipt, found, err)
	}
	latest, err := NewRunStore(f.db).Get(ctx, source.ID)
	if err != nil || latest.ThreadID != source.ThreadID || latest.WorkspaceID != source.WorkspaceID || latest.RuntimeWorkset.ContentHash != w.ContentHash {
		t.Fatalf("source changed: %+v err=%v", latest, err)
	}
	if afterBound != nil {
		boundRecord, readErr := store.Read(ctx, record.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		boundChild, readErr := NewRunStore(f.db).Get(ctx, child.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		afterBound(t, f, ctx, store, boundRecord, latest, boundChild)
	}
}
