package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestTopicCreateCoreRejectsNestedStartupOutsideComposition(t *testing.T) {
	service, err := NewStaticService(BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []externalTopicCreateInput{
		{AISessionID: "session", Title: "Area", ProblemStatement: "Record only.", InitialTask: &externalInitialTaskInput{Title: "Task", ProblemStatement: "Work", RepositoryID: "repository"}},
		{AISessionID: "session", Title: "Area", ProblemStatement: "Record only.", Mode: "CREATE_ONLY", InitialReview: &externalInitialReviewInput{ReviewDepth: "FAST", ReviewInstruction: "Inspect.", Model: "gpt-5.6-terra"}},
	} {
		_, callErr := TopicCreateCore(context.Background(), service, input)
		if callErr == nil || !strings.Contains(callErr.Error(), "CREATE_ONLY rejects initialTask and initialReview") {
			t.Fatalf("direct Core accepted or hid nested startup input: %+v err=%v", input, callErr)
		}
	}
}
