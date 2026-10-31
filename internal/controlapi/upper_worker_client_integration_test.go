package controlapi_test

// These tests run the reusable consumer against the actual Local MCP HTTP
// adapter and genuinely admitted UPPER Review. Only the client response after
// a completed server call is lost; no Core outcome or business row is seeded.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/upperworker"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var upperClientResponseLost = errors.New("test-only: actual Core response lost after handler completion")

type upperClientWire struct {
	*sdkmcp.ClientSession
	mu           sync.Mutex
	calls        []string
	lose         string
	lost         bool
	omit         string
	stripClaim   bool
	lostResponse *sdkmcp.CallToolResult
}

func (w *upperClientWire) CallTool(ctx context.Context, in *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	w.mu.Lock()
	w.calls = append(w.calls, in.Name)
	w.mu.Unlock()
	out, err := w.ClientSession.CallTool(ctx, in)
	w.mu.Lock()
	defer w.mu.Unlock()
	if in.Name == w.lose && !w.lost && err == nil && out != nil && !out.IsError {
		w.lost, w.lostResponse = true, out
		return nil, upperClientResponseLost
	}
	return out, err
}

func (w *upperClientWire) ListTools(ctx context.Context, in *sdkmcp.ListToolsParams) (*sdkmcp.ListToolsResult, error) {
	out, err := w.ClientSession.ListTools(ctx, in)
	if err != nil || out == nil {
		return out, err
	}
	copy := *out
	copy.Tools = nil
	for _, tool := range out.Tools {
		if tool.Name == w.omit {
			continue
		}
		cloned := *tool
		if w.stripClaim && tool.Name == "review_result_submit" {
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return nil, err
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				return nil, err
			}
			props, ok := schema["properties"].(map[string]any)
			if !ok {
				return nil, errors.New("test expected actual report input object")
			}
			delete(props, "workerClaim")
			cloned.InputSchema = schema
		}
		copy.Tools = append(copy.Tools, &cloned)
	}
	return &copy, nil
}

func (w *upperClientWire) count(name string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for _, call := range w.calls {
		if call == name {
			count++
		}
	}
	return count
}

func upperClientRequest(f *upperLifecycleFixture) upperworker.Request {
	return upperworker.Request{Claim: review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "client-original-delivery", WorkerID: "trusted-test-callback-not-dot-identity"}}, ReportRequestID: "client-original-report"}
}

func upperClientCounts(t *testing.T, f *upperLifecycleFixture) (claims, reports, runs int) {
	t.Helper()
	for _, q := range []struct {
		sql  string
		args []any
		dest *int
	}{
		{"SELECT count(*) FROM upper_review_worker_claims WHERE run_id=?", []any{f.run}, &claims},
		{"SELECT count(*) FROM review_reports WHERE review_id=?", []any{f.review}, &reports},
		{"SELECT count(*) FROM runs WHERE task_id=?", []any{f.task}, &runs},
	} {
		if err := f.plane.DB.SQL().QueryRowContext(f.ctx, q.sql, q.args...).Scan(q.dest); err != nil {
			t.Fatal(err)
		}
	}
	var status string
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT status FROM tasks WHERE id=?", f.task).Scan(&status); err != nil || status != "ACTIVE" {
		t.Fatalf("Review consumer changed user acceptance: status=%s err=%v", status, err)
	}
	return
}

type upperClientTree struct{ Content, HEAD, Status string }

func upperClientTrees(t *testing.T, f *upperLifecycleFixture) map[string]upperClientTree {
	t.Helper()
	roots := map[string]string{}
	repositories, err := f.plane.Repositories.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range repositories {
		roots["source:"+repository.ID] = repository.Path
	}
	rows, err := f.plane.DB.SQL().QueryContext(f.ctx, `SELECT b.member_id,w.path FROM run_repository_bindings b JOIN worktrees w ON w.id=b.worktree_id WHERE b.run_id=? ORDER BY b.member_id`, f.run)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var member, root string
		if err := rows.Scan(&member, &root); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		roots["managed:"+member] = root
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	if len(roots) != 5 {
		t.Fatalf("expected three source and two managed roots, got %v", roots)
	}
	out := map[string]upperClientTree{}
	for id, root := range roots {
		raw, err := os.ReadFile(filepath.Join(root, "same.txt"))
		if err != nil {
			t.Fatal(err)
		}
		git := func(args ...string) string {
			command := exec.Command("git", args...)
			command.Dir = root
			raw, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("read-only git %v: %s: %v", args, raw, err)
			}
			return string(raw)
		}
		out[id] = upperClientTree{string(raw), git("rev-parse", "HEAD"), git("status", "--porcelain", "--untracked-files=all")}
	}
	return out
}

func upperClientReadReport(t *testing.T, f *upperLifecycleFixture, ctx context.Context, d upperworker.Delivery, files upperworker.Files) review.ReviewReport {
	t.Helper()
	if d.Receipt.UpperTarget != f.access.Target || d.Payload.Access.Target != f.access.Target || len(d.Payload.Access.RepositoryTargets) != 2 {
		t.Fatalf("delivery lost original authority: %+v", d)
	}
	var instruction string
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT codex_instruction FROM runs WHERE id=?", f.run).Scan(&instruction); err != nil {
		t.Fatal(err)
	}
	if d.Payload.Instruction != instruction {
		t.Fatal("consumer rewrote frozen instruction")
	}
	want := map[string]string{f.repos["app"]: "app source\n", f.repos["sdk"]: "sdk source\n"}
	report := f.findingsOnly("unused-test-helper-request", f.access.Target).Report
	for _, target := range f.targets {
		listed, err := files.List(ctx, target.MemberID, ".")
		if err != nil || listed.RepositoryID != target.RepositoryID || listed.MemberID != target.MemberID {
			t.Fatalf("bound list: %+v err=%v", listed, err)
		}
		got, err := files.Read(ctx, target.MemberID, "same.txt")
		if err != nil || got.Content != want[target.RepositoryID] || got.EvidenceID == "" || got.RepositoryID != target.RepositoryID || got.MemberID != target.MemberID || got.MemberVersion != target.MemberVersion {
			t.Fatalf("bound actual source read: %+v err=%v", got, err)
		}
		sum := sha256.Sum256([]byte(got.Content))
		if got.Digest != hex.EncodeToString(sum[:]) {
			t.Fatal("file evidence digest does not match actual bytes")
		}
		report.EvidenceRefs = append(report.EvidenceRefs, review.EvidenceRef{EvidenceID: got.EvidenceID, RepositoryID: got.RepositoryID, MemberID: got.MemberID, Path: got.Path, Digest: got.Digest})
	}
	return report
}

func TestUpperWorkerClientRealCoreHTTP(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ctx, session, _ := workerHTTPClient(t, f)
	request := upperClientRequest(f)
	before := upperClientTrees(t, f)
	_, _, beforeRuns := upperClientCounts(t, f)
	for _, bad := range []*upperClientWire{{ClientSession: session, omit: "review_worker_claim"}, {ClientSession: session, stripClaim: true}} {
		client, err := upperworker.New(bad, request, func(context.Context, upperworker.Delivery, upperworker.Files) (review.ReviewReport, error) {
			t.Fatal("incompatible connector invoked reviewer")
			return review.ReviewReport{}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		out, err := client.Run(ctx)
		if err == nil || out.State != upperworker.NotDispatched || bad.count("review_worker_claim") != 0 || len(bad.calls) != 0 {
			t.Fatalf("preflight reached Core business entry: %+v err=%v calls=%v", out, err, bad.calls)
		}
		if claims, reports, runs := upperClientCounts(t, f); claims != 0 || reports != 0 || runs != beforeRuns {
			t.Fatalf("preflight mutated real Core: %d/%d/%d", claims, reports, runs)
		}
	}
	wire := &upperClientWire{ClientSession: session}
	callbacks := 0
	var retained upperworker.Files
	var authored review.ReviewReport
	client, err := upperworker.New(wire, request, func(ctx context.Context, d upperworker.Delivery, files upperworker.Files) (review.ReviewReport, error) {
		callbacks++
		retained = files
		authored = upperClientReadReport(t, f, ctx, d, files)
		// Detached callback data cannot alter the bound reader or submission.
		d.Receipt.WorkerID = "foreign"
		d.Payload.Access.Target.ReviewID = "foreign"
		d.Payload.Access.RepositoryTargets[0].MemberID = "foreign"
		if _, err := files.Read(ctx, f.targets[0].MemberID, "same.txt"); err != nil {
			t.Fatalf("callback mutated internal reader identity: %v", err)
		}
		return authored, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if err != nil || out.State != upperworker.ResultRecorded || out.ClaimReceipt == nil || out.ReportInput == nil || out.ReportReceipt == nil || callbacks != 1 {
		t.Fatalf("real consumer: %+v err=%v callbacks=%d admittedAccess=%+v", out, err, callbacks, f.access)
	}
	expected := review.SubmitInput{UpperTarget: request.Claim.UpperTarget, WorkerClaim: &request.Claim.WorkerClaimIdentity, RequestID: request.ReportRequestID, Report: authored}
	if !reflect.DeepEqual(*out.ReportInput, expected) {
		t.Fatalf("submission changed authored report or frozen identity: %+v", out.ReportInput)
	}
	raw, _ := json.Marshal(expected)
	sum := sha256.Sum256(raw)
	if out.ReportReceipt.PayloadSHA256 != hex.EncodeToString(sum[:]) || out.ReportReceipt.RequestID != request.ReportRequestID || out.ReportReceipt.RunID != f.run || out.ReportReceipt.ReviewID != f.review {
		t.Fatalf("wrong complete-input report receipt: %+v", out.ReportReceipt)
	}
	reads := wire.count("file_read")
	if _, err := retained.Read(ctx, f.targets[0].MemberID, "same.txt"); err == nil || wire.count("file_read") != reads {
		t.Fatal("retained reader remained usable after callback")
	}
	if _, err := client.Run(ctx); err == nil || callbacks != 1 || wire.count("review_worker_claim") != 1 || wire.count("review_result_submit") != 1 {
		t.Fatal("repeated Run replayed the callback or business command")
	}
	observed, err := client.Observe(ctx)
	if err != nil || observed.State != upperworker.ResultRecorded || !reflect.DeepEqual(observed.ReportReceipt, out.ReportReceipt) || callbacks != 1 || wire.count("review_worker_claim") != 1 || wire.count("review_result_submit") != 1 {
		t.Fatalf("pure observation changed completion: %+v err=%v", observed, err)
	}
	if claims, reports, runs := upperClientCounts(t, f); claims != 1 || reports != 1 || runs != beforeRuns {
		t.Fatalf("consumer created duplicate or successor business work: %d/%d/%d", claims, reports, runs)
	}
	if !reflect.DeepEqual(upperClientTrees(t, f), before) {
		t.Fatal("read-only worker altered source/managed bytes, HEADs or status")
	}
}

func TestUpperWorkerClientRealCoreResponseLoss(t *testing.T) {
	for _, lostTool := range []string{"review_worker_claim", "review_result_submit"} {
		t.Run(lostTool, func(t *testing.T) {
			f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
			ctx, session, closeHTTP := workerHTTPClient(t, f)
			wire := &upperClientWire{ClientSession: session, lose: lostTool}
			before := upperClientTrees(t, f)
			_, _, beforeRuns := upperClientCounts(t, f)
			callbacks := 0
			client, err := upperworker.New(wire, upperClientRequest(f), func(ctx context.Context, d upperworker.Delivery, files upperworker.Files) (review.ReviewReport, error) {
				callbacks++
				return upperClientReadReport(t, f, ctx, d, files), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			out, err := client.Run(ctx)
			wantState, wantCallbacks, wantReports := upperworker.ClaimUnknown, 0, 0
			if lostTool == "review_result_submit" {
				wantState, wantCallbacks, wantReports = upperworker.ReportUnknown, 1, 1
			}
			if err == nil || !strings.Contains(err.Error(), upperClientResponseLost.Error()) || out.State != wantState || !wire.lost || wire.lostResponse == nil || callbacks != wantCallbacks {
				t.Fatalf("uncertain response classified as safe: %+v err=%v callbacks=%d", out, err, callbacks)
			}
			if claims, reports, runs := upperClientCounts(t, f); claims != 1 || reports != wantReports || runs != beforeRuns {
				t.Fatalf("actual commit lost behind transport error: %d/%d/%d", claims, reports, runs)
			}
			observed, err := client.Observe(ctx)
			wantObserved := upperworker.DeliveryUnknown
			if wantReports == 1 {
				wantObserved = upperworker.ResultRecorded
			}
			if err != nil || observed.State != wantObserved || observed.ClaimReceipt == nil || callbacks != wantCallbacks || wire.count("review_worker_claim") != 1 || wire.count("review_result_submit") != wantReports {
				t.Fatalf("observation replayed original work: %+v err=%v calls=%v", observed, err, wire.calls)
			}
			if wantReports == 1 {
				if out.ReportInput == nil || observed.ReportReceipt == nil {
					t.Fatal("lost report response discarded exact original submission")
				}
				raw, _ := json.Marshal(out.ReportInput)
				sum := sha256.Sum256(raw)
				if observed.ReportReceipt.PayloadSHA256 != hex.EncodeToString(sum[:]) {
					t.Fatal("readback borrowed a different report payload")
				}
			}
			if _, err := client.Run(ctx); err == nil || callbacks != wantCallbacks || wire.count("review_worker_claim") != 1 || wire.count("review_result_submit") != wantReports {
				t.Fatal("uncertain Run became a safe retry")
			}
			if !reflect.DeepEqual(upperClientTrees(t, f), before) {
				t.Fatal("response loss altered source/managed files")
			}
			// A new consumer can only observe the same original identity after a
			// real Core reopen. It cannot recover/re-authorize the old worker.
			oldEpoch := f.plane.DB.RuntimeEpoch()
			closeHTTP()
			reopenDirectDeliveryFixture(t, f)
			if f.plane.DB.RuntimeEpoch() == oldEpoch {
				t.Fatal("cold readback reused the old Core epoch")
			}
			coldCtx, coldSession, _ := workerHTTPClient(t, f)
			coldWire := &upperClientWire{ClientSession: coldSession}
			coldClient, err := upperworker.New(coldWire, upperClientRequest(f), func(context.Context, upperworker.Delivery, upperworker.Files) (review.ReviewReport, error) {
				t.Fatal("historical Observe invoked a new callback")
				return review.ReviewReport{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tables := []string{"upper_review_worker_claims", "review_reports", "review_grants", "confirmation_commit_requests", "task_action_heads"}
			ledger := definitionFormalSnapshot(t, f, tables)
			cold, err := coldClient.Observe(coldCtx)
			if err != nil || cold.State != observed.State || !reflect.DeepEqual(cold.ClaimReceipt, observed.ClaimReceipt) || !reflect.DeepEqual(cold.ReportReceipt, observed.ReportReceipt) || cold.ReportInput != nil || !reflect.DeepEqual(coldWire.calls, []string{"review_worker_get"}) {
				t.Fatalf("cold readback borrowed/replayed another identity: %+v err=%v calls=%v", cold, err, coldWire.calls)
			}
			if definitionFormalSnapshot(t, f, tables) != ledger || !reflect.DeepEqual(upperClientTrees(t, f), before) {
				t.Fatal("cold observation refreshed authority or changed retained files/receipts")
			}
		})
	}
}

func TestUpperWorkerClientRealCoreCallbackFailure(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ctx, session, _ := workerHTTPClient(t, f)
	wire := &upperClientWire{ClientSession: session}
	_, _, beforeRuns := upperClientCounts(t, f)
	before := upperClientTrees(t, f)
	callbacks := 0
	var retained upperworker.Files
	client, err := upperworker.New(wire, upperClientRequest(f), func(ctx context.Context, d upperworker.Delivery, files upperworker.Files) (review.ReviewReport, error) {
		callbacks++
		retained = files
		upperClientReadReport(t, f, ctx, d, files)
		return review.ReviewReport{}, errors.New("test-only callback failed without an engineering report")
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := client.Run(ctx)
	if err == nil || out.State != upperworker.CallbackFailed || callbacks != 1 || wire.count("review_worker_claim") != 1 || wire.count("review_result_submit") != 0 || out.ReportInput != nil || out.ReportReceipt != nil {
		t.Fatalf("callback failure manufactured a result: %+v err=%v calls=%v", out, err, wire.calls)
	}
	reads := wire.count("file_read")
	if _, err := retained.Read(ctx, f.targets[0].MemberID, "same.txt"); err == nil || wire.count("file_read") != reads {
		t.Fatal("callback failure kept a live reader")
	}
	observed, err := client.Observe(ctx)
	if err != nil || observed.State != upperworker.DeliveryUnknown || observed.ReportReceipt != nil || callbacks != 1 {
		t.Fatalf("callback error invented cancellation/completion: %+v err=%v", observed, err)
	}
	if claims, reports, runs := upperClientCounts(t, f); claims != 1 || reports != 0 || runs != beforeRuns {
		t.Fatalf("callback error changed Core result/admission: %d/%d/%d", claims, reports, runs)
	}
	var grantState string
	if err := f.plane.DB.SQL().QueryRowContext(f.ctx, "SELECT state FROM review_grants WHERE review_id=?", f.review).Scan(&grantState); err != nil || grantState != "ACTIVE" {
		t.Fatalf("callback failure silently canceled grant: %s %v", grantState, err)
	}
	if !reflect.DeepEqual(upperClientTrees(t, f), before) {
		t.Fatal("failed callback changed source/managed files")
	}
}
