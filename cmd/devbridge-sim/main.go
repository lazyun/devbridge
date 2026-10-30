// devbridge-sim runs isolated deterministic actors against the real Core.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/lazyun/devbridge/test/simulation"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	var c simulation.Config
	flag.StringVar(&c.BinDir, "bin-dir", "", "directory containing devbridge, devbridge-mcp and devbridge-local-mcp")
	flag.StringVar(&c.EvidenceDir, "evidence-dir", "", "parent for a new private evidence directory (default system temp)")
	flag.StringVar(&c.Scenario, "scenario", "all", "all or "+strings.Join(simulation.Scenarios(), ", "))
	flag.DurationVar(&c.Timeout, "timeout", 5*time.Minute, "timeout per scenario")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	report, err := simulation.Run(ctx, c)
	if report.Directory != "" {
		fmt.Printf("Simulation evidence: %s\n", report.Directory)
	}
	for _, scenario := range report.Scenarios {
		fmt.Printf("%s %s (%d checks)\n", scenario.Status, scenario.Name, len(scenario.Checks))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("PASS: real Core; simulated GPT/user/pull workers; no models or native verification")
}
