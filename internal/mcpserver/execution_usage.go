package mcpserver

import (
	"github.com/lazyun/devbridge/internal/codexusage"
	"github.com/lazyun/devbridge/internal/run"
)

// An Execution total is exact only when every contributing Run is exact.
// Missing or partial data must never become an AVAILABLE zero or partial sum.
func aggregateExecutionUsage(runs []run.Run) TokenUsageOutput {
	result := TokenUsageOutput{Status: string(run.TokenUsageUnavailable)}
	if len(runs) == 0 {
		return result
	}
	var total codexusage.Counts
	pending := false
	seen := make(map[string]struct{})
	for _, value := range runs {
		if value.ID != "" {
			if _, duplicate := seen[value.ID]; duplicate {
				continue
			}
			seen[value.ID] = struct{}{}
		}
		switch value.TokenUsage.Status {
		case run.TokenUsagePending:
			if value.Status == run.StatusSucceeded || value.Status == run.StatusFailed || value.Status == run.StatusInfrastructureFailed || value.Status == run.StatusCanceled {
				return result
			}
			pending = true
		case run.TokenUsageAvailable:
			if value.TokenUsage.Usage == nil {
				return result
			}
			sum, ok := codexusage.Add(total, codexusage.Counts(*value.TokenUsage.Usage))
			if !ok {
				return result
			}
			total = sum
		default:
			return result
		}
	}
	if pending {
		result.Status = string(run.TokenUsagePending)
		return result
	}
	counts := TokenCountsOutput(total)
	result.Status, result.Usage = string(run.TokenUsageAvailable), &counts
	return result
}
