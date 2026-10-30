package execution

import (
	"context"

	"github.com/lazyun/devbridge/internal/observability"
)

// logReviewCommand records bounded diagnostics only. Command text is never
// classified and this event is not an authorization input.
func logReviewCommand(ctx context.Context, command string) {
	observability.Info(ctx, "review", "codex.command.observed", "Codex command observed during Review", map[string]any{
		"command": observability.Bound(command, 2048), "commandFingerprint": observability.Fingerprint(command),
		"truncated": len(command) > 2048,
	})
}
