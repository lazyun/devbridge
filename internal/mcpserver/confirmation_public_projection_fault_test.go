package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectFaultService connects a client to a server that registers the
// confirmation tools against an arbitrary BridgeService, mirroring the real
// transport so a projection fault is exercised end to end.
func connectFaultService(t *testing.T, service BridgeService) (*sdkmcp.ClientSession, context.Context, context.CancelFunc) {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fault-mount", Version: "1"}, nil)
	RegisterConfirmationTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fault-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	return session, ctx, cancel
}

func newFaultStaticService(t *testing.T) *StaticService {
	t.Helper()
	base, err := NewStaticService(BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// Layer 0/1 unit proof: a valid projection is returned unchanged, and a primary
// the advertised schema rejects is replaced by the fixed, schema-valid,
// failure-only fallback that carries no business intent or options.
func TestPublicConfirmationResultFallsBackWithoutLosingSafety(t *testing.T) {
	valid := liveShapeReviewIntent()
	if out, invalid, err := publicConfirmationResultOrFallback(c.Result{Intent: &valid}); invalid || err != nil {
		t.Fatalf("canonical result must be Layer 0: invalid=%v err=%v", invalid, err)
	} else if out.Intent == nil {
		t.Fatal("Layer 0 must return the caller's intent unchanged")
	}
	// intent + options together violate the closed envelope schema (not.required).
	conflicting := c.Result{Intent: &valid, Options: &c.Options{AISessionID: "session", IntentID: valid.IntentID, Version: 1, Purpose: c.Review}}
	fallback, invalid, err := publicConfirmationResultOrFallback(conflicting)
	if err != nil {
		t.Fatalf("Layer 1 fallback must not error: %v", err)
	}
	if !invalid {
		t.Fatal("a schema-invalid primary must report projectionInvalid")
	}
	if fallback.Intent != nil || fallback.Options != nil || fallback.Display != nil {
		t.Fatalf("fallback must be failure-only, no business payload: %+v", fallback)
	}
	if fallback.Failure == nil || fallback.Failure.Code != "CONFIRMATION_PUBLIC_PROJECTION_INVALID" {
		t.Fatalf("fallback must carry the fixed typed projection failure: %+v", fallback.Failure)
	}
	// The fallback itself must satisfy the advertised schema (it is Layer 1, not 2).
	if _, err := validatePublicConfirmationResult(fallback); err != nil {
		t.Fatalf("fixed fallback must be schema-valid: %v", err)
	}
}

// No-panic invariant: adversarial results never panic and always yield either a
// value or a non-nil error, never a malformed value reported as healthy.
func TestPublicConfirmationResultNeverPanicsAcrossAdversarialInputs(t *testing.T) {
	emptyIntent := c.Intent{}
	longFeedback := liveShapeReviewIntent()
	withOptions := liveShapeReviewIntent()
	cases := []c.Result{
		{},
		{Intent: &emptyIntent},
		{Intent: &longFeedback},
		{Options: &c.Options{}},
		{Failure: &c.Failure{}},
		{Intent: &withOptions, Options: &c.Options{}},
	}
	for index, input := range cases {
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("case %d panicked: %v", index, recovered)
				}
			}()
			result, invalid, err := publicConfirmationResultOrFallback(input)
			if err != nil {
				// Layer 2: an error result carries no structured payload.
				if result.Intent != nil || result.Options != nil || result.Failure != nil {
					t.Fatalf("case %d Layer 2 must be empty, got %+v", index, result)
				}
				return
			}
			if _, validateErr := validatePublicConfirmationResult(result); validateErr != nil {
				t.Fatalf("case %d returned a value that violates the schema (invalid=%v): %v", index, invalid, validateErr)
			}
		}()
	}
}

// togglingConfirmationService returns a scripted sequence of intents so one MCP
// session can observe a projection fault followed by a healthy read.
type togglingConfirmationService struct {
	*StaticService
	intents []c.Intent
	readErr []error
	calls   int
	reads   int
}

func (s *togglingConfirmationService) ReadConfirmation(_ context.Context, _ c.ReadInput) (c.Intent, error) {
	s.reads++
	index := s.calls
	if index >= len(s.intents) {
		index = len(s.intents) - 1
	}
	s.calls++
	err := s.readErr[index]
	return s.intents[index], err
}

func (s *togglingConfirmationService) ConfirmationDisplay(context.Context, c.Intent) *c.DisplayMetadata {
	return nil
}

// Transport proof: after a projection fault forces the safe fallback (IsError, no
// Card meta, no intent), the SAME session still mounts a subsequent healthy show,
// proving the connection is not poisoned and the durable read is untouched.
func TestConfirmationShowSurvivesProjectionFaultThenMounts(t *testing.T) {
	bad := liveShapeReviewIntent()
	bad.State = "BOGUS_PROJECTION_FAULT" // not in the advertised state enum -> Layer 1 fallback at the show handler
	good := liveShapeReviewIntent()
	service := &togglingConfirmationService{
		StaticService: newFaultStaticService(t),
		intents:       []c.Intent{bad, good},
		readErr:       []error{nil, nil},
	}
	session, ctx, cancel := connectFaultService(t, service)
	defer cancel()

	faulted, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
	if err != nil {
		t.Fatal(err)
	}
	if !faulted.IsError {
		t.Fatal("projection fault must return an IsError result")
	}
	if metaResourceURI(faulted.Meta) != "" || faulted.Meta["emission"] != nil {
		t.Fatalf("no Card may be mounted for a projection fault: meta=%+v", faulted.Meta)
	}
	faultedRaw, _ := json.Marshal(faulted.StructuredContent)
	var faultedDecoded map[string]any
	json.Unmarshal(faultedRaw, &faultedDecoded)
	if faultedDecoded["intent"] != nil {
		t.Fatalf("projection fault must not expose an intent: %s", faultedRaw)
	}
	if failure, _ := faultedDecoded["failure"].(map[string]any); failure["code"] != "CONFIRMATION_PUBLIC_PROJECTION_INVALID" {
		t.Fatalf("expected the fixed typed projection failure, got %s", faultedRaw)
	}

	healthy, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
	if err != nil {
		t.Fatalf("connection did not survive the projection fault: %v", err)
	}
	if healthy.IsError {
		t.Fatal("healthy show after a prior fault must succeed")
	}
	if metaResourceURI(healthy.Meta) != confirmationResourceURI || healthy.Meta["emission"] != "CONFIRMATION" {
		t.Fatalf("Card must mount for a healthy show after a prior fault: meta=%+v", healthy.Meta)
	}
}

// The MCP SDK contract the Layer 2 safety net relies on: a handler that returns a
// non-nil error yields an IsError result with no structured content and no Card
// meta, and the session remains usable for a following successful call.
func TestToolHandlerErrorYieldsIsErrorWithoutStructuredContent(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fault-sdk", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "always_errors", InputSchema: confirmationReadSchema(false), OutputSchema: confirmationResultSchema()}, func(context.Context, *sdkmcp.CallToolRequest, c.ReadInput) (*sdkmcp.CallToolResult, c.Result, error) {
		// Simulate Layer 2: the projection is unavailable and refuses to emit bad data.
		return nil, c.Result{}, errors.New("confirmation public projection is unavailable")
	})
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fault-sdk-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "always_errors", Arguments: showArguments()})
	if err != nil {
		t.Fatalf("tool error must be reported in-band, not as a transport failure: %v", err)
	}
	if !result.IsError {
		t.Fatal("handler error must produce an IsError result")
	}
	if result.StructuredContent != nil {
		t.Fatalf("no structured content may accompany a Layer 2 error: %#v", result.StructuredContent)
	}
	if metaResourceURI(result.Meta) != "" {
		t.Fatalf("no Card resource may attach to a Layer 2 error: meta=%+v", result.Meta)
	}
}

// Host mount-gate ordering proof: an entry the Host decides to mount (Card meta
// attached) is byte-for-byte accepted by the absolute Card-entry validator on the
// final public JSON, so any entry mounted to a new Card can be bound by that Card.
func TestMountedConfirmationEnvelopeIsCardBindableRoundTrip(t *testing.T) {
	service := &togglingConfirmationService{
		StaticService: newFaultStaticService(t),
		intents:       []c.Intent{liveShapeReviewIntent(), liveShapeReviewIntent()},
		readErr:       []error{nil, nil},
	}
	session, ctx, cancel := connectFaultService(t, service)
	defer cancel()

	first, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
	if err != nil || first.IsError {
		t.Fatalf("show err=%v isError=%v", err, first.IsError)
	}
	if metaResourceURI(first.Meta) != confirmationResourceURI {
		t.Fatal("canonical entry must mount the Card")
	}
	mountedRaw, err := json.Marshal(first.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	// The exact bytes attached to the Card must pass the absolute entry gate.
	if failure := ValidateConfirmationCardEntryProjectionJSON(mountedRaw); failure != nil {
		t.Fatalf("Host mounted an entry the Card cannot bind: %+v (%s)", failure, mountedRaw)
	}
	// Re-show is a pure read: identical envelope and no extra side-effecting call.
	second, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "confirmation_show", Arguments: showArguments()})
	if err != nil || second.IsError {
		t.Fatalf("re-show err=%v isError=%v", err, second.IsError)
	}
	secondRaw, _ := json.Marshal(second.StructuredContent)
	if string(secondRaw) != string(mountedRaw) {
		t.Fatalf("re-show must be pure and byte-identical:\nfirst=%s\nsecond=%s", mountedRaw, secondRaw)
	}
	if service.calls != 2 || service.reads != 2 {
		t.Fatalf("confirmation_show must perform exactly one read per call, got calls=%d reads=%d", service.calls, service.reads)
	}
}
