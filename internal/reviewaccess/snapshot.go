package reviewaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	maxSnapshotFiles = 4096
	maxSnapshotBytes = 32 << 20
	maxSnapshotDirs  = 4096
	maxSnapshotDepth = 64
)

// ScopedSnapshot fingerprints only explicitly authorized repository content.
// It is descriptor-confined and deliberately does not invoke Git or a shell.
func ScopedSnapshot(ctx context.Context, rootPath string, allowed, forbidden []string) (string, error) {
	if ctx == nil {
		return "", errors.New("snapshot context is required")
	}
	if err := validateSnapshotPaths(allowed); err != nil {
		return "", err
	}
	if err := validateSnapshotPaths(forbidden); err != nil {
		return "", err
	}
	root, err := OpenRoot(rootPath)
	if err != nil {
		return "", err
	}
	defer root.Close()
	state := snapshotState{root: root, ctx: ctx, forbidden: normalizePaths(forbidden), seenFiles: map[string]bool{}, seenDirs: map[string]bool{}, records: map[string]snapshotRecord{}}
	starts := normalizePaths(allowed)
	if len(starts) == 0 {
		starts = []string{"."}
	}
	for _, path := range starts {
		if state.excluded(path) || protectedSnapshotPath(path) {
			continue
		}
		if err := state.visit(path); err != nil {
			return "", err
		}
	}
	h := sha256.New()
	paths := make([]string, 0, len(state.records))
	for path := range state.records {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		r := state.records[path]
		_, _ = io.WriteString(h, "F\x00"+path+"\x00"+strconv.FormatInt(r.size, 10)+"\x00"+r.digest+"\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type snapshotState struct {
	root                        *Root
	ctx                         context.Context
	forbidden                   []string
	files, bytes, dirs, entries int
	seenFiles, seenDirs         map[string]bool
	records                     map[string]snapshotRecord
}
type snapshotRecord struct {
	size   int64
	digest string
}

func (s *snapshotState) visit(path string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.excluded(path) || protectedSnapshotPath(path) {
		return nil
	}
	if strings.Count(path, "/") > maxSnapshotDepth {
		return errors.New("snapshot depth exceeded")
	}
	file, normalized, err := s.root.OpenFileStrict(OperationRead, path)
	if err == nil {
		defer file.Close()
		return s.hashFile(normalized, file)
	}
	dir, normalized, dirErr := s.root.OpenDirectoryStrict(OperationRead, path)
	if dirErr != nil {
		return err
	}
	defer dir.Close()
	if s.seenDirs[normalized] {
		return nil
	}
	s.seenDirs[normalized] = true
	s.dirs++
	if s.dirs > maxSnapshotDirs {
		return errors.New("snapshot directory bounds exceeded")
	}
	for {
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return readErr
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			s.entries++
			if s.entries > maxSnapshotFiles*8 {
				return errors.New("snapshot entry bounds exceeded")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := entry.Name()
			if normalized != "." {
				child = normalized + "/" + child
			}
			child = filepath.ToSlash(child)
			if s.excluded(child) || protectedSnapshotPath(child) {
				continue
			}
			if entry.IsDir() || entry.Type().IsRegular() {
				if err := s.visit(child); err != nil {
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return nil
}

func (s *snapshotState) hashFile(path string, f *os.File) error {
	if s.seenFiles[path] {
		return nil
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("snapshot regular file unavailable")
	}
	if s.files >= maxSnapshotFiles || s.bytes >= maxSnapshotBytes {
		return errors.New("snapshot bounds exceeded")
	}
	remaining := maxSnapshotBytes - s.bytes
	fileHash := sha256.New()
	n, err := io.Copy(fileHash, io.LimitReader(f, int64(remaining)+1))
	if err != nil {
		return err
	}
	if n > int64(remaining) || n != info.Size() {
		return errors.New("snapshot file changed or bounds exceeded")
	}
	s.seenFiles[path] = true
	s.records[path] = snapshotRecord{size: n, digest: hex.EncodeToString(fileHash.Sum(nil))}
	s.files++
	s.bytes += int(n)
	return nil
}

func validateSnapshotPaths(paths []string) error {
	for _, path := range paths {
		if path == "." {
			continue
		}
		if err := RepositoryBoundary().ValidatePaths(OperationRead, []string{path}); err != nil {
			return err
		}
	}
	return nil
}
func normalizePaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
func (s *snapshotState) excluded(path string) bool {
	for _, p := range s.forbidden {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
func protectedSnapshotPath(path string) bool { return UpperPathProtected(path) }

// UpperPathProtected preserves the existing Direct File administrative boundary.
func UpperPathProtected(path string) bool {
	for _, component := range strings.Split(path, "/") {
		if strings.EqualFold(component, ".git") {
			return true
		}
	}
	return false
}
