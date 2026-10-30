package controlapi_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

func TestMultiRepositoryDirectAcceptanceMissingExactResultIsNotFound(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	if started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil || started.State != "COMPLETED" {
		t.Fatalf("start %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "actual-unaccepted-result", Summary: "No changes, await separate acceptance", VerificationNotes: "explicit isolated fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration})
	if err != nil {
		t.Fatal(err)
	}
	scope, _ := aisession.CallerScopeFromContext(f.ctx)
	ref := aiprojectselection.ClientBindingReference{CallerScope: scope}
	other, err := f.plane.AIContext.InitializeSession(f.ctx, ref, "foreign-acceptance-session", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.AIContext.BindProject(f.ctx, ref, other.AISessionID, f.project, "bind-foreign-acceptance"); err != nil {
		t.Fatal(err)
	}
	if other.AISessionID == f.session {
		t.Fatal("test needs distinct real sessions")
	}
	input := directfile.ImplementationAcceptInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, ResultID: result.ID, RequestID: "correct-acceptance", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	for _, kind := range []string{"foreign-session", "revision", "workflow", "result"} {
		t.Run(kind, func(t *testing.T) {
			bad := input
			bad.RequestID = "invalid-accept-" + kind
			switch kind {
			case "foreign-session":
				bad.AISessionID = other.AISessionID
			case "revision":
				bad.TaskDefinitionRevisionID = "not-the-result-revision"
			case "workflow":
				bad.WorkflowID = f.area
			case "result":
				bad.ResultID = f.review
			}
			out, err := f.facade.AcceptFileImplementationResult(f.ctx, bad)
			var failure *controlapi.Error
			if !errors.As(err, &failure) || failure.Code != controlapi.CodeNotFound {
				t.Errorf("missing exact result must be NOT_FOUND, not internal error: %+v %v", out, err)
			}
			if !reflect.DeepEqual(out, directfile.ImplementationResult{}) {
				t.Fatalf("foreign identity leaked/accepted result %+v", out)
			}
			read, err := f.facade.GetFileImplementationResult(f.ctx, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: input.TaskDefinitionRevisionID, WorkflowID: state.WorkflowID})
			if err != nil || read.Accepted || read.ID != result.ID {
				t.Fatalf("changed result %+v %v", read, err)
			}
			owner, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil || owner.Status != task.StatusActive || owner.Phase != task.PhaseAcceptancePending {
				t.Fatalf("changed owner %+v %v", owner, err)
			}
		})
	}
	accepted, err := f.facade.AcceptFileImplementationResult(f.ctx, input)
	if err != nil || !accepted.Accepted || accepted.CommitError != "" || accepted.VerificationStatus != "NOT_RUN" {
		t.Fatalf("correct separate acceptance %+v %v", accepted, err)
	}
}
