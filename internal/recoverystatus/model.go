// Package recoverystatus records engineering reconciliation independently of
// runtime request delivery. Its records never constitute response receipts.
package recoverystatus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/executioncancel"
	"strings"
	"time"
)

var (
	ErrConflict   = errors.New("RECOVERY_STATUS_CONFLICT")
	ErrNotFound   = errors.New("RECOVERY_STATUS_NOT_FOUND")
	ErrIneligible = errors.New("RECOVERY_STATUS_INELIGIBLE")
	ErrStale      = errors.New("RECOVERY_STATUS_STALE_EPOCH")
)

type Source struct {
	ProjectID                string `json:"projectId"`
	FeatureAreaID            string `json:"featureAreaId,omitempty"`
	OwnerKind                string `json:"ownerKind,omitempty"`
	OwnerID                  string `json:"ownerId,omitempty"`
	TaskID                   string `json:"taskId,omitempty"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId,omitempty"`
	OperationKind            string `json:"sourceOperationKind"`
	OperationID              string `json:"sourceOperationId"`
	RunID                    string `json:"sourceRunId"`
	TurnID                   string `json:"sourceTurnId"`
	RuntimeEpoch             string `json:"expectedRuntimeEpoch"`
	RuntimeWorksetHash       string `json:"expectedRuntimeWorksetHash"`
	WorkspaceID              string `json:"sourceWorkspaceId,omitempty"`
	AgentConversationID      string `json:"agentConversationId"`
	ThreadID                 string `json:"threadId"`
	AppServerInstanceID      string `json:"appServerInstanceId"`
}
type Spec struct {
	Source
	RequestID           string `json:"requestId"`
	ActorID             string `json:"actorId,omitempty"`
	RecoveryReason      string `json:"recoveryReason"`
	Instruction         string `json:"instruction"`
	OutputSchemaVersion string `json:"outputSchemaVersion"`
	PayloadHash         string `json:"payloadHash"`
	Generation          int    `json:"generation"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func (s Spec) Validate() error {
	inspection := s.OwnerKind == "PROJECT_INSPECTION"
	if s.ProjectID == "" || (!inspection && s.FeatureAreaID == "") || (inspection && (s.FeatureAreaID != "" || s.OwnerID == "" || s.WorkspaceID == "" || s.OperationKind != "INSPECTION" || s.OperationID != s.OwnerID || s.OutputSchemaVersion != "v2")) || (!inspection && (s.OwnerKind != "" || s.OwnerID != "" || s.WorkspaceID != "" || s.OutputSchemaVersion != "v1")) || s.OperationKind == "" || s.OperationID == "" || s.RunID == "" || s.TurnID == "" || s.RuntimeEpoch == "" || s.AgentConversationID == "" || s.ThreadID == "" || s.AppServerInstanceID == "" || s.RequestID == "" || s.ActorID == "" || s.Generation < 1 || s.RecoveryReason == "" || s.RuntimeWorksetHash == "" || (s.OperationKind == "EXECUTION" && s.TaskDefinitionRevisionID == "") || len(s.Instruction) == 0 || len(s.Instruction) > 262144 || strings.ContainsRune(s.Instruction, 0) {
		return ErrIneligible
	}
	for _, value := range []string{s.ProjectID, s.FeatureAreaID, s.OwnerKind, s.OwnerID, s.TaskID, s.TaskDefinitionRevisionID, s.OperationKind, s.OperationID, s.RunID, s.TurnID, s.RuntimeEpoch, s.RuntimeWorksetHash, s.WorkspaceID, s.AgentConversationID, s.ThreadID, s.AppServerInstanceID, s.RequestID, s.ActorID, s.RecoveryReason} {
		if len(value) > 4096 || strings.ContainsRune(value, 0) {
			return ErrIneligible
		}
	}
	return nil
}
func (s Spec) ComputePayloadHash() string {
	s.PayloadHash = ""
	s.ActorID = ""
	b, _ := json.Marshal(s)
	return Hash(b)
}

type Admission struct {
	SourceTurnTerminal bool            `json:"sourceTurnTerminal"`
	ThreadIdle         bool            `json:"threadIdle"`
	NoPendingRequests  bool            `json:"noPendingRequests"`
	Quiesced           bool            `json:"quiesced"`
	Before             json.RawMessage `json:"before"`
	ObservedAt         time.Time       `json:"observedAt"`
}
type Record struct {
	AnalysisRequired          bool            `json:"analysisRequired,omitempty"`
	ID                        string          `json:"id"`
	Spec                      Spec            `json:"spec"`
	RecoveryRunID             string          `json:"recoveryRunId,omitempty"`
	RecoveryTurnID            string          `json:"recoveryTurnId,omitempty"`
	RuntimeEpoch              string          `json:"runtimeEpoch"`
	State                     string          `json:"state"`
	InstructionHash           string          `json:"instructionHash"`
	SchemaHash                string          `json:"schemaHash"`
	Schema                    json.RawMessage `json:"schema"`
	SourceResponseDisposition string          `json:"sourceResponseDisposition,omitempty"`
	Admission                 Admission       `json:"admission"`
	Report                    json.RawMessage `json:"report,omitempty"`
	After                     json.RawMessage `json:"after,omitempty"`
	Facts                     json.RawMessage `json:"facts,omitempty"`
	FailureReason             string          `json:"failureReason,omitempty"`
	Assessment                json.RawMessage `json:"assessment,omitempty"`
	Resolution                json.RawMessage `json:"resolution,omitempty"`
	ReportHash                string          `json:"reportHash,omitempty"`
	AssessmentHash            string          `json:"assessmentHash,omitempty"`
	CreatedAt                 time.Time       `json:"createdAt"`
	UpdatedAt                 time.Time       `json:"updatedAt"`
	StartedAt                 *time.Time      `json:"startedAt,omitempty"`
	CompletedAt               *time.Time      `json:"completedAt,omitempty"`
}
type ResolveInput struct {
	ID             string           `json:"id"`
	RequestID      string           `json:"requestId"`
	ActorID        string           `json:"actorId,omitempty"`
	RuntimeEpoch   string           `json:"expectedRuntimeEpoch"`
	AssessmentHash string           `json:"assessmentHash"`
	ReportHash     string           `json:"reportHash"`
	PayloadHash    string           `json:"payloadHash"`
	Purpose        *PurposeProposal `json:"purpose,omitempty"`
	Admission      Admission        `json:"-"`
}

func (s ResolveInput) ComputePayloadHash() string {
	s.PayloadHash = ""
	s.ActorID = ""
	b, _ := json.Marshal(s)
	return Hash(b)
}

type Store interface {
	Prepare(context.Context, Spec, []byte) (Record, error)
	BeforeStart(context.Context, string, string) (Record, error)
	BindTurn(context.Context, string, string, string) (Record, error)
	SaveReport(context.Context, string, string, []byte, []byte) (Record, error)
	SaveAssessment(context.Context, string, string, []byte, []byte) (Record, error)
	Read(context.Context, string) (Record, error)
	LatestForSource(context.Context, string) (Record, error)
	GetByRecoveryRun(context.Context, string) (Record, error)
	BeginCancel(context.Context, string, executioncancel.Input) (Record, executioncancel.Receipt, bool, error)
	FinishCancel(context.Context, string, executioncancel.Input, Admission) (Record, executioncancel.Receipt, error)
	Fail(context.Context, string, string, string) (Record, error)
	MarkUnknown(context.Context, string, string, string) (Record, error)
	Adopt(context.Context, string) (Record, error)
	ListRecoverable(context.Context) ([]Record, error)
	Resolve(context.Context, ResolveInput) (Record, error)
}
