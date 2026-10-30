package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lazyun/devbridge/test/real_e2e/harness"
)

func main() {
	root := flag.String("root", "", "fixture/artifact root (default: temporary directory)")
	keep := flag.Bool("keep", false, "preserve artifacts even when every gate passes or skips")
	gate := flag.String("gate", "", "run one gate by ID (default: all gates)")
	requirePass := flag.Bool("require-pass", false, "exit unsuccessfully unless every gate passes")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall gate timeout")
	bridge := flag.String("bridge", os.Getenv("DEVBRIDGE_REAL_E2E_BRIDGE"), "path to the production Dev Bridge executable")
	mcpBridge := flag.String("mcp", os.Getenv("DEVBRIDGE_REAL_E2E_MCP"), "path to the production Dev Bridge MCP executable")
	codex := flag.String("codex", os.Getenv("DEVBRIDGE_REAL_E2E_CODEX"), "optional absolute path to the real Codex executable")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	var adapter harness.Adapter
	if *bridge != "" {
		adapter = &harness.RealAdapter{BridgePath: *bridge, MCPPath: *mcpBridge, CodexPath: *codex}
	}
	var only []harness.GateID
	if *gate != "" {
		selected := harness.GateID(*gate)
		switch selected {
		case harness.GateNormalExecution, harness.GateRuntimeApproval, harness.GateActiveInterruption, harness.GateApprovalInterruption, harness.GateParallelIsolation, harness.GateWorkspaceContinuity, harness.GateWorkVerification, harness.GateExecutionScope, harness.GateTaskFirstScopeReview:
			only = []harness.GateID{selected}
		default:
			fmt.Fprintln(os.Stderr, "unknown gate:", *gate)
			os.Exit(2)
		}
	}
	results, preservedRoot, err := (harness.Suite{Adapter: adapter, Root: *root, Keep: *keep, Only: only}).Run(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "real E2E harness error:", err)
		os.Exit(2)
	}
	if err := harness.WriteResults(os.Stdout, results); err != nil {
		fmt.Fprintln(os.Stderr, "write gate results:", err)
		os.Exit(2)
	}
	if preservedRoot != "" {
		fmt.Fprintln(os.Stderr, "artifacts preserved at", preservedRoot)
	}
	if *requirePass {
		for _, result := range results {
			if result.Status != harness.StatusPass {
				os.Exit(1)
			}
		}
	}
}
