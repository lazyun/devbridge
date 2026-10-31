package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A canceled driver query must release its prepared reader before returning its
// connection to database/sql. Otherwise a sibling commit leaves a stale WAL
// snapshot on that connection, and even the first UPDATE in a new transaction
// fails SQLITE_BUSY_SNAPSHOT (517). This exercises the same acquisition ordering
// as beginWrite, without workflow fixtures, model calls, or seeded authority.
// Cancellation timing is deliberately varied: this is a bounded stress
// regression, not a claim that a particular microsecond schedule is guaranteed.
func TestSQLiteCanceledReadReleasesPooledSnapshot(t *testing.T) {
	// Cancellation must be able to run while the pure-Go SQLite driver is
	// preparing/returning rows. A single scheduler P can defer every timer
	// until after these short queries, leaving the cancellation path untested.
	// This test is intentionally nonparallel and restores the caller's setting.
	if previous := runtime.GOMAXPROCS(0); previous < 2 {
		runtime.GOMAXPROCS(2)
		t.Cleanup(func() { runtime.GOMAXPROCS(previous) })
	}
	ctx := context.Background()
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "probe.db"), RawQuery: "_pragma=journal_mode(WAL)&_pragma=busy_timeout(1000)"}).String()
	open := func() *sql.DB {
		db, err := sql.Open("sqlite", dsn)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	reader, writer := open(), open()
	if _, err := writer.ExecContext(ctx, `CREATE TABLE canceled_read_probe(id INTEGER PRIMARY KEY, value INTEGER); INSERT INTO canceled_read_probe VALUES(1,0)`); err != nil {
		t.Fatal(err)
	}
	columns := make([]string, 150)
	for i := range columns {
		columns[i] = fmt.Sprintf("value AS column%d%s", i, strings.Repeat("a", 100))
	}
	query := "SELECT " + strings.Join(columns, ",") + " FROM canceled_read_probe"
	canceled := 0
	for i := 0; i < 100; i++ {
		queryCtx, cancel := context.WithCancel(ctx)
		timer := time.AfterFunc(time.Duration(10+i%100)*time.Microsecond, cancel)
		rows, err := reader.QueryContext(queryCtx, query)
		if rows != nil {
			_ = rows.Close()
		}
		timer.Stop()
		cancel()
		if errors.Is(err, context.Canceled) {
			canceled++
		} else if err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
		// A separate connection advances WAL after the canceled read, before
		// database/sql leases the reader's connection for a fresh writer.
		if _, err = writer.ExecContext(ctx, `UPDATE canceled_read_probe SET value=value+1`); err != nil {
			t.Fatalf("sibling commit %d: %v", i, err)
		}
		tx, err := reader.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("new writer %d: %v", i, err)
		}
		var observed int
		err = tx.QueryRowContext(ctx, `UPDATE canceled_read_probe SET value=value RETURNING value`).Scan(&observed)
		rollbackErr := tx.Rollback()
		if err != nil || rollbackErr != nil {
			t.Fatalf("first writer statement %d after canceled read: statement=%v rollback=%v", i, err, rollbackErr)
		}
		if observed != i+1 {
			t.Fatalf("writer %d observed stale value %d", i, observed)
		}
	}
	if canceled == 0 {
		t.Fatal("cancellation stress did not exercise a canceled query")
	}
	t.Logf("checked 100 fresh writer acquisitions after %d canceled queries", canceled)
}
