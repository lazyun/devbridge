package sqlite

import (
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/run"
)

func TestNativeResultStateRequiresExactNativeTerminalObservation(t *testing.T) {
	f, ctx, impl, verify := implementationAnalysisFixture(t)
	runs, conversations := NewRunStore(f.db), NewAgentConversationStore(f.db)
	completed, err := runs.Get(ctx, impl.ID)
	if err != nil || completed.NativeResultState != "RECEIVED" {
		t.Fatalf("completed implementation: %s %v", completed.NativeResultState, err)
	}
	setAgentRunState(t, f.db, verify.ID, run.StatusPreparing, "", "")
	if _, err = conversations.BeginRun(ctx, verify.ID); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, verify.ID, run.StatusRunning, "analysis-thread", "")
	if err = conversations.MarkTurnPending(ctx, verify.ID); err != nil {
		t.Fatal(err)
	}
	setAgentRunState(t, f.db, verify.ID, run.StatusRunning, "analysis-thread", "verification-turn")
	// A local terminal status and convincing chat text do not prove receipt of
	// this exact native turn/completed; the implementation's receipt cannot be borrowed.
	for _, status := range []run.Status{run.StatusSucceeded, run.StatusFailed, run.StatusInfrastructureFailed, run.StatusCanceled} {
		if _, err = f.db.sql.Exec(`UPDATE runs SET status=?,result_text='All work is finished' WHERE id=?`, status, verify.ID); err != nil {
			t.Fatal(err)
		}
		v, e := runs.Get(ctx, verify.ID)
		if e != nil || v.NativeResultState != "UNKNOWN" {
			t.Fatalf("local %s inferred native result: %s %v", status, v.NativeResultState, e)
		}
	}
	setAgentRunState(t, f.db, verify.ID, run.StatusFailed, "analysis-thread", "verification-turn")
	if err = conversations.FinishRun(ctx, verify.ID, agentconversation.KnownAdvance, "verification-turn", true); err != nil {
		t.Fatal(err)
	}
	v, err := runs.Get(ctx, verify.ID)
	if err != nil || v.Status != run.StatusFailed || v.NativeResultState != "RECEIVED" {
		t.Fatalf("known failed/canceled result lost: %+v %v", v, err)
	}
}

func TestNativeResultStateNoDispatchNeedsNoResultRecovery(t *testing.T) {
	f, ctx, _, verify := implementationAnalysisFixture(t)
	setAgentRunState(t, f.db, verify.ID, run.StatusPreparing, "", "")
	c := NewAgentConversationStore(f.db)
	if _, err := c.BeginRun(ctx, verify.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishRun(ctx, verify.ID, agentconversation.NoAdvance, "", true); err != nil {
		t.Fatal(err)
	}
	v, err := NewRunStore(f.db).Get(ctx, verify.ID)
	if err != nil || v.NativeResultState != "NOT_STARTED" {
		t.Fatalf("no dispatch: %s %v", v.NativeResultState, err)
	}
}
