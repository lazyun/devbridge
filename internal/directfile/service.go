package directfile

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

// Service exposes the deliberately small, whole-file Direct File capability.
// Authority owns admission and supplies a Root pinned to the canonical worktree
// for every operation.
type Service struct{ authority Authority }

func NewService(authority Authority) *Service { return &Service{authority: authority} }

func (s *Service) Enable(ctx context.Context, scope Scope) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	return s.authority.Enable(ctx, scope)
}

func (s *Service) Disable(ctx context.Context, scope Scope) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	return s.authority.Disable(ctx, scope)
}

func (s *Service) Status(ctx context.Context, session, area string) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	return s.authority.Status(ctx, session, area)
}

// StartTaskAuthorization is intentionally separate from Status and Enable.
// Only a consumed purpose-scoped confirmation supplies StartInput.
func (s *Service) StartTaskAuthorization(ctx context.Context, in StartInput) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	a, ok := s.authority.(TaskStartAuthority)
	if !ok {
		return State{}, Failure(NotEnabled, "task direct-file authorization is unavailable")
	}
	return a.StartTaskAuthorization(ctx, in)
}

// SetAfterFileEffectHookForTest exposes only the crash-boundary seam of a
// concrete authority. It is unavailable to production callers that provide
// an ordinary Authority implementation.
func (s *Service) SetAfterFileEffectHookForTest(fn func() error) bool {
	if s == nil || s.authority == nil {
		return false
	}
	setter, ok := s.authority.(interface{ SetAfterFileEffectHook(func() error) })
	if !ok {
		return false
	}
	setter.SetAfterFileEffectHook(fn)
	return true
}

func (s *Service) List(ctx context.Context, in Input) (out Result, err error) {
	ctx = withOperation(ctx, operationFromInput("LIST", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		name, e := resolve(root, in.Path, true)
		if e != nil {
			return e
		}
		f, e := root.Open(name)
		if e != nil {
			return fileError(e)
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return fileError(e)
		}
		if !info.IsDir() {
			return Failure(InvalidTarget, "path is not a directory")
		}
		entries, e := f.ReadDir(MaxListEntries + 1)
		if e != nil && !errors.Is(e, io.EOF) {
			return fileError(e)
		}
		out.Path = name
		if len(entries) > MaxListEntries {
			out.Truncated, entries = true, entries[:MaxListEntries]
		}
		out.Entries = make([]Entry, 0, len(entries))
		for _, entry := range entries {
			if !entryAllowed(ctx, filepath.Join(name, entry.Name())) {
				continue
			}
			info, e := entry.Info()
			if e != nil {
				return fileError(e)
			}
			typ := ""
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				typ = "symlink"
			case info.IsDir():
				typ = "directory"
			case info.Mode().IsRegular():
				typ = "file"
			default:
				continue // Direct File never exposes special filesystem objects.
			}
			out.Entries = append(out.Entries, Entry{Name: entry.Name(), Type: typ})
		}
		sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func (s *Service) Read(ctx context.Context, in Input) (out Result, err error) {
	ctx = withOperation(ctx, operationFromInput("READ", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		name, e := resolve(root, in.Path, false)
		if e != nil {
			return e
		}
		preflight, e := root.Lstat(name)
		if e != nil {
			return fileError(e)
		}
		if !preflight.Mode().IsRegular() {
			return Failure(InvalidTarget, "path is not a regular file")
		}
		f, e := openForRead(root, name)
		if e != nil {
			return fileError(e)
		}
		defer f.Close()
		info, e := f.Stat()
		if e != nil {
			return fileError(e)
		}
		if !info.Mode().IsRegular() {
			return Failure(InvalidTarget, "path is not a regular file")
		}
		if info.Size() > MaxReadBytes {
			return Failure(TooLarge, "file exceeds read limit")
		}
		b, e := io.ReadAll(io.LimitReader(f, MaxReadBytes+1))
		if e != nil {
			return fileError(e)
		}
		if len(b) > MaxReadBytes {
			return Failure(TooLarge, "file exceeds read limit")
		}
		out = Result{Path: name, Size: int64(len(b))}
		sum := sha256.Sum256(b)
		out.Digest = fmt.Sprintf("%x", sum[:])
		if utf8.Valid(b) {
			out.Content, out.ContentEncoding = string(b), "utf8"
		} else {
			out.Content, out.ContentEncoding = base64.StdEncoding.EncodeToString(b), "base64"
		}
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func (s *Service) Create(ctx context.Context, in Input) (out Result, err error) {
	ctx = withOperation(ctx, operationFromInput("CREATE", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		content, e := inputBytes(in)
		if e != nil {
			return e
		}
		if len(content) > MaxWriteBytes {
			return Failure(TooLarge, "content exceeds write limit")
		}
		name, e := resolve(root, in.Path, true)
		if e != nil {
			return e
		}
		if e = ensureParents(root, name); e != nil {
			return e
		}
		if _, e = root.Lstat(name); e == nil {
			return Failure(AlreadyExists, "destination already exists")
		} else if !errors.Is(e, os.ErrNotExist) {
			return fileError(e)
		}
		if e = publishCreate(root, name, content); e != nil {
			return e
		}
		out = Result{Path: name, Size: int64(len(content))}
		sum := sha256.Sum256(content)
		out.Digest = fmt.Sprintf("%x", sum[:])
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func (s *Service) Replace(ctx context.Context, in Input) (out Result, err error) {
	ctx = withOperation(ctx, operationFromInput("REPLACE", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		content, e := inputBytes(in)
		if e != nil {
			return e
		}
		if len(content) > MaxWriteBytes {
			return Failure(TooLarge, "content exceeds write limit")
		}
		name, e := resolve(root, in.Path, false)
		if e != nil {
			return e
		}
		info, e := root.Lstat(name)
		if e != nil {
			return fileError(e)
		}
		if !info.Mode().IsRegular() {
			return Failure(InvalidTarget, "path is not a regular file")
		}
		if e = publishReplace(root, name, content); e != nil {
			return e
		}
		out = Result{Path: name, Size: int64(len(content))}
		sum := sha256.Sum256(content)
		out.Digest = fmt.Sprintf("%x", sum[:])
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func (s *Service) Delete(ctx context.Context, in Input) (out Result, err error) {
	ctx = withOperation(ctx, operationFromInput("DELETE", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		name, e := resolve(root, in.Path, false)
		if e != nil {
			return e
		}
		info, e := root.Lstat(name)
		if e != nil {
			return fileError(e)
		}
		if !info.Mode().IsRegular() {
			return Failure(InvalidTarget, "path is not a regular file")
		}
		if e = removeFile(root, name); e != nil {
			return fileError(e)
		}
		out = Result{Path: name, Size: info.Size()}
		out.Digest = "ABSENT"
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func (s *Service) Move(ctx context.Context, in Input) (out Result, err error) {
	if (in.DestinationRepositoryID != "" && in.DestinationRepositoryID != in.RepositoryID) || (in.DestinationMemberID != "" && in.DestinationMemberID != in.MemberID) {
		return Result{}, Failure("CROSS_REPOSITORY_MOVE_UNSUPPORTED", "cross-repository move requires separate copy and delete authorizations")
	}

	ctx = withOperation(ctx, operationFromInput("MOVE", in))
	ctx = withResultCapture(ctx)
	err = s.withRoot(ctx, in, func(root *os.Root) error {
		source, e := resolve(root, in.Path, false)
		if e != nil {
			return e
		}
		destination, e := resolve(root, in.Destination, true)
		if e != nil {
			return e
		}
		info, e := root.Lstat(source)
		if e != nil {
			return fileError(e)
		}
		if !info.Mode().IsRegular() {
			return Failure(InvalidTarget, "source is not a regular file")
		}
		if _, e = root.Lstat(destination); e == nil {
			return Failure(AlreadyExists, "destination already exists")
		} else if !errors.Is(e, os.ErrNotExist) {
			return fileError(e)
		}
		if e = ensureParents(root, destination); e != nil {
			return e
		}
		f, e := openForRead(root, source)
		if e != nil {
			return fileError(e)
		}
		bytes, e := io.ReadAll(io.LimitReader(f, MaxWriteBytes+1))
		_ = f.Close()
		if e != nil || len(bytes) > MaxWriteBytes {
			return Failure(OperationFailed, "cannot capture move source state")
		}
		if e = moveNoReplace(root, source, destination); e != nil {
			return e
		}
		out = Result{Path: destination, Size: info.Size()}
		sum := sha256.Sum256(bytes)
		out.Digest = fmt.Sprintf("%x", sum[:])
		setResultCapture(ctx, out)
		return nil
	})
	return completed(in, capturedResult(ctx, out, err), err)
}

func operationFromInput(kind string, in Input) Operation {
	// Snapshot the correlation token with the operation payload; an adapter's
	// later mutation of its input must not alter the durable intent identity.
	var worker *WorkerClaimIdentity
	if in.WorkerClaim != nil {
		copy := *in.WorkerClaim
		worker = &copy
	}
	return Operation{WorkerClaim: worker, Kind: kind, Path: in.Path, Destination: in.Destination,
		RepositoryID: in.RepositoryID, MemberID: in.MemberID, MemberVersion: in.MemberVersion,
		DestinationRepositoryID: in.DestinationRepositoryID, DestinationMemberID: in.DestinationMemberID,
		RequestID: in.RequestID, RuntimeEpoch: in.RuntimeEpoch, GrantGeneration: in.GrantGeneration, ExpectedDigest: in.ExpectedDigest,
		Content: in.Content, ContentEncoding: in.ContentEncoding}
}

func (s *Service) withRoot(ctx context.Context, in Input, fn func(*os.Root) error) error {
	if s == nil || s.authority == nil {
		return Failure(OperationFailed, "direct file authority is unavailable")
	}
	return s.authority.WithCapability(ctx, in.Scope, in.WorkflowComplete, func(root *os.Root) error {
		// v0.1 uses descriptor-confined publication on the supported Core hosts.
		// Do not silently enable less restrictive os.Root implementations.
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			return Failure(OperationFailed, "Direct File is unsupported on this platform")
		}
		return fn(root)
	})
}

func completed(in Input, out Result, err error) (Result, error) {
	value := out.Content
	if value == "" {
		value = in.Content
	}
	if value != "" && out.Digest == "" {
		sum := sha256.Sum256([]byte(value))
		out.Digest = fmt.Sprintf("%x", sum[:])
	}
	if err == nil {
		out.WorkflowComplete = in.WorkflowComplete
		// Digest is evidence for v2's exact source precondition and operation
		// replay. Reads hash the returned bytes/text; mutations hash submitted
		// content where applicable.
	}
	return out, err
}

func capturedResult(ctx context.Context, fallback Result, err error) Result {
	if err == nil {
		if value, ok := CapturedResult(ctx); ok && (value.Path != "" || value.Digest != "") {
			return value
		}
	}
	return fallback
}

func resolve(root *os.Root, input string, allowMissing bool) (string, error) {
	if len(input) == 0 {
		return "", Failure(InvalidPath, "path is required")
	}
	if len(input) > MaxPathBytes || strings.IndexByte(input, 0) >= 0 || filepath.IsAbs(input) {
		return "", Failure(InvalidPath, "path must be a bounded relative path")
	}
	clean := filepath.Clean(input)
	if clean == "." {
		return clean, nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", Failure(OutsideWorktree, "path escapes the worktree")
	}
	parts := strings.Split(clean, string(filepath.Separator))
	for _, part := range parts {
		if strings.EqualFold(part, ".git") {
			return "", Failure(InvalidPath, "Git metadata is not available")
		}
	}
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", Failure(InvalidPath, "path contains an invalid component")
		}
		info, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && allowMissing {
				break
			}
			return "", fileError(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", Failure(SymlinkEscape, "symbolic links are not supported")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", Failure(InvalidTarget, "path ancestor is not a directory")
		}
	}
	return clean, nil
}

// inputBytes preserves Go string bytes for utf8 mode. Base64 provides the
// explicit JSON-safe representation needed when callers supply arbitrary bytes.
func inputBytes(in Input) ([]byte, error) {
	switch in.ContentEncoding {
	case "", "utf8":
		if len(in.Content) > MaxWriteBytes {
			return nil, Failure(TooLarge, "content exceeds write limit")
		}
		return []byte(in.Content), nil
	case "base64":
		if len(in.Content) > MaxBase64EncodedBytes {
			return nil, Failure(TooLarge, "base64 content exceeds encoded input limit")
		}
		content, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil {
			return nil, Failure(InvalidTarget, "content is not valid base64")
		}
		if len(content) > MaxWriteBytes {
			return nil, Failure(TooLarge, "content exceeds write limit")
		}
		return content, nil
	default:
		return nil, Failure(InvalidTarget, "unsupported content encoding")
	}
}

func ensureParents(root *os.Root, name string) error {
	dir := filepath.Dir(name)
	if dir == "." {
		return nil
	}
	parts := strings.Split(dir, string(filepath.Separator))
	for i := range parts {
		parent := filepath.Join(parts[:i+1]...)
		info, err := root.Lstat(parent)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return Failure(SymlinkEscape, "symbolic links are not supported")
			}
			if !info.IsDir() {
				return Failure(InvalidTarget, "path ancestor is not a directory")
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fileError(err)
		}
		if err = root.Mkdir(parent, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
			return fileError(err)
		}
	}
	return nil
}

func fileError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return Failure(NotFound, "file does not exist")
	}
	if errors.Is(err, os.ErrExist) {
		return Failure(AlreadyExists, "file already exists")
	}
	return Failure(OperationFailed, fmt.Sprintf("filesystem operation failed: %v", err))
}

func (s *Service) TaskStatus(ctx context.Context, session, taskID string) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	a, ok := s.authority.(TaskAuthority)
	if !ok {
		return State{}, Failure(OperationFailed, "task file authority is unavailable")
	}
	return a.TaskStatus(ctx, session, taskID)
}
func (s *Service) ReconcileTaskAuthorization(ctx context.Context, in Registration) (State, error) {
	if s == nil || s.authority == nil {
		return State{}, Failure(OperationFailed, "direct file authority is unavailable")
	}
	a, ok := s.authority.(TaskAuthority)
	if !ok {
		return State{}, Failure(OperationFailed, "task file authority is unavailable")
	}
	return a.ReconcileTaskAuthorization(ctx, in)
}
