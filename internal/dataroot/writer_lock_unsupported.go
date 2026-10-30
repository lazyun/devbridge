//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package dataroot

import (
	"errors"
	"os"
)

var errWriterLockUnsupported = errors.New("DataRoot writer locking is unsupported on this platform")

func lockWriterFile(*os.File) error   { return errWriterLockUnsupported }
func unlockWriterFile(*os.File) error { return nil }
func isWriterLockConflict(error) bool { return false }
