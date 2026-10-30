package wire

// Minimal common turn/steer contract verified against 0.159.2 and 0.163.0.
// No model, sandbox, root or output-schema overrides are permitted.
type TurnSteerParams struct {
	ThreadID       string      `json:"threadId"`
	ExpectedTurnID string      `json:"expectedTurnId"`
	Input          []UserInput `json:"input"`
}
type TurnSteerResponse struct {
	TurnID string `json:"turnId"`
}
