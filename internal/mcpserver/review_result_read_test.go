package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
	"github.com/lazyun/devbridge/internal/workset"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type reviewResultReadFixture struct {
	*surfaceReadFixture
	source                               runanalysis.Source
	readError                            error
	upper                                review.StoredReport
	analysisReads, upperReads, mutations int
}

func naturalReviewReadFixture() *reviewResultReadFixture {
	f := &reviewResultReadFixture{surfaceReadFixture: newSurfaceReadFixture()}
	f.review = review.Review{ID: "review", RunID: "run", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", ExecutorKind: review.ExecutorCodingAgent, BaseCommit: "original-baseline"}
	f.run = run.Run{ID: "run", ReviewID: "review", TaskID: "task", ProjectID: "project", FeatureAreaID: "area", Purpose: run.PurposeScopeReview, Status: run.StatusSucceeded, ResultProtocol: run.NaturalResultProtocol, Result: &run.Result{AgentClaim: evidence.AgentClaim{Message: "结论：只读审查完成。这不是 JSON，不应重新解析。"}}}
	owner := f.review
	f.source = runanalysis.Source{Run: f.run, Kind: "REVIEW", State: "RECORDED", Review: &owner, SourceHash: strings.Repeat("b", 64), RecordedSourceHash: strings.Repeat("a", 64), Report: json.RawMessage(`{"version":1,"summary":"原 GPT 分析"}`), Receipt: &runanalysis.Receipt{RunID: "run", Kind: "REVIEW", RequestID: "original-submission"}, ReviewResult: &review.ImplementationResult{Version: 1, Summary: "原 GPT 分析", ReviewDisposition: review.DispositionFurtherReviewRequired}}
	return f
}
func (f *reviewResultReadFixture) GetRunAnalysis(_ context.Context, session, id string) (runanalysis.Source, error) {
	f.analysisReads++
	if session != "session" || id != "run" {
		return runanalysis.Source{}, errors.New("scope mismatch")
	}
	return f.source, f.readError
}
func (f *reviewResultReadFixture) GetReviewReportForSession(context.Context, string, string) (review.StoredReport, error) {
	f.upperReads++
	return f.upper, f.readError
}
func (f *reviewResultReadFixture) GetReviewResultReceiptForSession(_ context.Context, _, id, request string) (review.ReportReceipt, error) {
	f.upperReads++
	return review.ReportReceipt{ReviewID: id, RunID: f.upper.RunID, RequestID: request}, f.readError
}
func (f *reviewResultReadFixture) SubmitReviewResult(context.Context, review.SubmitInput) (review.ReportReceipt, error) {
	f.mutations++
	return review.ReportReceipt{}, errors.New("read must not submit")
}
func (f *reviewResultReadFixture) UpperReviewAccessForSession(context.Context, string, string) (review.UpperAccess, error) {
	return review.UpperAccess{}, errors.New("not needed")
}
func (f *reviewResultReadFixture) ListUpperReviewEvidenceForSession(context.Context, string, string) ([]review.FileEvidence, error) {
	return nil, errors.New("not needed")
}

func TestReviewResultReadNaturalReportAndOperationUseSameRecordedSource(t *testing.T) {
	f := naturalReviewReadFixture()
	read, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review"})
	if err != nil || read.State != "RECORDED" || read.Source != "GPT_RUN_ANALYSIS" || read.Report != nil || read.Analysis == nil || string(read.Analysis.Report) != string(f.source.Report) || read.Proposal != f.source.ReviewResult || read.AnalysisSourceHash != f.source.RecordedSourceHash {
		t.Fatalf("formal report: %+v %v", read, err)
	}
	out, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: "review", OwnerTaskID: "task", RunID: "run"}, Include: []string{"RESULT"}})
	if err != nil || out.Result.ReviewProposalSource != "GPT_RUN_ANALYSIS:run" || out.Result.ReviewSource.Summary != "原 GPT 分析" || out.Result.ReviewSource.SummaryStatus != "AVAILABLE" || out.Result.ReviewResult == nil || out.Result.ReviewResult.AnalysisSourceHash != read.AnalysisSourceHash {
		t.Fatalf("operation: %+v %v", out, err)
	}
	for _, partition := range out.Result.Partitions {
		if (partition.Name == "REVIEW_PROPOSAL" || partition.Name == "REVIEW_SUMMARY") && partition.ReadError != "" {
			t.Fatalf("native prose parsed as JSON: %+v", partition)
		}
	}
	if len(out.Result.AgentClaims) != 1 || out.Result.AgentClaims[0].Claim.Message != f.run.Result.AgentClaim.Message || f.upperReads != 0 || f.mutations != 0 {
		t.Fatal("read rewrote native text, queried wrong report storage or mutated state")
	}
	var wire any
	encoded, _ := json.Marshal(out)
	_ = json.Unmarshal(encoded, &wire)
	if err := resolveSchema(t, json.RawMessage(OperationGetOutputSchema)).Validate(wire); err != nil {
		t.Fatal(err)
	}
}

func TestReviewResultReadMissingAnalysisNeverPromotesNativeText(t *testing.T) {
	f := naturalReviewReadFixture()
	f.source.State = "REQUIRED"
	f.source.Report = nil
	f.source.Receipt = nil
	f.source.ReviewResult = nil
	f.source.RecordedSourceHash = ""
	read, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review"})
	if err != nil || read.State != "REQUIRED" || read.Analysis == nil || read.Proposal != nil || read.Report != nil {
		t.Fatalf("pending analysis: %+v %v", read, err)
	}
	out, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: "review", RunID: "run"}, Include: []string{"RESULT"}})
	if err != nil || out.Result.ReviewSource.SummaryStatus != "WAITING_ANALYSIS" || out.Result.ReviewProposal != nil {
		t.Fatalf("pending operation: %+v %v", out, err)
	}
	for _, part := range out.Result.Partitions {
		if (part.Name == "REVIEW_SUMMARY" || part.Name == "REVIEW_PROPOSAL") && part.ReadError != "" {
			t.Fatal(part.ReadError)
		}
	}
}

func TestReviewResultReadRejectsMismatchedAndUnreadableSources(t *testing.T) {
	for _, name := range []string{"project", "task", "review", "run", "baseline", "receipt", "purpose", "missing-proposal", "corrupt-report", "unavailable"} {
		t.Run(name, func(t *testing.T) {
			f := naturalReviewReadFixture()
			switch name {
			case "project":
				f.source.Run.ProjectID = "other"
			case "task":
				f.source.Run.TaskID = "other"
			case "review":
				f.source.Review.ID = "other"
			case "run":
				f.source.Run.ID = "other"
			case "baseline":
				f.source.Review.BaseCommit = "other"
			case "receipt":
				f.source.Receipt.RunID = "other"
			case "purpose":
				f.source.Run.Purpose = run.PurposeImplementation
			case "missing-proposal":
				f.source.ReviewResult = nil
			case "corrupt-report":
				f.source.Report = []byte("坏 JSON")
			case "unavailable":
				f.readError = errors.New("storage read failed")
			}
			if _, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review"}); err == nil {
				t.Fatal("invalid source accepted")
			}
			if f.upperReads != 0 || f.mutations != 0 {
				t.Fatal("read failure fell back to another authority")
			}
		})
	}
}

func TestReviewResultReadReceiptIsExactAndDoesNotInventUpperReceipt(t *testing.T) {
	f := naturalReviewReadFixture()
	for _, request := range []string{"original-submission", "wrong-submission"} {
		out, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review", RequestID: request})
		if request == "wrong-submission" {
			if !errors.Is(err, review.ErrNotFound) {
				t.Fatalf("wrong receipt: %v", err)
			}
			continue
		}
		if err != nil || out.Receipt != nil || out.Proposal != nil || out.Analysis == nil || out.Analysis.Receipt.RequestID != request || out.Analysis.Report != nil {
			t.Fatalf("receipt: %+v %v", out, err)
		}
	}
}

func TestReviewResultReadUpperAndStructuredLegacyRemainSeparate(t *testing.T) {
	f := naturalReviewReadFixture()
	f.review.ExecutorKind = review.ExecutorUpperLLM
	f.upper = review.StoredReport{ReviewID: "review", RunID: "run", Baseline: f.review.BaseCommit, Report: review.ReviewReport{SchemaVersion: 1, Summary: "GPT 直接审查", Disposition: review.DispositionBlocked}}
	out, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review"})
	if err != nil || out.Report == nil || out.Analysis != nil || out.Source != "UPPER_REVIEW_REPORT" || out.Proposal.Summary != "GPT 直接审查" || f.analysisReads != 0 {
		t.Fatalf("upper: %+v %v", out, err)
	}
	f = naturalReviewReadFixture()
	f.run.ResultProtocol = ""
	raw, _ := json.Marshal(f.source.ReviewResult)
	f.run.Result.AgentClaim.Message = string(raw)
	out, err = ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: "review"})
	if err != nil || out.Source != "VALIDATED_REVIEW_RESULT" || out.Proposal == nil || f.analysisReads != 0 {
		t.Fatalf("legacy: %+v %v", out, err)
	}
}

func TestReviewResultReadPublicMCPWireExposesNaturalAnalysis(t *testing.T) {
	f := naturalReviewReadFixture()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "review-read", Version: "1"}, nil)
	RegisterUpperReviewTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "review_result_get", Arguments: ReviewResultGetInput{AISessionID: "session", ReviewID: "review"}})
	if err != nil || result.IsError {
		t.Fatalf("wire: %+v %v", result, err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var decoded ReviewResultGetOutput
	if err = json.Unmarshal(raw, &decoded); err != nil || decoded.State != "RECORDED" || decoded.Analysis == nil || decoded.Proposal == nil || decoded.Report != nil {
		t.Fatalf("wire result %s %v", raw, err)
	}
}

func upperV2ResultReadFixture(t *testing.T) *reviewResultReadFixture {
	t.Helper()
	f := naturalReviewReadFixture()
	f.review.ExecutorKind = review.ExecutorUpperLLM
	f.review.BaseCommit = ""
	f.review.WorkspaceID = "workspace"
	f.review.RepositoryTargets = []workset.Target{
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/app", Read: true, Write: workset.WriteScope{Kind: "NONE"}},
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 1, OriginCommit: strings.Repeat("b", 40), OriginRef: "refs/heads/sdk", Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}
	baseline, err := (workset.Set{Version: workset.Version, WorkspaceID: "review:" + f.review.ID, Targets: f.review.RepositoryTargets}).Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	f.upper = review.StoredReport{ReviewID: f.review.ID, RunID: f.run.ID, Baseline: baseline, Report: review.ReviewReport{SchemaVersion: 2, Summary: "Frozen multi-member findings", Disposition: review.DispositionBlocked}}
	return f
}

func TestReviewResultReadUpperV2UsesAdmittedWorksetBaseline(t *testing.T) {
	f := upperV2ResultReadFixture(t)
	// Findings-only reports need not propose any implementation targets. Their
	// baseline still belongs to the Review's original immutable read set.
	out, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: f.review.ID})
	if err != nil || out.State != "RECORDED" || out.Source != "UPPER_REVIEW_REPORT" || out.Report == nil || out.Report.Baseline != f.upper.Baseline || out.Analysis != nil || f.analysisReads != 0 || f.mutations != 0 {
		t.Fatalf("v2 upper report: %+v %v", out, err)
	}
	operation, err := OperationGetCore(context.Background(), f, OperationGetInput{AISessionID: "session", Target: p.OperationTarget{Kind: "REVIEW", OperationID: f.review.ID, OwnerTaskID: f.review.TaskID, RunID: f.run.ID}, Include: []string{"RESULT"}})
	if err != nil || operation.Result.ReviewResult == nil || operation.Result.ReviewResult.Report == nil || operation.Result.ReviewResult.Report.Baseline != f.upper.Baseline || operation.Result.ReviewSource.SummaryStatus != "AVAILABLE" || f.analysisReads != 0 || f.mutations != 0 {
		t.Fatalf("v2 operation uses the same exact report: %+v %v", operation, err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "upper-v2-read", Version: "1"}, nil)
	RegisterUpperReviewTools(server, f)
	client := connectPresentationTest(t, context.Background(), server)
	result, err := client.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "review_result_get", Arguments: ReviewResultGetInput{AISessionID: "session", ReviewID: f.review.ID}})
	if err != nil || result.IsError {
		t.Fatalf("v2 public read: %+v %v", result, err)
	}
	for _, disposition := range []review.Disposition{review.DispositionFurtherReviewRequired, review.DispositionApprovalReady} {
		f.upper.Report.Disposition = disposition
		if disposition == review.DispositionApprovalReady {
			f.upper.Report.RepositoryTargets = workset.CloneTargets(f.review.RepositoryTargets)
			f.upper.Report.RepositoryTargets[0].Write = workset.WriteScope{Kind: "PATHS", Paths: []string{"same.txt"}}
		}
		out, err = ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: f.review.ID})
		if err != nil || out.Report == nil || out.Report.Baseline != f.upper.Baseline || out.Proposal.ReviewDisposition != disposition {
			t.Fatalf("%s must use admitted targets, not optional proposed writes: %+v %v", disposition, out, err)
		}
	}
}

func TestReviewResultReadUpperV2RejectsMismatchedBaselineAndOwner(t *testing.T) {
	for _, name := range []string{"empty-baseline", "foreign-baseline", "changed-origin", "changed-member", "invalid-admitted-target", "missing-admitted-targets", "missing-workspace", "mixed-scalar-repository", "mixed-scalar-baseline", "wrong-report-schema", "foreign-review", "foreign-run"} {
		t.Run(name, func(t *testing.T) {
			f := upperV2ResultReadFixture(t)
			switch name {
			case "empty-baseline":
				f.upper.Baseline = ""
			case "foreign-baseline":
				f.upper.Baseline = strings.Repeat("0", 64)
			case "changed-origin":
				f.review.RepositoryTargets[0].OriginCommit = strings.Repeat("c", 40)
			case "changed-member":
				f.review.RepositoryTargets[0].MemberID = "another-member"
			case "invalid-admitted-target":
				f.review.RepositoryTargets[0].OriginCommit = "invalid"
			case "missing-admitted-targets":
				f.review.RepositoryTargets = nil
			case "missing-workspace":
				f.review.WorkspaceID = ""
			case "mixed-scalar-repository":
				f.review.RepositoryID = "app"
			case "mixed-scalar-baseline":
				f.review.BaseCommit = f.upper.Baseline
			case "wrong-report-schema":
				f.upper.Report.SchemaVersion = 1
			case "foreign-review":
				f.upper.ReviewID = "other-review"
			case "foreign-run":
				f.upper.RunID = "other-run"
			}
			out, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: f.review.ID})
			if err == nil || out.Report != nil || out.Proposal != nil || f.mutations != 0 || f.analysisReads != 0 {
				t.Fatalf("invalid v2 report source accepted: %+v %v", out, err)
			}
		})
	}
}

func TestReviewResultReadUpperV1RetainsScalarBaselineFence(t *testing.T) {
	for _, invalid := range []string{"baseline", "mixed-workspace", "schema"} {
		f := naturalReviewReadFixture()
		f.review.ExecutorKind = review.ExecutorUpperLLM
		f.upper = review.StoredReport{ReviewID: f.review.ID, RunID: f.run.ID, Baseline: f.review.BaseCommit, Report: review.ReviewReport{SchemaVersion: 1, Summary: "Legacy findings", Disposition: review.DispositionBlocked}}
		switch invalid {
		case "baseline":
			f.upper.Baseline = "wrong-scalar"
		case "mixed-workspace":
			f.review.WorkspaceID = "workspace"
		case "schema":
			f.upper.Report.SchemaVersion = 2
		}
		if out, err := ReviewResultGetCore(context.Background(), f, ReviewResultGetInput{AISessionID: "session", ReviewID: f.review.ID}); err == nil || out.Report != nil {
			t.Fatalf("v1 %s mismatch accepted: %+v %v", invalid, out, err)
		}
	}
}
