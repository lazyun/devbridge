package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexagent"
	"github.com/lazyun/devbridge/internal/run"
)

type capabilitySession struct {
	*fakeSession
	runID                  string
	tools                  []agent.ToolSpec
	configuredBeforeThread bool
}

func (s *capabilitySession) SetUsageRunID(id string)                { s.runID = id }
func (s *capabilitySession) SetDynamicTools(tools []agent.ToolSpec) { s.tools = tools }
func (s *capabilitySession) StartThread(ctx context.Context, cwd string) (string, error) {
	s.configuredBeforeThread = s.runID != "" && len(s.tools) != 0
	return s.fakeSession.StartThread(ctx, cwd)
}

func TestCoordinatorUsesExportedAgentCapabilitiesBeforeThreadStart(t *testing.T) {
	c, _, tasks, trees, backend, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	instruction := " \tinspect e\u0301 中文\r\n "
	tasks.value.WorkSpec.CodexInstruction = instruction
	session := &capabilitySession{fakeSession: backend}
	c.Codex = sessionFactory{session: session}
	c.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}}
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := executeDirectFeatureAreaRun(context.Background(), c, prepared)
	if err != nil || completed.Status != run.StatusSucceeded {
		t.Fatalf("run=%+v error=%v", completed, err)
	}
	if !session.configuredBeforeThread || session.runID != completed.ID {
		t.Fatalf("Core did not bind usage/tools before thread start: %+v", session)
	}
	want := FeatureAreaHostToolV1Specs()
	if len(session.tools) != len(want) {
		t.Fatalf("tools=%+v want=%+v", session.tools, want)
	}
	for i := range want {
		if !reflect.DeepEqual(session.tools[i], agent.ToolSpec(want[i])) {
			t.Fatalf("tool %d changed", i)
		}
	}
	if backend.prompt != instruction {
		t.Fatalf("frozen instruction changed: %q", backend.prompt)
	}
}

func TestCodexSessionExtractionPreservesContinuityCapabilities(t *testing.T) {
	binding := appserver.Binding{ID: "original-instance", Epoch: 7}
	cleanupErr := errors.New("unproven idle cleanup")
	calls := 0
	session := &codexSession{Session: codexagent.NewSession(nil, codexagent.Options{}), independent: true, appServerInstance: binding, idleCleanup: func(context.Context) error { calls++; return cleanupErr }}
	var _ agent.Session = session
	var _ agent.RuntimeBinder = session
	var _ agent.UsageRunBinder = session
	var _ agent.ToolConfigurer = session
	var _ InteractiveCodexSession = session
	var _ rejoiningCodexSession = session
	if !session.independentlyOwned() || !reflect.DeepEqual(session.continuityBinding(), binding) {
		t.Fatal("same-process ownership lost at adapter boundary")
	}
	if err := session.stopIdleAppServer(context.Background()); !errors.Is(err, cleanupErr) || calls != 1 {
		t.Fatalf("cleanup observation changed: calls=%d err=%v", calls, err)
	}
}
