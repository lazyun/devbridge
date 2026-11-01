package controlapi_test

// Unlike the CallTool-level response-loss tests, this gate truncates an actual
// loopback TCP response after the real Local MCP handler has completed. Bootstrap
// uses the existing admitted NONE fixture; no native runtime or model is launched.
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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type tcpMutationSnapshot struct {
	Info   os.FileInfo
	Digest [32]byte
}

func snapshotTCPMutation(path string) (tcpMutationSnapshot, error) {
	info, err := os.Stat(path)
	if err != nil {
		return tcpMutationSnapshot{}, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return tcpMutationSnapshot{}, err
	}
	return tcpMutationSnapshot{info, sha256.Sum256(raw)}, nil
}
func sameTCPMutation(a, b tcpMutationSnapshot) bool {
	return os.SameFile(a.Info, b.Info) && a.Info.ModTime().Equal(b.Info.ModTime()) && a.Info.Size() == b.Info.Size() && a.Digest == b.Digest
}

type tcpMutationCapture struct {
	mu               sync.Mutex
	calls            map[string]int
	truncated        bool
	completeResponse []byte
	beforeDisconnect tcpMutationSnapshot
	faultError       error
	sentBytes        int
}

func TestFileWorkerClientRealCoreTCPResponseLoss(t *testing.T) {
	f, current, _, req := fileClientFixture(t, task.VerificationModeNone)
	member := current.RepositoryTargets[0].MemberID
	var root string
	if err := f.plane.DB.SQL().QueryRow(`SELECT canonical_path FROM direct_file_task_members WHERE workflow_id=? AND member_id=?`, req.Claim.WorkflowID, member).Scan(&root); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "same.txt")
	beforeTrees := upperClientTrees(t, f)
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "tcp-loss-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(adapter.Handler())
	t.Cleanup(upstream.Close)
	capture := &tcpMutationCapture{calls: map[string]int{}}
	faultDone := make(chan struct{})
	relayClient := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect forbidden") }}
	defer relayClient.CloseIdleConnections()
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, e := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		r.Body.Close()
		var envelope struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		if e = json.Unmarshal(raw, &envelope); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if envelope.Method == "tools/call" {
			capture.mu.Lock()
			capture.calls[envelope.Params.Name]++
			capture.mu.Unlock()
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
		capture.mu.Lock()
		cut := envelope.Method == "tools/call" && envelope.Params.Name == "file_write" && !capture.truncated
		if cut {
			capture.truncated = true
			capture.completeResponse = append([]byte(nil), full...)
			capture.beforeDisconnect, capture.faultError = snapshotTCPMutation(target)
		}
		capture.mu.Unlock()
		if cut {
			defer close(faultDone)
			// Preserve the JSON media type and full declared length but send only a
			// strict prefix on the actual client TCP connection, then close it. This
			// is neither a fake ReadCloser nor a wrapper around a successful CallTool.
			conn, rw, e := w.(http.Hijacker).Hijack()
			if e != nil {
				capture.mu.Lock()
				capture.faultError = e
				capture.mu.Unlock()
				return
			}
			partial := len(full) / 2
			_, e = fmt.Fprintf(rw, "HTTP/1.1 %d OK\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", response.StatusCode, response.Header.Get("Content-Type"), len(full))
			if e == nil {
				_, e = rw.Write(full[:partial])
			}
			if e == nil {
				e = rw.Flush()
			}
			closeErr = conn.Close()
			capture.mu.Lock()
			capture.sentBytes = partial
			if e != nil {
				capture.faultError = e
			}
			if closeErr != nil {
				capture.faultError = closeErr
			}
			capture.mu.Unlock()
			return
		}
		for k, v := range response.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(full)
	}))
	t.Cleanup(relay.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connect := func(name string) *sdkmcp.ClientSession {
		t.Helper()
		session, e := sdkmcp.NewClient(&sdkmcp.Implementation{Name: name, Version: "test-only"}, nil).Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: relay.URL + localmcp.MCPPath, HTTPClient: &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { session.Close() })
		return session
	}
	session := connect("tcp-loss-original")
	callbacks := 0
	var mutationErr error
	client, err := fileworker.New(session, req, func(callCtx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		callbacks++
		before, e := files.Read(callCtx, member, "same.txt")
		if e != nil {
			return fileworker.Completion{}, e
		}
		_, e = files.Write(callCtx, fileworker.Mutation{RequestID: "tcp-lost-replace", Action: fileworker.ActionReplace, MemberID: member, Path: "same.txt", ExpectedDigest: before.Digest, Content: "real mutation behind truncated TCP response\n"})
		mutationErr = e
		if !errors.Is(e, fileworker.ErrMutationUnknown) {
			t.Errorf("truncated socket did not become mutation unknown: %v", e)
		}
		if _, e = files.Read(callCtx, member, "same.txt"); e == nil {
			t.Error("unknown mutation allowed another read")
		}
		if _, e = files.Write(callCtx, fileworker.Mutation{RequestID: "must-not-dispatch", Action: fileworker.ActionCreate, MemberID: member, Path: "new.txt", Content: "no"}); e == nil {
			t.Error("unknown mutation allowed another write")
		}
		return fileworker.Completion{Summary: "deliberately ignore uncertainty: client must not submit this"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if !errors.Is(err, fileworker.ErrMutationUnknown) || out.State != fileworker.MutationUnknown || callbacks != 1 || out.ResultInput != nil || len(out.Mutations) != 1 || out.Mutations[0].Response != nil {
		t.Fatalf("unsafe lost response result %+v err=%v callbacks=%d", out, err, callbacks)
	}
	if mutationErr == nil || !strings.Contains(mutationErr.Error(), "unexpected EOF") {
		t.Fatalf("missing real truncated-body error: %v", mutationErr)
	}
	// Close/join the failed SDK session before inspecting the completed relay.
	_ = session.Close()
	select {
	case <-faultDone:
	case <-ctx.Done():
		t.Fatal("fault handler did not complete")
	}
	capture.mu.Lock()
	faultErr := capture.faultError
	full := append([]byte(nil), capture.completeResponse...)
	snap := capture.beforeDisconnect
	sent := capture.sentBytes
	counts := map[string]int{}
	for k, v := range capture.calls {
		counts[k] = v
	}
	capture.mu.Unlock()
	if faultErr != nil || sent <= 0 || sent >= len(full) {
		t.Fatalf("fault was not actual truncation: %d/%d %v", sent, len(full), faultErr)
	}
	var completed struct {
		Error  json.RawMessage       `json:"error"`
		Result sdkmcp.CallToolResult `json:"result"`
	}
	if err = json.Unmarshal(full, &completed); err != nil || len(completed.Error) > 0 || completed.Result.IsError {
		t.Fatalf("upstream did not complete successfully: %s %v", full, err)
	}
	capturedJSON, err := json.Marshal(completed.Result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var capturedEnvelope map[string]any
	if err = json.Unmarshal(capturedJSON, &capturedEnvelope); err != nil {
		t.Fatal(err)
	}
	result, ok := capturedEnvelope["result"].(map[string]any)
	wantDigest := sha256.Sum256([]byte("real mutation behind truncated TCP response\n"))
	if !ok || result["path"] != "same.txt" || result["digest"] != fmt.Sprintf("%x", wantDigest) || snap.Digest != wantDigest {
		t.Fatal("upstream receipt or actual managed file lacks exact mutation bytes")
	}
	if counts["file_worker_claim"] != 1 || counts["file_read"] != 1 || counts["file_write"] != 1 || counts["file_implementation_result_submit"] != 0 {
		t.Fatalf("transport or callback resent business work: %v", counts)
	}
	compositionEffects(t, f, 1, 1, 1, 0)
	var receiptState string
	if err = f.plane.DB.SQL().QueryRow(`SELECT state FROM direct_file_operation_receipts WHERE workflow_id=? AND request_id=?`, req.Claim.WorkflowID, "tcp-lost-replace").Scan(&receiptState); err != nil || receiptState != "COMPLETED" {
		t.Fatalf("missing durable completed mutation receipt: %s %v", receiptState, err)
	}
	if _, err = client.Run(ctx); !errors.Is(err, fileworker.ErrAlreadyRun) {
		t.Fatalf("same client reran: %v", err)
	}
	coldSession := connect("tcp-loss-new-client")
	cold, err := fileworker.New(coldSession, req, func(context.Context, fileworker.Delivery, fileworker.Files) (fileworker.Completion, error) {
		t.Error("cold observation executed callback")
		return fileworker.Completion{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ledger := definitionFormalSnapshot(t, f, []string{"direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "confirmation_commit_requests", "task_action_heads"})
	observed, err := cold.Observe(ctx)
	if err != nil || observed.State != fileworker.DeliveryUnknown || observed.ClaimReceipt == nil {
		t.Fatalf("cold observation %+v %v", observed, err)
	}
	capture.mu.Lock()
	if capture.calls["file_write"] != 1 || capture.calls["file_worker_claim"] != 1 || capture.calls["file_worker_get"] != 1 || capture.calls["file_implementation_result_submit"] != 0 {
		t.Errorf("cold observation redispatched: %v", capture.calls)
	}
	capture.mu.Unlock()
	if ledger != definitionFormalSnapshot(t, f, []string{"direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "confirmation_commit_requests", "task_action_heads"}) {
		t.Fatal("observation changed formal ledger")
	}
	afterObserve, err := snapshotTCPMutation(target)
	if err != nil || !sameTCPMutation(snap, afterObserve) {
		t.Fatalf("observation changed inode/mtime/digest: %v", err)
	}
	// One explicitly requested exact replay probes Core's immutable receipt path.
	// It is not automatic recovery by either worker Client, and uses no new ID.
	replay := callUpperMCP(t, ctx, coldSession, "file_write", out.Mutations[0].Input)["result"]
	if !reflect.DeepEqual(replay, capturedEnvelope["result"]) {
		t.Fatal("exact replay changed the original successful result")
	}
	afterReplay, err := snapshotTCPMutation(target)
	if err != nil || !sameTCPMutation(snap, afterReplay) {
		t.Fatalf("replay physically re-applied mutation: %v", err)
	}
	if ledger != definitionFormalSnapshot(t, f, []string{"direct_file_worker_claims", "direct_file_operation_receipts", "direct_file_implementation_results", "confirmation_commit_requests", "task_action_heads"}) {
		t.Fatal("completed replay changed formal ledger")
	}
	capture.mu.Lock()
	writes := capture.calls["file_write"]
	results := capture.calls["file_implementation_result_submit"]
	capture.mu.Unlock()
	if writes != 2 || results != 0 {
		t.Fatalf("unexpected explicit replay traffic writes=%d results=%d", writes, results)
	}
	compositionEffects(t, f, 1, 1, 1, 0)
	for key, now := range upperClientTrees(t, f) {
		old := beforeTrees[key]
		if key != "managed:"+member && now != old || now.HEAD != old.HEAD {
			t.Fatalf("source or HEAD changed: %s", key)
		}
	}
	t.Logf("actual TCP truncation %d/%d bytes; automatic write POSTs=1, explicit exact replay POSTs=1; one durable receipt, stable inode/mtime/digest, no result or acceptance", sent, len(full))
}
