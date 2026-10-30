package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

// Both public reads use the exact recorded executor/protocol. Native prose,
// GPT analysis and an Upper report remain separate immutable sources.
func ReviewResultGetCore(ctx context.Context, s BridgeService, in ReviewResultGetInput) (ReviewResultGetOutput, error) {
	session, err := surfaceSession(ctx, s, in.AISessionID)
	if err != nil {
		return ReviewResultGetOutput{}, err
	}
	v, err := s.GetReview(ctx, in.AISessionID, in.ReviewID)
	if err != nil {
		return ReviewResultGetOutput{}, err
	}
	if v.ID != in.ReviewID || v.ProjectID != session.Project.ID {
		return ReviewResultGetOutput{}, errors.New("Review Project mismatch")
	}
	if v.RunID == "" {
		if in.RequestID != "" {
			return ReviewResultGetOutput{}, review.ErrNotFound
		}
		return ReviewResultGetOutput{ReviewID: v.ID, State: "NOT_STARTED"}, nil
	}
	r, err := s.GetRunForSession(ctx, in.AISessionID, v.RunID)
	if err != nil {
		return ReviewResultGetOutput{}, err
	}
	return readExactReviewResult(ctx, s, in, v, r)
}

func readExactReviewResult(ctx context.Context, s BridgeService, in ReviewResultGetInput, v review.Review, r run.Run) (ReviewResultGetOutput, error) {
	out := ReviewResultGetOutput{ReviewID: v.ID, RunID: r.ID, ResultProtocol: r.ResultProtocol, State: "UNAVAILABLE"}
	if !v.ExecutorKind.Effective().Valid() {
		return out, review.ErrExecutorMismatch
	}
	if v.ID != in.ReviewID || r.ID == "" || r.ID != v.RunID || r.ReviewID != v.ID || r.TaskID != v.TaskID || r.ProjectID != v.ProjectID || r.FeatureAreaID != v.FeatureAreaID || r.Purpose != run.PurposeScopeReview ||
		(r.OperationKind != "" && r.OperationKind != run.OperationReview) || (r.OperationID != "" && r.OperationID != v.ID) {
		return out, errors.New("Exact Review result identity mismatch")
	}
	if v.ExecutorKind.Effective() == review.ExecutorUpperLLM {
		p, ok := s.(upperReviewService)
		if !ok {
			return out, errors.New("UPPER_LLM Review result service unavailable")
		}
		out.Source = "UPPER_REVIEW_REPORT"
		if in.RequestID != "" {
			receipt, err := p.GetReviewResultReceiptForSession(ctx, in.AISessionID, v.ID, in.RequestID)
			if err != nil {
				return out, err
			}
			if receipt.ReviewID != v.ID || receipt.RunID != r.ID || receipt.RequestID != in.RequestID {
				return out, errors.New("Review receipt identity mismatch")
			}
			out.Receipt, out.State = &receipt, "RECORDED"
			return out, nil
		}
		report, err := p.GetReviewReportForSession(ctx, in.AISessionID, v.ID)
		if err != nil {
			return out, err
		}
		if report.ReviewID != v.ID || report.RunID != r.ID || report.Baseline != v.BaseCommit {
			return out, errors.New("Review report identity mismatch")
		}
		proposal := report.Report.CompatibilityResult()
		out.Report, out.Proposal, out.State = &report, &proposal, "RECORDED"
		return out, nil
	}
	if r.ResultProtocol == run.NaturalResultProtocol {
		p, ok := s.(interface {
			GetRunAnalysis(context.Context, string, string) (runanalysis.Source, error)
		})
		if !ok {
			return out, errors.New("Review Run analysis reader unavailable")
		}
		source, err := p.GetRunAnalysis(ctx, in.AISessionID, r.ID)
		if err != nil {
			return out, err
		}
		if source.Kind != "REVIEW" || source.Run.ID != r.ID || source.Run.ReviewID != v.ID || source.Run.TaskID != r.TaskID || source.Run.ProjectID != r.ProjectID || source.Run.FeatureAreaID != r.FeatureAreaID || source.Run.Purpose != r.Purpose || source.Run.ResultProtocol != run.NaturalResultProtocol || source.Review == nil || source.Review.ID != v.ID || source.Review.RunID != r.ID || source.Review.ProjectID != r.ProjectID || source.Review.TaskID != r.TaskID || source.Review.FeatureAreaID != r.FeatureAreaID || source.Review.BaseCommit != v.BaseCommit {
			return out, errors.New("Recorded Review analysis identity mismatch")
		}
		out.State, out.Source = source.State, "GPT_RUN_ANALYSIS"
		out.Analysis = &runanalysis.RecordedReport{State: source.State}
		if source.State != "RECORDED" {
			if in.RequestID != "" {
				return out, review.ErrNotFound
			}
			return out, nil
		}
		if source.Receipt == nil || source.Receipt.RunID != r.ID || source.Receipt.Kind != "REVIEW" || source.ReviewResult == nil || !json.Valid(source.Report) || len(source.RecordedSourceHash) != 64 {
			return out, errors.New("Recorded Review analysis is incomplete")
		}
		out.Analysis.Receipt = source.Receipt
		if in.RequestID != "" {
			if source.Receipt.RequestID != in.RequestID {
				return out, review.ErrNotFound
			}
			return out, nil
		}
		out.Analysis.Report, out.Proposal, out.AnalysisSourceHash = source.Report, source.ReviewResult, source.RecordedSourceHash
		return out, nil
	}
	if in.RequestID != "" {
		return out, review.ErrNotFound
	}
	if r.Status != run.StatusSucceeded || r.Result == nil {
		return out, nil
	}
	proposal, err := review.ParseResult(r.Result.AgentClaim.Message)
	if err != nil {
		return out, err
	}
	out.Proposal, out.Source, out.State = &proposal, "VALIDATED_REVIEW_RESULT", "RECORDED"
	return out, nil
}
