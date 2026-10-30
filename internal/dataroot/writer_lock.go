package dataroot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ErrWriterLocked means another live process or file descriptor currently
// owns the exclusive writer lock for this DataRoot.
var ErrWriterLocked = errors.New("Dev Bridge DataRoot is already open for writing")

// WriterLock holds the operating-system lock for one DataRoot. The lock file
// is intentionally retained after Close; deleting it would allow two callers
// to lock different inodes under the same pathname.
type WriterLock struct {
	mu   sync.Mutex
	file *os.File
	held bool
}

// AcquireWriterLock opens path and acquires its non-blocking exclusive
// operating-system lock. The parent directory must already exist.
func AcquireWriterLock(path string) (*WriterLock, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("DataRoot writer lock path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve DataRoot writer lock path: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("DataRoot writer lock must be a regular file: %s", absolute)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open DataRoot writer lock %q: %w", absolute, err)
	}
	info, statErr := file.Stat()
	current, pathErr := os.Lstat(absolute)
	if statErr != nil || pathErr != nil || !current.Mode().IsRegular() || !os.SameFile(info, current) {
		_ = file.Close()
		return nil, fmt.Errorf("DataRoot writer lock changed or is not a regular file: %s", absolute)
	}
	if err := lockWriterFile(file); err != nil {
		_ = file.Close()
		if isWriterLockConflict(err) {
			return nil, fmt.Errorf("%w: %s", ErrWriterLocked, absolute)
		}
		return nil, fmt.Errorf("acquire DataRoot writer lock %q: %w", absolute, err)
	}
	return &WriterLock{file: file, held: true}, nil
}

// Held reports whether this handle still owns its file descriptor and lock.
func (l *WriterLock) Held() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// Close releases the lock and closes its file descriptor. It is idempotent.
// Process termination also closes the descriptor and releases the OS lock.
func (l *WriterLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.held {
		return nil
	}
	l.held = false
	unlockErr := unlockWriterFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
