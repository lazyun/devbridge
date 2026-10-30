package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type GateID string

const (
	GateNormalExecution      GateID = "REAL-E2E-01"
	GateRuntimeApproval      GateID = "REAL-E2E-02"
	GateActiveInterruption   GateID = "REAL-E2E-03"
	GateApprovalInterruption GateID = "REAL-E2E-04"
	GateParallelIsolation    GateID = "REAL-E2E-05"
	GateWorkspaceContinuity  GateID = "REAL-E2E-06"
	GateWorkVerification     GateID = "REAL-E2E-07"
	GateExecutionScope       GateID = "REAL-E2E-08"
	GateTaskFirstScopeReview GateID = "REAL-E2E-09"
)

type GateStatus string

const (
	StatusPass    GateStatus = "PASS"
	StatusFail    GateStatus = "FAIL"
	StatusSkipped GateStatus = "SKIPPED"
	StatusBlocked GateStatus = "BLOCKED"
)

// Evidence is deliberately string based so results remain stable and useful to
// shell tooling without exposing production domain types.
type Evidence map[string]string

type GateResult struct {
	GateID     GateID     `json:"gate_id"`
	Status     GateStatus `json:"status"`
	Reason     string     `json:"reason"`
	Evidence   Evidence   `json:"evidence,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
}

func (r GateResult) Validate() error {
	if r.GateID == "" {
		return fmt.Errorf("gate id is required")
	}
	switch r.Status {
	case StatusPass, StatusFail, StatusSkipped, StatusBlocked:
	default:
		return fmt.Errorf("invalid gate status %q", r.Status)
	}
	if r.Reason == "" {
		return fmt.Errorf("gate reason is required")
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) {
		return fmt.Errorf("invalid gate timestamps")
	}
	return nil
}

func WriteResults(w io.Writer, results []GateResult) error {
	for _, result := range results {
		if err := result.Validate(); err != nil {
			return fmt.Errorf("validate %s: %w", result.GateID, err)
		}
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(results)
}
