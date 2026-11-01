package controlapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
)

type adoptionQualificationBusiness struct {
	wp.Store
	firstErr, writerErr error
	calls               int
}

func (*adoptionQualificationBusiness) ReadAdoption(context.Context, fe.ReadIdentity) (fe.AdoptionReceipt, error) {
	return fe.AdoptionReceipt{}, fe.ErrMissing
}
func (*adoptionQualificationBusiness) ReadAdoptionRequired(context.Context, fe.ReadIdentity) (bool, error) {
	return true, nil
}
func (s *adoptionQualificationBusiness) CommitAdoption(_ context.Context, _ fe.ReadIdentity, ref rs.PlatformReference) (fe.AdoptionReceipt, error) {
	s.calls++
	if ref == (rs.PlatformReference{}) {
		return fe.AdoptionReceipt{}, s.firstErr
	}
	return fe.AdoptionReceipt{}, s.writerErr
}

type adoptionQualificationResolver struct {
	err   error
	calls int
}

func (r *adoptionQualificationResolver) ResolvePlatformReference(context.Context, codexconfig.BackendSelection, string) (rs.PlatformReference, error) {
	r.calls++
	return rs.PlatformReference{QualificationID: "isolated-qualification"}, r.err
}

func TestFormalAdoptionQualificationNoEffectClassificationIsNarrow(t *testing.T) {
	missing := runtimeplatform.ErrQualificationMissing
	for _, tc := range []struct {
		name                            string
		firstErr, resolveErr, writerErr error
		definite                        bool
		commits, resolves               int
	}{
		{name: "typed-missing", firstErr: fe.ErrQualificationRequired, resolveErr: missing, definite: true, commits: 1, resolves: 1},
		{name: "wrapped-typed-missing", firstErr: fe.ErrQualificationRequired, resolveErr: fmt.Errorf("exact missing mapping: %w", missing), definite: true, commits: 1, resolves: 1},
		{name: "message-is-not-proof", firstErr: fe.ErrQualificationRequired, resolveErr: errors.New(missing.Error()), commits: 1, resolves: 1},
		{name: "context-canceled", firstErr: fe.ErrQualificationRequired, resolveErr: context.Canceled, commits: 1, resolves: 1},
		{name: "qualification-invalidated", firstErr: fe.ErrQualificationRequired, resolveErr: runtimeplatform.ErrQualificationInvalidated, commits: 1, resolves: 1},
		{name: "identity-mismatch", firstErr: fe.ErrQualificationRequired, resolveErr: runtimeplatform.ErrIdentityMismatch, commits: 1, resolves: 1},
		{name: "first-writer-typed-missing", firstErr: missing, commits: 1},
		{name: "second-writer-typed-missing", firstErr: fe.ErrQualificationRequired, writerErr: missing, commits: 2, resolves: 1},
		{name: "second-writer-uncertain", firstErr: fe.ErrQualificationRequired, writerErr: errors.New("commit outcome unavailable"), commits: 2, resolves: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			business := &adoptionQualificationBusiness{firstErr: tc.firstErr, writerErr: tc.writerErr}
			resolver := &adoptionQualificationResolver{err: tc.resolveErr}
			f := &Facade{deps: Dependencies{Confirmations: formalReplayConfirmation{}, WorkflowProgression: business, RuntimeQualification: resolver}}
			intent := confirmation.Intent{AISessionID: "session", IntentID: "intent", Version: 1, ContentHash: "original-content", Action: "START_APPROVED", Commit: &confirmation.CommitInput{RequestID: "commit", BackendSelection: &codexconfig.BackendSelection{ConfigID: "isolated-backend"}}, Content: confirmation.PrepareInput{Execution: &confirmation.ExecutionBody{TaskID: "isolated-task", ExpectedTarget: &wp.TaskExecutionExpectation{}}}}
			variant := confirmation.Variant{ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: []workset.Target{{MemberID: "isolated-member"}}}
			step, err := f.performConfirmationStep(context.Background(), intent, variant, confirmation.Step{Kind: "START_IMPLEMENTATION", RequestID: "isolated-subrequest", State: "UNKNOWN"})
			cause := tc.resolveErr
			if cause == nil {
				cause = tc.writerErr
			}
			if cause == nil {
				cause = tc.firstErr
			}
			if !errors.Is(err, cause) || business.calls != tc.commits || resolver.calls != tc.resolves {
				t.Fatalf("changed boundary or originating failure: err=%v commits=%d resolves=%d", err, business.calls, resolver.calls)
			}
			code, definite := confirmationDefiniteRejection(err, step)
			if definite != tc.definite {
				t.Fatalf("definite=%v want=%v step=%+v err=%v", definite, tc.definite, step, err)
			}
			if tc.definite && (step.Effect != "NOT_STARTED" || code != missing.Error()) {
				t.Fatalf("lost no-effect code: %s %+v", code, step)
			}
			if !tc.definite && step.Effect == "NOT_STARTED" {
				t.Fatalf("uncertain failure became no-effect: %+v", step)
			}
		})
	}
}
