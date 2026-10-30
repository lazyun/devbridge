package controlapi

import (
	"context"
	"errors"
	"github.com/lazyun/devbridge/internal/execution"

	"github.com/lazyun/devbridge/internal/localoperator"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

func (f *Facade) GetRunAnalysis(ctx context.Context, session, id string) (runanalysis.Source, error) {
	if f == nil || f.deps.RunAnalyses == nil {
		return runanalysis.Source{}, MapError(errors.New("Run analysis service unavailable"))
	}
	if _, _, err := f.scopedRun(ctx, session, id); err != nil {
		return runanalysis.Source{}, err
	}
	out, err := f.deps.RunAnalyses.Get(ctx, session, id)
	if err == nil {
		execution.DescribeRunAnalysis(&out)
	}
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}

func (f *Facade) SubmitRunAnalysis(ctx context.Context, in runanalysis.Input) (runanalysis.Receipt, error) {
	if f == nil || f.deps.RunAnalyses == nil {
		return runanalysis.Receipt{}, MapError(errors.New("Run analysis service unavailable"))
	}
	if _, _, err := f.scopedRun(ctx, in.AISessionID, in.RunID); err != nil {
		return runanalysis.Receipt{}, err
	}
	out, err := f.deps.RunAnalyses.Submit(ctx, in)
	if err == nil && out.Kind == "RECOVERY" {
		if source, e := f.deps.RunAnalyses.Get(ctx, in.AISessionID, in.RunID); e == nil && source.Recovery != nil {
			if resumed, ok := f.deps.RecoveryStatus.(interface{ ResumeAnalysis(context.Context, string) }); ok {
				resumed.ResumeAnalysis(ctx, source.Recovery.ID)
			}
		}
	}
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}

// RecordedRunAnalysisForOperator exposes only the original persisted GPT report.
func (f *Facade) RecordedRunAnalysisForOperator(ctx context.Context, project, id string) (runanalysis.RecordedReport, error) {
	ctx, err := localoperator.BindProject(ctx, project)
	if err != nil {
		return runanalysis.RecordedReport{}, MapError(err)
	}
	if f == nil || f.deps.RunAnalyses == nil {
		return runanalysis.RecordedReport{}, errors.New("Run analysis reader unavailable")
	}
	reader, ok := f.deps.RunAnalyses.Store.(interface {
		RecordedForOperator(context.Context, string, string) (runanalysis.RecordedReport, error)
	})
	if !ok {
		return runanalysis.RecordedReport{}, errors.New("Run analysis reader unavailable")
	}
	out, err := reader.RecordedForOperator(ctx, project, id)
	if err != nil {
		return out, MapError(err)
	}
	return out, nil
}
