//go:build !darwin && !linux

package directfile

import "os"

// The portability fallback is safe for platforms without Unix FIFOs; the
// caller already requires a regular-file Lstat preflight.
func openForRead(root *os.Root, name string) (*os.File, error) { return root.Open(name) }
