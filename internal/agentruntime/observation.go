package agentruntime

import "time"

// RuntimeObservation is cached protocol evidence, never a workflow decision.
// Reading it must not connect, resume, dispatch, or start an App Server.
type RuntimeObservation struct {
	AppServerInstanceID string    `json:"appServerInstanceId"`
	InstanceEpoch       int64     `json:"instanceEpoch"`
	InstanceStatus      string    `json:"instanceStatus"`
	ConnectionState     string    `json:"connectionState"`
	ThreadID            string    `json:"threadId"`
	TurnID              string    `json:"turnId"`
	ThreadStatus        string    `json:"threadStatus,omitempty"`
	RequestReattachment string    `json:"requestReattachment"`
	ObservedAt          time.Time `json:"observedAt"`
	SourceMethod        string    `json:"sourceMethod"`
	FailureReason       string    `json:"failureReason,omitempty"`
	Summary             string    `json:"summary"`
}
