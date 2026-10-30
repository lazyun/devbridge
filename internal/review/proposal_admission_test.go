package review

import (
	"errors"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestPublicationChecksExecutablePlanWithoutChangingHistoricalDecode(t *testing.T) {
	w := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Implement approved work", Constraints: []string{}, CodexInstruction: "Implement original scope", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeRepository}}
	v := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "repository", CodexInstruction: "Verify", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Instruction: "run tests", Required: true}}, AcceptanceCriteria: []string{"tests pass"}}
	r := ReviewReport{SchemaVersion: 1, Summary: "Ready", Disposition: DispositionApprovalReady, Coverage: []string{"repository"}, Findings: []Finding{}, Limitations: []string{"source only"}, RecommendedNextSteps: []string{"approve"}, EvidenceRefs: []EvidenceRef{}, WorkSpec: &w, VerificationSpec: &v}
	if err := r.Validate(); err != nil {
		t.Fatalf("historical report must remain readable: %v", err)
	}
	if err := r.ValidateAdmission(); !errors.Is(err, task.ErrUnobservableRequiredCheck) {
		t.Fatalf("unexecutable proposal published: %v", err)
	}
	v.Checks[0].Instruction = ""
	v.Checks[0].Command = "go test ./..."
	if err := r.ValidateAdmission(); err != nil {
		t.Fatal(err)
	}
	v.Checks[0] = task.VerificationCheck{Kind: task.VerificationCheckCustom, Instruction: "Assess boundary contract", Required: true}
	if err := r.ValidateAdmission(); err != nil {
		t.Fatalf("required CUSTOM analysis was weakened: %v", err)
	}
}
