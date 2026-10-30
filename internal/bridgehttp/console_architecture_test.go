package bridgehttp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConsoleArchitectureBoundaries(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source location")
	}
	internalRoot := filepath.Dir(filepath.Dir(current))
	for _, directory := range []string{filepath.Join(internalRoot, "bridgehttp"), filepath.Join(internalRoot, "consoleapi")} {
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, forbidden := range []string{"internal/storage/sqlite", `"database/sql"`} {
				if strings.Contains(string(contents), forbidden) {
					t.Errorf("UI/read handler %s imports direct storage dependency %q", path, forbidden)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	err := filepath.WalkDir(internalRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Cross-adapter tests compose public transports; production dependencies stay inward.
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, string(filepath.Separator)+"bridgehttp"+string(filepath.Separator)) || strings.Contains(path, string(filepath.Separator)+"consoleapi"+string(filepath.Separator)) {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, forbidden := range []string{"internal/bridgehttp", "internal/consoleapi"} {
			if strings.Contains(string(contents), forbidden) {
				t.Errorf("domain/control package %s imports UI package %q", path, forbidden)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
