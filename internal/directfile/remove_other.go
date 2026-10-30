//go:build !darwin && !linux

package directfile

import "os"

func removeFile(*os.Root, string) error {
	return Failure(OperationFailed, "safe file removal is unsupported on this platform")
}
