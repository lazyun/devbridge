package recoverystatus

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestRecoveryReviewReportRoundTripPreservesStrictOptionals(t *testing.T) {
	r, f := factFixture(t)
	r.SourceOperationKind = "REVIEW"
	f.Source.OperationKind = "REVIEW"
	member := f.Workset.Members[0]
	work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Implement reviewed change", Constraints: []string{"Keep existing behavior"}, CodexInstruction: "Update file.txt", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "changed file", CodexInstruction: "Verify the change", AcceptanceCriteria: []string{"file exists"}, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "test -f file.txt", Required: true, RepositoryID: member.RepositoryID, RepositoryIDs: []string{member.RepositoryID}, WorkingDirectory: "."}}}
	rr := &review.ReviewReport{SchemaVersion: 2, Summary: "Ready", Disposition: review.DispositionApprovalReady, RepositoryTargets: []workset.Target{{RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: 1, OriginCommit: member.StartHEAD, Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"file.txt"}}}}, PrimaryMemberID: member.MemberID, Coverage: []string{"file.txt"}, Findings: []review.Finding{}, Limitations: []string{"No implementation yet"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{{EvidenceID: "ref", RepositoryID: member.RepositoryID, MemberID: member.MemberID, Path: "file.txt", Digest: Hash([]byte("evidence"))}}, WorkSpec: &work, VerificationSpec: &verification}
	if err := rr.Validate(); err != nil {
		t.Fatalf("invalid typed Review fixture: %v", err)
	}
	r.ReviewReport = rr
	standaloneBefore, _ := json.Marshal(rr)
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseReport(raw, f.Source)
	if err != nil {
		t.Fatalf("typed Review did not preserve strict wire shape: %v", err)
	}
	if _, err = Assess(parsed, f); err != nil {
		t.Fatalf("parsed strict report cannot be assessed: %v", err)
	}
	standaloneAfter, _ := json.Marshal(rr)
	if string(standaloneBefore) != string(standaloneAfter) {
		t.Fatal("strict encoding mutated standalone Review")
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	embedded := object["reviewReport"].(map[string]any)
	checks := embedded["verificationSpec"].(map[string]any)["checks"].([]any)
	// Original wire validation must still reject omission, even though typed
	// re-encoding restores optional fields as explicit nulls.
	delete(checks[0].(map[string]any), "instruction")
	malformed, _ := json.Marshal(object)
	if _, err = ParseReport(malformed, f.Source); err == nil {
		t.Fatal("external wire omission was normalized before validation")
	}
}
