package execution

import (
	"context"
	"github.com/lazyun/devbridge/internal/run"
	"reflect"
	"testing"
)

func TestNativeTextCannotDeclareCoreScopeConflict(t *testing.T) {
	c, _, tasks, _, session, _ := successfulCoordinator()
	before := tasks.value.WorkSpec
	message := `{"resultDisposition":"SCOPE_CONFLICT","summary":"The approved security boundary excludes the required credential flow."}`
	session.message = &message
	value, err := c.StartRun(context.Background(), "task-1")
	if err != nil || value.Status != run.StatusSucceeded || value.Failure != nil || value.Result.AgentClaim.Message != message {
		t.Fatalf("conflict=%+v err=%v", value, err)
	}
	if !reflect.DeepEqual(before, tasks.value.WorkSpec) {
		t.Fatal("conflict rewrote definition")
	}
	if session.closed != 1 {
		t.Fatal("backend not closed")
	}
}
func TestScopeConflictResultIsStructured(t *testing.T) {
	for _, raw := range []string{"SCOPE_CONFLICT: reason", "complete", `{"resultDisposition":"SCOPE_CONFLICT"}`, `{"resultDisposition":"UNKNOWN","summary":"reason"}`, `{"resultDisposition":"COMPLETED","summary":"ok","extra":true}`} {
		if _, err := parseImplementationReport(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	result, err := parseImplementationReport(`{"resultDisposition":"COMPLETED","summary":"SCOPE_CONFLICT: is ordinary text"}`)
	if err != nil || result.ResultDisposition != "COMPLETED" {
		t.Fatalf("%+v %v", result, err)
	}
}
