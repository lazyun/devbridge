package execution

import (
	"errors"

	"github.com/lazyun/devbridge/internal/task"
)

// Validate technical repository containment for read-only Review focus.
// Implementation modification paths are engineering metadata, not a gate.
func validateReviewScopeBoundary(worktreeRoot string, scope task.ExecutionScope) error {
	canonical, err := task.CanonicalizeExecutionScope(worktreeRoot, scope)
	if err != nil {
		return err
	}
	if canonical.Kind != scope.Kind || len(canonical.Paths) != len(scope.Paths) {
		return errors.New("confirmed execution scope no longer matches repository state")
	}
	for index := range canonical.Paths {
		if canonical.Paths[index] != scope.Paths[index] {
			return errors.New("confirmed execution scope canonical target changed")
		}
	}
	return nil
}
