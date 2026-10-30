package formalexecution

import (
	"context"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// AdoptionStartRequest consumes an existing completed admission. Its durable
// identity is independent from the confirmation that granted that admission.
type AdoptionStartRequest struct {
	AISessionID    string                            `json:"aiSessionId"`
	TaskID         string                            `json:"taskId"`
	RequestID      string                            `json:"requestId"`
	ExpectedTarget wp.TaskExecutionExpectation       `json:"expectedTarget"`
	RuntimeConfig  codexruntime.NextRunRuntimeConfig `json:"runtimeConfig"`
	Backend        codexconfig.BackendSelectionInput `json:"backend"`
}

type AdoptionStartStore interface {
	ReplayAdoptionStart(context.Context, AdoptionStartRequest) (confirmation.Intent, bool, error)
	BindAdoptionStart(context.Context, AdoptionStartRequest) (confirmation.Intent, bool, error)
}
