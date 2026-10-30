//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package dataroot

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockWriterFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockWriterFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}

func isWriterLockConflict(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}
