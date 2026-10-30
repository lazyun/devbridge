package controlapi_test

import (
	"bytes"
	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"testing"
)

func TestRecordedRunAnalysisConsolePreservesOriginalSourcesAndScope(t *testing.T) {
	withDefinitionOnlyCodingReview(t, func(f *upperLifecycleFixture, current task.Task, reviewed review.Review) {
		source, err := f.facade.GetRunAnalysis(f.ctx, f.session, reviewed.RunID)
		if err != nil || source.State != "RECORDED" {
			t.Fatalf("original analysis: %+v %v", source, err)
		}
		reader, err := consoleapi.New(f.plane, f.facade)
		if err != nil {
			t.Fatal(err)
		}
		ctx := operatorActionContext(t, f.project, current.ID, "analysis-console-read", "START_EXECUTION")
		tables := definitionFormalEveryTable(t, f)
		before := definitionFormalSnapshot(t, f, tables)
		detail, err := reader.GetRunForTask(ctx, f.project, current.ID, reviewed.RunID)
		if err != nil || detail.AnalysisReadError != "" || detail.Analysis == nil || detail.Analysis.State != "RECORDED" || !bytes.Equal(detail.Analysis.Report, source.Report) || *detail.Analysis.Receipt != *source.Receipt {
			t.Fatalf("Console lost GPT report: %+v %v", detail, err)
		}
		if detail.NativeOutput != source.Run.Result.AgentClaim.Message {
			t.Fatal("GPT report replaced the native result")
		}
		full, err := reader.GetProductReviewResult(ctx, f.project, reviewed.ID)
		if err != nil || full.Analysis == nil || !bytes.Equal(full.Analysis.Report, source.Report) || full.Result != source.Run.Result.AgentClaim.Message {
			t.Fatalf("Review full result: %+v %v", full, err)
		}
		if _, err = f.facade.RecordedRunAnalysisForOperator(f.ctx, f.project, reviewed.RunID); err == nil {
			t.Fatal("AI Session used as local operator authority")
		}
		if _, err = f.facade.RecordedRunAnalysisForOperator(ctx, "another-project", reviewed.RunID); err == nil {
			t.Fatal("cross-project report leaked")
		}
		if _, err = reader.GetRunForTask(ctx, f.project, "another-task", reviewed.RunID); err == nil {
			t.Fatal("cross-task report leaked")
		}
		assertDefinitionFormalSnapshot(t, f, tables, before, "Console report reads")
	})
}
