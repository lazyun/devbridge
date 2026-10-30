package controlapi

import (
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

const requestChangesRequiresReview = "REQUEST_CHANGES_REQUIRES_FORMAL_REVIEW"
const operatorStartRequiresConfirmation = "FORMAL_START_CONFIRMATION_REQUIRED"

func v2CodingDefinition(t task.Task) bool {
	return t.CurrentDefinitionRevisionID != "" && len(t.RepositoryTargets) > 0 && t.WorkSpec.ImplementationMode == task.ImplementationCodingAgent
}

func requestChangesRequiresReviewFailure() *confirmation.Failure {
	return confirmation.NewFailure(requestChangesRequiresReview, "CORE", "CONFIRMATION_PREFLIGHT", "Changing this Coding definition requires a supplemental Review and a new formal approval. Request a supplemental Review instead of REQUEST_CHANGES.", confirmation.ReadCurrentTarget, nil)
}

func operatorStartRequiresConfirmationFailure() *Error {
	return &Error{Code: ErrorCode(operatorStartRequiresConfirmation), Message: "This definition has no formally prepared execution. Use START_APPROVED confirmation from its authorized AI Session, or request a new Review and formal approval; an operator selection cannot create runtime authority."}
}
