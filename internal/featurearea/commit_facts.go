package featurearea

import (
	"github.com/lazyun/devbridge/internal/delivery"
	"time"
)

// CommitFacts is a read-only view of existing Git checkpoints and receipts.
// It never authorizes or advances a command.
type CommitFacts struct {
	Delivery        delivery.Status  `json:"delivery"`
	Tasks           []TaskCommitFact `json:"tasks"`
	RequestID       string           `json:"requestId,omitempty"`
	Pending         bool             `json:"pending"`
	LastError       string           `json:"lastError,omitempty"`
	SourceHead      string           `json:"sourceHead,omitempty"`
	ExpectedDevHead string           `json:"expectedDevHead,omitempty"`
	WorkspaceHead   string           `json:"workspaceHead,omitempty"`
	DevHead         string           `json:"devHead,omitempty"`
}
type TaskCommitFact struct {
	// Scalar integration may complete before the containing multi-repository merge.
	MergedCommit     string          `json:"mergedCommit,omitempty"`
	MergedAt         *time.Time      `json:"mergedAt,omitempty"`
	IntegrationState string          `json:"integrationState,omitempty"`
	Delivery         delivery.Status `json:"delivery"`
	TaskID           string          `json:"taskId"`
	Pending          bool            `json:"pending"`
	LastError        string          `json:"lastError,omitempty"`
}

func (f CommitFacts) Task(id string) (TaskCommitFact, bool) {
	for _, t := range f.Tasks {
		if t.TaskID == id {
			return t, true
		}
	}
	return TaskCommitFact{}, false
}
