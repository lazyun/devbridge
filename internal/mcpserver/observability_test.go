package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/observability"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolLoggingMiddlewareCorrelatesOneTerminalResultWithoutBodies(t *testing.T) {
	for _, test := range []struct {
		name      string
		failed    bool
		wantEvent string
	}{
		{name: "success", wantEvent: "mcp.tool.succeeded"},
		{name: "failure", failed: true, wantEvent: "mcp.tool.failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := observability.New(observability.Options{Process: "devbridge-mcp", Writer: &output})
			ctx := observability.WithLogger(context.Background(), logger)
			request := &sdkmcp.CallToolRequest{Params: &sdkmcp.CallToolParamsRaw{
				Name:      "review_get",
				Arguments: json.RawMessage(`{"aiSessionId":"session-a","featureAreaId":"featureArea-a","reviewId":"review-a","reviewDepth": "DEEP", "reviewInstruction":"complete private prompt","authorization":"Bearer secret"}`),
			}}
			next := func(context.Context, string, sdkmcp.Request) (sdkmcp.Result, error) {
				result := &sdkmcp.CallToolResult{StructuredContent: map[string]any{"runId": "run-a", "result": "complete private response"}}
				if test.failed {
					result.SetError(errors.New("INVALID_STATE: rejected"))
				}
				return result, nil
			}
			result, err := toolLoggingMiddleware()(next)(ctx, "tools/call", request)
			if err != nil || result == nil {
				t.Fatalf("middleware result=%v err=%v", result, err)
			}

			entries := decodeLogEntries(t, output.Bytes())
			if len(entries) != 2 || entries[0]["event"] != "mcp.tool.requested" || entries[1]["event"] != test.wantEvent {
				t.Fatalf("events=%#v", entries)
			}
			if entries[0]["requestId"] == "" || entries[0]["requestId"] != entries[1]["requestId"] || entries[0]["featureAreaId"] != "featureArea-a" || entries[0]["reviewId"] != "review-a" || entries[0]["aiSessionId"] != "session-a" {
				t.Fatalf("correlation=%#v", entries)
			}
			if bytes.Contains(output.Bytes(), []byte("private prompt")) || bytes.Contains(output.Bytes(), []byte("private response")) || bytes.Contains(output.Bytes(), []byte("Bearer secret")) {
				t.Fatalf("body leaked: %s", output.Bytes())
			}
			if test.failed && entries[1]["errorCode"] != "INVALID_STATE" {
				t.Fatalf("failure=%#v", entries[1])
			}
		})
	}
}

func TestContinuationFailureClassificationOnlyCallsDeliveryFailureAfterAttempt(t *testing.T) {
	for _, test := range []struct {
		name, want string
		err        error
	}{
		{name: "delivery", want: "continuation.delivery_failed", err: &approval.DeliveryError{Err: errors.New("transport closed")}},
		{name: "rejected", want: "continuation.rejected", err: errors.New("invalid decision")},
		{name: "duplicate", want: "continuation.skipped_duplicate", err: approval.ErrAlreadyResolved},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
			logContinuationDecisionFailure(ctx, test.err)
			entries := decodeLogEntries(t, output.Bytes())
			if len(entries) != 1 || entries[0]["event"] != test.want {
				t.Fatalf("entries=%#v want=%s", entries, test.want)
			}
		})
	}
}

func decodeLogEntries(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var result []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		result = append(result, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
