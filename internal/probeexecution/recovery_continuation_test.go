package probeexecution

import "testing"

func TestInspectionContinuationPayloadIdentity(t *testing.T) {
	in := InspectionContinuationInput{RecoveryStatusID: "recovery", RequestID: "request", Instruction: "exact bytes\n", ExpectedRuntimeEpoch: "epoch"}
	hash := in.ComputePayloadHash()
	in.ActorID = "authenticated-actor"
	in.PayloadHash = hash
	if in.ComputePayloadHash() != hash {
		t.Fatal("server actor/checksum changed caller payload")
	}
	for _, mutate := range []func(*InspectionContinuationInput){func(v *InspectionContinuationInput) { v.Instruction += " " }, func(v *InspectionContinuationInput) { v.ExpectedRuntimeEpoch = "other" }, func(v *InspectionContinuationInput) { v.RequestID = "other" }, func(v *InspectionContinuationInput) { v.RecoveryStatusID = "other" }} {
		changed := in
		mutate(&changed)
		if changed.ComputePayloadHash() == hash {
			t.Fatal("continuation identity change did not alter payload")
		}
	}
}
