package workflowpresentation

import (
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/agentexecution"
	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopeexpansion"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/taskplan"
	"github.com/lazyun/devbridge/internal/workspace"
)

// DetailsPage is a client-neutral, read-only history page. Every embedded domain
// value retains its durable identity and provenance; it is never a current
// workflow selector or a mutable UI state model.
type DetailsPage struct {
	AgentExecutions         []agentexecution.Projection         `json:"agentExecutions"`
	SchemaVersion           string                              `json:"schemaVersion"`
	FeatureArea             featurearea.FeatureArea             `json:"featureArea"`
	DevelopmentContext      DevelopmentContext                  `json:"developmentContext"`
	Reviews                 []review.Review                     `json:"reviews"`
	Tasks                   []task.Task                         `json:"tasks"`
	TaskPlans               []taskplan.Plan                     `json:"taskPlans"`
	TaskDefinitionRevisions []task.DefinitionRevision           `json:"taskDefinitionRevisions"`
	Executions              []executionproposal.Proposal        `json:"executions"`
	Runs                    []run.Run                           `json:"runs"`
	Workspaces              []workspace.Workspace               `json:"workspaces"`
	Approvals               []approval.Approval                 `json:"approvals"`
	TaskDecisionReceipts    []decisionresolution.Receipt        `json:"taskDecisionReceipts"`
	CancellationReceipts    []executioncancel.Receipt           `json:"cancellationReceipts"`
	RecoveryConversations   []agentconversation.Conversation    `json:"recoveryConversations"`
	RecoveryReceipts        []agentconversation.RecoveryReceipt `json:"recoveryReceipts"`
	ScopeExpansions         []scopeexpansion.Expansion          `json:"scopeExpansions"`
	SectionTotals           map[string]int                      `json:"sectionTotals"`
	SectionHasMore          map[string]bool                     `json:"sectionHasMore"`
	Limit                   int                                 `json:"limit"`
	Offset                  int                                 `json:"offset"`
	HasPrevious             bool                                `json:"hasPrevious"`
	HasNext                 bool                                `json:"hasNext"`
}
