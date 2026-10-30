package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	rs "github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestNoRunRevisionReviewRecoverySurvivesOldCancellation(t *testing.T) {
	ctx := context.Background()
	f, owner, predecessor := acceptedExecutionFixture(t, "-review-recovery-replay")
	readyNoRunSuccessor(t, f, owner, predecessor, "cancel-before-review-recovery")
	ctx = retryCallerCtx(t, f, ctx)
	if _, err := f.store.StopAndReviseNoRunTask(ctx, wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID, ActorAuthority: f.session}); err != nil {
		t.Fatal(err)
	}
	started := f.start(t)
	clock := acceptanceTestClock(started.Run.CreatedAt.Add(time.Minute))
	runs := run.NewService(NewRunStore(f.db), nil, clock)
	if _, err := runs.Transition(ctx, started.Run.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	conversations := NewAgentConversationStore(f.db)
	conversation, err := conversations.BeginRun(ctx, started.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThreadWithRuntimeProvenance(ctx, started.Run.ID, "revision-review-thread", "model", "review-source", *f.candidate.BackendSelection); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, started.Run.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	source, err := runs.AttachTurn(ctx, started.Run.ID, "revision-review-turn")
	if err != nil {
		t.Fatal(err)
	}
	if err = conversations.MarkTurnPending(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, ReviewID: started.Review.ID, RunID: source.ID, WorkspaceID: "revision-review-workspace", PrimaryMemberID: "member", Members: []workset.RuntimeMemberBinding{{RepositoryID: f.candidate.RepositoryID, RepositoryAlias: "app", MemberID: "member", MemberVersion: 1, WorktreeID: "revision-review-worktree", CanonicalRoot: "/managed/revision-review", RootFileIdentity: "root", OriginCommit: strings.Repeat("a", 40), StartHEAD: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 64), Read: true, Write: workset.WriteScope{Kind: "NONE"}, RuntimeRootIdentity: "identity"}}, BackendConfigID: "config", BackendConfigFingerprint: strings.Repeat("c", 64), Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex", RuntimeProtocolIdentity: "v1", CreatedAt: time.Now().UTC()}
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runs.BindRuntimeWorkset(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, source.ID, appserver.Binding{ID: "revision-review-instance", Epoch: 1, Endpoint: "unix:/fixture", ProcessStartIdentity: "owned"}); err != nil {
		t.Fatal(err)
	}
	spec := rs.Spec{Source: rs.Source{ProjectID: source.ProjectID, FeatureAreaID: source.FeatureAreaID, TaskID: source.TaskID, OperationKind: string(source.OperationKind), OperationID: source.OperationID, RunID: source.ID, TurnID: source.TurnID, RuntimeEpoch: source.RuntimeEpoch, RuntimeWorksetHash: w.ContentHash, AgentConversationID: conversation.ID, ThreadID: source.ThreadID, AppServerInstanceID: "revision-review-instance"}, RequestID: "revision-review-recovery", ActorID: "fixture-observer", RecoveryReason: "MISSING_RESULT", Instruction: "Inspect the current Review without advancing it", OutputSchemaVersion: "v1", Generation: 1}
	spec.PayloadHash = spec.ComputePayloadHash()
	recovery := NewRecoveryStatusStore(f.db)
	record, err := recovery.Prepare(ctx, spec, rs.ReportSchemaForSource(spec.Source))
	if err != nil || record.State != "PREPARED" {
		t.Fatalf("canonical Review recovery: id=%s state=%s err=%v", record.ID, record.State, err)
	}
	// Compare persisted representations, not Prepare's in-memory nil/empty
	// fields against the JSON-normalized record returned by a later read.
	record, err = recovery.Read(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	history := noRunRevisionHistory(t, f.db, predecessor.ID)
	// Run the production boot lifecycle reconciler over the real current Review
	// recovery. Its old canceled Execution must not suppress this new owner.
	if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, err := NewTaskStore(f.db).Get(ctx, owner.ID)
	if err != nil || before.Phase != task.PhaseRecoveryRequired || before.CurrentReviewID != started.Review.ID || before.CurrentExecutionID != predecessor.ID {
		t.Fatalf("current Review recovery was hidden by the old no-Run cancellation: %+v err=%v", before, err)
	}
	for i := 0; i < 2; i++ {
		if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
			t.Fatalf("old cancellation replay rejected after Review recovery: %v", err)
		}
		if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		after, err := NewTaskStore(f.db).Get(ctx, owner.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("old cancellation replay/reconcile replaced Review recovery: before=%+v after=%+v err=%v", before, after, err)
		}
		kept, err := recovery.Read(ctx, record.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(record, kept) {
			t.Fatalf("replay %d changed persisted Recovery record %s", i, record.ID)
		}
		if noRunRevisionHistory(t, f.db, predecessor.ID) != history {
			t.Fatalf("replay %d changed canceled predecessor history", i)
		}
	}
}

func TestNoRunRevisionReviewExitsKeepOldCancellationReadOnly(t *testing.T) {
	for _, exit := range []string{"FURTHER_REVIEW_REQUIRED", "REJECT", "REVISE"} {
		t.Run(exit, func(t *testing.T) {
			ctx := context.Background()
			f, owner, predecessor := acceptedExecutionFixture(t, "-review-exit-"+exit)
			readyNoRunSuccessor(t, f, owner, predecessor, "cancel-before-review-exit")
			ctx = retryCallerCtx(t, f, ctx)
			if _, err := f.store.StopAndReviseNoRunTask(ctx, wp.StopAndReviseNoRunTask{ProjectID: owner.ProjectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: predecessor.TaskDefinitionRevisionID, ActorAuthority: f.session}); err != nil {
				t.Fatal(err)
			}
			started := f.start(t)
			raw := approvedResult(t)
			if exit == "FURTHER_REVIEW_REQUIRED" {
				raw = `{"version":1,"reviewDisposition":"` + exit + `","summary":"Further investigation is required"}`
			}
			f.complete(t, started, raw)
			if exit == "REJECT" || exit == "REVISE" {
				if _, err := f.store.Resolve(ctx, f.input(started, exit)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := NewTaskStore(f.db).Get(ctx, owner.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := task.PhaseReviewApprovalPending
			if exit == "REVISE" {
				wantPhase = task.PhaseReviewing
			}
			if before.Phase != wantPhase || before.CurrentExecutionID != predecessor.ID || before.CurrentDefinitionRevisionID != predecessor.TaskDefinitionRevisionID || exit == "REJECT" && before.Status != task.StatusCanceled {
				t.Fatalf("Review did not reach %s exit: %+v", exit, before)
			}
			history := noRunRevisionHistory(t, f.db, predecessor.ID)
			for i := 0; i < 2; i++ {
				if _, err = NewExecutionCancellationStore(f.db).ProjectCanceledAdmittedExecutionToSuccessorRequired(ctx, owner.ID, predecessor.ID); err != nil {
					t.Fatal(err)
				}
				if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				after, err := NewTaskStore(f.db).Get(ctx, owner.ID)
				if err != nil || !reflect.DeepEqual(before, after) || noRunRevisionHistory(t, f.db, predecessor.ID) != history {
					t.Fatalf("old cancellation changed %s exit: before=%+v after=%+v err=%v", exit, before, after, err)
				}
				if noRunCount(t, f, "execution_proposals") != 1 || noRunCount(t, f, "no_run_successor_effect_authorities") != 0 || noRunCount(t, f, "no_run_successor_command_receipts") != 0 {
					t.Fatal("Review exit or old cancellation replay created successor authority")
				}
			}
		})
	}
}
