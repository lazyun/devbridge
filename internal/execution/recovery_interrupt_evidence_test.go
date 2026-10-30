package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/executioncancel"
)

type evidenceInterruptClient struct {
	err          error
	cancel       context.CancelFunc
	calls        int
	thread, turn string
}

func (c *evidenceInterruptClient) InterruptTurn(_ context.Context, thread, turn string) error {
	c.calls++
	c.thread, c.turn = thread, turn
	c.cancel()
	return c.err
}

type evidenceInterruptStore struct {
	err               error
	outcome, recovery string
	input             executioncancel.Input
	ctxErr            error
}

func (s *evidenceInterruptStore) RecordCancelInterrupt(ctx context.Context, recovery string, in executioncancel.Input, outcome string) (executioncancel.Receipt, error) {
	s.ctxErr, s.outcome, s.recovery, s.input = ctx.Err(), outcome, recovery, in
	return executioncancel.Receipt{BackendInterruptState: outcome}, s.err
}

func TestRecoveryInterruptPersistsActualOutcomeAfterCallerDisconnect(t *testing.T) {
	rpcFailure, storeFailure := errors.New("response lost"), errors.New("receipt write failed")
	for _, tc := range []struct {
		name             string
		rpcErr, storeErr error
		outcome          string
	}{
		{"ack", nil, nil, "ACKNOWLEDGED"},
		{"lost", rpcFailure, nil, "OUTCOME_UNKNOWN"},
		{"ack-write-failed", nil, storeFailure, "ACKNOWLEDGED"},
		{"lost-write-failed", rpcFailure, storeFailure, "OUTCOME_UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &evidenceInterruptClient{err: tc.rpcErr, cancel: cancel}
			store := &evidenceInterruptStore{err: tc.storeErr}
			in := executioncancel.Input{ProjectID: "project", RequestID: "request", RunID: "source"}
			receipt, err := interruptRecoveryChild(ctx, client, store, "recovery", in, "child-thread", "child-turn")
			if client.calls != 1 || client.thread != "child-thread" || client.turn != "child-turn" || store.ctxErr != nil || store.recovery != "recovery" || store.input != in || store.outcome != tc.outcome || receipt.BackendInterruptState != tc.outcome {
				t.Fatalf("inexact or lost transport outcome: client=%+v store=%+v receipt=%+v", client, store, receipt)
			}
			for _, cause := range []error{tc.rpcErr, tc.storeErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Fatalf("dropped failure %v: %v", cause, err)
				}
			}
			if tc.rpcErr == nil && tc.storeErr == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
}
