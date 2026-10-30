package execution_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/evidence"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

type protocolTree struct{ root string }

func (p protocolTree) Create(_ context.Context, in worktree.CreateInput) (worktree.Worktree, error) {
	path := filepath.Join(p.root, in.WorkspaceID)
	if err := os.MkdirAll(path, 0700); err != nil {
		return worktree.Worktree{}, err
	}
	return worktree.Worktree{ID: "wt_" + in.WorkspaceID, RepositoryID: in.RepositoryID, RunID: in.RunID, Path: path, Branch: "test/" + in.WorkspaceID, BaseCommit: in.BaseCommit, Status: worktree.StatusReady}, nil
}

// Physical Git validation is covered by real manager integration tests.
func (protocolTree) Validate(context.Context, worktree.Worktree) error { return nil }

type protocolEvidence struct{}

func (protocolEvidence) CaptureSnapshot(context.Context, string) (string, error) {
	return "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", nil
}
func (protocolEvidence) CollectRun(_ context.Context, _ string, base, start, end string) (evidence.GitEvidence, error) {
	return evidence.GitEvidence{Dirty: true, Diff: "diff", BaseCommit: base, HeadCommit: base, StartSnapshot: start, EndSnapshot: end}, nil
}

type protocolSession struct {
	starts, resumes, turns int
	lastResume             string
	cwds                   []string
	prompts                []string
	schema                 json.RawMessage
}

func (s *protocolSession) StartThread(_ context.Context, cwd string) (string, error) {
	s.cwds = append(s.cwds, cwd)
	s.starts++
	return fmt.Sprintf("thread-%d", s.starts), nil
}
func (s *protocolSession) ResumeThread(_ context.Context, id, cwd string, _ *run.TokenCounts) (string, error) {
	s.resumes++
	s.cwds = append(s.cwds, cwd)
	s.lastResume = id
	return id, nil
}
func (s *protocolSession) StartTurn(_ context.Context, _, prompt string) (exec.CodexTurnResult, error) {
	s.turns++
	s.prompts = append(s.prompts, prompt)
	return exec.CodexTurnResult{TurnID: fmt.Sprintf("turn-%d", s.turns), Message: "done"}, nil
}
func (s *protocolSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (exec.CodexTurnResult, error) {
	s.schema = append(json.RawMessage(nil), schema...)
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == exec.ImplementationReportJSONSchema {
		result.Message = `{"resultDisposition":"COMPLETED","summary":"done"}`
	}
	return result, err
}
func (*protocolSession) Close() error { return nil }

type protocolFactory struct{ s *protocolSession }

func (f protocolFactory) Start(context.Context, codexconfig.Config) (exec.CodexSession, error) {
	return f.s, nil
}

type protocolRepositories struct {
	*sqlite.RepositoryStore
	base string
}

func (p protocolRepositories) CurrentHEAD(context.Context, string) (string, error) {
	return p.base, nil
}

func TestFeatureAreaConversationCoordinatorProtocol(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	p := project.Project{ID: "p", Name: "p", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	repo := repository.Repository{ID: "repo", ProjectID: "p", Name: "r", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}
	if _, _, err := sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repo); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "m", ProjectID: "p", Key: "m", Name: "m", CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Use the production Session-first protocol.  Direct fixture creation is
	// deliberately closed and must keep returning SESSION_INITIALIZATION_REQUIRED.
	s := aisession.Session{ID: "s", ProjectID: "p", CreatedAt: now, UpdatedAt: now}
	sessions := sqlite.NewAISessionStore(db)
	if _, err := sessions.InitializeSession(ctx, "execution-conversation-fixture", "initialize-conversation", s.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.BindSessionProject(ctx, "execution-conversation-fixture", "bind-conversation", s.ID, p.ID, "selection-conversation", now); err != nil {
		t.Fatal(err)
	}
	fa := featurearea.FeatureArea{ID: "fa", ProjectID: "p", CreatedByAISessionID: "s", Title: "fa", ProblemStatement: "x", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err := sqlite.NewFeatureAreaStore(db).Create(ctx, fa); err != nil {
		t.Fatal(err)
	}
	base := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	add := func(id string, planning ...bool) {
		v := task.Task{ID: id, ProjectID: "p", FeatureAreaID: "fa", ModuleID: "m", RepositoryID: "repo", CodexConfigID: codexconfig.DefaultConfigID, Goal: "g", WorkSpec: task.WorkSpec{Goal: "g", CodexInstruction: "g", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "test"}}, BaseCommit: base, Title: "Runtime intent", Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
		if len(planning) > 0 {
			v.Status = task.StatusActive
			v.ProblemStatement = "Inspect implementation boundary"
			v.Goal = ""
			v.WorkSpec = task.WorkSpec{}
			v.VerificationSpec = task.VerificationSpec{}
		}
		if err := sqlite.NewTaskStore(db).Create(ctx, v); err != nil {
			t.Fatal(err)
		}
		if len(planning) == 0 {
			workflowtest.Approve(t, db, v, "s", v.WorkSpec, v.VerificationSpec)
		}
	}
	add("reviewed", true)
	add("a")
	add("b")
	add("c")
	seq := 0
	runs := run.NewService(sqlite.NewRunStore(db), func() (string, error) { seq++; return fmt.Sprintf("run-%d", seq), nil }, func() time.Time { return now })
	tasks := task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, nil, func() time.Time { return now })
	wsSeq := 0
	ws := workspace.NewService(sqlite.NewWorkspaceStore(db), func() (string, error) { wsSeq++; return fmt.Sprintf("ws-%d", wsSeq), nil }, func() time.Time { return now })
	ses := &protocolSession{}
	conv := sqlite.NewAgentConversationStore(db)
	c := &exec.Coordinator{Repositories: protocolRepositories{sqlite.NewRepositoryStore(db), base}, CodexConfigs: sqlite.NewCodexConfigStore(db), RuntimeConfigProvider: codexruntime.Default, Tasks: tasks, Runs: runs, Worktrees: &worktree.DurableManager{External: protocolTree{filepath.Join(root, "wt")}, Store: sqlite.NewWorktreeStore(db)}, Workspaces: ws, Codex: protocolFactory{ses}, Evidence: protocolEvidence{}, Conversations: conv}
	reviews, err := review.NewService(sqlite.NewReviewStore(db), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Reviews = reviews
	config, err := sqlite.NewCodexConfigStore(db).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("execution-conversation-fixture")
	productReview, err := reviews.Create(ctx, review.Review{ReviewDepth: "DEEP", TaskID: "reviewed", ProjectID: "p", FeatureAreaID: "fa", CreatedByAISessionID: "s", RepositoryID: "repo", CodexConfigID: codexconfig.DefaultConfigID, BackendSelection: &selection, RuntimeConfig: codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}, BaseCommit: base, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: "Inspect the capability", Instruction: " \t检查 e\u0301 <&$`>\r\n" + strings.Repeat("界", 21000) + "\n "})
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := runs.CreateFocusedScopeReviewForReview(ctx, productReview.ID, productReview.Instruction, run.ReviewScope{Kind: run.ReviewScopeRepository}, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}})
	if err != nil {
		t.Fatal(err)
	}
	preparedReview, err := c.PrepareScopeReview(ctx, reserved.ID)
	if err != nil {
		t.Fatal(err)
	}
	reviewRun, err := c.ExecuteRun(ctx, preparedReview, nil)
	if err != nil {
		t.Fatal(err)
	}
	storedReview, err := reviews.Get(ctx, productReview.ID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(productReview.Instruction))
	if storedReview.Instruction != productReview.Instruction || storedReview.FinalCodexPrompt != storedReview.Instruction || reviewRun.FinalCodexPrompt != storedReview.Instruction || ses.prompts[0] != storedReview.Instruction || storedReview.PromptSHA256 != hex.EncodeToString(sum[:]) || reviewRun.PromptSHA256 != storedReview.PromptSHA256 {
		t.Fatal("durable Review prompt byte identity failed")
	}
	if ses.starts != 1 || ses.resumes != 0 || reviewRun.ThreadID != "thread-1" {
		t.Fatalf("first Review did not start canonical Thread: %+v", reviewRun)
	}
	first, err := startOwnedRuntimeExecution(t, ctx, c, db, "a", "s")
	if err != nil {
		t.Fatal(err)
	}
	if ses.starts != 1 || first.ThreadID != "thread-1" {
		t.Fatalf("first=%+v starts=%d", first, ses.starts)
	}
	second, err := startOwnedRuntimeExecution(t, ctx, c, db, "b", "s")
	if err != nil {
		t.Fatal(err)
	}
	if ses.resumes != 2 || ses.lastResume != "thread-1" || second.ThreadID != "thread-1" {
		t.Fatalf("second=%+v resumes=%d id=%q", second, ses.resumes, ses.lastResume)
	}
	var conversationID, previousTurn string
	for _, operation := range []run.Run{reviewRun, first, second} {
		boundary, err := conv.Boundary(ctx, operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if conversationID == "" {
			conversationID = boundary.AgentConversationID
		}
		if boundary.AgentConversationID != conversationID || boundary.EntryBoundaryTurnID != previousTurn || boundary.ExitBoundaryTurnID != operation.TurnID || boundary.Advance != agentconversation.KnownAdvance || !boundary.Quiesced {
			t.Fatalf("operation boundary=%+v previous=%q", boundary, previousTurn)
		}
		previousTurn = operation.TurnID
		workspace, err := ws.Get(ctx, operation.WorkspaceID)
		if err != nil || workspace.LegacyThreadID != "" {
			t.Fatalf("workspace secretly owns Thread: %+v err=%v", workspace, err)
		}
	}
	for i, cwd := range ses.cwds {
		ses.cwds[i], err = filepath.EvalSymlinks(cwd)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(ses.cwds) != 3 || ses.cwds[0] != ses.cwds[1] || ses.cwds[1] != ses.cwds[2] {
		t.Fatalf("unexpected cwd reuse/rebinding: %v", ses.cwds)
	}
	old, err := conv.Current(ctx, "fa")
	if err != nil {
		t.Fatal(err)
	}
	if old.BackendThreadID != "thread-1" {
		t.Fatal(old)
	}
	beforeReset, err := conv.Boundary(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeReview, err := reviews.Get(ctx, productReview.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeTask, err := tasks.Get(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	beforeRun, err := runs.Get(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Reset(ctx, "fa", "reset"); err != nil {
		t.Fatal(err)
	}
	afterReview, reviewErr := reviews.Get(ctx, productReview.ID)
	afterTask, taskErr := tasks.Get(ctx, "b")
	afterRun, runErr := runs.Get(ctx, second.ID)
	if reviewErr != nil || taskErr != nil || runErr != nil ||
		!reflect.DeepEqual(beforeReview, afterReview) ||
		!reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRun, afterRun) {
		t.Fatal("reset modified historical product operations")
	}
	third, err := startOwnedRuntimeExecution(t, ctx, c, db, "c", "s")
	if err != nil {
		t.Fatal(err)
	}
	if ses.starts != 2 || third.ThreadID != "thread-2" {
		t.Fatalf("third=%+v starts=%d", third, ses.starts)
	}
	thirdTask, err := tasks.Get(ctx, "c")
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := exec.BuildRunPrompt(thirdTask, ses.cwds[len(ses.cwds)-1], third.ID, repo.ID, thirdTask.WorkSpec.ExecutionScope, exec.Instruction{CodexInstruction: thirdTask.WorkSpec.CodexInstruction})
	if ses.prompts[len(ses.prompts)-1] != wantPrompt {
		t.Fatal("reset injected context beyond the next Task's ordinary operation prompt")
	}
	afterReset, err := conv.Boundary(ctx, second.ID)
	if err != nil || !reflect.DeepEqual(beforeReset, afterReset) {
		t.Fatalf("reset rewrote old operation: %+v err=%v", afterReset, err)
	}
	newBoundary, err := conv.Boundary(ctx, third.ID)
	if err != nil || newBoundary.AgentConversationID == conversationID || newBoundary.EntryBoundaryTurnID != "" {
		t.Fatalf("reset successor inherited context: %+v err=%v", newBoundary, err)
	}
	history, err := conv.ListByFeatureArea(ctx, "fa")
	if err != nil || len(history) != 2 || history[0].EndedAt == nil {
		t.Fatalf("history=%+v err=%v", history, err)
	}
}
