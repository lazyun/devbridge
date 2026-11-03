package controlapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Real MCP prepare/commit/read and SQLite arbitrate opposite legal choices.
// The public composition fixture deliberately simulates the UPPER starter: it
// returns the durable reserved Run without workspace preparation or native Dot.
func TestConfirmationDifferentVariantsActualMCP(t *testing.T) {
	for _, order := range []string{"a-first", "b-first", "concurrent-entry"} {
		t.Run(order, func(t *testing.T) {
			f := newCompositionFixture(t)
			dataRoot := f.plane.DataPaths.Root
			defer func() { _ = f.plane.Close() }()
			f.ctx = aisession.WithCallerScope(context.Background(), "data-instance-local")
			binding := aiprojectselection.ClientBindingReference{CallerScope: "data-instance-local"}
			initialized, err := f.plane.AIContext.InitializeSession(f.ctx, binding, "variant-wire-session", "")
			if err != nil {
				t.Fatal(err)
			}
			f.sessionID = initialized.AISessionID
			if _, err = f.plane.AIContext.BindProject(f.ctx, binding, f.sessionID, f.projectID, "variant-wire-bind"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()
			instructions := []string{"Inspect tracked files read-only and report interface risks. Do not modify files.", "Inspect tracked files read-only and report test coverage gaps. Do not modify files."}
			var mu sync.Mutex
			armed, arrivals := false, 0
			gate := make(chan struct{})
			openWire := func() (*sdkmcp.ClientSession, func()) {
				service, e := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "variant-race-test", Ready: true}, f.plane.AIContext, f.facade)
				if e != nil {
					t.Fatal(e)
				}
				adapter, e := mcpserver.New(mcpserver.Config{}, service)
				if e != nil {
					t.Fatal(e)
				}
				host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
						wait := armed && rpc.Method == "tools/call" && rpc.Params.Name == "confirmation_commit"
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
							case <-ctx.Done():
								http.Error(w, "entry barrier timed out", 504)
								return
							case <-r.Context().Done():
								return
							}
						}
					}
					adapter.Handler().ServeHTTP(w, r)
				}))
				client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "variant-race", Version: "1"}, nil)
				session, e := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: host.URL + mcpserver.MCPPath, HTTPClient: host.Client(), DisableStandaloneSSE: true, MaxRetries: -1}, nil)
				if e != nil {
					host.Close()
					t.Fatal(e)
				}
				return session, func() { _ = session.Close(); host.Close() }
			}
			wire, closeWire := openWire()
			defer func() { closeWire() }()
			type reply struct {
				wire   *sdkmcp.CallToolResult
				result c.Result
				err    error
			}
			call := func(name string, args any) reply {
				r, e := wire.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
				out := reply{wire: r, err: e}
				if e == nil {
					raw, encodeErr := json.Marshal(r.StructuredContent)
					if encodeErr != nil {
						out.err = encodeErr
					} else {
						out.err = json.Unmarshal(raw, &out.result)
					}
				}
				return out
			}
			logReply := func(label string, r reply) {
				t.Helper()
				if r.err != nil {
					t.Fatalf("%s transport/decode: %v", label, r.err)
				}
				raw, _ := json.Marshal(r.wire)
				t.Logf("%s: %s", label, raw)
			}
			createInput := f.taskPrepare("create-task-only", c.Variant{ID: "create", Label: "Create Task", Summary: "Create a Task without starting review"})
			createInput.Action = "CREATE_TASK"
			createInput.Task.StartReview = false
			taskPrepared := call("task_prepare", map[string]any{"aiSessionId": f.sessionID, "requestId": "create-task-only", "purpose": "TASK", "action": "CREATE_TASK", "task": map[string]any{"featureAreaId": f.areaID, "repositoryId": f.repositoryID, "title": "Variant competition Task", "problemStatement": "Inspect source read-only", "constraints": []string{"read only"}, "startReview": false}, "variants": createInput.Variants})
			logReply("task-only-prepare", taskPrepared)
			if taskPrepared.wire.IsError || taskPrepared.result.Intent == nil {
				t.Fatalf("task-only prepare %+v", taskPrepared.result)
			}
			tp := taskPrepared.result.Intent
			taskCommitted := call("confirmation_commit", map[string]any{"aiSessionId": f.sessionID, "intentId": tp.IntentID, "version": tp.Version, "contentHash": tp.ContentHash, "purpose": tp.Purpose, "action": tp.Action, "variantId": "create", "requestId": "create-task-only-commit", "selection": map[string]any{}})
			logReply("task-only-commit", taskCommitted)
			if taskCommitted.wire.IsError || taskCommitted.result.Intent == nil || taskCommitted.result.Intent.State != "COMPLETED" {
				t.Fatalf("task-only commit %+v", taskCommitted.result)
			}
			taskID := compositionStep(t, *taskCommitted.result.Intent, "CREATE_TASK").TaskID
			preparedReply := call("review_prepare", map[string]any{"aiSessionId": f.sessionID, "requestId": "variant-race-prepare", "purpose": "REVIEW", "action": "START_REVIEW", "review": map[string]any{"taskId": taskID, "featureAreaId": f.areaID, "repositoryId": f.repositoryID, "reviewDepth": "DEEP", "reviewPlan": map[string]any{"mode": "REPOSITORY"}}, "variants": []c.Variant{compositionUpperVariant("a", "Inspect interface risks", instructions[0]), compositionUpperVariant("b", "Inspect test coverage", instructions[1])}})
			logReply("prepare", preparedReply)
			if preparedReply.wire.IsError || preparedReply.result.Intent == nil || preparedReply.result.Intent.State != "PENDING" {
				t.Fatalf("prepare failed %+v", preparedReply.result)
			}
			prepared := *preparedReply.result.Intent
			if len(prepared.Content.Variants) != 2 {
				t.Fatal("missing two legal variants")
			}
			if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 0 || runs != 0 {
				t.Fatalf("prepare effects %d/%d/%d", tasks, reviews, runs)
			}
			commitArgs := func(variant, request string) map[string]any {
				return map[string]any{"aiSessionId": f.sessionID, "intentId": prepared.IntentID, "version": prepared.Version, "contentHash": prepared.ContentHash, "purpose": prepared.Purpose, "action": prepared.Action, "variantId": variant, "requestId": request, "selection": map[string]any{}}
			}
			commit := func(index int) reply {
				return call("confirmation_commit", commitArgs([]string{"a", "b"}[index], []string{"choose-a", "choose-b"}[index]))
			}
			replies := make([]reply, 2)
			switch order {
			case "a-first":
				replies[0] = commit(0)
				replies[1] = commit(1)
			case "b-first":
				replies[1] = commit(1)
				replies[0] = commit(0)
			default:
				mu.Lock()
				armed = true
				mu.Unlock()
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); replies[0] = commit(0) }()
				go func() { defer wg.Done(); replies[1] = commit(1) }()
				wg.Wait()
				mu.Lock()
				n := arrivals
				mu.Unlock()
				if n != 2 {
					t.Fatalf("HTTP arrivals %d", n)
				}
			}
			logReply("choice-a", replies[0])
			logReply("choice-b", replies[1])
			read := func(request string) reply {
				args := map[string]any{"aiSessionId": f.sessionID, "intentId": prepared.IntentID, "version": prepared.Version, "purpose": prepared.Purpose, "contentHash": prepared.ContentHash}
				if request != "" {
					args["requestId"] = request
				}
				return call("confirmation_get", args)
			}
			current := read("")
			logReply("settled-read", current)
			if current.wire.IsError || current.result.Intent == nil || current.result.Intent.Commit == nil || current.result.Intent.State != "COMPLETED" {
				t.Fatalf("not completed %+v", current.result)
			}
			settled := *current.result.Intent
			winner := 0
			if settled.Commit.VariantID == "b" {
				winner = 1
			} else if settled.Commit.VariantID != "a" {
				t.Fatalf("unknown choice %s", settled.Commit.VariantID)
			}
			if order == "a-first" && winner != 0 || order == "b-first" && winner != 1 {
				t.Fatal("ordered positive control chose opposite variant")
			}
			winning := replies[winner]
			if winning.wire.IsError || winning.result.Failure != nil || winning.result.Intent == nil || winning.result.Intent.Submission == nil || winning.result.Intent.Submission.Outcome != c.Consumed || winning.result.Intent.Submission.Request.VariantID != settled.Commit.VariantID {
				t.Fatalf("winner consumption missing %+v", winning.result)
			}
			requestIDs := []string{"choose-a", "choose-b"}
			variantIDs := []string{"a", "b"}
			assertSubmissionIdentity := func(sub *c.Submission, index int) {
				t.Helper()
				if sub == nil {
					return
				}
				r := sub.Request
				if r.RequestID != requestIDs[index] || r.AISessionID != f.sessionID || r.IntentID != prepared.IntentID || r.Version != prepared.Version || r.ContentHash != prepared.ContentHash || r.Purpose != prepared.Purpose || r.Action != prepared.Action {
					t.Fatalf("request-specific submission borrowed another identity: %+v", sub)
				}
				if index == winner {
					if sub.Outcome != c.Consumed || !sub.Recorded || r.VariantID != variantIDs[index] || sub.ReceiptRequestID != requestIDs[winner] {
						t.Fatalf("winner receipt identity %+v", sub)
					}
				} else {
					// Shared Intent.Commit may show winner history. The request-bound
					// Submission must never lend that consumption to the losing request.
					// UNKNOWN and an empty VariantID are legitimate unrecorded reads.
					if sub.Outcome == c.Consumed || sub.Consumed != nil && *sub.Consumed || sub.ConsumptionDisposition == "CONSUMED" || sub.BusinessOutcome == "COMMITTED" || sub.ReceiptRequestID != "" && sub.ReceiptRequestID != requestIDs[index] || r.VariantID != "" && r.VariantID != variantIDs[index] {
						t.Fatalf("loser borrowed consumption or receipt: %+v", sub)
					}
				}
			}
			assertSubmissionIdentity(winning.result.Intent.Submission, winner)
			assertLoser := func(r reply) {
				t.Helper()
				if r.err != nil {
					t.Fatal(r.err)
				}
				if !r.wire.IsError || r.result.Failure == nil {
					t.Fatalf("opposite choice falsely succeeded %+v", r.result)
				}
				if r.result.Intent != nil {
					assertSubmissionIdentity(r.result.Intent.Submission, 1-winner)
				}
			}
			assertLoser(replies[1-winner])
			reviewStep := compositionStep(t, settled, "START_REVIEW")
			assertFacts := func() {
				t.Helper()
				if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 1 || runs != 1 {
					t.Fatalf("duplicate composition %d/%d/%d", tasks, reviews, runs)
				}
				rv, e := f.plane.Reviews.Get(f.ctx, reviewStep.ReviewID)
				if e != nil || rv.Instruction != instructions[winner] {
					t.Fatalf("review choice mismatch %+v %v", rv, e)
				}
				r := read("")
				if r.err != nil || r.wire.IsError || r.result.Intent == nil {
					t.Fatalf("read %+v %v", r.result, r.err)
				}
				got := r.result.Intent
				if got.State != "COMPLETED" || !reflect.DeepEqual(got.Commit, settled.Commit) || got.ContentHash != prepared.ContentHash || !reflect.DeepEqual(got.Content, prepared.Content) || !reflect.DeepEqual(got.Steps, settled.Steps) {
					t.Fatalf("immutable intent drift %+v", got)
				}
				owner, e := f.plane.Tasks.Get(f.ctx, taskID)
				if e != nil || owner.CurrentReviewID != reviewStep.ReviewID {
					t.Fatalf("Task current Review %+v %v", owner, e)
				}
				if compositionStep(t, *got, "START_REVIEW").ReviewID != reviewStep.ReviewID {
					t.Fatal("business identity drift")
				}
			}
			assertFacts()
			// Different request IDs are not intrinsically conflicts: sameChoice can
			// legally refer to the original consumption without creating more objects.
			same := call("confirmation_commit", commitArgs(settled.Commit.VariantID, "same-choice-alias"))
			logReply("same-choice-new-request", same)
			if same.wire.IsError || same.result.Intent == nil || same.result.Intent.Submission == nil || same.result.Intent.Submission.Outcome != c.Consumed || same.result.Intent.Submission.Request.RequestID != "same-choice-alias" || same.result.Intent.Submission.Request.VariantID != settled.Commit.VariantID || same.result.Intent.Submission.ReceiptRequestID != settled.Commit.RequestID || !reflect.DeepEqual(same.result.Intent.Steps, settled.Steps) || !reflect.DeepEqual(same.result.Intent.Commit, settled.Commit) {
				t.Fatalf("same choice alias %+v", same.result)
			}
			assertFacts()
			for _, cold := range []bool{false, true} {
				if cold {
					closeWire()
					closeWire = func() {}
					if err = f.plane.Close(); err != nil {
						t.Fatal(err)
					}
					f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: dataRoot})
					if err != nil {
						t.Fatal(err)
					}
					f.facade, err = controlapi.NewFromControlPlane(f.plane)
					if err != nil {
						t.Fatal(err)
					}
					wire, closeWire = openWire()
				}
				for i := range 2 {
					replay := commit(i)
					logReply("exact-replay", replay)
					if i == winner {
						if replay.wire.IsError || replay.result.Intent == nil || !reflect.DeepEqual(replay.result.Intent.Submission, winning.result.Intent.Submission) {
							t.Fatalf("winner exact receipt changed cold=%t %+v", cold, replay.result)
						}
					} else {
						assertLoser(replay)
					}
					rr := read([]string{"choose-a", "choose-b"}[i])
					logReply("request-read", rr)
					if rr.wire.IsError || rr.result.Intent == nil || rr.result.Intent.Commit == nil || rr.result.Intent.Commit.VariantID != settled.Commit.VariantID {
						t.Fatalf("request read changed chosen variant cold=%t %+v", cold, rr.result)
					}
					assertSubmissionIdentity(rr.result.Intent.Submission, i)
				}
				assertFacts()
				t.Logf("WITNESS order=%s cold=%t winner=%s originalRequest=%s task=%s review=%s run=%s oneTaskReviewRun=true", order, cold, settled.Commit.VariantID, settled.Commit.RequestID, taskID, reviewStep.ReviewID, reviewStep.RunID)
			}
		})
	}
}
