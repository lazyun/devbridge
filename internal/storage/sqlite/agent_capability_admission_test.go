package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/task"
)

func TestPiKnownResumeRequirementRejectedBeforeAdmission(t *testing.T) {
	f := newReviewApprovalFixture(t)
	inventory := f.store.runtimeAllowlist()
	inventory.AgentConfigFingerprints = map[string]string{"pi": strings.Repeat("c", 64)}
	f.store.SetRuntimeConfigProvider(func() codexruntime.Allowlist { return inventory.Clone() })
	review := f.start(t)
	f.complete(t, review, approvedResult(t))
	in := f.input(review, "APPROVE")
	in.NextRunRuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high", AgentID: "pi", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1", AgentConfigFingerprint: strings.Repeat("c", 64)}
	before := map[string]int{}
	for _, table := range []string{"execution_proposals", "runtime_scope_plans", "runs", "feature_area_agent_claims"} {
		var n int
		if err := f.db.sql.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		before[table] = n
	}
	if _, err := f.store.Resolve(context.Background(), in); !errors.Is(err, agent.ErrCapabilityUnsupported) {
		t.Fatalf("expected explicit thread_resume refusal, got %v", err)
	}
	for table, want := range before {
		var n int
		if err := f.db.sql.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != want {
			t.Fatalf("%s changed %d -> %d (%v)", table, want, n, err)
		}
	}
}

func TestPiCurrentConversationRequiresResume(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE agent_conversations(feature_area_id TEXT,ended_at TEXT,backend_thread_id TEXT)`); err != nil {
		t.Fatal(err)
	}
	config := codexruntime.NextRunRuntimeConfig{AgentID: "pi", ExecutionPolicy: "COOPERATIVE_TRUSTED_V1", AgentConfigFingerprint: strings.Repeat("c", 64)}
	ctx := context.Background()
	if err = validatePiResumeRequirementTx(ctx, db, "area", config, task.VerificationModeNone); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO agent_conversations VALUES('area',NULL,'canonical-thread')`); err != nil {
		t.Fatal(err)
	}
	if err = validatePiResumeRequirementTx(ctx, db, "area", config, task.VerificationModeNone); !errors.Is(err, agent.ErrCapabilityUnsupported) {
		t.Fatalf("existing thread was not blocked: %v", err)
	}
	if err = validatePiResumeRequirementTx(ctx, db, "other-area", config, task.VerificationModeNone); err != nil {
		t.Fatal("unrelated thread blocked:", err)
	}
	if _, err = db.Exec(`UPDATE agent_conversations SET ended_at='ended'`); err != nil {
		t.Fatal(err)
	}
	if err = validatePiResumeRequirementTx(ctx, db, "area", config, task.VerificationModeNone); err != nil {
		t.Fatal("ended thread treated as current:", err)
	}
	config.AgentID = "codex"
	if err = validatePiResumeRequirementTx(ctx, db, "area", config, task.VerificationModeRun); err != nil {
		t.Fatal("Codex capability changed:", err)
	}
}
