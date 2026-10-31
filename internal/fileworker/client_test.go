package fileworker

// Synthetic responses test client orchestration/refusal only; real Core admission
// and actual effects live in separate controlapi integration fixtures.
import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	return Request{Claim: directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: "session", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", DecisionID: "decision", WorkflowID: "workflow", ProtocolVersion: 2, RuntimeEpoch: "epoch", GrantGeneration: 1}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "claim", WorkerID: "worker"}}, ResultRequestID: "result", VerificationMode: task.VerificationModeNone}
}
func claimFixture(r Request) directfile.WorkerClaimResult {
	target := workset.Target{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt", "new.txt", "moved.txt"}}}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "synthetic unit work", Constraints: []string{}, CodexInstruction: "unchanged\nengineering bytes", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verify := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "synthetic local fixture"}}
	if r.VerificationMode == task.VerificationModeRun {
		verify = task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "same", Checks: []task.VerificationCheck{{Kind: task.VerificationCheckTestCommand, Command: "true", Required: true, RepositoryID: target.RepositoryID, WorkingDirectory: ".", RepositoryIDs: []string{target.RepositoryID}}}, AcceptanceCriteria: []string{"observed"}, CodexInstruction: "verify exact"}
	}
	definition := task.Definition{Version: 2, ProjectID: "project", FeatureAreaID: r.Claim.FeatureAreaID, RepositoryTargets: []workset.Target{target}, WorkSpec: work, VerificationSpec: verify, Goals: []task.DefinitionGoal{}}
	access := directfile.State{State: "ACTIVE", Enabled: true, AISessionID: r.Claim.AISessionID, FeatureAreaID: r.Claim.FeatureAreaID, ProjectID: "project", TaskID: r.Claim.TaskID, TaskDefinitionRevisionID: r.Claim.TaskDefinitionRevisionID, DecisionID: r.Claim.DecisionID, WorkflowID: r.Claim.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: r.Claim.RuntimeEpoch, GrantGeneration: r.Claim.GrantGeneration, WorkspaceID: "workspace", ExecutionScope: work.ExecutionScope, AllowedOperations: []string{"READ", "LIST", "CREATE", "REPLACE", "DELETE", "MOVE"}, RepositoryTargets: []directfile.MemberGrant{{Target: target, WorkspaceID: "workspace", WorktreeID: "worktree", State: "READY", StartHead: strings.Repeat("a", 40), StartSnapshot: strings.Repeat("b", 40)}}}
	payload := directfile.WorkerExecutionPayload{Instruction: work.CodexInstruction, Definition: definition, Access: access, Verification: verify}
	_, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		panic(err)
	}
	return directfile.WorkerClaimResult{State: directfile.WorkerDispatchIntent, Receipt: directfile.WorkerClaimReceipt{WorkerTarget: r.Claim.WorkerTarget, WorkerClaimIdentity: r.Claim.WorkerClaimIdentity, ClaimID: "file-claim", InstructionSHA256: digest([]byte(payload.Instruction)), DefinitionSHA256: hash, WorksetSHA256: strings.Repeat("c", 64), PayloadSHA256: digestJSON(payload), CreatedAt: time.Now().UTC()}, ExecutionPayload: &payload}
}
func resultFixture(r Request, claim directfile.WorkerClaimResult, in directfile.ImplementationResultInput) directfile.ImplementationResult {
	m := claim.ExecutionPayload.Access.RepositoryTargets[0]
	result := directfile.ImplementationResult{ID: "result-id", AISessionID: r.Claim.AISessionID, TaskID: r.Claim.TaskID, TaskDefinitionRevisionID: r.Claim.TaskDefinitionRevisionID, WorkflowID: r.Claim.WorkflowID, RequestID: r.ResultRequestID, Summary: in.Summary, VerificationNotes: in.VerificationNotes, ProtocolVersion: 2, RuntimeEpoch: r.Claim.RuntimeEpoch, GrantGeneration: r.Claim.GrantGeneration, VerificationStatus: "NOT_RUN", RepositoryTargets: claim.ExecutionPayload.Definition.RepositoryTargets, Members: []directfile.MemberResult{{Target: m.Target, WorkspaceID: m.WorkspaceID, WorktreeID: m.WorktreeID, Branch: "feature", StartHead: m.StartHead, StartSnapshot: m.StartSnapshot, EndSnapshot: m.StartSnapshot, State: "NO_CHANGE", ChangedPaths: []string{}}}}
	if r.VerificationMode == task.VerificationModeRun {
		result.ExecutionID = "execution"
		result.VerificationStatus = "READY"
		result.VerificationStartError = "explicit synthetic dispatch failure"
	}
	return result
}
func wire(v any) *sdkmcp.CallToolResult { return &sdkmcp.CallToolResult{StructuredContent: v} }
func complete(context.Context, Delivery, Files) (Completion, error) {
	return Completion{Summary: "synthetic completion", VerificationNotes: "no execution claimed"}, nil
}
func mutation() Mutation {
	return Mutation{RequestID: "write-one", Action: ActionReplace, MemberID: "member", Path: "same.txt", Content: "new", ExpectedDigest: digest([]byte("old"))}
}
func callNames(f *fakeMCP) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls...)
}
func happyFake(t *testing.T, r Request) (*fakeMCP, directfile.WorkerClaimResult) {
	claim := claimFixture(r)
	f := &fakeMCP{tools: catalog(t)}
	var recorded *directfile.ImplementationResult
	var mu sync.Mutex
	f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		switch p.Name {
		case "file_worker_claim":
			return wire(copyValue(claim)), nil
		case "file_worker_get":
			out := copyValue(claim)
			out.State = directfile.WorkerOutcomeUnknown
			out.Replay = true
			out.ExecutionPayload = nil
			if recorded != nil {
				v := copyValue(*recorded)
				out.ResultReceipt = &v
				out.State = directfile.WorkerResultRecorded
			}
			return wire(out), nil
		case "file_read":
			return wire(directfile.Result{Path: p.Arguments.(map[string]any)["path"].(string), Content: "old", Digest: digest([]byte("old")), Size: 3}), nil
		case "file_list":
			return wire(directfile.Result{Path: ".", Entries: []directfile.Entry{{Name: "same.txt", Type: "file"}}}), nil
		case "file_write":
			args := p.Arguments.(map[string]any)
			content, _ := args["content"].(string)
			out := directfile.Result{Path: args["path"].(string), Size: int64(len(content)), Digest: digest([]byte(content))}
			if args["action"] == ActionDelete {
				out.Digest = "ABSENT"
			}
			if args["action"] == ActionMove {
				out.Path = args["destination"].(string)
				out.Digest = args["expectedDigest"].(string)
			}
			return wire(out), nil
		case "file_implementation_result_submit":
			in := p.Arguments.(directfile.ImplementationResultInput)
			v := resultFixture(r, claim, in)
			recorded = &v
			return wire(copyValue(v)), nil
		}
		return nil, errors.New("unexpected tool")
	}
	return f, claim
}
func TestNewAndBoundedRun(t *testing.T) {
	r := requestFixture()
	changes := []func(*Request){func(r *Request) { r.Claim.WorkflowID = "" }, func(r *Request) { r.Claim.ProtocolVersion = 1 }, func(r *Request) { r.Claim.GrantGeneration = 0 }, func(r *Request) { r.ResultRequestID = " " }, func(r *Request) { r.VerificationMode = "" }}
	for _, change := range changes {
		bad := r
		change(&bad)
		if _, err := New(&fakeMCP{}, bad, complete); err == nil {
			t.Fatal("bad input accepted")
		}
	}
	if _, err := New(nil, r, complete); err == nil {
		t.Fatal("nil MCP")
	}
	if _, err := New(&fakeMCP{}, r, nil); err == nil {
		t.Fatal("nil callback")
	}
	f, _ := happyFake(t, r)
	c, _ := New(f, r, complete)
	out, err := c.Run(context.Background())
	if err == nil || out.State != NotDispatched || len(callNames(f)) != 0 {
		t.Fatal(out, err)
	}
}
func TestPreflightBeforeBusinessCalls(t *testing.T) {
	for _, kind := range []string{"missing-worker", "missing-write-identity", "missing-result-identity", "duplicate", "repeated-cursor"} {
		t.Run(kind, func(t *testing.T) {
			r := requestFixture()
			f, _ := happyFake(t, r)
			switch kind {
			case "missing-worker":
				for i, tool := range f.tools {
					if tool.Name == "file_worker_claim" {
						f.tools = append(f.tools[:i], f.tools[i+1:]...)
						break
					}
				}
			case "missing-write-identity", "missing-result-identity":
				name := "file_write"
				if kind == "missing-result-identity" {
					name = "file_implementation_result_submit"
				}
				for i, tool := range f.tools {
					if tool.Name != name {
						continue
					}
					v := *tool
					raw, _ := json.Marshal(v.InputSchema)
					var schema map[string]any
					_ = json.Unmarshal(raw, &schema)
					if name == "file_write" {
						for _, branch := range schema["oneOf"].([]any) {
							delete(branch.(map[string]any)["properties"].(map[string]any), "workerClaim")
						}
					} else {
						delete(schema["properties"].(map[string]any), "workerClaim")
					}
					v.InputSchema = schema
					f.tools[i] = &v
				}
			case "duplicate":
				f.tools = append(f.tools, f.tools[0])
			case "repeated-cursor":
				f.list = func(*sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
					return &sdkmcp.ListToolsResult{NextCursor: "repeat"}, nil
				}
			}
			c, _ := New(f, r, complete)
			out, err := c.Run(bounded(t))
			if err == nil || out.State != NotDispatched || len(callNames(f)) != 0 {
				t.Fatal(out, err, callNames(f))
			}
		})
	}
}
func TestInvalidClaimNeverExecutes(t *testing.T) {
	for _, kind := range []string{"foreign", "instruction", "definition", "payload", "mode", "replay-payload", "missing-replay", "access"} {
		t.Run(kind, func(t *testing.T) {
			r := requestFixture()
			f, claim := happyFake(t, r)
			switch kind {
			case "foreign":
				claim.Receipt.WorkerID = "other"
			case "instruction":
				claim.ExecutionPayload.Instruction += "changed"
			case "definition":
				claim.Receipt.DefinitionSHA256 = strings.Repeat("d", 64)
			case "payload":
				claim.Receipt.PayloadSHA256 = strings.Repeat("d", 64)
			case "mode":
				r.VerificationMode = task.VerificationModeRun
			case "replay-payload":
				claim.Replay = true
			case "access":
				claim.ExecutionPayload.Access.RepositoryTargets[0].State = "BROKEN"
				claim.Receipt.PayloadSHA256 = digestJSON(claim.ExecutionPayload)
			}
			f.call = func(*sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				if kind == "missing-replay" {
					raw, _ := json.Marshal(claim)
					var v map[string]any
					_ = json.Unmarshal(raw, &v)
					delete(v, "replay")
					return wire(map[string]any{"replay": false, "result": v}), nil
				}
				return wire(claim), nil
			}
			var calls int
			c, _ := New(f, r, func(context.Context, Delivery, Files) (Completion, error) { calls++; return Completion{}, nil })
			out, err := c.Run(bounded(t))
			if err == nil || out.State != ClaimUnknown || calls != 0 {
				t.Fatal(out, err, calls)
			}
		})
	}
}
func TestOneShotConcurrentAndDetached(t *testing.T) {
	r := requestFixture()
	f, _ := happyFake(t, r)
	var callbacks atomic.Int32
	var held Files
	c, _ := New(f, r, func(ctx context.Context, d Delivery, files Files) (Completion, error) {
		callbacks.Add(1)
		held = files
		d.Payload.Access.RepositoryTargets[0].MemberID = "changed"
		_, err := files.Read(ctx, "member", "same.txt")
		if err != nil {
			return Completion{}, err
		}
		return complete(ctx, d, files)
	})
	var wg sync.WaitGroup
	var recorded Result
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := c.Run(bounded(t))
			if err == nil {
				mu.Lock()
				recorded = out
				mu.Unlock()
			} else if !errors.Is(err, ErrAlreadyRun) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if callbacks.Load() != 1 || recorded.State != ResultRecorded {
		t.Fatal(callbacks.Load(), recorded)
	}
	recorded.ClaimReceipt.WorkerID = "changed"
	recorded.ResultInput.Summary = "changed"
	recorded.ResultReceipt.Members[0].EndSnapshot = "changed"
	observed, err := c.Observe(bounded(t))
	if err != nil || observed.State != ResultRecorded {
		t.Fatal(observed, err)
	}
	if _, err = held.Read(bounded(t), "member", "same.txt"); !errors.Is(err, ErrFilesClosed) {
		t.Fatal(err)
	}
}
func TestMutationUnknownCannotBeSwallowed(t *testing.T) {
	for _, kind := range []string{"transport", "nil", "is-error", "foreign-path", "bad-digest"} {
		t.Run(kind, func(t *testing.T) {
			r := requestFixture()
			f, _ := happyFake(t, r)
			base := f.call
			f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				if p.Name != "file_write" {
					return base(p)
				}
				switch kind {
				case "transport":
					return nil, errors.New("response lost")
				case "nil":
					return nil, nil
				case "is-error":
					return &sdkmcp.CallToolResult{IsError: true}, nil
				case "foreign-path":
					return wire(directfile.Result{Path: "other", Digest: digest([]byte("new")), Size: 3}), nil
				}
				return wire(directfile.Result{Path: "same.txt", Digest: digest([]byte("bad")), Size: 3}), nil
			}
			c, _ := New(f, r, func(ctx context.Context, d Delivery, files Files) (Completion, error) {
				_, err := files.Write(ctx, mutation())
				if !errors.Is(err, ErrMutationUnknown) {
					t.Error(err)
				}
				next := mutation()
				next.RequestID = "not-a-retry"
				if _, err = files.Write(ctx, next); !errors.Is(err, ErrMutationUnknown) {
					t.Error(err)
				}
				if _, err = files.Read(ctx, "member", "same.txt"); !errors.Is(err, ErrMutationUnknown) {
					t.Error(err)
				}
				return complete(ctx, d, files)
			})
			out, err := c.Run(bounded(t))
			if !errors.Is(err, ErrMutationUnknown) || out.State != MutationUnknown || len(out.Mutations) != 1 || out.Mutations[0].Response != nil || !reflect.DeepEqual(callNames(f), []string{"file_worker_claim", "file_write"}) {
				t.Fatal(out, err, callNames(f))
			}
			observed, err := c.Observe(bounded(t))
			if err != nil || observed.State != DeliveryUnknown {
				t.Fatal(observed, err)
			}
		})
	}
}
func TestLocalRefusalsAndMutationIDs(t *testing.T) {
	r := requestFixture()
	f, _ := happyFake(t, r)
	c, _ := New(f, r, func(ctx context.Context, d Delivery, files Files) (Completion, error) {
		for _, change := range []func(*Mutation){func(m *Mutation) { m.Path = "../escape" }, func(m *Mutation) { m.Path = "outside" }, func(m *Mutation) { m.MemberID = "foreign" }, func(m *Mutation) { m.ExpectedDigest = "" }, func(m *Mutation) { m.ContentEncoding = "bad" }, func(m *Mutation) { m.Action = ActionMove; m.Content = ""; m.Destination = "outside" }} {
			m := mutation()
			change(&m)
			if _, err := files.Write(ctx, m); err == nil {
				t.Error("bad mutation accepted")
			}
		}
		if _, err := files.Write(ctx, mutation()); err != nil {
			return Completion{}, err
		}
		if _, err := files.Write(ctx, mutation()); !errors.Is(err, ErrMutationRepeated) {
			t.Error(err)
		}
		return complete(ctx, d, files)
	})
	out, err := c.Run(bounded(t))
	if err != nil || out.State != ResultRecorded || len(out.Mutations) != 1 || out.Mutations[0].Response == nil {
		t.Fatal(out, err)
	}
	if !reflect.DeepEqual(callNames(f), []string{"file_worker_claim", "file_write", "file_implementation_result_submit"}) {
		t.Fatal(callNames(f))
	}
}
func TestResultUnknownReadbackAndMutableProjection(t *testing.T) {
	for _, mode := range []task.VerificationMode{task.VerificationModeNone, task.VerificationModeRun} {
		t.Run(string(mode), func(t *testing.T) {
			r := requestFixture()
			r.VerificationMode = mode
			f, _ := happyFake(t, r)
			base := f.call
			f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				out, err := base(p)
				if p.Name == "file_implementation_result_submit" {
					return nil, errors.New("lost committed result")
				}
				return out, err
			}
			c, _ := New(f, r, complete)
			out, err := c.Run(bounded(t))
			if err == nil || out.State != ResultUnknown {
				t.Fatal(out, err)
			}
			out, err = c.Observe(bounded(t))
			if err != nil || out.State != ResultRecorded {
				t.Fatal(out, err)
			}
			f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				v, e := base(p)
				if p.Name == "file_worker_get" {
					got := v.StructuredContent.(directfile.WorkerClaimResult)
					got.ResultReceipt.Accepted = true
					if mode == task.VerificationModeRun {
						got.ResultReceipt.VerificationStatus = "SUCCEEDED"
						got.ResultReceipt.VerificationRunID = "verification-run"
						got.ResultReceipt.VerificationStartError = ""
					}
					return wire(got), e
				}
				return v, e
			}
			out, err = c.Observe(bounded(t))
			if err != nil || out.State != ResultRecorded || !out.ResultReceipt.Accepted {
				t.Fatal(out, err)
			}
			prior := f.call
			f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				v, e := prior(p)
				got := v.StructuredContent.(directfile.WorkerClaimResult)
				got.ResultReceipt.Members[0].EndSnapshot = strings.Repeat("e", 40)
				return wire(got), e
			}
			if _, err = c.Observe(bounded(t)); !errors.Is(err, ErrResponse) {
				t.Fatal(err)
			}
			for _, name := range callNames(f) {
				if name != "file_worker_claim" && name != "file_implementation_result_submit" && name != "file_worker_get" {
					t.Fatal(name)
				}
			}
		})
	}
}
func TestCallbackFailureAndInflightCancellation(t *testing.T) {
	for _, panicCallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicCallback], func(t *testing.T) {
			r := requestFixture()
			f, _ := happyFake(t, r)
			began := make(chan struct{})
			finished := make(chan struct{})
			base := f.call
			f.callContext = func(ctx context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				if p.Name == "file_read" {
					close(began)
					<-ctx.Done()
					close(finished)
					return nil, ctx.Err()
				}
				return base(p)
			}
			c, _ := New(f, r, func(ctx context.Context, d Delivery, files Files) (Completion, error) {
				go func() { _, _ = files.Read(context.Background(), "member", "same.txt") }()
				<-began
				if panicCallback {
					panic("synthetic")
				}
				return Completion{}, errors.New("callback stopped")
			})
			out, err := c.Run(bounded(t))
			if err == nil || out.State != CallbackFailed {
				t.Fatal(out, err)
			}
			select {
			case <-finished:
			default:
				t.Fatal("inflight read was not joined")
			}
			if !reflect.DeepEqual(callNames(f), []string{"file_worker_claim", "file_read"}) {
				t.Fatal(callNames(f))
			}
		})
	}
}

func TestMutationPanicAndInflightCloseStayUnknown(t *testing.T) {
	for _, kind := range []string{"transport-panic", "inflight-close"} {
		t.Run(kind, func(t *testing.T) {
			r := requestFixture()
			f, _ := happyFake(t, r)
			base := f.call
			began := make(chan struct{})
			finished := make(chan struct{})
			f.callContext = func(ctx context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				if p.Name != "file_write" {
					return base(p)
				}
				if kind == "transport-panic" {
					panic("after possible dispatch")
				}
				close(began)
				<-ctx.Done()
				close(finished)
				return nil, ctx.Err()
			}
			c, _ := New(f, r, func(ctx context.Context, d Delivery, files Files) (Completion, error) {
				if kind == "transport-panic" {
					_, _ = files.Write(ctx, mutation())
				} else {
					go func() { _, _ = files.Write(context.Background(), mutation()) }()
					<-began
				}
				return complete(ctx, d, files)
			})
			out, err := c.Run(bounded(t))
			if !errors.Is(err, ErrMutationUnknown) || out.State != MutationUnknown || len(out.Mutations) != 1 || out.Mutations[0].Response != nil {
				t.Fatal(out, err)
			}
			if kind == "inflight-close" {
				select {
				case <-finished:
				default:
					t.Fatal("mutation not canceled/joined")
				}
			}
			if !reflect.DeepEqual(callNames(f), []string{"file_worker_claim", "file_write"}) {
				t.Fatal(callNames(f))
			}
		})
	}
}
func TestMalformedResultIsUnknownNotZeroValueProof(t *testing.T) {
	for _, kind := range []string{"missing-accepted", "missing-members", "foreign-id", "body-change", "bad-worktree"} {
		t.Run(kind, func(t *testing.T) {
			r := requestFixture()
			f, _ := happyFake(t, r)
			base := f.call
			f.call = func(p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
				v, e := base(p)
				if p.Name != "file_implementation_result_submit" {
					return v, e
				}
				got := v.StructuredContent.(directfile.ImplementationResult)
				switch kind {
				case "foreign-id":
					got.RequestID = "other"
				case "body-change":
					got.Summary = "not authored"
				case "bad-worktree":
					got.Members[0].WorktreeID = "other"
				}
				raw, _ := json.Marshal(got)
				var fields map[string]any
				_ = json.Unmarshal(raw, &fields)
				if kind == "missing-accepted" {
					delete(fields, "accepted")
				}
				if kind == "missing-members" {
					delete(fields, "members")
				}
				return wire(fields), nil
			}
			c, _ := New(f, r, complete)
			out, err := c.Run(bounded(t))
			if err == nil || out.State != ResultUnknown || out.ResultReceipt != nil {
				t.Fatal(out, err)
			}
		})
	}
}
func TestObservePinsOriginalWithoutAliasingReturnedReceipt(t *testing.T) {
	r := requestFixture()
	f, claim := happyFake(t, r)
	c, _ := New(f, r, complete)
	first, err := c.Observe(bounded(t))
	if err != nil || first.State != DeliveryUnknown {
		t.Fatal(first, err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
				first.ClaimReceipt.WorkerID = "public mutation"
			}
		}
	}()
	for i := 0; i < 3; i++ {
		out, err := c.Observe(bounded(t))
		if err != nil || out.ClaimReceipt.WorkerID != r.Claim.WorkerID {
			t.Error(out, err)
		}
	}
	close(done)
	wg.Wait()
	claim.Replay = true
	claim.State = directfile.WorkerOutcomeUnknown
	claim.ExecutionPayload = nil
	claim.Receipt.WorksetSHA256 = strings.Repeat("f", 64)
	f.call = func(*sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) { return wire(claim), nil }
	if _, err = c.Observe(bounded(t)); !errors.Is(err, ErrResponse) {
		t.Fatal(err)
	}
}
