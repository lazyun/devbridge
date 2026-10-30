//go:build !darwin && !linux

package directfile

import "os"

// Other platforms have no no-clobber rename primitive exposed by the standard
// dependency set. Failing closed is preferable to a check-then-rename race.
func moveNoReplace(_ *os.Root, _, _ string) error {
	return Failure(OperationFailed, "safe no-replace rename is unsupported on this platform")
}
