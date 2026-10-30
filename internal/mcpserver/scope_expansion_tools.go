package mcpserver

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type scopeExpansionCreateInput struct {
	AISessionID     string           `json:"aiSessionId"`
	ReviewID        string           `json:"reviewId"`
	CurrentScope    reviewScopeInput `json:"currentScope"`
	AdditionalScope reviewScopeInput `json:"additionalScope"`
	Reason          string           `json:"reason"`
}

type scopeExpansionRefInput struct {
	AISessionID string `json:"aiSessionId"`
	ExpansionID string `json:"expansionId"`
}

type ScopeExpansionOutput struct {
	FeatureAreaID   string            `json:"featureAreaId"`
	ID              string            `json:"id"`
	AISessionID     string            `json:"aiSessionId"`
	ProjectID       string            `json:"projectId"`
	ReviewID        string            `json:"reviewId"`
	CurrentScope    ReviewScopeOutput `json:"currentScope"`
	AdditionalScope ReviewScopeOutput `json:"additionalScope"`
	Reason          string            `json:"reason"`
	Status          string            `json:"status"`
	CreatedAt       string            `json:"createdAt"`
	UpdatedAt       string            `json:"updatedAt"`
	DecidedAt       *string           `json:"decidedAt,omitempty"`
}

type ScopeExpansionListOutput struct {
	ScopeExpansions []ScopeExpansionOutput `json:"scopeExpansions"`
}

const (
	reviewScopeSchema               = `{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":4096}}},"required":["kind","paths"],"additionalProperties":false}`
	scopeExpansionCreateInputSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","minLength":1,"maxLength":128},"currentScope":` + reviewScopeSchema + `,"additionalScope":` + reviewScopeSchema + `,"reason":{"type":"string","minLength":1,"maxLength":16384}},"required":["aiSessionId","reviewId","currentScope","additionalScope","reason"],"additionalProperties":false}`
	scopeExpansionRefInputSchema    = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"expansionId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","expansionId"],"additionalProperties":false}`
	scopeExpansionOutputSchema      = `{"type":"object","properties":{"featureAreaId":{"type":"string"},"id":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"reviewId":{"type":"string"},"currentScope":` + reviewScopeSchema + `,"additionalScope":` + reviewScopeSchema + `,"reason":{"type":"string","maxLength":16384},"status":{"type":"string","enum":["PENDING_CONFIRMATION","ALLOWED","REJECTED"]},"createdAt":{"type":"string"},"updatedAt":{"type":"string"},"decidedAt":{"type":"string"}},"required":["id","aiSessionId","projectId","reviewId","currentScope","additionalScope","reason","status","createdAt","updatedAt"],"additionalProperties":false}`
	scopeExpansionListOutputSchema  = `{"type":"object","properties":{"scopeExpansions":{"type":"array","maxItems":100,"items":` + scopeExpansionOutputSchema + `}},"required":["scopeExpansions"],"additionalProperties":false}`
)

func registerScopeExpansionTools(server *sdkmcp.Server, service BridgeService) {
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "scope_expansion_create", Title: "Propose scope expansion", Description: "Create the exact pending scope-expansion authorization for the exact FeatureArea Review, present its interactive card, and stop for the user's button decision. A suggestion alone never widens scope or starts Codex; natural language remains for changing the FeatureArea goal.", InputSchema: json.RawMessage(scopeExpansionCreateInputSchema), OutputSchema: json.RawMessage(scopeExpansionOutputSchema), Annotations: stateChangingAnnotations("Propose scope expansion", false), Meta: decisionToolMeta()}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input scopeExpansionCreateInput) (*sdkmcp.CallToolResult, ScopeExpansionOutput, error) {
		value, err := service.CreateScopeExpansion(ctx, scopeexpansion.CreateInput{AISessionID: input.AISessionID, ReviewID: input.ReviewID, CurrentScope: run.ReviewScope{Kind: run.ReviewScopeKind(input.CurrentScope.Kind), Paths: input.CurrentScope.Paths}, AdditionalScope: run.ReviewScope{Kind: run.ReviewScopeKind(input.AdditionalScope.Kind), Paths: input.AdditionalScope.Paths}, Reason: input.Reason})
		if err != nil {
			return nil, ScopeExpansionOutput{}, publicToolError(err)
		}
		return nil, scopeExpansionOutput(value), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "scope_expansion_get", Title: "Get scope expansion", Description: "Get one exact scope-expansion proposal visible to the AI session's Project. Present a pending result as an interactive decision card.", InputSchema: json.RawMessage(scopeExpansionRefInputSchema), OutputSchema: json.RawMessage(scopeExpansionOutputSchema), Annotations: toolAnnotations("Get scope expansion", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input scopeExpansionRefInput) (*sdkmcp.CallToolResult, ScopeExpansionOutput, error) {
		value, err := service.GetScopeExpansion(ctx, input.AISessionID, input.ExpansionID)
		if err != nil {
			return nil, ScopeExpansionOutput{}, publicToolError(err)
		}
		return nil, scopeExpansionOutput(value), nil
	})

	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "scope_expansion_list", Title: "List scope expansions", Description: "List bounded durable scope-expansion proposals.", InputSchema: json.RawMessage(listInputSchema), OutputSchema: json.RawMessage(scopeExpansionListOutputSchema), Annotations: toolAnnotations("List scope expansions", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input listInput) (*sdkmcp.CallToolResult, ScopeExpansionListOutput, error) {
		values, err := service.ListScopeExpansions(ctx, input.AISessionID, controlapi.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return nil, ScopeExpansionListOutput{}, publicToolError(err)
		}
		outputs := make([]ScopeExpansionOutput, 0, len(values))
		for _, value := range values {
			outputs = append(outputs, scopeExpansionOutput(value))
		}
		return nil, ScopeExpansionListOutput{ScopeExpansions: outputs}, nil
	})

	for _, definition := range []struct {
		name, title, description string
		destructive              bool
		invoke                   func(context.Context, string, string) (scopeexpansion.Expansion, error)
	}{
		{"scope_expansion_allow", "Allow scope expansion", "Handle one Allow expansion card click for the exact still-pending proposal. This durably authorizes only its displayed additional scope and does not start Codex or change the Task goal. Stale or repeated clicks fail safely.", false, service.AllowScopeExpansion},
		{"scope_expansion_reject", "Keep current scope", "Handle one Keep current scope card click for the exact still-pending proposal. It records rejection and preserves the current scope. Stale or repeated clicks fail safely.", true, service.RejectScopeExpansion},
	} {
		definition := definition
		sdkmcp.AddTool(server, &sdkmcp.Tool{Name: definition.name, Title: definition.title, Description: definition.description, InputSchema: json.RawMessage(scopeExpansionRefInputSchema), OutputSchema: json.RawMessage(scopeExpansionOutputSchema), Annotations: stateChangingAnnotations(definition.title, definition.destructive), Meta: decisionToolMeta()}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input scopeExpansionRefInput) (*sdkmcp.CallToolResult, ScopeExpansionOutput, error) {
			value, err := definition.invoke(ctx, input.AISessionID, input.ExpansionID)
			if err != nil {
				return nil, ScopeExpansionOutput{}, publicToolError(err)
			}
			return nil, scopeExpansionOutput(value), nil
		})
	}
}

func scopeExpansionOutput(value scopeexpansion.Expansion) ScopeExpansionOutput {
	return ScopeExpansionOutput{ID: value.ID, AISessionID: value.AISessionID, ProjectID: value.ProjectID, FeatureAreaID: value.FeatureAreaID, ReviewID: value.ReviewID, CurrentScope: ReviewScopeOutput{Kind: string(value.CurrentScope.Kind), Paths: append([]string(nil), value.CurrentScope.Paths...)}, AdditionalScope: ReviewScopeOutput{Kind: string(value.AdditionalScope.Kind), Paths: append([]string(nil), value.AdditionalScope.Paths...)}, Reason: boundedUTF8(value.Reason, scopeexpansion.MaxReasonBytes), Status: string(value.Status), CreatedAt: value.CreatedAt.UTC().Format(time.RFC3339Nano), UpdatedAt: value.UpdatedAt.UTC().Format(time.RFC3339Nano), DecidedAt: formattedTime(value.DecidedAt)}
}
