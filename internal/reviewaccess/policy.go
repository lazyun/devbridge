// Package reviewaccess owns the structural repository-access boundary for a
// Feature Area Review. Reviewer behavior belongs to the REVIEWER prompt; this package
// does not infer semantic intent from command text or repository path names.
package reviewaccess

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

const MaxReportedViolations = 16

type FailureCode string

const FailureScopeForbidden FailureCode = "REVIEW_SCOPE_FORBIDDEN"

var ErrScopeForbidden = errors.New(string(FailureScopeForbidden))

type Operation string

const (
	OperationScope  Operation = "SCOPE"
	OperationSearch Operation = "SEARCH"
	OperationRead   Operation = "READ"
)

// Violation is a stable, bounded structural failure suitable for propagation
// through Bridge API boundaries without exposing repository contents.
type Violation struct {
	Code      FailureCode
	Operation Operation
	Paths     []string
}

func (e *Violation) Error() string {
	detail := "Feature Area Review repository boundary rejected access"
	if len(e.Paths) != 0 {
		detail += ": " + strings.Join(e.Paths, ", ")
	}
	return string(e.Code) + ": " + detail
}

func (e *Violation) Is(target error) bool {
	return target == ErrScopeForbidden && e.Code == FailureScopeForbidden
}

// Policy validates only lexical facts required for repository-relative path
// safety. Names such as tests/, *_test.go, *.test.*, and *.spec.* are ordinary
// repository paths here; the REVIEWER prompt carries the instruction not to
// inspect test source.
type Policy struct{}

func RepositoryBoundary() Policy { return Policy{} }

func (Policy) ValidatePaths(operation Operation, paths []string) error {
	offending := make([]string, 0)
	for _, path := range paths {
		if _, ok := normalizeRelative(path); !ok {
			offending = append(offending, filepath.ToSlash(path))
		}
	}
	if len(offending) == 0 {
		return nil
	}
	sort.Strings(offending)
	offending = uniqueBounded(offending, MaxReportedViolations)
	return &Violation{Code: FailureScopeForbidden, Operation: operation, Paths: offending}
}

func scopeViolation(operation Operation, paths ...string) error {
	values := append([]string(nil), paths...)
	if len(values) > MaxReportedViolations {
		values = values[:MaxReportedViolations]
	}
	return &Violation{Code: FailureScopeForbidden, Operation: operation, Paths: values}
}

func normalizeRelative(value string) (string, bool) {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') {
		return "", false
	}
	for _, component := range strings.Split(value, "/") {
		if component == ".." {
			return "", false
		}
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

func uniqueBounded(values []string, limit int) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) != 0 && result[len(result)-1] == value {
			continue
		}
		if len(result) == limit {
			break
		}
		result = append(result, value)
	}
	return result
}
