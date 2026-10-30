// Package appserver identifies independently lived App Server instances. It owns
// processes, never Tasks, Runs, decisions, or operation recovery policy.
package appserver

import "time"

type Binding struct {
	TaskPermissionHash   string    `json:"taskPermissionHash,omitempty"`
	DataRoot             string    `json:"dataRoot"`
	DataRootIdentity     string    `json:"dataRootIdentity"`
	ID                   string    `json:"id"`
	Epoch                int64     `json:"epoch"`
	Endpoint             string    `json:"endpoint"`
	EndpointKind         string    `json:"endpointKind"`
	SocketPath           string    `json:"socketPath"`
	SocketIdentity       string    `json:"socketIdentity"`
	SupervisorID         string    `json:"supervisorId"`
	PID                  int       `json:"pid"`
	ProcessStartIdentity string    `json:"processStartIdentity"`
	ExecutablePath       string    `json:"executablePath"`
	ExecutableSHA256     string    `json:"executableSha256"`
	CodexVersion         string    `json:"codexVersion"`
	ProtocolIdentity     string    `json:"protocolIdentity"`
	CodexHomeIdentity    string    `json:"codexHomeIdentity"`
	BackendConfigID      string    `json:"backendConfigId"`
	BackendFingerprint   string    `json:"backendFingerprint"`
	LaunchedAt           time.Time `json:"launchedAt"`
	LastConfirmedAliveAt time.Time `json:"lastConfirmedAliveAt"`
	Status               string    `json:"status"`
}
type Spec struct {
	TaskPermissionHash                                                                                       string
	DataRoot, ExecutablePath, CodexHome, BackendConfigID, BackendFingerprint, CodexVersion, ProtocolIdentity string
	Args, Env                                                                                                []string
}
