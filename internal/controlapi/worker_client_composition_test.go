package controlapi_test

// This composes the two host-neutral clients through the production GPT HTTP
// middleware. Confirmation calls below represent an explicit SIMULATED USER,
// not a model/worker authority and not qualification of the confirmation UI.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/fileworker"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/upperworker"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func compositionGPTClient(t *testing.T, f *upperLifecycleFixture) (context.Context, *sdkmcp.ClientSession) {
	t.Helper()
	service, err := mcpserver.NewService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "composition-test", Ready: true}, f.plane.AIContext, f.facade)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := mcpserver.New(mcpserver.Config{}, service)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(adapter.Handler())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "trusted-test-consumer-not-dot", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: server.URL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 20 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		server.Close()
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close(); server.Close(); cancel() })
	return ctx, session
}

func compositionEffects(t *testing.T, f *upperLifecycleFixture, grants, claims, mutations, results int) {
	t.Helper()
	for table, want := range map[string]int{"direct_file_task_authorizations": grants, "direct_file_worker_claims": claims, "direct_file_operation_receipts": mutations, "direct_file_implementation_results": results, "direct_file_implementation_acceptances": 0, "execution_proposals": 0, "task_commit_receipts": 0} {
		if got := fileClientCount(t, f, table); got != want {
			t.Fatalf("automatic advancement: %s=%d want%d", table, got, want)
		}
	}
	var runs int
	if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE task_id=?`, f.task).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("invented implementation/verification Run: %d %v", runs, err)
	}
	fileClientNoAcceptance(t, f)
}

func TestWorkerClientsComposeThroughProductionGPTHTTP(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	ctx, session := compositionGPTClient(t, f)
	before := upperClientTrees(t, f)
	compositionEffects(t, f, 0, 0, 0, 0)
	upperRequest := upperClientRequest(f)
	upperRequest.Claim.RequestID = "composition-review-delivery"
	upperRequest.Claim.WorkerID = "trusted-review-callback-not-dot"
	upperRequest.ReportRequestID = "composition-review-report"
	upperWire := &upperClientWire{ClientSession: session}
	upperCallbacks := 0
	var authored review.ReviewReport
	upper, err := upperworker.New(upperWire, upperRequest, func(ctx context.Context, d upperworker.Delivery, files upperworker.Files) (review.ReviewReport, error) {
		upperCallbacks++
		// This helper performs actual bounded List/Read for both members and uses
		// returned Core evidence, never seeded or client-invented evidence IDs.
		report := upperClientReadReport(t, f, ctx, d, files)
		targets := workset.CloneTargets(f.targets)
		checks := []task.VerificationCheck{}
		for i, target := range targets {
			targets[i].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
			checks = append(checks, task.VerificationCheck{Kind: task.VerificationCheckTestCommand, Command: "test -f same.txt", Required: true, RepositoryID: target.RepositoryID, RepositoryIDs: []string{target.RepositoryID}, WorkingDirectory: "."})
		}
		report.Summary = "Propose exact file updates after reviewing both real members"
		report.Disposition = review.DispositionApprovalReady
		report.RepositoryTargets = targets
		report.RecommendedNextSteps = []string{"Explicit user approval and independent file start required"}
		report.WorkSpec = &task.WorkSpec{ImplementationMode: task.ImplementationDirectFile, Goal: "Update both reviewed members", Constraints: []string{"preserve unrelated bytes"}, CodexInstruction: "Update same.txt in each approved member.\nPreserve this exact instruction.\n", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
		report.VerificationSpec = &task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "both members", Checks: checks, AcceptanceCriteria: []string{"both pass"}, CodexInstruction: "Run both checks"}
		authored = report
		return report, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err := upper.Run(ctx)
	if err != nil || reviewed.State != upperworker.ResultRecorded || reviewed.ReportReceipt == nil || reviewed.ReportInput == nil || upperCallbacks != 1 {
		t.Fatalf("UPPER client %+v %v calls=%v", reviewed, err, upperWire.calls)
	}
	if !reflect.DeepEqual(reviewed.ReportInput.Report, authored) || reviewed.ReportInput.WorkerClaim == nil || *reviewed.ReportInput.WorkerClaim != upperRequest.Claim.WorkerClaimIdentity {
		t.Fatal("UPPER client changed authored report")
	}
	if fileClientCount(t, f, "upper_review_worker_claims") != 1 || fileClientCount(t, f, "review_reports") != 1 {
		t.Fatal("duplicate review claim/report")
	}
	var originalReportJSON, originalReportHash string
	if err := f.plane.DB.SQL().QueryRow(`SELECT report_json,payload_sha256 FROM review_reports WHERE id=? AND review_id=? AND ai_session_id=?`, reviewed.ReportReceipt.ReportID, f.review, f.session).Scan(&originalReportJSON, &originalReportHash); err != nil {
		t.Fatal(err)
	}
	assertOriginalReport := func() {
		t.Helper()
		var raw, hash string
		if err := f.plane.DB.SQL().QueryRow(`SELECT report_json,payload_sha256 FROM review_reports WHERE id=? AND review_id=? AND ai_session_id=?`, reviewed.ReportReceipt.ReportID, f.review, f.session).Scan(&raw, &hash); err != nil {
			t.Fatal(err)
		}
		var original review.ReviewReport
		if err := json.Unmarshal([]byte(raw), &original); err != nil {
			t.Fatal(err)
		}
		if raw != originalReportJSON || hash != originalReportHash || hash != reviewed.ReportReceipt.PayloadSHA256 || original.VerificationSpec == nil || original.VerificationSpec.Mode != task.VerificationModeRun || !reflect.DeepEqual(original, authored) || !reflect.DeepEqual(reviewed.ReportInput.Report, authored) {
			t.Fatal("explicit user waiver rewrote original RUN review report")
		}
	}
	assertOriginalReport()
	compositionEffects(t, f, 0, 0, 0, 0)
	if !reflect.DeepEqual(before, upperClientTrees(t, f)) {
		t.Fatal("UPPER report mutated sources")
	}

	// SIMULATED USER: approve the actual original report, explicitly waive RUN.
	// Preparing or approving a proposal must not itself grant file authority.
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "composition-prepare-approval", Purpose: c.Execution, Action: "APPROVE", Execution: exactReviewApprovalBody(t, f.ctx, f.facade, f.task, f.area, f.review, "APPROVE"), Variants: []c.Variant{{ID: "direct", Summary: "Explicit approval without start", ImplementationMode: task.ImplementationDirectFile, RepositoryTargets: authored.RepositoryTargets, WorkSpec: authored.WorkSpec, VerificationSpec: authored.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	compositionEffects(t, f, 0, 0, 0, 0)
	waiver := task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "SIMULATED USER explicitly waives RUN only for this isolated composition test"}
	approved, err := f.facade.CommitConfirmation(f.ctx, c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "composition-approve", ContentHash: prepared.ContentHash, VariantID: "direct", Selection: c.Selection{VerificationWaiver: &waiver}})
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("approval %+v %v", approved, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != f.task || !reflect.DeepEqual(current.WorkSpec, *authored.WorkSpec) || !reflect.DeepEqual(current.RepositoryTargets, authored.RepositoryTargets) || current.VerificationSpec.Mode != task.VerificationModeNone || current.VerificationSpec.Waiver == nil || *current.VerificationSpec.Waiver != waiver || authored.VerificationSpec.Mode != task.VerificationModeRun {
		t.Fatalf("approval lost engineering context or explicit waiver: %+v", current)
	}
	assertOriginalReport()
	compositionEffects(t, f, 0, 0, 0, 0)
	if !reflect.DeepEqual(before, upperClientTrees(t, f)) {
		t.Fatal("approval mutated source/managed bytes")
	}

	// SIMULATED USER: a separate START confirmation consumes the approved
	// revision. The implementer did not prepare, approve, enable or start it.
	started, err := directConfirmation(t, f, current, "START_DIRECT_FILE", directfile.State{})
	if err != nil || started.State != "COMPLETED" {
		t.Fatalf("independent start %+v %v", started, err)
	}
	grant, err := f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || !grant.Enabled || grant.State != "ACTIVE" || grant.ProtocolVersion != 2 || grant.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("grant %+v %v", grant, err)
	}
	compositionEffects(t, f, 1, 0, 0, 0)
	if !reflect.DeepEqual(before, upperClientTrees(t, f)) {
		t.Fatal("START changed source/managed bytes")
	}
	req := fileworker.Request{Claim: directfile.WorkerClaimInput{WorkerTarget: directfile.WorkerTarget{AISessionID: f.session, FeatureAreaID: f.area, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, DecisionID: grant.DecisionID, WorkflowID: grant.WorkflowID, ProtocolVersion: grant.ProtocolVersion, RuntimeEpoch: grant.RuntimeEpoch, GrantGeneration: grant.GrantGeneration}, WorkerClaimIdentity: directfile.WorkerClaimIdentity{RequestID: "composition-file-delivery", WorkerID: "trusted-file-callback-not-dot"}}, ResultRequestID: "composition-file-result", VerificationMode: current.VerificationSpec.Mode}
	fileWire := fileClientTransport(t, session, req, current)
	fileCallbacks := 0
	implementer, err := fileworker.New(fileWire, req, func(ctx context.Context, d fileworker.Delivery, files fileworker.Files) (fileworker.Completion, error) {
		fileCallbacks++
		if d.Receipt.WorkerTarget != req.Claim.WorkerTarget || d.Receipt.WorkerClaimIdentity != req.Claim.WorkerClaimIdentity || !reflect.DeepEqual(d.Payload.Definition.RepositoryTargets, authored.RepositoryTargets) || d.Payload.Instruction != authored.WorkSpec.CodexInstruction || !reflect.DeepEqual(d.Payload.Definition.WorkSpec, *authored.WorkSpec) || d.Payload.Verification.Mode != task.VerificationModeNone || !reflect.DeepEqual(d.Payload.Verification.Waiver, &waiver) {
			t.Fatalf("file delivery lost frozen approved context %+v", d)
		}

		// TEST-ONLY negative probe, not fileclient behavior: the actual UPPER
		// identity cannot be transferred into this genuinely enrolled file grant.
		member := current.RepositoryTargets[0]
		source, err := files.Read(ctx, member.MemberID, "same.txt")
		if err != nil {
			t.Fatal(err)
		}
		unchanged := upperClientTrees(t, f)
		bad := directInput(f, grant, member)
		bad.RequestID = "composition-cross-channel-probe"
		bad.ExpectedDigest = source.Digest
		bad.Content = "forbidden transferred review claim\n"
		bad.WorkerClaim = &directfile.WorkerClaimIdentity{RequestID: upperRequest.Claim.RequestID, WorkerID: upperRequest.Claim.WorkerID}
		args := mcpObject(t, bad)
		args["action"] = "REPLACE"
		args["protocolVersion"] = 2
		negativeTables := []string{"upper_review_worker_claims", "review_reports", "direct_file_worker_claims", "direct_file_task_authorizations", "direct_file_task_members", "direct_file_operation_receipts", "direct_file_implementation_results", "direct_file_implementation_acceptances", "confirmation_commit_requests", "task_action_heads"}
		negativeLedger := definitionFormalSnapshot(t, f, negativeTables)
		rejected, callErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "file_write", Arguments: args})
		if callErr != nil || rejected == nil || !rejected.IsError {
			t.Fatalf("cross-channel probe did not reach a Core refusal: response=%+v err=%v", rejected, callErr)
		}
		raw, marshalErr := json.Marshal(rejected.StructuredContent)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(raw, &failure); err != nil {
			t.Fatal(err)
		}
		// Exact existing sqlite.fileWorkerConflict -> directFileStructuredError
		// contract: a transport/schema error cannot satisfy this assertion.
		if failure.Code != directfile.NotEnabled || failure.Message != "exact file worker identity or authority is unavailable" {
			t.Fatalf("probe failed for a reason other than worker identity: %s", raw)
		}
		if definitionFormalSnapshot(t, f, negativeTables) != negativeLedger {
			t.Fatal("worker-identity refusal changed durable authority or receipt rows")
		}
		compositionEffects(t, f, 1, 1, 0, 0)
		if !reflect.DeepEqual(unchanged, upperClientTrees(t, f)) {
			t.Fatal("cross-channel claim modified actual files")
		}
		fileClientReplaceBoth(t, f, current, ctx, d, files)
		return fileworker.Completion{Summary: "Changed both originally reviewed members once through Core", VerificationNotes: "Explicit simulated user NONE waiver; no command execution claimed"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := implementer.Run(ctx)
	if err != nil || result.State != fileworker.ResultRecorded || result.ResultReceipt == nil || fileCallbacks != 1 {
		t.Fatalf("file client %+v %v calls=%v", result, err, fileWire.calls)
	}
	fileClientSubmissionProof(t, f, req, result.ResultInput)
	receipt := result.ResultReceipt
	if receipt.Accepted || receipt.ExecutionID != "" || receipt.VerificationRunID != "" || receipt.VerificationStatus != "NOT_RUN" || receipt.TaskID != f.task || receipt.AISessionID != f.session || receipt.TaskDefinitionRevisionID != current.CurrentDefinitionRevisionID || len(receipt.Members) != 2 {
		t.Fatalf("invented execution or changed identity %+v", receipt)
	}
	compositionEffects(t, f, 1, 1, 2, 1)
	grant, err = f.facade.DirectFileTaskStatus(f.ctx, f.session, f.task)
	if err != nil || grant.Enabled || grant.State != "CLOSED" {
		t.Fatalf("result left grant active %+v %v", grant, err)
	}
	if reviewed.ClaimReceipt.ClaimID == result.ClaimReceipt.ClaimID || upperRequest.Claim.RequestID == req.Claim.RequestID || upperRequest.Claim.WorkerID == req.Claim.WorkerID {
		t.Fatal("claim namespaces conflated")
	}
	if upperWire.count("review_worker_claim") != 1 || upperWire.count("review_result_submit") != 1 || fileWire.count("file_worker_claim") != 1 || fileWire.count("file_write") != 2 || fileWire.count("file_implementation_result_submit") != 1 {
		t.Fatal("duplicate client dispatch")
	}

	// Original historical review remains its own record after implementation.
	tables := []string{"upper_review_worker_claims", "review_reports", "direct_file_worker_claims", "direct_file_task_authorizations", "direct_file_operation_receipts", "direct_file_implementation_results", "confirmation_commit_requests", "task_action_heads"}
	ledger := definitionFormalSnapshot(t, f, tables)
	observed, err := upper.Observe(ctx)
	if err != nil || observed.State != upperworker.ResultRecorded || !reflect.DeepEqual(observed.ReportReceipt, reviewed.ReportReceipt) || !reflect.DeepEqual(observed.ClaimReceipt, reviewed.ClaimReceipt) || upperCallbacks != 1 {
		t.Fatalf("history borrowed file outcome %+v %v", observed, err)
	}
	historical := callUpperMCP(t, ctx, session, "review_worker_get", mcpObject(t, upperRequest.Claim))["result"].(map[string]any)
	if historical["executionPayload"] != nil || historical["state"] != review.WorkerResultRecorded {
		t.Fatalf("historical review redispatched %+v", historical)
	}
	if definitionFormalSnapshot(t, f, tables) != ledger {
		t.Fatal("historical read advanced or changed durable work")
	}
	assertOriginalReport()
	compositionEffects(t, f, 1, 1, 2, 1)
	after := upperClientTrees(t, f)
	for key, old := range before {
		now := after[key]
		if strings.HasPrefix(key, "source:") {
			if now != old {
				t.Fatalf("source/unrelated repo changed %s", key)
			}
		} else if now.HEAD != old.HEAD || now.Status != " M same.txt\n" || (now.Content != "coded app\n" && now.Content != "coded sdk\n") {
			t.Fatalf("unexpected managed changes %s %+v", key, now)
		}
	}
}
