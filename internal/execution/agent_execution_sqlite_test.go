package execution_test

import (
	"context"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"testing"

	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
)

// These cases use real durable admission, then supply a stale or corrupt
// handoff snapshot. No recording Codex session may even be constructed.
func TestDurableAgentExecutionRejectsCorruptHandoffBeforeCodex(t *testing.T) {
	cases := map[string]func(*run.Run){
		"missing execution":    func(r *run.Run) { r.AgentExecutionID = "" },
		"wrong execution":      func(r *run.Run) { r.AgentExecutionID = "not-the-durable-execution" },
		"wrong task":           func(r *run.Run) { r.TaskID = "other-task" },
		"wrong project":        func(r *run.Run) { r.ProjectID = "other-project" },
		"wrong feature area":   func(r *run.Run) { r.FeatureAreaID = "other-area" },
		"wrong semantic owner": func(r *run.Run) { r.OperationID = "other-owner" },
		"stale epoch":          func(r *run.Run) { r.RuntimeEpoch = "previous-epoch" },
	}
	for name, corrupt := range cases {
		t.Run(name, func(t *testing.T) {
			f := newAuthorizedPlanPanicFixture(t)
			defer f.db.Close()
			defer f.supervisor.Shutdown()
			ctx := context.Background()
			reserved := f.implementationRun
			if reserved.AgentExecutionID != f.proposalValue.ID {
				t.Fatalf("admission omitted exact universal identity: %+v", reserved)
			}
			prepared, err := f.coordinator.PrepareReservedRun(ctx, reserved.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
			if err != nil {
				t.Fatal(err)
			}
			corrupt(&prepared.Run)
			if _, err := f.coordinator.ExecuteRun(ctx, prepared, nil); err == nil {
				t.Fatal("corrupt ownership authorized backend")
			}
			stored, err := f.runs.Get(ctx, reserved.ID)
			if err != nil || stored.Status != run.StatusPreparing || stored.Failure != nil {
				t.Fatalf("corrupt ownership mutated durable Run: %+v %v", stored, err)
			}
			select {
			case <-f.factory.created:
				t.Fatal("Codex factory invoked for invalid ownership")
			default:
			}
		})
	}
}

// Even a custom conversation runtime cannot substitute a different outgoing
// resume thread while retaining the real Run and Execution identities.
func TestDurableAgentExecutionRejectsWrongOutgoingResumeThread(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	ctx := context.Background()
	reserved := f.implementationRun
	prepared, err := f.coordinator.PrepareReservedRun(ctx, reserved.ID, exec.Instruction{CodexInstruction: f.work.CodexInstruction, ExecutionScope: f.work.ExecutionScope})
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator.Conversations = wrongResumeConversation{f.coordinator.Conversations}
	backend := &protocolSession{}
	f.coordinator.Codex = protocolFactory{backend}
	if _, err = f.coordinator.ExecuteRun(ctx, prepared, nil); err == nil {
		t.Fatal("wrong resume thread authorized")
	}
	if backend.starts != 0 || backend.resumes != 0 || backend.turns != 0 {
		t.Fatalf("wrong outgoing thread reached backend: %+v", backend)
	}
}

type wrongResumeConversation struct{ agentconversation.Runtime }

func (r wrongResumeConversation) BeginRun(ctx context.Context, id string) (agentconversation.Conversation, error) {
	value, err := r.Runtime.BeginRun(ctx, id)
	value.BackendThreadID = "wrong-outgoing-thread"
	return value, err
}
