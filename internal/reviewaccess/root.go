package reviewaccess

import (
	"errors"
	"os"
)

// Root is an opened Repository directory capability. Path resolution performed
// through Root never reopens an already-validated absolute pathname.
type Root struct {
	handle *rootHandle
	hook   func(accessEvent)
}

type accessEvent struct {
	stage string
	path  string
}

func OpenRoot(path string) (*Root, error) {
	return openRoot(path, nil)
}

func openRoot(path string, hook func(accessEvent)) (*Root, error) {
	handle, err := openRootHandle(path)
	if err != nil {
		return nil, err
	}
	root := &Root{handle: handle, hook: hook}
	root.fire("root-opened", ".")
	return root, nil
}

func (r *Root) Close() error {
	if r == nil || r.handle == nil {
		return nil
	}
	err := r.handle.close()
	r.handle = nil
	return err
}

func (r *Root) Path() string {
	if r == nil || r.handle == nil {
		return ""
	}
	return r.handle.path()
}

func (r *Root) OpenFile(operation Operation, supplied string) (*os.File, string, error) {
	return r.open(operation, supplied, pathRegular)
}

// OpenTrackedFile opens a tracked regular path while rejecting a final
// worktree symlink. Git's tracked-file search does not follow a tracked entry
// whose final component has changed from a regular file to a symlink.
func (r *Root) OpenTrackedFile(operation Operation, supplied string) (*os.File, error) {
	file, _, err := r.open(operation, supplied, pathRegularNoFinalSymlink)
	return file, err
}

func (r *Root) OpenDirectory(operation Operation, supplied string) (*os.File, string, error) {
	if supplied == "" || supplied == "." {
		supplied = "."
	} else if err := RepositoryBoundary().ValidatePaths(operation, []string{supplied}); err != nil {
		return nil, "", err
	}
	return r.open(operation, supplied, pathDirectory)
}

func (r *Root) Resolve(operation Operation, supplied string) (string, bool, error) {
	if err := RepositoryBoundary().ValidatePaths(operation, []string{supplied}); err != nil {
		return "", false, err
	}
	if len(supplied) > 4096 {
		return "", false, scopeViolation(operation, supplied)
	}
	file, normalized, err := r.open(operation, supplied, pathAny)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", false, scopeViolation(operation, supplied)
	}
	return normalized, info.IsDir(), nil
}

func (r *Root) open(operation Operation, supplied string, kind pathKind) (*os.File, string, error) {
	if r == nil || r.handle == nil {
		return nil, "", errors.New("Repository root capability is closed")
	}
	if supplied != "." {
		if err := RepositoryBoundary().ValidatePaths(operation, []string{supplied}); err != nil {
			return nil, "", err
		}
		if len(supplied) > 4096 {
			return nil, "", scopeViolation(operation, supplied)
		}
	}
	file, normalized, err := r.handle.open(supplied, kind, r.fire)
	if err != nil {
		return nil, "", scopeViolation(operation, supplied)
	}
	return file, normalized, nil
}

func (r *Root) fire(stage, path string) {
	if r.hook != nil {
		r.hook(accessEvent{stage: stage, path: path})
	}
}

type pathKind uint8

const (
	pathAny pathKind = iota
	pathRegular
	pathRegularNoFinalSymlink
	pathDirectory
	pathRegularNoSymlinks
	pathDirectoryNoSymlinks
)

// Strict read capabilities reject every symlink component, including links
// within the repository that could otherwise bypass a narrower grant scope.
func (r *Root) OpenFileStrict(operation Operation, supplied string) (*os.File, string, error) {
	return r.open(operation, supplied, pathRegularNoSymlinks)
}
func (r *Root) OpenDirectoryStrict(operation Operation, supplied string) (*os.File, string, error) {
	if supplied == "" {
		supplied = "."
	}
	return r.open(operation, supplied, pathDirectoryNoSymlinks)
}
