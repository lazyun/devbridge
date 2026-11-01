package controlapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
)

type cancellationRefusalAuthority struct {
	facadeFileAuthority
	err   error
	state directfile.State
	calls int
}

func (a *cancellationRefusalAuthority) StartTaskAuthorization(context.Context, directfile.StartInput) (directfile.State, error) {
	a.calls++
	return a.state, a.err
}

// Only the authority's dedicated pre-effect refusal may release a consumed
// cancellation as rejected. A generic/transport/storage error remains unknown,
// even when its text resembles that refusal or a different action returns it.
func TestDirectFileCancellationNoEffectClassificationIsNarrow(t *testing.T) {
	refusal := directfile.Failure(directfile.CancellationMutationUnresolved, "unresolved file mutation receipt requires recovery before cancellation")
	for _, tc := range []struct {
		name, action string
		err          error
		state        directfile.State
		definite     bool
	}{
		{name: "typed-cancel", action: "CANCEL_DIRECT_FILE", err: refusal, definite: true},
		{name: "wrapped-typed-cancel", action: "CANCEL_DIRECT_FILE", err: fmt.Errorf("wrapped: %w", refusal), definite: true},
		{name: "ordinary-file-failure", action: "CANCEL_DIRECT_FILE", err: directfile.Failure(directfile.OperationFailed, refusal.Error())},
		{name: "message-is-not-proof", action: "CANCEL_DIRECT_FILE", err: errors.New(refusal.Error())},
		{name: "storage-uncertain", action: "CANCEL_DIRECT_FILE", err: errors.New("commit outcome unavailable")},
		{name: "canceled-context", action: "CANCEL_DIRECT_FILE", err: context.Canceled},
		{name: "start-not-cancel", action: "START_DIRECT_FILE", err: refusal},
		{name: "recover-not-cancel", action: "RECOVER_DIRECT_FILE", err: refusal},
		{name: "reserved-state-is-not-no-effect", action: "CANCEL_DIRECT_FILE", err: refusal, state: directfile.State{WorkflowID: "reserved-workflow"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &cancellationRefusalAuthority{err: tc.err, state: tc.state}
			f := &Facade{deps: Dependencies{DirectFiles: directfile.NewService(a)}}
			intent := confirmation.Intent{AISessionID: "isolated-session", IntentID: "isolated-confirmation", Action: tc.action, Commit: &confirmation.CommitInput{}, Content: confirmation.PrepareInput{Execution: &confirmation.ExecutionBody{TaskID: "isolated-task", TaskDefinitionRevisionID: "isolated-revision", DecisionID: "isolated-decision", WorkflowID: "isolated-workflow"}}}
			step, err := f.performConfirmationStep(context.Background(), intent, confirmation.Variant{}, confirmation.Step{Kind: tc.action, RequestID: "isolated-subrequest", State: "UNKNOWN"})
			if a.calls != 1 || !errors.Is(err, tc.err) {
				t.Fatalf("lost exact authority failure or repeated call: calls=%d err=%v", a.calls, err)
			}
			code, definite := confirmationDefiniteRejection(err, step)
			if definite != tc.definite {
				t.Fatalf("definite=%v want=%v; step=%+v err=%v", definite, tc.definite, step, err)
			}
			if tc.definite && (step.Effect != "NOT_STARTED" || code != directfile.CancellationMutationUnresolved) {
				t.Fatalf("incorrect no-effect classification: code=%s step=%+v", code, step)
			}
			if !tc.definite && step.Effect == "NOT_STARTED" {
				t.Fatalf("uncertain authority failure became no-effect: %+v", step)
			}
		})
	}
}
