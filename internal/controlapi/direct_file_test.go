package controlapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
)

func TestDirectFileErrorCodesRemainStructured(t *testing.T) {
	for _, code := range []string{directfile.NotEnabled, directfile.CapabilityActive, directfile.CodexActive, directfile.WorktreeUnavailable, directfile.InvalidPath, directfile.OutsideWorktree, directfile.SymlinkEscape, directfile.NotFound, directfile.AlreadyExists, directfile.InvalidTarget, directfile.DestinationAlreadyExists, directfile.TooLarge, directfile.OperationFailed} {
		mapped := MapError(fmt.Errorf("wrapped: %w", directfile.Failure(code, "bounded failure")))
		if string(mapped.Code) != code || mapped.Message != "bounded failure" {
			t.Fatalf("%s => %+v", code, mapped)
		}
	}
}

func TestDirectFileFacadeRejectsReviewScopedGenericOperations(t *testing.T) {
	f := &Facade{deps: Dependencies{DirectFiles: directfile.NewService(&facadeFileAuthority{})}}
	target := &review.UpperTarget{AISessionID: "session", FeatureAreaID: "area", TaskID: "task", ReviewID: "review", RunID: "run", RuntimeEpoch: "epoch", GrantGeneration: 1}
	for _, call := range []func(context.Context, directfile.Input) (directfile.Result, error){f.FileList, f.FileRead, f.FileCreate, f.FileReplace, f.FileDelete, f.FileMove} {
		if _, err := call(context.Background(), directfile.Input{ReviewTarget: target}); err == nil {
			t.Fatal("generic Direct File endpoint accepted review-scoped input")
		}
	}
}

func TestDirectFileUnconfiguredFacadeFailsClosed(t *testing.T) {
	f := &Facade{}
	for _, call := range []func(context.Context, directfile.Input) (directfile.Result, error){f.FileList, f.FileRead, f.FileCreate, f.FileReplace, f.FileDelete, f.FileMove} {
		_, err := call(context.Background(), directfile.Input{})
		var api *Error
		if !errors.As(err, &api) || string(api.Code) != directfile.NotEnabled {
			t.Fatalf("error = %v", err)
		}
	}
}

type facadeFileAuthority struct {
	root   *os.Root
	active bool
	scope  directfile.Scope
}

func (a *facadeFileAuthority) Enable(_ context.Context, s directfile.Scope) (directfile.State, error) {
	a.scope = s
	a.active = true
	return directfile.State{Enabled: true}, nil
}
func (a *facadeFileAuthority) Disable(_ context.Context, _ directfile.Scope) (directfile.State, error) {
	a.active = false
	return directfile.State{}, nil
}
func (a *facadeFileAuthority) Status(context.Context, string, string) (directfile.State, error) {
	return directfile.State{Enabled: a.active}, nil
}
func (a *facadeFileAuthority) WithCapability(_ context.Context, s directfile.Scope, complete bool, fn func(*os.Root) error) error {
	if !a.active || a.scope != s {
		return directfile.Failure(directfile.NotEnabled, "not enabled")
	}
	if complete {
		defer func() { a.active = false }()
	}
	return fn(a.root)
}

func TestDirectFileFacadeWorkflowPreservesBytesAndClosesAfterFinalRead(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &facadeFileAuthority{root: root}
	f := &Facade{deps: Dependencies{DirectFiles: directfile.NewService(a)}}
	scope := directfile.Scope{AISessionID: "session", FeatureAreaID: "area", WorkflowID: "workflow"}
	ctx := context.Background()
	if _, err = f.DirectFileEnable(ctx, scope); err != nil {
		t.Fatal(err)
	}
	content := "  leading\r\nUnicode 中文\ntrailing  "
	if _, err = f.FileCreate(ctx, directfile.Input{Scope: scope, Path: "new/whole.txt", Content: content}); err != nil {
		t.Fatal(err)
	}
	if state, err := f.DirectFileStatus(ctx, "session", "area"); err != nil || !state.Enabled {
		t.Fatalf("%+v %v", state, err)
	}
	got, err := f.FileRead(ctx, directfile.Input{Scope: scope, Path: "new/whole.txt", WorkflowComplete: true})
	if err != nil || got.Content != content || !got.WorkflowComplete {
		t.Fatalf("%+v %v", got, err)
	}
	if state, err := f.DirectFileStatus(ctx, "session", "area"); err != nil || state.Enabled {
		t.Fatalf("%+v %v", state, err)
	}
	if _, err = f.FileRead(ctx, directfile.Input{Scope: scope, Path: "new/whole.txt"}); err == nil {
		t.Fatal("completed workflow retained authority")
	}
	if _, err = f.DirectFileDisable(ctx, scope); err != nil {
		t.Fatal(err)
	}
}

func TestDirectFileFacadeAcceptsExactly256KiBDecodedBase64(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	a := &facadeFileAuthority{root: root}
	f := &Facade{deps: Dependencies{DirectFiles: directfile.NewService(a)}}
	scope := directfile.Scope{AISessionID: "session", FeatureAreaID: "area", WorkflowID: "workflow"}
	if _, err = f.DirectFileEnable(context.Background(), scope); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, directfile.MaxWriteBytes))
	value, err := f.FileCreate(context.Background(), directfile.Input{Scope: scope, Path: "exact.bin", Content: encoded, ContentEncoding: "base64"})
	if err != nil || value.Size != directfile.MaxWriteBytes {
		t.Fatalf("exact 256KiB base64 create=%+v err=%v", value, err)
	}
	over := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, directfile.MaxWriteBytes+1))
	if _, err = f.FileCreate(context.Background(), directfile.Input{Scope: scope, Path: "over.bin", Content: over, ContentEncoding: "base64"}); err == nil {
		t.Fatal("decoded 256KiB+1 base64 create succeeded")
	} else {
		var api *Error
		if !errors.As(err, &api) || string(api.Code) != directfile.TooLarge {
			t.Fatalf("decoded oversize error=%v", err)
		}
	}
}
