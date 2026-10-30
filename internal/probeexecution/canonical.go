package probeexecution

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/workset"
)

// CanonicalSpec is the execution view of the immutable confirmed target receipt.
// It is loaded from Core storage, never accepted from a runtime client.
type CanonicalSpec struct {
	Probe            contextprobe.Probe           `json:"probe"`
	Receipt          json.RawMessage              `json:"receipt"`
	RequestID        string                       `json:"requestId"`
	PayloadHash      string                       `json:"payloadHash"`
	WorkspaceID      string                       `json:"workspaceId"`
	Targets          []workset.Target             `json:"targets"`
	PrimaryMemberID  string                       `json:"primaryMemberId"`
	BackendSelection codexconfig.BackendSelection `json:"backendSelection"`
	Model            string                       `json:"model"`
	ReasoningEffort  string                       `json:"reasoningEffort"`
	ServiceTier      string                       `json:"serviceTier"`
}
type CanonicalState struct {
	ContinuationFreshThread      bool                                 `json:"continuationFreshThread,omitempty"`
	ContinuationRecoveryStatusID string                               `json:"continuationRecoveryStatusId,omitempty"`
	ContinuationSourceTurnID     string                               `json:"continuationSourceTurnId,omitempty"`
	Spec                         CanonicalSpec                        `json:"spec"`
	Run                          run.Run                              `json:"run"`
	Conversation                 agentconversation.Conversation       `json:"conversation"`
	Workset                      workset.RuntimeWorkset               `json:"workset"`
	State                        string                               `json:"state"`
	Before                       []recoverystatus.WorktreeObservation `json:"_before,omitempty"`
	After                        []recoverystatus.WorktreeObservation `json:"_after,omitempty"`
	Schema                       []byte                               `json:"_schema,omitempty"`
	Report                       []byte                               `json:"_report,omitempty"`
	ReportHash                   string                               `json:"reportHash,omitempty"`
	FailureReason                string                               `json:"failureReason,omitempty"`
	Finalization                 *InspectionFinalization              `json:"_finalization,omitempty"`
	UpdatedAt                    time.Time                            `json:"updatedAt"`
}
type InspectionFinalization struct {
	Commands         []run.ObservedCommand                `json:"commands,omitempty"`
	RunID            string                               `json:"runId"`
	InspectionID     string                               `json:"inspectionId"`
	RuntimeEpoch     string                               `json:"runtimeEpoch"`
	ThreadID         string                               `json:"threadId"`
	TurnID           string                               `json:"turnId"`
	WorksetHash      string                               `json:"worksetHash"`
	ReportHash       string                               `json:"reportHash"`
	Status           string                               `json:"status"`
	Reason           string                               `json:"reason,omitempty"`
	Report           []byte                               `json:"report"`
	Before           []recoverystatus.WorktreeObservation `json:"before"`
	After            []recoverystatus.WorktreeObservation `json:"after"`
	Terminal         bool                                 `json:"terminal"`
	TerminalStatus   string                               `json:"terminalStatus"`
	Quiesced         bool                                 `json:"quiesced"`
	NoBackendAdvance bool                                 `json:"noBackendAdvance,omitempty"`
	ObservedAt       time.Time                            `json:"observedAt"`
	TokenUsage       *run.TokenUsageObservation           `json:"tokenUsage,omitempty"`
}
type CanonicalStore interface {
	ConfirmedInspection(context.Context, string, string) (CanonicalSpec, error)
	ReserveInspection(context.Context, CanonicalSpec) (CanonicalState, error)
	BeginInspection(context.Context, CanonicalSpec, workset.RuntimeWorkset, []recoverystatus.WorktreeObservation) (CanonicalState, error)
	InspectionState(context.Context, string) (CanonicalState, error)
	InspectionBeforeThread(context.Context, string, string) (CanonicalState, error)
	InspectionBindThread(context.Context, string, string, string) (CanonicalState, error)
	InspectionBeforeTurn(context.Context, string, string) (CanonicalState, error)
	InspectionBindTurn(context.Context, string, string, string) (CanonicalState, error)
	InspectionSaveReport(context.Context, string, string, []byte) (CanonicalState, error)
	FinalizeInspection(context.Context, InspectionFinalization) (CanonicalState, error)
	MarkInspectionUnknown(context.Context, string, string, string) (CanonicalState, error)
	AdoptInspection(context.Context, string) (CanonicalState, error)
	ListCanonicalInspections(context.Context) ([]string, error)
	CancelPreparedInspection(context.Context, string, string) (CanonicalState, error)
}
type InspectionAppServers interface {
	BindAppServer(context.Context, string, appserver.Binding) error
	SaveAppServerCheckpoint(context.Context, string, json.RawMessage) error
	AppServerCheckpoint(context.Context, string) (json.RawMessage, error)
	AppServerBinding(context.Context, string) (appserver.Binding, error)
}
type InspectionMaterializer interface {
	MaterializeInspection(context.Context, CanonicalSpec) (workset.RuntimeWorkset, []recoverystatus.WorktreeObservation, error)
}
