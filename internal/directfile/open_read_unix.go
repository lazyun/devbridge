//go:build darwin || linux

package directfile

import (
	"os"

	"golang.org/x/sys/unix"
)

// Opening non-blocking closes the race between the regular-file preflight and
// open: a concurrent swap to a FIFO cannot stall a capability operation.
func openForRead(root *os.Root, name string) (*os.File, error) {
	return root.OpenFile(name, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
