package task

// Phase is the durable semantic stage of a Task. Child operations supply gate
// evidence; only the canonical Task transaction changes this value.
type Phase string

const (
	PhaseReviewRequired          Phase  = "REVIEW_REQUIRED"
	PhaseReviewing               Phase  = "REVIEWING"
	PhaseReviewApprovalPending   Phase  = "REVIEW_APPROVAL_PENDING"
	PhaseImplementationReady     Phase  = "IMPLEMENTATION_READY"
	PhaseImplementing            Phase  = "IMPLEMENTING"
	PhaseVerificationReady       Phase  = "VERIFICATION_READY"
	PhaseVerifying               Phase  = "VERIFYING"
	PhaseAcceptancePending       Phase  = "ACCEPTANCE_PENDING"
	PhaseDelivering              Phase  = "DELIVERING"
	PhaseRecoveryRequired        Phase  = "RECOVERY_REQUIRED"
	PhaseNoRunSuccessorRequired  Phase  = "NO_RUN_SUCCESSOR_REQUIRED"
	PhaseImplementationPreparing Phase  = "IMPLEMENTATION_PREPARING"
	StatusActive                 Status = "ACTIVE"
)

func (p Phase) Valid() bool {
	switch p {
	case PhaseReviewRequired, PhaseReviewing, PhaseReviewApprovalPending,
		PhaseImplementationReady, PhaseImplementing, PhaseVerificationReady,
		PhaseVerifying, PhaseAcceptancePending, PhaseDelivering, PhaseRecoveryRequired, PhaseNoRunSuccessorRequired, PhaseImplementationPreparing:
		return true
	}
	return false
}

func (p Phase) ReviewStage() bool {
	return p == PhaseReviewRequired || p == PhaseReviewing || p == PhaseReviewApprovalPending
}
