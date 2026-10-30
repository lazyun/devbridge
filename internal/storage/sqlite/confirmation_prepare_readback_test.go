package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/confirmation"
	projectpkg "github.com/lazyun/devbridge/internal/project"
)

// prepareReadbackFixture seeds only the durable rows Prepare needs: a bound
// Session whose caller scope is placed on the context. The intent is stored as
// a single JSON blob, so completeness can only be proven by re-reading through
// the production reader, not the in-memory value.
func prepareReadbackFixture(t *testing.T) (*ConfirmationStore, context.Context, string, string) {
	t.Helper()
	db := openTestDB(t, t.TempDir())
	t.Cleanup(func() { db.Close() })
	const session = "confirm-readback-session"
	const project = "confirm-readback-project"
	if err := NewProjectStore(db).Create(context.Background(), projectpkg.Project{ID: project, Name: project, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedBoundSession(t, db, aisession.Session{ID: session, ProjectID: project, CreatedAt: testNow, UpdatedAt: testNow})
	ctx := aisession.WithCallerScope(context.Background(), "storage-fixture-"+session)
	return NewConfirmationStore(db), ctx, session, project
}

// The Recovery transaction entry point must use the same canonical identity
// and readback path as the public Prepare command.
func TestConfirmationTransactionPrepareParity(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	in := taskPrepareInput(session, "rb-transaction-parity")
	tx, err := store.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareConfirmationIntentTx(ctx, tx, project, in, time.Now().UTC(), store.db.epoch)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("transaction prepare: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	replay, err := store.Prepare(ctx, project, in)
	if err != nil {
		t.Fatalf("public prepare replay: %v", err)
	}
	if prepared.IntentID != replay.IntentID || prepared.ContentHash != replay.ContentHash || prepared.Version != replay.Version || confirmationPrepareReadbackMismatch(prepared, replay) != "" {
		t.Fatalf("canonical prepare mismatch: transaction=%+v public=%+v", prepared, replay)
	}
	if complete, code, _ := confirmation.IntentFactsComplete(&replay); !complete {
		t.Fatalf("incomplete replay: %s", code)
	}
}

func taskPrepareInput(session, requestID string) confirmation.PrepareInput {
	return confirmation.PrepareInput{
		AISessionID:   session,
		RequestID:     requestID,
		Purpose:       confirmation.Task,
		Action:        "CREATE_TASK",
		SubmittedHash: strings.Repeat("a", 64),
		Task:          &confirmation.TaskBody{Title: "Readback task", ProblemStatement: "Prove prepare read-after-write parity.", Constraints: []string{"none"}, StartReview: false, FeatureAreaID: "area", RepositoryID: "repo"},
		Variants:      []confirmation.Variant{{ID: "only", Label: "Only", Summary: "Inspect the repository read-only."}},
	}
}

// A freshly prepared intent must be readable through the production reader with
// identical immutable facts, and IntentFactsComplete must approve it.
func TestConfirmationPrepareReadAfterWriteParity(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	in := taskPrepareInput(session, "rb-prepare")
	prepared, err := store.Prepare(ctx, project, in)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.State != "PENDING" || prepared.ContentHash == "" || prepared.IntentID == "" {
		t.Fatalf("prepared=%+v", prepared)
	}
	if complete, code, message := confirmation.IntentFactsComplete(&prepared); !complete {
		t.Fatalf("prepared intent incomplete: %s %s", code, message)
	}
	reloaded, err := store.Get(ctx, session, prepared.IntentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if mismatch := confirmationPrepareReadbackMismatch(prepared, reloaded); mismatch != "" {
		t.Fatalf("readback mismatch=%s prepared=%+v reloaded=%+v", mismatch, prepared, reloaded)
	}
}

// Replaying the same request id returns the identical intent; a changed payload
// under the same request id is a conflict and neither mutates the original.
func TestConfirmationPrepareReplayAndConflict(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	first, err := store.Prepare(ctx, project, taskPrepareInput(session, "rb-replay"))
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	replay, err := store.Prepare(ctx, project, taskPrepareInput(session, "rb-replay"))
	if err != nil || replay.IntentID != first.IntentID || replay.ContentHash != first.ContentHash {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	changed := taskPrepareInput(session, "rb-replay")
	changed.Task.Title = "different payload"
	// SubmittedHash is the caller's canonical argument identity; a genuinely
	// different payload reusing the request id carries a different hash.
	changed.SubmittedHash = strings.Repeat("b", 64)
	if _, err := store.Prepare(ctx, project, changed); !errors.Is(err, confirmation.ErrRequestConflict) && !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err=%v", err)
	}
}

// A submitted hash is required to prove original input identity; without it
// prepare refuses before any durable write.
func TestConfirmationPrepareRejectsUnprovenIdentity(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	in := taskPrepareInput(session, "rb-legacy")
	in.SubmittedHash = ""
	if _, err := store.Prepare(ctx, project, in); !errors.Is(err, confirmation.ErrLegacyReplayUnproven) {
		t.Fatalf("legacy identity err=%v", err)
	}
}

// A complete, committed-intent-id-free prepare classifies as PREPARED (readable,
// never committed): re-show, do not re-prepare.
func TestClassifyPreparationStateCompletePending(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	prepared, err := store.Prepare(ctx, project, taskPrepareInput(session, "cls-complete"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	got, err := store.ClassifyPreparationState(ctx, session, prepared.IntentID)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got.State != confirmation.PreparedPresentationUnavailable || got.ReprepareAllowed {
		t.Fatalf("classification=%+v", got)
	}
}

// A historical half-written intent with no attempt, receipt or business object
// proves nothing committed and may be freshly re-prepared.
func TestClassifyPreparationStateHalfWrittenNothingCommitted(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	half := `{"intentId":"confirm_half","aiSessionId":"` + session + `","version":1,"purpose":"TASK","action":"CREATE_TASK","state":"PENDING","projectId":"` + project + `"}`
	if _, err := store.db.sql.ExecContext(ctx, `INSERT INTO confirmation_intents(intent_id,ai_session_id,project_id,prepare_request_id,prepare_hash,intent_json,submitted_sha256) VALUES(?,?,?,?,?,?,?)`, "confirm_half", session, project, "cls-half", "0", half, strings.Repeat("c", 64)); err != nil {
		t.Fatalf("seed half intent: %v", err)
	}
	got, err := store.ClassifyPreparationState(ctx, session, "confirm_half")
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got.State != confirmation.PreparationIncompleteNotCommitted || !got.ReprepareAllowed || got.Consumed == nil || *got.Consumed {
		t.Fatalf("classification=%+v", got)
	}
}

// A durable non-terminal commit state is consumption evidence: fail closed to
// ATTEMPT_EXISTS_OUTCOME_UNKNOWN, never reprepare.
func TestClassifyPreparationStateAttemptUnknown(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	prepared, err := store.Prepare(ctx, project, taskPrepareInput(session, "cls-attempt"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Advance the durable state the way a committed transition would, without a
	// commit-request ledger row (whose trigger requires paired receipt evidence).
	inFlight := prepared
	inFlight.State = "CONFIRMING"
	raw, err := json.Marshal(inFlight)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.sql.ExecContext(ctx, `UPDATE confirmation_intents SET intent_json=? WHERE intent_id=? AND ai_session_id=?`, string(raw), prepared.IntentID, session); err != nil {
		t.Fatalf("advance state: %v", err)
	}
	got, err := store.ClassifyPreparationState(ctx, session, prepared.IntentID)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	if got.State != confirmation.AttemptExistsOutcomeUnknown || got.ReprepareAllowed || got.Consumed != nil {
		t.Fatalf("classification=%+v", got)
	}
}

// G2/G5: a sub-fact written incomplete must trip the in-transaction readback
// gate, return a typed failure, and roll the whole prepare back so that zero
// intent rows (and therefore zero PENDING / zero bindable Card) remain durable.
func TestConfirmationPrepareIncompleteFactsRollbackLeavesNoVisibleIntent(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	in := taskPrepareInput(session, "rb-incomplete")
	in.Action = "" // immutable fact the completeness gate refuses
	_, err := store.Prepare(ctx, project, in)
	var failure *confirmation.Failure
	if !errors.As(err, &failure) || failure.Code != "CONFIRMATION_PREPARE_FACTS_INCOMPLETE" {
		t.Fatalf("incomplete prepare err=%v want typed CONFIRMATION_PREPARE_FACTS_INCOMPLETE", err)
	}
	var visible, pending int
	if err := store.db.sql.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN json_extract(intent_json,'$.state')='PENDING' THEN 1 ELSE 0 END),0) FROM confirmation_intents WHERE ai_session_id=?`, session).Scan(&visible, &pending); err != nil {
		t.Fatalf("count intents: %v", err)
	}
	if visible != 0 || pending != 0 {
		t.Fatalf("incomplete prepare left durable intent: visible=%d pending=%d", visible, pending)
	}
}

// G5: the readback comparator must detect a divergence in every immutable fact
// the prepare transaction froze, and stay silent only on a true match.
func TestConfirmationPrepareReadbackMismatchDetectsEveryImmutableFact(t *testing.T) {
	store, ctx, session, project := prepareReadbackFixture(t)
	written, err := store.Prepare(ctx, project, taskPrepareInput(session, "rb-mismatch"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if confirmationPrepareReadbackMismatch(written, written) != "" {
		t.Fatal("identical facts reported a mismatch")
	}
	cases := []struct {
		name   string
		mutate func(*confirmation.Intent)
	}{
		{"INTENT_ID", func(i *confirmation.Intent) { i.IntentID += "x" }},
		{"VERSION", func(i *confirmation.Intent) { i.Version++ }},
		{"AI_SESSION_ID", func(i *confirmation.Intent) { i.AISessionID += "x" }},
		{"PROJECT_ID", func(i *confirmation.Intent) { i.ProjectID += "x" }},
		{"PURPOSE", func(i *confirmation.Intent) { i.Purpose = confirmation.Review }},
		{"ACTION", func(i *confirmation.Intent) { i.Action = "OTHER_ACTION" }},
		{"CONTENT_HASH", func(i *confirmation.Intent) { i.ContentHash = strings.Repeat("9", 64) }},
		{"CONTENT", func(i *confirmation.Intent) { i.Content.Task = &confirmation.TaskBody{Title: "drifted"} }},
		{"STEPS", func(i *confirmation.Intent) {
			i.Steps = []confirmation.Step{{Kind: "K", RequestID: "r", State: "PENDING"}}
		}},
	}
	for _, tc := range cases {
		reloaded := written
		tc.mutate(&reloaded)
		if got := confirmationPrepareReadbackMismatch(written, reloaded); got != tc.name {
			t.Fatalf("%s: mismatch=%q want %q", tc.name, got, tc.name)
		}
	}
}
