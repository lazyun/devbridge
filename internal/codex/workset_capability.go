package codex

import (
	"errors"

	"github.com/lazyun/devbridge/internal/workset"
)

// ErrRuntimeBindingRequired is retained only for callers that have not yet
// supplied prepared roots to Client options. It is a configuration error, not
// a statement about hostile-host isolation or protocol capability.
var ErrRuntimeBindingRequired = errors.New("runtime binding with prepared workspace roots is required")

// ErrExactWorksetUnsupported remains a source-compatible sentinel for older
// callers. Runtime binding is now the precise missing prerequisite.
var ErrExactWorksetUnsupported = ErrRuntimeBindingRequired

// RejectExactWorkset is a deprecated compatibility shim for old owners. New
// admission validates the workset and supplies RuntimeBinding to the Client.
func RejectExactWorkset() error { return ErrRuntimeBindingRequired }

// ValidateExactTargets validates frozen repository identities before a caller
// derives its RuntimeBinding. Trusted Executor operation relies on the pinned
// Client wire binding rather than a separate hostile-host proof.
func ValidateExactTargets(targets []workset.Target) error {
	return workset.ValidateTargets(targets)
}

// ValidateExactWorkset validates a frozen workset. It intentionally does not
// invent runtime paths: execution supplies environment-native roots through
// RuntimeBinding before the Client can serialize the request.
func ValidateExactWorkset(set workset.Set) error {
	return set.Validate()
}
