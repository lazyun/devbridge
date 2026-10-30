package codex

import "encoding/json"

type Event struct {
	RequestID json.RawMessage
	Method    string
	Params    json.RawMessage
}

type ServerRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}
