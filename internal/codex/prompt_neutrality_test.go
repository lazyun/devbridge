package codex

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codex/wire"
)

func TestPromptNeutralityStartTurnWireByteIdentityAndSHA(t *testing.T) {
	for name, instruction := range map[string]string{"leading": " \tinspect", "trailing": "inspect \t", "newlines": "\r\ninspect\n\n", "unicode": "检查 e\u0301 🚀", "symbols": "<$&`{}\\> # +=", "long": " \n" + strings.Repeat("界", 21000) + "\n "} {
		t.Run(name, func(t *testing.T) {
			client, requests, responses := newClientFixture(t)
			done := make(chan error, 1)
			schema := json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`)
			go func() {
				_, err := client.StartTurn(context.Background(), TurnOptions{ThreadID: "thread-neutral", Prompt: instruction, OutputSchema: schema, AuthorizationCeiling: AuthorizationReadOnly})
				done <- err
			}()
			request := readClientRequest(t, requests)
			var params wire.TurnStartParams
			if err := json.Unmarshal(request["params"], &params); err != nil {
				t.Fatal(err)
			}
			if len(params.Input) != 1 || params.Input[0].Type != "text" || params.Input[0].Text != instruction || sha256.Sum256([]byte(params.Input[0].Text)) != sha256.Sum256([]byte(instruction)) || string(params.OutputSchema) != string(schema) {
				t.Fatal("wire changed instruction or schema")
			}
			if params.SandboxPolicy == nil || params.SandboxPolicy.Type != "readOnly" {
				t.Fatal("read-only authority depended on prompt wording")
			}
			writeServerJSON(t, responses, map[string]any{"id": request["id"], "result": map[string]any{"turn": map[string]any{"id": "turn-neutral", "status": "inProgress", "items": []any{}}}})
			writeTokenUsage(t, responses, "thread-neutral", "turn-neutral", tokenBreakdown(0, 0, 0, 0, 0, 0), tokenBreakdown(0, 0, 0, 0, 0, 0), nil)
			writeServerJSON(t, responses, map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread-neutral", "turn": map[string]any{"id": "turn-neutral", "status": "completed", "items": []any{}}}})
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
