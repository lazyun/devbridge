package execution

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/taskgoal"
	"github.com/lazyun/devbridge/internal/workset"
)

type continuityIdentityRuns struct {
	RunService
	value       run.Run
	err         error
	strictReads int
}

func (s *continuityIdentityRuns) GetForRuntimeAuthority(context.Context, string) (run.Run, error) {
	s.strictReads++
	return s.value, s.err
}
func (s *continuityIdentityRuns) Get(context.Context, string) (run.Run, error) {
	panic("display read used for continuity authority")
}

func continuityIdentityFixture() (*Coordinator, *continuityIdentityRuns, agentconversation.Conversation, run.Run, taskgoal.Execution) {
	source := run.Run{ID: "source-run", ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ThreadID: "original-thread", ExecutionProposalID: "execution", Purpose: run.PurposeImplementation}
	runs := &continuityIdentityRuns{value: source}
	execution := taskgoal.Execution{ID: "execution", ProjectID: "project", TaskID: "task", RunID: source.ID, RuntimeAdapterIdentity: executionproposal.RuntimeAdapterCodexAppServerV1, RuntimeProtocolIdentity: executionproposal.RuntimeProtocolCodex0149}
	c := &Coordinator{Runs: runs, Executions: executionSnapshotLookup{value: execution}}
	conversation := agentconversation.Conversation{Backend: "CODEX", BackendThreadID: source.ThreadID, BackendSelectionSourceRunID: source.ID, BackendSelectionSourceThreadID: source.ThreadID}
	current := run.Run{ID: "current-run", ProjectID: "project", FeatureAreaID: "area"}
	return c, runs, conversation, current, execution
}

func TestConversationAgentIdentityRejectsCrossAgentAndPolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		source, selected codexruntime.NextRunRuntimeConfig
	}{
		{"codex-to-pi", codexruntime.NextRunRuntimeConfig{}, codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}},
		{"pi-to-legacy-codex", codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}, codexruntime.NextRunRuntimeConfig{}},
		{"codex-native-to-codex-cooperative", codexruntime.NextRunRuntimeConfig{}, codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "codex", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, runs, conversation, current, execution := continuityIdentityFixture()
			execution.RuntimeConfig = tc.source
			c.Executions = executionSnapshotLookup{value: execution}
			if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, tc.selected); err == nil {
				t.Fatal("cross-agent/policy resume accepted")
			}
			if runs.strictReads != 1 {
				t.Fatal("exact strict source Run was not read")
			}
		})
	}
}

func TestConversationAgentIdentityAcceptsExactFrozenCooperativeOwnerOnly(t *testing.T) {
	selection := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}
	for _, mutate := range []string{"none", "owner", "run", "thread", "project", "area", "source-thread", "fingerprint"} {
		t.Run(mutate, func(t *testing.T) {
			c, runs, conversation, current, execution := continuityIdentityFixture()
			execution.RuntimeConfig = selection
			execution.RuntimeAdapterIdentity, execution.RuntimeProtocolIdentity = agentpolicy.CooperativeAdapterIdentity, agentpolicy.CooperativeProtocolIdentity
			switch mutate {
			case "fingerprint":
				execution.RuntimeConfig.AgentConfigFingerprint = strings.Repeat("b", 64)
			case "owner":
				execution.ID = "other-execution"
			case "run":
				execution.RunID = "other-run"
			case "thread":
				runs.value.ThreadID = "different-thread"
			case "project":
				current.ProjectID = "different-project"
			case "area":
				current.FeatureAreaID = "different-area"
			case "source-thread":
				conversation.BackendSelectionSourceThreadID = "different-thread"
			}
			c.Executions = executionSnapshotLookup{value: execution}
			err := c.validateConversationAgentIdentity(context.Background(), conversation, current, selection)
			if (err == nil) != (mutate == "none") {
				t.Fatalf("mutation=%s error=%v", mutate, err)
			}
		})
	}
}

func TestConversationAgentIdentityHistoricalNativeRequiresProofForExplicitSelection(t *testing.T) {
	c, _, conversation, current, execution := continuityIdentityFixture()
	selected := codexruntime.NextRunRuntimeConfig{AgentID: "codex", ExecutionPolicy: agentpolicy.PolicyTaskSandboxV1}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, selected); err != nil {
		t.Fatal(err)
	}
	execution.RuntimeAdapterIdentity, execution.RuntimeProtocolIdentity = "", ""
	c.Executions = executionSnapshotLookup{value: execution}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, selected); err == nil {
		t.Fatal("unproven historical agent accepted for explicit selection")
	}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, codexruntime.NextRunRuntimeConfig{}); err != nil {
		t.Fatalf("unchanged fullyempty legacy path rejected: %v", err)
	}
}

func TestConversationAgentIdentityNeverTreatsLegacyPiReviewAsCodex(t *testing.T) {
	c, runs, conversation, current, _ := continuityIdentityFixture()
	runs.value.ExecutionProposalID, runs.value.ReviewID = "", "review"
	c.Reviews = &coordinatorReviewRuntime{value: review.Review{ID: "review", RunID: runs.value.ID, ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ExecutorKind: review.ExecutorPiAgent}}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, codexruntime.NextRunRuntimeConfig{}); err == nil {
		t.Fatal("legacy Pi review adopted as Codex")
	}
}

func TestConversationAgentIdentityUnavailableFailsClosedOnlyForExplicitSelection(t *testing.T) {
	c, runs, conversation, current, _ := continuityIdentityFixture()
	cause := errors.New("source authority unreadable")
	runs.err = cause
	selected := codexruntime.NextRunRuntimeConfig{AgentConfigFingerprint: strings.Repeat("a", 64), AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, selected); !errors.Is(err, cause) {
		t.Fatalf("source read failure lost: %v", err)
	}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, codexruntime.NextRunRuntimeConfig{}); err != nil {
		t.Fatalf("legacy compatibility changed: %v", err)
	}
	runs.err = nil
	runs.value.ExecutionProposalID = ""
	runs.value.RuntimeWorkset = &workset.RuntimeWorkset{AgentID: "pi", ExecutionPolicy: agentpolicy.PolicyCooperativeTrustedV1}
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, codexruntime.NextRunRuntimeConfig{}); err == nil {
		t.Fatal("positive conflicting workset evidence ignored by legacy fallback")
	}
	conversation.BackendThreadID = ""
	if err := c.validateConversationAgentIdentity(context.Background(), conversation, current, selected); err != nil {
		t.Fatalf("new thread required prior identity: %v", err)
	}
}
