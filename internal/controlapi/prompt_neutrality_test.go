package controlapi

import (
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestMissingAuthoredInstructionMapsToInvalidArgument(t *testing.T) {
	for _, err := range []error{task.ValidateWorkSpec(task.WorkSpec{Goal: "must not become instruction"}), task.ValidateVerificationSpec(task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "must not become instruction"})} {
		if err == nil || MapError(err).Code != CodeInvalidArgument {
			t.Fatalf("missing authored instruction: %v", err)
		}
	}
}
