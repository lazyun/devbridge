package execution

import (
	"context"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codexconfig"
)

type recordingConversationRuntime struct {
	conversation                 agentconversation.Conversation
	begin, bind, pending, finish int
	advance                      agentconversation.Advance
	exit                         string
	quiesced                     bool
	bindRun                      func(string, string) error
}

// bindDirectFixtureRun supplies the durable facts that the old coordinator
// unit fixtures predate. It is deliberately test-only: production persists
// this atomically in AgentConversationStore, before the Run proceeds.
func (r *recordingConversationRuntime) bindDirectFixtureRun(runs RunService, runID string, selection codexconfig.BackendSelection) {
	if r.bindRun == nil {
		r.bindRun = func(id, threadID string) error {
			_, err := runs.AttachThread(context.Background(), id, threadID)
			return err
		}
	}
	if r.conversation.BackendThreadID != "" {
		bound := selection
		r.conversation.BackendSelection = &bound
		r.conversation.BackendSelectionSourceRunID = "historical-" + runID
		r.conversation.BackendSelectionSourceThreadID = r.conversation.BackendThreadID
	}
}

func (r *recordingConversationRuntime) BeginRun(context.Context, string) (agentconversation.Conversation, error) {
	r.begin++
	return r.conversation, nil
}
func (r *recordingConversationRuntime) BindThread(_ context.Context, runID string, thread string) error {
	if r.bindRun != nil {
		if err := r.bindRun(runID, thread); err != nil {
			return err
		}
	}
	r.bind++
	r.conversation.BackendThreadID = thread
	return nil
}
func (r *recordingConversationRuntime) MarkThreadStartPending(context.Context, string) error {
	return nil
}

func (r *recordingConversationRuntime) MarkTurnPending(context.Context, string) error {
	r.pending++
	return nil
}
func (r *recordingConversationRuntime) FinishRun(_ context.Context, _ string, advance agentconversation.Advance, exit string, quiesced bool) error {
	r.finish++
	r.advance, r.exit, r.quiesced = advance, exit, quiesced
	if advance == agentconversation.KnownAdvance && quiesced {
		r.conversation.CurrentBoundaryTurnID = exit
	}
	return nil
}
func (r *recordingConversationRuntime) AuthorizeRun(context.Context, string) (agentconversation.Claim, error) {
	return agentconversation.Claim{}, nil
}

func TestFeatureAreaRunUsesConversationThreadAndDurableBoundary(t *testing.T) {
	coordinator, _, tasks, trees, session, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	tasks.value.FeatureAreaID = "feature-a"
	runtime := &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation-a", FeatureAreaID: "feature-a", ContinuationSafe: true}}
	coordinator.Conversations = runtime
	prepared, err := coordinator.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	value, err := executeDirectFeatureAreaRun(context.Background(), coordinator, prepared)
	if err != nil || value.ThreadID == "" {
		t.Fatalf("run=%+v err=%v", value, err)
	}
	if session.startCalls != 1 || session.resumeCalls != 0 || runtime.begin != 1 || runtime.bind != 1 || runtime.pending != 1 || runtime.finish != 1 {
		t.Fatalf("calls start=%d resume=%d runtime=%+v", session.startCalls, session.resumeCalls, runtime)
	}
	if runtime.advance != agentconversation.KnownAdvance || runtime.exit != "turn-1" || !runtime.quiesced {
		t.Fatalf("boundary=%+v", runtime)
	}
}

func (r *recordingConversationRuntime) BindThreadWithRuntimeProvenance(ctx context.Context, id, thread, _, _ string, selection codexconfig.BackendSelection) error {
	r.conversation.BackendSelection = &selection
	r.conversation.BackendSelectionSourceRunID = id
	r.conversation.BackendSelectionSourceThreadID = thread
	return r.BindThread(ctx, id, thread)
}
