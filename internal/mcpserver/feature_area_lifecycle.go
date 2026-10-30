package mcpserver

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/featurearea"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type FeatureAreaLifecycleInput struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId"`
	RequestID     string `json:"requestId"`
}
type FeatureAreaDecomposeInput struct {
	AISessionID   string                        `json:"aiSessionId"`
	FeatureAreaID string                        `json:"featureAreaId"`
	RequestID     string                        `json:"requestId"`
	Children      []featurearea.ChildDefinition `json:"children"`
}
type FeatureAreaDecomposeOutput struct {
	Parent   ExternalTopicOutput   `json:"parent"`
	Children []ExternalTopicOutput `json:"children"`
}
type AgentContextResetOutput struct {
	FeatureAreaID string `json:"featureAreaId"`
	RequestID     string `json:"requestId"`
}

const FeatureAreaLifecycleInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","featureAreaId","requestId"],"additionalProperties":false}`
const FeatureAreaDecomposeInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128},"children":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"object","properties":{"title":{"type":"string","minLength":1,"maxLength":256},"problemStatement":{"type":"string","minLength":1,"maxLength":16384}},"required":["title","problemStatement"],"additionalProperties":false}}},"required":["aiSessionId","featureAreaId","requestId","children"],"additionalProperties":false}`
const AgentContextResetOutputSchema = `{"type":"object","properties":{"featureAreaId":{"type":"string"},"requestId":{"type":"string"}},"required":["featureAreaId","requestId"],"additionalProperties":false}`

type featureAreaLifecycleBridge interface {
	ActivateFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error)
	RetireFeatureArea(context.Context, string, string, string) (featurearea.FeatureArea, error)
	DecomposeFeatureArea(context.Context, featurearea.DecomposeInput) (featurearea.LifecycleResult, error)
}

func (s *StaticService) ActivateFeatureArea(ctx context.Context, session, id, request string) (featurearea.FeatureArea, error) {
	if s.control == nil {
		return featurearea.FeatureArea{}, errors.New("control API unavailable")
	}
	return s.control.ActivateFeatureArea(ctx, session, id, request)
}
func (s *StaticService) RetireFeatureArea(ctx context.Context, session, id, request string) (featurearea.FeatureArea, error) {
	if s.control == nil {
		return featurearea.FeatureArea{}, errors.New("control API unavailable")
	}
	return s.control.RetireFeatureArea(ctx, session, id, request)
}
func (s *StaticService) DecomposeFeatureArea(ctx context.Context, input featurearea.DecomposeInput) (featurearea.LifecycleResult, error) {
	if s.control == nil {
		return featurearea.LifecycleResult{}, errors.New("control API unavailable")
	}
	return s.control.DecomposeFeatureArea(ctx, input)
}

func lifecycleAnnotations(title string, destructive bool) *sdkmcp.ToolAnnotations {
	value := stateChangingAnnotations(title, destructive)
	value.IdempotentHint = true
	return value
}
