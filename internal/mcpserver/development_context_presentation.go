package mcpserver

import (
	"context"
	_ "embed"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const devBridgeCardResourceURI = "ui://devbridge/card/task-panel-v47"
const previousScopedTaskPanelResourceURI = "ui://devbridge/card/task-panel-v22"
const previousSessionTaskPanelResourceURI = "ui://devbridge/card/task-panel-v19"
const previousTabbedTaskPanelResourceURI = "ui://devbridge/card/task-panel-v18"
const previousPrioritizedTaskPanelResourceURI = "ui://devbridge/card/task-panel-v17"
const previousUnifiedTaskPanelResourceURI = "ui://devbridge/card/task-panel-v16"
const previousVerificationDecisionsDevBridgeCardResourceURI = "ui://devbridge/card/verification-decisions-v15"
const previousRunResultRecoveryDevBridgeCardResourceURI = "ui://devbridge/card/run-result-recovery-v14"
const previousOneNextStepDevBridgeCardResourceURI = "ui://devbridge/card/one-next-step-v13"
const previousCurrentRunAnalysisDevBridgeCardResourceURI = "ui://devbridge/card/current-run-analysis-v12"
const previousBusinessStageDevBridgeCardResourceURI = "ui://devbridge/card/business-stage-recovery-v11"
const previousAutoStartDevBridgeCardResourceURI = "ui://devbridge/card/approval-auto-start-v10"
const previousNaturalResultDevBridgeCardResourceURI = "ui://devbridge/card/natural-result-analysis-v9"
const previousTerminalStatusDevBridgeCardResourceURI = "ui://devbridge/card/read-only-terminal-status-v8"
const previousStickyViewDevBridgeCardResourceURI = "ui://devbridge/card/read-only-sticky-view-v7"
const previousReviewActionsDevBridgeCardResourceURI = "ui://devbridge/card/read-only-review-actions-v6"
const previousReviewAnalysisDevBridgeCardResourceURI = "ui://devbridge/card/read-only-review-analysis-v5"
const previousConversationDevBridgeCardResourceURI = "ui://devbridge/card/read-only-conversation-v4"
const previousPiPV3DevBridgeCardResourceURI = "ui://devbridge/card/read-only-pip-v3"
const previousPiPDevBridgeCardResourceURI = "ui://devbridge/card/read-only-pip-v2"
const previousDevBridgeCardResourceURI = "ui://devbridge/card/read-only-v1"
const legacyDevBridgeCardResourceURI = "ui://devbridge/card"
const developmentContextResourceURI = devBridgeCardResourceURI
const featureAreaResourceURI = devBridgeCardResourceURI

//go:embed dev_bridge_ui.html
var devBridgeBaseHTML string

var devBridgeUIHTML = confirmationStackHTML(devBridgeBaseHTML)

func RegisterDevelopmentContextCard(server *sdkmcp.Server) {
	meta := sdkmcp.Meta{"devbridge/componentVersion": "TASK_PANEL_V45", "devbridge/presentationPolicy": "SINGLE_PANEL_WITH_EXPLICIT_USER_DECISIONS", "openai/ui": map[string]any{"availableDisplayModes": []string{"inline", "pip", "fullscreen"}}, "ui": map[string]any{"csp": map[string]any{"connectDomains": []string{}, "resourceDomains": []string{}, "frameDomains": []string{}}, "prefersBorder": true}}
	for _, uri := range []string{devBridgeCardResourceURI, "ui://devbridge/card/task-panel-v46", "ui://devbridge/card/task-panel-v45", "ui://devbridge/card/task-panel-v44", "ui://devbridge/card/task-panel-v43", "ui://devbridge/card/task-panel-v42", "ui://devbridge/card/task-panel-v41", "ui://devbridge/card/task-panel-v40", "ui://devbridge/card/task-panel-v39", "ui://devbridge/card/task-panel-v38", "ui://devbridge/card/task-panel-v37", "ui://devbridge/card/task-panel-v36", "ui://devbridge/card/task-panel-v35", "ui://devbridge/card/task-panel-v34", "ui://devbridge/card/task-panel-v33", "ui://devbridge/card/task-panel-v32", "ui://devbridge/card/task-panel-v31", "ui://devbridge/card/task-panel-v30", "ui://devbridge/card/task-panel-v29", "ui://devbridge/card/task-panel-v28", "ui://devbridge/card/task-panel-v27", "ui://devbridge/card/task-panel-v26", "ui://devbridge/card/task-panel-v25", "ui://devbridge/card/task-panel-v24", "ui://devbridge/card/task-panel-v23", previousScopedTaskPanelResourceURI, "ui://devbridge/card/task-panel-v21", "ui://devbridge/card/task-panel-v20", previousSessionTaskPanelResourceURI, previousTabbedTaskPanelResourceURI, previousPrioritizedTaskPanelResourceURI, previousUnifiedTaskPanelResourceURI, previousVerificationDecisionsDevBridgeCardResourceURI, previousRunResultRecoveryDevBridgeCardResourceURI, previousOneNextStepDevBridgeCardResourceURI, previousCurrentRunAnalysisDevBridgeCardResourceURI, previousBusinessStageDevBridgeCardResourceURI, previousAutoStartDevBridgeCardResourceURI, previousNaturalResultDevBridgeCardResourceURI, previousTerminalStatusDevBridgeCardResourceURI, previousStickyViewDevBridgeCardResourceURI, previousReviewActionsDevBridgeCardResourceURI, previousReviewAnalysisDevBridgeCardResourceURI, previousConversationDevBridgeCardResourceURI, previousPiPV3DevBridgeCardResourceURI, previousPiPDevBridgeCardResourceURI, previousDevBridgeCardResourceURI, legacyDevBridgeCardResourceURI} {
		server.AddResource(&sdkmcp.Resource{URI: uri, Name: "devbridge_card", Title: "Dev Bridge Task Panel", MIMEType: decisionResourceMIME, Meta: meta}, func(context.Context, *sdkmcp.ReadResourceRequest) (*sdkmcp.ReadResourceResult, error) {
			return &sdkmcp.ReadResourceResult{Contents: []*sdkmcp.ResourceContents{{URI: uri, MIMEType: decisionResourceMIME, Text: devBridgeUIHTML, Meta: meta}}}, nil
		})
	}
}
