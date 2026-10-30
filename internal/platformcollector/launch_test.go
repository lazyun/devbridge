package platformcollector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

func TestNativeLaunchRechecksCapturedArtifacts(t *testing.T) {
	root := t.TempDir()
	files := []string{filepath.Join(root, "launcher"), filepath.Join(root, "native"), filepath.Join(root, "adapter")}
	original := []byte("\x7fELFcaptured-native-test-bytes")
	for _, path := range files {
		if err := os.WriteFile(path, original, 0700); err != nil {
			t.Fatal(err)
		}
	}
	hash := digest(original)
	p := &Provider{manifest: Manifest{LauncherPath: files[0], LauncherDigest: hash, NativePath: files[1], NativeDigest: hash, AdapterArtifactPath: files[2], AdapterDigest: hash}, descriptors: map[string]runtimeplatform.Descriptor{"IMPLEMENTATION": {DescriptorID: "captured"}}}
	launch, err := p.NativeLaunch(context.Background(), "captured")
	if err != nil {
		t.Fatal(err)
	}
	if path, err := launch.Executable(context.Background()); err != nil || path != files[1] {
		t.Fatalf("captured native=%q %v", path, err)
	}
	for _, path := range files {
		if err = os.WriteFile(path, []byte("replaced"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err = launch.Executable(context.Background()); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
			t.Fatalf("changed %s accepted: %v", filepath.Base(path), err)
		}
		if err = os.WriteFile(path, original, 0700); err != nil {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = launch.Executable(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch=%v", err)
	}
	if _, err = p.NativeLaunch(context.Background(), "foreign-descriptor"); !errors.Is(err, runtimeplatform.ErrDescriptorMissing) {
		t.Fatalf("foreign descriptor acquired launch: %v", err)
	}
}
