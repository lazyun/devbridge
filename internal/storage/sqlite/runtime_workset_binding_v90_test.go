package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

// This fixture exercises the v90 binding boundary after a durable Run and
// observation exist. The admission and external workspace builder have their
// own tests; this test checks the four writes in the binding transaction.
func preparedBindingFixture(t *testing.T) (*DB, string, workset.RuntimeWorkset) {
	t.Helper()
	ctx := context.Background()
	db := openTestDB(t, t.TempDir())
	f := runtimeWorksetFixture(t, db)
	now := time.Now().UTC()
	const workspaceID = "workspace-v90-binding"
	if err := NewWorkspaceStore(db).Create(ctx, workspace.Workspace{ID: workspaceID, ProjectID: f.project.ID, RepositoryID: f.repository.ID, FeatureAreaID: f.task.FeatureAreaID, OwnerKind: workspace.OwnerFeatureArea, OwnerID: f.task.FeatureAreaID, BaseCommit: strings.Repeat("a", 40), Status: workspace.StatusReady, CreatedAt: now, UpdatedAt: now}); err != nil {
		db.Close()
		t.Fatal(err)
	}
	tx, err := db.beginWrite(ctx)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, f.project.ID)
	if err != nil {
		tx.Rollback()
		db.Close()
		t.Fatal(err)
	}
	target := workset.Target{RepositoryID: f.repository.ID, MemberID: "member-v90-binding", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"internal"}}}
	plan := runtimepreparation.Plan{ID: "plan-v90-binding", Generation: 1, OwnerKind: runtimepreparation.OwnerImplementation, OwnerID: f.run.ExecutionProposalID, ProjectID: f.project.ID, FeatureAreaID: f.task.FeatureAreaID, TaskID: f.task.ID, SourceCommand: "TEST_BINDING", RepositoryTargets: []workset.Target{target}, PrimaryMemberID: target.MemberID, BackendIdentity: f.task.CodexConfigID, Model: "model", ReasoningEffort: "high", RuntimeAdapterIdentity: "codex-app-server", TopologyRevision: topology, DevelopmentContextHash: development, DevelopmentRefs: map[string]string{}, ExpectedSourceHEADs: map[string]string{target.MemberID: target.OriginCommit}, WriteScopes: map[string]workset.WriteScope{target.MemberID: target.Write}, PreparationRequestID: "create-v90-binding", PreparationPayloadHash: strings.Repeat("1", 64), CreatedAt: now}
	plan.ContentHash, err = plan.ComputeContentHash()
	if err == nil {
		err = freezeRuntimeScopePlanTx(ctx, tx, plan)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO runtime_preparation_attempts(attempt_id,plan_id,run_id,request_id,payload_hash,stage,outcome,created_at) VALUES(?,?,?,?,?,'RUN_CREATED','PENDING',?)`, "attempt-v90-binding", plan.ID, f.run.ID, plan.PreparationRequestID, plan.PreparationPayloadHash, formatTime(now))
	}
	if err == nil {
		err = tx.Commit()
	} else {
		tx.Rollback()
	}
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	store := NewRunStore(db)
	const claimRequest = "claim-v90-binding"
	claimHash := strings.Repeat("2", 64)
	if replay, err := store.ClaimWorkspacePreparation(ctx, plan.ID, f.run.ID, claimRequest, claimHash, now); err != nil || replay {
		db.Close()
		t.Fatalf("claim replay=%v err=%v", replay, err)
	}
	observation := runtimepreparation.WorkspaceObservation{ID: "observation-v90-binding", PlanID: plan.ID, RunID: f.run.ID, WorkspaceID: workspaceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, WorktreeID: "worktree-v90-binding", CanonicalRoot: "/managed/v90-binding", RootIdentity: "root-v90-binding", StartHEAD: target.OriginCommit, StartSnapshot: target.OriginCommit, Branch: "refs/heads/main", MetadataDigest: strings.Repeat("3", 64), Generation: 1, ObservedAt: now.Add(time.Second)}
	observationHash, err := store.RecordWorkspaceObservations(ctx, plan.ID, f.run.ID, claimRequest, claimHash, []runtimepreparation.WorkspaceObservation{observation}, now.Add(2*time.Second))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if replayHash, err := store.RecordWorkspaceObservations(ctx, plan.ID, f.run.ID, claimRequest, claimHash, []runtimepreparation.WorkspaceObservation{observation}, now.Add(2*time.Second)); err != nil || replayHash != observationHash {
		db.Close()
		t.Fatalf("observation response-loss replay hash=%q err=%v", replayHash, err)
	}
	w := workset.RuntimeWorkset{ProtocolVersion: workset.RuntimeProtocolVersion, ProjectID: f.project.ID, FeatureAreaID: f.task.FeatureAreaID, TaskID: f.task.ID, TaskDefinitionRevisionID: "revision-v90-binding", ExecutionID: f.run.ExecutionProposalID, RunID: f.run.ID, WorkspaceID: workspaceID, PrimaryMemberID: target.MemberID, Members: []workset.RuntimeMemberBinding{{RepositoryID: target.RepositoryID, RepositoryAlias: "app", MemberID: target.MemberID, MemberVersion: target.MemberVersion, WorktreeID: observation.WorktreeID, CanonicalRoot: observation.CanonicalRoot, RootFileIdentity: observation.RootIdentity, OriginCommit: target.OriginCommit, StartHEAD: observation.StartHEAD, StartSnapshot: observation.StartSnapshot, Read: true, Write: target.Write, RuntimeRootIdentity: "runtime-root-v90-binding"}}, BackendConfigID: plan.BackendIdentity, BackendConfigFingerprint: strings.Repeat("4", 64), Model: plan.Model, ReasoningEffort: plan.ReasoningEffort, RuntimeAdapterIdentity: plan.RuntimeAdapterIdentity, RuntimeProtocolIdentity: "1", CreatedAt: now}
	w, err = w.WithHash()
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, plan.ID, w
}

func TestRuntimeWorksetAtomicBindingAndExactReplayV90(t *testing.T) {
	for _, faultStage := range []string{"workset", "binding", "start-authority", "receipt", ""} {
		t.Run(faultStage, func(t *testing.T) {
			db, planID, value := preparedBindingFixture(t)
			defer db.Close()
			store := NewRunStore(db)
			if faultStage != "" {
				db.runtimeBindingFaultHook = func(stage string) error {
					if stage == faultStage {
						return errors.New("injected binding fault")
					}
					return nil
				}
			}
			requestID, payloadHash, authorityID := "bind-v90-binding", strings.Repeat("5", 64), "authority-v90-binding"
			_, err := store.BindPreparedRuntimeWorkset(context.Background(), planID, requestID, payloadHash, authorityID, value, time.Now().UTC())
			if faultStage != "" {
				if err == nil {
					t.Fatal("injected fault accepted")
				}
				for _, table := range []string{"runtime_workset_bindings", "runtime_start_authorities"} {
					var count int
					if e := db.sql.QueryRow(`SELECT count(*) FROM `+table+` WHERE plan_id=?`, planID).Scan(&count); e != nil || count != 0 {
						t.Fatalf("%s survived rollback: %d %v", table, count, e)
					}
				}
				var raw any
				if e := db.sql.QueryRow(`SELECT runtime_workset_json FROM runs WHERE id=?`, value.RunID).Scan(&raw); e != nil || raw != nil {
					t.Fatalf("Workset survived rollback: %v %v", raw, e)
				}
				db.runtimeBindingFaultHook = nil
				_, err = store.BindPreparedRuntimeWorkset(context.Background(), planID, requestID, payloadHash, authorityID, value, time.Now().UTC())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = store.BindPreparedRuntimeWorkset(context.Background(), planID, requestID, payloadHash, authorityID, value, time.Now().UTC()); err != nil {
				t.Fatalf("exact response-loss replay: %v", err)
			}
			if _, err = store.BindPreparedRuntimeWorkset(context.Background(), planID, requestID, strings.Repeat("6", 64), authorityID, value, time.Now().UTC()); !errors.Is(err, ErrConflict) {
				t.Fatalf("different payload replay error=%v", err)
			}
		})
	}
}

func TestRuntimeWorksetBindingRejectsScopeExpansionV90(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*workset.RuntimeWorkset)
	}{
		{"write-scope", func(w *workset.RuntimeWorkset) { w.Members[0].Write = workset.WriteScope{Kind: "REPOSITORY"} }},
		{"origin-commit", func(w *workset.RuntimeWorkset) { w.Members[0].OriginCommit = strings.Repeat("b", 40) }},
		{"backend", func(w *workset.RuntimeWorkset) { w.BackendConfigID = "different-backend" }},
		{"model", func(w *workset.RuntimeWorkset) { w.Model = "different-model" }},
		{"workspace", func(w *workset.RuntimeWorkset) { w.WorkspaceID = "different-workspace" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			db, planID, value := preparedBindingFixture(t)
			defer db.Close()
			change.mutate(&value)
			var err error
			value, err = value.WithHash()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := NewRunStore(db).BindPreparedRuntimeWorkset(context.Background(), planID, "bind-v90-binding", strings.Repeat("5", 64), "authority-v90-binding", value, time.Now().UTC()); !errors.Is(err, ErrConflict) {
				t.Fatalf("expanded authority error=%v", err)
			}
		})
	}
}

func TestRuntimePreparationCreateRunRejectsTopologyDriftV90(t *testing.T) {
	db, planID, value := preparedBindingFixture(t)
	defer db.Close()
	if _, err := db.sql.Exec(`UPDATE project_repository_topologies SET revision=revision+1 WHERE project_id=?`, value.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunStore(db).CreatePreparationRun(context.Background(), planID, "second-run-v90-binding", "create-v90-binding", strings.Repeat("1", 64), time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatalf("topology drift error=%v", err)
	}
	var count int
	if err := db.sql.QueryRow(`SELECT count(*) FROM runs WHERE id='second-run-v90-binding'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("stale preparation created Run count=%d err=%v", count, err)
	}
}
