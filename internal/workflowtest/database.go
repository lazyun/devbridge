package workflowtest

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

var emptySchema struct {
	sync.Once
	bytes []byte
	err   error
}

// SeedEmptyDatabase copies only the schema produced by the real migrations.
// Workflow tests still run the normal Core bootstrap, writer lock and fresh
// epoch allocation, and own separate database files. Historical migration tests
// deliberately do not use this helper. No Session, qualification or Run is copied.
func SeedEmptyDatabase(t testing.TB, stateRoot string) {
	t.Helper()
	emptySchema.Do(func() {
		root, err := os.MkdirTemp("", "devbridge-empty-test-schema-")
		if err != nil {
			emptySchema.err = err
			return
		}
		defer os.RemoveAll(root)
		path := filepath.Join(root, "devbridge.db")
		raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
		if err != nil {
			emptySchema.err = err
			return
		}
		raw.SetMaxOpenConns(1)
		err = sqlite.ApplyMigrations(context.Background(), raw, sqlite.DefaultMigrations())
		closeErr := raw.Close()
		if err != nil {
			emptySchema.err = err
			return
		}
		if closeErr != nil {
			emptySchema.err = closeErr
			return
		}
		emptySchema.bytes, emptySchema.err = os.ReadFile(path)
	})
	if emptySchema.err != nil {
		t.Fatalf("create empty workflow schema: %v", emptySchema.err)
	}
	if err := os.MkdirAll(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateRoot, "devbridge.db")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatal(fmt.Errorf("seed isolated workflow database: %w", err))
	}
	_, err = file.Write(emptySchema.bytes)
	closeErr := file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}
