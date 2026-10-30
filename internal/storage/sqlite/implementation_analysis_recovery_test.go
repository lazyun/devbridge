package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"github.com/lazyun/devbridge/internal/task"
)

func implementationAnalysisFixture(t *testing.T) (agentStorageFixture, context.Context, run.Run, run.Run) {
	t.Helper()
	f := newAgentStorageFixture(t, "implementation-analysis")
	ctx := aisession.WithCallerScope(context.Background(), "agent-fixture-implementation-analysis")
	f.task.VerificationSpec = task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "fixture", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}}, AcceptanceCriteria: []string{"tests pass"}, CodexInstruction: "Run fixture tests"}
	id := seedExecutionOwner(t, f.db, f.task, f.session, "analysis-execution")
	proposals, runs, conversations := NewExecutionProposalStore(f.db), NewRunStore(f.db), NewAgentConversationStore(f.db)
	_, impl, err := proposals.ConsumeAndCreateRun(ctx, id, "analysis-implementation", testNow)
	if err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, impl.ID, run.StatusPreparing, "", "")
	if _, err = conversations.BeginRun(ctx, impl.ID); err != nil {
		t.Fatal(err)
	}
	config, err := NewCodexConfigStore(f.db).Get(ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	if err = conversations.BindThreadWithRuntimeProvenance(ctx, impl.ID, "analysis-thread", f.task.Model, "analysis-fixture", config.Selection("analysis-fixture")); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, impl.ID, run.StatusRunning, "analysis-thread", "")
	if err = runs.PrepareNaturalResult(ctx, impl.ID); err != nil {
		t.Fatal(err)
	}
	if err = conversations.MarkTurnPending(ctx, impl.ID); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, impl.ID, run.StatusRunning, "analysis-thread", "analysis-turn")
	captured, _ := json.Marshal(run.ExecutionEvidence{Commands: []run.ObservedCommand{}})
	if err = runs.RecordExecutionCommands(ctx, impl.ID, captured); err != nil {
		t.Fatal(err)
	}
	if err = conversations.FinishRun(ctx, impl.ID, agentconversation.KnownAdvance, "analysis-turn", true); err != nil {
		t.Fatal(err)
	}
	impl, err = runs.Update(ctx, impl.ID, func(r *run.Run) error {
		r.Status, r.Result = run.StatusSucceeded, &run.Result{AgentClaim: evidence.AgentClaim{Message: "Gate inspection completed; no implementation changes were made."}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, verify, err := proposals.ReserveVerificationRun(ctx, id, "analysis-verification", testNow.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return f, ctx, impl, verify
}

func TestImplementationAnalysisWhileVerificationOwnsClaim(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		name := "verification-pending"
		if interrupted {
			name = "verification-interrupted"
		}
		t.Run(name, func(t *testing.T) {
			f, ctx, impl, verify := implementationAnalysisFixture(t)
			if interrupted {
				if _, err := NewRunStore(f.db).Update(ctx, verify.ID, func(r *run.Run) error {
					r.Status = run.StatusInfrastructureFailed
					r.Failure = &run.Failure{Code: "bridge_shutdown_interrupted", Message: "Interrupted by service restart"}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.sql.Exec(`UPDATE tasks SET phase='RECOVERY_REQUIRED' WHERE id=?`, impl.TaskID); err != nil {
					t.Fatal(err)
				}
				owner, err := NewTaskStore(f.db).Get(ctx, impl.TaskID)
				if err != nil || owner.Phase != task.PhaseRecoveryRequired {
					t.Fatalf("expected recovery fixture: %+v %v", owner, err)
				}
			}
			store := NewRunAnalysisStore(f.db)
			service := &runanalysis.Service{Store: store, Analyze: execution.AnalyzeRunReport}
			source, err := service.Get(ctx, f.session.ID, impl.ID)
			if err != nil || source.State != "OPTIONAL" || source.Kind != "EXECUTION" {
				t.Fatalf("source %+v %v", source, err)
			}
			in := runanalysis.Input{AISessionID: f.session.ID, RunID: impl.ID, RequestID: "implementation-summary", SourceHash: source.SourceHash, Report: []byte(`{"summary":"Only gate inspection completed; implementation remains unproven. Assess verification separately."}`)}
			tables := []string{"tasks", "execution_proposals", "runs", "feature_area_agent_claims", "agent_conversations", "agent_operation_boundaries", "execution_cancellations", "review_approval_decisions"}
			before := platformTableSnapshot(t, f.db, tables)
			receipt, err := service.Submit(ctx, in)
			if err != nil || receipt.Kind != "EXECUTION" {
				t.Fatalf("submit %+v %v", receipt, err)
			}
			replay, err := service.Submit(ctx, in)
			if err != nil || replay != receipt {
				t.Fatalf("replay %+v %v", replay, err)
			}
			if after := platformTableSnapshot(t, f.db, tables); before != after {
				t.Fatal("analysis changed workflow, execution evidence, or recovery authority")
			}
			saved, err := service.Get(ctx, f.session.ID, impl.ID)
			if err != nil || saved.State != "RECORDED" || saved.SourceHash != source.SourceHash || string(saved.Report) != string(in.Report) {
				t.Fatalf("recorded %+v %v", saved, err)
			}
			changed := in
			changed.RequestID = "different-summary"
			if _, err = service.Submit(ctx, changed); !errors.Is(err, runanalysis.ErrConflict) {
				t.Fatalf("conflicting analysis accepted: %v", err)
			}
		})
	}
}

func TestImplementationAnalysisStillRejectsStaleAndForeignTargets(t *testing.T) {
	for _, mutation := range []string{"source-hash", "writer-source-hash", "foreign-session", "foreign-claim", "inactive-task", "inactive-area"} {
		t.Run(mutation, func(t *testing.T) {
			f, ctx, impl, _ := implementationAnalysisFixture(t)
			store := NewRunAnalysisStore(f.db)
			service := &runanalysis.Service{Store: store, Analyze: execution.AnalyzeRunReport}
			source, err := service.Get(ctx, f.session.ID, impl.ID)
			if err != nil {
				t.Fatal(err)
			}
			in := runanalysis.Input{AISessionID: f.session.ID, RunID: impl.ID, RequestID: "rejected-summary", SourceHash: source.SourceHash, Report: []byte(`{"summary":"Analysis"}`)}
			want := runanalysis.ErrStale
			switch mutation {
			case "source-hash":
				in.SourceHash = strings.Repeat("0", 64)
			case "writer-source-hash":
				source.SourceHash = strings.Repeat("0", 64)
			case "foreign-session":
				ctx, want = aisession.WithCallerScope(ctx, "another-caller"), aisession.ErrScopeMismatch
			case "foreign-claim":
				_, err = f.db.sql.Exec(`UPDATE feature_area_agent_claims SET
 operation_kind='REVIEW',operation_id=(SELECT current_review_id FROM tasks WHERE id=?),
 run_id=(SELECT linked_run_id FROM reviews WHERE id=(SELECT current_review_id FROM tasks WHERE id=?))
 WHERE feature_area_id=?`, impl.TaskID, impl.TaskID, f.area.ID)
			case "inactive-task":
				_, err = f.db.sql.Exec(`UPDATE tasks SET status='CANCELED' WHERE id=?`, impl.TaskID)
			case "inactive-area":
				requestRetirement(t, f)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := platformTableSnapshot(t, f.db, []string{"tasks", "runs", "feature_area_agent_claims", "run_analyses", "events"})
			if mutation == "writer-source-hash" {
				_, err = store.Commit(ctx, in, source, runanalysis.Result{})
			} else {
				_, err = service.Submit(ctx, in)
			}
			if !errors.Is(err, want) {
				t.Fatalf("rejection=%v want=%v", err, want)
			}
			if before != platformTableSnapshot(t, f.db, []string{"tasks", "runs", "feature_area_agent_claims", "run_analyses", "events"}) {
				t.Fatal("rejection wrote state")
			}
		})
	}
}
