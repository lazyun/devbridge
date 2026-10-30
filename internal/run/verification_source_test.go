package run

import "testing"

func TestRunClonePreservesVerificationProvenance(t *testing.T) {
	source := &VerificationObservationSource{Kind: "COMMAND", RunID: "verification", RepositoryID: "app", CWD: "/managed/app"}
	original := Run{VerificationReport: &VerificationReport{Checks: []VerificationCheckOutcome{{Observed: &VerificationObservedFact{Source: source}}}}}
	copy := clone(original)
	copy.VerificationReport.Checks[0].Observed.Source.RepositoryID = "sdk"
	copy.VerificationReport.Checks[0].Observed.Source.CWD = "/managed/sdk"
	if source.RepositoryID != "app" || source.CWD != "/managed/app" {
		t.Fatal("Run readback aliases stored verification provenance")
	}
}
