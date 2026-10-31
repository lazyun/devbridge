package mcpserver_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const internalImportRoot = "github.com/lazyun/devbridge/internal/"

// These exact packages are transport-facing: the server adapters and the
// injected outbound worker clients. No prefix, basename or subtree exemption
// is implied. New adapter packages require an explicit architecture decision.
var mcpTransportPackages = map[string]bool{
	"mcp":         true,
	"mcpserver":   true,
	"localmcp":    true,
	"upperworker": true,
	"fileworker":  true,
}

func forbiddenInwardMCPImport(value string) bool {
	if value == "github.com/modelcontextprotocol" || strings.HasPrefix(value, "github.com/modelcontextprotocol/") {
		return true
	}
	for name := range mcpTransportPackages {
		root := internalImportRoot + name
		if value == root || strings.HasPrefix(value, root+"/") {
			return true
		}
	}
	return false
}

// Scan source rather than the current build graph, so platform-specific or
// build-tagged production files cannot bypass the boundary. Only _test.go is
// exempt: black-box integration tests may exercise the outward adapters.
func inwardMCPDependencyViolations(internalRoot string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		} // Always inspect descendants of allowed roots.
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(internalRoot, filepath.Dir(path))
		if err != nil {
			return err
		}
		if mcpTransportPackages[filepath.ToSlash(relative)] {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			value, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if forbiddenInwardMCPImport(value) {
				violations = append(violations, fmt.Sprintf("domain/control-plane file %s imports transport dependency %q", path, value))
			}
		}
		return nil
	})
	return violations, err
}

func TestDomainPackagesDoNotDependOnMCP(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	internalRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	violations, err := inwardMCPDependencyViolations(internalRoot)
	if err != nil {
		t.Fatalf("walk internal packages: %v", err)
	}
	for _, violation := range violations {
		t.Error(violation)
	}
}

func TestMCPArchitectureExactTransportBoundary(t *testing.T) {
	const sdk = "github.com/modelcontextprotocol/go-sdk/mcp"
	type fixtureCase struct {
		name, file, dependency string
		forbidden              bool
	}
	tests := []fixtureCase{
		{"domain-to-domain", "controlapi/service.go", internalImportRoot + "review", false},
		{"domain-test-to-client", "controlapi/service_test.go", internalImportRoot + "upperworker", false},
		{"domain-test-to-sdk", "storage/sqlite/integration_test.go", sdk, false},
		{"domain-sdk", "controlapi/service.go", sdk, true},
		{"domain-sdk-root", "review/service.go", "github.com/modelcontextprotocol", true},
		{"nested-client-name", "controlapi/upperworker/service.go", sdk, true},
		{"nested-server-name", "controlapi/mcpserver/service.go", sdk, true},
		{"mcp-prefix-not-adapter", "mcpdomain/service.go", sdk, true},
		{"client-prefix-not-adapter", "upperworkerextra/service.go", sdk, true},
		{"file-client-prefix-not-adapter", "fileworkerextra/service.go", sdk, true},
		// Import lookalikes are ordinary domain packages, not false-positive edges.
		{"domain-import-lookalike", "controlapi/service.go", internalImportRoot + "upperworkerextra", false},
		{"domain-import-mcp-lookalike", "controlapi/service.go", internalImportRoot + "mcpdomain", false},
		{"test-prefix-not-exempt", "review/test_service.go", sdk, true},
		{"platform-file-not-exempt", "review/service_windows.go", sdk, true},
		{"build-tag-not-exempt", "review/tagged.go", sdk, true},
	}
	for _, name := range []string{"mcp", "mcpserver", "localmcp", "upperworker", "fileworker"} {
		tests = append(tests,
			fixtureCase{"adapter-" + name, name + "/adapter.go", sdk, false},
			fixtureCase{"adapter-to-domain-" + name, name + "/adapter.go", internalImportRoot + "review", false},
			fixtureCase{"reverse-" + name, "controlplane/plane.go", internalImportRoot + name, true},
			fixtureCase{"reverse-descendant-" + name, "task/service.go", internalImportRoot + name + "/child", true},
			fixtureCase{"adapter-descendant-" + name, name + "/child/service.go", sdk, true},
		)
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, filepath.FromSlash(tc.file))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			source := "package fixture\nimport " + strconv.Quote(tc.dependency) + "\n"
			if tc.name == "build-tag-not-exempt" {
				source = "//go:build architecture_fixture_never_enabled\n\n" + source
			}
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			violations, err := inwardMCPDependencyViolations(root)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.forbidden {
				want = 1
			}
			if len(violations) != want {
				t.Fatalf("violations=%v want count=%d", violations, want)
			}
			if tc.forbidden && (!strings.Contains(violations[0], path) || !strings.Contains(violations[0], tc.dependency)) {
				t.Fatalf("incomplete diagnostic: %v", violations)
			}
		})
	}
}

func TestMCPArchitectureScannerSurfacesErrors(t *testing.T) {
	t.Run("missing-root", func(t *testing.T) {
		if _, err := inwardMCPDependencyViolations(filepath.Join(t.TempDir(), "absent")); err == nil {
			t.Fatal("missing source tree silently passed")
		}
	})
	t.Run("invalid-production-import", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "broken.go"), []byte("package fixture\nimport \"unterminated\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := inwardMCPDependencyViolations(root); err == nil {
			t.Fatal("invalid production source silently passed")
		}
	})
}
