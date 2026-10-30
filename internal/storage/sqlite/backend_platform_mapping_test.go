package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/dataroot"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	rq "github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/runtimeselection"
)

var backendMappingTables = []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_lineages", "runtime_platform_requests", "backend_platform_qualification_requests", "backend_platform_qualifications", "backend_platform_mapping_heads", "runtime_selection_bindings", "runs"}

func backendTestMapping(t *testing.T, f *platformStoreFixture, backend, request string, q rp.Qualification) rq.Mapping {
	t.Helper()
	config := codexconfig.Config{ID: backend, Name: backend, Enabled: true, CreatedAt: testNow, UpdatedAt: testNow}
	if _, err := NewCodexConfigStore(f.db).Get(f.ctx, backend); errors.Is(err, codexconfig.ErrNotFound) || errors.Is(err, ErrNotFound) {
		if err = NewCodexConfigStore(f.db).Create(f.ctx, config); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	l, err := f.store.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	m := rq.Mapping{BackendID: backend, BackendFingerprint: config.Fingerprint(), RuntimeRole: f.descriptor.RuntimeRole, RequestID: request, Platform: runtimeselection.PlatformReference{PlatformLineageID: q.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, DescriptorID: q.DescriptorID, DescriptorHash: f.descriptor.ComputeHash(), CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash, LineageVersion: l.Version}}
	if err = NewRuntimePlatformStore(f.db).ReserveQualificationRequest(f.ctx, rq.RequestIdentity{BackendID: backend, BackendFingerprint: m.BackendFingerprint, RuntimeRole: m.RuntimeRole, RequestID: request, DescriptorID: m.Platform.DescriptorID, DescriptorHash: m.Platform.DescriptorHash}); err != nil {
		t.Fatal(err)
	}
	return m
}

func supersedeBackendTestPlatform(t *testing.T, f *platformStoreFixture, old rp.Qualification) rp.Qualification {
	t.Helper()
	in := f.input("qualification-next", "issue-next", old.Generation+1)
	in.ExpectedQualificationID, in.ExpectedGeneration = old.QualificationID, old.Generation
	q, err := f.store.Supersede(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func TestBackendPlatformMappingHistoryCASAndAtomicHead(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	_, q1 := f.qualify()
	store := NewRuntimePlatformStore(f.db)
	first := backendTestMapping(t, f, "backend-a", "map-a-original", q1)
	if err := store.SaveBackendMapping(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	q2 := supersedeBackendTestPlatform(t, f, q1)
	left := backendTestMapping(t, f, "backend-a", "map-a-next-left", q2)
	right := backendTestMapping(t, f, "backend-a", "map-a-next-right", q2)
	before := platformTableSnapshot(t, f.db, backendMappingTables)
	if _, err := f.db.sql.Exec(`CREATE TRIGGER mapping_head_fault BEFORE UPDATE ON backend_platform_mapping_heads BEGIN SELECT RAISE(ABORT,'mapping head fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBackendMapping(f.ctx, left); err == nil {
		t.Fatal("head fault ignored")
	}
	if after := platformTableSnapshot(t, f.db, backendMappingTables); after != before {
		t.Fatal("failed head advancement left mapping or platform writes")
	}
	if _, err := f.db.sql.Exec(`DROP TRIGGER mapping_head_fault`); err != nil {
		t.Fatal(err)
	}
	var results [2]error
	var wg sync.WaitGroup
	for i, m := range []rq.Mapping{left, right} {
		wg.Add(1)
		go func(i int, m rq.Mapping) { defer wg.Done(); results[i] = store.SaveBackendMapping(f.ctx, m) }(i, m)
	}
	wg.Wait()
	var winner rq.Mapping
	successes, conflicts := 0, 0
	for i, err := range results {
		if err == nil {
			successes++
			winner = []rq.Mapping{left, right}[i]
		} else if errors.Is(err, rp.ErrSourceConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("mapping CAS successes=%d conflicts=%d", successes, conflicts)
	}
	current, err := store.ReadBackendMapping(f.ctx, first.BackendID, first.BackendFingerprint, first.RuntimeRole, first.Platform.DescriptorID)
	if err != nil || current != winner {
		t.Fatalf("head=%+v error=%v", current, err)
	}
	before = platformTableSnapshot(t, f.db, backendMappingTables)
	for _, statement := range []string{
		`UPDATE backend_platform_mapping_heads SET request_id='map-a-original',version=version+1`,
		`UPDATE backend_platform_mapping_heads SET runtime_role='REVIEW',version=version+1`,
		`UPDATE backend_platform_mapping_heads SET version=version+1`,
		`DELETE FROM backend_platform_mapping_heads`,
		`UPDATE backend_platform_qualifications SET qualification_id='qualification-next' WHERE request_id='map-a-original'`,
		`DELETE FROM backend_platform_qualifications`,
	} {
		if _, err = f.db.sql.Exec(statement); err == nil {
			t.Fatalf("illegal mutation succeeded: %s", statement)
		}
	}
	changed := winner
	changed.Platform.QualificationID = q1.QualificationID
	if err = store.SaveBackendMapping(f.ctx, changed); !errors.Is(err, rp.ErrSourceConflict) {
		t.Fatalf("changed request accepted: %v", err)
	}
	if err = store.SaveBackendMapping(f.ctx, first); err != nil {
		t.Fatal(err)
	}
	if after := platformTableSnapshot(t, f.db, backendMappingTables); after != before {
		t.Fatal("reject/replay wrote facts")
	}
	f.reopen()
	store = NewRuntimePlatformStore(f.db)
	before = platformTableSnapshot(t, f.db, backendMappingTables)
	for _, m := range []rq.Mapping{first, winner} {
		got, err := store.ReadBackendMappingForRequest(f.ctx, m.RequestID)
		if err != nil || got != m {
			t.Fatalf("cold receipt=%+v error=%v", got, err)
		}
		if err = store.SaveBackendMapping(f.ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if after := platformTableSnapshot(t, f.db, backendMappingTables); after != before {
		t.Fatal("cold replay wrote facts")
	}
}

func TestBackendPlatformV104UpgradePreservesHistoricalReceipts(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyMigrations(ctx, raw, migrationsThroughVersion(104)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	writer, err := dataroot.AcquireWriterLock(filepath.Join(root, "core-writer.lock"))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	db := &DB{sql: raw, stateRoot: root, writer: writer}
	defer func() { _ = db.Close() }()
	if err = db.allocateRuntimeEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	// Persistence-port fixtures reproduce the old schema's legitimate q1/q2
	// history. No business approval, runtime binding, or native proof is forged.
	template := newPlatformStoreFixture(t)
	f := &platformStoreFixture{t: t, ctx: ctx, db: db, store: NewRuntimePlatformStore(db), descriptor: template.descriptor, evidence: template.evidence}
	f.register()
	_, q1 := f.qualify()
	insertOld := func(m rq.Mapping) {
		t.Helper()
		encoded, _ := json.Marshal(m)
		_, err := raw.Exec(`INSERT INTO backend_platform_qualifications(backend_id,backend_fingerprint,runtime_role,descriptor_id,request_id,qualification_id,payload_json,payload_sha256) VALUES(?,?,?,?,?,?,?,?)`, m.BackendID, m.BackendFingerprint, m.RuntimeRole, m.Platform.DescriptorID, m.RequestID, m.Platform.QualificationID, string(encoded), runtimeselection.HashValue(m))
		if err != nil {
			t.Fatal(err)
		}
	}
	a := backendTestMapping(t, f, "historical-a", "historical-a-request", q1)
	insertOld(a)
	q2 := supersedeBackendTestPlatform(t, f, q1)
	b := backendTestMapping(t, f, "historical-b", "historical-b-request", q2)
	insertOld(b)
	oldReceipts := func() string {
		t.Helper()
		rows, err := db.sql.Query(`SELECT request_id,payload_json,payload_sha256 FROM backend_platform_qualifications ORDER BY request_id`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out [][3]string
		for rows.Next() {
			var r [3]string
			if err = rows.Scan(&r[0], &r[1], &r[2]); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	before := oldReceipts()
	platformBefore := platformTableSnapshot(t, db, backendMappingTables[:6])
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	f.db, f.store = db, NewRuntimePlatformStore(db)
	store := NewRuntimePlatformStore(db)
	if oldReceipts() != before || platformTableSnapshot(t, db, backendMappingTables[:6]) != platformBefore {
		t.Fatal("upgrade rewrote historical payload/hash/platform facts")
	}
	for _, m := range []rq.Mapping{a, b} {
		got, err := store.ReadBackendMapping(ctx, m.BackendID, m.BackendFingerprint, m.RuntimeRole, m.Platform.DescriptorID)
		if err != nil || got != m {
			t.Fatalf("migration substituted historical mapping: %+v %v", got, err)
		}
	}
	renew := backendTestMapping(t, f, a.BackendID, "historical-a-explicit-renew", q2)
	if err = store.SaveBackendMapping(ctx, renew); err != nil {
		t.Fatal(err)
	}
	if got, err := store.ReadBackendMappingForRequest(ctx, a.RequestID); err != nil || got != a {
		t.Fatalf("renew rewrote original receipt: %+v %v", got, err)
	}
	if got, err := store.ReadBackendMapping(ctx, a.BackendID, a.BackendFingerprint, a.RuntimeRole, a.Platform.DescriptorID); err != nil || got != renew {
		t.Fatalf("renew head=%+v error=%v", got, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store = NewRuntimePlatformStore(db)
	snapshot := platformTableSnapshot(t, db, backendMappingTables)
	for _, m := range []rq.Mapping{a, b, renew} {
		got, err := store.ReadBackendMappingForRequest(ctx, m.RequestID)
		if err != nil || got != m {
			t.Fatalf("cold history=%+v error=%v", got, err)
		}
		if err = store.SaveBackendMapping(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if platformTableSnapshot(t, db, backendMappingTables) != snapshot {
		t.Fatal("cold original and renewal receipt replay wrote facts")
	}
}
