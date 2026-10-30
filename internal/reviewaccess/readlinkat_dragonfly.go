//go:build dragonfly

package reviewaccess

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

// x/sys/unix exposes SYS_READLINKAT but not its generated wrapper on DragonFly.
func platformReadlinkat(dirfd int, path string, buffer []byte) (int, error) {
	pointer, err := unix.BytePtrFromString(path)
	if err != nil {
		return 0, err
	}
	var destination unsafe.Pointer
	if len(buffer) != 0 {
		destination = unsafe.Pointer(&buffer[0])
	}
	result, _, errno := unix.Syscall6(unix.SYS_READLINKAT, uintptr(dirfd), uintptr(unsafe.Pointer(pointer)), uintptr(destination), uintptr(len(buffer)), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(result), nil
}
