//go:build darwin || linux

package directfile

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Unlike os.Root.Remove, unlink without AT_REMOVEDIR cannot delete a directory
// if an external writer swaps the target after the regular-file check.
func removeFile(root *os.Root, name string) error {
	parent, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Unlinkat(int(parent.Fd()), filepath.Base(name), 0)
}
