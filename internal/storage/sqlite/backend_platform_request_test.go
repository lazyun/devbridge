package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
)

// A pending request is only an immutable intent fence. It grants neither a
// descriptor nor compatibility evidence, qualification, binding, or Run.
func TestBackendPlatformPendingRequestIdentitySurvivesRestart(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	db, e := Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	config := codexconfig.Config{ID: "pending-backend", Name: "pending request only", Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if e = NewCodexConfigStore(db).Create(ctx, config); e != nil {
		t.Fatal(e)
	}
	store := NewRuntimePlatformStore(db)
	first := runtimequalification.RequestIdentity{RequestID: "single-request", BackendID: config.ID, BackendFingerprint: config.Fingerprint(), RuntimeRole: "IMPLEMENTATION", DescriptorID: "not-yet-registered", DescriptorHash: platformTestDigest("pending descriptor identity")}
	second := first
	second.RuntimeRole = "REVIEW"
	candidates := []runtimequalification.RequestIdentity{first, second}
	var outcomes [2]error
	var wg sync.WaitGroup
	for i := range candidates {
		wg.Add(1)
		go func(i int) { defer wg.Done(); outcomes[i] = store.ReserveQualificationRequest(ctx, candidates[i]) }(i)
	}
	wg.Wait()
	successes, conflicts := 0, 0
	var winner runtimequalification.RequestIdentity
	for i, e := range outcomes {
		if e == nil {
			successes++
			winner = candidates[i]
		} else if errors.Is(e, runtimeplatform.ErrSourceConflict) {
			conflicts++
		} else {
			t.Fatal(e)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("request CAS winners=%d conflicts=%d", successes, conflicts)
	}
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	db = nil
	db, e = Open(ctx, root)
	if e != nil {
		t.Fatal(e)
	}
	store = NewRuntimePlatformStore(db)
	if pending, e := store.ReadQualificationRequest(ctx, winner.RequestID); e != nil || pending != winner {
		t.Fatalf("cold pending identity: %+v %v", pending, e)
	}
	if e = store.ReserveQualificationRequest(ctx, winner); e != nil {
		t.Fatalf("cold exact reservation retry: %v", e)
	}
	changed := winner
	changed.DescriptorHash = platformTestDigest("different artifact")
	if e = store.ReserveQualificationRequest(ctx, changed); !errors.Is(e, runtimeplatform.ErrSourceConflict) {
		t.Fatalf("same request changed artifact accepted: %v", e)
	}
	if _, e = db.SQL().ExecContext(ctx, `UPDATE backend_platform_qualification_requests SET descriptor_id='replacement'`); e == nil {
		t.Fatal("immutable pending request updated")
	}
	if _, e = db.SQL().ExecContext(ctx, `DELETE FROM backend_platform_qualification_requests`); e == nil {
		t.Fatal("immutable pending request deleted")
	}
	for _, table := range []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_requests", "runtime_selection_bindings", "backend_platform_qualifications", "runs"} {
		var n int
		if e = db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("pending request granted %s=%d err=%v", table, n, e)
		}
	}
	var count int
	if e = db.SQL().QueryRowContext(ctx, `SELECT count(*) FROM backend_platform_qualification_requests`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("pending requests=%d %v", count, e)
	}
}
