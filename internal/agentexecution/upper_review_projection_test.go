package agentexecution

import (
	"github.com/lazyun/devbridge/internal/run"
	"testing"
)

func TestUpperReportPrecedenceKeepsRuntimeEvidenceWithoutActionableRecovery(t *testing.T) {
	e, r := exactFixture(KindReview)
	for _, disposition := range []string{"UNKNOWN"} {
		f := Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, Runtime: map[string]RuntimeFacts{r.ID: {BackendDisposition: disposition, UpperInterruptionSuperseded: true}}}
		p := Reduce(e, f)
		if p.Status != "COMPLETED" || p.RecoveryRequired || p.BackendDisposition != disposition {
			t.Fatalf("current report contradicted by historical runtime: %+v", p)
		}
		f.Runtime[r.ID] = RuntimeFacts{BackendDisposition: disposition}
		if got := Reduce(e, f); got.Status != disposition {
			t.Fatalf("native uncertainty lost: %+v", got)
		}
		f.Runtime[r.ID] = RuntimeFacts{BackendDisposition: disposition, UpperInterruptionSuperseded: true}
		f.CurrentRunID = "other-run"
		if got := Reduce(e, f); got.Status == "COMPLETED" {
			t.Fatalf("borrowed another report: %+v", got)
		}
	}
	r.Status = run.StatusRunning
	if got := Reduce(e, Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, Runtime: map[string]RuntimeFacts{r.ID: {BackendDisposition: "UNKNOWN", UpperInterruptionSuperseded: true}}}); got.Status != "RUNNING" || got.RecoveryRequired {
		t.Fatalf("reauthorized GPT still marked recovery: %+v", got)
	}
	r.Status = run.StatusRunning
	if got := Reduce(e, Facts{Runs: []run.Run{r}, CurrentRunID: r.ID, BackendDisposition: "UNKNOWN"}); got.Status != "UNKNOWN" || !got.RecoveryRequired {
		t.Fatalf("active run falsely completed: %+v", got)
	}
}
