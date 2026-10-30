package mcpserver

import (
	"context"
	"strings"
	"testing"
)

func TestReviewEmptyInstructionRejectsBeforeServiceOrRun(t *testing.T) {
	for _, instruction := range []string{"", " \n\t"} {
		_, err := StartTopicReviewCore(context.Background(), nil, ExternalReviewInput{ReviewDepth: "DEEP", ReviewInstruction: instruction})
		if err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
			t.Fatalf("empty instruction: %v", err)
		}
	}
}
