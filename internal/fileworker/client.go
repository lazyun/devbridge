// Package fileworker consumes one already-started DIRECT_FILE workflow through
// caller-owned MCP and trusted callback code. It is not a host, model launcher,
// sandbox, authorization issuer, or acceptance/recovery controller.
package fileworker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCP must not automatically resend business requests (SDK HTTP MaxRetries:-1).
// Connections, credentials and durable caller journals remain caller-owned.
type MCP interface {
	ListTools(context.Context, *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error)
	CallTool(context.Context, *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error)
}
type Request struct {
	Claim           directfile.WorkerClaimInput
	ResultRequestID string
	// Mandatory acknowledgement of the already-approved mode. RUN submission can
	// dispatch its previously confirmed real backend; this is not extra authority.
	VerificationMode task.VerificationMode
}
type Delivery struct {
	Receipt directfile.WorkerClaimReceipt
	Payload directfile.WorkerExecutionPayload
}
type Completion struct{ Summary, VerificationNotes string }
type Files interface {
	Read(context.Context, string, string) (directfile.Result, error)
	List(context.Context, string, string) (directfile.Result, error)
	Write(context.Context, Mutation) (directfile.Result, error)
}
type Implementer func(context.Context, Delivery, Files) (Completion, error)
type State string

const (
	NotDispatched      State = "NOT_DISPATCHED"
	ClaimUnknown       State = "CLAIM_UNKNOWN"
	DeliveryUnknown    State = "DELIVERY_UNKNOWN"
	CallbackFailed     State = "CALLBACK_FAILED"
	MutationUnknown    State = "MUTATION_UNKNOWN"
	ResultNotSubmitted State = "RESULT_NOT_SUBMITTED"
	ResultUnknown      State = "RESULT_UNKNOWN"
	ResultRecorded     State = "RESULT_RECORDED"
)

type Result struct {
	State         State
	ClaimReceipt  *directfile.WorkerClaimReceipt
	ResultInput   *directfile.ImplementationResultInput
	ResultReceipt *directfile.ImplementationResult
	Mutations     []MutationRecord
}

var (
	ErrAlreadyRun       = errors.New("file worker consumer is single-use; observe original identity")
	ErrFilesClosed      = errors.New("file worker callback capability closed")
	ErrMutationUnknown  = errors.New("file mutation outcome unknown; further file calls and result submission blocked")
	ErrMutationRepeated = errors.New("file mutation identity already used; no replay or replacement")
	ErrContract         = errors.New("file worker MCP contract unavailable")
	ErrResponse         = errors.New("file worker response identity or proof mismatch")
)

type Client struct {
	mcp         MCP
	request     Request
	implementer Implementer
	started     atomic.Bool
	mu          sync.Mutex
	receipt     *directfile.WorkerClaimReceipt
	submitted   *directfile.ImplementationResultInput
	access      *directfile.State
	result      *directfile.ImplementationResult
}

// New generates no IDs. Persist the original full target and all request IDs
// before dispatch. A new Client does not make replaying a callback safe.
func New(mcp MCP, request Request, implementer Implementer) (*Client, error) {
	t := request.Claim.WorkerTarget
	for _, id := range []string{t.AISessionID, t.FeatureAreaID, t.TaskID, t.TaskDefinitionRevisionID, t.DecisionID, t.WorkflowID, t.RuntimeEpoch, request.Claim.RequestID, request.Claim.WorkerID, request.ResultRequestID} {
		if !validID(id) {
			return nil, errors.New("exact bounded original identities required")
		}
	}
	if t.ProtocolVersion != 2 || t.GrantGeneration < 1 || mcp == nil || implementer == nil {
		return nil, errors.New("v2 original grant, transport and implementer required")
	}
	if request.VerificationMode != task.VerificationModeNone && request.VerificationMode != task.VerificationModeRun {
		return nil, errors.New("explicit already-approved verification mode required")
	}
	return &Client{mcp: mcp, request: request, implementer: implementer}, nil
}
func (c *Client) Run(ctx context.Context) (out Result, err error) {
	out.State = NotDispatched
	if !c.started.CompareAndSwap(false, true) {
		out.State = ClaimUnknown
		return out, ErrAlreadyRun
	}
	if _, ok := ctx.Deadline(); !ok {
		return out, errors.New("bounded context required")
	}
	contract, err := preflight(ctx, c.mcp, c.request)
	if err != nil {
		return out, err
	}
	var claim directfile.WorkerClaimResult
	out.State = ClaimUnknown
	if err = contract.call(ctx, c.mcp, "file_worker_claim", c.request.Claim, &claim); err != nil {
		return out, err
	}
	if err = c.validateClaim(claim, true); err != nil {
		return out, err
	}
	if err = c.pinClaim(claim.Receipt); err != nil {
		return out, err
	}
	receipt := claim.Receipt
	out.ClaimReceipt = &receipt
	if claim.ExecutionPayload == nil {
		return c.observed(claim)
	}
	access := copyValue(claim.ExecutionPayload.Access)
	c.mu.Lock()
	c.access = &access
	c.mu.Unlock()
	files := newFiles(ctx, c.mcp, contract, c.request.Claim, access)
	completion, callbackErr := invoke(ctx, c.implementer, Delivery{Receipt: receipt, Payload: copyValue(*claim.ExecutionPayload)}, files)
	files.close()
	out.Mutations = files.records()
	if files.unknown() {
		out.State = MutationUnknown
		return out, ErrMutationUnknown
	}
	if callbackErr != nil {
		out.State = CallbackFailed
		return out, callbackErr
	}
	out.State = ResultNotSubmitted
	if strings.TrimSpace(completion.Summary) == "" || len(completion.Summary) > 16384 || len(completion.VerificationNotes) > 16384 || !utf8.ValidString(completion.Summary) || !utf8.ValidString(completion.VerificationNotes) {
		return out, errors.New("bounded actual completion summary and notes required")
	}
	t := c.request.Claim.WorkerTarget
	worker := c.request.Claim.WorkerClaimIdentity
	input := directfile.ImplementationResultInput{WorkerClaim: &worker, AISessionID: t.AISessionID, TaskID: t.TaskID, TaskDefinitionRevisionID: t.TaskDefinitionRevisionID, WorkflowID: t.WorkflowID, RequestID: c.request.ResultRequestID, Summary: completion.Summary, VerificationNotes: completion.VerificationNotes, ProtocolVersion: 2, RuntimeEpoch: t.RuntimeEpoch, GrantGeneration: t.GrantGeneration}
	if err = contract.validate("file_implementation_result_submit", input); err != nil {
		return out, err
	}
	private := copyValue(input)
	c.mu.Lock()
	c.submitted = &private
	c.mu.Unlock()
	public := copyValue(input)
	out.ResultInput = &public
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out.State = ResultUnknown
	var result directfile.ImplementationResult
	// This existing Core operation can initiate confirmed RUN verification. No
	// follow-up execution/continue/accept call is made by this consumer.
	if err = contract.call(ctx, c.mcp, "file_implementation_result_submit", input, &result); err != nil {
		return out, err
	}
	if err = c.validateAndPinResult(result); err != nil {
		return out, err
	}
	out.State = ResultRecorded
	value := copyValue(result)
	out.ResultReceipt = &value
	return out, nil
}
func invoke(ctx context.Context, fn Implementer, d Delivery, f Files) (out Completion, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("file worker callback panicked; no result synthesized")
		}
	}()
	return fn(ctx, d, f)
}

// Observe is only tools/list and the exact original file_worker_get. It never
// retries a mutation, renews access, resubmits or continues verification.
func (c *Client) Observe(ctx context.Context) (Result, error) {
	out := Result{State: ClaimUnknown}
	contract, err := preflight(ctx, c.mcp, c.request)
	if err != nil {
		return out, err
	}
	var got directfile.WorkerClaimResult
	if err = contract.call(ctx, c.mcp, "file_worker_get", c.request.Claim, &got); err != nil {
		return out, err
	}
	if err = c.validateClaim(got, false); err != nil {
		return out, err
	}
	if err = c.pinClaim(got.Receipt); err != nil {
		return out, err
	}
	return c.observed(got)
}
func (c *Client) pinClaim(r directfile.WorkerClaimReceipt) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.receipt != nil && digestJSON(*c.receipt) != digestJSON(r) {
		return ErrResponse
	}
	private := r
	c.receipt = &private
	return nil
}
func (c *Client) observed(got directfile.WorkerClaimResult) (Result, error) {
	receipt := got.Receipt
	out := Result{State: DeliveryUnknown, ClaimReceipt: &receipt}
	c.mu.Lock()
	if c.submitted != nil {
		v := copyValue(*c.submitted)
		out.ResultInput = &v
	}
	c.mu.Unlock()
	if got.State == directfile.WorkerResultRecorded {
		if got.ResultReceipt == nil {
			return out, ErrResponse
		}
		if err := c.validateAndPinResult(*got.ResultReceipt); err != nil {
			return out, err
		}
		v := copyValue(*got.ResultReceipt)
		out.ResultReceipt = &v
		out.State = ResultRecorded
	}
	return out, nil
}
func (c *Client) validateClaim(got directfile.WorkerClaimResult, allowPayload bool) error {
	r := got.Receipt
	if r.WorkerTarget != c.request.Claim.WorkerTarget || r.WorkerClaimIdentity != c.request.Claim.WorkerClaimIdentity || !validID(r.ClaimID) || r.CreatedAt.IsZero() || !validDigest(r.InstructionSHA256) || !validDigest(r.DefinitionSHA256) || !validDigest(r.WorksetSHA256) || !validDigest(r.PayloadSHA256) {
		return ErrResponse
	}
	if got.State == directfile.WorkerDispatchIntent {
		if !allowPayload || got.Replay || got.ExecutionPayload == nil || got.ResultReceipt != nil {
			return ErrResponse
		}
		p := got.ExecutionPayload
		_, definitionHash, err := task.EncodeDefinition(p.Definition)
		if err != nil || p.Definition.Version != 2 || p.Definition.WorkSpec.ImplementationMode != task.ImplementationDirectFile || p.Instruction == "" || p.Instruction != p.Definition.WorkSpec.CodexInstruction || digest([]byte(p.Instruction)) != r.InstructionSHA256 || definitionHash != r.DefinitionSHA256 || digestJSON(p) != r.PayloadSHA256 || digestJSON(p.Verification) != digestJSON(p.Definition.VerificationSpec) || p.Verification.Mode != c.request.VerificationMode {
			return ErrResponse
		}
		if err = task.ValidateTargetWorkSpec(p.Definition.WorkSpec); err != nil {
			return fmt.Errorf("%w: %v", ErrResponse, err)
		}
		if err = task.ValidateVerificationSpec(p.Verification); err != nil {
			return fmt.Errorf("%w: %v", ErrResponse, err)
		}
		return validateAccess(p.Access, c.request.Claim.WorkerTarget, p.Definition)
	}
	if !got.Replay || got.ExecutionPayload != nil {
		return ErrResponse
	}
	if got.State == directfile.WorkerOutcomeUnknown && got.ResultReceipt == nil {
		return nil
	}
	if got.State == directfile.WorkerResultRecorded && got.ResultReceipt != nil {
		return nil
	}
	return ErrResponse
}
