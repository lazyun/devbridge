package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Opposite, individually legal intentions must not both consume one rejected
// predecessor. The barrier observes complete HTTP requests before the production
// adapter, not an instrumented DB schedule; there are no injected SQL/Git faults.
func TestMergeRetryVersusReleaseActualMCP(t *testing.T) {
	for _, order := range []string{"retry-first", "release-first", "concurrent-entry"} {
		t.Run(order, func(t *testing.T) {
			f := completeFollowupTask(t)
			prepared, confirm := prepareFollowupMerge(t, f)
			plan := prepared.Content.FeatureArea.DeliveryPlan
			first, second := plan.Members[0], plan.Members[1]
			dirty := filepath.Join(second.TargetPath, "same.txt")
			original, err := os.ReadFile(dirty)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(dirty, []byte("actual uncommitted target bytes\n"), 0600); err != nil {
				t.Fatal(err)
			}
			partial, err := f.facade.CommitConfirmation(f.ctx, confirm)
			if err == nil || len(partial.Steps) == 0 {
				t.Fatalf("expected actual dirty-target rejection: %+v %v", partial, err)
			}
			request := partial.Steps[0].RequestID
			var predecessor, outcome, rejection string
			var beforeEffect int
			err = f.plane.DB.SQL().QueryRow(`SELECT o.operation_id,r.outcome,r.before_external_effect,r.error FROM merge_delivery_current_operations o JOIN merge_delivery_outcomes r ON r.operation_id=o.operation_id WHERE o.merge_request_id=? AND o.repository_id=?`, request, second.RepositoryID).Scan(&predecessor, &outcome, &beforeEffect, &rejection)
			if err != nil || outcome != "REJECTED_NOT_CONSUMED" || beforeEffect != 1 || !strings.Contains(rejection, "DIRTY") {
				t.Fatalf("not actual dirty pre-effect refusal: %s %d %s %v", outcome, beforeEffect, rejection, err)
			}
			if gitOutput(t, first.TargetPath, "rev-parse", first.DevelopmentRef) != first.SourceHead || gitOutput(t, second.TargetPath, "rev-parse", second.DevelopmentRef) != second.ExpectedDevHead {
				t.Fatal("incorrect partial Git effects")
			}
			if err = os.WriteFile(dirty, original, 0600); err != nil {
				t.Fatal(err)
			}

			// A normal public AI session binding, never a forged business row.
			wireCtx := aisession.WithCallerScope(context.Background(), "data-instance-local")
			ref := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
			initialized, err := f.plane.AIContext.InitializeSession(wireCtx, ref, "merge-race-session", "")
			if err != nil {
				t.Fatal(err)
			}
			wireSession := initialized.AISessionID
			if _, err = f.plane.AIContext.BindProject(wireCtx, ref, wireSession, f.project, "merge-race-bind"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			type reply struct {
				result *sdkmcp.CallToolResult
				err    error
			}
			var mu sync.Mutex
			armed, arrivals := false, 0
			gate := make(chan struct{})
			openWire := func() (*sdkmcp.ClientSession, func()) {
				service, e := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "merge-race-test", Ready: true}, f.plane.AIContext, f.facade)
				if e != nil {
					t.Fatal(e)
				}
				adapter, e := mcpserver.New(mcpserver.Config{}, service)
				if e != nil {
					t.Fatal(e)
				}
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						body, e := io.ReadAll(r.Body)
						if e != nil {
							http.Error(w, e.Error(), 400)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(body))
						var rpc struct {
							Method string `json:"method"`
							Params struct {
								Name string `json:"name"`
							} `json:"params"`
						}
						_ = json.Unmarshal(body, &rpc)
						mu.Lock()
						wait := armed && rpc.Method == "tools/call" && rpc.Params.Name == "feature_area_merge_reprepare"
						if wait {
							arrivals++
							if arrivals == 2 {
								armed = false
								close(gate)
							}
						}
						mu.Unlock()
						if wait {
							select {
							case <-gate:
							case <-r.Context().Done():
								return
							case <-ctx.Done():
								http.Error(w, "entry barrier timeout", 504)
								return
							}
						}
					}
					adapter.Handler().ServeHTTP(w, r)
				})
				host := httptest.NewServer(handler)
				client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "merge-race", Version: "1"}, nil)
				session, e := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + mcpserver.MCPPath, HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
				if e != nil {
					host.Close()
					t.Fatal(e)
				}
				return session, func() { _ = session.Close(); host.Close() }
			}
			wire, closeWire := openWire()
			defer func() { closeWire() }()
			candidates, err := wire.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_merge_retry_candidates", Arguments: map[string]any{"aiSessionId": wireSession, "featureAreaId": f.area}})
			if err != nil || candidates.IsError {
				t.Fatalf("candidates: %+v %v", candidates, err)
			}
			var projected struct {
				Result struct {
					Candidates []delivery.MergeRetryCandidate `json:"candidates"`
				} `json:"result"`
			}
			raw, _ := json.Marshal(candidates.StructuredContent)
			if err = json.Unmarshal(raw, &projected); err != nil {
				t.Fatal(err)
			}
			if len(projected.Result.Candidates) != 1 {
				t.Fatalf("candidate projection %s", raw)
			}
			target := projected.Result.Candidates[0]
			if !target.CanRelease || target.OperationID != predecessor || target.MergeRequestID != request || target.RepositoryID != second.RepositoryID {
				t.Fatalf("wrong exact target %+v", target)
			}
			args := func(release bool) map[string]any {
				return map[string]any{"aiSessionId": wireSession, "featureAreaId": f.area, "mergeRequestId": target.MergeRequestID, "repositoryId": target.RepositoryID, "predecessorOperationId": target.OperationID, "requestId": fmt.Sprintf("opposite-%t", release), "releasePlan": release}
			}
			call := func(release bool) reply {
				r, e := wire.CallTool(ctx, &sdkmcp.CallToolParams{Name: "feature_area_merge_reprepare", Arguments: args(release)})
				return reply{r, e}
			}
			scalar := func(query string, args ...any) string {
				t.Helper()
				var value string
				if e := f.plane.DB.SQL().QueryRow(query, args...).Scan(&value); e != nil {
					t.Fatal(e)
				}
				return value
			}
			frozen := scalar(`SELECT plan_json||plan_sha256 FROM feature_area_commit_receipts WHERE request_id=?`, request)
			firstReceipt := func() string {
				return scalar(`SELECT json_object('state',state,'head',resulting_dev_head,'completed',completed_at,'error',last_error) FROM feature_area_commit_members WHERE request_id=? AND repository_id=?`, request, first.RepositoryID)
			}
			firstBefore := firstReceipt()
			firstLog := gitOutput(t, first.TargetPath, "reflog", "show", "--format=%H %gs", first.DevelopmentRef)
			secondLog := gitOutput(t, second.TargetPath, "reflog", "show", "--format=%H %gs", second.DevelopmentRef)
			replies := make([]reply, 2)
			switch order {
			case "retry-first":
				replies[0] = call(false)
				replies[1] = call(true)
			case "release-first":
				replies[1] = call(true)
				replies[0] = call(false)
			default:
				mu.Lock()
				armed = true
				mu.Unlock()
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); replies[0] = call(false) }()
				go func() { defer wg.Done(); replies[1] = call(true) }()
				wg.Wait()
				mu.Lock()
				n := arrivals
				mu.Unlock()
				if n != 2 {
					t.Fatalf("only %d complete HTTP arrivals", n)
				}
			}
			for i, r := range replies {
				if r.err != nil {
					t.Fatalf("transport %d: %v", i, r.err)
				}
				wireJSON, _ := json.Marshal(r.result)
				t.Logf("initial release=%t response=%s", i == 1, wireJSON)
			}
			var children, releases, dispatches, outcomes int
			var released, completed bool
			err = f.plane.DB.SQL().QueryRow(`SELECT (SELECT count(*) FROM merge_delivery_operations WHERE predecessor_operation_id=?),(SELECT count(*) FROM merge_plan_releases WHERE merge_request_id=?),(SELECT count(*) FROM merge_delivery_dispatches),(SELECT count(*) FROM merge_delivery_outcomes),released_at IS NOT NULL,completed_at IS NOT NULL FROM feature_area_commit_receipts WHERE request_id=?`, predecessor, request, request).Scan(&children, &releases, &dispatches, &outcomes, &released, &completed)
			if err != nil {
				t.Fatal(err)
			}
			retryWon := children == 1 && releases == 0 && !released && completed
			releaseWon := children == 0 && releases == 1 && released && !completed
			if !retryWon && !releaseWon {
				t.Fatalf("contradictory/nonterminal facts children=%d releases=%d released=%t complete=%t dispatch=%d outcomes=%d", children, releases, released, completed, dispatches, outcomes)
			}
			if order == "retry-first" && !retryWon || order == "release-first" && !releaseWon {
				t.Fatal("single-intention positive control failed")
			}
			wantDispatch := 2
			if retryWon {
				wantDispatch = 3
			}
			if dispatches != wantDispatch || outcomes != wantDispatch {
				t.Fatalf("dispatch/outcome %d/%d want %d", dispatches, outcomes, wantDispatch)
			}
			current := scalar(`SELECT operation_id FROM merge_delivery_current_operations WHERE merge_request_id=? AND repository_id=?`, request, second.RepositoryID)
			if releaseWon && current != predecessor {
				t.Fatal("release replaced current operation")
			}
			if retryWon {
				var child, childOutcome, mergeOID string
				err = f.plane.DB.SQL().QueryRow(`SELECT o.operation_id,r.outcome,r.merge_oid FROM merge_delivery_operations o JOIN merge_delivery_dispatches d ON d.operation_id=o.operation_id JOIN merge_delivery_outcomes r ON r.operation_id=o.operation_id WHERE o.predecessor_operation_id=?`, predecessor).Scan(&child, &childOutcome, &mergeOID)
				if err != nil || child != current || childOutcome != "APPLIED" || mergeOID != second.SourceHead {
					t.Fatalf("child proof current=%s child=%s outcome=%s oid=%s error=%v", current, child, childOutcome, mergeOID, err)
				}
			}
			winner := 0
			if releaseWon {
				winner = 1
			}
			if replies[winner].result.IsError {
				t.Fatalf("winner not acknowledged %+v", replies[winner].result)
			}
			// An error bit alone is not the effect oracle. Independently reject any
			// loser receipt claiming an authorized operation or an effected release,
			// even if that contradictory receipt is accompanied by IsError=true.
			assertLoser := func(r *sdkmcp.CallToolResult) {
				t.Helper()
				if r == nil {
					t.Fatal("missing loser response")
				}
				var envelope struct {
					Result delivery.MergeRetryReceipt `json:"result"`
				}
				encoded, e := json.Marshal(r.StructuredContent)
				if e != nil {
					t.Fatal(e)
				}
				if e = json.Unmarshal(encoded, &envelope); e != nil {
					t.Fatal(e)
				}
				if envelope.Result.PlanReleased || envelope.Result.OperationID != "" {
					t.Fatalf("loser advertised contradictory authority/effect: %s", encoded)
				}
				// Production's closed retry/release contract returns an error when
				// the exact predecessor is no longer eligible; do not bind wording.
				if !r.IsError {
					t.Fatalf("loser falsely acknowledged success: %s", encoded)
				}
			}
			assertLoser(replies[1-winner].result)
			stable := func() string {
				return scalar(`SELECT json_object('ops',(SELECT json_group_array(operation_id||payload_sha256) FROM (SELECT * FROM merge_delivery_operations ORDER BY operation_id)),'dispatch',(SELECT count(*) FROM merge_delivery_dispatches),'outcomes',(SELECT count(*) FROM merge_delivery_outcomes),'release',(SELECT count(*) FROM merge_plan_releases),'current',(SELECT json_group_array(operation_id) FROM (SELECT * FROM merge_delivery_current_operations ORDER BY repository_id)),'parent',json_object('completed',completed_at,'released',released_at,'plan',plan_json,'sha',plan_sha256)) FROM feature_area_commit_receipts WHERE request_id=?`, request)
			}
			terminal := stable()
			check := func() {
				t.Helper()
				if frozen != scalar(`SELECT plan_json||plan_sha256 FROM feature_area_commit_receipts WHERE request_id=?`, request) || firstReceipt() != firstBefore || stable() != terminal {
					t.Fatal("frozen/terminal/first receipt changed")
				}
				if gitOutput(t, first.TargetPath, "reflog", "show", "--format=%H %gs", first.DevelopmentRef) != firstLog {
					t.Fatal("already completed member changed")
				}
				wantHead := second.ExpectedDevHead
				if retryWon {
					wantHead = second.SourceHead
				}
				if gitOutput(t, second.TargetPath, "rev-parse", second.DevelopmentRef) != wantHead {
					t.Fatal("second ref mismatch")
				}
				log := gitOutput(t, second.TargetPath, "reflog", "show", "--format=%H %gs", second.DevelopmentRef)
				if releaseWon && log != secondLog {
					t.Fatal("release produced Git effect")
				}
				if retryWon && strings.Count(log, "merge "+second.SourceHead+":") != 1 {
					t.Fatalf("expected exactly one retry merge: %s", log)
				}
				facts, e := f.plane.WorkflowProgression.CommitPresentationFacts(f.ctx, f.project, f.area)
				if e != nil {
					t.Fatal(e)
				}
				wantState := "COMPLETED"
				if releaseWon {
					wantState = "RELEASED"
				}
				if facts.Pending || facts.Delivery.State != wantState {
					t.Fatalf("public delivery facts %+v", facts)
				}
				cap, e := f.plane.WorkflowProgression.CommitCapability(f.ctx, f.project, f.area)
				if e != nil || cap.MergeRetryOperation != nil {
					t.Fatalf("stale retry authority %+v %v", cap, e)
				}
				if releaseWon {
					old, e := f.facade.ReadConfirmation(f.ctx, c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash})
					if e != nil || old.CanResume || old.State == "COMPLETED" {
						t.Fatalf("old released confirmation %+v %v", old, e)
					}
				}
			}
			check()
			secondTerminalLog := gitOutput(t, second.TargetPath, "reflog", "show", "--format=%H %gs", second.DevelopmentRef)
			for _, cold := range []bool{false, true} {
				if cold {
					closeWire()
					closeWire = func() {}
					reopenDirectDeliveryFixture(t, f)
					f.plane.Retirement.Close()
					wire, closeWire = openWire()
				}
				for _, release := range []bool{false, true} {
					r := call(release)
					if r.err != nil {
						t.Fatal(r.err)
					}
					index := 0
					if release {
						index = 1
					}
					if index == winner {
						a, _ := json.Marshal(replies[index].result.StructuredContent)
						b, _ := json.Marshal(r.result.StructuredContent)
						if r.result.IsError || string(a) != string(b) {
							t.Fatalf("winner receipt drift cold=%t: %s / %s", cold, a, b)
						}
					}
					if index != winner {
						assertLoser(r.result)
					}
					wireJSON, _ := json.Marshal(r.result)
					t.Logf("replay cold=%t release=%t response=%s", cold, release, wireJSON)
				}
				check()
				if gitOutput(t, second.TargetPath, "reflog", "show", "--format=%H %gs", second.DevelopmentRef) != secondTerminalLog {
					t.Fatal("replay produced additional physical effect")
				}
			}
			t.Logf("WITNESS %s: real dirty refusal %q; children=%d releases=%d dispatches=%d outcomes=%d; released=%t completed=%t; frozen plan=%s; terminal=%s", order, rejection, children, releases, dispatches, outcomes, released, completed, plan.SHA256, terminal)
		})
	}
}
