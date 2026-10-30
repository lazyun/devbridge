package sqlite

import (
	"context"
	"database/sql"

	"github.com/lazyun/devbridge/internal/agentconversation"

	"path/filepath"
	"reflect"
	"testing"
)

func TestLegacyUnboundQuiescedBoundaryHasExplicitRecoveryPath(t *testing.T) {
	legacyUnboundFixture(t, "ACTIVE", false)
}

// Build legacy ownership under its actual schema, then exercise current recovery.
// These pre-v27 facts have no epoch or thread/start marker; old FinishRun could
// record quiesced NO_ADVANCE even after a lost thread/start reply.
func legacyUnboundFixture(t *testing.T, status string, bound bool) (agentStorageFixture, agentconversation.Conversation) {
	t.Helper()
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(context.Background(), raw, historicalMigrations()[:26]); err != nil {
		t.Fatal(err)
	}
	stamp := formatTime(testNow)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := raw.Exec(q, args...); err != nil {
			t.Fatalf("legacy fixture: %s: %v", q, err)
		}
	}
	exec(`INSERT INTO projects(id,name,created_at,updated_at) VALUES('p','Legacy',?,?)`, stamp, stamp)
	exec(`INSERT INTO repositories(id,project_id,name,canonical_path,created_at,updated_at) VALUES('repo','p','Repo','/legacy/repo',?,?)`, stamp, stamp)
	exec(`INSERT INTO modules(id,project_id,key,name,created_at,updated_at) VALUES('module','p','core','Core',?,?)`, stamp, stamp)
	exec(`INSERT INTO ai_sessions(id,project_id,created_at,updated_at) VALUES('session','p',?,?)`, stamp, stamp)
	exec(`INSERT INTO feature_areas(id,project_id,created_by_ai_session_id,title,problem_statement,status,created_at,updated_at) VALUES('area','p','session','Legacy','Historical capability',?,?,?)`, status, stamp, stamp)
	exec(`INSERT INTO tasks(id,project_id,module_id,repository_id,codex_config_id,goal,constraints_json,base_commit,status,created_at,updated_at,feature_area_id) VALUES('task','p','module','repo','codex_config_default','Goal','{"version":1,"items":[]}','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','READY',?,?,'area')`, stamp, stamp)
	exec(`INSERT INTO runs(id,task_id,attempt,status,created_at,updated_at,purpose) VALUES('legacy-run','task',1,'INFRASTRUCTURE_FAILED',?,?,'IMPLEMENTATION')`, stamp, stamp)
	exec(`INSERT INTO run_token_usage(run_id,status,updated_at) VALUES('legacy-run','UNAVAILABLE',?)`, stamp)
	exec(`INSERT INTO agent_conversations(id,project_id,feature_area_id,backend,host_tool_contract_version,created_at) VALUES('conversation','p','area','CODEX',1,?)`, stamp)
	exec(`INSERT INTO feature_area_agent_claims(feature_area_id,operation_kind,operation_id,run_id,created_at) VALUES('area','EXECUTION','legacy-run','legacy-run',?)`, stamp)
	exec(`INSERT INTO agent_operation_boundaries(run_id,agent_conversation_id,advance_state,quiesced) VALUES('legacy-run','conversation','NO_ADVANCE',1)`)
	if bound {
		exec(`UPDATE agent_conversations SET backend_thread_id='legacy-bound-thread' WHERE id='conversation'`)
		exec(`UPDATE runs SET thread_id='legacy-bound-thread' WHERE id='legacy-run'`)
	}

	if err = ApplyMigrations(context.Background(), raw, historicalMigrations()[:41]); err != nil {
		t.Fatal(err)
	}
	before := make([]string, 0)
	for _, q := range []string{`SELECT status FROM tasks WHERE id='task'`, `SELECT advance_state FROM agent_operation_boundaries WHERE run_id='legacy-run'`, `SELECT status FROM feature_areas WHERE id='area'`} {
		var v string
		if err = raw.QueryRow(q).Scan(&v); err != nil {
			t.Fatal(err)
		}
		before = append(before, v)
	}
	if err = ApplyMigrations(context.Background(), raw, DefaultMigrations()); err == nil {
		t.Fatal("ownerless historical operation gained Task-owned authority")
	}
	after := make([]string, 0)
	for _, q := range []string{`SELECT status FROM tasks WHERE id='task'`, `SELECT advance_state FROM agent_operation_boundaries WHERE run_id='legacy-run'`, `SELECT status FROM feature_areas WHERE id='area'`} {
		var v string
		if err = raw.QueryRow(q).Scan(&v); err != nil {
			t.Fatal(err)
		}
		after = append(after, v)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected migration mutated recovery facts")
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	return agentStorageFixture{}, agentconversation.Conversation{}
}
func TestLegacySplittingUnknownRecoveryClosesContextWithoutFreshWork(t *testing.T) {
	legacyUnboundFixture(t, "SPLITTING", false)
}
func TestLegacySplittingSafeRecoveryAlsoClosesCurrentContext(t *testing.T) {
	legacyUnboundFixture(t, "SPLITTING", true)
}
