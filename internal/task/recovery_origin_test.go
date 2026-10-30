package task

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRecoveryOriginCannotChangeCanonicalTaskPayload(t *testing.T) {
	owner := Task{ID: "task", Phase: PhaseRecoveryRequired, Status: StatusActive}
	before, _ := json.Marshal(owner)
	owner.RecoveryOrigin = &RecoveryOrigin{Phase: PhaseVerifying, SourceEventID: "original-recovery-event"}
	after, _ := json.Marshal(owner)
	if !bytes.Equal(before, after) {
		t.Fatal("Display origin entered canonical Task payload")
	}
	cloned := clone(owner)
	cloned.RecoveryOrigin.Phase = PhaseImplementing
	if owner.RecoveryOrigin.Phase != PhaseVerifying {
		t.Fatal("Clone shares mutable origin")
	}
}
