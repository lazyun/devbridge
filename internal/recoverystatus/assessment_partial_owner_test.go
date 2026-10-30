package recoverystatus

import "testing"

func TestPartialRecoveryRequiresSupportedSourceOwner(t *testing.T) {
	for _, tc := range []struct {
		purpose, operation string
		supported          bool
	}{
		{"IMPLEMENTATION", "EXECUTION", true},
		{"VERIFICATION", "EXECUTION", false},
		{"SCOPE_REVIEW", "REVIEW", false},
		{"IMPLEMENTATION", "REVIEW", false},
		{"VERIFICATION", "IMPLEMENTATION", false},
		{"", "EXECUTION", false},
		{"", "IMPLEMENTATION", true},
	} {
		t.Run(tc.operation+"/"+tc.purpose, func(t *testing.T) {
			r, f := factFixture(t)
			f.SourcePurpose = tc.purpose
			f.Source.OperationKind = tc.operation
			r.SourceOperationKind = tc.operation
			r.OverallStatus = "PARTIAL"
			r.CompletedGoals = []GoalObservation{}
			r.RemainingGoals = []GoalObservation{{GoalID: "approved-goal", Status: "PARTIAL", EvidenceSummary: "source work is incomplete"}}
			a, err := Assess(r, f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.supported {
				if a.EngineeringDisposition != "RECOVERED_PARTIAL" {
					t.Fatalf("supported partial blocked: %+v", a)
				}
			} else {
				if a.Consistency != "INSUFFICIENT" || a.EngineeringDisposition != "RECOVERY_INSUFFICIENT" {
					t.Fatalf("unsupported partial acquired resolution: %+v", a)
				}
				p := ProjectProjection(Projection{State: "ASSESSED", Required: true, Consistency: a.Consistency, EngineeringDisposition: a.EngineeringDisposition, ReasonCodes: a.ReasonCodes})
				for _, action := range p.AvailableActions {
					if action == "RESOLVE_RECOVERY_STATUS" {
						t.Fatal("unsupported partial offers resolution")
					}
				}
			}
		})
	}
}
