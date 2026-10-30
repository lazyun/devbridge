package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func wakeupFixture() (reviewWakeupIdentity, presentation.Projection) {
	in := reviewWakeupIdentity{AISessionID: "session", ProjectID: "project", FeatureAreaID: "area", ReviewID: "review", DecisionID: "REVIEW_APPROVAL:review", ResultSHA256: strings.Repeat("a", 64), ProvenanceSHA256: strings.Repeat("b", 64)}
	p := presentation.Projection{FeatureArea: presentation.Identity{ID: in.FeatureAreaID}, LifecycleStatus: "ACTIVE", CurrentState: "REVIEW_APPROVAL_PENDING", PendingDecision: &presentation.Decision{Kind: "REVIEW_APPROVAL", ID: in.DecisionID, SubjectID: "intent"}, Guidance: &wp.Guidance{TaskID: "intent", TaskPhase: task.PhaseReviewApprovalPending, WorkflowMode: "CURRENT", ProjectID: in.ProjectID, FeatureAreaID: in.FeatureAreaID, CurrentState: "REVIEW_APPROVAL_PENDING", ReviewID: in.ReviewID, DecisionID: in.DecisionID, CurrentWorkflowReview: &wp.ReviewFact{ID: in.ReviewID, Status: "SUCCEEDED"}, Approval: &wp.Approval{TaskID: "intent", Status: "PENDING", ReviewID: in.ReviewID, DecisionID: in.DecisionID, ResultSHA256: in.ResultSHA256, ProvenanceSHA256: in.ProvenanceSHA256}}}
	return in, p
}
func TestReviewWakeupClaimsConcurrentIdentityAndCapacity(t *testing.T) {
	in, _ := wakeupFixture()
	ledger := &reviewWakeupClaims{attempted: make(map[reviewWakeupIdentity]struct{})}
	var wg sync.WaitGroup
	var winners atomic.Int32
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := ledger.reserve(in)
			if err != nil {
				t.Error(err)
			}
			if v.Status == "CLAIMED" {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("auto winners=%d", winners.Load())
	}
	for _, change := range []func(*reviewWakeupIdentity){func(v *reviewWakeupIdentity) { v.AISessionID = "another" }, func(v *reviewWakeupIdentity) { v.ProjectID = "another" }, func(v *reviewWakeupIdentity) { v.FeatureAreaID = "another" }, func(v *reviewWakeupIdentity) { v.ReviewID = "another" }, func(v *reviewWakeupIdentity) { v.DecisionID = "another" }, func(v *reviewWakeupIdentity) { v.ResultSHA256 = strings.Repeat("c", 64) }, func(v *reviewWakeupIdentity) { v.ProvenanceSHA256 = strings.Repeat("c", 64) }} {
		other := in
		change(&other)
		v, err := ledger.reserve(other)
		if err != nil || v.Status != "CLAIMED" {
			t.Fatalf("new identity=%+v %v", v, err)
		}
	}
	withRun := in
	withRun.RunID = "review-run"
	if v, err := ledger.reserve(withRun); err != nil || v.Status != "ALREADY_ATTEMPTED" {
		t.Fatalf("mixed-version key=%+v %v", v, err)
	}
	for len(ledger.attempted) < reviewWakeupCapacity {
		other := in
		other.AISessionID = fmt.Sprint(len(ledger.attempted))
		ledger.attempted[other] = struct{}{}
	}
	other := in
	other.AISessionID = "full"
	if _, err := ledger.reserve(other); err == nil {
		t.Fatal("capacity evicted old receipt")
	}
	if v, err := ledger.reserve(in); err != nil || v.Status != "ALREADY_ATTEMPTED" {
		t.Fatal(v, err)
	}
}
func TestReviewWakeupEligibilityRejectsStaleAndForeignFacts(t *testing.T) {
	in, p := wakeupFixture()
	if !eligibleReviewWakeup(in, p) {
		t.Fatal("exact fixture rejected")
	}
	in.RunID = "review-run"
	p.Details.RunID = in.RunID
	if !eligibleReviewWakeup(in, p) {
		t.Fatal("exact Review run rejected")
	}
	in.RunID = "foreign-run"
	if eligibleReviewWakeup(in, p) {
		t.Fatal("foreign Review run accepted")
	}
	in.RunID = ""
	changes := []func(*presentation.Projection){
		func(p *presentation.Projection) { p.Guidance = nil }, func(p *presentation.Projection) { p.LifecycleStatus = "RETIRING" }, func(p *presentation.Projection) { p.LifecycleStatus = "RETIRED" }, func(p *presentation.Projection) { p.FeatureArea.ID = "foreign" }, func(p *presentation.Projection) { p.CurrentState = "REVIEW_RUNNING" }, func(p *presentation.Projection) { p.Guidance.CurrentState = "IMPLEMENTATION_RUNNING" }, func(p *presentation.Projection) { p.Guidance.RefreshRequired = true }, func(p *presentation.Projection) { p.Guidance.Legacy = true }, func(p *presentation.Projection) { p.Guidance.TaskID = "development" }, func(p *presentation.Projection) { p.Guidance.ProjectID = "foreign" }, func(p *presentation.Projection) { p.Guidance.FeatureAreaID = "foreign" }, func(p *presentation.Projection) { p.Guidance.ReviewID = "superseded" }, func(p *presentation.Projection) { p.Guidance.CurrentWorkflowReview.Status = "FAILED" }, func(p *presentation.Projection) { p.Guidance.CurrentWorkflowReview.ID = "foreign" }, func(p *presentation.Projection) { p.Guidance.Approval.Status = "APPROVE" }, func(p *presentation.Projection) { p.Guidance.Approval.ResultSHA256 = "" }, func(p *presentation.Projection) { p.Guidance.Approval.ProvenanceSHA256 = "" }, func(p *presentation.Projection) { p.PendingDecision.ID = "foreign" }, func(p *presentation.Projection) { p.PendingDecision.SubjectID = "foreign" }, func(p *presentation.Projection) { p.PendingDecision.Kind = "RUNTIME_APPROVAL" }, func(p *presentation.Projection) { p.WorkflowMode = "HISTORICAL" }, func(p *presentation.Projection) {
			p.CurrentWorkflowReview = &wp.ReviewFact{ID: "foreign", Status: "SUCCEEDED"}
		}, func(p *presentation.Projection) { p.Recovery = &presentation.Recovery{Required: true} }, func(p *presentation.Projection) { p.Cancellation = &presentation.Cancellation{} },
	}
	for i, change := range changes {
		_, v := wakeupFixture()
		change(&v)
		if eligibleReviewWakeup(in, v) {
			t.Fatalf("stale variant %d accepted", i)
		}
	}
	in.ResultSHA256 = "not-a-digest"
	p.Guidance.Approval.ResultSHA256 = in.ResultSHA256
	if eligibleReviewWakeup(in, p) {
		t.Fatal("invalid durable digest accepted")
	}
}
func TestReviewWakeupToolChecksSessionBeforeReservingAndIsSilent(t *testing.T) {
	ctx := context.Background()
	in, p := wakeupFixture()
	service := &presentationOnlyService{projection: p}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "wakeup", Version: "1"}, nil)
	registerReviewWakeupClaim(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(v reviewWakeupIdentity) (string, bool) {
		t.Helper()
		out, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "dev_bridge_review_wakeup_claim", Arguments: v})
		if err != nil {
			t.Fatal(err)
		}
		if out.IsError {
			return "", true
		}
		if out.Meta["emission"] != "SILENT" || out.Meta["openai/outputTemplate"] != nil {
			t.Fatal("not silent", out.Meta)
		}
		data, _ := json.Marshal(out.StructuredContent)
		var result reviewWakeupResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result.Status, false
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 || !strings.Contains(listed.Tools[0].Description, "after the Card has completed non-message preflight") {
		t.Fatalf("late claim contract missing: %v %v", listed, err)
	}
	// Even the exact identity cannot consume a reservation while Core says it is
	// ineligible. Once eligible, its first attempt must still receive CLAIMED.
	service.projection.LifecycleStatus = "RETIRING"
	if _, failed := call(in); !failed {
		t.Fatal("ineligible preflight reserved")
	}
	service.projection.LifecycleStatus = "ACTIVE"
	foreign := in
	foreign.AISessionID = "foreign"
	if _, failed := call(foreign); !failed {
		t.Fatal("foreign session reserved")
	}
	foreign = in
	foreign.ProjectID = "foreign"
	if _, failed := call(foreign); !failed {
		t.Fatal("foreign project reserved")
	}
	if status, failed := call(in); failed || status != "CLAIMED" {
		t.Fatal(status, failed)
	}
	if status, failed := call(in); failed || status != "ALREADY_ATTEMPTED" {
		t.Fatal(status, failed)
	}
	service.projection.LifecycleStatus = "RETIRING"
	if _, failed := call(in); !failed {
		t.Fatal("retirement reused stale eligibility")
	}
	// A restarted MCP ledger is intentionally distinct, while same-widget receipts
	// suppress automatic delivery across restart. This is not a durable outbox.
	fresh := &reviewWakeupClaims{attempted: make(map[reviewWakeupIdentity]struct{})}
	if status, err := fresh.reserve(in); err != nil || status.Status != "CLAIMED" {
		t.Fatal(status, err)
	}
}

func TestReviewWakeupConcurrentLegacyAndRunIdentityShareOneReservation(t *testing.T) {
	in, _ := wakeupFixture()
	ledger := &reviewWakeupClaims{attempted: make(map[reviewWakeupIdentity]struct{})}
	var claimed atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 32; i++ {
		candidate := in
		if i%2 == 0 {
			candidate.RunID = "verified-current-run"
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, err := ledger.reserve(candidate)
			if err != nil {
				t.Error(err)
				return
			}
			if value.Status == "CLAIMED" {
				claimed.Add(1)
			} else if value.Status != "ALREADY_ATTEMPTED" {
				t.Error(value.Status)
			}
		}()
	}
	wait.Wait()
	if claimed.Load() != 1 || len(ledger.attempted) != 1 {
		t.Fatalf("claimed=%d ledger=%d", claimed.Load(), len(ledger.attempted))
	}
}
