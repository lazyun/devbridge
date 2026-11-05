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

func TestReviewCommandLogRedactsBasicAuthorization(t *testing.T) {
	const credential = "ZmFrZS11c2VyOmZha2UtcGFzc3dvcmQ=" // fake-user:fake-password
	command := "curl -H 'Authorization: Basic " + credential + "' https://example.test/status"
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	ctx = observability.WithContext(ctx, observability.Correlation{RunID: "run-review"})
	logReviewCommand(ctx, command)
	if strings.Contains(output.String(), credential) {
		t.Fatalf("Review command diagnostic leaked Basic credential: %s", output.String())
	}
	var entry struct {
		Event string `json:"event"`
		RunID string `json:"runId"`
		Data  struct {
			Command     string         `json:"command"`
			Fingerprint map[string]any `json:"commandFingerprint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Event != "codex.command.observed" || entry.RunID != "run-review" || !strings.Contains(entry.Data.Command, "curl") || !strings.Contains(entry.Data.Command, "[redacted]") {
		t.Fatalf("Review command diagnostic shape lost: %s", output.String())
	}
	if entry.Data.Fingerprint["sha256"] != observability.Fingerprint(command)["sha256"] {
		t.Fatalf("command fingerprint changed: %#v", entry.Data.Fingerprint)
	}
}
