package controlapi_test

// Real caller-owned UPPER callback authority fencing, not host termination or
// native Dot qualification. The observer delegates every SDK call unchanged.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/upperworker"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type liveUpperCall struct {
	Name     string
	Input    json.RawMessage
	Response *sdkmcp.CallToolResult
	Err      error
}
type liveUpperWire struct {
	*sdkmcp.ClientSession
	mu    sync.Mutex
	calls []liveUpperCall
}

func (w *liveUpperWire) CallTool(ctx context.Context, in *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	raw, err := json.Marshal(in.Arguments)
	if err != nil {
		return nil, err
	}
	out, err := w.ClientSession.CallTool(ctx, in)
	w.mu.Lock()
	w.calls = append(w.calls, liveUpperCall{in.Name, raw, out, err})
	w.mu.Unlock()
	return out, err
}
func (w *liveUpperWire) snapshot() []liveUpperCall {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]liveUpperCall(nil), w.calls...)
}

func TestUpperWorkerLiveCallbackRevocationRealHTTP(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "no_revocation_positive"
		if revoke {
			name = "canceled_with_live_successor"
		}
		t.Run(name, func(t *testing.T) { testUpperLiveRevocation(t, revoke) })
	}
}
func testUpperLiveRevocation(t *testing.T, revoke bool) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ctx, session, _ := workerHTTPClient(t, f)
	_, external, _ := workerHTTPClient(t, f)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wire := &liveUpperWire{ClientSession: session}
	request := upperClientRequest(f)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var retained upperworker.Files
	var authored review.ReviewReport
	var lateReadErr error
	callbacks := 0
	client, err := upperworker.New(wire, request, func(callCtx context.Context, d upperworker.Delivery, files upperworker.Files) (review.ReviewReport, error) {
		callbacks++
		retained = files
		authored = upperClientReadReport(t, f, callCtx, d, files)
		close(entered)
		select {
		case <-release:
		case <-callCtx.Done():
			return review.ReviewReport{}, callCtx.Err()
		}
		_, lateReadErr = files.Read(callCtx, f.targets[0].MemberID, "same.txt")
		return authored, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var out upperworker.Result
	var runErr error
	go func() { defer close(done); out, runErr = client.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		unblock()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("callback/Run failed to join")
		}
	})
	select {
	case <-entered:
	case <-done:
		t.Fatalf("callback exited before gate: %+v %v", out, runErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var cancellation any
	var successor *upperLifecycleFixture
	if revoke {
		envelope := callUpperMCP(t, ctx, external, "operation_cancel", map[string]any{"aiSessionId": f.session, "ownerType": "REVIEW", "ownerId": f.review, "runId": f.run, "requestId": "live-cancel", "reason": "explicit revoke while trusted callback remains live", "expectedUpperVersion": mcpObject(t, f.upperVersion())})
		receipt := envelope["result"].(map[string]any)
		cancellation = receipt
		if receipt["state"] != "CANCELED" || receipt["backendDisposition"] != "NO_BACKEND" || receipt["ownerId"] != f.review {
			t.Fatalf("cancel: %+v", receipt)
		}
		prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "live-successor", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: f.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "separately authorized successor", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Explicit successor, never adopt old callback"}}})
		if err != nil {
			t.Fatal(err)
		}
		committed, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "live-commit-successor", ContentHash: prepared.ContentHash, VariantID: "upper"})
		if err != nil || committed.State != "COMPLETED" || len(committed.Steps) == 0 {
			t.Fatalf("successor: %+v %v", committed, err)
		}
		access, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, committed.Steps[0].ReviewID)
		if err != nil {
			t.Fatal(err)
		}
		access, err = f.facade.PrepareUpperReview(f.ctx, access.Target)
		if err != nil || string(access.State) != "ACTIVE" || access.Target.ReviewID == f.review || access.Target.RunID == f.run || access.Target.TaskID != f.task {
			t.Fatalf("prepared successor: %+v %v", access, err)
		}
		current, err := f.plane.Tasks.Get(f.ctx, f.task)
		if err != nil || current.CurrentReviewID != access.Target.ReviewID {
			t.Fatalf("successor is not current Task owner: %+v %v", current, err)
		}
		successorRun, err := f.plane.Runs.Get(f.ctx, access.Target.RunID)
		if err != nil || successorRun.TaskID != f.task || successorRun.ReviewID != access.Target.ReviewID {
			t.Fatalf("successor Run wrong owner: %+v %v", successorRun, err)
		}
		copyFixture := *f
		copyFixture.run, copyFixture.review, copyFixture.access = access.Target.RunID, access.Target.ReviewID, access
		successor = &copyFixture
		member := access.RepositoryTargets[0]
		readEnvelope := callUpperMCP(t, ctx, external, "file_read", map[string]any{"reviewTarget": mcpObject(t, access.Target), "repositoryId": member.RepositoryID, "memberId": member.MemberID, "memberVersion": member.MemberVersion, "path": "same.txt"})
		raw, err := json.Marshal(readEnvelope["result"])
		if err != nil {
			t.Fatal(err)
		}
		var got directfile.Result
		if err = json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		wantContent := map[string]string{f.repos["app"]: "app source\n", f.repos["sdk"]: "sdk source\n"}[member.RepositoryID]
		digest := sha256.Sum256([]byte(wantContent))
		if wantContent == "" || got.RepositoryID != member.RepositoryID || got.MemberID != member.MemberID || got.MemberVersion != member.MemberVersion || got.WorkspaceID == "" || got.WorkspaceID != access.WorkspaceID || got.Path != "same.txt" || got.Content != wantContent || got.Digest != hex.EncodeToString(digest[:]) || got.EvidenceID == "" {
			t.Fatalf("successor read not exact authorized content: %+v access=%+v", got, access)
		}
		t.Logf("successor actual ACTIVE preparation/current Task + exact read: %s", raw)
	}
	tables := []string{"tasks", "reviews", "runs", "review_grants", "upper_review_worker_claims", "review_reports", "review_result_receipts", "review_access_evidence", "feature_area_agent_claims", "confirmation_commit_requests", "task_action_heads", "execution_proposals", "execution_cancellations", "run_repository_bindings", "confirmation_intents"}
	before := definitionFormalSnapshot(t, f, tables)
	trees := upperClientTrees(t, f)
	var successorTrees map[string]upperClientTree
	if successor != nil {
		successorTrees = upperClientTrees(t, successor)
	}
	_, _, runs := upperClientCounts(t, f)
	unblock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if ctx.Err() != nil {
		t.Fatal("callback cancellation was caused by test deadline")
	}
	expected := review.SubmitInput{UpperTarget: request.Claim.UpperTarget, WorkerClaim: &request.Claim.WorkerClaimIdentity, RequestID: request.ReportRequestID, Report: authored}
	if callbacks != 1 || out.ReportInput == nil || !reflect.DeepEqual(*out.ReportInput, expected) || out.ClaimReceipt == nil {
		t.Fatalf("original report/claim changed: %+v", out)
	}
	calls := wire.snapshot()
	if len(calls) != 8 {
		t.Fatalf("unexpected callback dispatches: %+v", calls)
	}
	for _, index := range []int{6, 7} {
		call := calls[index]
		if (index == 6 && call.Name != "file_read") || (index == 7 && call.Name != "review_result_submit") {
			t.Fatalf("wrong late dispatch: %+v", call)
		}
		if index == 6 {
			var actual map[string]any
			if err := json.Unmarshal(call.Input, &actual); err != nil {
				t.Fatal(err)
			}
			target := f.targets[0]
			want := map[string]any{"reviewTarget": mcpObject(t, request.Claim.UpperTarget), "repositoryId": target.RepositoryID, "memberId": target.MemberID, "memberVersion": float64(target.MemberVersion), "path": "same.txt"}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("late read rebound: got=%v want=%v", actual, want)
			}
		} else {
			var actual review.SubmitInput
			if err := json.Unmarshal(call.Input, &actual); err != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatalf("wire report changed original full input: %+v %v", actual, err)
			}
		}
		if revoke {
			if call.Err != nil || call.Response == nil || !call.Response.IsError {
				t.Fatalf("not real Core refusal: %+v", call)
			}
			rawResponse, marshalErr := json.Marshal(call.Response)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var decoded struct {
				StructuredContent struct {
					Code string `json:"code"`
				} `json:"structuredContent"`
			}
			if err := json.Unmarshal(rawResponse, &decoded); err != nil {
				t.Fatal(err)
			}
			if index == 6 {
				if decoded.StructuredContent.Code != "REVIEW_TARGET_STALE" {
					t.Fatalf("wrong file read Core code: %s", rawResponse)
				}
			} else {
				exact := false
				for _, item := range call.Response.Content {
					if text, ok := item.(*sdkmcp.TextContent); ok && strings.HasPrefix(text.Text, "REVIEW_TARGET_STALE:") {
						exact = true
					}
				}
				if !exact {
					t.Fatalf("wrong report Core code: %s", rawResponse)
				}
			}
			t.Logf("late %s actual Core error: input=%s response=%s", call.Name, call.Input, rawResponse)
		}
	}
	if revoke {
		if !errors.Is(lateReadErr, upperworker.ErrResponse) || !errors.Is(runErr, upperworker.ErrResponse) || out.State != upperworker.ReportUnknown || out.ReportReceipt != nil {
			t.Fatalf("unsafe late result: %+v read=%v run=%v", out, lateReadErr, runErr)
		}
	} else {
		if lateReadErr != nil || runErr != nil || out.State != upperworker.ResultRecorded || out.ReportReceipt == nil {
			t.Fatalf("positive: %+v %v %v", out, lateReadErr, runErr)
		}
		raw, _ := json.Marshal(expected)
		hash := sha256.Sum256(raw)
		if out.ReportReceipt.PayloadSHA256 != hex.EncodeToString(hash[:]) || out.ReportReceipt.ReviewID != f.review || out.ReportReceipt.RunID != f.run {
			t.Fatalf("positive receipt: %+v", out.ReportReceipt)
		}
	}
	observed, err := client.Observe(ctx)
	if err != nil || !reflect.DeepEqual(observed.ReportInput, out.ReportInput) || !reflect.DeepEqual(observed.ClaimReceipt, out.ClaimReceipt) {
		t.Fatalf("observation changed original identities: %+v %v", observed, err)
	}
	if revoke {
		if observed.State != upperworker.DeliveryUnknown || observed.ReportReceipt != nil {
			t.Fatalf("observe borrowed successor: %+v", observed)
		}
	} else if observed.State != upperworker.ResultRecorded || !reflect.DeepEqual(observed.ReportReceipt, out.ReportReceipt) {
		t.Fatalf("positive observe: %+v", observed)
	}
	if _, err = retained.Read(ctx, f.targets[0].MemberID, "same.txt"); !errors.Is(err, upperworker.ErrReaderClosed) {
		t.Fatalf("retained reader: %v", err)
	}
	if _, err = client.Run(ctx); !errors.Is(err, upperworker.ErrAlreadyRun) {
		t.Fatalf("rerun: %v", err)
	}
	finalCalls := wire.snapshot()
	if len(finalCalls) != 9 || finalCalls[8].Name != "review_worker_get" || callbacks != 1 {
		t.Fatalf("observation redispatched: %+v", finalCalls)
	}
	if revoke && before != definitionFormalSnapshot(t, f, tables) {
		t.Fatal("stale callback/observation changed canonical state")
	}
	if !reflect.DeepEqual(trees, upperClientTrees(t, f)) {
		t.Fatal("read-only callback changed files/HEAD/status")
	}
	if successor != nil && !reflect.DeepEqual(successorTrees, upperClientTrees(t, successor)) {
		t.Fatal("old callback changed successor files/HEAD/status")
	}
	claims, reports, finalRuns := upperClientCounts(t, f)
	wantReports := 1
	if revoke {
		wantReports = 0
	}
	if claims != 1 || reports != wantReports || finalRuns != runs {
		t.Fatalf("unexpected claim/report/run count %d/%d/%d", claims, reports, finalRuns)
	}
	t.Logf("revoke=%t callback joined; original report retained; cancellation=%+v; exact old claims/reports=%d/%d; no implicit admission or host termination claim", revoke, cancellation, claims, reports)
}
