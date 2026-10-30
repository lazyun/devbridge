package execution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/platformcollector"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

// The installed native supplies the measured schema and performs initialize.
// The npm-shaped launcher deliberately executes a different program. No model,
// provider request, qualification record, or business authority is fabricated.
func TestQualifiedLaunchUsesMeasuredNativeInsteadOfPackageWrapper(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; no online provider")
	}
	native, err := filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../build/constraint-gap-fixes-2026-10-02/qualified-launch")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "native-wrapper-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("launch evidence:", root)
	home := filepath.Join(root, "home")
	if err = os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", home)
	packageRoot := filepath.Join(root, "package")
	wrapper := filepath.Join(packageRoot, "bin", "codex.js")
	marker := filepath.Join(root, "wrong-wrapper-executed")
	if err = os.MkdirAll(filepath.Dir(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(packageRoot, "package.json"), []byte(`{"name":"@openai/codex"}`), 0600); err != nil {
		t.Fatal(err)
	}
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nprintf wrong > "+quotedMarker+"\nexec /usr/bin/false\n"), 0700); err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[runtime.GOARCH]
	platform := map[string]string{"darwin": "apple-darwin", "linux": "unknown-linux-musl"}[runtime.GOOS]
	if arch == "" || platform == "" {
		t.Skip("native npm resolution supports Darwin and Linux")
	}
	vendorNative := filepath.Join(packageRoot, "vendor", arch+"-"+platform, "bin", "codex")
	if err = os.MkdirAll(filepath.Dir(vendorNative), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(native, vendorNative); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	provider, _, err := platformcollector.New(ctx, platformcollector.Config{CodexPath: wrapper, EvidenceRoot: filepath.Join(root, "artifacts")})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := provider.Descriptor("IMPLEMENTATION")
	if err != nil {
		t.Fatal(err)
	}
	launch, err := provider.NativeLaunch(ctx, descriptor.DescriptorID)
	if err != nil {
		t.Fatal(err)
	}
	// A Settings change after measurement must not participate in this launch.
	selectionFile := filepath.Join(root, "selection.json")
	if err = codexlaunch.Write(selectionFile, codexlaunch.Selection{Error: "changed after verification"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexlaunch.SelectionEnv, selectionFile)
	factory := DefaultCodexFactory{Config: codex.Config{CodexPath: "/missing/default"}}
	session, err := factory.StartForQualifiedRun(ctx, "launch-only", codexconfig.Config{ID: "selected", CodexPath: wrapper}, "", "", "", codex.AuthorizationReadOnly, launch, executionCheckpoint{}, nil)
	if err != nil {
		t.Fatalf("verified native launch used wrapper/default instead: %v", err)
	}
	actual := session.(*codexSession).client.Executable()
	if err = session.Close(); err != nil {
		t.Fatal(err)
	}
	if actual != native {
		t.Fatalf("measured %q, launched %q", native, actual)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmeasured wrapper executed: %v", err)
	}
	// Changes to any qualified artifact remain a refusal, not a fallback to the
	// old wrapper or to the newly selected executable.
	if err = os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 72\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = factory.StartForQualifiedRun(ctx, "changed-launch", codexconfig.Config{CodexPath: wrapper}, "", "", "", codex.AuthorizationReadOnly, launch, executionCheckpoint{}, nil); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
		t.Fatalf("changed measured wrapper accepted: %v", err)
	}
}
