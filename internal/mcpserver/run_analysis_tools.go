package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/runanalysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type runAnalysisService interface {
	GetRunAnalysis(context.Context, string, string) (runanalysis.Source, error)
	SubmitRunAnalysis(context.Context, runanalysis.Input) (runanalysis.Receipt, error)
}
type runAnalysisGetInput struct {
	AISessionID string `json:"aiSessionId"`
	RunID       string `json:"runId"`
}

func registerRunAnalysisTools(server *sdkmcp.Server, service BridgeService) {
	// The report contract is returned by the exact source read. It belongs to
	// GPT's explicit submission, never to the native Codex final message.
	target := `"aiSessionId":` + upperIDSchema + `,"runId":` + upperIDSchema
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "run_analysis_get", OutputSchema: json.RawMessage(runtimeDTOSchema[runanalysis.Source]()), Description: "Read exact native execution output, immutable command evidence, frozen verification contract and GPT analysis state. SUCCEEDED means Codex execution completed, not engineering correctness. If state=REQUIRED, analyze these sources and submit run_analysis_submit using the returned reportSchema; do not reexecute Codex or ask the user to approve merely to repair output formatting. Read-only.", InputSchema: json.RawMessage(`{"type":"object","properties":{` + target + `},"required":["aiSessionId","runId"],"additionalProperties":false}`), Annotations: toolAnnotations("Read execution evidence for GPT analysis", true, true)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runAnalysisGetInput) (*sdkmcp.CallToolResult, runanalysis.Source, error) {
		p, ok := service.(runAnalysisService)
		if !ok {
			return nil, runanalysis.Source{}, errors.New("Run analysis unavailable")
		}
		out, err := p.GetRunAnalysis(ctx, in.AISessionID, in.RunID)
		return nil, out, publicToolError(err)
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "run_analysis_submit", OutputSchema: json.RawMessage(runtimeDTOSchema[runanalysis.Receipt]()), Description: "Record GPT's explicit engineering analysis of a completed Codex Run, using run_analysis_get evidence and reportSchema. Never copy natural output blindly or invent observed evidence. Immutable requestId and payload allow exact replay after response loss. Core keeps native execution success and raw text unchanged, validates report scope and observed checks, and prepares the existing business decision. This neither approves implementation nor accepts delivery, and never runs commands.", InputSchema: json.RawMessage(`{"type":"object","properties":{` + target + `,"requestId":` + upperIDSchema + `,"sourceHash":{"type":"string","pattern":"^[a-f0-9]{64}$"},"report":{"type":"object","additionalProperties":true}},"required":["aiSessionId","runId","requestId","sourceHash","report"],"additionalProperties":false}`), Annotations: stateChangingAnnotations("Submit GPT execution analysis", false)}, func(ctx context.Context, _ *sdkmcp.CallToolRequest, in runanalysis.Input) (*sdkmcp.CallToolResult, runanalysis.Receipt, error) {
		p, ok := service.(runAnalysisService)
		if !ok {
			return nil, runanalysis.Receipt{}, errors.New("Run analysis unavailable")
		}
		out, err := p.SubmitRunAnalysis(ctx, in)
		return nil, out, publicToolError(err)
	})
}
func (s *StaticService) GetRunAnalysis(ctx context.Context, session, id string) (runanalysis.Source, error) {
	return s.control.GetRunAnalysis(ctx, session, id)
}
func (s *StaticService) SubmitRunAnalysis(ctx context.Context, in runanalysis.Input) (runanalysis.Receipt, error) {
	return s.control.SubmitRunAnalysis(ctx, in)
}
