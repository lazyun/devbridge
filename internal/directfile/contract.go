// Package directfile implements explicitly authorized, whole-file operations.
package directfile

import (
	"context"
	"github.com/lazyun/devbridge/internal/task"
	"os"
	"time"

	"github.com/lazyun/devbridge/internal/review"
)

const (
	MaxReadBytes  = 256 << 10
	MaxWriteBytes = 256 << 10
	// MaxBase64EncodedBytes is the largest RFC 4648 padded encoding whose
	// decoded payload can still fit MaxWriteBytes. It bounds decoder allocation;
	// MaxWriteBytes remains the authority for the decoded content.
	MaxBase64EncodedBytes    = (MaxWriteBytes + 2) / 3 * 4
	MaxListEntries           = 256
	MaxPathBytes             = 4096
	NotEnabled               = "DIRECT_FILE_CAPABILITY_NOT_ENABLED"
	CapabilityActive         = "DIRECT_FILE_CAPABILITY_ACTIVE"
	CodexActive              = "CODEX_OPERATION_ACTIVE"
	WorktreeUnavailable      = "DIRECT_FILE_WORKTREE_UNAVAILABLE"
	InvalidPath              = "INVALID_RELATIVE_PATH"
	OutsideWorktree          = "PATH_OUTSIDE_WORKTREE"
	SymlinkEscape            = "SYMLINK_ESCAPE"
	NotFound                 = "FILE_NOT_FOUND"
	AlreadyExists            = "FILE_ALREADY_EXISTS"
	InvalidTarget            = "INVALID_FILE_TARGET"
	DestinationAlreadyExists = "DESTINATION_ALREADY_EXISTS"
	TooLarge                 = "FILE_TOO_LARGE"
	OperationFailed          = "FILE_OPERATION_FAILED"
	// CancellationMutationUnresolved proves only that cancellation did not
	// start: an earlier file effect still needs its own durable receipt proof.
	CancellationMutationUnresolved = "DIRECT_FILE_CANCELLATION_MUTATION_UNRESOLVED"
)

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string           { return e.Code + ": " + e.Message }
func Failure(code, message string) error { return &Error{Code: code, Message: message} }

// Scope identifies one explicit Upper LLM authorization workflow. A fresh
// workflow ID is required for each unrelated user request.
type Scope struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId"`
	WorkflowID    string `json:"workflowId"`
}
type Input struct {
	WorkerClaim *WorkerClaimIdentity `json:"workerClaim,omitempty"`
	// RequestID, RuntimeEpoch and GrantGeneration fence a v2 task grant.  They
	// are deliberately optional here: standalone and historical v1 callers use
	// the same small file API, while the durable authority requires them for a
	// protocol-v2 mutation.
	RequestID               string `json:"requestId,omitempty"`
	RuntimeEpoch            string `json:"runtimeEpoch,omitempty"`
	GrantGeneration         int64  `json:"grantGeneration,omitempty"`
	ExpectedDigest          string `json:"expectedDigest,omitempty"`
	RepositoryID            string `json:"repositoryId,omitempty"`
	MemberID                string `json:"memberId,omitempty"`
	MemberVersion           int    `json:"memberVersion,omitempty"`
	DestinationRepositoryID string `json:"destinationRepositoryId,omitempty"`
	DestinationMemberID     string `json:"destinationMemberId,omitempty"`

	Scope
	// ReviewTarget binds a read to an admitted Upper LLM review. Generic file
	// mutation endpoints must reject an input carrying this context.
	ReviewTarget     *review.UpperTarget `json:"reviewTarget,omitempty"`
	Path             string              `json:"path,omitempty"`
	Destination      string              `json:"destination,omitempty"`
	Content          string              `json:"content,omitempty"`
	ContentEncoding  string              `json:"contentEncoding,omitempty"`
	WorkflowComplete bool                `json:"workflowComplete,omitempty"`
}
type Registration struct {
	AISessionID              string `json:"aiSessionId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	DecisionID               string `json:"decisionId"`
	ImplementationMode       string `json:"implementationMode"`
	Confirmed                bool   `json:"confirmed"`
}

type State struct {
	State                    string              `json:"state"`
	AISessionID              string              `json:"aiSessionId,omitempty"`
	TaskID                   string              `json:"taskId,omitempty"`
	TaskDefinitionRevisionID string              `json:"taskDefinitionRevisionId,omitempty"`
	DecisionID               string              `json:"decisionId,omitempty"`
	WorkspaceID              string              `json:"workspaceId,omitempty"`
	AllowedOperations        []string            `json:"allowedOperations"`
	ExecutionScope           task.ExecutionScope `json:"executionScope"`
	FailureCode              string              `json:"failureCode,omitempty"`
	FailureReason            string              `json:"failureReason,omitempty"`
	StartedAt                *time.Time          `json:"startedAt,omitempty"`
	LastOperation            string              `json:"lastOperation,omitempty"`
	Enabled                  bool                `json:"enabled"`
	ProjectID                string              `json:"projectId,omitempty"`
	RepositoryID             string              `json:"repositoryId,omitempty"`
	FeatureAreaID            string              `json:"featureAreaId,omitempty"`
	WorktreeID               string              `json:"worktreeId,omitempty"`
	WorkflowID               string              `json:"workflowId,omitempty"`
	ProtocolVersion          int                 `json:"protocolVersion,omitempty"`
	RuntimeEpoch             string              `json:"runtimeEpoch,omitempty"`
	GrantGeneration          int64               `json:"grantGeneration,omitempty"`
	RepositoryTargets        []MemberGrant       `json:"repositoryTargets,omitempty"`
}
type Entry struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type Result struct {
	RepositoryID     string  `json:"repositoryId,omitempty"`
	MemberID         string  `json:"memberId,omitempty"`
	MemberVersion    int     `json:"memberVersion,omitempty"`
	SourceKind       string  `json:"sourceKind,omitempty"`
	WorkspaceID      string  `json:"workspaceId,omitempty"`
	WorktreeID       string  `json:"worktreeId,omitempty"`
	OriginCommit     string  `json:"originCommit,omitempty"`
	StartSnapshot    string  `json:"startSnapshot,omitempty"`
	Path             string  `json:"path"`
	Content          string  `json:"content"`
	ContentEncoding  string  `json:"contentEncoding,omitempty"`
	Size             int64   `json:"size"`
	Entries          []Entry `json:"entries,omitempty"`
	Truncated        bool    `json:"truncated,omitempty"`
	WorkflowComplete bool    `json:"workflowComplete"`
	EvidenceID       string  `json:"evidenceId,omitempty"`
	Digest           string  `json:"digest,omitempty"`
}

// Authority uses the existing Core admission boundary to validate and hold
// the exact canonical worktree throughout a bounded file operation.
type Authority interface {
	Enable(context.Context, Scope) (State, error)
	Disable(context.Context, Scope) (State, error)
	WithCapability(context.Context, Scope, bool, func(*os.Root) error) error
	Status(context.Context, string, string) (State, error)
}

// TaskAuthority is the existing authority's exact approved-task read/maintenance extension.
type TaskAuthority interface {
	TaskStatus(context.Context, string, string) (State, error)
	ReconcileTaskAuthorization(context.Context, Registration) (State, error)
}

// TaskStartAuthority is deliberately an extension rather than an addition to
// TaskAuthority: v1 adapters and tests remain valid while the v2 confirmation
// route requires this stronger authority.
type TaskStartAuthority interface {
	StartTaskAuthorization(context.Context, StartInput) (State, error)
}

type operationKey struct{}
type resultCaptureKey struct{}
type resultCapture struct{ value Result }
type Operation struct {
	// Omission preserves historical mutation payload hashes exactly.
	WorkerClaim                                  *WorkerClaimIdentity `json:"workerClaim,omitempty"`
	RepositoryID, MemberID                       string
	MemberVersion                                int
	DestinationRepositoryID, DestinationMemberID string

	Kind, Path, Destination                 string
	RequestID, RuntimeEpoch, ExpectedDigest string
	GrantGeneration                         int64
	Content, ContentEncoding                string
	filter                                  *func(string, bool) bool
}

// OperationFromContext exposes only service-supplied paths; raw clients cannot
// manufacture this typed context through a JSON parameter.
func OperationFromContext(ctx context.Context) (Operation, bool) {
	v, ok := ctx.Value(operationKey{}).(Operation)
	return v, ok
}
func withOperation(ctx context.Context, op Operation) context.Context {
	var filter func(string, bool) bool
	op.filter = &filter
	return context.WithValue(ctx, operationKey{}, op)
}
func withResultCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, resultCaptureKey{}, &resultCapture{})
}
func setResultCapture(ctx context.Context, value Result) {
	if capture, ok := ctx.Value(resultCaptureKey{}).(*resultCapture); ok && capture != nil {
		capture.value = value
	}
}

// CapturedResult is populated only by the service-created operation callback.
func CapturedResult(ctx context.Context) (Result, bool) {
	capture, ok := ctx.Value(resultCaptureKey{}).(*resultCapture)
	return func() (Result, bool) {
		if !ok || capture == nil {
			return Result{}, false
		}
		return capture.value, true
	}()
}
func SetCapturedResult(ctx context.Context, value Result) { setResultCapture(ctx, value) }

// SetPathFilter applies only inside the service-created operation context.
func SetPathFilter(ctx context.Context, filter func(string, bool) bool) {
	if op, ok := OperationFromContext(ctx); ok && op.filter != nil {
		*op.filter = filter
	}
}
func entryAllowed(ctx context.Context, path string) bool {
	op, ok := OperationFromContext(ctx)
	return !ok || op.filter == nil || *op.filter == nil || (*op.filter)(path, true)
}
