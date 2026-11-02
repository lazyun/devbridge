package controlapi_test

// This is a caller-owned callback lifecycle test, not a Dot/model/host sandbox
// qualification. Only the HTTP relay is gated: Core, SQLite, SDK and filesystem
// mutations are real, and no business response or storage row is manufactured.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type earlyReturnEnvelope struct {
	Method string `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

type earlyReturnHTTPProof struct {
	request earlyReturnEnvelope
	status  int
	body    []byte
}

func TestFileWorkerClientRealCoreEarlyCallbackReturn(t *testing.T) {
	for _, early := range []bool{true, false} {
		name := "await_response_positive"
		if early {
			name = "return_before_response"
		}
		t.Run(name, func(t *testing.T) { testFileWorkerEarlyReturn(t, early) })
	}
}

func testFileWorkerEarlyReturn(t *testing.T, early bool) {
	t.Helper()
	f, current, _, req := fileClientFixture(t, task.VerificationModeNone)
	member := current.RepositoryTargets[0]
	var root string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, req.Claim.WorkflowID, member.MemberID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "same.txt")
	beforeTrees := upperClientTrees(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	responseRelease, callbackRelease := make(chan struct{}), make(chan struct{})
	var responseOnce, callbackOnce sync.Once
	releaseResponse := func() { responseOnce.Do(func() { close(responseRelease) }) }
	releaseCallback := func() { callbackOnce.Do(func() { close(callbackRelease) }) }
	// Also release on fatal assertion so cleanup cannot strand a relay/callback.
	defer releaseResponse()
	defer releaseCallback()
	wait := func(ch <-chan struct{}, what string) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", what, ctx.Err())
		}
	}
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "early-return-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	upstreamDone := make(chan struct{})
	var upstreamOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		r.Body.Close()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var envelope earlyReturnEnvelope
		_ = json.Unmarshal(raw, &envelope)
		if envelope.Method == "tools/call" && envelope.Params.Name == "file_write" {
			defer upstreamOnce.Do(func() { close(upstreamDone) })
		}
		adapter.Handler().ServeHTTP(w, r)
	}))
	t.Cleanup(upstream.Close)
	relayClient := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}
	defer relayClient.CloseIdleConnections()
	captured := make(chan earlyReturnHTTPProof, 1)
	relayDone := make(chan struct{})
	relayStarted := make(chan struct{})
	var mu sync.Mutex
	calls := map[string]int{}
	forwardedBytes := 0
	var gateError error
	canceled := false
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		r.Body.Close()
		var envelope earlyReturnEnvelope
		if e = json.Unmarshal(raw, &envelope); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		gated := envelope.Method == "tools/call" && envelope.Params.Name == "file_write"
		if envelope.Method == "tools/call" {
			mu.Lock()
			calls[envelope.Params.Name]++
			n := calls[envelope.Params.Name]
			mu.Unlock()
			if gated && n != 1 {
				http.Error(w, "unexpected repeat write", 500)
				return
			}
		}
		if gated {
			close(relayStarted)
			defer close(relayDone)
		}
		forwarded, e := http.NewRequestWithContext(r.Context(), r.Method, upstream.URL+r.URL.RequestURI(), bytes.NewReader(raw))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		forwarded.Header = r.Header.Clone()
		response, e := relayClient.Do(forwarded)
		if e != nil {
			http.Error(w, e.Error(), 502)
			return
		}
		full, e := io.ReadAll(io.LimitReader(response.Body, 8<<20))
		closeErr := response.Body.Close()
		if e != nil || closeErr != nil {
			http.Error(w, fmt.Sprint(e, closeErr), 502)
			return
		}
		if gated {
			captured <- earlyReturnHTTPProof{envelope, response.StatusCode, full}
			// No header or body byte has reached the SDK when this gate is signaled.
			select {
			case <-responseRelease:
			case <-r.Context().Done():
				mu.Lock()
				canceled = true
				gateError = r.Context().Err()
				mu.Unlock()
				return
			}
		}
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(response.StatusCode)
		n, e := w.Write(full)
		if gated {
			mu.Lock()
			forwardedBytes = n
			gateError = e
			mu.Unlock()
		}
	}))
	t.Cleanup(relay.Close)
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	session, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "early-return-real-sdk", Version: "test-only"}, nil).Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: relay.URL + localmcp.MCPPath, HTTPClient: &http.Client{Timeout: 30 * time.Second, Transport: transport}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	const content = "real committed mutation before callback returns\n"
	const mutationID = "early-callback-mutation"
	var retained fileworker.Files
	callbacks := 0
	writeDone := make(chan struct{})
	writeStarted := make(chan struct{})
	var writeResult directfile.Result
	var writeErr error
	client, err := fileworker.New(session, req, func(callCtx context.Context, _ fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		retained = files
		before, e := files.Read(callCtx, member.MemberID, "same.txt")
		if e != nil {
			return fileworker.Completion{}, e
		}
		close(writeStarted)
		go func() {
			defer close(writeDone)
			writeResult, writeErr = files.Write(callCtx, fileworker.Mutation{RequestID: mutationID, Action: fileworker.ActionReplace, MemberID: member.MemberID, Path: "same.txt", ExpectedDigest: before.Digest, Content: content})
		}()
		select {
		case <-callbackRelease:
		case <-callCtx.Done():
			return fileworker.Completion{}, callCtx.Err()
		}
		if !early {
			select {
			case <-writeDone:
			case <-callCtx.Done():
				return fileworker.Completion{}, callCtx.Err()
			}
			if writeErr != nil {
				return fileworker.Completion{}, writeErr
			}
		}
		return fileworker.Completion{Summary: "Callback reports completion", VerificationNotes: "Explicit NONE waiver; no command or acceptance claimed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	runDone := make(chan struct{})
	var out fileworker.Result
	var runErr error
	go func() { defer close(runDone); out, runErr = client.Run(ctx) }()
	// Fatal proof failures must not leave callback-owned work running while the
	// fixture closes SQLite. Join only goroutines/handlers that actually started.
	t.Cleanup(func() {
		cancel()
		releaseCallback()
		releaseResponse()
		join := func(ch <-chan struct{}, what string) {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-ch:
			case <-timer.C:
				t.Errorf("cleanup did not join %s", what)
			}
		}
		join(runDone, "consumer Run")
		select {
		case <-writeStarted:
			join(writeDone, "callback-owned Write")
		default:
		}
		select {
		case <-relayStarted:
			join(relayDone, "mutation relay")
		default:
		}
	})
	// Every proof below precedes permission for the callback to return.
	var proof earlyReturnHTTPProof
	select {
	case proof = <-captured:
	case <-runDone:
		t.Fatalf("consumer exited before response gate: %+v %v", out, runErr)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	wait(upstreamDone, "actual Core HTTP handler exit")
	if proof.status != http.StatusOK {
		t.Fatalf("upstream status=%d body=%s", proof.status, proof.body)
	}
	var response struct {
		Error  json.RawMessage       `json:"error"`
		Result sdkmcp.CallToolResult `json:"result"`
	}
	if err = json.Unmarshal(proof.body, &response); err != nil || len(response.Error) > 0 || response.Result.IsError {
		t.Fatalf("not a successful real response: %s %v", proof.body, err)
	}
	resultRaw, err := json.Marshal(response.Result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Result directfile.Result `json:"result"`
	}
	if err = json.Unmarshal(resultRaw, &envelope); err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	if envelope.Result.Path != "same.txt" || envelope.Result.Digest != wantDigest || envelope.Result.Size != int64(len(content)) {
		t.Fatalf("wrong real response: %+v", envelope.Result)
	}
	snap, err := snapshotTCPMutation(target)
	if err != nil || fmt.Sprintf("%x", snap.Digest) != wantDigest {
		t.Fatalf("physical mutation not present before callback return: %+v %v", snap, err)
	}
	var state, storedResult string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state,result_json FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=? AND repository_id=? AND member_id=?`, req.Claim.WorkflowID, mutationID, member.RepositoryID, member.MemberID).Scan(&state, &storedResult); err != nil || state != "COMPLETED" {
		t.Fatalf("durable receipt %s %v", state, err)
	}
	var stored directfile.Result
	if err = json.Unmarshal([]byte(storedResult), &stored); err != nil || !reflect.DeepEqual(stored, envelope.Result) {
		t.Fatalf("canonical receipt differs: %+v %+v %v", stored, envelope.Result, err)
	}
	mutationLedger := definitionFormalSnapshot(t, f, []string{"direct_file_operation_receipts"})
	allLedger := definitionFormalSnapshot(t, f, []string{"direct_file_worker_claims", "direct_file_implementation_results", "direct_file_task_authorizations", "confirmation_commit_requests", "task_action_heads"})
	compositionEffects(t, f, 1, 1, 1, 0)
	select {
	case <-writeDone:
		t.Fatal("Write completed while response remains withheld")
	default:
	}
	select {
	case <-runDone:
		t.Fatal("Run completed before callback release")
	default:
	}
	if !early {
		releaseResponse()
	}
	releaseCallback()
	wait(runDone, "consumer Run")
	wait(writeDone, "callback-owned Write goroutine")
	wait(relayDone, "gated HTTP relay handler")
	if ctx.Err() != nil {
		t.Fatalf("parent timeout caused cancellation, not callback closure: %v", ctx.Err())
	}
	if callbacks != 1 || len(out.Mutations) != 1 || !reflect.DeepEqual(out.Mutations[0].Input, proof.request.Params.Arguments) {
		t.Fatalf("lost exact dispatched identity: %+v callbacks=%d wire=%+v", out, callbacks, proof.request)
	}
	mu.Lock()
	gotCanceled, gateErr, sent := canceled, gateError, forwardedBytes
	mu.Unlock()
	wantResults := 1
	if early {
		wantResults = 0
		if runErr != fileworker.ErrMutationUnknown || out.State != fileworker.MutationUnknown || out.ResultInput != nil || out.ResultReceipt != nil || out.Mutations[0].Response != nil || !errors.Is(writeErr, fileworker.ErrMutationUnknown) || !errors.Is(writeErr, context.Canceled) {
			t.Fatalf("unsafe early completion: %+v run=%v write=%v", out, runErr, writeErr)
		}
		if !gotCanceled || !errors.Is(gateErr, context.Canceled) || sent != 0 {
			t.Fatalf("not a canceled actual in-flight HTTP reply: canceled=%t err=%v bytes=%d", gotCanceled, gateErr, sent)
		}
		if allLedger != definitionFormalSnapshot(t, f, []string{"direct_file_worker_claims", "direct_file_implementation_results", "direct_file_task_authorizations", "confirmation_commit_requests", "task_action_heads"}) {
			t.Fatal("early callback completion advanced formal state")
		}
	} else {
		if runErr != nil || out.State != fileworker.ResultRecorded || writeErr != nil || out.Mutations[0].Response == nil || !reflect.DeepEqual(writeResult, envelope.Result) || !reflect.DeepEqual(*out.Mutations[0].Response, envelope.Result) {
			t.Fatalf("positive control: %+v run=%v write=%v", out, runErr, writeErr)
		}
		if gotCanceled || gateErr != nil || sent != len(proof.body) {
			t.Fatalf("positive reply not fully delivered: canceled=%t err=%v bytes=%d/%d", gotCanceled, gateErr, sent, len(proof.body))
		}
		fileClientSubmissionProof(t, f, req, out.ResultInput)
		if out.ResultReceipt == nil || out.ResultReceipt.Accepted || out.ResultReceipt.VerificationStatus != "NOT_RUN" || out.ResultReceipt.ExecutionID != "" || out.ResultReceipt.VerificationRunID != "" {
			t.Fatalf("positive result exceeded explicit NONE admission: %+v", out.ResultReceipt)
		}
	}
	// Closure is local capability invalidation, not revocation of the Core grant.
	wantClosed := fileworker.ErrFilesClosed
	if early {
		wantClosed = fileworker.ErrMutationUnknown
	} // uncertainty takes priority
	for _, invoke := range []func() error{
		func() error { _, e := retained.Read(ctx, member.MemberID, "same.txt"); return e },
		func() error { _, e := retained.List(ctx, member.MemberID, "."); return e },
		func() error {
			_, e := retained.Write(ctx, fileworker.Mutation{RequestID: "must-not-dispatch", Action: fileworker.ActionCreate, MemberID: member.MemberID, Path: "new.txt", Content: "no"})
			return e
		},
	} {
		if e := invoke(); !errors.Is(e, wantClosed) {
			t.Fatalf("retained capability: %v want %v", e, wantClosed)
		}
	}
	if _, e := client.Run(ctx); !errors.Is(e, fileworker.ErrAlreadyRun) {
		t.Fatalf("consumer reran: %v", e)
	}
	// Close network endpoints after the actual write/relay have separately joined.
	// This is not proof that arbitrary callback-created work or a Dot process stops.
	session.Close()
	relay.Close()
	upstream.Close()
	mu.Lock()
	counts := map[string]int{}
	for k, v := range calls {
		counts[k] = v
	}
	mu.Unlock()
	wantCalls := map[string]int{"file_worker_claim": 1, "file_read": 1, "file_write": 1}
	if wantResults != 0 {
		wantCalls["file_implementation_result_submit"] = wantResults
	}
	if !reflect.DeepEqual(counts, wantCalls) {
		t.Fatalf("unexpected HTTP business dispatches: got=%v want=%v", counts, wantCalls)
	}
	compositionEffects(t, f, 1, 1, 1, wantResults)
	if mutationLedger != definitionFormalSnapshot(t, f, []string{"direct_file_operation_receipts"}) {
		t.Fatal("completed mutation receipt changed")
	}
	after, err := snapshotTCPMutation(target)
	if err != nil || !sameTCPMutation(snap, after) {
		t.Fatalf("committed effect changed after callback closure: %v", err)
	}
	for key, now := range upperClientTrees(t, f) {
		before := beforeTrees[key]
		if (key != "managed:"+member.MemberID && now != before) || now.HEAD != before.HEAD {
			t.Fatalf("source, other member, or HEAD changed: %s", key)
		}
	}
	t.Logf("early=%t: genuine completed mutation+SQLite receipt before callback return; write/relay joined; reply bytes=%d/%d, Core requests claim/read/write/result=1/1/1/%d; no acceptance/verification", early, sent, len(proof.body), wantResults)
}
