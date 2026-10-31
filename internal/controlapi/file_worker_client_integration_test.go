package controlapi_test

// Real admitted Core and Local MCP HTTP. Response loss is injected only after
// the server finishes; it is not a process crash. RUN uses an explicitly fake
// protocol adapter and real local commands, never a model or native backend.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func fileClientApprove(t *testing.T, waive bool) (*upperLifecycleFixture, task.Task) {
	t.Helper()
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	targets := workset.CloneTargets(f.targets)
	refs := []review.EvidenceRef{}
	checks := []task.VerificationCheck{}
	for i, target := range targets {
		got, err := f.facade.UpperReviewRead(f.ctx, f.inputFor(target, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: target.RepositoryID, MemberID: target.MemberID, Path: "same.txt", Digest: got.Digest})
		targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt", "new.txt", "moved.txt"}}
		checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
	}
	work := task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "Update exact file members", Constraints: []string{"preserve unrelated bytes"}, CodexInstruction: "Update same.txt in writable members", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
	verification := task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both members", Checks: checks, AcceptanceCriteria: []string{"both pass"}, CodexInstruction: "Run both checks"}
	primary := ""
	if !waive {
		primary = targets[0].MemberID
	}
	_, err := f.facade.SubmitReviewResult(f.ctx, review.SubmitInput{UpperTarget: f.access.Target, RequestID: "direct-proposal", Report: review.ReviewReport{SchemaVersion: 2, RepositoryTargets: targets, PrimaryMemberID: primary, Summary: "Direct File exact members", Disposition: review.DispositionApprovalReady, Coverage: []string{"both members"}, Findings: []review.Finding{}, Limitations: []string{"source only"}, RecommendedNextSteps: []string{"explicit approval"}, EvidenceRefs: refs, WorkSpec: &work, VerificationSpec: &verification}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "prepare-direct-approval", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "direct", Summary: "Approve exact Direct File proposal without starting", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: targets, PrimaryMemberID: primary, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	selection := c.Selection{}
	if waive {
		selection.VerificationWaiver = &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Test user explicitly waives command verification for this isolated file-only fixture"}
	}
	confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "confirm-direct-approval", ContentHash: prepared.ContentHash, VariantID: "direct", Selection: selection})
	if err != nil {
		t.Fatalf("approval: %+v %v", confirmed, err)
	}
	if confirmed.State != "COMPLETED" {
		t.Fatalf("approval %+v", confirmed)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"SELECT count(*) FROM direct_file_task_authorizations", "SELECT count(*) FROM execution_proposals", "SELECT count(*) FROM task_commit_receipts"} {
		var n int
		if err := f.plane.DB.SQL().QueryRow(query).Scan(&n); err != nil || n != 0 {
			t.Fatalf("NO_START effect: %s=%d %v", query, n, err)
		}
	}
	return f, current
}

func fileClientFixture(t *testing.T, mode task.VerificationMode) (*upperLifecycleFixture, task.Task, directfile.State, fileworker.Request) {
	t.Helper()
	f, current := fileClientApprove(t, mode == task.VerificationModeNone)
	if mode == task.VerificationModeRun {
		installProtocolFixturePlatform(t, f)
		root := t.TempDir()
		launcher := filepath.Join(root, "test-only-codex-protocol")
		t.Setenv("DEVBRIDGE_CODING_V1_HELPER", "1")
		t.Setenv("DEVBRIDGE_TEST_FILE_VERIFICATION", "1")
		t.Setenv("DEVBRIDGE_CODING_V1_WIRE_LOG", filepath.Join(root, "wire.jsonl"))
		executable := "'" + strings.ReplaceAll(os.Args[0], "'", "'\"'\"'") + "'"
		if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexec "+executable+" -test.run=^TestMultiRepositoryCodingV1AppServerHelper$\n"), 0700); err != nil {
			t.Fatal(err)
		}
		f.plane.Coordinator.Codex = execution.DefaultCodexFactory{Config: codex.Config{CodexPath: launcher, AgentRuntime: f.plane.Runs.AgentRuntime}, DynamicToolHandler: func(context.Context, codex.DynamicToolCallParams) (codex.DynamicToolCallResponse, error) {
			return codex.DynamicToolCallResponse{Success: false}, nil
		}}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "client-run-start", Purpose: c.Execution, Action: "START_DIRECT_FILE", Execution: &c.ExecutionBody{Action: "START_DIRECT_FILE", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "files", Summary: "Explicit test-only protocol verification", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
		if err != nil {
			t.Fatal(err)
		}
		opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
		if err != nil || len(opts.BackendCandidates) == 0 {
			t.Fatalf("options %+v %v", opts, err)
		}
		backend := opts.BackendCandidates[0]
		runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
		confirmed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "client-run-start-commit", ContentHash: prepared.ContentHash, VariantID: "files", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}})
		if err != nil || confirmed.State != "COMPLETED" {
			t.Fatalf("start %+v %v", confirmed, err)
		}
	} else {
		started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
		if err != nil || started.State != "COMPLETED" {
			t.Fatalf("start %+v %v", started, err)
		}
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || !state.Enabled || state.ProtocolVersion != 2 {
		t.Fatalf("state %+v %v", state, err)
	}
	claim := directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: state.DecisionID, WorkflowID: state.WorkflowID, ProtocolVersion: 2, RuntimeEpoch: state.RuntimeEpoch, GrantGeneration: state.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "client-file-delivery", WorkerID: "trusted-test-callback-not-dot-identity"}}
	return f, current, state, fileworker.Request{Claim: claim, ResultRequestID: "client-file-result", VerificationMode: mode}
}

// Inspect the actual outgoing HTTP arguments, not a mock authority.
type fileClientWire struct {
	*upperClientWire
	t                *testing.T
	request          fileworker.Request
	members          map[string]workset.Target
	stripResultClaim bool
}

func (w *fileClientWire) ListTools(ctx context.Context, p *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
	out, err := w.upperClientWire.ListTools(ctx, p)
	if err != nil || !w.stripResultClaim {
		return out, err
	}
	for i, tool := range out.Tools {
		if tool.Name == "file_implementation_result_submit" {
			clone := *tool
			raw, _ := json.Marshal(tool.InputSchema)
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				return nil, err
			}
			delete(schema["properties"].(map[string]any), "workerClaim")
			clone.InputSchema = schema
			out.Tools[i] = &clone
		}
	}
	return out, nil
}
func (w *fileClientWire) CallTool(ctx context.Context, p *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	if p.Name == "file_write" || p.Name == "file_read" || p.Name == "file_list" {
		raw, err := json.Marshal(p.Arguments)
		if err != nil {
			w.t.Fatal(err)
		}
		var in directfile.Input
		if err = json.Unmarshal(raw, &in); err != nil {
			w.t.Fatal(err)
		}
		member, ok := w.members[in.MemberID]
		claim := w.request.Claim
		if !ok || in.RepositoryID != member.RepositoryID || in.MemberVersion != member.MemberVersion || in.Scope != (directfile.Scope{AISessionID: claim.AISessionID, FeatureAreaID: claim.FeatureAreaID, WorkflowID: claim.WorkflowID}) || in.RuntimeEpoch != claim.RuntimeEpoch || in.GrantGeneration != claim.GrantGeneration || in.WorkflowComplete {
			w.t.Fatalf("file call escaped exact original fences: %+v", in)
		}
		if p.Name == "file_write" && (in.WorkerClaim == nil || *in.WorkerClaim != claim.WorkerClaimIdentity || in.RequestID == "") {
			w.t.Fatalf("write lost exact worker/request fence: %+v", in)
		}
	}
	return w.upperClientWire.CallTool(ctx, p)
}
func fileClientTransport(t *testing.T, s *sdkmcp.ClientSession, req fileworker.Request, current task.Task) *fileClientWire {
	w := &fileClientWire{upperClientWire: &upperClientWire{ClientSession: s}, t: t, request: req, members: map[string]workset.Target{}}
	for _, member := range current.RepositoryTargets {
		w.members[member.MemberID] = member
	}
	return w
}
func fileClientCount(t *testing.T, f *upperLifecycleFixture, table string) int {
	t.Helper()
	var n int
	if err := f.plane.DB.SQL().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func fileClientSubmissionProof(t *testing.T, f *upperLifecycleFixture, req fileworker.Request, input *directfile.ImplementationResultInput) {
	t.Helper()
	if input == nil {
		t.Fatal("missing exact original result input")
	}
	want := directfile.ImplementationResultInput{WorkerClaim: &req.Claim.WorkerClaimIdentity, AISessionID: req.Claim.AISessionID, TaskID: req.Claim.TaskID, TaskDefinitionRevisionID: req.Claim.TaskDefinitionRevisionID, WorkflowID: req.Claim.WorkflowID, RequestID: req.ResultRequestID, Summary: input.Summary, VerificationNotes: input.VerificationNotes, ProtocolVersion: 2, RuntimeEpoch: req.Claim.RuntimeEpoch, GrantGeneration: req.Claim.GrantGeneration}
	if !reflect.DeepEqual(*input, want) {
		t.Fatalf("result input escaped original frozen identity: %+v", input)
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	var stored string
	if err := f.plane.DB.SQL().QueryRow(`SELECT payload_sha256 FROM direct_file_implementation_results WHERE ai_session_id=? AND request_id=?`, req.Claim.AISessionID, req.ResultRequestID).Scan(&stored); err != nil || stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("actual result full-input hash %s %v", stored, err)
	}
}

func fileClientNoAcceptance(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	if n := fileClientCount(t, f, "direct_file_implementation_acceptances"); n != 0 {
		t.Fatalf("unexpected acceptance %d", n)
	}
	owner, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || owner.Status != task.StatusActive {
		t.Fatalf("unexpected owner %+v %v", owner, err)
	}
	var n int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=? AND purpose='IMPLEMENTATION'`, f.task).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fabricated implementation Run %d %v", n, err)
	}
}
func fileClientReplaceBoth(t *testing.T, f *upperLifecycleFixture, current task.Task, ctx context.Context, d fileworker.Delivery, files fileworker.Files) {
	t.Helper()
	if d.Payload.Instruction != current.WorkSpec.CodexInstruction || !reflect.DeepEqual(d.Payload.Definition.WorkSpec, current.WorkSpec) || !reflect.DeepEqual(d.Payload.Verification, current.VerificationSpec) {
		t.Fatalf("altered frozen definition: %+v", d)
	}
	for _, member := range current.RepositoryTargets {
		list, err := files.List(ctx, member.MemberID, ".")
		if err != nil || list.Path != "." {
			t.Fatalf("list %+v %v", list, err)
		}
		before, err := files.Read(ctx, member.MemberID, "same.txt")
		if err != nil || before.Path != "same.txt" || before.Digest == "" {
			t.Fatalf("read %+v %v", before, err)
		}
		name := "sdk"
		if member.RepositoryID == f.repos["app"] {
			name = "app"
		}
		if before.Content != name+" source\n" {
			t.Fatalf("wrong member source bytes: %+v", before)
		}
		after, err := files.Write(ctx, fileworker.Mutation{RequestID: "replace-" + member.MemberID, Action: fileworker.ActionReplace, MemberID: member.MemberID, Path: "same.txt", ExpectedDigest: before.Digest, Content: "coded " + name + "\n"})
		if err != nil {
			t.Fatalf("replace %+v %v", after, err)
		}
		got, err := files.Read(ctx, member.MemberID, "same.txt")
		if err != nil || got.Content != "coded "+name+"\n" || got.Digest == before.Digest {
			t.Fatalf("actual bytes %+v %v", got, err)
		}
	}
}

func TestFileWorkerClientRealCoreHTTP(t *testing.T) {
	f, current, _, req := fileClientFixture(t, task.VerificationModeNone)
	ctx, session, _ := workerHTTPClient(t, f)
	before := upperClientTrees(t, f)
	for _, bad := range []string{"missing-tool", "missing-schema"} {
		wire := fileClientTransport(t, session, req, current)
		if bad == "missing-tool" {
			wire.omit = "file_write"
		} else {
			wire.stripResultClaim = true
		}
		client, err := fileworker.New(wire, req, func(context.Context, fileworker.Delivery, fileworker.Files) (fileworker.Completion, error) {
			t.Fatal("preflight invoked callback")
			return fileworker.Completion{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := client.Run(ctx)
		if err == nil || out.State != fileworker.NotDispatched || len(wire.calls) != 0 || fileClientCount(t, f, "direct_file_worker_claims") != 0 {
			t.Fatalf("preflight reached business call: %+v %v %v", out, err, wire.calls)
		}
	}
	wire := fileClientTransport(t, session, req, current)
	callbacks := 0
	var retained fileworker.Files
	client, err := fileworker.New(wire, req, func(ctx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		retained = files
		// Detached delivery must not retarget a capability.
		original := d.Payload.Access.RepositoryTargets[0].MemberID
		d.Payload.Access.RepositoryTargets[0].MemberID = "foreign"
		d.Receipt.WorkerID = "foreign"
		if _, err := files.Read(ctx, original, "same.txt"); err != nil {
			t.Fatalf("detached delivery changed reader %v", err)
		}
		reads := wire.count("file_read")
		writes := wire.count("file_write")
		if _, err := files.Read(ctx, original, "../same.txt"); err == nil || wire.count("file_read") != reads {
			t.Fatal("traversal dispatched")
		}
		if _, err := files.Write(ctx, fileworker.Mutation{RequestID: "bad-path", Action: fileworker.ActionCreate, MemberID: original, Path: "../escape", Content: "no"}); err == nil || wire.count("file_write") != writes {
			t.Fatal("mutation traversal dispatched")
		}
		fileClientReplaceBoth(t, f, current, ctx, d, files)
		member := current.RepositoryTargets[0].MemberID
		created, err := files.Write(ctx, fileworker.Mutation{RequestID: "create-temp", Action: fileworker.ActionCreate, MemberID: member, Path: "new.txt", Content: "temporary\n"})
		if err != nil {
			t.Fatalf("create %+v %v", created, err)
		}
		read, err := files.Read(ctx, member, "new.txt")
		if err != nil || read.Content != "temporary\n" {
			t.Fatalf("created bytes %+v %v", read, err)
		}
		if _, err = files.Write(ctx, fileworker.Mutation{RequestID: "move-temp", Action: fileworker.ActionMove, MemberID: member, Path: "new.txt", Destination: "moved.txt", ExpectedDigest: read.Digest}); err != nil {
			t.Fatal(err)
		}
		moved, err := files.Read(ctx, member, "moved.txt")
		if err != nil || moved.Content != read.Content {
			t.Fatalf("moved bytes %+v %v", moved, err)
		}
		if _, err = files.Write(ctx, fileworker.Mutation{RequestID: "delete-temp", Action: fileworker.ActionDelete, MemberID: member, Path: "moved.txt", ExpectedDigest: moved.Digest}); err != nil {
			t.Fatal(err)
		}
		return fileworker.Completion{Summary: "Changed both exact members through Core", VerificationNotes: "Original explicit NONE waiver; no command execution claimed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if err != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultInput == nil || callbacks != 1 {
		t.Fatalf("consumer %+v %v calls=%v", out, err, wire.calls)
	}
	fileClientSubmissionProof(t, f, req, out.ResultInput)
	receipt := out.ResultReceipt
	if receipt.Accepted || receipt.VerificationStatus != "NOT_RUN" || receipt.ExecutionID != "" || receipt.VerificationRunID != "" || len(receipt.Members) != 2 || receipt.RequestID != req.ResultRequestID || out.ResultInput.WorkerClaim == nil || *out.ResultInput.WorkerClaim != req.Claim.WorkerClaimIdentity {
		t.Fatalf("invented/lost result facts %+v", out)
	}
	if wire.count("file_write") != 5 || fileClientCount(t, f, "direct_file_operation_receipts") != 5 || fileClientCount(t, f, "direct_file_worker_claims") != 1 || fileClientCount(t, f, "direct_file_implementation_results") != 1 || fileClientCount(t, f, "execution_proposals") != 0 {
		t.Fatalf("wrong effects calls=%v", wire.calls)
	}
	reads := wire.count("file_read")
	if _, err := retained.Read(ctx, current.RepositoryTargets[0].MemberID, "same.txt"); !errors.Is(err, fileworker.ErrFilesClosed) || wire.count("file_read") != reads {
		t.Fatal("retained reader live")
	}
	if _, err := client.Run(ctx); !errors.Is(err, fileworker.ErrAlreadyRun) || callbacks != 1 {
		t.Fatal("reran callback")
	}
	observed, err := client.Observe(ctx)
	if err != nil || observed.State != fileworker.ResultRecorded || !reflect.DeepEqual(observed.ResultReceipt, receipt) {
		t.Fatalf("observe %+v %v", observed, err)
	}
	fileClientNoAcceptance(t, f)
	after := upperClientTrees(t, f)
	for key, old := range before {
		now := after[key]
		if strings.HasPrefix(key, "source:") {
			if now != old {
				t.Fatalf("source changed %s", key)
			}
		} else if now.HEAD != old.HEAD || now.Status != " M same.txt\n" || (now.Content != "coded app\n" && now.Content != "coded sdk\n") {
			t.Fatalf("unexpected managed diff %s: %+v", key, now)
		}
	}
	// Diff is a local test oracle, not a nonexistent public file_diff tool.
	rows, err := f.plane.DB.SQL().Query(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=?`, req.Claim.WorkflowID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("git", "diff", "--name-only")
		cmd.Dir = root
		raw, err := cmd.Output()
		if err != nil || string(raw) != "same.txt\n" {
			t.Fatalf("unexpected diff %s %v", raw, err)
		}
		for _, path := range []string{"new.txt", "moved.txt"} {
			if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
				t.Fatalf("temporary file survived %s %v", path, err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestFileWorkerClientRealCoreResponseLoss(t *testing.T) {
	for _, lostTool := range []string{"file_worker_claim", "file_write", "file_implementation_result_submit"} {
		t.Run(lostTool, func(t *testing.T) {
			f, current, _, req := fileClientFixture(t, task.VerificationModeNone)
			ctx, session, closeHTTP := workerHTTPClient(t, f)
			wire := fileClientTransport(t, session, req, current)
			wire.lose = lostTool
			callbacks := 0
			client, err := fileworker.New(wire, req, func(ctx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
				callbacks++
				if lostTool == "file_write" {
					member := current.RepositoryTargets[0].MemberID
					before, err := files.Read(ctx, member, "same.txt")
					if err != nil {
						t.Fatal(err)
					}
					if _, err = files.Write(ctx, fileworker.Mutation{RequestID: "uncertain-write", Action: fileworker.ActionReplace, MemberID: member, Path: "same.txt", ExpectedDigest: before.Digest, Content: "actual committed bytes behind lost response\n"}); err == nil {
						t.Fatal("loss did not reach callback")
					}
					calls := len(wire.calls)
					if _, err = files.Read(ctx, member, "same.txt"); err == nil {
						t.Fatal("poisoned reader continued")
					}
					if _, err = files.Write(ctx, fileworker.Mutation{RequestID: "must-not-dispatch", Action: fileworker.ActionCreate, MemberID: member, Path: "new.txt", Content: "no"}); err == nil {
						t.Fatal("poisoned writer continued")
					}
					if len(wire.calls) != calls {
						t.Fatal("poisoned capability called Core")
					}
					// Deliberately ignore the uncertainty: the consumer must refuse result.
					return fileworker.Completion{Summary: "Callback incorrectly pretends success"}, nil
				}
				fileClientReplaceBoth(t, f, current, ctx, d, files)
				return fileworker.Completion{Summary: "Both members actually changed", VerificationNotes: "Original NONE waiver"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(ctx)
			want, wantCallbacks, wantResults, wantWrites := fileworker.ClaimUnknown, 0, 0, 0
			if lostTool == "file_write" {
				want, wantCallbacks, wantWrites = fileworker.MutationUnknown, 1, 1
			}
			if lostTool == "file_implementation_result_submit" {
				want, wantCallbacks, wantResults, wantWrites = fileworker.ResultUnknown, 1, 1, 2
			}
			if err == nil || out.State != want || !wire.lost || callbacks != wantCallbacks {
				t.Fatalf("unsafe uncertainty %+v %v calls=%v", out, err, wire.calls)
			}
			if fileClientCount(t, f, "direct_file_worker_claims") != 1 || fileClientCount(t, f, "direct_file_operation_receipts") != wantWrites || fileClientCount(t, f, "direct_file_implementation_results") != wantResults || wire.count("file_write") != wantWrites || wire.count("file_implementation_result_submit") != wantResults {
				t.Fatalf("unexpected effects %v", wire.calls)
			}
			if lostTool == "file_write" {
				var root string
				if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, req.Claim.WorkflowID, current.RepositoryTargets[0].MemberID).Scan(&root); err != nil {
					t.Fatal(err)
				}
				raw, err := os.ReadFile(filepath.Join(root, "same.txt"))
				if err != nil || string(raw) != "actual committed bytes behind lost response\n" {
					t.Fatalf("loss did not conceal real effect %s %v", raw, err)
				}
				if out.ResultInput != nil {
					t.Fatal("swallowed mutation error created result")
				}
			}
			observed, err := client.Observe(ctx)
			wantObserved := fileworker.DeliveryUnknown
			if wantResults == 1 {
				wantObserved = fileworker.ResultRecorded
			}
			if err != nil || observed.State != wantObserved || observed.ClaimReceipt == nil || callbacks != wantCallbacks {
				t.Fatalf("observation %+v %v", observed, err)
			}
			if wantResults == 1 {
				fileClientSubmissionProof(t, f, req, out.ResultInput)
			}
			if wantResults == 1 && (observed.ResultReceipt == nil || observed.ResultReceipt.RequestID != req.ResultRequestID || observed.ResultReceipt.Summary != out.ResultInput.Summary) {
				t.Fatal("observation borrowed different result")
			}
			if _, err := client.Run(ctx); !errors.Is(err, fileworker.ErrAlreadyRun) || wire.count("file_worker_claim") != 1 || wire.count("file_write") != wantWrites || wire.count("file_implementation_result_submit") != wantResults {
				t.Fatal("uncertainty retried business action")
			}
			before := upperClientTrees(t, f)
			oldEpoch := f.plane.DB.RuntimeEpoch()
			closeHTTP()
			reopenDirectDeliveryFixture(t, f)
			if oldEpoch == f.plane.DB.RuntimeEpoch() {
				t.Fatal("Core did not reopen")
			}
			coldCtx, coldSession, _ := workerHTTPClient(t, f)
			coldWire := fileClientTransport(t, coldSession, req, current)
			coldClient, err := fileworker.New(coldWire, req, func(context.Context, fileworker.Delivery, fileworker.Files) (fileworker.Completion, error) {
				t.Fatal("cold observation dispatched callback")
				return fileworker.Completion{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tables := []string{"direct_file_worker_claims", "direct_file_task_authorizations", "direct_file_operation_receipts", "direct_file_implementation_results", "confirmation_commit_requests", "task_action_heads"}
			ledger := definitionFormalSnapshot(t, f, tables)
			cold, err := coldClient.Observe(coldCtx)
			if err != nil || cold.State != observed.State || !reflect.DeepEqual(cold.ClaimReceipt, observed.ClaimReceipt) || !reflect.DeepEqual(cold.ResultReceipt, observed.ResultReceipt) || cold.ResultInput != nil || !reflect.DeepEqual(coldWire.calls, []string{"file_worker_get"}) {
				t.Fatalf("cold observation %+v %v calls=%v", cold, err, coldWire.calls)
			}
			if definitionFormalSnapshot(t, f, tables) != ledger || !reflect.DeepEqual(before, upperClientTrees(t, f)) {
				t.Fatal("cold read refreshed authority or altered files")
			}
			fileClientNoAcceptance(t, f)
		})
	}
}

func TestFileWorkerClientRealCoreVerificationHandoff(t *testing.T) {
	f, current, _, req := fileClientFixture(t, task.VerificationModeRun)
	before := upperClientTrees(t, f)
	ctx, session, _ := workerHTTPClient(t, f)
	wire := fileClientTransport(t, session, req, current)
	callbacks := 0
	client, err := fileworker.New(wire, req, func(ctx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		fileClientReplaceBoth(t, f, current, ctx, d, files)
		return fileworker.Completion{Summary: "Both members changed by bounded file client", VerificationNotes: "Core must run the separately confirmed test-only verification adapter; this callback ran no commands"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if err != nil || out.State != fileworker.ResultRecorded || out.ResultReceipt == nil || out.ResultReceipt.ExecutionID == "" || out.ResultReceipt.VerificationRunID == "" || out.ResultReceipt.VerificationStartError != "" || out.ResultReceipt.Accepted || callbacks != 1 {
		t.Fatalf("handoff %+v %v calls=%v", out, err, wire.calls)
	}
	fileClientSubmissionProof(t, f, req, out.ResultInput)
	receipt := out.ResultReceipt
	deadline := time.Now().Add(25 * time.Second)
	var vr run.Run
	for time.Now().Before(deadline) {
		vr, err = f.facade.GetRunForSession(f.ctx, f.session, receipt.VerificationRunID)
		if err != nil {
			t.Fatal(err)
		}
		if vr.Status == run.StatusSucceeded || vr.Status == run.StatusFailed || vr.Status == run.StatusInfrastructureFailed {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if vr.Status == run.StatusSucceeded && vr.ResultProtocol == run.NaturalResultProtocol && vr.VerificationReport == nil {
		submitNaturalFixtureAnalysis(t, f, vr.ID, nil)
		vr, err = f.facade.GetRunForSession(f.ctx, f.session, vr.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if vr.Status != run.StatusSucceeded || vr.Purpose != run.PurposeVerification || vr.VerificationReport == nil || len(vr.VerificationReport.Checks) != 2 {
		t.Fatalf("actual verification status=%s report=%+v failure=%+v", vr.Status, vr.VerificationReport, vr.Failure)
	}
	seen := map[string]bool{}
	for _, check := range vr.VerificationReport.Checks {
		if check.Observed == nil || check.Observed.ExitCode == nil || *check.Observed.ExitCode != 0 || check.Observed.Command != "test -f same.txt" || check.Observed.Source == nil {
			t.Fatalf("missing actual command observation %+v", check)
		}
		seen[check.Observed.Source.RepositoryID] = true
	}
	if len(seen) != 2 || !seen[f.repos["app"]] || !seen[f.repos["sdk"]] {
		t.Fatalf("verification omitted exact member: %v", seen)
	}
	var verifications int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE execution_proposal_id=? AND purpose='VERIFICATION'`, receipt.ExecutionID).Scan(&verifications); err != nil || verifications != 1 {
		t.Fatalf("verification count %d %v", verifications, err)
	}
	if fileClientCount(t, f, "execution_proposals") != 1 || fileClientCount(t, f, "direct_file_implementation_results") != 1 {
		t.Fatal("duplicate handoff")
	}
	state, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || state.Enabled || state.State != "CLOSED" {
		t.Fatalf("verification did not close file grant %+v %v", state, err)
	}
	observed, err := client.Observe(ctx)
	exact, exactErr := f.facade.GetFileImplementationResult(f.ctx, directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, WorkflowID: req.Claim.WorkflowID})
	if err != nil || exactErr != nil || observed.ResultReceipt == nil || observed.ResultReceipt.ExecutionID != exact.ExecutionID || observed.ResultReceipt.VerificationRunID != exact.VerificationRunID || observed.ResultReceipt.VerificationStatus != exact.VerificationStatus || observed.ResultReceipt.Accepted {
		t.Fatalf("readback hides real verification %+v exact=%+v errors=%v/%v", observed, exact, err, exactErr)
	}
	if wire.count("file_worker_claim") != 1 || wire.count("file_implementation_result_submit") != 1 || callbacks != 1 {
		t.Fatal("observation retriggered handoff")
	}
	fileClientNoAcceptance(t, f)
	after := upperClientTrees(t, f)
	for key, old := range before {
		now := after[key]
		if strings.HasPrefix(key, "source:") && now != old {
			t.Fatalf("verification delivered source %s", key)
		}
		if now.HEAD != old.HEAD {
			t.Fatalf("unexpected commit %s", key)
		}
	}
}
