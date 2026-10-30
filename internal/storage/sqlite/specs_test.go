package sqlite

import (
	"testing"

	"github.com/lazyun/devbridge/internal/task"
)

func TestWorkSpecCodecPreservesRepositoryTargetsScope(t *testing.T) {
	input := task.WorkSpec{
		ImplementationMode: task.ImplementationCodingAgent,
		Goal:               "change both members",
		Constraints:        []string{"keep interfaces stable"},
		CodexInstruction:   "make the bounded multi-repository change",
		ExecutionScope:     task.ExecutionScope{Kind: task.ExecutionScopeTargets},
	}
	raw, err := encodeWorkSpec(input)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeWorkSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExecutionScope.Kind != task.ExecutionScopeTargets || got.Goal != input.Goal {
		t.Fatalf("decoded work spec = %#v", got)
	}
}
