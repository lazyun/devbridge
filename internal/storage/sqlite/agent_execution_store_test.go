package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestAgentExecutionAdmissionAndExactValidation(t *testing.T) {
	for _, kind := range []agentconversation.OperationKind{agentconversation.Review, agentconversation.Execution} {
		t.Run(string(kind), func(t *testing.T) {
			f := newAgentStorageFixture(t, "execution-identity-"+string(kind))
			ctx := context.Background()
			op := prepareAgentOperation(t, f, kind, "identity-"+string(kind))
			r, err := op.make(ctx, "run-identity-"+string(kind))
			if err != nil {
				t.Fatal(err)
			}
			e, err := NewAgentExecutionStore(f.db).Get(ctx, r.AgentExecutionID)
			if err != nil {
				t.Fatal(err)
			}
			if e.TaskID != r.TaskID || e.ProjectID != r.ProjectID || e.FeatureAreaID != r.FeatureAreaID || e.SemanticOwnerID != r.OperationID || e.SemanticOwnerKind != string(r.OperationKind) || e.ProvenanceKind != agentexecution.ProvenanceNative {
				t.Fatalf("execution=%+v run=%+v", e, r)
			}
			if kind == agentconversation.Execution && (e.ID != r.ExecutionProposalID || e.ExecutionProposalID != e.ID) {
				t.Fatalf("development identity=%+v", e)
			}
			taskValue, err := NewTaskStore(f.db).Get(ctx, r.TaskID)
			if err != nil || taskValue.Work.ExecutionID != e.ID {
				t.Fatalf("task work=%+v err=%v", taskValue.Work, err)
			}

			for _, id := range []any{nil, "missing-execution"} {
				_, err := f.db.sql.Exec(`INSERT INTO runs(id,task_id,project_id,feature_area_id,operation_kind,operation_id,review_id,execution_proposal_id,purpose,attempt,status,runtime_epoch,agent_execution_id,created_at,updated_at) SELECT 'bypass',task_id,project_id,feature_area_id,operation_kind,operation_id,review_id,execution_proposal_id,purpose,attempt+100,'CREATED',runtime_epoch,?,created_at,updated_at FROM runs WHERE id=?`, id, r.ID)
				if err == nil || !strings.Contains(err.Error(), "Run requires exact Agent Execution association") {
					t.Fatalf("SQL bypass guard=%v", err)
				}
			}
			store := NewRunStore(f.db)
			if err = store.ValidateRunExecution(ctx, r); err != nil {
				t.Fatal(err)
			}
			if err = store.ValidateRunExecutionDispatch(ctx, r); err == nil {
				t.Fatal("missing boundary dispatch accepted")
			}
			setAgentRunState(t, f.db, r.ID, run.StatusPreparing, "", "")
			if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			if err = store.ValidateRunExecutionDispatch(ctx, r); err != nil {
				t.Fatal(err)
			}
			for _, mutate := range []func(*run.Run){func(v *run.Run) { v.AgentExecutionID = "" }, func(v *run.Run) { v.AgentExecutionID = "wrong" }, func(v *run.Run) { v.TaskID = "wrong" }, func(v *run.Run) { v.ProjectID = "wrong" }, func(v *run.Run) { v.FeatureAreaID = "wrong" }, func(v *run.Run) { v.OperationID = "wrong" }, func(v *run.Run) { v.RuntimeEpoch = "wrong" }} {
				bad := r
				mutate(&bad)
				if err = store.ValidateRunExecution(ctx, bad); err == nil {
					t.Fatalf("invalid association accepted: %+v", bad)
				}
			}
			if _, err = f.db.sql.Exec(`UPDATE runs SET agent_execution_id=NULL WHERE id=?`, r.ID); err == nil {
				t.Fatal("mutable association")
			}
			if _, err = f.db.sql.Exec(`UPDATE agent_executions SET task_id='wrong' WHERE id=?`, e.ID); err == nil {
				t.Fatal("mutable execution")
			}
			if _, err = store.Update(ctx, r.ID, func(v *run.Run) error { v.AgentExecutionID = "wrong"; return nil }); !errors.Is(err, run.ErrOwnerImmutable) {
				t.Fatalf("owner guard=%v", err)
			}
		})
	}
}
func TestAgentExecutionInspectionAdmission(t *testing.T) {
	f := newAgentStorageFixture(t, "inspection-envelope")
	ctx := context.Background()
	store := NewContextProbeStore(f.db)
	p := contextprobe.Probe{ID: "inspection-envelope", AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := store.Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	p, err := store.Update(ctx, p.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusRunning
		v.StartedAt = &testNow
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewRunStore(f.db).Get(ctx, p.RunID)
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewAgentExecutionStore(f.db).Get(ctx, r.AgentExecutionID)
	if err != nil || e.Kind != agentexecution.KindInspection || e.TaskID != p.TaskID || e.FeatureAreaID != "" {
		t.Fatalf("execution=%+v %v", e, err)
	}
	if err = store.ValidateInspectionExecution(runtimeepoch.WithFence(ctx, p.RuntimeEpoch, p.RunID), p); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM feature_area_agent_claims WHERE run_id=?`, r.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("inspection claim=%d %v", n, err)
	}
	p.ThreadID = "wrong"
	if err = store.ValidateInspectionExecution(runtimeepoch.WithFence(ctx, p.RuntimeEpoch, p.RunID), p); err == nil {
		t.Fatal("wrong thread accepted")
	}
}
func TestAgentExecutionAdmissionRollback(t *testing.T) {
	f := newAgentStorageFixture(t, "execution-rollback")
	ctx := context.Background()
	first := prepareAgentOperation(t, f, agentconversation.Review, "rollback-first")
	second := prepareAgentOperation(t, f, agentconversation.Review, "rollback-second")
	if _, err := first.make(ctx, "rollback-first"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.make(ctx, "rollback-second"); !errors.Is(err, agentconversation.ErrBusy) {
		t.Fatalf("expected claim race rejection: %v", err)
	}
	var count int
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM agent_executions WHERE semantic_owner_id=?`, second.id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back execution count=%d %v", count, err)
	}
	if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM runs WHERE id='rollback-second'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rolled back run count=%d %v", count, err)
	}
}

// Build historical rows under the actual v35 rules, then test the actual
// current v41 -> v42 boundary (including explicit unprovable provenance).
func TestAgentExecutionV41UpgradeAndLegacyReadOnly(t *testing.T) {
	ctx := context.Background()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "upgrade.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:35]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	for _, q := range []string{`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Migration',?,?)`, `INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repo','/fixture-only',?,?)`, `INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`, `INSERT INTO feature_areas(id,project_id,title,problem_statement,status,created_at,updated_at) VALUES('fa','p','Area','Problem','DISCOVERING',?,?)`} {
		if _, err = raw.Exec(q, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"review_known", "review_unknown"} {
		if _, err = raw.Exec(`INSERT INTO reviews(id,project_id,feature_area_id,created_by_ai_session_id,repository_id,codex_config_id,base_commit,focus_kind,focus_summary,focus_paths_json,engineering_goal,instruction,created_at) VALUES(?,'p','fa','session','repo','codex_config_default','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','REPOSITORY','History','{"version":1,"paths":[]}','Review','Inspect',?)`, id, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = raw.Exec(`INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,review_id,attempt,status,purpose,result_text,created_at,updated_at) VALUES(?,'p','fa','REVIEW',?,?,1,'SUCCEEDED','SCOPE_REVIEW','Original result',?,?)`, "run-"+id, id, id, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err = raw.Exec(`UPDATE reviews SET linked_run_id=? WHERE id=?`, "run-"+id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err = ApplyMigrations(ctx, raw, historicalMigrations()[:41]); err != nil {
		t.Fatal(err)
	}
	// Deliberately damaged historical fact; the migration must not invent its owner.
	var guard string
	if err = raw.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='runs_operation_owner_immutable'`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`DROP TRIGGER runs_operation_owner_immutable; UPDATE runs SET operation_id='unprovable-owner' WHERE id='run-review_unknown';` + guard); err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
		t.Fatal(err)
	}
	store := NewAgentExecutionStore(&DB{sql: raw})
	known, err := store.Get(ctx, "review:review_known")
	if err != nil || known.ProvenanceKind != agentexecution.ProvenanceBackfilled || known.SemanticOwnerID != "review_known" {
		t.Fatalf("known=%+v %v", known, err)
	}
	unknown, err := store.Get(ctx, "legacy-run:run-review_unknown")
	if err != nil || unknown.ProvenanceKind != agentexecution.ProvenanceLegacyUnknown || unknown.SemanticOwnerID != "" || unknown.SemanticOwnerKind != "" {
		t.Fatalf("unknown=%+v %v", unknown, err)
	}
	var id string
	if err = raw.QueryRow(`SELECT agent_execution_id FROM runs WHERE id='run-review_unknown'`).Scan(&id); err != nil || id != unknown.ID {
		t.Fatalf("association=%s %v", id, err)
	}
	if _, err = raw.Exec(`UPDATE runs SET updated_at=updated_at WHERE id='run-review_unknown'`); err == nil {
		t.Fatal("unknown history mutable")
	}
	if _, err = raw.Exec(`UPDATE agent_executions SET provenance_kind='NATIVE' WHERE id=?`, unknown.ID); err == nil {
		t.Fatal("unknown provenance gained authority")
	}
	if err = ApplyMigrations(ctx, raw, historicalMigrations()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = raw.QueryRow(`SELECT COUNT(*) FROM agent_executions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("replay count=%d %v", count, err)
	}
	rows, err := raw.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("migration broke foreign keys")
	}
}

func TestLegacyUnknownCancellationReceiptCannotTerminalizeSafeOwner(t *testing.T) {
	f := newAgentStorageFixture(t, "legacy-cancellation-replay")
	ctx := context.Background()
	r, _ := beginRecoveryRun(t, f, agentconversation.Review, "legacy-cancellation-replay", true)
	if err := NewAgentConversationStore(f.db).RecordFinalization(ctx, r.ID, agentconversation.Finalization{Advance: agentconversation.KnownAdvance, ExitBoundaryTurnID: "turn-legacy-cancellation-replay", Quiesced: true}); err != nil {
		t.Fatal(err)
	}
	if err := terminalizeRun(t, f.db, r.ID); err != nil {
		t.Fatal(err)
	}
	r, _ = NewRunStore(f.db).Get(ctx, r.ID)
	p, err := NewReviewStore(f.db).Get(ctx, r.ReviewID)
	if err != nil {
		t.Fatal(err)
	}
	in := executioncancel.Input{ProjectID: p.ProjectID, AISessionID: p.CreatedByAISessionID, OwnerType: "REVIEW", OwnerID: p.ID, RunID: r.ID, RequestID: "legacy-request", Reason: "historical request"}
	// Reproduce an existing pre-migration pending receipt, not a new v42 request.
	_, err = f.db.sql.Exec(`INSERT INTO execution_cancellations(project_id,request_id,execution_id,run_id,requested_by_ai_session_id,reason,requested_at,runtime_epoch,state,interrupt_state,owner_type,owner_id,task_id,backend_disposition) VALUES(?,?,NULL,?,?,?,?,?,'CANCEL_REQUESTED','NOT_SENT','REVIEW',?,?,'ACTIVE')`, in.ProjectID, in.RequestID, in.RunID, in.AISessionID, in.Reason, formatTime(testNow), r.RuntimeEpoch, p.ID, p.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeClaim, beforeClaimErr := NewAgentConversationStore(f.db).GetClaim(ctx, p.FeatureAreaID)
	var guard string
	if err = f.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE type='trigger' AND name='agent_execution_identity_immutable'`).Scan(&guard); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`DROP TRIGGER agent_execution_identity_immutable`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE agent_executions SET provenance_kind='LEGACY_UNKNOWN',semantic_owner_kind=NULL,semantic_owner_id=NULL WHERE id=?`, r.AgentExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(guard); err != nil {
		t.Fatal(err)
	}
	store := NewExecutionCancellationStore(f.db)
	if _, _, err = store.Begin(ctx, in, false); !errors.Is(err, agentexecution.ErrInvalidAssociation) {
		t.Fatalf("replay granted authority: %v", err)
	}
	if err = store.FinishInterrupted(ctx, r.ID); !errors.Is(err, agentexecution.ErrInvalidAssociation) {
		t.Fatalf("offline completion granted authority: %v", err)
	}
	if err = store.RecordInterrupt(ctx, in.ProjectID, in.RequestID, "ACKNOWLEDGED"); err == nil {
		t.Fatal("unknown cancellation receipt mutable")
	}
	reopenAgentFixtureDB(t, &f)
	if err = NewRecoveryStore(f.db).RecoverAgentOperations(ctx, testNow); err != nil {
		t.Fatalf("unknown receipt blocked startup: %v", err)
	}
	store = NewExecutionCancellationStore(f.db)
	current, err := NewReviewStore(f.db).Get(ctx, p.ID)
	if err != nil || current.RunID != r.ID {
		t.Fatalf("owner mutated: %+v %v", current, err)
	}
	receipt, err := store.Get(ctx, in.ProjectID, in.RequestID)
	if err != nil || receipt.BackendDisposition != "ACTIVE" {
		t.Fatalf("receipt mutated: %+v %v", receipt, err)
	}
	// Read projection observes the newly fenced epoch; the durable history must not change.
	var persistedState, persistedInterrupt string
	if err = f.db.sql.QueryRow(`SELECT state,interrupt_state FROM execution_cancellations WHERE project_id=? AND request_id=?`, in.ProjectID, in.RequestID).Scan(&persistedState, &persistedInterrupt); err != nil || persistedState != "CANCEL_REQUESTED" || persistedInterrupt != "NOT_SENT" {
		t.Fatalf("receipt history mutated: %s %s %v", persistedState, persistedInterrupt, err)
	}
	var classified int
	if err = f.db.sql.QueryRow(`SELECT COUNT(*) FROM agent_operation_recovery_states WHERE run_id=?`, r.ID).Scan(&classified); err != nil || classified != 0 {
		t.Fatalf("unknown history classified with authority: %d %v", classified, err)
	}
	claim, err := NewAgentConversationStore(f.db).GetClaim(ctx, p.FeatureAreaID)
	if !reflect.DeepEqual(claim, beforeClaim) || !errors.Is(err, beforeClaimErr) {
		t.Fatalf("claim lost: %+v %v", claim, err)
	}
}

// Current schema already has exact native ownership for every operation kind.
func TestAgentExecutionAllKindsSurviveReopenWithoutBackfill(t *testing.T) {
	f := newAgentStorageFixture(t, "native-kinds-reopen")
	ctx := context.Background()
	reviewOwner := prepareAgentOperation(t, f, agentconversation.Review, "native-review")
	reviewRun, err := reviewOwner.make(ctx, "native-review-run")
	if err != nil {
		t.Fatal(err)
	}
	dev := f.addAreaTask(t, "native-execution-area", "native-execution-task")
	executionOwner := prepareAgentOperation(t, dev, agentconversation.Execution, "native-execution")
	implementation, err := executionOwner.make(ctx, "native-implementation")
	if err != nil {
		t.Fatal(err)
	}
	probes := NewContextProbeStore(f.db)
	probe := contextprobe.Probe{ID: "native-inspection", AISessionID: f.session.ID, ProjectID: f.project.ID, RepositoryID: f.task.RepositoryID, CodexConfigID: f.task.CodexConfigID, Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository, Paths: []string{}}, CodexInstruction: "inspect", Status: contextprobe.StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, CreatedAt: testNow, UpdatedAt: testNow}
	if err := probes.Create(ctx, probe); err != nil {
		t.Fatal(err)
	}
	probe, err = probes.Update(ctx, probe.ID, func(v *contextprobe.Probe) error {
		v.Status = contextprobe.StatusRunning
		v.StartedAt = &testNow
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := NewRunStore(f.db).Get(ctx, probe.RunID)
	if err != nil {
		t.Fatal(err)
	}
	expected := []run.Run{reviewRun, implementation, inspection}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, f.root)
	defer db.Close()
	kinds := map[agentexecution.Kind]bool{}
	for _, before := range expected {
		after, err := NewRunStore(db).Get(ctx, before.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !before.SameOwner(after) || before.Status != after.Status || !before.CreatedAt.Equal(after.CreatedAt) {
			t.Fatal("reopen changed operation identity")
		}
		e, err := NewAgentExecutionStore(db).Get(ctx, after.AgentExecutionID)
		if err != nil || e.TaskID != before.TaskID || e.SemanticOwnerID != before.OperationID || e.ProvenanceKind != agentexecution.ProvenanceNative {
			t.Fatalf("exact native owner: %+v %v", e, err)
		}
		kinds[e.Kind] = true
	}
	if len(kinds) != 3 {
		t.Fatal("missing operation kind")
	}
}
