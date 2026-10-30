package sqlite

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// A live independently-owned App Server is an active recovery candidate. Boot
// reconciliation must leave the Task in its in-flight phase so the exact
// loaded Thread/Turn can be reattached by the continuity supervisor.
func TestTaskContinuityBootLifecycle(t *testing.T) {
	f, owner, proposal := acceptedExecutionFixture(t, "-continuity-live")
	ctx := context.Background()
	clock := acceptanceTestClock(proposal.CreatedAt.Add(time.Minute))
	_, implementation, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, proposal.ID, "impl-continuity-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-run-id", nil }, clock)
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runs SET thread_id='continuity-thread',turn_id='continuity-turn' WHERE id=?`, implementation.ID); err != nil {
		t.Fatal(err)
	}
	binding := appserver.Binding{ID: "continuity-instance", Epoch: 1, Endpoint: "unix://fixture", EndpointKind: "UNIX_WEBSOCKET", SocketPath: "/tmp/continuity.sock", ProcessStartIdentity: "owned", PID: 1}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, implementation.ID, binding); err != nil {
		t.Fatal(err)
	}
	checkpoint, _ := json.Marshal(map[string]any{"version": 1, "runId": implementation.ID})
	if _, err = f.db.sql.Exec(`UPDATE app_server_run_bindings SET checkpoint_json=? WHERE run_id=?`, string(checkpoint), implementation.ID); err != nil {
		t.Fatal(err)
	}
	if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if status, phase := taskPhaseStatus(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseImplementing {
		t.Fatalf("live App Server run was prematurely reconciled: %s/%s", status, phase)
	}
	if n := recoveryMarkEvents(t, f, owner.ID); n != 0 {
		t.Fatalf("live App Server recovery mark events=%d want 0", n)
	}
}

func TestTaskContinuityBootUncertaintyIsNotHiddenByBinding(t *testing.T) {
	f, owner, proposal := acceptedExecutionFixture(t, "-continuity-cancel")
	ctx := context.Background()
	clock := acceptanceTestClock(proposal.CreatedAt.Add(time.Minute))
	_, implementation, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, proposal.ID, "impl-continuity-cancel-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-run-id", nil }, clock)
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`UPDATE runs SET thread_id='continuity-thread',turn_id='continuity-turn' WHERE id=?`, implementation.ID); err != nil {
		t.Fatal(err)
	}
	binding := appserver.Binding{ID: "continuity-instance", Epoch: 1, Endpoint: "unix://fixture", EndpointKind: "UNIX_WEBSOCKET", SocketPath: "/tmp/continuity.sock", ProcessStartIdentity: "owned", PID: 1}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, implementation.ID, binding); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.sql.Exec(`INSERT INTO execution_cancellations(project_id,request_id,reason,requested_at,runtime_epoch,state,interrupt_state,terminal_observed,owner_type,owner_id,task_id,requested_by_ai_session_id) VALUES(?,?,?,?,?,'CANCEL_REQUESTED','OUTCOME_UNKNOWN',0,'EXECUTION',?,?,?)`, f.project, "continuity-cancel", "unknown", formatTime(testNow), f.db.epoch, proposal.ID, owner.ID, f.session); err != nil {
		t.Fatal(err)
	}
	if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if status, phase := taskPhaseStatus(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseRecoveryRequired {
		t.Fatalf("uncertainty was hidden by binding: %s/%s", status, phase)
	}
}

func TestTaskContinuityBootNonCandidateStillRecovers(t *testing.T) {
	f, owner, proposal := acceptedExecutionFixture(t, "-continuity-no-turn")
	ctx := context.Background()
	clock := acceptanceTestClock(proposal.CreatedAt.Add(time.Minute))
	_, implementation, err := NewExecutionProposalStore(f.db).ConsumeAndCreateRun(ctx, proposal.ID, "impl-continuity-no-turn-"+proposal.ID, clock())
	if err != nil {
		t.Fatal(err)
	}
	runs := run.NewService(NewRunStore(f.db), func() (string, error) { return "unused-run-id", nil }, clock)
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusPreparing); err != nil {
		t.Fatal(err)
	}
	if _, err = NewAgentConversationStore(f.db).BeginRun(ctx, implementation.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = runs.Transition(ctx, implementation.ID, run.StatusRunning); err != nil {
		t.Fatal(err)
	}
	binding := appserver.Binding{ID: "continuity-instance-no-turn", Epoch: 1, Endpoint: "unix://fixture", EndpointKind: "UNIX_WEBSOCKET", SocketPath: "/tmp/continuity.sock", ProcessStartIdentity: "owned", PID: 1}
	if err = NewAgentRuntimeStore(f.db).BindAppServer(ctx, implementation.ID, binding); err != nil {
		t.Fatal(err)
	}
	if err = NewRecoveryStore(f.db).RecoverTaskLifecycles(ctx, testNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if status, phase := taskPhaseStatus(t, f, owner.ID); status != task.StatusActive || phase != task.PhaseRecoveryRequired {
		t.Fatalf("non-candidate was not recovered: %s/%s", status, phase)
	}
}
