package taskproposal_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskproposal"
)

func explicitSpecs() (task.WorkSpec, task.VerificationSpec) {
	return task.WorkSpec{
			Goal:             "Implement the confirmed change",
			Constraints:      []string{"preserve compatibility"},
			CodexInstruction: "Edit exactly the requested implementation and preserve compatibility.",
			ExecutionScope:   task.ExecutionScope{Kind: task.ExecutionScopeRepository},
		}, task.VerificationSpec{
			Mode:               task.VerificationModeRun,
			Scope:              "changed Go packages",
			Checks:             []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "go test ./...", Required: true}},
			AcceptanceCriteria: []string{"all tests pass"},
			CodexInstruction:   "Run go test ./... and report the result without changing files.",
		}
}

func TestHistoricalStructuredSpecsRemainClonedAndReadOnly(t *testing.T) {
	work, verification := explicitSpecs()
	value := taskproposal.Proposal{ID: "historical", AISessionID: "session-a", ProjectID: "project-a", Status: taskproposal.StatusConfirmed, WorkSpec: work, VerificationSpec: verification}
	service := historicalService(t, value)
	first, err := service.Get(context.Background(), "session-a", value.ID)
	if err != nil {
		t.Fatal(err)
	}
	first.WorkSpec.Constraints[0] = "mutated"
	first.VerificationSpec.Checks[0].Command = "false"
	second, err := service.Get(context.Background(), "session-a", value.ID)
	if err != nil || second.WorkSpec.Constraints[0] != "preserve compatibility" || second.VerificationSpec.Checks[0].Command != "go test ./..." {
		t.Fatalf("historical definition changed: %+v err=%v", second, err)
	}
	if _, err := service.Apply(context.Background(), "session-a", value.ID); !errors.Is(err, taskproposal.ErrRetired) {
		t.Fatalf("historical definition became executable: %v", err)
	}
}
