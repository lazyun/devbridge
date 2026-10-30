package controlapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Use the actual Local MCP HTTP stack, including workflow and surface-envelope
// middleware. The earlier in-memory shared catalog tests cannot catch this
// transport's enrichment of successful command receipts.
func workerHTTPClient(t *testing.T, f *upperLifecycleFixture) (context.Context, *sdkmcp.ClientSession, func()) {
	t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "worker-http-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := localmcp.New(localmcp.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(adapter.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "worker-http-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + localmcp.MCPPath, HTTPClient: &http.Client{Timeout: 20 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		server.Close()
		cancel()
		t.Fatal(err)
	}
	close := func() { session.Close(); server.Close(); cancel() }
	t.Cleanup(close)
	return ctx, session, close
}

func TestUpperReviewWorkerHTTPCancelUsesSemanticReviewOwner(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ctx, session, _ := workerHTTPClient(t, f)
	in := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "http-delivery", WorkerID: "worker-label"}}
	callUpperMCP(t, ctx, session, "review_worker_claim", mcpObject(t, in))
	args := map[string]any{"aiSessionId": f.session, "ownerType": "REVIEW", "ownerId": f.review, "runId": f.run, "requestId": "http-cancel", "reason": "HTTP regression", "expectedUpperVersion": mcpObject(t, f.upperVersion())}
	envelope := callUpperMCP(t, ctx, session, "operation_cancel", args)
	receipt := envelope["result"].(map[string]any)
	if receipt["state"] != "CANCELED" || receipt["backendDisposition"] != "NO_BACKEND" || receipt["ownerId"] != f.review || receipt["executionId"] != "review:"+f.review {
		t.Fatalf("changed receipt=%+v", receipt)
	}
	scope := envelope["context"].(map[string]any)
	workflow := envelope["workflow"].(map[string]any)
	if scope["reviewId"] != f.review || scope["executionId"] != nil || scope["taskId"] != f.task || workflow["readError"] != nil || workflow["stale"] == true || envelope["refreshRequired"] == true {
		t.Fatalf("receipt enriched with wrong Execution scope: %+v", envelope)
	}
	if workflow["currentState"] != "REVIEW_REQUIRED" {
		t.Fatalf("workflow lost actual post-cancel state: %+v", workflow)
	}
	replay := callUpperMCP(t, ctx, session, "operation_cancel", args)
	if !reflect.DeepEqual(receipt, replay["result"]) {
		t.Fatal("HTTP cancellation replay rewrote immutable receipt")
	}
	state := callUpperMCP(t, ctx, session, "review_worker_get", mcpObject(t, in))["result"].(map[string]any)
	if state["state"] != review.WorkerOutcomeUnknown || state["executionPayload"] != nil {
		t.Fatalf("cancel invented host termination: %+v", state)
	}
	// A completed old cancellation replay must not display the new Review's
	// identity/actions as if they belonged to this immutable old receipt.
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "http-successor-review", Purpose: c.Review, Action: "START_REVIEW", Review: &c.ReviewBody{TaskID: f.task, FeatureAreaID: f.area, RepositoryTargets: f.targets, ReviewDepth: task.ReviewDepthDeep, ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository}}, Variants: []c.Variant{{ID: "upper", Summary: "successor for immutable replay regression", ExecutorKind: string(review.ExecutorUpperLLM), ReviewInstruction: "Explicit disposable successor Review for cancellation replay regression"}}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "commit-http-successor", ContentHash: prepared.ContentHash, VariantID: "upper"})
	if err != nil || next.State != "COMPLETED" || len(next.Steps) == 0 || next.Steps[0].ReviewID == f.review || next.Steps[0].ReviewID == "" {
		t.Fatalf("successor not admitted: %+v %v", next, err)
	}
	old := callUpperMCP(t, ctx, session, "operation_cancel", args)
	oldContext := old["context"].(map[string]any)
	oldWorkflow := old["workflow"].(map[string]any)
	if !reflect.DeepEqual(old["result"], receipt) || oldContext["reviewId"] != f.review || oldContext["runId"] != f.run || old["refreshRequired"] != true || oldWorkflow["stale"] != true || oldWorkflow["nextAction"] != nil {
		t.Fatalf("old cancellation reinterpreted as successor: %+v", old)
	}
	for _, name := range []string{"review_worker_get", "review_worker_claim"} {
		wire := callUpperMCP(t, ctx, session, name, mcpObject(t, in))
		ctx := wire["context"].(map[string]any)
		workflow := wire["workflow"].(map[string]any)
		if !reflect.DeepEqual(wire["result"], state) || ctx["reviewId"] != f.review || ctx["runId"] != f.run || wire["refreshRequired"] != true || workflow["stale"] != true || workflow["nextAction"] != nil {
			t.Fatalf("%s historical receipt adopted successor: %+v", name, wire)
		}
	}
}

func TestUpperReviewWorkerHTTPRestartGuidanceRefusesContinuation(t *testing.T) {
	for _, enrolled := range []bool{true, false} {
		name := "enrolled"
		if !enrolled {
			name = "legacy-unclaimed"
		}
		t.Run(name, func(t *testing.T) {
			f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
			ctx, session, closeHTTP := workerHTTPClient(t, f)
			in := review.WorkerClaimInput{UpperTarget: f.access.Target, WorkerClaimIdentity: review.WorkerClaimIdentity{RequestID: "http-restart-delivery", WorkerID: "worker-label"}}
			var receipt any
			var otherTaskID string
			if enrolled {
				wire := callUpperMCP(t, ctx, session, "review_worker_claim", mcpObject(t, in))
				receipt = wire["result"].(map[string]any)["receipt"]
				other := callUpperMCP(t, ctx, session, "task_create", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "requestId": "http-other-task", "title": "Another independently selected Task", "problemStatement": "Planning only; never transfer the interrupted worker", "repositoryId": f.targets[0].RepositoryID})
				otherTaskID = other["result"].(map[string]any)["id"].(string)
			}
			closeHTTP()
			if err := f.plane.Close(); err != nil {
				t.Fatal(err)
			}
			f.plane = nil
			var err error
			f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
			if err != nil {
				t.Fatal(err)
			}
			f.facade, err = controlapi.NewFromControlPlane(f.plane)
			if err != nil {
				t.Fatal(err)
			}
			ctx, session, _ = workerHTTPClient(t, f)
			access := callUpperMCP(t, ctx, session, "review_access_get", map[string]any{"aiSessionId": f.session, "reviewId": f.review})
			state := access["result"].(map[string]any)
			if state["state"] != "RECOVERY_REQUIRED" || !reflect.DeepEqual(state["target"], mcpObject(t, in.UpperTarget)) {
				t.Fatalf("restart facts rewritten: %+v", state)
			}
			workflow := access["workflow"].(map[string]any)
			action := workflow["nextAction"].(map[string]any)
			want := "RECOVER_AGENT_OPERATION"
			if enrolled {
				want = "CANCEL_REVIEW"
			}
			if workflow["currentState"] != "RECOVERY_REQUIRED" || action["code"] != want {
				t.Fatalf("wrong restart guidance enrolled=%t: %+v", enrolled, workflow)
			}
			if !enrolled {
				return
			}
			if !strings.Contains(strings.ToLower(action["message"].(string)), "unsupported") {
				t.Fatalf("worker limitation not explained: %+v", action)
			}
			workspace := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": f.task, "include": []string{"TASK"}})
			if workspace["workflow"].(map[string]any)["nextAction"].(map[string]any)["code"] != want {
				t.Fatalf("workspace offers unusable recovery: %+v", workspace["workflow"])
			}
			other := callUpperMCP(t, ctx, session, "workspace_get", map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "taskId": otherTaskID, "include": []string{"TASK"}})
			otherWorkflow := other["workflow"].(map[string]any)
			otherAction := otherWorkflow["nextAction"].(map[string]any)
			if otherAction["code"] == "RECOVER" || otherAction["code"] == "RECOVER_AGENT_OPERATION" || otherAction["code"] == "CANCEL_REVIEW" {
				t.Fatalf("different selected Task inherited worker action: %+v", otherWorkflow)
			}
			got := callUpperMCP(t, ctx, session, "review_worker_get", mcpObject(t, in))["result"].(map[string]any)
			if got["state"] != review.WorkerOutcomeUnknown || got["executionPayload"] != nil || !reflect.DeepEqual(got["receipt"], receipt) {
				t.Fatalf("readback changed dispatch: %+v", got)
			}
			var epoch string
			var generation int64
			if err = f.plane.DB.SQL().QueryRow(`SELECT runtime_epoch,generation FROM review_grants WHERE review_id=?`, f.review).Scan(&epoch, &generation); err != nil {
				t.Fatal(err)
			}
			if epoch != in.RuntimeEpoch || generation != in.GrantGeneration {
				t.Fatal("guidance reads refreshed grant")
			}
		})
	}
}
