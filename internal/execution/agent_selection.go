package execution

import (
	"errors"
	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
)

// selectAgentFactory uses the already selected executor kind, never a fallback
// chosen by prompt text. The launch seam still carries existing frozen Codex
// config/checkpoints in this extraction; selection itself is backend neutral.
func (c *Coordinator) selectAgentFactory(value *review.Review, required []agent.Capability) (CodexFactory, error) {
	factories := []CodexFactory{c.Codex, c.PiAgent}
	selectedSlot := 0
	if value != nil && value.ExecutorKind == review.ExecutorPiAgent {
		selectedSlot = 1
	}
	factory := factories[selectedSlot]
	if factory == nil {
		return nil, agent.ErrAgentUnavailable
	}
	if _, declared := factory.(agent.Described); !declared {
		// Existing injected factories remain a compatibility seam, not a
		// declaration of capabilities. Their established method checks remain.
		return factory, nil
	}
	selectedID := ""
	entries := []agent.Registration[CodexFactory]{}
	for slot, candidate := range factories {
		if declaration, ok := candidate.(agent.Described); ok {
			// Read each declaration once: a mutable descriptor must not change
			// the exact selected slot into another registered backend identity.
			descriptor := declaration.AgentDescriptor()
			if slot == selectedSlot {
				selectedID = descriptor.ID
			}
			entries = append(entries, agent.Registration[CodexFactory]{Descriptor: descriptor, Value: candidate})
		}
	}
	registry, err := agent.NewRegistry(entries...)
	if err != nil {
		return nil, err
	}
	return registry.Select(selectedID, required...)
}

func (f DefaultCodexFactory) AgentDescriptor() agent.Descriptor {
	capabilities := []agent.Capability{
		agent.CapabilityThreadResume, agent.CapabilityStructuredOutput,
		agent.CapabilityTurnStarted, agent.CapabilityInterrupt, agent.CapabilityUsage,
		agent.CapabilityRuntimeBinding,
		agent.CapabilityServiceTier, agent.CapabilityQualifiedLaunch,
		agent.CapabilityAuthorizationCeiling,
	}
	if f.DynamicToolHandler != nil {
		capabilities = append(capabilities, agent.CapabilityHostTools)
	}
	if f.AppServers != nil && f.Config.AgentRuntime != nil {
		capabilities = append(capabilities, agent.CapabilitySameTurnRecovery)
	}
	return agent.Descriptor{ID: "codex", Capabilities: capabilities}
}

// Declaration is necessary but not sufficient: required session operations must
// actually be implemented. These checks run before thread/turn effects. Legacy
// undescribed injected factories keep their established compatibility behavior.
func validateAgentSessionCapabilities(factory any, session agent.Session, required []agent.Capability) error {
	if _, declared := factory.(agent.Described); !declared {
		return nil
	}
	var missing []agent.Capability
	for _, capability := range required {
		implemented := true
		switch capability {
		case agent.CapabilityThreadResume:
			_, implemented = session.(agent.ThreadResumer)
		case agent.CapabilityStructuredOutput:
			_, structured := session.(agent.StructuredSession)
			_, started := session.(agent.StructuredTurnStarter)
			implemented = structured || started
		case agent.CapabilityTurnStarted:
			_, implemented = session.(agent.TurnStarter)
		case agent.CapabilityInterrupt:
			_, implemented = session.(agent.TurnInterrupter)
		case agent.CapabilityRuntimeBinding:
			_, implemented = session.(agent.RuntimeBinder)
		case agent.CapabilityHostTools:
			_, implemented = session.(agent.ToolConfigurer)
		case agent.CapabilitySameTurnRecovery:
			_, implemented = session.(rejoiningCodexSession)
		}
		if !implemented {
			missing = append(missing, capability)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &agent.CapabilityError{AgentID: factory.(agent.Described).AgentDescriptor().ID, Missing: missing}
}

// Refusing a new adapter capability cannot finish an already dispatched Turn.
// Only local transport/recorder ownership is released; history and the original
// Run status stay untouched and require the usual exact recovery observations.
func detachRejectedAgentRejoin(current run.Run, rejoin *rejoinExecution, session agent.Session, cause error) (run.Run, error) {
	var closeErr error
	if session != nil {
		closeErr = session.Close()
	}
	if rejoin != nil && rejoin.Runtime != nil {
		rejoin.Runtime.Close()
	}
	return current, errors.Join(ErrAppServerDetached, cause, closeErr)
}
