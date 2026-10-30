//go:build linux

package directfile

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

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
	if err = unix.Renameat2(int(from.Fd()), sourceName, int(to.Fd()), destName, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return Failure(AlreadyExists, "destination already exists")
		}
		return Failure(OperationFailed, "safe no-replace rename failed")
	}
	return nil
}
