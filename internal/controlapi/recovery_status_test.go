package controlapi

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"testing"
)

func TestRecoveryStatusReadCommandsBoundToDurableAssessment(t *testing.T) {
	r := recoverystatus.Record{ID: "id", State: "ASSESSED", RuntimeEpoch: "new-epoch", Spec: recoverystatus.Spec{Source: recoverystatus.Source{RunID: "source", TurnID: "old-turn"}, RequestID: "start-request", PayloadHash: "exact-spec-hash"}, AssessmentHash: "assessment-hash", ReportHash: "report-hash", SourceResponseDisposition: "UNKNOWN", Assessment: json.RawMessage(`{"consistency":"CONSISTENT","engineeringDisposition":"RECOVERED_COMPLETE"}`)}
	out := recoveryStatusResult(r)
	if out.StartCommand != nil || out.ResolveCommand == nil {
		t.Fatalf("bad commands: %+v", out)
	}
	in := out.ResolveCommand
	expected := recoverystatus.ResolveInput{ID: r.ID, RequestID: in.RequestID, RuntimeEpoch: r.RuntimeEpoch, AssessmentHash: r.AssessmentHash, ReportHash: r.ReportHash}
	if in.PayloadHash != expected.ComputePayloadHash() || in.Source != r.Spec.Source {
		t.Fatal("resolution command loses durable binding")
	}
	if out.Projection.SourceResponseDisposition != "UNKNOWN" {
		t.Fatal("consumption rewritten")
	}
	if recoveryStatusError(nil) != nil {
		t.Fatal("typed nil success error")
	}
	r.Assessment = json.RawMessage(`{"consistency":"CONFLICT","engineeringDisposition":"RECOVERY_INSUFFICIENT"}`)
	if blocked := recoveryStatusResult(r); blocked.ResolveCommand != nil {
		t.Fatal("conflicting assessment exposed a resolution command")
	}
	r.State, r.RecoveryRunID, r.RecoveryTurnID = "RUN_CREATED", "existing-child", ""
	if prepared := recoveryStatusResult(r); prepared.Projection.State != "PREPARED" || prepared.StartCommand == nil || prepared.Record.RecoveryRunID != "existing-child" {
		t.Fatal("persisted undispatched child lost explicit start guidance")
	}
	r.State = "UNKNOWN"
	if uncertain := recoveryStatusResult(r); uncertain.StartCommand != nil || uncertain.Projection.Eligibility {
		t.Fatal("uncertain dispatch offered another start")
	}
	r.State, r.RecoveryTurnID, r.FailureReason = "FAILED", "child-turn", "RECOVERY_CANCELED"
	r.Resolution = json.RawMessage(`{"state":"CANCELED"}`)
	if canceled := recoveryStatusResult(r); canceled.Projection.Required || canceled.Projection.DisplayTitle != "工程状态恢复已取消" || len(canceled.Projection.AvailableActions) != 1 {
		t.Fatal("canonical cancellation still shown as recovery required")
	}
}
