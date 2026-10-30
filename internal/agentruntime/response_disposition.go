package agentruntime

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrResponseDispositionUnknown = errors.New("RESPONSE_DISPOSITION_UNKNOWN")
var ErrRequestConflict = errors.New("REQUEST_CONFLICT")

// Dispatch attempts and evidence are append-only audit facts. State and Delivery
// remain the sole current lifecycle/disposition authority.
type ResponseDispatchAttempt struct {
	ID string `json:"id"`
	Identity
	RuntimeRequestID   string    `json:"runtimeRequestId"`
	UserDecisionID     string    `json:"userDecisionId"`
	BackendRequestID   string    `json:"backendRequestId"`
	ItemID             string    `json:"itemId,omitempty"`
	CommandRequestID   string    `json:"commandRequestId"`
	PayloadSHA256      string    `json:"payloadSHA256"`
	Payload            []byte    `json:"payload"`
	StartedAt          time.Time `json:"startedAt"`
	RecoveryEvidenceID string    `json:"recoveryEvidenceId,omitempty"`
}
type ResponseRecoveryEvidence struct {
	ID string `json:"id"`
	Identity
	RuntimeRequestID string    `json:"runtimeRequestId"`
	BackendRequestID string    `json:"backendRequestId"`
	AttemptID        string    `json:"attemptId,omitempty"`
	Kind             string    `json:"kind"`
	ObservedAt       time.Time `json:"observedAt"`
	PayloadSHA256    string    `json:"payloadSHA256,omitempty"`
}
type RuntimeRequestProjection struct {
	RuntimeRequestID          string     `json:"runtimeRequestId"`
	Kind                      string     `json:"kind"`
	OwnerOperation            string     `json:"ownerOperation"`
	LifecycleState            string     `json:"lifecycleState"`
	ResponseDisposition       string     `json:"responseDisposition"`
	UserDecisionRecorded      bool       `json:"userDecisionRecorded"`
	UserDecisionSummary       string     `json:"userDecisionSummary"`
	ResponseDispatchStartedAt *time.Time `json:"responseDispatchStartedAt"`
	ResponseResolvedAt        *time.Time `json:"responseResolvedAt"`
	AppServerInstanceStatus   string     `json:"appServerInstanceStatus"`
	ThreadStatus              string     `json:"threadStatus"`
	TurnStatus                string     `json:"turnStatus"`
	RecoveryStatus            string     `json:"recoveryStatus"`
	ReasonCode                string     `json:"reasonCode"`
	ResendAllowed             bool       `json:"resendAllowed"`
	AvailableActions          []string   `json:"availableActions"`
	LastObservedAt            time.Time  `json:"lastObservedAt"`
	DisplayTitle              string     `json:"displayTitle"`
	DisplayMessage            string     `json:"displayMessage"`
	Title                     string     `json:"title"`
	Summary                   string     `json:"summary"`
}

func ResponseDisposition(r Request) string {
	if r.Kind != "CODEX_REQUEST" {
		return "NOT_APPLICABLE"
	}
	if r.UserDecision == nil {
		if r.State == "WAITING_FOR_USER" {
			return "NOT_DISPATCHED"
		}
		return "NOT_APPLICABLE"
	}
	if r.UserDecision.Action != "RESPOND" {
		return "NOT_APPLICABLE"
	}
	switch r.Delivery {
	case "NOT_DISPATCHED", "NOT_SENT", "NOT_SENT_FENCED":
		return "NOT_DISPATCHED"
	case "CONSUMED":
		return "CONSUMED"
	case "UNKNOWN", "RESPONSE_UNCERTAIN", "TRANSPORT_WRITTEN":
		return "UNKNOWN"
	case "DISPATCHING":
		return "DISPATCHING"
	}
	if r.State == "RESPONDED" && r.ResolvedAt != nil {
		return "CONSUMED"
	}
	if r.State == "USER_DECISION_RECORDED" || r.State == "WAITING_FOR_USER" {
		return "NOT_DISPATCHED"
	}
	// A legacy generic UNKNOWN without dispatch evidence is not proof of entry.
	return "NOT_APPLICABLE"
}
func NormalizeRequest(r *Request) {
	if r.Kind != "CODEX_REQUEST" {
		return
	}
	d := ResponseDisposition(*r)
	if r.State == "UNKNOWN" && r.UserDecision != nil && r.UserDecision.Action == "RESPOND" {
		if d == "DISPATCHING" {
			d = "UNKNOWN"
		}
		if d == "NOT_DISPATCHED" {
			r.State = "USER_DECISION_RECORDED"
		}
	}
	if d != "NOT_APPLICABLE" {
		r.Delivery = d
	}
	if d == "UNKNOWN" && r.State != "CANCELED" && r.State != "STALE" {
		r.State = "RESPONDING"
		r.ResolvedAt = nil
	}
	if d == "CONSUMED" && r.State != "CANCELED" && r.State != "STALE" {
		r.State = "RESPONDED"
	}
}
func HasUnresolvedResponse(v Snapshot) bool {
	for _, r := range v.Requests {
		if r.State == "CANCELED" {
			continue
		}
		d := ResponseDisposition(r)
		if d == "UNKNOWN" || d == "DISPATCHING" || (r.Kind == "CODEX_REQUEST" && r.State == "UNKNOWN") {
			return true
		}
	}
	return false
}
func ProjectRequest(r Request, o *RuntimeObservation, turnStatus string) RuntimeRequestProjection {
	NormalizeRequest(&r)
	p := RuntimeRequestProjection{RuntimeRequestID: r.ID, Kind: r.Kind, OwnerOperation: r.OperationKind + ":" + r.OperationID, LifecycleState: r.State, ResponseDisposition: ResponseDisposition(r), UserDecisionRecorded: r.UserDecision != nil, TurnStatus: turnStatus, LastObservedAt: r.UpdatedAt, AvailableActions: append([]string{}, r.AvailableActions...), AppServerInstanceStatus: "UNOBSERVED", RecoveryStatus: "NONE"}
	if r.UserDecision != nil {
		parts := []string{}
		if r.UserDecision.RawText != "" {
			parts = append(parts, r.UserDecision.RawText)
		}
		if r.UserDecision.SelectedOptionID != "" {
			parts = append(parts, r.UserDecision.SelectedOptionID)
		}
		for _, a := range r.UserDecision.Answers {
			if a.RawText != "" {
				parts = append(parts, a.RawText)
			} else if a.SelectedOptionID != "" {
				parts = append(parts, a.SelectedOptionID)
			}
		}
		p.UserDecisionSummary = strings.Join(parts, "\n")
	}
	if len(r.DispatchAttempts) > 0 {
		t := r.DispatchAttempts[len(r.DispatchAttempts)-1].StartedAt
		p.ResponseDispatchStartedAt = &t
	}
	if p.ResponseDisposition == "CONSUMED" {
		p.ResponseResolvedAt = r.ResolvedAt
	}
	if o != nil {
		p.AppServerInstanceStatus = o.InstanceStatus
		p.ThreadStatus = o.ThreadStatus
		p.RecoveryStatus = o.RequestReattachment
		if o.ObservedAt.After(p.LastObservedAt) {
			p.LastObservedAt = o.ObservedAt
		}
	}
	if r.State == "UNKNOWN" && p.ResponseDisposition == "NOT_APPLICABLE" {
		p.ReasonCode = "LEGACY_RECOVERY_REQUIRED"
		p.RecoveryStatus = "RECOVERY_REQUIRED"
		p.AvailableActions = []string{}
	}
	switch r.State {
	case "WAITING_FOR_USER":
		p.Title = "等待用户回答"
	case "USER_DECISION_RECORDED":
		p.Title = "回答已记录"
	case "RESPONDING":
		p.Title = "正在发送回答"
	case "RESPONDED":
		p.Title = "Codex 已确认响应"
	case "CANCELED":
		p.Title = "已取消"
	case "STALE":
		p.Title = "原请求已失效"
	}
	if p.ResponseDisposition == "DISPATCHING" {
		p.AvailableActions = []string{}
	}
	if p.ResponseDisposition == "UNKNOWN" {
		p.Title = "回答状态无法确认"
		p.Summary = "你的回答已经保存。Codex 可能已经收到，但当前协议无法确认。系统不会自动重发。"
		p.ReasonCode = "CODEX_RESPONSE_RESOLUTION_READBACK_UNAVAILABLE"
		p.AvailableActions = []string{"CONTINUE_WAITING", "CANCEL_OPERATION", "STOP_AND_REVISE"}
	}
	if o != nil && (o.InstanceStatus == "LOST" || o.ConnectionState == "RUNTIME_SESSION_LOST") {
		p.Title = "Codex 会话已丢失"
		p.Summary = "原 Codex 会话无法恢复，需要按既有恢复流程处理。已保存的回答不会自动重发。"
		p.ReasonCode = "CODEX_RUNTIME_SESSION_LOST"
		p.RecoveryStatus = "RECOVERY_REQUIRED"
		p.AvailableActions = []string{}
	}
	if r.State == "CANCELED" || r.State == "STALE" {
		p.AvailableActions = []string{}
		if r.State == "CANCELED" {
			p.Title = "已取消"
		} else {
			p.Title = "原请求已失效"
		}
	}
	p.DisplayTitle = p.Title
	p.DisplayMessage = p.Summary
	return p
}
func ApplyRequestProjections(v *Snapshot) {
	for i := range v.Requests {
		r := &v.Requests[i]
		NormalizeRequest(r)
		p := ProjectRequest(*r, v.Observation, v.TurnStatus)
		r.Projection = &p
		r.AvailableActions = append([]string{}, p.AvailableActions...)
	}
	if HasUnresolvedResponse(*v) {
		v.WaitingState = "RESPONSE_DISPOSITION_UNKNOWN"
	}
}

func responseEvidence(r Request, kind string, at time.Time) ResponseRecoveryEvidence {
	e := ResponseRecoveryEvidence{Identity: r.Identity, RuntimeRequestID: r.ID, BackendRequestID: string(r.RequestID), Kind: kind, ObservedAt: at, PayloadSHA256: hashBytes(r.Resolution)}
	if len(r.DispatchAttempts) > 0 {
		e.AttemptID = r.DispatchAttempts[len(r.DispatchAttempts)-1].ID
		e.PayloadSHA256 = r.DispatchAttempts[len(r.DispatchAttempts)-1].PayloadSHA256
	}
	e.ID = "response_evidence_" + hashBytes(raw([]any{e.Identity, e.RuntimeRequestID, e.Kind, e.AttemptID, len(r.RecoveryEvidence), at}))
	return e
}
func newResponseDispatchAttempt(r Request) ResponseDispatchAttempt {
	a := ResponseDispatchAttempt{Identity: r.Identity, RuntimeRequestID: r.ID, UserDecisionID: r.UserDecision.ID, BackendRequestID: string(r.RequestID), ItemID: r.ItemID, CommandRequestID: r.UserDecision.SubmissionRequestID, Payload: append([]byte(nil), r.Resolution...), PayloadSHA256: hashBytes(r.Resolution), StartedAt: r.UpdatedAt}
	a.ID = "response_dispatch_" + hashBytes(raw([]any{r.ID, r.ConnectionID, r.UserDecision.ID, fmt.Sprint(len(r.DispatchAttempts))}))
	if len(r.RecoveryEvidence) > 0 {
		e := r.RecoveryEvidence[len(r.RecoveryEvidence)-1]
		if e.Kind == "EXACT_PENDING_REPLAY" || e.Kind == "PRE_EFFECT_REJECTED" {
			a.RecoveryEvidenceID = e.ID
		}
	}
	return a
}
