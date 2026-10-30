package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/observability"
)

func TestReviewCommandLogIsNeutralObservationWithoutSemanticVerdict(t *testing.T) {
	commands := []string{
		`rg -n -g '*.go' -g '!*_test.go' 'CreateReview|FinalCodexPrompt|PromptSHA256|ReviewEvidence|reviewId' .`,
		`rg -n needle -g '!test/**' -g '!tests/**' -g '*.spec.*' .`,
		"go test ./...",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			var output bytes.Buffer
			ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
			ctx = observability.WithContext(ctx, observability.Correlation{RunID: "run-review"})
			logReviewCommand(ctx, command)
			var entry map[string]any
			if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry["event"] != "codex.command.observed" || entry["runId"] != "run-review" {
				t.Fatalf("neutral command diagnostic=%#v", entry)
			}
			encoded := output.String()
			for _, forbidden := range []string{"REVIEW_COMMAND_FORBIDDEN", `"decision"`, "classifierReason", "selectorInterpretation"} {
				if strings.Contains(encoded, forbidden) {
					t.Fatalf("semantic verdict %q leaked into neutral observation: %s", forbidden, encoded)
				}
			}
		})
	}
}
