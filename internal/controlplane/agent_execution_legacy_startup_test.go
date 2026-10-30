package controlplane

import (
	"context"
	"database/sql"
	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/dataroot"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPreTaskSchemaFailsBeforeStartupAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	opened, err := dataroot.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(opened.Paths.State, "devbridge.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/historical_v41_unknown.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.ExecContext(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	snapshot := func(db *sql.DB, q string) []any {
		t.Helper()
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		out := []any{}
		for rows.Next() {
			v := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range v {
				ptr[i] = &v[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			out = append(out, v...)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	runQuery := `SELECT id,task_id,status,updated_at,finished_at,runtime_epoch FROM runs WHERE id='legacy-run'`
	taskQuery := `SELECT id,status,updated_at FROM tasks WHERE id='task'`
	beforeRun, beforeTask := snapshot(raw, runQuery), snapshot(raw, taskQuery)
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "backend-called")
	binary := filepath.Join(t.TempDir(), "fake-codex")
	if err = os.WriteFile(binary, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		plane, err := Open(ctx, Config{DataRoot: root, Codex: codex.Config{CodexPath: binary}})
		if err == nil || !strings.Contains(err.Error(), "explicit recreation") {
			if plane != nil {
				plane.Close()
			}
			t.Fatalf("startup %d did not reject pre-Task schema: %v", i, err)
		}
		raw, err = sql.Open("sqlite", "file:"+filepath.Join(opened.Paths.State, "devbridge.db"))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(beforeRun, snapshot(raw, runQuery)) || !reflect.DeepEqual(beforeTask, snapshot(raw, taskQuery)) {
			t.Fatal("rejected migration mutated source history")
		}
		raw.Close()

	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unexpected backend invocation: %v", err)
	}
}
