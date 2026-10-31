package controlapi_test

// This scenario loses response bytes, not Core journal records. It never adds a
// production fault hook, changes business state directly, or retries a command.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSimulatedContinuationResponseLoss(t *testing.T) {
	runSimulatedContinuationGate(t, (*continuationGate).exerciseResponseLoss)
}

var errContinuationResponseLost = errors.New("test-only continuation response body lost after Core completed")

// One dedicated MCP client may forward only the exact frozen commit once. Other
// protocol messages pass unchanged. All bookkeeping is synchronized because the
// SDK calls RoundTrip and response-body methods in transport goroutines.
type continuationResponseLoss struct {
	base     http.RoundTripper
	expected c.CommitInput
	mu       sync.Mutex
	capture  continuationLossCapture
}

type continuationLossCapture struct {
	Attempts, Forwarded, BodyReads int
	BodyClosed                     bool
	Request, Response              json.RawMessage
	Status                         int
	ContentType                    string
}

func (d *continuationResponseLoss) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return d.base.RoundTrip(req)
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, (1<<20)+1))
	_ = req.Body.Close()
	if err != nil || len(body) > 1<<20 {
		return nil, fmt.Errorf("test-only request capture failed: size=%d err=%v", len(body), err)
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	var rpc struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, err
	}
	if rpc.Method != "tools/call" {
		return d.base.RoundTrip(req)
	}
	d.mu.Lock()
	d.capture.Attempts++
	hash, hashErr := c.InputHash(rpc.Params.Arguments)
	exact := rpc.Params.Name == "confirmation_commit" && hashErr == nil && c.ValueHash(d.expected) == hash
	if !exact || d.capture.Forwarded != 0 {
		d.mu.Unlock()
		return nil, errors.New("test-only loss transport refused unexpected or repeated business request")
	}
	d.capture.Forwarded++
	d.capture.Request = append(json.RawMessage(nil), body...)
	d.mu.Unlock()
	response, err := d.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// Receiving headers alone does not prove the command finished. Drain/close
	// the real bounded JSON response and verify its exact completed receipt
	// before hiding ALL its body bytes from the client. No receipt is fabricated.
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, (4<<20)+1))
	closeErr := response.Body.Close()
	d.mu.Lock()
	d.capture.Status, d.capture.ContentType = response.StatusCode, response.Header.Get("Content-Type")
	d.capture.Response = append(json.RawMessage(nil), raw...)
	d.mu.Unlock()
	if readErr != nil || closeErr != nil || len(raw) > 4<<20 {
		return nil, fmt.Errorf("test-only response capture failed: size=%d read=%v close=%v", len(raw), readErr, closeErr)
	}
	var envelope struct {
		ID     json.RawMessage        `json:"id"`
		Result *sdkmcp.CallToolResult `json:"result"`
		Error  json.RawMessage        `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "application/json") || !bytes.Equal(rpc.ID, envelope.ID) || len(envelope.Error) != 0 || envelope.Result == nil || envelope.Result.IsError {
		return nil, errors.New("test-only loss requires a matching successful JSON-RPC response")
	}
	structured, err := json.Marshal(envelope.Result.StructuredContent)
	if err != nil {
		return nil, err
	}
	var result c.Result
	if err := json.Unmarshal(structured, &result); err != nil {
		return nil, err
	}
	if result.Failure != nil || result.Intent == nil || result.Intent.State != "COMPLETED" || result.Intent.Submission == nil || !result.Intent.Submission.Recorded || result.Intent.Submission.Outcome != c.Consumed || !reflect.DeepEqual(result.Intent.Submission.Request, d.expected) {
		return nil, errors.New("test-only loss requires Core's exact consumed original request")
	}
	// Return genuine headers but a reader that yields only the injected error.
	// This is a response-stream failure, not HTTPClient.Do's ErrRejected path.
	response.Body = &continuationLostBody{owner: d}
	return response, nil
}

type continuationLostBody struct{ owner *continuationResponseLoss }

func (b *continuationLostBody) Read([]byte) (int, error) {
	b.owner.mu.Lock()
	b.owner.capture.BodyReads++
	b.owner.mu.Unlock()
	return 0, errContinuationResponseLost
}
func (b *continuationLostBody) Close() error {
	b.owner.mu.Lock()
	b.owner.capture.BodyClosed = true
	b.owner.mu.Unlock()
	return nil
}

func (s *continuationGate) loseConfirmationResponse(in c.CommitInput) {
	s.t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test-only-response-loss", Ready: true}, s.f.plane.AIContext, s.f.facade)
	if err != nil {
		s.t.Fatal(err)
	}
	server, err := mcpserver.New(mcpserver.Config{RequestTimeout: continuationRequestTimeout}, service)
	if err != nil {
		s.t.Fatal(err)
	}
	host := httptest.NewServer(server.Handler())
	defer host.Close()
	httpClient := host.Client()
	loss := &continuationResponseLoss{base: httpClient.Transport, expected: in}
	httpClient.Transport = loss
	// Only the transport connection is new. The original real business Session,
	// intent and request identity remain unchanged. No Session/intent is created.
	client, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture-user-response-loss", Version: "test-only"}, nil).Connect(s.ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	defer client.Close()
	started := time.Now().UTC()
	result, callErr := client.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: "confirmation_commit", Arguments: in})
	// Close/join the transport before copying counters; a body failure may close
	// the SDK connection. Reads below use the ordinary existing GPT connection.
	_ = client.Close()
	loss.mu.Lock()
	capture := loss.capture
	loss.mu.Unlock()
	s.save("lost-response-"+in.RequestID+".json", capture)
	entry := map[string]any{"startedAt": started, "finishedAt": time.Now().UTC(), "durationMillis": time.Since(started).Milliseconds(), "actor": "fixture_user", "tool": "confirmation_commit", "input": in, "result": result, "testOnlyFault": "post-handler response body loss"}
	if callErr != nil {
		entry["transportError"] = callErr.Error()
	}
	if err := json.NewEncoder(s.transcript).Encode(entry); err != nil {
		s.t.Fatal(err)
	}
	s.check(callErr != nil && strings.Contains(callErr.Error(), errContinuationResponseLost.Error()) && result == nil, "client observes only the exact injected response loss, no usable receipt", entry)
	s.check(capture.Attempts == 1 && capture.Forwarded == 1 && capture.BodyReads > 0 && capture.BodyClosed, "one exact commit forwarded, response body lost/closed, no transport resend", capture)
}

func (s *continuationGate) checkRecoveredChoice(intent c.Intent, in c.CommitInput) {
	s.t.Helper()
	sub := intent.Submission
	s.check(intent.State == "COMPLETED" && intent.IntentID == in.IntentID && intent.Version == in.Version && intent.Purpose == in.Purpose && intent.Action == in.Action && intent.ContentHash == in.ContentHash && intent.Commit != nil && intent.Commit.RequestID == in.RequestID, "original identity read recovers completed choice after response loss", intent)
	s.check(sub != nil && sub.Recorded && sub.Outcome == c.Consumed && sub.BusinessOutcome == "COMMITTED" && sub.ConsumptionDisposition == "CONSUMED" && sub.Consumed != nil && *sub.Consumed && !sub.RedispatchSameIntent && !sub.ReprepareAllowed && sub.RetryDisposition == c.RetryNone && sub.PayloadSHA256 == c.ValueHash(in) && reflect.DeepEqual(sub.Request, in), "only exact original request yields immutable consumed receipt and no retry authority", sub)
	s.check(len(intent.Steps) == 1 && intent.Steps[0].Kind == "RECORD_TASK_CHOICE" && intent.Steps[0].State == "COMPLETED" && intent.Steps[0].Effect == "COMMAND_COMPLETED" && intent.Steps[0].RunID == "" && intent.Steps[0].TaskID == s.f.task && intent.Steps[0].ExecutionID == s.executionID && intent.Steps[0].RevisionID == s.revision, "recovered effect is one exact task choice, not Run dispatch", intent.Steps)
}

func (s *continuationGate) exerciseResponseLoss() {
	v := s.checkCheckpoint(false, false)
	note := "Retain exact verified fixture evidence; not acceptance or permission to merge."
	pending := s.prepare(s.choice("DEFER_TASK", "loss-prepare-defer", note, int(v.ActionNode.Generation)))
	s.checkCheckpoint(false, false)
	deferInput := s.commitInput(pending, "loss-commit-defer")
	s.loseConfirmationResponse(deferInput)
	// An explicitly never-sent read-only probe cannot stand in for the original
	// request, even though this same intent already has a real consumed choice.
	miss := s.confirmationRead(pending, "loss-never-submitted-read-probe")
	unknown := miss.Submission
	s.check(unknown != nil && !unknown.Recorded && unknown.Outcome == c.Unknown && unknown.BusinessOutcome == "OUTCOME_UNKNOWN" && unknown.ConsumptionDisposition == "UNKNOWN" && unknown.Consumed == nil && unknown.PayloadSHA256 == "" && unknown.ReceiptRequestID == "" && unknown.Request.RequestID == "loss-never-submitted-read-probe" && unknown.NextAction == c.ReadOriginalReceipt && unknown.RetryDisposition == c.RetryReadOriginalReceipt && !unknown.RedispatchSameIntent && !unknown.ReprepareAllowed, "missing exact request is UNKNOWN, never non-consumption or retry authority", unknown)
	// The recovery address was frozen BEFORE dispatch. Never select a new
	// request, latest intent or an identity from intercepted oracle-only bytes.
	deferred := s.confirmationRead(pending, deferInput.RequestID)
	s.checkRecoveredChoice(deferred, deferInput)
	s.checkCheckpoint(true, false)
	s.cold()
	coldDeferred := s.confirmationRead(pending, deferInput.RequestID)
	s.checkRecoveredChoice(coldDeferred, deferInput)
	s.check(reflect.DeepEqual(coldDeferred.Commit, deferred.Commit) && reflect.DeepEqual(coldDeferred.Steps, deferred.Steps) && reflect.DeepEqual(coldDeferred.Submission, deferred.Submission), "cold exact DEFER receipt unchanged without commit replay", coldDeferred)
	v = s.checkCheckpoint(true, false)
	pendingContinue := s.prepare(s.choice("CONTINUE_TASK", "loss-prepare-continue", "", int(v.ActionNode.Generation)))
	continueInput := s.commitInput(pendingContinue, "loss-commit-continue")
	s.loseConfirmationResponse(continueInput)
	// For CONTINUE, intentionally reopen Core before the first recovery read.
	// Neither the missing response nor restart authorizes another business call.
	s.cold()
	continued := s.confirmationRead(pendingContinue, continueInput.RequestID)
	s.checkRecoveredChoice(continued, continueInput)
	s.checkCheckpoint(false, true)
	old := s.confirmationRead(pending, deferInput.RequestID)
	s.checkRecoveredChoice(old, deferInput)
	s.check(reflect.DeepEqual(old.Commit, deferred.Commit) && reflect.DeepEqual(old.Steps, deferred.Steps) && reflect.DeepEqual(old.Submission, deferred.Submission), "old DEFER read remains history after CONTINUE, never recreates hold", old)
	continuedAgain := s.confirmationRead(pendingContinue, continueInput.RequestID)
	s.check(reflect.DeepEqual(continuedAgain.Commit, continued.Commit) && reflect.DeepEqual(continuedAgain.Steps, continued.Steps) && reflect.DeepEqual(continuedAgain.Submission, continued.Submission), "repeat pure read preserves original CONTINUE receipt", continuedAgain)
	s.checkCheckpoint(false, true)
	s.save("final-workspace.json", s.workspace())
}
