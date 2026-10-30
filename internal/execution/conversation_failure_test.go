package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
)

type faultConversationRuntime struct {
	recordingConversationRuntime
	bindErr, pendingErr, finishErr error
}

func (r *faultConversationRuntime) BindThread(ctx context.Context, id, thread string) error {
	if r.bindErr != nil {
		return r.bindErr
	}
	return r.recordingConversationRuntime.BindThread(ctx, id, thread)
}
func (r *faultConversationRuntime) MarkTurnPending(ctx context.Context, id string) error {
	if r.pendingErr != nil {
		return r.pendingErr
	}
	return r.recordingConversationRuntime.MarkTurnPending(ctx, id)
}
func (r *faultConversationRuntime) FinishRun(ctx context.Context, id string, a agentconversation.Advance, exit string, q bool) error {
	_ = r.recordingConversationRuntime.FinishRun(ctx, id, a, exit, q)
	return r.finishErr
}

func TestFeatureAreaConversationFailureBoundaries(t *testing.T) {
	boom := errors.New("synthetic runtime failure")
	cases := []struct {
		name     string
		setup    func(*Coordinator, *fakeSession, *faultConversationRuntime)
		want     agentconversation.Advance
		quiesced bool
		turns    int
	}{
		{"factory failure", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { c.Codex = fakeFactory{err: boom} }, agentconversation.NoAdvance, true, 0},
		{"thread start failure", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { s.threadErr = boom }, agentconversation.Unknown, true, 0},
		{"thread established persistence fails", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { r.bindErr = boom }, agentconversation.Unknown, true, 0},
		{"resume failure", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) {
			r.conversation.BackendThreadID = "established-thread"
			s.threadErr = boom
		}, agentconversation.NoAdvance, true, 0},
		{"pending persistence fails", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { r.pendingErr = boom }, agentconversation.NoAdvance, true, 0},
		{"turn outcome unknown despite known start id", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { s.turnErr = boom }, agentconversation.Unknown, true, 1},
		{"cancellation unknown", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { s.turnErr = context.Canceled }, agentconversation.Unknown, true, 1},
		{"runtime close not proven", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) { s.closeErr = boom }, agentconversation.Unknown, false, 1},
		{"terminal backend failure", func(c *Coordinator, s *fakeSession, r *faultConversationRuntime) {
			s.turnErr = &codex.TurnError{ThreadID: "thread-1", TurnID: "turn-1", Status: "failed", Message: "backend completed with failure"}
		}, agentconversation.KnownAdvance, true, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, tasks, trees, session, _ := successfulCoordinator()
			trees.value.Path = t.TempDir()
			tasks.value.FeatureAreaID = "feature-a"
			runtime := &faultConversationRuntime{recordingConversationRuntime: recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation-a", FeatureAreaID: "feature-a", ContinuationSafe: true}}}
			c.Conversations = runtime
			tc.setup(c, session, runtime)
			prepared, prepareErr := c.PrepareRun(context.Background(), tasks.value.ID)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			_, err := executeDirectFeatureAreaRun(context.Background(), c, prepared)
			if err == nil {
				t.Fatal("injected failure was lost")
			}
			if runtime.finish != 1 || runtime.advance != tc.want || runtime.quiesced != tc.quiesced || session.turn != tc.turns {
				t.Fatalf("advance=%s quiesced=%v finish=%d turns=%d error=%v", runtime.advance, runtime.quiesced, runtime.finish, session.turn, err)
			}
			if tc.want == agentconversation.KnownAdvance && runtime.exit != "turn-1" {
				t.Fatalf("known exit=%q", runtime.exit)
			}
			if tc.want != agentconversation.KnownAdvance && runtime.exit != "" {
				t.Fatalf("invented exit=%q", runtime.exit)
			}
		})
	}
}

func TestFeatureAreaFinishPersistenceFailureIsReported(t *testing.T) {
	c, _, tasks, trees, _, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	tasks.value.FeatureAreaID = "feature-a"
	runtime := &faultConversationRuntime{recordingConversationRuntime: recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation-a", FeatureAreaID: "feature-a", ContinuationSafe: true}}, finishErr: errors.New("finish persistence unavailable")}
	c.Conversations = runtime
	prepared, prepareErr := c.PrepareRun(context.Background(), tasks.value.ID)
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	_, err := executeDirectFeatureAreaRun(context.Background(), c, prepared)
	if err == nil || !strings.Contains(err.Error(), "finish persistence unavailable") {
		t.Fatalf("error=%v", err)
	}
}

func (r *faultConversationRuntime) BindThreadWithRuntimeProvenance(ctx context.Context, id, thread, _, _ string, selection codexconfig.BackendSelection) error {
	if r.bindErr != nil {
		return r.bindErr
	}
	return r.recordingConversationRuntime.BindThreadWithRuntimeProvenance(ctx, id, thread, "", "", selection)
}
