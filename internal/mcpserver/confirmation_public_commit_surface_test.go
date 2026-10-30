package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// confirmationBlindService is a BridgeService that exposes none of the confirmation
// methods. It models the one surface §13 has to survive: the public commit entry is
// advertised, but the service behind it cannot be found. The correct answer is the
// catalog's own condition, never an invented submission outcome.
type confirmationBlindService struct{ BridgeService }

func TestAbsentCommitServiceReportsItsOwnConditionNotAnUnknownOutcome(t *testing.T) {
	session, ctx, cancel := connectFaultService(t, confirmationBlindService{newFaultStaticService(t)})
	defer cancel()

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var commit *sdkmcp.Tool
	for _, tool := range list.Tools {
		if tool.Name == "confirmation_commit" {
			commit = tool
		}
	}
	if commit == nil {
		t.Fatal("confirmation_commit disappeared from the advertised catalog")
	}

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_commit", Arguments: map[string]any{
		"aiSessionId": "session", "intentId": "confirm_absent", "version": 1, "purpose": string(c.Review),
		"action": "REVISE", "requestId": "request_absent", "contentHash": strings.Repeat("a", 64),
		"variantId": "revise", "selection": map[string]any{},
	}})
	if err != nil {
		t.Fatalf("confirmation_commit transport: %v", err)
	}
	var envelope c.Result
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("commit envelope %s: %v", raw, err)
	}
	if !result.IsError || envelope.Failure == nil {
		t.Fatalf("an unbacked commit entry returned a success envelope: %s", raw)
	}
	// §13: the missing entry is its own typed condition, routed to a pure read, and it
	// says nothing about whether anything was consumed.
	if envelope.Failure.Code != "CONFIRMATION_COMMIT_TOOL_UNAVAILABLE" || envelope.Failure.NextAction != c.ReadCurrentTarget {
		t.Fatalf("failure=%+v", envelope.Failure)
	}
	if envelope.Failure.NextAction == c.EditSelection || envelope.Failure.NextAction == c.PrepareNewVersion {
		t.Fatalf("an unbacked commit entry was offered back as a retry: %+v", envelope.Failure)
	}
	if envelope.Intent != nil || envelope.Options != nil {
		t.Fatalf("the absent commit entry invented confirmation state: %s", raw)
	}
	for _, forbidden := range []string{`"outcome"`, `"submission"`, `"recorded"`, `"consumed"`, string(c.Unknown), string(c.RejectedNotConsumed), string(c.ReadOriginalReceipt)} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("the missing-tool condition leaked a submission fact %q: %s", forbidden, raw)
		}
	}
	// The same surface answers the pure reads with their own condition, so a caller is
	// never told a submission is unknown when nothing was submitted.
	read, readErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_get", Arguments: map[string]any{
		"aiSessionId": "session", "intentId": "confirm_absent", "version": 1, "purpose": string(c.Review), "contentHash": strings.Repeat("a", 64),
	}})
	if readErr != nil {
		t.Fatalf("confirmation_get transport: %v", readErr)
	}
	if !read.IsError {
		t.Fatal("an unbacked exact read returned success")
	}
}
