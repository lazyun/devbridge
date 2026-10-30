package execution

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

type declaredFactory struct {
	descriptor agent.Descriptor
	session    agent.Session
	starts     int
}

func (f *declaredFactory) AgentDescriptor() agent.Descriptor { return f.descriptor }
func (f *declaredFactory) Start(context.Context, codexconfig.Config) (agent.Session, error) {
	f.starts++
	return f.session, nil
}

func TestAgentSelectionUsesExactExecutorWithoutCapabilityFallback(t *testing.T) {
	codex := &declaredFactory{descriptor: agent.Descriptor{ID: "codex", Capabilities: []agent.Capability{agent.CapabilityAuthorizationCeiling}}}
	pi := &declaredFactory{descriptor: agent.Descriptor{ID: "pi"}}
	c := &Coordinator{Codex: codex, PiAgent: pi}
	selected, err := c.selectAgentFactory(nil, []agent.Capability{agent.CapabilityAuthorizationCeiling})
	if err != nil || selected != codex {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	selected, err = c.selectAgentFactory(&review.Review{ExecutorKind: review.ExecutorPiAgent}, []agent.Capability{agent.CapabilityAuthorizationCeiling})
	if selected != nil || !errors.Is(err, agent.ErrCapabilityUnsupported) {
		t.Fatalf("unsupported Pi silently selected/fell back: %v %v", selected, err)
	}
	if codex.starts != 0 || pi.starts != 0 {
		t.Fatal("selection launched a backend")
	}
}

func TestCoordinatorRefusesUndeclaredCapabilityBeforeBackendStart(t *testing.T) {
	c, _, tasks, trees, session, _ := successfulCoordinator()
	trees.value.Path = t.TempDir()
	factory := &declaredFactory{descriptor: agent.Descriptor{ID: "test"}, session: session}
	c.Codex = factory
	c.Conversations = &recordingConversationRuntime{conversation: agentconversation.Conversation{ID: "conversation", FeatureAreaID: tasks.value.FeatureAreaID, ContinuationSafe: true}}
	prepared, err := c.PrepareRun(context.Background(), tasks.value.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := executeDirectFeatureAreaRun(context.Background(), c, prepared)
	if !errors.Is(err, agent.ErrCapabilityUnsupported) || completed.Status != run.StatusInfrastructureFailed || factory.starts != 0 || session.startCalls != 0 || session.turn != 0 {
		t.Fatalf("unsupported capability caused effects: run=%+v err=%v starts=%d session=%+v", completed, err, factory.starts, session)
	}
}

func TestAgentDeclarationCannotSubstituteForSessionMethod(t *testing.T) {
	factory := &declaredFactory{descriptor: agent.Descriptor{ID: "test", Capabilities: []agent.Capability{agent.CapabilityRuntimeBinding}}}
	if err := validateAgentSessionCapabilities(factory, &fakeSession{}, []agent.Capability{agent.CapabilityRuntimeBinding}); !errors.Is(err, agent.ErrCapabilityUnsupported) {
		t.Fatalf("unimplemented binding accepted: %v", err)
	}
}

func TestNeutralTerminalObservationsRequireExactInteraction(t *testing.T) {
	cause := errors.New("native failure")
	observed := &agent.TerminalError{ThreadID: "thread", TurnID: "turn", State: agent.TurnFailed, Err: cause}
	err := &agent.ExecutionError{Err: observed}
	if terminalAgentObservation(err) != observed || !observed.Matches("thread", "turn") || observed.Matches("thread", "new-turn") || !errors.Is(err, cause) {
		t.Fatal("neutral error lost identity or original cause")
	}
	if terminalAgentObservation(cause) != nil {
		t.Fatal("transport failure became terminal")
	}
}

type changingDescriptorFactory struct {
	declaredFactory
	descriptorReads int
}

func (f *changingDescriptorFactory) AgentDescriptor() agent.Descriptor {
	f.descriptorReads++
	if f.descriptorReads == 1 {
		return agent.Descriptor{ID: "pi"}
	}
	return agent.Descriptor{ID: "codex"}
}

func TestAgentSelectionSnapshotsDescriptorOnceWithoutCrossSelection(t *testing.T) {
	codexFactory := &changingDescriptorFactory{}
	piFactory := &declaredFactory{descriptor: agent.Descriptor{ID: "pi"}}
	c := &Coordinator{Codex: codexFactory, PiAgent: piFactory}
	selected, err := c.selectAgentFactory(nil, nil)
	if err == nil || selected != nil {
		t.Fatalf("conflicting selected identity redirected to another backend: selected=%v err=%v", selected, err)
	}
	if codexFactory.descriptorReads != 1 {
		t.Fatalf("selected descriptor read %d times; want one immutable snapshot", codexFactory.descriptorReads)
	}
	if codexFactory.starts != 0 || piFactory.starts != 0 {
		t.Fatal("identity validation launched an agent")
	}
}

func TestCodexHostToolsCapabilityRequiresConfiguredHandler(t *testing.T) {
	for _, configured := range []bool{false, true} {
		factory := DefaultCodexFactory{}
		if configured {
			factory.DynamicToolHandler = func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
				return codex.DynamicToolCallResponse{}, nil
			}
		}
		hasHostTools := false
		for _, capability := range factory.AgentDescriptor().Capabilities {
			if capability == agent.CapabilityHostTools {
				hasHostTools = true
			}
		}
		if hasHostTools != configured {
			t.Fatalf("configured=%v, declared host tools=%v", configured, hasHostTools)
		}
	}
}

func TestAgentCapabilityRefusalKeepsDispatchedRunAndOriginalReceipt(t *testing.T) {
	current := run.Run{ID: "original-run", Status: run.StatusRunning, ThreadID: "original-thread", TurnID: "original-turn"}
	backend := &fakeSession{}
	cause := &agent.CapabilityError{AgentID: "codex", Missing: []agent.Capability{agent.CapabilityHostTools}}
	result, err := detachRejectedAgentRejoin(current, &rejoinExecution{Client: backend}, backend, cause)
	if !reflect.DeepEqual(result, current) || !errors.Is(err, ErrAppServerDetached) || !errors.Is(err, agent.ErrCapabilityUnsupported) || backend.closed != 1 || backend.startCalls != 0 || backend.turn != 0 {
		t.Fatalf("capability refusal changed original operation: result=%+v error=%v backend=%+v", result, err, backend)
	}
}
