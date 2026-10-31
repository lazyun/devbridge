package upperworker

// Synthetic wire tests exercise client refusal and orchestration only. Actual
// Core authority/effects are covered separately by controlapi HTTP integration.
import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type fakeMCP struct {
	tools       []*sdkmcp.Tool
	list        func(*sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error)
	call        func(*sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
	mu          sync.Mutex
	calls       []string
	callContext func(context.Context, *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

func (f *fakeMCP) ListTools(_ context.Context, p *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
	if f.list != nil {
		return f.list(p)
	}
	return &sdkmcp.ListToolsResult{Tools: f.tools}, nil
}
func (f *fakeMCP) CallTool(ctx context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, p.Name)
	f.mu.Unlock()
	if f.callContext != nil {
		return f.callContext(ctx, p)
	}
	if f.call == nil {
		return nil, errors.New("unexpected call")
	}
	return f.call(p)
}
func bounded(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func catalog(t *testing.T) []*sdkmcp.Tool {
	t.Helper()
	ctx := bounded(t)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "schema-only", Version: "unit"}, nil)
	mcpserver.RegisterSurfaceBusinessTools(server, &mcpserver.StaticService{})
	a, b := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "schema-only", Version: "unit"}, nil).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return list.Tools
}
func requestFixture() Request {
	return Request{Claim: review.WorkerClaimInput{UpperTarget: review.UpperTarget{AISessionID: "session", FeatureAreaID: "area", TaskID: "task", ReviewID: "review_unit", RunID: "run", RuntimeEpoch: "epoch", GrantGeneration: 1}, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "claim", WorkerID: "worker"}}, ReportRequestID: "report"}
}
func claimFixture(r Request) review.WorkerClaimResult {
	access := review.UpperAccess{Target: r.Claim.UpperTarget, State: "ACTIVE", ProtocolVersion: 2, WorkspaceID: "workspace", Baseline: strings.Repeat("a", 40), StartSnapshot: "snapshot", AllowedPaths: []string{"."}, ForbiddenPaths: []string{".git"}, RepositoryTargets: []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}}
	payload := review.WorkerExecutionPayload{Instruction: "unchanged\nengineering bytes", Access: access}
	return review.WorkerClaimResult{State: review.WorkerDispatchIntent, Receipt: review.WorkerClaimReceipt{UpperTarget: r.Claim.UpperTarget, WorkerClaimIdentity: r.Claim.WorkerClaimIdentity, ClaimID: "claim-id", InstructionSHA256: digest([]byte(payload.Instruction)), PayloadSHA256: digestJSON(payload), WorksetSHA256: strings.Repeat("a", 64), CreatedAt: time.Now().UTC()}, ExecutionPayload: &payload}
}
func reportFixture() review.ReviewReport {
	return review.ReviewReport{SchemaVersion: 2, Summary: "unit-only report", Disposition: review.DispositionBlocked, Coverage: []string{"unit"}, Findings: []review.Finding{}, Limitations: []string{"not real Core evidence"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}
}
func wire(v any) *sdkmcp.CallToolResult { return &sdkmcp.CallToolResult{StructuredContent: v} }
func noReview(context.Context, Delivery, Files) (review.ReviewReport, error) {
	return reportFixture(), nil
}

func TestNewRequiresExactIdentity(t *testing.T) {
	fields := []func(*Request){func(r *Request) { r.Claim.AISessionID = "" }, func(r *Request) { r.Claim.ReviewID = "foreign space" }, func(r *Request) { r.Claim.RunID = "" }, func(r *Request) { r.Claim.RuntimeEpoch = "" }, func(r *Request) { r.Claim.GrantGeneration = 0 }, func(r *Request) { r.Claim.RequestID = "" }, func(r *Request) { r.Claim.WorkerID = "" }, func(r *Request) { r.ReportRequestID = "" }}
	for n, change := range fields {
		r := requestFixture()
		change(&r)
		if _, err := New(&fakeMCP{}, r, noReview); err == nil {
			t.Fatalf("invalid identity %d accepted", n)
		}
	}
	if _, err := New(nil, requestFixture(), noReview); err == nil {
		t.Fatal("nil transport accepted")
	}
	if _, err := New(&fakeMCP{}, requestFixture(), nil); err == nil {
		t.Fatal("nil callback accepted")
	}
}
func TestPreflightRejectsCatalogAndSchemaDriftBeforeCalls(t *testing.T) {
	tools := catalog(t)
	cases := []struct {
		name string
		edit func(*fakeMCP)
	}{
		{"missing-tool", func(f *fakeMCP) {
			for i, v := range f.tools {
				if v.Name == "review_worker_get" {
					f.tools = append(f.tools[:i:i], f.tools[i+1:]...)
					break
				}
			}
		}},
		{"duplicate", func(f *fakeMCP) { f.tools = append(f.tools, f.tools[0]) }},
		{"unbounded-object", func(f *fakeMCP) {
			for i, v := range f.tools {
				if v.Name == "review_worker_claim" {
					x := *v
					x.InputSchema = map[string]any{"type": "object"}
					f.tools[i] = &x
				}
			}
		}},
		{"missing-workerClaim", func(f *fakeMCP) {
			for i, v := range f.tools {
				if v.Name == "review_result_submit" {
					x := *v
					schema := copyValue(v.InputSchema)
					var m map[string]any
					raw := jsonBytes(schema)
					decodeJSON(raw, &m)
					delete(m["properties"].(map[string]any), "workerClaim")
					x.InputSchema = m
					f.tools[i] = &x
				}
			}
		}},
		{"repeated-cursor", func(f *fakeMCP) {
			f.list = func(*sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
				return &sdkmcp.ListToolsResult{NextCursor: "loop"}, nil
			}
		}},
		{"nil-page", func(f *fakeMCP) {
			f.list = func(*sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) { return nil, nil }
		}},
		{"list-error", func(f *fakeMCP) {
			f.list = func(*sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) { return nil, errors.New("offline") }
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeMCP{tools: append([]*sdkmcp.Tool{}, tools...)}
			tc.edit(f)
			c, _ := New(f, requestFixture(), noReview)
			out, err := c.Run(bounded(t))
			if err == nil || out.State != NotDispatched || len(f.calls) != 0 {
				t.Fatalf("%+v %v calls=%v", out, err, f.calls)
			}
		})
	}
}
func TestPaginatedSchemaPreflightAndDeadline(t *testing.T) {
	tools := catalog(t)
	f := &fakeMCP{list: func(p *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
		if p.Cursor == "" {
			return &sdkmcp.ListToolsResult{Tools: tools[:len(tools)/2], NextCursor: "second"}, nil
		}
		return &sdkmcp.ListToolsResult{Tools: tools[len(tools)/2:]}, nil
	}}
	if _, err := preflight(bounded(t), f, requestFixture()); err != nil {
		t.Fatal(err)
	}
	c, _ := New(f, requestFixture(), noReview)
	out, err := c.Run(context.Background())
	if err == nil || out.State != NotDispatched || len(f.calls) != 0 {
		t.Fatal("unbounded invocation dispatched")
	}
}
func TestClaimProofRefusesMismatchesAndExecutableReadback(t *testing.T) {
	r := requestFixture()
	c, _ := New(&fakeMCP{}, r, noReview)
	cases := []struct {
		name   string
		change func(*review.WorkerClaimResult)
	}{
		{"foreign-session", func(g *review.WorkerClaimResult) { g.Receipt.AISessionID = "foreign" }},
		{"foreign-worker", func(g *review.WorkerClaimResult) { g.Receipt.WorkerID = "foreign" }},
		{"instruction", func(g *review.WorkerClaimResult) { g.ExecutionPayload.Instruction += "changed" }},
		{"payload", func(g *review.WorkerClaimResult) { g.Receipt.PayloadSHA256 = strings.Repeat("b", 64) }},
		{"empty-workset-digest", func(g *review.WorkerClaimResult) { g.Receipt.WorksetSHA256 = "" }},
		{"replayed-payload", func(g *review.WorkerClaimResult) { g.Replay = true }},
		{"unknown-with-payload", func(g *review.WorkerClaimResult) { g.State = review.WorkerOutcomeUnknown; g.Replay = true }},
		{"foreign-access", func(g *review.WorkerClaimResult) {
			g.ExecutionPayload.Access.Target.RunID = "foreign"
			g.Receipt.PayloadSHA256 = digestJSON(g.ExecutionPayload)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := claimFixture(r)
			tc.change(&g)
			if c.validateClaim(g, true) == nil {
				t.Fatal("invalid proof accepted")
			}
		})
	}
	g := claimFixture(r)
	if err := c.validateClaim(g, true); err != nil {
		t.Fatal(err)
	}
	if c.validateClaim(g, false) == nil {
		t.Fatal("get payload accepted")
	}
	g.ExecutionPayload = nil
	g.Replay = true
	g.State = review.WorkerOutcomeUnknown
	if err := c.validateClaim(g, false); err != nil {
		t.Fatal(err)
	}
}
func TestRunConcurrentOneShotAndCallbackFailure(t *testing.T) {
	tools := catalog(t)
	r := requestFixture()
	claim := claimFixture(r)
	f := &fakeMCP{tools: tools, call: func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		switch p.Name {
		case "review_access_get":
			return wire(claim.ExecutionPayload.Access), nil
		case "review_worker_claim":
			return wire(map[string]any{"result": claim, "workflow": map[string]any{"stale": true}}), nil
		}
		return nil, errors.New("unexpected")
	}}
	var n atomic.Int32
	var files Files
	c, _ := New(f, r, func(_ context.Context, _ Delivery, got Files) (review.ReviewReport, error) {
		n.Add(1)
		files = got
		return review.ReviewReport{}, errors.New("real callback failure")
	})
	ctx := bounded(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan Result, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; out, err := c.Run(ctx); results <- out; errs <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	counts := map[State]int{}
	for out := range results {
		counts[out.State]++
	}
	for err := range errs {
		if err == nil {
			t.Fatal("failed callback reported success")
		}
	}
	if n.Load() != 1 || counts[CallbackFailed] != 1 || counts[ClaimUnknown] != 7 || !reflect.DeepEqual(f.calls, []string{"review_access_get", "review_worker_claim"}) {
		t.Fatalf("callbacks=%d states=%v calls=%v", n.Load(), counts, f.calls)
	}
	if _, err := files.Read(ctx, "member", "same.txt"); !errors.Is(err, ErrReaderClosed) {
		t.Fatal("retained reader alive", err)
	}
}
func TestRunInvalidPayloadNeverInvokesCallback(t *testing.T) {
	tools := catalog(t)
	r := requestFixture()
	claim := claimFixture(r)
	access := copyValue(claim.ExecutionPayload.Access)
	claim.ExecutionPayload.Instruction = "tampered"
	f := &fakeMCP{tools: tools, call: func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		if p.Name == "review_access_get" {
			return wire(access), nil
		}
		return wire(claim), nil
	}}
	c, _ := New(f, r, func(context.Context, Delivery, Files) (review.ReviewReport, error) {
		t.Fatal("invalid delivery executed")
		return review.ReviewReport{}, nil
	})
	out, err := c.Run(bounded(t))
	if err == nil || out.State != ClaimUnknown || len(f.calls) != 2 {
		t.Fatalf("%+v %v %v", out, err, f.calls)
	}
}

func jsonBytes(v any) []byte       { raw, _ := json.Marshal(v); return raw }
func decodeJSON(raw []byte, v any) { _ = json.Unmarshal(raw, v) }

func TestReportUncertaintyNeverRepeatsCallbackOrSubmission(t *testing.T) {
	tools := catalog(t)
	cases := []struct {
		name           string
		alter          func(*review.ReportReceipt) *sdkmcp.CallToolResult
		transportError bool
	}{
		{name: "transport-loss", transportError: true},
		{name: "nil-result", alter: func(*review.ReportReceipt) *sdkmcp.CallToolResult { return nil }},
		{name: "isError", alter: func(*review.ReportReceipt) *sdkmcp.CallToolResult { return &sdkmcp.CallToolResult{IsError: true} }},
		{name: "malformed", alter: func(*review.ReportReceipt) *sdkmcp.CallToolResult { return wire(map[string]any{"status": 17}) }},
		{name: "foreign-report-id", alter: func(r *review.ReportReceipt) *sdkmcp.CallToolResult { r.RequestID = "different"; return wire(r) }},
		{name: "report-only-hash", alter: func(r *review.ReportReceipt) *sdkmcp.CallToolResult {
			r.PayloadSHA256 = digestJSON(reportFixture())
			return wire(r)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFixture()
			claim := claimFixture(r)
			var sent review.SubmitInput
			var receipt review.ReportReceipt
			callbacks := 0
			f := &fakeMCP{tools: tools, call: func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				switch p.Name {
				case "review_access_get":
					return wire(claim.ExecutionPayload.Access), nil
				case "review_worker_claim":
					return wire(claim), nil
				case "review_result_submit":
					sent = copyValue(p.Arguments.(review.SubmitInput))
					receipt = review.ReportReceipt{RequestID: r.ReportRequestID, ReviewID: r.Claim.ReviewID, RunID: r.Claim.RunID, ReportID: "report-id", Status: "COMPLETED", Disposition: sent.Report.Disposition, PayloadSHA256: digestJSON(sent), CompletedAt: time.Now().UTC()}
					if tc.transportError {
						return nil, errors.New("lost response")
					}
					changed := receipt
					return tc.alter(&changed), nil
				case "review_worker_get":
					g := claim
					g.ExecutionPayload = nil
					g.Replay = true
					g.State = review.WorkerResultRecorded
					g.ResultReceipt = &receipt
					return wire(g), nil
				}
				return nil, errors.New("unexpected")
			}}
			c, _ := New(f, r, func(context.Context, Delivery, Files) (review.ReviewReport, error) {
				callbacks++
				return reportFixture(), nil
			})
			ctx := bounded(t)
			out, err := c.Run(ctx)
			if err == nil || out.State != ReportUnknown || out.ReportInput == nil || callbacks != 1 {
				t.Fatalf("%+v %v callbacks=%d", out, err, callbacks)
			}
			// Public result mutation cannot alter the private exact input used to verify
			// later readback. This is not a persistent journal or host recovery claim.
			out.ReportInput.Report.Summary = "caller changed returned copy"
			out.ClaimReceipt.WorksetSHA256 = strings.Repeat("b", 64)
			seen, err := c.Observe(ctx)
			if err != nil || seen.State != ResultRecorded || !reflect.DeepEqual(seen.ReportReceipt, &receipt) || seen.ReportInput.Report.Summary != sent.Report.Summary {
				t.Fatalf("readback %+v %v", seen, err)
			}
			if callbacks != 1 || !reflect.DeepEqual(f.calls, []string{"review_access_get", "review_worker_claim", "review_result_submit", "review_worker_get"}) {
				t.Fatalf("unsafe repetition %d %v", callbacks, f.calls)
			}
		})
	}
}

func TestObserveRejectsChangedImmutableClaimAndNeverInvokesReviewer(t *testing.T) {
	tools := catalog(t)
	r := requestFixture()
	claim := claimFixture(r)
	changed := false
	f := &fakeMCP{tools: tools, call: func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		g := claim
		g.ExecutionPayload = nil
		g.Replay = true
		g.State = review.WorkerOutcomeUnknown
		if changed {
			g.Receipt.WorksetSHA256 = strings.Repeat("b", 64)
		}
		return wire(g), nil
	}}
	c, _ := New(f, r, func(context.Context, Delivery, Files) (review.ReviewReport, error) {
		t.Fatal("read invoked reviewer")
		return review.ReviewReport{}, nil
	})
	first, err := c.Observe(bounded(t))
	if err != nil || first.State != DeliveryUnknown {
		t.Fatalf("first observation %+v %v", first, err)
	}
	first.ClaimReceipt.WorkerID = "caller-mutated-copy"
	changed = true
	out, err := c.Observe(bounded(t))
	if err == nil || out.State != ClaimUnknown || !reflect.DeepEqual(f.calls, []string{"review_worker_get", "review_worker_get"}) {
		t.Fatalf("%+v %v %v", out, err, f.calls)
	}
}

func TestBoundReadsRefuseEscapesBeforeTransport(t *testing.T) {
	r := requestFixture()
	claim := claimFixture(r)
	f := &fakeMCP{}
	files := newFiles(bounded(t), f, nil, claim.ExecutionPayload.Access)
	defer files.close()
	for _, pair := range [][2]string{{"foreign", "same.txt"}, {"member", "../other"}, {"member", "/absolute"}, {"member", "a/../b"}, {"member", "a\\b"}} {
		if _, err := files.Read(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatalf("accepted %v", pair)
		}
	}
	if len(f.calls) != 0 {
		t.Fatal("invalid read reached transport")
	}
}

func TestClaimDecodeRequiresExplicitReplayFlag(t *testing.T) {
	g := claimFixture(requestFixture())
	var raw map[string]any
	decodeJSON(jsonBytes(g), &raw)
	delete(raw, "replay")
	var got review.WorkerClaimResult
	if decode(wire(map[string]any{"result": raw, "replay": false}), &got) == nil {
		t.Fatal("missing replay flag accepted as first delivery")
	}
	raw["replay"] = nil
	if decode(wire(map[string]any{"result": raw}), &got) == nil {
		t.Fatal("null replay flag accepted")
	}
}

func TestClosingReaderCancelsInFlightRead(t *testing.T) {
	f := &fakeMCP{tools: catalog(t)}
	r := requestFixture()
	ctx := bounded(t)
	contract, err := preflight(ctx, f, r)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	f.callContext = func(ctx context.Context, _ *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	files := newFiles(ctx, f, contract, claimFixture(r).ExecutionPayload.Access)
	done := make(chan error, 1)
	go func() { _, err := files.Read(context.Background(), "member", "same.txt"); done <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("reader did not enter")
	}
	closed := make(chan struct{})
	go func() { files.close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("close failed to cancel bound background read")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reader did not join")
	}
}

func TestReturnedReceiptMutationDoesNotAffectConcurrentObserve(t *testing.T) {
	r := requestFixture()
	claim := claimFixture(r)
	f := &fakeMCP{tools: catalog(t), call: func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		switch p.Name {
		case "review_access_get":
			return wire(claim.ExecutionPayload.Access), nil
		case "review_worker_claim":
			return wire(claim), nil
		case "review_worker_get":
			g := claim
			g.State = review.WorkerOutcomeUnknown
			g.Replay = true
			g.ExecutionPayload = nil
			return wire(g), nil
		}
		return nil, errors.New("unexpected")
	}}
	c, _ := New(f, r, func(context.Context, Delivery, Files) (review.ReviewReport, error) {
		return review.ReviewReport{}, errors.New("callback failed")
	})
	ctx := bounded(t)
	out, err := c.Run(ctx)
	if err == nil || out.ClaimReceipt == nil {
		t.Fatal("expected delivered failed callback")
	}
	stop, done, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				out.ClaimReceipt.WorkerID = "caller-mutated-copy"
			}
		}
	}()
	<-started
	defer func() { close(stop); <-done }()
	for range 5 {
		observed, err := c.Observe(ctx)
		if err != nil || observed.State != DeliveryUnknown || observed.ClaimReceipt.WorkerID != r.Claim.WorkerID {
			t.Fatalf("mutable public receipt corrupted baseline: %+v %v", observed, err)
		}
	}
}
