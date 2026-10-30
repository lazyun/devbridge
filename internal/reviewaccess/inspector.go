package reviewaccess

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	DefaultSearchLimit = 50
	MaxSearchLimit     = 100
	DefaultListLimit   = 100
	MaxListLimit       = 200
	MaxReadBytes       = 128 << 10
	MaxReadLines       = 1000
)

type SearchMatch struct {
	Path    string
	Line    int
	Snippet string
}

// GitSearchMatch is the small structural interface adapter implemented by
// internal/git.Client through Inspector's constructor adapter. It keeps the
// repository access package independent from Git process ownership.
type GitSearchMatch struct {
	Path    string
	Line    int
	Snippet string
}

type SearchBackend interface {
	SearchRepository(context.Context, *Root, string, []string) ([]GitSearchMatch, error)
}

type Inspector struct {
	search SearchBackend
	hook   func(accessEvent)
}

func NewInspector(search SearchBackend) (*Inspector, error) {
	if search == nil {
		return nil, errors.New("repository search backend is required")
	}
	return &Inspector{search: search}, nil
}

type SearchResult struct {
	Matches   []SearchMatch `json:"matches"`
	Truncated bool          `json:"truncated"`
}

type ReadResult struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

type ListEntry struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type ListResult struct {
	Path      string      `json:"path"`
	Entries   []ListEntry `json:"entries"`
	Truncated bool        `json:"truncated"`
}

func (i *Inspector) Search(ctx context.Context, root, query string, paths []string, limit int) (SearchResult, error) {
	if i == nil || i.search == nil || strings.TrimSpace(query) == "" || len(query) > 4096 || strings.ContainsRune(query, '\x00') {
		return SearchResult{}, errors.New("invalid Review repository search")
	}
	if limit == 0 {
		limit = DefaultSearchLimit
	}
	if limit < 1 || limit > MaxSearchLimit || len(paths) > 64 {
		return SearchResult{}, errors.New("invalid Review repository search bounds")
	}
	capability, err := openRoot(root, i.hook)
	if err != nil {
		return SearchResult{}, err
	}
	defer capability.Close()
	validated := make([]string, 0, len(paths))
	for _, supplied := range paths {
		normalized, _, err := capability.Resolve(OperationSearch, supplied)
		if err != nil {
			return SearchResult{}, err
		}
		validated = append(validated, normalized)
	}
	matches, err := i.search.SearchRepository(ctx, capability, query, validated)
	if err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Matches: make([]SearchMatch, 0, min(limit, len(matches))), Truncated: len(matches) > limit}
	for index, match := range matches {
		if index == limit {
			break
		}
		result.Matches = append(result.Matches, SearchMatch{Path: filepath.ToSlash(match.Path), Line: match.Line, Snippet: boundedString(match.Snippet, 512)})
	}
	return result, nil
}

func (i *Inspector) Read(_ context.Context, root, supplied string, startLine, endLine int) (ReadResult, error) {
	capability, err := openRoot(root, i.hook)
	if err != nil {
		return ReadResult{}, err
	}
	defer capability.Close()
	if startLine == 0 {
		startLine = 1
	}
	if endLine == 0 {
		endLine = startLine + 199
	}
	if startLine < 1 || endLine < startLine || endLine-startLine+1 > MaxReadLines {
		return ReadResult{}, errors.New("invalid Review repository read bounds")
	}
	file, normalized, err := capability.OpenFile(OperationRead, supplied)
	if err != nil {
		return ReadResult{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ReadResult{}, errors.New("Review repository path is not a regular file")
	}
	buffer := make([]byte, MaxReadBytes+1)
	n, readErr := file.Read(buffer)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return ReadResult{}, readErr
	}
	buffer = buffer[:n]
	if bytes.IndexByte(buffer, 0) >= 0 {
		return ReadResult{}, errors.New("binary Review repository content is unavailable")
	}
	lines := strings.Split(string(buffer), "\n")
	fileTruncated := n > MaxReadBytes
	if fileTruncated {
		buffer = buffer[:MaxReadBytes]
		lines = strings.Split(string(buffer), "\n")
	}
	if startLine > len(lines) {
		return ReadResult{Path: normalized, StartLine: startLine, EndLine: startLine - 1, Content: "", Truncated: fileTruncated}, nil
	}
	last := min(endLine, len(lines))
	content := strings.Join(lines[startLine-1:last], "\n")
	return ReadResult{Path: normalized, StartLine: startLine, EndLine: last, Content: content, Truncated: fileTruncated || last < len(lines)}, nil
}

func (i *Inspector) List(_ context.Context, root, supplied string, limit int) (ListResult, error) {
	if limit == 0 {
		limit = DefaultListLimit
	}
	if limit < 1 || limit > MaxListLimit {
		return ListResult{}, errors.New("invalid Review repository list bounds")
	}
	capability, err := openRoot(root, i.hook)
	if err != nil {
		return ListResult{}, err
	}
	defer capability.Close()
	directory, normalized, err := capability.OpenDirectory(OperationRead, supplied)
	if err != nil {
		return ListResult{}, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Path: normalized, Entries: make([]ListEntry, 0, min(limit, len(entries)))}
	for _, entry := range entries {
		candidate := entry.Name()
		if normalized != "." {
			candidate = normalized + "/" + candidate
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		} else if !entry.Type().IsRegular() {
			continue
		}
		if len(result.Entries) == limit {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, ListEntry{Path: filepath.ToSlash(candidate), Kind: kind})
	}
	sort.Slice(result.Entries, func(a, b int) bool { return result.Entries[a].Path < result.Entries[b].Path })
	return result, nil
}

func boundedString(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
