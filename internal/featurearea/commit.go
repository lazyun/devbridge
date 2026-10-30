package featurearea

import (
	"errors"
	"github.com/lazyun/devbridge/internal/delivery"
	"time"
)

var ErrCommitScopeConflict = errors.New("SCOPE_CONFLICT")

type CommitCapability struct {
	MergeRetryOperation *delivery.MergeRetryCandidate `json:"mergeRetryOperation,omitempty"`
	CanRecoverMerge     bool                          `json:"canRecoverMerge"`
	MergeRecoveryState  string                        `json:"mergeRecoveryState,omitempty"`
	RetryOperation      *delivery.RetryCandidate      `json:"retryOperation,omitempty"`
	CanRetry            bool                          `json:"canRetry"`
	MergedAt            *time.Time                    `json:"mergedAt,omitempty"`

	CommitStatus      string `json:"commitStatus"`
	MergedCommit      string `json:"mergedCommit"`
	CanCommit         bool   `json:"canCommit"`
	Reason            string `json:"reason,omitempty"`
	DevelopmentBranch string `json:"developmentBranch,omitempty"`
	RetryTaskID       string `json:"retryTaskId,omitempty"`
	RequestID         string `json:"requestId,omitempty"`
}
