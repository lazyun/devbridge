package review

import "github.com/lazyun/devbridge/internal/task"

// ValidateProposalAdmission is a new-publication check. Historical reports
// continue to decode with their original structural contract.
func ValidateProposalAdmission(result ImplementationResult) error {
	if result.ReviewDisposition != DispositionApprovalReady {
		return nil
	}
	return task.ValidateExecutableVerificationSpec(result.VerificationSpec)
}

func (r ReviewReport) ValidateAdmission() error {
	if err := r.Validate(); err != nil {
		return err
	}
	return ValidateProposalAdmission(r.CompatibilityResult())
}
