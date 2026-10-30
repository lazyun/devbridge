package harness

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGateResultJSONIsMachineReadable(t *testing.T) {
	now := time.Date(2026, 9, 3, 1, 2, 3, 0, time.UTC)
	result := GateResult{GateID: GateNormalExecution, Status: StatusPass, Reason: "verified", Evidence: Evidence{"diff": "ok"}, StartedAt: now, FinishedAt: now.Add(time.Second)}
	var output bytes.Buffer
	if err := WriteResults(&output, []GateResult{result}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"gate_id": "REAL-E2E-01"`, `"status": "PASS"`, `"diff": "ok"`} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("result JSON missing %s: %s", want, output.String())
		}
	}
}

func TestGateResultRejectsFakeStatus(t *testing.T) {
	now := time.Now()
	if err := (GateResult{GateID: GateNormalExecution, Status: "DEFERRED", Reason: "no", StartedAt: now, FinishedAt: now}).Validate(); err == nil {
		t.Fatal("invalid status accepted")
	}
}
