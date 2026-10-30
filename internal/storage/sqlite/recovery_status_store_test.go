package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

func recoveryStatusSourceFixture(t *testing.T) (agentStorageFixture, rs.Spec) {
	t.Helper()
	f := newAgentStorageFixture(t, "status-store")
	ctx := context.Background()
	executionID := seedExecutionOwner(t, f.db, f.task, f.session, "execution-status-store")
	_, source, e := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, executionID, "run-status-store", testNow)
	if e != nil {
		t.Fatal(e)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusPreparing, "", "")
	conversations := NewAgentConversationStore(f.db)
	conversation, e := conversations.BeginRun(ctx, source.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = conversations.BindThread(ctx, source.ID, "thread-status-store"); e != nil {
		t.Fatal(e)
	}
	setAgentRunState(t, f.db, source.ID, run.StatusRunning, "thread-status-store", "turn-status-store")
	if e = conversations.MarkTurnPending(ctx, source.ID); e != nil {
		t.Fatal(e)
	}
	source, e = NewRunStore(f.db).Get(ctx, source.ID)
	if e != nil {
		t.Fatal(e)
	}
	proposal, e := NewExecutionProposalStore(f.db).Get(ctx, source.ExecutionProposalID)
	if e != nil {
		t.Fatal(e)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, ExecutionID: source.ExecutionProposalID, RunID: source.ID, WorkspaceID: "workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: f.task.RepositoryID, RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "wt", CanonicalRoot: "/managed/test", RootFileIdentity: "root", OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}, RuntimeRootIdentity: "identity"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "v1", CreatedAt: time.Now().UTC()}
	w, e = w.WithHash()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = NewRunStore(f.db).BindRuntimeWorkset(ctx, w); e != nil {
		t.Fatal(e)
	}
	if e = NewAgentRuntimeStore(f.db).BindAppServer(ctx, source.ID, appserver.Binding{ID: "instance", Epoch: 1, Endpoint: "unix:/fake", ProcessStartIdentity: "owned"}); e != nil {
		t.Fatal(e)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, TaskDefinitionRevisionID: proposal.TaskDefinitionRevisionID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "instance"}, RequestID: "request", ActorID: "caller", RecoveryReason: "MISSING_RESULT", Instruction: "exact caller bytes\n世界", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	return f, spec
}

func TestRecoveryStatusStorePrepareReplayAndAdmissionFence(t *testing.T) {
	f, spec := recoveryStatusSourceFixture(t)
	ctx := context.Background()
	store := NewRecoveryStatusStore(f.db)
	record, e := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if e != nil {
		t.Fatal(e)
	}
	again, e := store.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if e != nil || again.ID != record.ID {
		t.Fatal("replay", e)
	}
	changed := spec
	changed.Instruction += "changed"
	changed.PayloadHash = changed.ComputePayloadHash()
	if _, e = store.Prepare(ctx, changed, rs.ReportSchemaForSource(spec.Source)); !errors.Is(e, rs.ErrConflict) {
		t.Fatal("changed instruction accepted", e)
	}
	admission := rs.Admission{SourceTurnTerminal: true, ThreadIdle: true, NoPendingRequests: true, Quiesced: true, Before: json.RawMessage(`[]`), ObservedAt: time.Now().UTC()}
	for _, name := range []string{"terminal", "idle", "pending", "quiesced"} {
		t.Run(name, func(t *testing.T) {
			bad := admission
			switch name {
			case "terminal":
				bad.SourceTurnTerminal = false
			case "idle":
				bad.ThreadIdle = false
			case "pending":
				bad.NoPendingRequests = false
			case "quiesced":
				bad.Quiesced = false
			}
			if _, e := store.AdmitRecoveryChild(ctx, record.ID, "invalid-"+name, strings.Repeat("d", 64), bad, time.Now().UTC()); !errors.Is(e, rs.ErrIneligible) {
				t.Fatal("unsafe admission", e)
			}
		})
	}
	var childRuns, plans int
	if e = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE id=?`, record.ID+":run").Scan(&childRuns); e != nil {
		t.Fatal(e)
	}
	if e = f.db.sql.QueryRowContext(ctx, `SELECT count(*) FROM runtime_scope_plans WHERE owner_kind='RECOVERY_STATUS' AND owner_id=?`, record.ID).Scan(&plans); e != nil {
		t.Fatal(e)
	}
	if childRuns != 0 || plans != 0 {
		t.Fatalf("rejected admission created child authority: runs=%d plans=%d", childRuns, plans)
	}
}
