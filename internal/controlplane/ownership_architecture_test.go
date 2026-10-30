package controlplane_test

import (
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workspace"
	"reflect"
	"testing"
)

func TestActiveOwnershipTypesCannotSelectLegacyContextOrReviewTask(t *testing.T) {
	for _, test := range []struct {
		typ   reflect.Type
		field string
	}{{reflect.TypeOf(task.Task{}), "AgentContext"}, {reflect.TypeOf(task.CreateInput{}), "AgentContext"}, {reflect.TypeOf(workspace.Workspace{}), "ThreadID"}, {reflect.TypeOf(scopereview.StartInput{}), "TaskID"}} {
		if _, ok := test.typ.FieldByName(test.field); ok {
			t.Fatalf("active %s exposes legacy %s", test.typ, test.field)
		}
	}
	for _, field := range []string{"ProjectID", "FeatureAreaID", "OperationKind", "OperationID"} {
		if _, ok := reflect.TypeOf(run.Run{}).FieldByName(field); !ok {
			t.Fatalf("Run lacks explicit %s", field)
		}
	}
	for _, method := range []string{"AttachThread", "ReplaceThread"} {
		if _, ok := reflect.TypeOf((*workspace.Service)(nil)).MethodByName(method); ok {
			t.Fatalf("Workspace owns context mutation %s", method)
		}
	}
	for _, method := range []string{"StartSplit", "CreateSplitChild", "CompleteSplit"} {
		if _, ok := reflect.TypeOf((*featurearea.Service)(nil)).MethodByName(method); ok {
			t.Fatalf("non-atomic lifecycle method %s", method)
		}
	}
	if featurearea.IsUsable(featurearea.FeatureArea{Status: featurearea.Status("SPLITTING")}) {
		t.Fatal("legacy split state became usable")
	}
}
