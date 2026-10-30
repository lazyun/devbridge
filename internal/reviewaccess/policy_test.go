package reviewaccess

import (
	"errors"
	"testing"
)

func TestRepositoryBoundaryTreatsTestLikeNamesAsOrdinaryPaths(t *testing.T) {
	policy := RepositoryBoundary()
	for _, path := range []string{
		"test/a.go", "tests/a.dart", "pkg/__tests__/a.ts", "pkg/a_test.go",
		"pkg/a.test.ts", "pkg/a.spec.js", "!test/**", "!tests/**",
		"!*_test.go", "*.spec.*",
	} {
		if err := policy.ValidatePaths(OperationRead, []string{path}); err != nil {
			t.Errorf("test-like path %q received semantic rejection: %v", path, err)
		}
	}
}

func TestRepositoryBoundaryRejectsOnlyMalformedOrEscapingPathSyntax(t *testing.T) {
	policy := RepositoryBoundary()
	for _, path := range []string{"", ".", "../outside", "a/../outside", "/absolute", "a\\b"} {
		err := policy.ValidatePaths(OperationScope, []string{path})
		if !errors.Is(err, ErrScopeForbidden) {
			t.Errorf("structurally invalid path %q error=%v", path, err)
		}
	}
	for _, path := range []string{"internal/task/service.go", "contest/entry.go", "pkg/service_test.go", "tests/unit.go"} {
		if err := policy.ValidatePaths(OperationScope, []string{path}); err != nil {
			t.Errorf("repository-relative path %q rejected: %v", path, err)
		}
	}
}
