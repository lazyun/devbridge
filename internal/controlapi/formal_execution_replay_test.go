package controlapi

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

type formalReplayConfirmation struct{ confirmation.Store }

func (formalReplayConfirmation) ReadSubmission(context.Context, confirmation.ReadInput) (confirmation.Submission, error) {
	return confirmation.Submission{Recorded: true, Outcome: confirmation.Consumed, ReceiptRequestID: "commit", PayloadSHA256: "original-wire-hash"}, nil
}

type formalReplayBusiness struct {
	wp.Store
	finalErr, progressErr error
	receipt               formalexecution.Receipt
	progressReads, writes int
	platform              formalexecution.PlatformReference
}

func (s *formalReplayBusiness) ReadApproveAndStart(context.Context, formalexecution.ReadIdentity) (formalexecution.Receipt, error) {
	return s.receipt, s.finalErr
}
func (s *formalReplayBusiness) ReadApproveAndStartRequired(context.Context, formalexecution.ReadIdentity) (bool, error) {
	return true, nil
}
func (s *formalReplayBusiness) ReadApproveAndStartProgress(context.Context, formalexecution.ReadIdentity) (formalexecution.Progress, error) {
	s.progressReads++
	return formalexecution.Progress{}, s.progressErr
}
func (s *formalReplayBusiness) CommitApproveAndStart(_ context.Context, _ formalexecution.ReadIdentity, p formalexecution.PlatformReference) (formalexecution.Receipt, error) {
	s.writes++
	s.platform = p
	return s.receipt, nil
}

type formalReplaySelection struct {
	runtimeselection.Store
	binds         int
	err           error
	replayPresent bool
}

func (s *formalReplaySelection) ReadSource(_ context.Context, id string) (runtimeselection.ApprovedSource, error) {
	if id != "original-source" {
		return runtimeselection.ApprovedSource{}, errors.New("wrong original source")
	}
	return runtimeselection.ApprovedSource{SourceID: id, SourcePayloadHash: "original-source-hash"}, nil
}
func (s *formalReplaySelection) MaterializeBinding(context.Context, runtimeselection.BindingInput) (runtimeselection.Binding, error) {
	s.binds++
	return runtimeselection.Binding{}, s.err
}
func (s *formalReplaySelection) ReplayBinding(context.Context, runtimeselection.BindingInput) (runtimeselection.Binding, error) {
	if s.replayPresent {
		return runtimeselection.Binding{}, nil
	}
	return runtimeselection.Binding{}, runtimeselection.ErrBindingMissing
}
func (s *formalReplaySelection) ReadBindingLineageVersion(context.Context, string) (int, error) {
	return 1, nil
}

type forbiddenQualificationRead struct{ t *testing.T }

func (s forbiddenQualificationRead) ResolvePlatformReference(context.Context, codexconfig.BackendSelection, string) (runtimeselection.PlatformReference, error) {
	s.t.Fatal("historical receipt/prefix consulted live platform")
	return runtimeselection.PlatformReference{}, nil
}

func TestFormalExecutionFinalAndPrefixReplayPrecedeLiveQualification(t *testing.T) {
	for _, name := range []string{"final", "partial", "corrupt-final", "binding-unavailable", "completed", "completed-binding-missing"} {
		t.Run(name, func(t *testing.T) {
			business := &formalReplayBusiness{}
			business.receipt.T1.Identity.SourceOriginID = "original-source"
			business.receipt.Archive.RevisionID = "original-revision"
			selection := &formalReplaySelection{}
			switch name {
			case "partial":
				business.finalErr = formalexecution.ErrMissing
			case "corrupt-final":
				business.finalErr = formalexecution.ErrCorrupt
			case "binding-unavailable":
				selection.err = runtimeselection.ErrBindingConflict
			}
			f := &Facade{deps: Dependencies{Confirmations: formalReplayConfirmation{}, WorkflowProgression: business, RuntimeSelections: selection, RuntimeQualification: forbiddenQualificationRead{t}}}
			intent := confirmation.Intent{AISessionID: "session", IntentID: "intent", Version: 1, ContentHash: "original-content", Commit: &confirmation.CommitInput{RequestID: "commit"}}
			if name == "completed" || name == "completed-binding-missing" {
				intent.State = "COMPLETED"
				selection.replayPresent = name == "completed"
			}
			receipt, err := f.finishFormalExecution(context.Background(), intent)
			if name == "corrupt-final" {
				if !errors.Is(err, formalexecution.ErrCorrupt) || business.progressReads != 0 || business.writes != 0 || selection.binds != 0 {
					t.Fatalf("corrupt final fell back: %+v %v", business, err)
				}
				return
			}
			if receipt.Archive.RevisionID != "original-revision" {
				t.Fatal("lost original business receipt")
			}
			if name == "completed-binding-missing" {
				if !errors.Is(err, runtimeselection.ErrBindingMissing) {
					t.Fatal(err)
				}
			} else if name == "binding-unavailable" {
				if !errors.Is(err, runtimeselection.ErrBindingConflict) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			want := 0
			if name == "partial" {
				want = 1
			}
			wantBinds := 1
			if intent.State == "COMPLETED" {
				wantBinds = 0
			}
			if business.progressReads != want || business.writes != want || business.platform != (formalexecution.PlatformReference{}) || selection.binds != wantBinds {
				t.Fatalf("wrong replay effects: business=%+v binds=%d", business, selection.binds)
			}
		})
	}
}
