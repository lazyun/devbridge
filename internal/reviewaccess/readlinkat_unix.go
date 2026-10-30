//go:build darwin || freebsd || linux || netbsd || openbsd

package reviewaccess

import "golang.org/x/sys/unix"

func platformReadlinkat(dirfd int, path string, buffer []byte) (int, error) {
	return unix.Readlinkat(dirfd, path, buffer)
}
