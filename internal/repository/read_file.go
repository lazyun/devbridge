package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/reviewaccess"
)

const MaxFileObservationBytes = 8 << 20
const MaxFilePageBytes = 64 << 10

var ErrFileVersionChanged = errors.New("REPOSITORY_FILE_VERSION_CHANGED")
var ErrFileReadBounds = errors.New("REPOSITORY_FILE_READ_BOUNDS")
var ErrFileTooLarge = errors.New("REPOSITORY_FILE_TOO_LARGE")

type FileReadInput struct {
	Path                  string `json:"path"`
	Commit                string `json:"commit,omitempty"`
	Offset                int64  `json:"offset,omitempty"`
	MaxBytes              int    `json:"maxBytes,omitempty"`
	ExpectedContentSHA256 string `json:"expectedContentSHA256,omitempty"`
}
type FileReadResult struct {
	ProjectID     string `json:"projectId"`
	RepositoryID  string `json:"repositoryId"`
	WorkspaceID   string `json:"workspaceId,omitempty"`
	SourceKind    string `json:"sourceKind"`
	Path          string `json:"path"`
	HeadCommit    string `json:"headCommit"`
	ContentSHA256 string `json:"contentSHA256"`
	SizeBytes     int64  `json:"sizeBytes"`
	Offset        int64  `json:"offset"`
	NextOffset    int64  `json:"nextOffset"`
	StartLine     int    `json:"startLine"`
	EndLine       int    `json:"endLine"`
	Truncated     bool   `json:"truncated"`
	Encoding      string `json:"encoding"`
	Content       string `json:"content"`
}

func exactHex(value string, n int) bool {
	if len(value) != n {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

// ReadFile observes registered source-worktree content, not a managed Task
// workspace. It never creates an authorization or mutates repository state.
func (s *Service) ReadFile(ctx context.Context, id string, in FileReadInput) (FileReadResult, error) {
	var out FileReadResult
	if in.MaxBytes == 0 {
		in.MaxBytes = MaxFilePageBytes
	}
	if in.Path == "" || len(in.Path) > 4096 || path.IsAbs(in.Path) || path.Clean(in.Path) != in.Path || strings.Contains(in.Path, "\\") || in.Offset < 0 || in.MaxBytes < 1 || in.MaxBytes > MaxFilePageBytes {
		return out, ErrFileReadBounds
	}
	for _, component := range strings.Split(in.Path, "/") {
		if strings.EqualFold(component, ".git") {
			return out, ErrFileReadBounds
		}
	}
	if in.Commit != "" && !exactHex(in.Commit, 40) && !exactHex(in.Commit, 64) {
		return out, ErrFileReadBounds
	}
	if in.ExpectedContentSHA256 != "" && !exactHex(in.ExpectedContentSHA256, 64) {
		return out, ErrFileReadBounds
	}
	if err := reviewaccess.RepositoryBoundary().ValidatePaths(reviewaccess.OperationRead, []string{in.Path}); err != nil {
		return out, err
	}
	value, err := s.store.Get(ctx, id)
	if err != nil {
		return out, err
	}
	root, err := reviewaccess.OpenRoot(value.Path)
	if err != nil {
		return out, err
	}
	defer root.Close()
	rootDirectory, _, err := root.OpenDirectoryStrict(reviewaccess.OperationRead, ".")
	if err != nil {
		return out, err
	}
	defer rootDirectory.Close()
	pinnedRoot, err := rootDirectory.Stat()
	if err != nil {
		return out, err
	}
	visibleRoot, err := os.Stat(root.Path())
	if err != nil || !os.SameFile(pinnedRoot, visibleRoot) {
		return out, ErrFileVersionChanged
	}
	var data []byte
	head := in.Commit
	kind := "COMMIT"
	if in.Commit != "" {
		reader, ok := s.git.(interface {
			ReadCommittedFileRoot(context.Context, *reviewaccess.Root, string, string) ([]byte, error)
		})
		if !ok {
			return out, errors.New("historical repository file read unavailable")
		}
		data, err = reader.ReadCommittedFileRoot(ctx, root, in.Commit, in.Path)
		if err != nil {
			return out, err
		}
	} else {
		kind = "REGISTERED_WORKTREE"
		head, err = readFileHEAD(ctx, s.git, root.Path())
		if err != nil {
			return out, err
		}
		file, _, err := root.OpenFileStrict(reviewaccess.OperationRead, in.Path)
		if err != nil {
			return out, err
		}
		defer file.Close()
		before, err := file.Stat()
		if err != nil {
			return out, err
		}
		if !before.Mode().IsRegular() {
			return out, errors.New("repository path is not a regular file")
		}
		if before.Size() > MaxFileObservationBytes {
			return out, ErrFileTooLarge
		}
		data, err = io.ReadAll(io.LimitReader(file, MaxFileObservationBytes+1))
		if err != nil {
			return out, err
		}
		after, err := file.Stat()
		if err != nil {
			return out, err
		}
		current, _, err := root.OpenFileStrict(reviewaccess.OperationRead, in.Path)
		if err != nil {
			return out, ErrFileVersionChanged
		}
		currentInfo, statErr := current.Stat()
		current.Close()
		if statErr != nil {
			return out, statErr
		}
		nextHead, err := readFileHEAD(ctx, s.git, root.Path())
		if err != nil {
			return out, err
		}
		if nextHead != head || !os.SameFile(before, after) || !os.SameFile(after, currentInfo) || before.Size() != after.Size() || after.Size() != currentInfo.Size() || !before.ModTime().Equal(after.ModTime()) || !after.ModTime().Equal(currentInfo.ModTime()) || int64(len(data)) != after.Size() {
			return out, ErrFileVersionChanged
		}
	}
	visibleRoot, err = os.Stat(root.Path())
	if err != nil || !os.SameFile(pinnedRoot, visibleRoot) {
		return out, ErrFileVersionChanged
	}
	if len(data) > MaxFileObservationBytes {
		return out, ErrFileTooLarge
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if in.ExpectedContentSHA256 != "" && in.ExpectedContentSHA256 != hash {
		return out, ErrFileVersionChanged
	}
	if in.Offset > int64(len(data)) {
		return out, ErrFileReadBounds
	}
	start := int(in.Offset)
	end := min(len(data), start+in.MaxBytes)
	part := data[start:end]
	encoding, content := "UTF8", string(part)
	if !utf8.Valid(part) || bytes.IndexByte(part, 0) >= 0 {
		encoding = "BASE64"
		content = base64.StdEncoding.EncodeToString(part)
	}
	firstLine := bytes.Count(data[:start], []byte{'\n'}) + 1
	lastLine := firstLine + bytes.Count(part, []byte{'\n'})
	if len(part) == 0 {
		lastLine = firstLine - 1
	}
	return FileReadResult{ProjectID: value.ProjectID, RepositoryID: value.ID, SourceKind: kind, Path: in.Path, HeadCommit: head, ContentSHA256: hash, SizeBytes: int64(len(data)), Offset: in.Offset, NextOffset: int64(end), StartLine: firstLine, EndLine: lastLine, Truncated: end < len(data), Encoding: encoding, Content: content}, nil
}

// The file-read observation must not fetch missing Git objects or reinterpret
// replacement objects. Other Git services keep their existing contracts.
func readFileHEAD(ctx context.Context, client any, root string) (string, error) {
	reader, ok := client.(interface {
		ReadFileHEAD(context.Context, string) (string, error)
	})
	if !ok {
		return "", errors.New("pure repository HEAD read unavailable")
	}
	return reader.ReadFileHEAD(ctx, root)
}
