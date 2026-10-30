// Package dataroot owns the recognizable on-disk boundary for one complete
// local Dev Bridge environment.
package dataroot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	MarkerFilename     = ".devbridge-data-root.json"
	FormatVersion      = 1
	WriterLockFilename = "core-writer.lock"
)

type ErrorCode string

const (
	CodePathInvalid       ErrorCode = "DATA_ROOT_PATH_INVALID"
	CodePathIsFile        ErrorCode = "DATA_ROOT_PATH_IS_FILE"
	CodeNotRecognized     ErrorCode = "DATA_ROOT_NOT_RECOGNIZED"
	CodeMarkerMalformed   ErrorCode = "DATA_ROOT_MARKER_MALFORMED"
	CodeFormatUnsupported ErrorCode = "DATA_ROOT_FORMAT_UNSUPPORTED"
	CodeChildInvalid      ErrorCode = "DATA_ROOT_CHILD_INVALID"
	CodeInitializeFailed  ErrorCode = "DATA_ROOT_INITIALIZE_FAILED"
)

// Error is stable enough for local launchers to present a bounded product
// message while retaining the underlying filesystem error for diagnostics.
type Error struct {
	Code ErrorCode
	Path string
	Err  error
}

func (e *Error) Error() string {
	switch e.Code {
	case CodePathIsFile:
		return "the selected Dev Bridge data directory is a file"
	case CodeNotRecognized:
		return "the selected directory is not empty and is not a Dev Bridge data directory"
	case CodeMarkerMalformed:
		return "the selected directory has an invalid Dev Bridge data root marker"
	case CodeFormatUnsupported:
		return "the selected directory uses an unsupported Dev Bridge data root format"
	case CodeChildInvalid:
		return "the selected Dev Bridge data directory contains an invalid managed path"
	case CodeInitializeFailed:
		return "the selected Dev Bridge data directory could not be initialized"
	default:
		return "the selected Dev Bridge data directory is invalid"
	}
}

func (e *Error) Unwrap() error { return e.Err }

// Paths contains every top-level location owned by one DataRoot. Project
// repositories are references outside this layout, never children of Root.
type DataPaths struct {
	Root      string
	State     string
	Worktrees string
	Evidence  string
	Logs      string
	Runtime   string
	Artifacts string
}

type DataRoot struct {
	writer        *WriterLock
	ownedPaths    DataPaths
	Paths         DataPaths
	Initialized   bool
	FormatVersion int
}

type marker struct {
	FormatVersion *int `json:"formatVersion"`
}

// DefaultRoot returns the platform application-data location used by Core on
// first launch. It does not create anything.
func DefaultRoot() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve platform application data directory: %w", err)
	}
	return filepath.Join(base, "DevBridge", "data"), nil
}

func Derive(root string) (DataPaths, error) {
	if root == "" {
		return DataPaths{}, &Error{Code: CodePathInvalid, Err: errors.New("data root is required")}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return DataPaths{}, &Error{Code: CodePathInvalid, Path: root, Err: err}
	}
	absolute = filepath.Clean(absolute)
	return DataPaths{
		Root:      absolute,
		State:     filepath.Join(absolute, "state"),
		Worktrees: filepath.Join(absolute, "worktrees"),
		Evidence:  filepath.Join(absolute, "evidence"),
		Logs:      filepath.Join(absolute, "logs"),
		Runtime:   filepath.Join(absolute, "runtime"),
		Artifacts: filepath.Join(absolute, "artifacts"),
	}, nil
}

// Close releases bootstrap ownership. Call only after every DataRoot writer,
// including SQLite and the process logger, has closed.
func (d DataRoot) Close() error { return d.writer.Close() }

// WriterHeld validates that this handle still owns the paths it exposes.
func (d DataRoot) WriterHeld() bool { return d.Paths == d.ownedPaths && d.writer.Held() }

// Open acquires exclusive ownership before initializing or repairing a root.
// The caller must Close the returned handle after its last DataRoot write.
// Invalid roots are rejected by a read-only preflight before creating the lock.
func Open(requested string) (DataRoot, error) {
	if requested == "" {
		var err error
		requested, err = DefaultRoot()
		if err != nil {
			return DataRoot{}, err
		}
	}
	paths, err := Derive(requested)
	if err != nil {
		return DataRoot{}, err
	}
	info, statErr := os.Stat(paths.Root)
	switch {
	case errors.Is(statErr, os.ErrNotExist):
		if err := os.MkdirAll(paths.Root, 0o700); err != nil {
			return DataRoot{}, &Error{Code: CodeInitializeFailed, Path: paths.Root, Err: err}
		}
	case statErr != nil:
		return DataRoot{}, &Error{Code: CodePathInvalid, Path: paths.Root, Err: statErr}
	case !info.IsDir():
		return DataRoot{}, &Error{Code: CodePathIsFile, Path: paths.Root}
	}

	resolved, err := filepath.EvalSymlinks(paths.Root)
	if err != nil {
		return DataRoot{}, &Error{Code: CodePathInvalid, Path: paths.Root, Err: err}
	}
	paths, err = Derive(resolved)
	if err != nil {
		return DataRoot{}, err
	}

	// Preflight avoids adding a lock artifact to foreign or invalid directories.
	if _, err := inspect(paths); err != nil {
		return DataRoot{}, err
	}
	writer, err := AcquireWriterLock(filepath.Join(paths.Root, WriterLockFilename))
	if err != nil {
		if errors.Is(err, ErrWriterLocked) {
			return DataRoot{}, err
		}
		return DataRoot{}, &Error{Code: CodeInitializeFailed, Path: paths.Root, Err: err}
	}
	opened := false
	defer func() {
		if !opened {
			_ = writer.Close()
		}
	}()
	// Revalidate under ownership; preflight is never mutation authority.
	initialized, err := inspect(paths)
	if err != nil {
		return DataRoot{}, err
	}
	if initialized {
		err = initialize(paths)
	} else {
		err = ensureChildren(paths, nil)
	}
	if err != nil {
		return DataRoot{}, err
	}
	opened = true
	return DataRoot{Paths: paths, ownedPaths: paths, writer: writer, Initialized: initialized, FormatVersion: FormatVersion}, nil
}

// inspect validates existing entries without creating or repairing anything.
// Missing managed paths are permitted and will be created under ownership.
func inspect(paths DataPaths) (bool, error) {
	markerPath := filepath.Join(paths.Root, MarkerFilename)
	markerInfo, markerErr := os.Lstat(markerPath)
	if errors.Is(markerErr, os.ErrNotExist) {
		entries, err := os.ReadDir(paths.Root)
		if err != nil {
			return false, &Error{Code: CodePathInvalid, Path: paths.Root, Err: err}
		}
		// A peer may publish the marker during the read-only preflight.
		if _, err := os.Lstat(markerPath); err == nil {
			return inspect(paths)
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, &Error{Code: CodeMarkerMalformed, Path: markerPath, Err: err}
		}
		if !recoverableInitializationEntries(entries) {
			return false, &Error{Code: CodeNotRecognized, Path: paths.Root}
		}
		return true, nil
	}
	if markerErr != nil || !markerInfo.Mode().IsRegular() {
		return false, &Error{Code: CodeMarkerMalformed, Path: markerPath, Err: markerErr}
	}
	value, err := readMarker(markerPath)
	if err != nil {
		return false, err
	}
	if *value.FormatVersion != FormatVersion {
		return false, &Error{Code: CodeFormatUnsupported, Path: markerPath, Err: fmt.Errorf("format version %d", *value.FormatVersion)}
	}
	for _, path := range []string{paths.State, paths.Worktrees, paths.Evidence, paths.Logs, paths.Runtime, paths.Artifacts} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return false, &Error{Code: CodeChildInvalid, Path: path, Err: err}
		}
	}
	return false, nil
}

func initialize(paths DataPaths) error {
	// Ownership is held throughout initialization. Preserve partial layout on
	// failure so a subsequent owner can safely retry after a crash.
	if err := ensureChildren(paths, nil); err != nil {
		return &Error{Code: CodeInitializeFailed, Path: paths.Root, Err: err}
	}
	version := FormatVersion
	data, err := json.MarshalIndent(marker{FormatVersion: &version}, "", "  ")
	if err != nil {
		return &Error{Code: CodeInitializeFailed, Path: paths.Root, Err: err}
	}
	data = append(data, '\n')
	if err := writeAtomic(filepath.Join(paths.Root, MarkerFilename), data); err != nil {
		return &Error{Code: CodeInitializeFailed, Path: paths.Root, Err: err}
	}
	return nil
}

func ensureChildren(paths DataPaths, created *[]string) error {
	for _, path := range []string{paths.State, paths.Worktrees, paths.Evidence, paths.Logs, paths.Runtime, paths.Artifacts} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(path, 0o700); err == nil {
				if created != nil {
					*created = append(*created, path)
				}
				continue
			} else if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create managed directory %q: %w", filepath.Base(path), err)
			}
			// A concurrent initializer won the create race. Revalidate the exact
			// object; EEXIST never makes a file or symlink acceptable.
			info, err = os.Lstat(path)
			if err != nil {
				return &Error{Code: CodeChildInvalid, Path: path, Err: err}
			}
		}
		if err != nil {
			return &Error{Code: CodeChildInvalid, Path: path, Err: err}
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return &Error{Code: CodeChildInvalid, Path: path}
		}
	}
	return nil
}

func recoverableInitializationEntries(entries []os.DirEntry) bool {
	if len(entries) == 0 {
		return true
	}
	managed := map[string]struct{}{
		"state": {}, "worktrees": {}, "evidence": {}, "logs": {}, "runtime": {}, "artifacts": {},
	}
	for _, entry := range entries {
		if _, ok := managed[entry.Name()]; ok {
			info, err := entry.Info()
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return false
			}
			continue
		}
		// A crashed initializer can leave its marker temp file and retained
		// bootstrap lock. Neither artifact alone indicates live ownership.
		if matched, _ := filepath.Match(".data-root-*.json", entry.Name()); matched || entry.Name() == WriterLockFilename {
			info, err := entry.Info()
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				return false
			}
			continue
		}
		return false
	}
	return true
}

func readMarker(path string) (marker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return marker{}, &Error{Code: CodeMarkerMalformed, Path: path, Err: err}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value marker
	if err := decoder.Decode(&value); err != nil {
		return marker{}, &Error{Code: CodeMarkerMalformed, Path: path, Err: err}
	}
	if value.FormatVersion == nil {
		return marker{}, &Error{Code: CodeMarkerMalformed, Path: path, Err: errors.New("marker formatVersion is required")}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return marker{}, &Error{Code: CodeMarkerMalformed, Path: path, Err: errors.New("marker contains trailing data")}
	}
	return value, nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".data-root-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
