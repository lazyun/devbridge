package execresolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBinary(t *testing.T, path string, executable bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0644)
	if executable {
		mode = 0o755
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

// canonicalRoot resolves the temporary root the way the resolver does, so
// expectations match macOS /var -> /private/var canonicalization.
func canonicalRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestResolveConfiguredPathWins(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", CoreBinary), true)
	configured := filepath.Join(root, "custom", "devbridge")
	writeBinary(t, configured, true)
	path, source, err := Resolve(Core, configured, Options{ExecutablePath: filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop"), Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if path != configured || source != "configured" {
		t.Fatalf("path=%q source=%q", path, source)
	}
}

func TestResolveConfiguredBareCommandNameKeepsPATHSemantics(t *testing.T) {
	path, source, err := Resolve(Core, "custom-devbridge", Options{Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "custom-devbridge" || source != "PATH" {
		t.Fatalf("path=%q source=%q", path, source)
	}
}

// Regression: the shipped default bare name ("devbridge", from persisted
// settings) must run the adjacent-layout discovery chain instead of being
// returned verbatim, otherwise a clean Finder launch never finds build/bin.
func TestResolveDefaultBareNameUsesAdjacentLayout(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", CoreBinary), true)
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, source, err := Resolve(Core, CoreBinary, Options{ExecutablePath: executable, Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonicalRoot(t, root), "bin", CoreBinary) || source != "adjacent build" {
		t.Fatalf("path=%q source=%q", path, source)
	}
}

func TestResolveNotFoundReturnsBareNameForSpawn(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, source, err := Resolve(Core, "", Options{ExecutablePath: executable, Env: []string{}})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
	if path != CoreBinary || source != "PATH" {
		t.Fatalf("path=%q source=%q, want bare name kept for spawn", path, source)
	}
}

func TestResolveCustomBareNameNotFoundStillReturnsVerbatim(t *testing.T) {
	path, source, err := Resolve(MCP, "custom-mcp", Options{Env: []string{}})
	if err != nil {
		t.Fatalf("custom bare names stay non-fatal, got %v", err)
	}
	if path != "custom-mcp" || source != "PATH" {
		t.Fatalf("path=%q source=%q", path, source)
	}
}

func TestResolveAdjacentBundleBinBeforePATH(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", CoreBinary), true)
	onPATH := filepath.Join(root, "elsewhere")
	writeBinary(t, filepath.Join(onPATH, CoreBinary), true)
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, source, err := Resolve(Core, "", Options{
		ExecutablePath: executable,
		Env:            []string{"PATH=" + onPATH},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonicalRoot(t, root), "bin", CoreBinary) {
		t.Fatalf("path=%q", path)
	}
	if source != "adjacent build" {
		t.Fatalf("source=%q", source)
	}
}

func TestResolveBundleRootWithSpacesAndSymlink(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "my builds")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	writeBinary(t, filepath.Join(root, "bin", MCPBinary), true)
	// The executable is reached through a symlinked directory.
	link := filepath.Join(outer, "latest")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(link, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, _, err := Resolve(MCP, "", Options{ExecutablePath: executable, Env: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonicalRoot(t, root), "bin", MCPBinary) {
		t.Fatalf("path=%q", path)
	}
}

func TestResolveNonExecutableAdjacentBinaryFailsFast(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", CoreBinary), false)
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, _, err := Resolve(Core, "", Options{ExecutablePath: executable, Env: []string{"PATH=" + root}})
	if err == nil {
		t.Fatalf("path=%q, want NotExecutableError", path)
	}
	var notExec *NotExecutableError
	if !errors.As(err, &notExec) {
		t.Fatalf("err = %v, want NotExecutableError", err)
	}
	if !strings.HasSuffix(notExec.Path, filepath.Join("bin", CoreBinary)) {
		t.Fatalf("path = %q", notExec.Path)
	}
}

func TestResolveMissingBinaryEverywhere(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	_, _, err := Resolve(Core, "", Options{ExecutablePath: executable, Env: []string{}})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
	if notFound.Name != CoreBinary || notFound.Selection != Core {
		t.Fatalf("notFound = %+v", notFound)
	}
}

func TestResolvePATHFallbackWhenNoAdjacentBinary(t *testing.T) {
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "tools", LocalMCPBinary), true)
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, source, err := Resolve(LocalMCP, "", Options{
		ExecutablePath: executable,
		Env:            []string{"PATH=" + filepath.Join(root, "tools")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(root, "tools", LocalMCPBinary) || source != "PATH" {
		t.Fatalf("path=%q source=%q", path, source)
	}
}

func TestResolveThreeBinariesFromSameAdjacentLayout(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{CoreBinary, MCPBinary, LocalMCPBinary} {
		writeBinary(t, filepath.Join(root, "bin", name), true)
	}
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	env := []string{"PATH=" + root}
	paths := map[Selection]string{}
	for _, selection := range []Selection{Core, MCP, LocalMCP} {
		path, source, err := Resolve(selection, "", Options{ExecutablePath: executable, Env: env})
		if err != nil {
			t.Fatal(err)
		}
		if source != "adjacent build" {
			t.Fatalf("%s source=%q, want adjacent build", selection, source)
		}
		paths[selection] = path
	}
	canonical := canonicalRoot(t, root)
	for _, selection := range []Selection{Core, MCP, LocalMCP} {
		if want := filepath.Join(canonical, "bin", DefaultName(selection)); paths[selection] != want {
			t.Fatalf("%s path=%q want %q", selection, paths[selection], want)
		}
	}
}

func TestResolvePATHIndependentOfProcessEnv(t *testing.T) {
	// PATH does not contain any devbridge binary; the adjacent layout must
	// still resolve. The simulated PATH points at an unrelated directory.
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", MCPBinary), true)
	unrelated := t.TempDir()
	writeBinary(t, filepath.Join(unrelated, MCPBinary), true)
	executable := filepath.Join(root, "Dev Bridge Desktop.app", "Contents", "MacOS", "devbridge-desktop")
	path, source, err := Resolve(MCP, "", Options{
		ExecutablePath: executable,
		Env:            []string{"PATH=" + unrelated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(canonicalRoot(t, root), "bin", MCPBinary) {
		t.Fatalf("path=%q resolved from the wrong location", path)
	}
	if source == "PATH" {
		t.Fatal("PATH was used although an adjacent binary exists")
	}
}

func TestSearchRootsRawDevelopmentExecutable(t *testing.T) {
	// A raw executable living outside a bundle searches its own directory.
	root := t.TempDir()
	writeBinary(t, filepath.Join(root, "bin", CoreBinary), true)
	executable := filepath.Join(root, "desktop", "build", "bin", "devbridge-desktop")
	roots := searchRoots(executable)
	joined := strings.Join(roots, string(os.PathListSeparator))
	if !strings.Contains(joined, filepath.Join(root, "desktop", "build", "bin")) {
		t.Fatalf("roots=%v missing executable directory", roots)
	}
	if !strings.Contains(joined, root) {
		t.Fatalf("roots=%v missing repository root", roots)
	}
}

func TestSearchRootsDerivedFromRealExecutable(t *testing.T) {
	// The test binary itself lives in a temp package directory; searchRoots
	// must derive candidates from os.Executable without error.
	roots := searchRoots("")
	if len(roots) == 0 {
		t.Fatal("no search roots derived from the running executable")
	}
}

func TestBundleRootLayout(t *testing.T) {
	cases := []struct {
		input    string
		expected string
		ok       bool
	}{
		{"/tmp/build/Dev Bridge Desktop.app/Contents/MacOS/Dev Bridge Desktop", "/tmp/build", true},
		{"/tmp/build/Dev Bridge Desktop.app/Contents/MacOS", "", false},
		{"/tmp/build/Dev Bridge Desktop.app/Contents", "", false},
		{"/tmp/build/Dev Bridge Desktop.app", "", false},
		{"/tmp/build/Contents/MacOS/exe", "", false},
	}
	for _, testCase := range cases {
		root, ok := bundleRoot(testCase.input)
		if ok != testCase.ok || (ok && root != testCase.expected) {
			t.Fatalf("bundleRoot(%q) = %q, %v", testCase.input, root, ok)
		}
	}
}
