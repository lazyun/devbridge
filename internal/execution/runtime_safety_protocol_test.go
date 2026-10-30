package execution_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"github.com/lazyun/devbridge/internal/workspace"
	"github.com/lazyun/devbridge/internal/worktree"
)

// The fake App Server is a separate process speaking the real JSON-RPC wire.
// It checks the durable dispatch marker before accepting requests, then drops
// the accepted Turn's response. No paid backend, network, or elapsed-time unlock.
func TestRuntimeSafetyAppServerHelper(t *testing.T) {
	if os.Getenv("DEVBRIDGE_SAFETY_HELPER") != "1" {
		return
	}
	mode := os.Getenv("DEVBRIDGE_SAFETY_MODE")
	db, err := sql.Open("sqlite", filepath.Join(os.Getenv("DEVBRIDGE_SAFETY_ROOT"), "devbridge.db"))
	if err != nil {
		os.Exit(31)
	}
	defer db.Close()
	write := func(v any) {
		data, e := json.Marshal(v)
		if e != nil {
			os.Exit(32)
		}
		fmt.Fprintln(os.Stdout, string(data))
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 2<<20)
	for scanner.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Permissions string            `json:"permissions"`
				ThreadID    string            `json:"threadId"`
				Items       []json.RawMessage `json:"items"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(33)
		}
		switch req.Method {
		case "config/read":
			write(map[string]any{"id": req.ID, "result": map[string]any{"config": map[string]any{}}})
		case "mcpServerStatus/list":
			write(map[string]any{"id": req.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}})
		case "initialize":
			write(map[string]any{"id": req.ID, "result": map[string]any{"userAgent": "synthetic safety gate"}})
		case "initialized":
		case "thread/name/set":
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "thread/resume":
			os.Exit(34) // recovery must create a fresh Thread.
		case "thread/start":
			var marked bool
			e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN feature_area_agent_claims c ON c.run_id=b.run_id WHERE b.advance_state='UNKNOWN' AND b.thread_start_pending=1 AND b.quiesced=0)`).Scan(&marked)
			if e != nil || !marked {
				os.Exit(35)
			}
			result := map[string]any{"thread": map[string]any{"id": "thread-" + mode}}
			// This helper proves protocol/lifecycle fencing only. Native sandbox
			// enforcement has its own opt-in test with actual denied writes.
			if req.Params.Permissions != "" {
				result["activePermissionProfile"] = map[string]any{"id": req.Params.Permissions}
			}
			write(map[string]any{"id": req.ID, "result": result})
		case "thread/inject_items":
			if req.Params.ThreadID != "thread-"+mode || len(req.Params.Items) != 1 || !strings.Contains(string(req.Params.Items[0]), `"role":"developer"`) || !strings.Contains(string(req.Params.Items[0]), "supersedes earlier host operation roles") {
				os.Exit(39)
			}
			write(map[string]any{"id": req.ID, "result": map[string]any{}})
		case "turn/start":
			var marked bool
			e := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_operation_boundaries b JOIN feature_area_agent_claims c ON c.run_id=b.run_id JOIN runs r ON r.id=b.run_id WHERE b.advance_state='UNKNOWN' AND b.thread_start_pending=0 AND b.quiesced=0 AND r.status='RUNNING' AND r.thread_id=?)`, "thread-"+mode).Scan(&marked)
			if e != nil || !marked {
				os.Exit(36)
			}
			if mode == "lost" {
				if os.WriteFile(filepath.Join(os.Getenv("DEVBRIDGE_SAFETY_ROOT"), "turn-accepted"), []byte("thread-lost/turn-lost"), 0600) != nil {
					os.Exit(37)
				}
				os.Exit(0) // externally accepted, local response/TurnStarted lost.
			}
			write(map[string]any{"id": req.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-fresh", "status": "inProgress", "items": []any{}}}})
			write(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "thread-fresh", "turnId": "turn-fresh", "item": map[string]any{"id": "message-fresh", "type": "agentMessage", "text": `{"resultDisposition":"COMPLETED","summary":"synthetic completed result"}`, "phase": "final_answer"}}})
			write(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-fresh", "turn": map[string]any{"id": "turn-fresh", "status": "completed", "items": []any{}}}})
		default:
			os.Exit(38)
		}
	}
	os.Exit(0)
}

type safetyApprovals struct{}

func (safetyApprovals) Register(string, approval.Resolver, context.CancelFunc) (func(), error) {
	return func() {}, nil
}
func (safetyApprovals) Request(context.Context, string, codex.ApprovalRequest) (approval.Approval, error) {
	return approval.Approval{}, errors.New("unexpected synthetic approval")
}
func (safetyApprovals) Wait(context.Context, string) error { return nil }

type safetyConfig struct{ launcher string }

func (s safetyConfig) Get(context.Context, string) (codexconfig.Config, error) {
	return codexconfig.Config{ID: codexconfig.DefaultConfigID, Name: "Default Codex configuration", Enabled: true, CodexPath: s.launcher}, nil
}

func TestAcceptedTurnLostResponseRestartRecoveryFreshThreadAndLateCallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	t.Setenv("DEVBRIDGE_SAFETY_HELPER", "1")
	t.Setenv("DEVBRIDGE_SAFETY_MODE", "lost")
	t.Setenv("DEVBRIDGE_SAFETY_ROOT", root)
	launcher := filepath.Join(root, "synthetic-codex")
	executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestRuntimeSafetyAppServerHelper$\n"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	base := strings.Repeat("a", 40)
	if _, err = db.SQL().ExecContext(ctx, `UPDATE codex_configs SET codex_path=?,updated_at=? WHERE id=?`, launcher, now.Format(time.RFC3339Nano), codexconfig.DefaultConfigID); err != nil {
		t.Fatal(err)
	}
	if err = sqlite.NewProjectStore(db).Create(ctx, project.Project{ID: "p", Name: "p", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err = sqlite.NewRepositoryStore(db).PutIfAbsent(ctx, repository.Repository{ID: "r", ProjectID: "p", Name: "r", Path: filepath.Join(root, "source"), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = sqlite.NewModuleStore(db).Create(ctx, module.Module{ID: "m", ProjectID: "p", Key: "m", Name: "m", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	sessions := sqlite.NewAISessionStore(db)
	if _, err = sessions.InitializeSession(ctx, "runtime-safety", "initialize", "s", now); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = sessions.BindSessionProject(ctx, "runtime-safety", "bind-project", "s", "p", "selection", now); err != nil {
		t.Fatal(err)
	}
	if err = sqlite.NewFeatureAreaStore(db).Create(ctx, featurearea.FeatureArea{ID: "fa", ProjectID: "p", CreatedByAISessionID: "s", Title: "fa", ProblemStatement: "runtime safety", Status: featurearea.StatusActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"old", "next", "successor"} {
		v := task.Task{ID: id, ProjectID: "p", FeatureAreaID: "fa", RepositoryID: "r", ModuleID: "m", CodexConfigID: codexconfig.DefaultConfigID, Model: "gpt-5.6-terra", ReasoningEffort: "high", Goal: "g", WorkSpec: task.WorkSpec{Goal: "g", CodexInstruction: "g", ImplementationMode: task.ImplementationCodingAgent, ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "synthetic"}}, BaseCommit: base, Title: "Runtime intent", Status: task.StatusActive, Type: task.TypeDevelopment, Phase: task.PhaseReviewRequired, CreatedAt: now, UpdatedAt: now}
		if err = sqlite.NewTaskStore(db).Create(ctx, v); err != nil {
			t.Fatal(err)
		}
		workflowtest.Approve(t, db, v, "s", v.WorkSpec, v.VerificationSpec)
	}
	seq := 0
	coordinator := func() *exec.Coordinator {
		ids := func() (string, error) { seq++; return fmt.Sprintf("safety-%d", seq), nil }
		return &exec.Coordinator{Repositories: protocolRepositories{sqlite.NewRepositoryStore(db), base}, CodexConfigs: safetyConfig{launcher}, RuntimeConfigProvider: codexruntime.Default, Tasks: task.NewService(sqlite.NewTaskStore(db), nil, nil, nil, nil, nil), Runs: run.NewService(sqlite.NewRunStore(db), ids, nil), Worktrees: &worktree.DurableManager{External: protocolTree{filepath.Join(root, "wt")}, Store: sqlite.NewWorktreeStore(db)}, Workspaces: workspace.NewService(sqlite.NewWorkspaceStore(db), ids, nil), Codex: exec.DefaultCodexFactory{DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}, Evidence: protocolEvidence{}, Conversations: sqlite.NewAgentConversationStore(db), Approvals: safetyApprovals{}}
	}
	c := coordinator()
	old, err := startOwnedRuntimeExecution(t, ctx, c, db, "old", "s")
	if err == nil {
		t.Fatal("accepted Turn with lost response succeeded")
	}
	if marker, e := os.ReadFile(filepath.Join(root, "turn-accepted")); e != nil || string(marker) != "thread-lost/turn-lost" {
		t.Fatalf("backend never accepted Turn: %s %v (Run error %v)", marker, e, err)
	}
	oldStore := sqlite.NewAgentConversationStore(db)
	claim, err := oldStore.GetClaim(ctx, "fa")
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := oldStore.Boundary(ctx, old.ID)
	if err != nil || boundary.Advance != agentconversation.Unknown || boundary.ExitBoundaryTurnID != "" {
		t.Fatalf("boundary=%+v err=%v", boundary, err)
	}
	if _, err = oldStore.Reset(ctx, "fa", "cannot-bypass"); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("reset=%v", err)
	}
	oldEpoch := db.RuntimeEpoch()
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if db.RuntimeEpoch() == oldEpoch {
		t.Fatal("restart reused epoch")
	}
	if err = sqlite.NewRecoveryStore(db).RecoverAgentOperations(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	conv := sqlite.NewAgentConversationStore(db)
	recovered, err := conv.Recover(ctx, agentconversation.RecoverInput{FeatureAreaID: "fa", OperationKind: claim.Kind, OperationID: claim.OperationID, RunID: old.ID, AgentConversationID: boundary.AgentConversationID, RequestID: "recover-lost"})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := conv.Current(ctx, "fa")
	if err != nil || fresh.ID != recovered.NewConversationID || fresh.BackendThreadID != "" || fresh.CurrentBoundaryTurnID != "" {
		t.Fatalf("fresh=%+v err=%v", fresh, err)
	}
	isolated, err := sqlite.NewWorkspaceStore(db).Get(ctx, old.WorkspaceID)
	if err != nil || !isolated.RuntimeQuarantined {
		t.Fatalf("old workspace=%+v err=%v", isolated, err)
	}
	t.Setenv("DEVBRIDGE_SAFETY_MODE", "fresh")
	next, err := startOwnedRuntimeExecution(t, ctx, coordinator(), db, "next", "s")
	if err != nil || next.Status != run.StatusSucceeded || next.ThreadID != "thread-fresh" {
		t.Fatalf("fresh Run=%+v err=%v", next, err)
	}
	if next.WorkspaceID == "" || next.WorkspaceID == old.WorkspaceID || next.WorktreeID == old.WorktreeID {
		t.Fatalf("quarantined workspace reused: old=%+v new=%+v", old, next)
	}
	currentWorkspace, e := sqlite.NewWorkspaceStore(db).GetByFeatureArea(ctx, "fa")
	if e != nil || currentWorkspace.ID != next.WorkspaceID {
		t.Fatalf("replacement pointer=%+v %v", currentWorkspace, e)
	}
	if _, err = conv.GetClaim(ctx, "fa"); !errors.Is(err, agentconversation.ErrNotFound) {
		t.Fatalf("normal completion did not atomically release claim: %v", err)
	}
	nextBoundary, err := conv.Boundary(ctx, next.ID)
	if err != nil || !nextBoundary.Quiesced || nextBoundary.Advance != agentconversation.KnownAdvance || nextBoundary.ExitBoundaryTurnID != next.TurnID {
		t.Fatalf("final boundary=%+v err=%v", nextBoundary, err)
	}
	successorExecution := seedOwnedRuntimeExecution(t, db, "successor", "s", "successor-execution")
	proposals := sqlite.NewExecutionProposalStore(db)
	if successorExecution.Status != executionproposal.StatusAdmitted || successorExecution.RunID != "" {
		t.Fatalf("successor fixture must be admission-only: %+v", successorExecution)
	}
	_, successor, err := proposals.PrepareAdmittedExecution(ctx, successorExecution.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before, err := conv.Current(ctx, "fa")
	if err != nil {
		t.Fatal(err)
	}
	stale := runtimeepoch.WithFence(ctx, oldEpoch, old.ID)
	if _, err = run.NewService(sqlite.NewRunStore(db), nil, nil).AttachTurn(stale, old.ID, "turn-lost"); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late TurnStarted/Completed callback=%v", err)
	}
	if err = conv.FinishRun(stale, old.ID, agentconversation.KnownAdvance, "turn-lost", true); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late finalizer=%v", err)
	}
	if err = conv.Release(stale, claim); !errors.Is(err, runtimeepoch.ErrFenced) {
		t.Fatalf("late release=%v", err)
	}
	if _, err = conv.AuthorizeRun(ctx, old.ID); err == nil {
		t.Fatal("old Host Tool authorized")
	}
	after, err := conv.Current(ctx, "fa")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("late callback mutated successor: before=%+v after=%+v err=%v", before, after, err)
	}
	currentClaim, err := conv.GetClaim(ctx, "fa")
	if err != nil || currentClaim.RunID != successor.ID {
		t.Fatalf("successor claim=%+v err=%v", currentClaim, err)
	}
	historical, err := conv.Boundary(ctx, old.ID)
	if err != nil || historical.Advance != agentconversation.Unknown || historical.ExitBoundaryTurnID != "" {
		t.Fatalf("UNKNOWN history rewritten=%+v err=%v", historical, err)
	}
}
