package sqlite

import (
	"context"
	"database/sql"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"strings"
	"testing"
	"time"
)

func TestAgentPolicyPlanPersistenceAndNoDowngrade(t *testing.T) {
	db, planID, w := preparedBindingFixture(t, "pi", "COOPERATIVE_TRUSTED_V1")
	defer db.Close()
	ctx := context.Background()
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, db.sql, planID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AgentConfigFingerprint != w.AgentConfigFingerprint || plan.AgentID != w.AgentID || plan.ExecutionPolicy != w.ExecutionPolicy {
		t.Fatal("frozen policy lost at readback")
	}
	original := w
	w.AgentID = ""
	w.ExecutionPolicy = ""
	w.AgentConfigFingerprint = ""
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunStore(db).BindPreparedRuntimeWorkset(ctx, planID, "downgrade", strings.Repeat("7", 64), "authority", w, time.Now().UTC())
	if err == nil {
		t.Fatal("native downgrade of cooperative plan accepted")
	}
	// A changed agent is equally not the confirmed selection.
	w = original
	w.AgentID = "codex"
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRunStore(db).BindPreparedRuntimeWorkset(ctx, planID, "changed-agent", strings.Repeat("8", 64), "authority", w, time.Now().UTC())
	if err == nil {
		t.Fatal("agent replacement accepted")
	}
	w = original
	w.AgentConfigFingerprint = strings.Repeat("d", 64)
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunStore(db).BindPreparedRuntimeWorkset(ctx, planID, "changed-config", strings.Repeat("a", 64), "authority", w, time.Now().UTC()); err == nil {
		t.Fatal("configured launcher substitution accepted")
	}
	store := NewRunStore(db)
	if _, err = store.BindPreparedRuntimeWorkset(ctx, planID, "exact-policy", strings.Repeat("9", 64), "authority", original, time.Now().UTC()); err != nil {
		t.Fatalf("exact cooperative plan binding failed: %v", err)
	}
	if _, err = store.BindPreparedRuntimeWorkset(ctx, planID, "exact-policy", strings.Repeat("9", 64), "authority", original, time.Now().UTC()); err != nil {
		t.Fatalf("exact cooperative replay failed: %v", err)
	}
	restored, err := store.RuntimeWorkset(ctx, original.RunID)
	if err != nil || restored.AgentConfigFingerprint != original.AgentConfigFingerprint || restored.AgentID != original.AgentID || restored.ExecutionPolicy != original.ExecutionPolicy || restored.ContentHash != original.ContentHash {
		t.Fatalf("cooperative workset readback lost policy: %+v %v", restored, err)
	}
}

func TestAgentPolicyMigrationIsAdditive(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE runtime_scope_plans(plan_id TEXT PRIMARY KEY,content_hash TEXT NOT NULL,payload_json TEXT NOT NULL); INSERT INTO runtime_scope_plans VALUES('legacy','frozen-hash','{"version":"TASK_SANDBOX_V1"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(agentPolicyV136SQL); err != nil {
		t.Fatal(err)
	}
	var hash, payload, agent, policy, fingerprint string
	err = db.QueryRow(`SELECT content_hash,payload_json,agent_id,execution_policy,agent_config_fingerprint FROM runtime_scope_plans WHERE plan_id='legacy'`).Scan(&hash, &payload, &agent, &policy, &fingerprint)
	if err != nil || hash != "frozen-hash" || payload != `{"version":"TASK_SANDBOX_V1"}` || agent != "" || policy != "" || fingerprint != "" {
		t.Fatalf("historical record changed: %q %q %q %q %v", hash, payload, agent, policy, err)
	}
}

func TestAgentPolicyExecutionProposalSurvivesRestart(t *testing.T) {
	f := newReviewApprovalFixture(t)
	inventory := f.store.runtimeAllowlist()
	inventory.AgentConfigFingerprints = map[string]string{"codex": strings.Repeat("c", 64)}
	f.store.SetRuntimeConfigProvider(func() codexruntime.Allowlist { return inventory.Clone() })
	a := f.start(t)
	f.complete(t, a, approvedResult(t))
	in := f.input(a, "APPROVE")
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", CodexFastMode: true, AgentID: "codex", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1", AgentConfigFingerprint: strings.Repeat("c", 64)}
	out, err := f.store.Resolve(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := f.store.AuthorizeTaskExecution(context.Background(), f.session, out.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.Resolve(context.Background(), in)
	if err != nil || !replay.Replay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changedConfig := *in.NextRunRuntimeConfig
	changedConfig.AgentConfigFingerprint = strings.Repeat("d", 64)
	forged := in
	forged.NextRunRuntimeConfig = &changedConfig
	if _, err = f.store.Resolve(context.Background(), forged); err == nil {
		t.Fatal("replay accepted changed configured launcher")
	}
	changed := *in.NextRunRuntimeConfig
	changed.AgentID = ""
	changed.ExecutionPolicy = ""
	changed.AgentConfigFingerprint = ""
	in.NextRunRuntimeConfig = &changed
	if _, err = f.store.Resolve(context.Background(), in); err == nil {
		t.Fatal("review replay accepted a changed runtime config")
	}
	if got := execution.RuntimeConfig; got.Model != "gpt-5.6-terra" || got.ReasoningEffort != "high" || !got.CodexFastMode || got.ServiceTier != "priority" || got.AgentConfigFingerprint != strings.Repeat("c", 64) || got.AgentID != "codex" || got.ExecutionPolicy != "COOPERATIVE_TRUSTED_V1" {
		t.Fatalf("runtime=%+v", got)
	}
	if execution.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || execution.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity {
		t.Fatalf("cooperative execution claims native identities: %+v", execution)
	}
	root := f.db.StateRoot()
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := NewExecutionProposalStore(reopened).Get(context.Background(), execution.ID)
	if err != nil || restored.RuntimeConfig != execution.RuntimeConfig {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
}

func TestAgentPolicyDirectBindingRequiresFormalSelection(t *testing.T) {
	db, _, w := preparedBindingFixture(t)
	defer db.Close()
	w.AgentID = "pi"
	w.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
	w.AgentConfigFingerprint = strings.Repeat("c", 64)
	w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
	var err error
	w, err = w.WithHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRunStore(db).BindRuntimeWorkset(context.Background(), w); err == nil {
		t.Fatal("workset granted itself cooperative execution")
	}
	var bound int
	if err = db.sql.QueryRow(`SELECT count(*) FROM runs WHERE id=? AND runtime_workset_json IS NOT NULL`, w.RunID).Scan(&bound); err != nil || bound != 0 {
		t.Fatalf("failed selection binding changed runtime: %d %v", bound, err)
	}
}

func TestAgentPolicyCannotDisappearWithEmptyRuntimeModel(t *testing.T) {
	if _, err := encodeExecutionRuntimeConfig(codexruntime.NextRunRuntimeConfig{AgentID: "pi", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1", AgentConfigFingerprint: strings.Repeat("c", 64)}); err == nil {
		t.Fatal("explicit selection silently serialized as absent runtime")
	}
}

func TestAgentPolicyNoRunSuccessorSourceRequiresExactSelection(t *testing.T) {
	f, owner, predecessor := qualifiedExecutionFixture(t, "-policy-source")
	in := readyNoRunSuccessor(t, f, owner, predecessor, "cancel-policy-source")
	ctx := context.Background()
	var planID string
	if err := f.db.sql.QueryRow(`SELECT runtime_scope_plan_id FROM execution_proposal_admissions WHERE proposal_id=?`, predecessor.ID).Scan(&planID); err != nil {
		t.Fatal(err)
	}
	plan, err := readArchivedRuntimeSelectionPlanTx(ctx, f.db.sql, planID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = validateSuccessorSourceScopeTx(ctx, tx, in, predecessor, plan); err != nil {
		t.Fatalf("baseline source invalid: %v", err)
	}
	plan.AgentConfigFingerprint = strings.Repeat("d", 64)
	if err = validateSuccessorSourceScopeTx(ctx, tx, in, predecessor, plan); err == nil {
		t.Fatal("successor source changed configured launcher fingerprint")
	}
	plan.AgentConfigFingerprint = ""
	plan.AgentID = "codex"
	if err = validateSuccessorSourceScopeTx(ctx, tx, in, predecessor, plan); err == nil {
		t.Fatal("successor source changed explicit agent")
	}
	plan.AgentID = ""
	plan.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
	if err = validateSuccessorSourceScopeTx(ctx, tx, in, predecessor, plan); err == nil {
		t.Fatal("successor source changed execution policy")
	}
}
