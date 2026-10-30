package mcpserver_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestDomainPackagesDoNotDependOnMCP(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate architecture test")
	}
	internalRoot := filepath.Clean(filepath.Join(filepath.Dir(filename), ".."))
	forbidden := []string{
		"github.com/modelcontextprotocol/",
		"github.com/lazyun/devbridge/internal/mcp",
	}

	err := filepath.WalkDir(internalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// MCP adapter packages are allowed to depend on MCP; all other
			// internal packages are the protected inward-facing side. The
			// Local MCP adapter is a second, independent MCP adapter.
			if path != internalRoot && (strings.HasPrefix(entry.Name(), "mcp") || entry.Name() == "localmcp") {
				return filepath.SkipDir
			}
			return nil
		}
		// This fence protects production dependency direction. Black-box
		// integration tests may exercise an adapter from a domain package;
		// importing it in a _test.go file does not make it a production
		// dependency.
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
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
			for _, prefix := range forbidden {
				if strings.HasPrefix(value, prefix) {
					t.Errorf("domain/control-plane file %s imports MCP dependency %q", path, value)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal packages: %v", err)
	}
}
