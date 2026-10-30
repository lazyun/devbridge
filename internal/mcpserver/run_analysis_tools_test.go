package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type analysisWireFixture struct {
	*surfaceReadFixture
	submitted []runanalysis.Input
}

func (f *analysisWireFixture) GetRunAnalysis(_ context.Context, session, id string) (runanalysis.Source, error) {
	if session != "session" || id != "native-run" {
		return runanalysis.Source{}, errors.New("scope mismatch")
	}
	return runanalysis.Source{Kind: "VERIFICATION", State: "REQUIRED", SourceHash: strings.Repeat("a", 64), ReportSchema: json.RawMessage(`{"type":"object"}`), Run: run.Run{ID: id, ResultProtocol: run.NaturalResultProtocol, Status: run.StatusSucceeded, Result: &run.Result{AgentClaim: evidence.AgentClaim{Message: "自然语言执行结果"}}}, Commands: []run.ObservedCommand{}}, nil
}
func (f *analysisWireFixture) SubmitRunAnalysis(_ context.Context, in runanalysis.Input) (runanalysis.Receipt, error) {
	f.submitted = append(f.submitted, in)
	return runanalysis.Receipt{RunID: in.RunID, RequestID: in.RequestID, Kind: "VERIFICATION", PayloadSHA256: runanalysis.Hash(in)}, nil
}

func TestRunAnalysisMCPWireCarriesExplicitSourceAndReport(t *testing.T) {
	f := &analysisWireFixture{surfaceReadFixture: newSurfaceReadFixture()}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "run-analysis", Version: "1"}, nil)
	registerRunAnalysisTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	read, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "run_analysis_get", Arguments: runAnalysisGetInput{AISessionID: "session", RunID: "native-run"}})
	if err != nil || read.IsError {
		t.Fatalf("read %+v %v", read, err)
	}
	raw, _ := json.Marshal(read.StructuredContent)
	var source runanalysis.Source
	if err = json.Unmarshal(raw, &source); err != nil || source.Run.Result.AgentClaim.Message != "自然语言执行结果" || source.State != "REQUIRED" {
		t.Fatalf("source %s %v", raw, err)
	}
	in := runanalysis.Input{AISessionID: "session", RunID: source.Run.ID, RequestID: "gpt-analysis", SourceHash: source.SourceHash, Report: json.RawMessage(`{"version":2,"summary":"GPT analysis"}`)}
	submitted, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "run_analysis_submit", Arguments: in})
	if err != nil || submitted.IsError || len(f.submitted) != 1 || f.submitted[0].SourceHash != source.SourceHash || runanalysis.Hash(f.submitted[0]) != runanalysis.Hash(in) {
		t.Fatalf("submit %+v %+v %v", submitted, f.submitted, err)
	}
	guessed := map[string]any{"aiSessionId": "session", "runId": "native-run", "requestId": "missing-source", "report": map[string]any{"summary": "unbound"}}
	rejected, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "run_analysis_submit", Arguments: guessed})
	if err == nil && !rejected.IsError || len(f.submitted) != 1 {
		t.Fatalf("missing source dispatched %+v %v", rejected, err)
	}
}
