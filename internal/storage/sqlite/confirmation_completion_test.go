package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

// This fixture exercises the confirmation journal through its internal writers.
// Step completion here does not assert platform eligibility or business issuance.
func completionFixture(t *testing.T) (*ConfirmationStore, context.Context, confirmation.Intent) {
	t.Helper()
	return completionFixtureWithAction(t, "CREATE_TASK")
}

func completionFixtureWithAction(t *testing.T, action string) (*ConfirmationStore, context.Context, confirmation.Intent) {
	t.Helper()
	store, ctx, session, project := prepareReadbackFixture(t)
	input := taskPrepareInput(session, "completion-prepare")
	input.Action = action
	input.SubmittedHash = confirmation.ValueHash(input)
	prepared, err := store.Prepare(ctx, project, input)
	if err != nil {
		t.Fatal(err)
	}
	in := confirmation.CommitInput{AISessionID: session, IntentID: prepared.IntentID, Version: prepared.Version,
		Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "completion-commit", ContentHash: prepared.ContentHash, VariantID: "only"}
	in.SubmittedHash = confirmation.ValueHash(in)
	steps := []confirmation.Step{{Kind: "FIRST", RequestID: "first", State: "PENDING"}, {Kind: "SECOND", RequestID: "second", State: "PENDING"}}
	out, fresh, err := store.BeginCommit(ctx, in, steps)
	if err != nil || !fresh {
		t.Fatalf("begin: fresh=%v err=%v", fresh, err)
	}
	return store, ctx, out
}

func completionSnapshot(t *testing.T, store *ConfirmationStore) string {
	t.Helper()
	return platformTableSnapshot(t, store.db, []string{"confirmation_intents", "confirmation_commit_requests", "tasks", "execution_proposals", "runs"})
}

func finishConfirmation(t *testing.T, store *ConfirmationStore, ctx context.Context, out confirmation.Intent) confirmation.Intent {
	t.Helper()
	for i, step := range out.Steps {
		step.State = "COMPLETED"
		state := "CONFIRMING"
		if i == len(out.Steps)-1 {
			state = "COMPLETED"
		}
		var err error
		out, err = store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, step, state)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestConfirmationCompletionRequiresEveryOriginalStep(t *testing.T) {
	store, ctx, out := completionFixture(t)
	before := completionSnapshot(t, store)
	step := out.Steps[1]
	step.State = "COMPLETED"
	rejected, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, step, "COMPLETED")
	if !errors.Is(err, ErrConflict) || rejected.State != "CONFIRMING" || rejected.Steps[1].State != "PENDING" {
		t.Fatalf("premature completion returned staged state: %+v, %v", rejected, err)
	}
	if completionSnapshot(t, store) != before {
		t.Fatal("premature completion changed durable facts")
	}
	first := out.Steps[0]
	first.State = "COMPLETED"
	out, err = store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, first, "CONFIRMING")
	if err != nil {
		t.Fatal(err)
	}
	before = completionSnapshot(t, store)
	if _, err = store.db.sql.Exec(`CREATE TRIGGER completion_write_fault BEFORE UPDATE ON confirmation_intents WHEN json_extract(NEW.intent_json,'$.state')='COMPLETED' BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	rejected, err = store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, step, "COMPLETED")
	if err == nil || rejected.State != "CONFIRMING" || rejected.Steps[1].State != "PENDING" {
		t.Fatalf("failed save exposed staged completion: %+v %v", rejected, err)
	}
	if completionSnapshot(t, store) != before {
		t.Fatal("failed save changed durable facts")
	}
	if _, err = store.db.sql.Exec(`DROP TRIGGER completion_write_fault`); err != nil {
		t.Fatal(err)
	}
	out, err = store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, step, "COMPLETED")
	if err != nil || out.State != "COMPLETED" {
		t.Fatalf("completion: %+v %v", out, err)
	}
}

func TestConfirmationCompletedReplayIsReadOnlyAndCannotReopen(t *testing.T) {
	store, ctx, out := completionFixture(t)
	out = finishConfirmation(t, store, ctx, out)
	before := completionSnapshot(t, store)
	last := out.Steps[len(out.Steps)-1]
	for _, state := range []string{"CONFIRMING", "PARTIAL", "UNKNOWN", "REJECTED"} {
		t.Run(state, func(t *testing.T) {
			rejected, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, last, state)
			if !errors.Is(err, ErrConflict) || rejected.State != "COMPLETED" {
				t.Fatalf("downgrade: state=%s err=%v", rejected.State, err)
			}
			if completionSnapshot(t, store) != before {
				t.Fatal("downgrade changed durable facts")
			}
		})
	}
	changed := last
	changed.RunID = "foreign-run"
	if _, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, changed, "COMPLETED"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed step: %v", err)
	}
	if _, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version+1, last, "COMPLETED"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed version: %v", err)
	}
	for _, table := range []string{"confirmation_intents", "runtime_authority"} {
		if _, err := store.db.sql.Exec(`CREATE TRIGGER completion_no_writes_` + table + ` BEFORE UPDATE ON ` + table + ` BEGIN SELECT RAISE(ABORT,'completed replay must not write'); END`); err != nil {
			t.Fatal(err)
		}
	}
	stale := runtimeepoch.WithFence(ctx, "old-epoch", "missing-old-run")
	replay, err := store.RecordStep(stale, out.AISessionID, out.IntentID, out.Version, last, "COMPLETED")
	if err != nil || !reflect.DeepEqual(replay, out) {
		t.Fatalf("read-only replay: %+v %v", replay, err)
	}
	if _, err := store.RecordStep(aisession.WithCallerScope(context.Background(), "foreign-scope"), out.AISessionID, out.IntentID, out.Version, last, "COMPLETED"); err == nil {
		t.Fatal("replay bypassed session caller authorization")
	}
	if completionSnapshot(t, store) != before {
		t.Fatal("replay changed durable facts")
	}
	for _, table := range []string{"confirmation_intents", "runtime_authority"} {
		if _, err := store.db.sql.Exec(`DROP TRIGGER completion_no_writes_` + table); err != nil {
			t.Fatal(err)
		}
	}
	root := store.db.stateRoot
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err := Open(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		store = NewConfirmationStore(db)
		replay, err := store.RecordStep(stale, out.AISessionID, out.IntentID, out.Version, last, "COMPLETED")
		if err != nil || !reflect.DeepEqual(replay, out) {
			t.Fatalf("cold replay %d: %+v %v", i, replay, err)
		}
		read, err := store.Get(ctx, out.AISessionID, out.IntentID)
		if err != nil || read.State != "COMPLETED" || read.CanResume || read.Submission == nil || read.Submission.Outcome != confirmation.Consumed {
			t.Fatalf("cold read: %+v %v", read, err)
		}
		if completionSnapshot(t, store) != before {
			t.Fatal("cold replay changed durable facts")
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfirmationCompletedSQLCannotMutateResult(t *testing.T) {
	store, ctx, out := completionFixture(t)
	out = finishConfirmation(t, store, ctx, out)
	before := completionSnapshot(t, store)
	for _, change := range []string{
		`intent_json=json_set(intent_json,'$.state','UNKNOWN')`,
		`intent_json=json_set(intent_json,'$.steps[0].runId','foreign')`,
		`intent_json=json_set(intent_json,'$.version',99)`,
		`intent_json=json_set(intent_json,'$.canResume',json('true'))`,
		`worker_epoch='foreign'`,
	} {
		if _, err := store.db.sql.Exec(`UPDATE confirmation_intents SET `+change+` WHERE intent_id=?`, out.IntentID); err == nil {
			t.Fatalf("accepted completed mutation: %s", change)
		}
		if completionSnapshot(t, store) != before {
			t.Fatal("SQL mutation changed completed facts")
		}
	}
}

func TestConfirmationCompletionSQLRejectsMalformedSnapshots(t *testing.T) {
	store, _, out := completionFixture(t)
	before := completionSnapshot(t, store)
	for _, change := range []string{
		`json_set(intent_json,'$.state','COMPLETED')`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('null'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('{}'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[null]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"state":"COMPLETED"}]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"kind":"FIRST","requestId":"first","state":"COMPLETED"}]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"kind":"FIRST","requestId":"first","state":null}]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"kind":null,"requestId":"first","state":"COMPLETED"}]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"kind":"FIRST","requestId":"","state":"COMPLETED"}]'))`,
		`json_set(intent_json,'$.state','COMPLETED','$.steps',json('[{"kind":"FIRST","requestId":"first","state":"COMPLETED"},{"kind":"FIRST","requestId":"first","state":"COMPLETED"}]'))`,
		`json_set(intent_json,'$.steps[0].requestId','foreign-request')`,
	} {
		if _, err := store.db.sql.Exec(`UPDATE confirmation_intents SET intent_json=`+change+` WHERE intent_id=?`, out.IntentID); err == nil {
			t.Fatalf("accepted malformed completion: %s", change)
		}
		if completionSnapshot(t, store) != before {
			t.Fatal("malformed completion changed durable facts")
		}
	}
}

type completionNoReads struct{ t *testing.T }

func (q completionNoReads) QueryRowContext(context.Context, string, ...any) *sql.Row {
	q.t.Fatal("stable completion consulted current worker or child facts")
	return nil
}

func TestConfirmationCompletedRecoveryDoesNotConsultLiveFacts(t *testing.T) {
	store, ctx, out := completionFixture(t)
	out = finishConfirmation(t, store, ctx, out)
	out.CanResume = true
	replay, err := recoverConfirmationTx(ctx, completionNoReads{t}, out, "different-worker")
	if err != nil || replay.State != "COMPLETED" || replay.CanResume {
		t.Fatalf("stable result: %+v %v", replay, err)
	}
	for _, kind := range []string{"missing-commit", "empty-steps", "pending-step", "missing-identity"} {
		t.Run(kind, func(t *testing.T) {
			raw, _ := json.Marshal(out)
			var corrupt confirmation.Intent
			if err := json.Unmarshal(raw, &corrupt); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "missing-commit":
				corrupt.Commit = nil
			case "empty-steps":
				corrupt.Steps = nil
			case "pending-step":
				corrupt.Steps[0].State = "PENDING"
			case "missing-identity":
				corrupt.Steps[0].RequestID = ""
			}
			rejected, err := recoverConfirmationTx(ctx, completionNoReads{t}, corrupt, "different-worker")
			if !errors.Is(err, ErrConflict) || !reflect.DeepEqual(rejected, corrupt) {
				t.Fatalf("corrupt completion downgraded or accepted: %+v %v", rejected, err)
			}
		})
	}
	if _, err := recoverConfirmationTx(ctx, completionNoReads{t}, confirmation.Intent{State: "CONFIRMING", Commit: out.Commit}, "worker"); !errors.Is(err, ErrConflict) {
		t.Fatalf("empty committed steps: %v", err)
	}
}

func TestConfirmationCompletionPreservesProvableLegacyConsumption(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		name := "exact-legacy-payload"
		if corrupt {
			name = "unproven-legacy-payload"
		}
		t.Run(name, func(t *testing.T) {
			store, ctx, out := completionFixture(t)
			// Install only the pre-v58 request representation. No business success
			// is forged: both confirmation steps remain pending until RecordStep.
			var immutableDDL string
			if err := store.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='confirmation_request_immutable'`).Scan(&immutableDDL); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.sql.Exec(`DROP TRIGGER confirmation_request_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.sql.Exec(`UPDATE confirmation_commit_requests SET request_json=NULL,submitted_sha256=NULL,outcome=NULL`); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if _, err := store.db.sql.Exec(`UPDATE confirmation_commit_requests SET payload_sha256=?`, workflowHash("different legacy request")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.db.sql.Exec(immutableDDL); err != nil {
				t.Fatal(err)
			}
			first := out.Steps[0]
			first.State = "COMPLETED"
			out, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, first, "CONFIRMING")
			if err != nil {
				t.Fatal(err)
			}
			before := completionSnapshot(t, store)
			last := out.Steps[1]
			last.State = "COMPLETED"
			result, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, last, "COMPLETED")
			if corrupt {
				if !errors.Is(err, confirmation.ErrLegacyReplayUnproven) || result.State != "CONFIRMING" || completionSnapshot(t, store) != before {
					t.Fatalf("unproven legacy completion: %+v %v", result, err)
				}
			} else if err != nil || result.State != "COMPLETED" {
				t.Fatalf("valid legacy completion: %+v %v", result, err)
			}
			var unchanged bool
			if err := store.db.sql.QueryRow(`SELECT request_json IS NULL AND submitted_sha256 IS NULL AND outcome IS NULL FROM confirmation_commit_requests`).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("legacy receipt was fabricated/backfilled: %v", err)
			}
		})
	}
}

func TestConfirmationCompletionRejectsAmbiguousJSON(t *testing.T) {
	for _, key := range []string{"state", "State", "ſtate"} {
		t.Run(key, func(t *testing.T) {
			store, ctx, out := completionFixtureWithAction(t, "CREATE_TASK_AND_REVIEW")
			for i := range out.Steps {
				out.Steps[i].State = "COMPLETED"
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			// SQL sees the canonical CONFIRMING value, whereas Go accepts the
			// last duplicate or case-folded alias as the COMPLETED state.
			ambiguous := strings.TrimSuffix(string(raw), "}") + `,"` + key + `":"COMPLETED"}`
			before := completionSnapshot(t, store)
			if _, err := store.db.sql.Exec(`UPDATE confirmation_intents SET intent_json=? WHERE intent_id=?`, ambiguous, out.IntentID); err == nil {
				t.Fatal("SQL accepted ambiguous state")
			}
			if completionSnapshot(t, store) != before {
				t.Fatal("rejected ambiguous JSON changed facts")
			}
			// A historical malformed row must also fail closed on read. Remove
			// only the new JSON guard to model that pre-v98 representation.
			var ddl string
			if err := store.db.sql.QueryRow(`SELECT sql FROM sqlite_master WHERE name='confirmation_json_identity_update'`).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.sql.Exec(`DROP TRIGGER confirmation_json_identity_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.sql.Exec(`UPDATE confirmation_intents SET intent_json=? WHERE intent_id=?`, ambiguous, out.IntentID); err != nil {
				t.Fatalf("install historical representation: %v", err)
			}
			if _, err := store.db.sql.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			before = completionSnapshot(t, store)
			read, err := store.Get(ctx, out.AISessionID, out.IntentID)
			if !errors.Is(err, ErrConflict) || read.State != "COMPLETED" {
				t.Fatalf("ambiguous read lost state or was accepted: %+v %v", read, err)
			}
			if listed, err := store.ListProject(ctx, out.AISessionID, out.ProjectID); !errors.Is(err, ErrConflict) {
				t.Fatalf("project list accepted ambiguous authority: %+v %v", listed, err)
			}
			if completionSnapshot(t, store) != before {
				t.Fatal("ambiguous read repaired historical data")
			}
			if _, err := store.db.sql.Exec(`UPDATE confirmation_intents SET intent_json=json_set(intent_json,'$.state','UNKNOWN') WHERE intent_id=?`, out.IntentID); err == nil {
				t.Fatal("ambiguous historical completion could be downgraded")
			}
		})
	}
}

func TestConfirmationCompletionV97UpgradePreservesPendingAndCompleted(t *testing.T) {
	for _, complete := range []bool{false, true} {
		name := "pending-steps"
		if complete {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			store, ctx, out := historicalCompletionFixture(t, 97)
			if complete {
				out = finishConfirmation(t, store, ctx, out)
			}
			before := completionSnapshot(t, store)
			root := store.db.stateRoot
			if err := store.db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := Open(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store = NewConfirmationStore(db)
			if completionSnapshot(t, store) != before {
				t.Fatal("v98 migration rewrote confirmation facts")
			}
			var applied, guards int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version=98`).Scan(&applied); err != nil || applied != 1 {
				t.Fatalf("migration applied=%d err=%v", applied, err)
			}
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('confirmation_json_identity_insert','confirmation_json_identity_update','confirmation_step_identity_immutable','confirmation_completed_immutable','confirmation_completed_insert_guard','confirmation_completed_update_guard')`).Scan(&guards); err != nil || guards != 6 {
				t.Fatalf("guards=%d err=%v", guards, err)
			}
			if complete {
				last := out.Steps[len(out.Steps)-1]
				if _, err := store.RecordStep(ctx, out.AISessionID, out.IntentID, out.Version, last, "UNKNOWN"); !errors.Is(err, ErrConflict) {
					t.Fatalf("upgraded completion downgraded: %v", err)
				}
			} else {
				out = finishConfirmation(t, store, ctx, out)
			}
			read, err := store.Get(ctx, out.AISessionID, out.IntentID)
			if err != nil || read.State != "COMPLETED" || read.CanResume {
				t.Fatalf("upgraded read: %+v %v", read, err)
			}
		})
	}
}
