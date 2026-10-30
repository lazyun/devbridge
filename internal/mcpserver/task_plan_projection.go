package mcpserver

import (
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
)

type taskCreateInput struct {
	FeatureAreaID    string `json:"featureAreaId"`
	AISessionID      string `json:"aiSessionId"`
	RepositoryID     string `json:"repositoryId"`
	CodexConfigID    string `json:"codexConfigId"`
	ModuleID         string `json:"moduleId"`
	ProblemStatement string `json:"problemStatement"`
	BaseRevision     string `json:"baseRevision"`
}

type repositorySearchInput struct {
	AISessionID  string   `json:"aiSessionId"`
	RepositoryID string   `json:"repositoryId"`
	Query        string   `json:"query"`
	Paths        []string `json:"paths,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}
type RepositorySearchMatchOutput struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Snippet string `json:"snippet"`
}
type RepositorySearchOutput struct {
	Matches   []RepositorySearchMatchOutput `json:"matches"`
	Truncated bool                          `json:"truncated"`
}

type taskPlanCreateInput struct {
	AISessionID      string                `json:"aiSessionId"`
	TaskID           string                `json:"taskId"`
	WorkSpec         task.WorkSpec         `json:"workSpec"`
	VerificationSpec task.VerificationSpec `json:"verificationSpec"`
}
type TaskPlanOutput struct {
	ID               string                 `json:"id"`
	AISessionID      string                 `json:"aiSessionId"`
	ProjectID        string                 `json:"projectId"`
	TaskID           string                 `json:"taskId"`
	Title            string                 `json:"title"`
	WorkSpec         WorkSpecOutput         `json:"workSpec"`
	VerificationSpec VerificationSpecOutput `json:"verificationSpec"`
	Status           string                 `json:"status"`
	CreatedAt        string                 `json:"createdAt"`
	UpdatedAt        string                 `json:"updatedAt"`
	ConfirmedAt      *string                `json:"confirmedAt,omitempty"`
	RejectedAt       *string                `json:"rejectedAt,omitempty"`
	AppliedAt        *string                `json:"appliedAt,omitempty"`
}
type scopeReviewStartInput struct {
	AISessionID      string           `json:"aiSessionId"`
	ReviewID         string           `json:"reviewId"`
	Scope            reviewScopeInput `json:"scope"`
	CodexInstruction string           `json:"codexInstruction"`
}
type reviewScopeInput struct {
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitempty"`
}

type ScopeReviewContinuationOutput struct {
	Mode                                   string `json:"mode"`
	ModelPollingRequired                   bool   `json:"modelPollingRequired"`
	SemanticContinuationRequiredOnTerminal bool   `json:"semanticContinuationRequiredOnTerminal"`
}

type ScopeReviewStartOutput struct {
	Run          RunOutput                     `json:"run"`
	Continuation ScopeReviewContinuationOutput `json:"continuation"`
}

const (
	repositorySearchInputSchema  = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","minLength":1,"maxLength":128},"query":{"type":"string","minLength":1,"maxLength":4096},"paths":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1,"maxLength":4096}},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["aiSessionId","query"],"additionalProperties":false}`
	repositorySearchOutputSchema = `{"type":"object","properties":{"matches":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"path":{"type":"string","maxLength":4096},"line":{"type":"integer","minimum":1},"snippet":{"type":"string","maxLength":512}},"required":["path","line","snippet"],"additionalProperties":false}},"truncated":{"type":"boolean"}},"required":["matches","truncated"],"additionalProperties":false}`
	taskPlanOutputSchema         = `{"type":"object","properties":{"id":{"type":"string"},"aiSessionId":{"type":"string"},"projectId":{"type":"string"},"taskId":{"type":"string"},"title":{"type":"string","maxLength":256},"workSpec":` + workSpecSchema + `,"verificationSpec":` + verificationSpecSchema + `,"status":{"type":"string","enum":["PENDING_CONFIRMATION","CONFIRMED","REJECTED","APPLIED"]},"createdAt":{"type":"string"},"updatedAt":{"type":"string"},"confirmedAt":{"type":"string"},"rejectedAt":{"type":"string"},"appliedAt":{"type":"string"}},"required":["id","aiSessionId","projectId","taskId","title","workSpec","verificationSpec","status","createdAt","updatedAt"],"additionalProperties":false}`
	scopeReviewStartInputSchema  = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","minLength":1,"maxLength":128},"scope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string","minLength":1,"maxLength":4096}}},"required":["kind"],"additionalProperties":false},"codexInstruction":{"type":"string","minLength":1,"maxLength":65536}},"required":["aiSessionId","reviewId","scope","codexInstruction"],"additionalProperties":false}`
	scopeReviewStartOutputSchema = `{"type":"object","properties":{"run":` + runOutputSchema + `,"continuation":{"type":"object","properties":{"mode":{"type":"string","enum":["APP_MONITORED"]},"modelPollingRequired":{"type":"boolean"},"semanticContinuationRequiredOnTerminal":{"type":"boolean"}},"required":["mode","modelPollingRequired","semanticContinuationRequiredOnTerminal"],"additionalProperties":false}},"required":["run","continuation"],"additionalProperties":false}`
)

func taskPlanOutput(value taskplan.Plan, details bool) TaskPlanOutput {
	return TaskPlanOutput{ID: value.ID, AISessionID: value.AISessionID, ProjectID: value.ProjectID, TaskID: value.TaskID, Title: boundedUTF8(value.Title, 256), WorkSpec: workSpecOutput(value.WorkSpec, details), VerificationSpec: verificationSpecOutput(value.VerificationSpec, details), Status: string(value.Status), CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat), ConfirmedAt: formattedTime(value.ConfirmedAt), RejectedAt: formattedTime(value.RejectedAt), AppliedAt: formattedTime(value.AppliedAt)}
}
