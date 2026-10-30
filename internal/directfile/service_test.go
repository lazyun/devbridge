package directfile

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type testAuthority struct {
	root     *os.Root
	enabled  bool
	disabled int
}

func newTestService(t *testing.T) (*Service, *testAuthority, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	a := &testAuthority{root: root}
	return NewService(a), a, dir
}

func (a *testAuthority) Enable(_ context.Context, _ Scope) (State, error) {
	a.enabled = true
	return State{Enabled: true}, nil
}
func (a *testAuthority) Disable(_ context.Context, _ Scope) (State, error) {
	a.enabled = false
	a.disabled++
	return State{}, nil
}
func (a *testAuthority) Status(_ context.Context, _, _ string) (State, error) {
	return State{Enabled: a.enabled}, nil
}
func (a *testAuthority) WithCapability(_ context.Context, _ Scope, complete bool, fn func(*os.Root) error) error {
	if !a.enabled {
		return Failure(NotEnabled, "not enabled")
	}
	err := fn(a.root)
	if err == nil && complete {
		a.enabled = false
		a.disabled++
	}
	return err
}

func scopeInput(path string) Input {
	return Input{Scope: Scope{AISessionID: "s", FeatureAreaID: "a", WorkflowID: "w"}, Path: path}
}

func errorCode(t *testing.T, err error) string {
	t.Helper()
	var direct *Error
	if !errors.As(err, &direct) {
		t.Fatalf("expected directfile Error, got %v", err)
	}
	return direct.Code
}

func TestServiceRequiresCapabilityAndCompletesWorkflow(t *testing.T) {
	s, a, _ := newTestService(t)
	ctx := context.Background()
	in := scopeInput("a.txt")
	if _, err := s.Create(ctx, Input{Scope: in.Scope, Path: in.Path, Content: "a"}); errorCode(t, err) != NotEnabled {
		t.Fatal(err)
	}
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	in.Content, in.WorkflowComplete = "a", true
	got, err := s.Create(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.WorkflowComplete || a.disabled != 1 || a.enabled {
		t.Fatalf("completion=%+v disabled=%d enabled=%v", got, a.disabled, a.enabled)
	}
}

func TestWorkflowRemainsEnabledUntilFinalReadOrList(t *testing.T) {
	s, a, _ := newTestService(t)
	ctx := context.Background()
	base := scopeInput("source")
	if _, err := s.Enable(ctx, base.Scope); err != nil {
		t.Fatal(err)
	}
	base.Content = "one"
	if _, err := s.Create(ctx, base); err != nil || !a.enabled {
		t.Fatalf("create err=%v enabled=%v", err, a.enabled)
	}
	base.Content = "two"
	if _, err := s.Replace(ctx, base); err != nil || !a.enabled {
		t.Fatalf("replace err=%v enabled=%v", err, a.enabled)
	}
	move := scopeInput("source")
	move.Destination = "destination"
	if _, err := s.Move(ctx, move); err != nil || !a.enabled {
		t.Fatalf("move err=%v enabled=%v", err, a.enabled)
	}
	read := scopeInput("destination")
	if got, err := s.Read(ctx, read); err != nil || got.WorkflowComplete || !a.enabled {
		t.Fatalf("read=%+v err=%v enabled=%v", got, err, a.enabled)
	}
	list := scopeInput(".")
	list.WorkflowComplete = true
	got, err := s.List(ctx, list)
	if err != nil || !got.WorkflowComplete || a.enabled || a.disabled != 1 {
		t.Fatalf("list=%+v err=%v enabled=%v disabled=%d", got, err, a.enabled, a.disabled)
	}
}

func TestWholeFileOperationsAndNoOverwrite(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("new/deep/file.txt")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	in.Content = "first"
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, in.Path)); err != nil || string(got) != "first" {
		t.Fatalf("disk=%q err=%v", got, err)
	}
	if _, err := s.Create(ctx, in); errorCode(t, err) != AlreadyExists {
		t.Fatal(err)
	}
	in.Content = "second"
	if _, err := s.Replace(ctx, in); err != nil {
		t.Fatal(err)
	}
	read, err := s.Read(ctx, scopeInput(in.Path))
	if err != nil || read.Content != "second" || read.Size != 6 {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	move := scopeInput(in.Path)
	move.Destination = "moved.txt"
	if _, err = s.Move(ctx, move); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(ctx, scopeInput(in.Path)); errorCode(t, err) != NotFound {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, scopeInput("moved.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Delete(ctx, scopeInput("moved.txt")); errorCode(t, err) != NotFound {
		t.Fatal(err)
	}
}

func TestMoveNeverOverwritesDestination(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("source")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	for _, pair := range []struct{ path, content string }{{"source", "source"}, {"destination", "destination"}} {
		create := scopeInput(pair.path)
		create.Content = pair.content
		if _, err := s.Create(ctx, create); err != nil {
			t.Fatal(err)
		}
	}
	move := scopeInput("source")
	move.Destination = "destination"
	if _, err := s.Move(ctx, move); errorCode(t, err) != AlreadyExists {
		t.Fatal(err)
	}
	for _, pair := range []struct{ path, want string }{{"source", "source"}, {"destination", "destination"}} {
		got, err := os.ReadFile(filepath.Join(dir, pair.path))
		if err != nil || string(got) != pair.want {
			t.Fatalf("%s=%q err=%v", pair.path, got, err)
		}
	}
}

func TestConcurrentCreatePublishesOnlyOneDestination(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("race")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			create := scopeInput("race")
			create.Content = "complete"
			_, err := s.Create(ctx, create)
			results <- err
		}()
	}
	success := 0
	for i := 0; i < cap(results); i++ {
		err := <-results
		if err == nil {
			success++
			continue
		}
		if errorCode(t, err) != AlreadyExists {
			t.Fatalf("unexpected create result: %v", err)
		}
	}
	if success != 1 {
		t.Fatalf("successful publishes = %d, want 1", success)
	}
	got, err := os.ReadFile(filepath.Join(dir, "race"))
	if err != nil || string(got) != "complete" {
		t.Fatalf("target=%q err=%v", got, err)
	}
}

func TestResolverRejectsEscapesGitAndSymlinks(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("x")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../outside", "a/../../outside", "/tmp/outside", ".git/config", "a/.GiT/config"} {
		create := scopeInput(path)
		create.Content = "x"
		if _, err := s.Create(ctx, create); err == nil {
			t.Fatalf("%q unexpectedly accepted", path)
		}
	}
	if err := os.Symlink("/tmp", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	create := scopeInput("link/escape")
	create.Content = "x"
	if _, err := s.Create(ctx, create); errorCode(t, err) != SymlinkEscape {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "real"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, scopeInput("inside")); errorCode(t, err) != SymlinkEscape {
		t.Fatal(err)
	}
}

func TestMissingAndDirectoryTargetsAndExplicitDisable(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("missing")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(ctx, in); errorCode(t, err) != NotFound {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o755); err != nil {
		t.Fatal(err)
	}
	directory := scopeInput("directory")
	if _, err := s.Replace(ctx, directory); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
	move := scopeInput("missing")
	move.Destination = "new/parent/destination"
	if _, err := s.Move(ctx, move); errorCode(t, err) != NotFound {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move created destination parent: %v", err)
	}
	if _, err := s.Disable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, in); errorCode(t, err) != NotEnabled {
		t.Fatal(err)
	}
	if _, err := s.List(ctx, scopeInput(".")); errorCode(t, err) != NotEnabled {
		t.Fatal(err)
	}
}

func TestReadListBoundsAndSpecialTargets(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("large")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large"), []byte(strings.Repeat("x", MaxReadBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, in); errorCode(t, err) != TooLarge {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "original"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	over := scopeInput("original")
	over.Content = strings.Repeat("x", MaxWriteBytes+1)
	if _, err := s.Replace(ctx, over); errorCode(t, err) != TooLarge {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "original")); err != nil || string(got) != "original" {
		t.Fatalf("oversize replace changed target %q: %v", got, err)
	}
	newLarge := scopeInput("too-large")
	newLarge.Content = strings.Repeat("x", MaxWriteBytes+1)
	if _, err := s.Create(ctx, newLarge); errorCode(t, err) != TooLarge {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "too-large")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversize create published a target: %v", err)
	}
	if _, err := s.Replace(ctx, scopeInput(".")); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(ctx, scopeInput("folder")); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
	for i := 0; i < MaxListEntries+2; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("entry-%03d", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List(ctx, scopeInput("."))
	if err != nil {
		t.Fatal(err)
	}
	if !list.Truncated || len(list.Entries) != MaxListEntries {
		t.Fatalf("list truncated=%v entries=%d", list.Truncated, len(list.Entries))
	}
	if _, err := s.List(ctx, scopeInput("")); errorCode(t, err) != InvalidPath {
		t.Fatal(err)
	}
}

func TestListReportsSymlinkAndReadRejectsFIFO(t *testing.T) {
	s, _, dir := newTestService(t)
	ctx := context.Background()
	in := scopeInput("target")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "target"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx, scopeInput("."))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range list.Entries {
		if entry.Name == "link" && entry.Type == "symlink" {
			found = true
		}
	}
	if !found {
		t.Fatalf("symlink missing from %+v", list.Entries)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, scopeInput("pipe")); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
}

func TestBase64ContentRoundTripsArbitraryBytes(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	in := scopeInput("bytes")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	raw := []byte{0xff, 0xfe, 0, 'x'}
	in.Content, in.ContentEncoding = base64.StdEncoding.EncodeToString(raw), "base64"
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, scopeInput("bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentEncoding != "base64" || got.Content != base64.StdEncoding.EncodeToString(raw) {
		t.Fatalf("got %+v", got)
	}
	in.Path, in.Content = "bad", "not base64!"
	if _, err := s.Create(ctx, in); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
}

func TestInputBytesAppliesWriteLimitAfterBase64Decode(t *testing.T) {
	for _, size := range []int{191 << 10, 200 << 10, 255 << 10, MaxWriteBytes} {
		t.Run(fmt.Sprintf("base64_%d", size), func(t *testing.T) {
			raw := []byte(strings.Repeat("x", size))
			got, err := inputBytes(Input{Content: base64.StdEncoding.EncodeToString(raw), ContentEncoding: "base64"})
			if err != nil || len(got) != size {
				t.Fatalf("decoded=%d err=%v, want %d bytes", len(got), err, size)
			}
		})
	}

	tooLarge := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", MaxWriteBytes+1)))
	if _, err := inputBytes(Input{Content: tooLarge, ContentEncoding: "base64"}); errorCode(t, err) != TooLarge {
		t.Fatal(err)
	}
	if _, err := inputBytes(Input{Content: "not base64!", ContentEncoding: "base64"}); errorCode(t, err) != InvalidTarget {
		t.Fatal(err)
	}
	if _, err := inputBytes(Input{Content: strings.Repeat("A", MaxBase64EncodedBytes+1), ContentEncoding: "base64"}); errorCode(t, err) != TooLarge {
		t.Fatal(err)
	}
	for _, size := range []int{MaxWriteBytes, MaxWriteBytes + 1} {
		_, err := inputBytes(Input{Content: strings.Repeat("x", size), ContentEncoding: "utf8"})
		if size == MaxWriteBytes && err != nil {
			t.Fatalf("plain %d bytes rejected: %v", size, err)
		}
		if size > MaxWriteBytes && errorCode(t, err) != TooLarge {
			t.Fatalf("plain %d bytes error: %v", size, err)
		}
	}
}

func TestUtf8ContentPreservesLeadingTrailingAndCRLFBytes(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	in := scopeInput("text")
	if _, err := s.Enable(ctx, in.Scope); err != nil {
		t.Fatal(err)
	}
	in.Content = "  leading\r\nbody\r\ntrailing  "
	if _, err := s.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, scopeInput("text"))
	if err != nil || got.ContentEncoding != "utf8" || got.Content != in.Content {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
