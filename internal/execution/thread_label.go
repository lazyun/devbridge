package execution

import (
	"strings"

	"github.com/lazyun/devbridge/internal/run"
)

// Human-readable engineering context only. Workflow routing IDs stay in Core's
// durable Thread/Run binding, not in the executor's conversation name.
func executorThreadLabel(title string, purpose run.Purpose) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "工程任务"
	}
	stage := "执行"
	switch purpose {
	case run.PurposeScopeReview:
		stage = "审查"
	case run.PurposeImplementation:
		stage = "实施"
	case run.PurposeVerification:
		stage = "验证"
	case run.PurposeInspection:
		stage = "探索"
	}
	return "Dev Bridge · " + stage + " · " + title
}
