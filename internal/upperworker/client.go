// Package upperworker consumes one already-authorized v2 UPPER Review through
// an injected MCP client. It does not launch a model, create authority, implement
// a dot runtime, or authenticate a worker label as a host/process identity.
package upperworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP is an already-connected caller-owned transport. It must not automatically
// resend business requests (SDK HTTP clients should set MaxRetries: -1).
// The consumer neither opens connections nor handles credentials.
type MCP interface {
	ListTools(context.Context, *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error)
	CallTool(context.Context, *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}

type Request struct {
	Claim           review.WorkerClaimInput
	ReportRequestID string
}

type Delivery struct {
	Receipt review.WorkerClaimReceipt
	Payload review.WorkerExecutionPayload
}

// Files exposes only bound Review reads. It cannot select a different target,
// close the grant, submit a report, or access a generic MCP tool. It is revoked
// when the callback returns. The injected callback is trusted, not sandboxed.
type Files interface {
	Read(context.Context, string, string) (directfile.Result, error)
	List(context.Context, string, string) (directfile.Result, error)
}

type Reviewer func(context.Context, Delivery, Files) (review.ReviewReport, error)

type State string

const (
	NotDispatched      State = "NOT_DISPATCHED"
	ClaimUnknown       State = "CLAIM_UNKNOWN"
	DeliveryUnknown    State = "DELIVERY_UNKNOWN"
	CallbackFailed     State = "CALLBACK_FAILED"
	ReportNotSubmitted State = "REPORT_NOT_SUBMITTED"
	ReportUnknown      State = "REPORT_UNKNOWN"
	ResultRecorded     State = "RESULT_RECORDED"
)

// These are consumer observations, not new Core authority or retry permission.
// ReportInput retains the exact locally produced submission for caller-owned
// journaling. An observed historical receipt never proves this callback ran.
type Result struct {
	State         State
	ClaimReceipt  *review.WorkerClaimReceipt
	ReportInput   *review.SubmitInput
	ReportReceipt *review.ReportReceipt
}

var (
	ErrAlreadyRun   = errors.New("upper worker consumer is single-use; observe the original request")
	ErrReaderClosed = errors.New("upper worker callback file access is closed")
	ErrContract     = errors.New("upper worker MCP contract unavailable")
	ErrResponse     = errors.New("upper worker response identity or proof mismatch")
)

type Client struct {
	mcp       MCP
	request   Request
	reviewer  Reviewer
	started   atomic.Bool
	mu        sync.Mutex
	submitted *review.SubmitInput
	receipt   *review.WorkerClaimReceipt
}

// New requires caller-owned stable identities. Persist both request IDs and the
// full target BEFORE Run. No identity is generated or replaced by this client.
func New(mcp MCP, request Request, reviewer Reviewer) (*Client, error) {
	t := request.Claim.UpperTarget
	for _, id := range []string{t.AISessionID, t.FeatureAreaID, t.TaskID, t.ReviewID, t.RunID, t.RuntimeEpoch, request.Claim.RequestID, request.Claim.WorkerID, request.ReportRequestID} {
		if !validID(id) {
			return nil, errors.New("exact bounded original worker and report identities required")
		}
	}
	if t.GrantGeneration < 1 || mcp == nil || reviewer == nil {
		return nil, errors.New("MCP, reviewer and positive original grant generation required")
	}
	return &Client{mcp: mcp, request: request, reviewer: reviewer}, nil
}

func validID(s string) bool {
	return s != "" && len(s) <= 128 && strings.IndexFunc(s, unicode.IsSpace) < 0 && !strings.ContainsRune(s, 0)
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func digestJSON(v any) string  { raw, _ := json.Marshal(v); return digest(raw) }
func validDigest(s string) bool {
	raw, err := hex.DecodeString(s)
	return err == nil && len(raw) == 32 && s == strings.ToLower(s)
}
func copyValue[T any](v T) T {
	raw, _ := json.Marshal(v)
	var out T
	_ = json.Unmarshal(raw, &out)
	return out
}

// Run makes at most one claim and one report submission. All errors after either
// call are conservatively uncertain; there is no automatic get, resend, new
// request, report fabrication, cancellation, recovery, or successor operation.
// The caller supplies a bounded context; a trusted callback must honor it.
func (c *Client) Run(ctx context.Context) (out Result, err error) {
	out.State = NotDispatched
	if !c.started.CompareAndSwap(false, true) {
		out.State = ClaimUnknown // A prior invocation may already have dispatched.
		return out, ErrAlreadyRun
	}
	if _, ok := ctx.Deadline(); !ok {
		return out, errors.New("bounded context required")
	}
	contracts, err := preflight(ctx, c.mcp, c.request)
	if err != nil {
		return out, err
	}
	var access review.UpperAccess
	if err = contracts.call(ctx, c.mcp, "review_access_get", map[string]any{"aiSessionId": c.request.Claim.AISessionID, "reviewId": c.request.Claim.ReviewID}, &access); err != nil {
		return out, err
	}
	if err = validateAccess(access, c.request.Claim.UpperTarget); err != nil {
		return out, err
	}
	var claim review.WorkerClaimResult
	out.State = ClaimUnknown
	if err = contracts.call(ctx, c.mcp, "review_worker_claim", c.request.Claim, &claim); err != nil {
		return out, err
	}
	if err = c.validateClaim(claim, true); err != nil {
		return out, err
	}
	receipt := claim.Receipt
	c.mu.Lock()
	privateReceipt := receipt
	c.receipt = &privateReceipt
	c.mu.Unlock()
	out.ClaimReceipt = &receipt
	if claim.ExecutionPayload == nil {
		return c.observed(claim, nil)
	}
	// Observation timestamps can change, but the frozen delivered member set and
	// authority must still match the exact pre-claim read. The server-only binding
	// digest includes private SQL facts and is preserved, not independently rebuilt.
	delivered := claim.ExecutionPayload.Access
	if !sameAuthority(access, delivered) {
		return out, ErrResponse
	}
	files := newFiles(ctx, c.mcp, contracts, delivered)
	report, callbackErr := invoke(ctx, c.reviewer, Delivery{Receipt: receipt, Payload: copyValue(*claim.ExecutionPayload)}, files)
	files.close()
	if callbackErr != nil {
		out.State = CallbackFailed
		return out, callbackErr
	}
	report = copyValue(report)
	out.State = ReportNotSubmitted
	if err = report.Validate(); err != nil {
		return out, err
	}
	if report.SchemaVersion != 2 {
		return out, errors.New("this consumer requires a v2 Review report")
	}
	worker := c.request.Claim.WorkerClaimIdentity
	input := review.SubmitInput{UpperTarget: c.request.Claim.UpperTarget, WorkerClaim: &worker, RequestID: c.request.ReportRequestID, Report: report}
	if err = contracts.validate("review_result_submit", input); err != nil {
		return out, err
	}
	// Freeze before dispatch. A callback cannot retain a pointer into this input.
	c.mu.Lock()
	saved := copyValue(input)
	c.submitted = &saved
	c.mu.Unlock()
	out.ReportInput = &input
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.State = ReportUnknown
	var result review.ReportReceipt
	if err = contracts.call(ctx, c.mcp, "review_result_submit", input, &result); err != nil {
		return out, err
	}
	if err = c.validateReport(result, &input); err != nil {
		return out, err
	}
	out.State = ResultRecorded
	out.ReportReceipt = &result
	return out, nil
}

func invoke(ctx context.Context, reviewer Reviewer, delivery Delivery, files Files) (report review.ReviewReport, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("upper worker callback panicked; no report submitted")
		}
	}()
	return reviewer(ctx, delivery, files)
}

// Observe performs only tools/list and the exact original review_worker_get.
// It works after Core restart without refreshing access. Missing/invalid results
// remain unknown. Never invoke Run/callback as a recovery action.
func (c *Client) Observe(ctx context.Context) (Result, error) {
	out := Result{State: ClaimUnknown}
	contracts, err := preflight(ctx, c.mcp, c.request)
	if err != nil {
		return out, err
	}
	var got review.WorkerClaimResult
	if err = contracts.call(ctx, c.mcp, "review_worker_get", c.request.Claim, &got); err != nil {
		return out, err
	}
	if err = c.validateClaim(got, false); err != nil {
		return out, err
	}
	c.mu.Lock()
	if c.receipt != nil && digestJSON(*c.receipt) != digestJSON(got.Receipt) {
		c.mu.Unlock()
		return out, ErrResponse
	}
	if c.receipt == nil {
		original := got.Receipt
		c.receipt = &original
	}
	var submitted *review.SubmitInput
	if c.submitted != nil {
		v := copyValue(*c.submitted)
		submitted = &v
	}
	c.mu.Unlock()
	return c.observed(got, submitted)
}

func (c *Client) observed(got review.WorkerClaimResult, submitted *review.SubmitInput) (Result, error) {
	receipt := got.Receipt
	out := Result{State: DeliveryUnknown, ClaimReceipt: &receipt, ReportInput: submitted}
	if got.State == review.WorkerResultRecorded {
		if got.ResultReceipt == nil {
			return out, ErrResponse
		}
		if err := c.validateReport(*got.ResultReceipt, submitted); err != nil {
			return out, err
		}
		out.State = ResultRecorded
		result := *got.ResultReceipt
		out.ReportReceipt = &result
	}
	return out, nil
}

func (c *Client) validateReport(r review.ReportReceipt, submitted *review.SubmitInput) error {
	if r.RequestID != c.request.ReportRequestID || r.ReviewID != c.request.Claim.ReviewID || r.RunID != c.request.Claim.RunID || !validID(r.ReportID) || r.Status != "COMPLETED" || r.CompletedAt.IsZero() || !validDigest(r.PayloadSHA256) {
		return ErrResponse
	}
	switch r.Disposition {
	case review.DispositionApprovalReady, review.DispositionFurtherReviewRequired, review.DispositionBlocked:
	default:
		return ErrResponse
	}
	if submitted != nil && (r.PayloadSHA256 != digestJSON(*submitted) || r.Disposition != submitted.Report.Disposition) {
		return ErrResponse
	}
	return nil
}

func (c *Client) validateClaim(got review.WorkerClaimResult, allowPayload bool) error {
	r := got.Receipt
	if r.UpperTarget != c.request.Claim.UpperTarget || r.WorkerClaimIdentity != c.request.Claim.WorkerClaimIdentity || !validID(r.ClaimID) || r.CreatedAt.IsZero() || !validDigest(r.InstructionSHA256) || !validDigest(r.PayloadSHA256) || !validDigest(r.WorksetSHA256) {
		return ErrResponse
	}
	if got.State == review.WorkerDispatchIntent {
		if !allowPayload || got.Replay || got.ExecutionPayload == nil || got.ResultReceipt != nil {
			return ErrResponse
		}
		p := got.ExecutionPayload
		if p.Instruction == "" {
			return ErrResponse
		}
		if digest([]byte(p.Instruction)) != r.InstructionSHA256 || digestJSON(p) != r.PayloadSHA256 {
			return ErrResponse
		}
		return validateAccess(p.Access, c.request.Claim.UpperTarget)
	}
	if !got.Replay || got.ExecutionPayload != nil {
		return ErrResponse
	}
	if got.State == review.WorkerOutcomeUnknown && got.ResultReceipt == nil {
		return nil
	}
	if got.State == review.WorkerResultRecorded && got.ResultReceipt != nil {
		return c.validateReport(*got.ResultReceipt, nil)
	}
	return ErrResponse
}

func validateAccess(a review.UpperAccess, target review.UpperTarget) error {
	if a.Target != target || a.ProtocolVersion != 2 || a.State != "ACTIVE" || a.Blocker != "" || a.WorkspaceID == "" {
		return ErrResponse
	}
	if err := workset.ValidateTargets(a.RepositoryTargets); err != nil {
		return fmt.Errorf("%w: %v", ErrResponse, err)
	}
	for _, member := range a.RepositoryTargets {
		if member.Write.Kind != "NONE" {
			return ErrResponse
		}
	}
	return nil
}
func sameAuthority(a, b review.UpperAccess) bool {
	a.LastObservedAt = nil
	b.LastObservedAt = nil
	return digestJSON(a) == digestJSON(b)
}

func decode(result *sdkmcp.CallToolResult, out any) error {
	if result == nil || result.IsError || result.StructuredContent == nil {
		return ErrResponse
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	if len(raw) > 2<<20 {
		return errors.New("upper worker response exceeds bound")
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return ErrResponse
	}
	if nested, ok := fields["result"]; ok {
		raw = nested
		fields = nil
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return ErrResponse
		}
	}
	// A missing boolean is not an explicit first-delivery assertion. Avoid
	// accepting a partial object merely because Go filled absent fields with zero.
	if _, ok := out.(*review.WorkerClaimResult); ok {
		for _, key := range []string{"receipt", "state", "replay"} {
			value, present := fields[key]
			if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return ErrResponse
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", ErrResponse, err)
	}
	return nil
}
