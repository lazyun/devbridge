//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package reviewaccess

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxSymlinkExpansions = 40

type rootHandle struct {
	file     *os.File
	rootPath string
}

func openRootHandle(path string) (*rootHandle, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(absolute, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errors.New("authorized Repository root is unavailable")
	}
	return &rootHandle{file: os.NewFile(uintptr(fd), absolute), rootPath: filepath.Clean(absolute)}, nil
}

func (r *rootHandle) close() error { return r.file.Close() }
func (r *rootHandle) path() string { return r.rootPath }

func (r *rootHandle) open(supplied string, kind pathKind, hook func(string, string)) (*os.File, string, error) {
	rootFD, err := unix.Dup(int(r.file.Fd()))
	if err != nil {
		return nil, "", err
	}
	stack := []*os.File{os.NewFile(uintptr(rootFD), r.rootPath)}
	names := make([]string, 0)
	closeStack := func() {
		for _, file := range stack {
			_ = file.Close()
		}
	}
	defer closeStack()

	components := splitPathComponents(supplied)
	symlinks := 0
	for {
		if len(components) == 0 {
			fd, dupErr := unix.Dup(int(stack[len(stack)-1].Fd()))
			if dupErr != nil {
				return nil, "", dupErr
			}
			file := os.NewFile(uintptr(fd), strings.Join(names, "/"))
			if checkErr := checkOpenedKind(file, kind); checkErr != nil {
				_ = file.Close()
				return nil, "", checkErr
			}
			normalized := strings.Join(names, "/")
			if normalized == "" {
				normalized = "."
			}
			hook("target-opened", normalized)
			return file, normalized, nil
		}

		component := components[0]
		components = components[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			if len(stack) == 1 {
				return nil, "", errors.New("symlink escapes Repository root")
			}
			_ = stack[len(stack)-1].Close()
			stack = stack[:len(stack)-1]
			names = names[:len(names)-1]
			continue
		}

		candidateNames := append(append([]string(nil), names...), component)
		candidate := strings.Join(candidateNames, "/")
		hook("before-component-open", candidate)
		last := len(components) == 0
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
		if !last || (kind == pathDirectory || kind == pathDirectoryNoSymlinks) {
			flags |= unix.O_DIRECTORY
		}
		fd, openErr := unix.Openat(int(stack[len(stack)-1].Fd()), component, flags, 0)
		if openErr == nil {
			file := os.NewFile(uintptr(fd), candidate)
			if last {
				if checkErr := checkOpenedKind(file, kind); checkErr != nil {
					_ = file.Close()
					return nil, "", checkErr
				}
				hook("target-opened", candidate)
				return file, candidate, nil
			}
			stack = append(stack, file)
			names = candidateNames
			hook("directory-opened", candidate)
			continue
		}

		target, linkErr := readlinkAt(int(stack[len(stack)-1].Fd()), component)
		if linkErr != nil {
			return nil, "", openErr
		}
		if (last && kind == pathRegularNoFinalSymlink) || kind == pathRegularNoSymlinks || kind == pathDirectoryNoSymlinks {
			return nil, "", errors.New("tracked Repository file changed to a symlink")
		}
		symlinks++
		if symlinks > maxSymlinkExpansions {
			return nil, "", errors.New("too many Repository symlink expansions")
		}
		hook("symlink-read", candidate)
		if filepath.IsAbs(target) {
			remainder, ok := beneathAbsoluteRoot(r.rootPath, target)
			if !ok {
				return nil, "", errors.New("absolute symlink escapes Repository root")
			}
			for len(stack) > 1 {
				_ = stack[len(stack)-1].Close()
				stack = stack[:len(stack)-1]
			}
			names = names[:0]
			components = append(splitPathComponents(remainder), components...)
		} else {
			components = append(splitPathComponents(target), components...)
		}
	}
}

func checkOpenedKind(file *os.File, kind pathKind) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	switch kind {
	case pathRegular, pathRegularNoFinalSymlink, pathRegularNoSymlinks:
		if !info.Mode().IsRegular() {
			return errors.New("Repository path is not a regular file")
		}
	case pathDirectory, pathDirectoryNoSymlinks:
		if !info.IsDir() {
			return errors.New("Repository path is not a directory")
		}
	case pathAny:
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("unsupported Repository path type")
		}
	}
	return nil
}

func readlinkAt(dirfd int, name string) (string, error) {
	size := 256
	for size <= 64<<10 {
		buffer := make([]byte, size)
		n, err := platformReadlinkat(dirfd, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
		size *= 2
	}
	return "", errors.New("Repository symlink target is too long")
}

func splitPathComponents(path string) []string {
	return strings.Split(filepath.ToSlash(path), "/")
}

func beneathAbsoluteRoot(root, target string) (string, bool) {
	root = filepath.Clean(root)
	if target == root {
		return ".", true
	}
	prefix := root
	if prefix != string(filepath.Separator) {
		prefix += string(filepath.Separator)
	}
	if !strings.HasPrefix(target, prefix) {
		return "", false
	}
	return strings.TrimPrefix(target, prefix), true
}
