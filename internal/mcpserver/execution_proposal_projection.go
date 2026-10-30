package mcpserver

import (
	"github.com/lazyun/devbridge/internal/executionproposal"
)

type ExecutionProposalOutput struct {
	AuthorizationActionable  bool                   `json:"authorizationActionable"`
	TaskDefinitionRevisionID string                 `json:"taskDefinitionRevisionId,omitempty"`
	ProvenanceKind           string                 `json:"provenanceKind,omitempty"`
	PrimaryMemberID          string                 `json:"primaryMemberId,omitempty"`
	ApprovedWorksetHash      string                 `json:"approvedWorksetHash,omitempty"`
	RuntimeAdapterIdentity   string                 `json:"runtimeAdapterIdentity,omitempty"`
	RuntimeProtocolIdentity  string                 `json:"runtimeProtocolIdentity,omitempty"`
	ID                       string                 `json:"id"`
	AISessionID              string                 `json:"aiSessionId"`
	ProjectID                string                 `json:"projectId"`
	TaskID                   string                 `json:"taskId"`
	TaskPlanID               string                 `json:"taskPlanId"`
	WorkSpec                 WorkSpecOutput         `json:"workSpec"`
	VerificationSpec         VerificationSpecOutput `json:"verificationSpec"`
	Status                   string                 `json:"status"`
	RunID                    string                 `json:"runId,omitempty"`
	ImplementationRunID      string                 `json:"implementationRunId,omitempty"`
	VerificationRunID        string                 `json:"verificationRunId,omitempty"`
	Outcome                  string                 `json:"outcome"`
	FailureCode              string                 `json:"failureCode,omitempty"`
	FailureMessage           string                 `json:"failureMessage,omitempty"`
	CreatedAt                string                 `json:"createdAt"`
	UpdatedAt                string                 `json:"updatedAt"`
	ConfirmedAt              *string                `json:"confirmedAt,omitempty"`
	RejectedAt               *string                `json:"rejectedAt,omitempty"`
	ConsumedAt               *string                `json:"consumedAt,omitempty"`
}

const (
	executionProposalOutputSchema = `{"type":"object","properties":{"authorizationActionable":{"type":"boolean"},"taskDefinitionRevisionId":{"type":"string"},"provenanceKind":{"type":"string"},"primaryMemberId":{"type":"string"},"approvedWorksetHash":{"type":"string"},"runtimeAdapterIdentity":{"type":"string"},"runtimeProtocolIdentity":{"type":"string"},"id":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"taskId":{"type":"string"},"taskPlanId":{"type":"string"},"workSpec":` + workSpecSchema + `,"verificationSpec":` + verificationSpecSchema + `,"status":{"type":"string","enum":["PENDING_CONFIRMATION","CONFIRMED","REJECTED","CONSUMED"]},"runId":{"type":"string"},"implementationRunId":{"type":"string"},"verificationRunId":{"type":"string"},"outcome":{"type":"string","enum":["PENDING","RUNNING","SUCCEEDED","FAILED","CANCELED"]},"failureCode":{"type":"string"},"failureMessage":{"type":"string","maxLength":2048},"createdAt":{"type":"string"},"updatedAt":{"type":"string"},"confirmedAt":{"type":"string"},"rejectedAt":{"type":"string"},"consumedAt":{"type":"string"}},"required":["id","aiSessionId","projectId","taskId","taskPlanId","workSpec","verificationSpec","status","outcome","createdAt","updatedAt"],"additionalProperties":false}`
)

func executionProposalOutput(value executionproposal.Proposal, details bool) ExecutionProposalOutput {
	return ExecutionProposalOutput{
		ID: value.ID, TaskDefinitionRevisionID: value.TaskDefinitionRevisionID, ProvenanceKind: value.ProvenanceKind, PrimaryMemberID: value.PrimaryMemberID, ApprovedWorksetHash: value.ApprovedWorksetHash, RuntimeAdapterIdentity: value.RuntimeAdapterIdentity, RuntimeProtocolIdentity: value.RuntimeProtocolIdentity, AISessionID: value.AISessionID, ProjectID: value.ProjectID, TaskID: value.TaskID, TaskPlanID: value.TaskPlanID,
		WorkSpec: workSpecOutput(value.WorkSpec, details), VerificationSpec: verificationSpecOutput(value.VerificationSpec, details), Status: string(value.Status), RunID: value.RunID, ImplementationRunID: value.ImplementationRunID, VerificationRunID: value.VerificationRunID, Outcome: string(value.Outcome), FailureCode: value.FailureCode, FailureMessage: boundedUTF8(value.FailureMessage, 2048), CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat),
		ConfirmedAt: formattedTime(value.ConfirmedAt), RejectedAt: formattedTime(value.RejectedAt), ConsumedAt: formattedTime(value.ConsumedAt),
	}
}
