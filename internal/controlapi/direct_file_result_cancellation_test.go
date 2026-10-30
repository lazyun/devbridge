package controlapi_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
)

func TestMultiRepositoryDirectFreshResultRequiresActiveGrantWithoutWorker(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "no-mutations"
		if partial {
			name = "partial-effects"
		}
		t.Run(name, func(t *testing.T) {
			f, current := approveDirectFixture(t, false, true)
			if started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil || started.State != "COMPLETED" {
				t.Fatalf("start %+v %v", started, err)
			}
			state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
			if err != nil {
				t.Fatal(err)
			}
			phase := task.PhaseImplementationReady
			if partial {
				mutation := directInput(f, state, current.RepositoryTargets[0])
				read, err := f.facade.FileRead(f.ctx, mutation)
				if err != nil {
					t.Fatal(err)
				}
				mutation.RequestID, mutation.ExpectedDigest, mutation.Content = "partial-before-cancel", read.Digest, "real partial bytes before cancel\n"
				if _, err = f.facade.FileReplace(f.ctx, mutation); err != nil {
					t.Fatal(err)
				}
				phase = task.PhaseImplementing
			}
			canceled, err := directConfirmation(t, f, current, "CANCEL_DIRECT_FILE", state)
			if err != nil || canceled.State != "COMPLETED" {
				t.Fatalf("cancel %+v %v", canceled, err)
			}
			input := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "fresh-after-cancel", Summary: "must not become a result after cancellation", VerificationNotes: "explicit fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
			for _, cold := range []bool{false, true} {
				if cold {
					reopenDirectDeliveryFixture(t, f)
				}
				result, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
				var denied *directfile.Error
				if err == nil || !errors.As(err, &denied) || denied.Code != directfile.NotEnabled {
					t.Fatalf("canceled authority created result %+v: %v", result, err)
				}
				current, err := f.facade.GetTask(f.ctx, f.task)
				if err != nil || current.Status != task.StatusActive || current.Phase != phase || current.CurrentExecutionID != "" {
					t.Fatalf("changed Task %+v %v", current, err)
				}
				closed, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
				if err != nil || closed.State != "CLOSED" || closed.Enabled || closed.WorkflowID != state.WorkflowID {
					t.Fatalf("changed grant %+v %v", closed, err)
				}
				if got, err := f.facade.GetFileImplementationResult(f.ctx, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: input.TaskDefinitionRevisionID, WorkflowID: state.WorkflowID}); err == nil {
					t.Fatalf("unexpected result %+v", got)
				}
			}
		})
	}
}

func TestMultiRepositoryDirectUnclaimedCompletedResultReplaySurvivesClosedGrant(t *testing.T) {
	f, current := approveDirectFixture(t, false, true)
	if started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{}); err != nil || started.State != "COMPLETED" {
		t.Fatalf("start %+v %v", started, err)
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	input := directfile.ImplementationResultInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: state.WorkflowID, RequestID: "real-no-change-result", Summary: "No file mutations; legitimate no-change report before separate acceptance", VerificationNotes: "explicit fixture waiver", ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}
	result, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
	if err != nil || result.Accepted || result.VerificationStatus != "NOT_RUN" {
		t.Fatalf("active result %+v %v", result, err)
	}
	// Facade-only private workspace fields are deliberately absent from persisted
	// receipt JSON; compare the complete public/persisted result shape.
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var persisted directfile.ImplementationResult
	if err = json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	result = persisted
	for _, cold := range []bool{false, true} {
		if cold {
			reopenDirectDeliveryFixture(t, f)
		}
		replay, err := f.facade.SubmitFileImplementationResult(f.ctx, input)
		if err != nil || !replay.Replay {
			t.Fatalf("historical result %+v %v", replay, err)
		}
		replay.Replay = false
		if !reflect.DeepEqual(replay, result) {
			t.Fatalf("changed historical facts %+v", replay)
		}
		late := input
		late.RequestID = "different-after-completion"
		if _, err = f.facade.SubmitFileImplementationResult(f.ctx, late); err == nil {
			t.Fatal("closed grant accepted a new result")
		}
		current, err := f.facade.GetTask(f.ctx, f.task)
		if err != nil || current.Phase != task.PhaseAcceptancePending || current.Status != task.StatusActive {
			t.Fatalf("result bypassed acceptance %+v %v", current, err)
		}
	}
}
