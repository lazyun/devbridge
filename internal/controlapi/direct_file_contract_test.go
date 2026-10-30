package controlapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
)

// Guard the deliberately narrow public file capability against acquiring a
// second writer API, Git authority, or a caller-selected filesystem root.
func TestDirectFileBoundaryHasNoProcessGitOrPatchAuthority(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeOf(directfile.Scope{}), reflect.TypeOf(directfile.Input{})} {
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			for _, forbidden := range []string{"root", "expectedsha", "patch", "append", "lease", "shell"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("public argument %s", typ.Field(i).Name)
				}
			}
		}
	}
	files, err := os.ReadDir("../directfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range files {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join("../directfile", entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if imp.Path.Value == `"os/exec"` || strings.Contains(imp.Path.Value, "internal/git") || strings.Contains(imp.Path.Value, "codex") {
				t.Fatalf("file service imported authority %s", imp.Path.Value)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "RemoveAll" {
				t.Fatalf("recursive deletion in %s", entry.Name())
			}
			return true
		})
	}
}
