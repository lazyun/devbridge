package mcpserver

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/controlapi"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const featureAreaDetailsResourceURI = "ui://devbridge/feature-area-details"

//go:embed feature_area_details_ui.html
var featureAreaDetailsUIHTML string

type featureAreaDetailsReader interface {
	FeatureAreaDetailsForSession(context.Context, string, string, controlapi.ListOptions) (presentation.DetailsPage, error)
}

func (s *StaticService) FeatureAreaDetailsForSession(ctx context.Context, sessionID, areaID string, options controlapi.ListOptions) (presentation.DetailsPage, error) {
	if s.control == nil {
		return presentation.DetailsPage{}, errors.New("FeatureArea details unavailable")
	}
	return s.control.FeatureAreaDetailsForSession(ctx, sessionID, areaID, options)
}

type featureAreaDetailsInput struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

type featureAreaDetailsShowOutput struct {
	SchemaVersion string `json:"schemaVersion"`
}

const FeatureAreaDetailsShowDescription = "Show bounded durable FeatureArea history in the separate Details view. This presentation-only read never claims, resumes, materializes, or changes a workflow."

// The Details widget obtains complete, paged data through the hidden get
// endpoint. Keep the visible tool's model-facing schema closed and limited to
// its display contract.
const featureAreaDetailsShowOutputSchema = `{"type":"object","properties":{"schemaVersion":{"type":"string"}},"required":["schemaVersion"],"additionalProperties":false}`

// RegisterFeatureAreaDetails installs the independent, read-only history view.
func RegisterFeatureAreaDetails(server *sdkmcp.Server, service BridgeService) {
	metadata := sdkmcp.Meta{"ui": map[string]any{"csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{}}, "prefersBorder": true}}
	server.AddResource(&sdkmcp.Resource{URI: featureAreaDetailsResourceURI, Name: "devbridge_feature_area_details", Title: "Dev Bridge FeatureArea details", MIMEType: decisionResourceMIME, Meta: metadata}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
		return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: featureAreaDetailsResourceURI, MIMEType: decisionResourceMIME, Text: featureAreaDetailsUIHTML, Meta: metadata}}}, nil
	})
	schema := `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"required":["aiSessionId","featureAreaId"],"additionalProperties":false}`
	read := func(ctx context.Context, input featureAreaDetailsInput) (presentation.DetailsPage, error) {
		reader, ok := service.(featureAreaDetailsReader)
		if !ok {
			return presentation.DetailsPage{}, publicToolError(errors.New("FeatureArea details unavailable"))
		}
		value, err := reader.FeatureAreaDetailsForSession(ctx, input.AISessionID, input.FeatureAreaID, controlapi.ListOptions{Limit: input.Limit, Offset: input.Offset})
		if err != nil {
			return presentation.DetailsPage{}, publicToolError(err)
		}
		return value, nil
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "feature_area_details_get", Title: "Read FeatureArea details", Description: "Read bounded durable FeatureArea history. This is a pure read: it never claims, resumes, materializes, or changes a workflow.", InputSchema: json.RawMessage(schema), Annotations: toolAnnotations("Read FeatureArea details", true, true), Meta: sdkmcp.Meta{"ui": map[string]any{"visibility": []string{"app"}}}}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, input featureAreaDetailsInput) (*sdkmcp.CallToolResult, presentation.DetailsPage, error) {
		value, err := read(ctx, input)
		if err != nil {
			return nil, presentation.DetailsPage{}, err
		}
		return &sdkmcp.CallToolResult{Meta: sdkmcp.Meta{"aiSessionId": input.AISessionID, "featureAreaId": input.FeatureAreaID}}, value, nil
	})
}
