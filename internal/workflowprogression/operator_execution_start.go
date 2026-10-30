package workflowprogression

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
)

// OperatorExecutionStartRequest is a local operator's explicit first start of
// a definition-only approval. Actor identity comes exclusively from trusted
// localoperator context. It is not a platform qualification or formal source.
type OperatorExecutionStartRequest struct {
	ProjectID        string                            `json:"projectId"`
	TaskID           string                            `json:"taskId"`
	RequestID        string                            `json:"requestId"`
	Expected         TaskExecutionExpectation          `json:"expected"`
	RuntimeConfig    codexruntime.NextRunRuntimeConfig `json:"runtimeConfig"`
	BackendSelection codexconfig.BackendSelectionInput `json:"backendSelection"`
}

func (v OperatorExecutionStartRequest) Hash() string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type OperatorExecutionStartStore interface {
	// Read is receipt-only and performs no live model, owner or writer check.
	ReadOperatorSelectedTaskExecution(context.Context, OperatorExecutionStartRequest) (executionproposal.Proposal, run.Run, bool, error)
	// Reserve atomically commits the explicit command, candidate, start effect,
	// ScopePlan and admission; Run preparation follows that durable boundary.
	ReserveOperatorSelectedTaskExecution(context.Context, OperatorExecutionStartRequest, codexconfig.BackendSelection) (executionproposal.Proposal, run.Run, error)
}
