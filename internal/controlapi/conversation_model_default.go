package controlapi

import (
	"context"
	"errors"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/confirmation"
)

// conversationModelDefault is a pure read. It neither allocates a logical
// conversation nor changes a claim, boundary, Thread, Run, or default.
func (f *Facade) conversationModelDefault(ctx context.Context, featureAreaID string) *agentconversation.ModelDefault {
	if featureAreaID == "" {
		return nil
	}
	reader, ok := f.deps.AgentConversations.(interface {
		Current(context.Context, string) (agentconversation.Conversation, error)
	})
	if !ok {
		return &agentconversation.ModelDefault{State: "READ_UNAVAILABLE"}
	}
	conversation, err := reader.Current(ctx, featureAreaID)
	if errors.Is(err, agentconversation.ErrNotFound) {
		return &agentconversation.ModelDefault{State: "THREADLESS"}
	}
	if err != nil {
		return &agentconversation.ModelDefault{State: "READ_UNAVAILABLE"}
	}
	out := &agentconversation.ModelDefault{
		ConversationID: conversation.ID,
		ThreadID:       conversation.BackendThreadID,
		InitialModel:   conversation.InitialModel,
		SourceRunID:    conversation.InitialModelSourceRunID,
		Evidence:       conversation.InitialModelEvidence,
	}
	if conversation.BackendThreadID == "" {
		out.State = "THREADLESS"
	} else if conversation.InitialModel != "" && conversation.InitialModelSourceRunID != "" && conversation.InitialModelSourceThreadID == conversation.BackendThreadID {
		out.State = "KNOWN"
	} else {
		out.State = "HISTORICAL_UNKNOWN"
		out.InitialModel, out.SourceRunID, out.Evidence = "", "", ""
	}
	return out
}

func confirmationFeatureAreaID(intent confirmation.Intent) string {
	if intent.Content.RecoveryPurpose != nil {
		return intent.Content.RecoveryPurpose.Proposal.FeatureAreaID
	}
	if intent.Content.Execution != nil {
		return intent.Content.Execution.FeatureAreaID
	}
	if intent.Content.Review != nil {
		return intent.Content.Review.FeatureAreaID
	}
	if intent.Content.Task != nil {
		return intent.Content.Task.FeatureAreaID
	}
	if intent.Content.FeatureArea != nil && intent.Content.FeatureArea.FeatureAreaID != "" {
		return intent.Content.FeatureArea.FeatureAreaID
	}
	if intent.Content.FeatureArea != nil && intent.Content.FeatureArea.InitialTask != nil {
		return intent.Content.FeatureArea.InitialTask.FeatureAreaID
	}
	return ""
}
