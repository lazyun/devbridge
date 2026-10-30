package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type agentStorageFixture struct {
	db      *DB
	root    string
	project project.Project
	area    featurearea.FeatureArea
	task    task.Task
	session aisession.Session
}

func newAgentStorageFixture(t *testing.T, suffix string) agentStorageFixture {
	t.Helper()
	suffix = strings.ReplaceAll(suffix, " ", "-")
	root := t.TempDir()
	db := openTestDB(t, root)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	p := project.Project{ID: "agent-project-" + suffix, Name: "Agent project " + suffix, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := repository.Repository{ID: "agent-repo-" + suffix, ProjectID: p.ID, Name: "repo", Path: filepath.Join(root, "source"), CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "agent-module-" + suffix, ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "agent-session-" + suffix, ProjectID: p.ID, CreatedAt: testNow, UpdatedAt: testNow}
	sessions := NewAISessionStore(db)
	scope := "agent-fixture-" + suffix
	if _, err := sessions.InitializeSession(ctx, scope, "initialize-"+suffix, session.ID, testNow); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, scope, "bind-project-"+suffix, session.ID, p.ID, "selection-"+suffix, testNow); err != nil {
		t.Fatal(err)
	}
	area := featurearea.FeatureArea{ID: "agent-area-" + suffix, ProjectID: p.ID, CreatedByAISessionID: session.ID, Title: "Area", ProblemStatement: "Agent context tests", Status: featurearea.StatusActive, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: "goal", Constraints: []string{"bounded"}, CodexInstruction: "goal", ImplementationMode: task.ImplementationCodingAgent, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test waiver"}}
	taskValue := task.Task{ID: "agent-task-" + suffix, ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: r.ID, CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", Goal: work.Goal, Constraints: work.Constraints, WorkSpec: work, VerificationSpec: verification, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Title: "Agent task", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, taskValue); err != nil {
		t.Fatal(err)
	}
	return agentStorageFixture{db: db, root: root, project: p, area: area, task: taskValue, session: session}
}

func (f agentStorageFixture) addTask(t *testing.T, id string) task.Task {
	t.Helper()
	v := f.task
	v.ID = id
	v.CreatedAt = v.CreatedAt.Add(time.Second)
	v.UpdatedAt = v.CreatedAt
	if err := NewTaskStore(f.db).Create(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	return v
}

func (f agentStorageFixture) addAreaTask(t *testing.T, areaID, taskID string) agentStorageFixture {
	t.Helper()
	result := f
	result.area.ID = areaID
	result.area.Title = "Area " + areaID
	result.area.CreatedAt = result.area.CreatedAt.Add(time.Second)
	result.area.UpdatedAt = result.area.CreatedAt
	if err := NewFeatureAreaStore(f.db).Create(context.Background(), result.area); err != nil {
		t.Fatal(err)
	}
	result.task.ID = taskID
	result.task.FeatureAreaID = areaID
	result.task.CreatedAt = result.task.CreatedAt.Add(time.Second)
	result.task.UpdatedAt = result.task.CreatedAt
	if err := NewTaskStore(f.db).Create(context.Background(), result.task); err != nil {
		t.Fatal(err)
	}
	return result
}

type preparedAgentOperation struct {
	kind agentconversation.OperationKind
	id   string
	make func(context.Context, string) (run.Run, error)
}

func prepareAgentOperation(t *testing.T, f agentStorageFixture, kind agentconversation.OperationKind, label string) preparedAgentOperation {
	t.Helper()
	ctx := context.Background()
	store := NewRunStore(f.db)
	switch kind {
	case agentconversation.Review:
		// A separate semantic intent may Review while another Task is approved;
		// the FeatureArea operation claim still serializes their runtime work.
		current, err := NewTaskStore(f.db).Get(ctx, f.task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !current.Phase.ReviewStage() || current.CurrentReviewID != "" {
			f.task = f.addTask(t, "review-task-"+label)
		}
		id := "review_" + label
		config, err := NewCodexConfigStore(f.db).Get(ctx, f.task.CodexConfigID)
		if err != nil {
			t.Fatal(err)
		}
		selection := config.Selection("agent-fixture")
		value := review.Review{ReviewDepth: task.ReviewDepthFast, TaskID: f.task.ID, ID: id, ProjectID: f.project.ID, FeatureAreaID: f.area.ID, CreatedByAISessionID: f.session.ID, AuthorKind: string(review.AuthorAISession), AuthorID: f.session.ID, RepositoryID: f.task.RepositoryID, ExecutorKind: review.ExecutorCodingAgent, CodexConfigID: f.task.CodexConfigID, BackendSelection: &selection, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: f.task.Model, ReasoningEffort: f.task.ReasoningEffort}, Model: f.task.Model, ReasoningEffort: f.task.ReasoningEffort, BaseCommit: f.task.BaseCommit, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "goal", Instruction: "review", CreatedAt: testNow}
		if err := NewReviewStore(f.db).Create(ctx, value); err != nil {
			t.Fatalf("create current-protocol review fixture %q: %v", id, err)
		}
		return preparedAgentOperation{kind: kind, id: id, make: func(ctx context.Context, runID string) (run.Run, error) {
			return store.CreateProductScopeReviewAttempt(ctx, id, runID, "review", run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{}, testNow)
		}}
	case agentconversation.Execution:
		executionID := seedExecutionOwner(t, f.db, f.task, f.session, "execution-"+label)
		return preparedAgentOperation{kind: kind, id: executionID, make: func(ctx context.Context, runID string) (run.Run, error) {
			return store.CreateExecutionAttempt(ctx, f.task.ID, runID, run.PurposeImplementation, executionID, testNow)
		}}
	default:
		t.Fatalf("unsupported operation kind %q", kind)
		return preparedAgentOperation{}
	}
}

func setAgentRunState(t *testing.T, db *DB, runID string, status run.Status, threadID, turnID string) {
	t.Helper()
	if _, err := db.sql.Exec(`UPDATE runs SET status=?,thread_id=?,turn_id=?,updated_at=? WHERE id=?`, status, nullString(threadID), nullString(turnID), formatTime(testNow.Add(time.Minute)), runID); err != nil {
		t.Fatal(err)
	}
}

func TestAgentConversationFirstCurrentAndUniqueSQL(t *testing.T) {
	f := newAgentStorageFixture(t, "first")
	op := prepareAgentOperation(t, f, agentconversation.Execution, "first")
	created, err := op.make(context.Background(), "run-first")
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, created.ID, run.StatusPreparing, "", "")
	store := NewAgentConversationStore(f.db)
	conversation, err := store.BeginRun(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.FeatureAreaID != f.area.ID || conversation.ProjectID != f.project.ID || conversation.BackendThreadID != "" || !conversation.ContinuationSafe || conversation.HostToolContractVersion != agentconversation.HostToolContractVersion {
		t.Fatalf("unexpected first conversation: %+v", conversation)
	}
	current, err := store.Current(context.Background(), f.area.ID)
	if err != nil || current.ID != conversation.ID {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	_, err = f.db.sql.Exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,backend,host_tool_contract_version,created_at) VALUES('second-current',?,?, 'CODEX',?,?)`, f.project.ID, f.area.ID, agentconversation.HostToolContractVersion, formatTime(testNow))
	if err == nil || !errors.Is(conflict(err), ErrConflict) {
		t.Fatalf("second current insert err=%v", err)
	}
}

func TestAgentConversationResetLineageReplayAndBusyByKind(t *testing.T) {
	t.Run("lineage and replay", func(t *testing.T) {
		f := newAgentStorageFixture(t, "reset-lineage")
		store := NewAgentConversationStore(f.db)
		op := prepareAgentOperation(t, f, agentconversation.Review, "reset-lineage")
		runValue, err := op.make(context.Background(), "run-reset-lineage")
		if err != nil {
			t.Fatal(err)
		}
		setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
		original, err := store.BeginRun(context.Background(), runValue.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.NoAdvance, "", true); err != nil {
			t.Fatal(err)
		}
		if _, err := NewRunStore(f.db).Update(context.Background(), runValue.ID, func(v *run.Run) error { v.Status = run.StatusFailed; v.UpdatedAt = testNow.Add(time.Hour); return nil }); err != nil {
			t.Fatal(err)
		}
		first, err := store.Reset(context.Background(), f.area.ID, "reset-1")
		if err != nil {
			t.Fatal(err)
		}
		if first.PredecessorID != original.ID || first.RotationReason != "" {
			t.Fatalf("first reset result=%+v", first)
		}
		previous, err := store.Get(context.Background(), first.PredecessorID)
		if err != nil || previous.EndedAt == nil || previous.RotationReason != agentconversation.ExplicitReset {
			t.Fatalf("previous=%+v err=%v", previous, err)
		}
		replay, err := store.Reset(context.Background(), f.area.ID, "reset-1")
		if err != nil || replay.ID != first.ID {
			t.Fatalf("replay=%+v err=%v", replay, err)
		}
		second, err := store.Reset(context.Background(), f.area.ID, "reset-2")
		if err != nil || second.PredecessorID != first.ID || second.ID == first.ID {
			t.Fatalf("second=%+v err=%v", second, err)
		}
		oldReplay, err := store.Reset(context.Background(), f.area.ID, "reset-1")
		if err != nil || oldReplay.ID != first.ID {
			t.Fatalf("old replay=%+v err=%v", oldReplay, err)
		}
		history, err := store.ListByFeatureArea(context.Background(), f.area.ID)
		if err != nil || len(history) != 3 {
			t.Fatalf("history len=%d err=%v values=%+v", len(history), err, history)
		}
	})
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review, agentconversation.Execution} {
		t.Run(string(kind), func(t *testing.T) {
			f := newAgentStorageFixture(t, "reset-busy-"+string(kind))
			op := prepareAgentOperation(t, f, kind, "reset_busy_"+string(kind))
			if _, err := op.make(context.Background(), "run-reset-busy-"+string(kind)); err != nil {
				t.Fatal(err)
			}
			if _, err := NewAgentConversationStore(f.db).Reset(context.Background(), f.area.ID, "blocked"); !errors.Is(err, agentconversation.ErrBusy) {
				t.Fatalf("reset while %s active err=%v", kind, err)
			}
		})
	}
}

func TestAgentConversationBoundaryCanonicalStates(t *testing.T) {
	tests := []struct {
		name         string
		advance      agentconversation.Advance
		exit         string
		wantBoundary string
		wantSafe     bool
		markPending  bool
	}{
		{name: "no advance", advance: agentconversation.NoAdvance, wantSafe: true},
		{name: "known advance", advance: agentconversation.KnownAdvance, exit: "turn-known", wantBoundary: "turn-known", wantSafe: true, markPending: true},
		{name: "unknown", advance: agentconversation.Unknown, wantSafe: false, markPending: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newAgentStorageFixture(t, "boundary-"+tc.name)
			op := prepareAgentOperation(t, f, agentconversation.Execution, "boundary")
			runValue, err := op.make(context.Background(), "run-boundary")
			if err != nil {
				t.Fatal(err)
			}
			setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
			store := NewAgentConversationStore(f.db)
			conversation, err := store.BeginRun(context.Background(), runValue.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.markPending {
				if err := store.BindThread(context.Background(), runValue.ID, "thread-boundary"); err != nil {
					t.Fatal(err)
				}
				setAgentRunState(t, f.db, runValue.ID, run.StatusRunning, "thread-boundary", tc.exit)
				if err := store.MarkTurnPending(context.Background(), runValue.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.FinishRun(context.Background(), runValue.ID, tc.advance, tc.exit, true); err != nil {
				t.Fatal(err)
			}
			if _, err := NewRunStore(f.db).Update(context.Background(), runValue.ID, func(v *run.Run) error { v.Status = run.StatusFailed; v.UpdatedAt = testNow.Add(time.Hour); return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := store.Get(context.Background(), conversation.ID)
			if err != nil || got.CurrentBoundaryTurnID != tc.wantBoundary || got.ContinuationSafe != tc.wantSafe {
				t.Fatalf("conversation=%+v err=%v", got, err)
			}
			boundary, err := store.Boundary(context.Background(), runValue.ID)
			if err != nil || boundary.Advance != tc.advance || boundary.ExitBoundaryTurnID != tc.exit || boundary.Quiesced != (tc.advance != agentconversation.Unknown) {
				t.Fatalf("boundary=%+v err=%v", boundary, err)
			}
		})
	}
}

func TestFeatureAreaClaimSameAreaPairsAndDifferentAreas(t *testing.T) {
	pairs := [][2]agentconversation.OperationKind{
		{agentconversation.Review, agentconversation.Review},
		{agentconversation.Review, agentconversation.Execution},
		{agentconversation.Execution, agentconversation.Execution},
	}
	for i, pair := range pairs {
		t.Run(fmt.Sprintf("%s_vs_%s", pair[0], pair[1]), func(t *testing.T) {
			f := newAgentStorageFixture(t, fmt.Sprintf("pair-%d", i))
			left := prepareAgentOperation(t, f, pair[0], fmt.Sprintf("left_%d", i))
			rightFixture := f
			{
				rightFixture.task = f.addTask(t, fmt.Sprintf("agent-task-pair-%d-right", i))
			}
			right := prepareAgentOperation(t, rightFixture, pair[1], fmt.Sprintf("right_%d", i))
			start := make(chan struct{})
			errs := make(chan error, 2)
			var wg sync.WaitGroup
			for n, op := range []preparedAgentOperation{left, right} {
				wg.Add(1)
				go func(n int, op preparedAgentOperation) {
					defer wg.Done()
					<-start
					_, err := op.make(context.Background(), fmt.Sprintf("run-pair-%d-%d", i, n))
					errs <- err
				}(n, op)
			}
			close(start)
			wg.Wait()
			close(errs)
			successes, busy := 0, 0
			for err := range errs {
				if err == nil {
					successes++
				} else if errors.Is(err, agentconversation.ErrBusy) {
					busy++
				} else {
					t.Fatalf("unexpected allocation error: %v", err)
				}
			}
			if successes != 1 || busy != 1 {
				t.Fatalf("successes=%d busy=%d", successes, busy)
			}
			var runCount, claimCount int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id IN (?,?)`, fmt.Sprintf("run-pair-%d-0", i), fmt.Sprintf("run-pair-%d-1", i)).Scan(&runCount); err != nil {
				t.Fatal(err)
			}
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE feature_area_id=?`, f.area.ID).Scan(&claimCount); err != nil {
				t.Fatal(err)
			}
			if runCount != 1 || claimCount != 1 {
				t.Fatalf("atomic allocation runCount=%d claimCount=%d", runCount, claimCount)
			}
		})
	}

	t.Run("different feature areas", func(t *testing.T) {
		left := newAgentStorageFixture(t, "different-left")
		right := left.addAreaTask(t, "agent-area-different-right", "agent-task-different-right")
		ops := []preparedAgentOperation{
			prepareAgentOperation(t, left, agentconversation.Review, "different_left"),
			prepareAgentOperation(t, right, agentconversation.Execution, "different_right"),
		}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i, op := range ops {
			wg.Add(1)
			go func(i int, op preparedAgentOperation) {
				defer wg.Done()
				_, err := op.make(context.Background(), fmt.Sprintf("run-different-%d", i))
				errs <- err
			}(i, op)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	})
}

func TestFeatureAreaClaimPersistsAcrossReopenAndSameOwnerRetry(t *testing.T) {
	f := newAgentStorageFixture(t, "restart")
	op := prepareAgentOperation(t, f, agentconversation.Execution, "restart")
	runValue, err := op.make(context.Background(), "run-restart")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := NewAgentConversationStore(f.db).GetClaim(context.Background(), f.area.ID)
	if err != nil || claim.RunID != runValue.ID {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	tx, err := f.db.sql.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := acquireAgentRunTx(context.Background(), tx, runValue, testNow); err != nil {
		_ = tx.Rollback()
		t.Fatalf("same owner retry: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	root := f.root
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	claim, err = NewAgentConversationStore(reopened).GetClaim(context.Background(), f.area.ID)
	if err != nil || claim.RunID != runValue.ID || claim.Kind != agentconversation.Execution {
		t.Fatalf("reopened claim=%+v err=%v", claim, err)
	}
}

func TestFeatureAreaClaimWrongOwnerAuthorizationAndRelease(t *testing.T) {
	f := newAgentStorageFixture(t, "owner")
	op := prepareAgentOperation(t, f, agentconversation.Execution, "owner")
	runValue, err := op.make(context.Background(), "run-owner")
	if err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	claim, err := store.GetClaim(context.Background(), f.area.ID)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
	if _, err := store.BeginRun(context.Background(), runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.BindThread(context.Background(), runValue.ID, "thread-owner"); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusRunning, "thread-owner", "turn-owner")
	if err := store.MarkTurnPending(context.Background(), runValue.ID); err != nil {
		t.Fatal(err)
	}
	if authorized, err := store.AuthorizeRun(context.Background(), runValue.ID); err != nil || authorized != claim {
		t.Fatalf("exact owner authorization=%+v err=%v", authorized, err)
	}
	if _, err := f.db.sql.Exec(`UPDATE feature_area_agent_claims SET operation_id='wrong-operation' WHERE feature_area_id=?`, f.area.ID); err == nil {
		t.Fatal("SQL accepted a claim with a forged operation owner")
	}
	if authorized, err := store.AuthorizeRun(context.Background(), runValue.ID); err != nil || authorized != claim {
		t.Fatalf("rejected forged owner changed authorization: %+v %v", authorized, err)
	}
	wrongOwners := []agentconversation.Claim{
		{FeatureAreaID: claim.FeatureAreaID, Kind: agentconversation.Review, OperationID: claim.OperationID, RunID: claim.RunID, RuntimeEpoch: claim.RuntimeEpoch},
		{FeatureAreaID: claim.FeatureAreaID, Kind: claim.Kind, OperationID: "wrong-operation", RunID: claim.RunID, RuntimeEpoch: claim.RuntimeEpoch},
		{FeatureAreaID: claim.FeatureAreaID, Kind: claim.Kind, OperationID: claim.OperationID, RunID: "wrong-run", RuntimeEpoch: claim.RuntimeEpoch},
	}
	for _, owner := range wrongOwners {
		if err := store.Release(context.Background(), owner); !errors.Is(err, agentconversation.ErrConflict) && !errors.Is(err, runtimeepoch.ErrFenced) {
			t.Fatalf("wrong owner %+v release err=%v", owner, err)
		}
	}
	if err := store.Release(context.Background(), claim); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("active exact owner release err=%v", err)
	}
	if persisted, err := store.GetClaim(context.Background(), f.area.ID); err != nil || persisted != claim {
		t.Fatalf("claim after rejected release=%+v err=%v", persisted, err)
	}
	if err := store.FinishRun(context.Background(), runValue.ID, agentconversation.Unknown, "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunStore(f.db).Update(context.Background(), runValue.ID, func(v *run.Run) error { v.Status = run.StatusFailed; v.UpdatedAt = testNow.Add(time.Hour); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Release(context.Background(), claim); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("UNKNOWN cannot be released even when quiesced: %v", err)
	}
	if current, err := store.GetClaim(context.Background(), f.area.ID); err != nil || current != claim {
		t.Fatalf("retained exact UNKNOWN claim=%+v err=%v", current, err)
	}
}

type terminalBeforeGoalPersistenceStore struct {
	*TaskStore
	beforeUpdate func()
	called       bool
}

func (s *terminalBeforeGoalPersistenceStore) UpdateGoalProgress(ctx context.Context, executionID, goalID string, status taskgoal.Status, now time.Time) error {
	s.called = true
	s.beforeUpdate()
	return s.TaskStore.UpdateGoalProgress(ctx, executionID, goalID, status, now)
}

func TestSameStatusGoalReportReauthorizesAtPersistenceBoundary(t *testing.T) {
	f := newAgentStorageFixture(t, "goal-same-status-authorization")
	ctx := context.Background()
	tasks := NewTaskStore(f.db)
	if err := tasks.DefineGoals(ctx, f.task.ID, []task.Goal{{ID: "goal_same_status", TaskID: f.task.ID, Position: 0, Title: "same status authorization", CreatedAt: testNow}}); err != nil {
		t.Fatal(err)
	}
	executionID := seedExecutionOwner(t, f.db, f.task, f.session, "execution-goal-same-status")
	proposals := NewExecutionProposalStore(f.db)
	proposal, err := proposals.Get(ctx, executionID)
	if err != nil {
		t.Fatal(err)
	}
	_, runValue, err := proposals.ConsumeAndCreateRun(ctx, proposal.ID, "run-goal-same-status", testNow.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusPreparing, "", "")
	conversations := NewAgentConversationStore(f.db)
	if _, err := conversations.BeginRun(ctx, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := conversations.BindThread(ctx, runValue.ID, "thread-goal-same-status"); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, runValue.ID, run.StatusRunning, "thread-goal-same-status", "turn-goal-same-status")
	if err := conversations.MarkTurnPending(ctx, runValue.ID); err != nil {
		t.Fatal(err)
	}
	if err := tasks.InitializeGoalProgress(ctx, proposal.ID, f.task.ID, f.project.ID, testNow.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	runContext := runtimeepoch.WithFence(ctx, runValue.RuntimeEpoch, runValue.ID)
	if err := tasks.UpdateGoalProgress(runContext, proposal.ID, "goal_same_status", taskgoal.StatusRunning, testNow.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	before, err := tasks.GoalProgress(ctx, proposal.ID)
	if err != nil || len(before) != 1 || before[0].Status != taskgoal.StatusRunning {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	if _, err := conversations.AuthorizeRun(ctx, runValue.ID); err != nil {
		t.Fatalf("precondition authorization: %v", err)
	}
	guarded := &terminalBeforeGoalPersistenceStore{TaskStore: tasks}
	guarded.beforeUpdate = func() {
		if err := conversations.FinishRun(ctx, runValue.ID, agentconversation.KnownAdvance, "turn-goal-same-status", true); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.ExecContext(ctx, `UPDATE runs SET status='FAILED',updated_at=? WHERE id=?`, formatTime(testNow.Add(5*time.Second)), runValue.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.sql.ExecContext(ctx, `UPDATE execution_proposals SET outcome='FAILED',failure_code='terminal',failure_message='concurrent terminal completion',updated_at=? WHERE id=? AND outcome='RUNNING'`, formatTime(testNow.Add(6*time.Second)), proposal.ID); err != nil {
			t.Fatal(err)
		}
	}
	service := taskgoal.NewService(guarded, &taskgoal.ExecutionAdapter{Proposals: proposals}, tasks, func() time.Time { return testNow.Add(7 * time.Second) })
	if _, err := service.Report(ctx, proposal.ID, "goal_same_status", taskgoal.StatusRunning); !errors.Is(err, agentconversation.ErrConflict) {
		t.Fatalf("same-status report after concurrent release err=%v", err)
	}
	if !guarded.called {
		t.Fatal("same-status report bypassed persistence authorization")
	}
	after, err := tasks.GoalProgress(ctx, proposal.ID)
	if err != nil || len(after) != 1 || after[0].Status != before[0].Status || !after[0].UpdatedAt.Equal(before[0].UpdatedAt) {
		t.Fatalf("after=%+v before=%+v err=%v", after, before, err)
	}
}

func TestMigrationV26DoesNotGuessLineageAndAllowsWorkspaceThreadHistoryReuse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	migrations := historicalMigrations()
	if err := ApplyMigrations(context.Background(), raw, migrations[:25]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO projects(id,name,created_at,updated_at) VALUES('legacy-project','Legacy',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('legacy-repo','legacy-project','Repo','/legacy/repo',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('legacy-module','legacy-project','core','Core',?,?)`, []any{stamp, stamp}},
		{`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at) VALUES('legacy-task-1','legacy-project','legacy-module','legacy-repo',?,'Goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?)`, []any{codexconfig.DefaultConfigID, stamp, stamp}},
		{`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at) VALUES('legacy-task-2','legacy-project','legacy-module','legacy-repo',?,'Goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?)`, []any{codexconfig.DefaultConfigID, stamp, stamp}},
		{`INSERT INTO workspaces(id,task_id,thread_id,status,token_baseline_available,created_at,updated_at) VALUES('legacy-workspace-1','legacy-task-1','legacy-thread','READY',1,?,?)`, []any{stamp, stamp}},
	}
	for _, statement := range statements {
		if _, err := raw.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed v25: %v", err)
		}
	}
	if err := ApplyMigrations(context.Background(), raw, migrations[:26]); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO workspaces(id,task_id,thread_id,status,token_baseline_available,created_at,updated_at) VALUES('legacy-workspace-2','legacy-task-2','legacy-thread','READY',1,?,?)`, stamp, stamp); err != nil {
		t.Fatalf("same historical thread across Task workspaces: %v", err)
	}
	if err := ApplyMigrations(context.Background(), raw, migrations); err == nil || !strings.Contains(err.Error(), "recreate") {
		t.Fatalf("old workspace ownership accepted: %v", err)
	}
	var count int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE legacy_thread_id='legacy-thread'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("source ownership altered after rejection: %d %v", count, err)
	}
}

func TestAgentContextResetFreshDiscoveringAreaWithoutTask(t *testing.T) {
	f := newAgentStorageFixture(t, "discovering-reset")
	ctx := context.Background()
	area := f.area
	area.ID = "discovering-reset-area"
	area.Status = featurearea.StatusDiscovering
	if err := NewFeatureAreaStore(f.db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	store := NewAgentConversationStore(f.db)
	first, err := store.Reset(ctx, area.ID, "reset-first")
	if err != nil {
		t.Fatal(err)
	}
	if first.FeatureAreaID != area.ID || first.PredecessorID == "" || first.BackendThreadID != "" {
		t.Fatalf("reset=%+v", first)
	}
	replay, err := store.Reset(ctx, area.ID, "reset-first")
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	var count int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM tasks WHERE feature_area_id=?`, area.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reset created Task: count=%d err=%v", count, err)
	}
}
