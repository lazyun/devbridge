//go:build darwin

package directfile

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// moveNoReplace uses directory descriptors obtained from the pinned Root, so
// the final rename remains anchored even if path names are concurrently moved.
func moveNoReplace(root *os.Root, source, destination string) error {
	sourceDir, sourceName := filepath.Dir(source), filepath.Base(source)
	destDir, destName := filepath.Dir(destination), filepath.Base(destination)
	from, err := root.Open(sourceDir)
	if err != nil {
		return fileError(err)
	}
	defer from.Close()
	to, err := root.Open(destDir)
	if err != nil {
		return fileError(err)
	}
	defer to.Close()
	if err = unix.RenameatxNp(int(from.Fd()), sourceName, int(to.Fd()), destName, unix.RENAME_EXCL); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return Failure(AlreadyExists, "destination already exists")
		}
		return Failure(OperationFailed, "safe no-replace rename failed")
	}
	return nil
}
