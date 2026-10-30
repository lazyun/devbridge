package controlapi_test

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"testing"
)

func TestConfirmationOptionsCreationContinuityIsReadOnly(t *testing.T) {
	f := newCompositionFixture(t)
	input := f.taskPrepare("first", compositionUpperVariant("upper", "Read", "Read only and report."))
	first, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	created, err := f.facade.CommitConfirmation(f.ctx, taskReviewCommit(first, "create-first", taskReviewBool(false)))
	if err != nil {
		t.Fatal(err)
	}
	taskID := compositionStep(t, created, "CREATE_TASK").TaskID
	input.RequestID = "second"
	second, err := f.facade.PrepareConfirmation(f.ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	before := c.ValueHash(second)
	options, err := f.facade.ConfirmationOptions(f.ctx, taskReviewOptions(second, taskReviewBool(false)))
	if err != nil {
		t.Fatal(err)
	}
	fact := options.TaskContinuity
	if fact == nil || fact.Availability != "AVAILABLE" || fact.ProjectID != f.projectID || fact.FeatureAreaID != f.areaID || fact.Total != 1 || len(fact.Tasks) != 1 || fact.Tasks[0].TaskID != taskID || fact.Tasks[0].Title == "" || fact.Tasks[0].Summary == "" {
		t.Fatalf("missing exact continuation: %+v", fact)
	}
	read, err := f.read(second, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.ValueHash(read) != before {
		t.Fatal("continuation read changed prepared intent")
	}
	if tasks, reviews, runs := f.counts(t); tasks != 1 || reviews != 0 || runs != 0 {
		t.Fatalf("read changed workflow: %d/%d/%d", tasks, reviews, runs)
	}
}
