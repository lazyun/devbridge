package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/dataroot"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/task"
)

type platformStoreFixture struct {
	t          *testing.T
	ctx        context.Context
	db         *DB
	store      rp.Store
	descriptor rp.Descriptor
	evidence   rp.CompatibilityEvidence
}

func platformTestDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newPlatformStoreFixture(t *testing.T) *platformStoreFixture {
	t.Helper()
	f := &platformStoreFixture{t: t, ctx: context.Background(), db: openTestDB(t, t.TempDir())}
	f.store = NewRuntimePlatformStore(f.db)
	t.Cleanup(func() { _ = f.db.Close() })
	// These are explicit Core persistence-port fixtures, not a production
	// collector, compatibility measurement, or authority for a Task/Run.
	f.descriptor = rp.Descriptor{DescriptorID: "descriptor-test", PlatformLineageID: "lineage-test", RuntimeRole: "IMPLEMENTATION", AdapterID: "adapter-test", AdapterImplementationDigest: platformTestDigest("test adapter artifact"), ProtocolID: "protocol-test", ProtocolSchemaDigest: platformTestDigest("test protocol artifact"), CapabilityDigest: platformTestDigest("test capability artifact"), DescriptorVersion: 1}
	f.evidence = rp.CompatibilityEvidence{DescriptorID: f.descriptor.DescriptorID, PlatformLineageID: f.descriptor.PlatformLineageID, RuntimeRole: f.descriptor.RuntimeRole, AdapterImplementationDigest: f.descriptor.AdapterImplementationDigest, ProtocolSchemaDigest: f.descriptor.ProtocolSchemaDigest, CompatibilityPolicyVersion: "test-policy-v1", MeasuredCompatibility: true, CollectorIdentity: "test-core-collector", CollectionArtifactHash: platformTestDigest("test compatibility report"), Source: rp.SourceCoreCompatibilityCollection}
	f.evidence.EvidenceHash = f.evidence.ComputeHash()
	return f
}

func (f *platformStoreFixture) register() {
	f.t.Helper()
	d, err := f.store.RegisterDescriptor(f.ctx, f.descriptor)
	if err != nil || !reflect.DeepEqual(d, f.descriptor) {
		f.t.Fatalf("descriptor=%+v err=%v", d, err)
	}
	e, err := f.store.MaterializeCompatibilityEvidence(f.ctx, f.evidence)
	if err != nil || !reflect.DeepEqual(e, f.evidence) {
		f.t.Fatalf("evidence=%+v err=%v", e, err)
	}
}

func (f *platformStoreFixture) input(id, request string, generation int) rp.QualificationInput {
	f.t.Helper()
	lineage, err := f.store.GetLineage(f.ctx, f.descriptor.PlatformLineageID)
	if err != nil {
		f.t.Fatal(err)
	}
	return rp.QualificationInput{QualificationID: id, PlatformLineageID: f.descriptor.PlatformLineageID, DescriptorID: f.descriptor.DescriptorID, CompatibilityPolicyVersion: f.evidence.CompatibilityPolicyVersion, CompatibilityEvidenceHash: f.evidence.EvidenceHash, IssuanceRequestID: request, Generation: generation, ExpectedLineageVersion: lineage.Version}
}

func (f *platformStoreFixture) qualify() (rp.QualificationInput, rp.Qualification) {
	f.t.Helper()
	in := f.input("qualification-1", "issue-1", 1)
	q, err := f.store.Qualify(f.ctx, in)
	if err != nil {
		f.t.Fatal(err)
	}
	return in, q
}

func (f *platformStoreFixture) reopen() {
	f.t.Helper()
	root := f.db.stateRoot
	if err := f.db.Close(); err != nil {
		f.t.Fatal(err)
	}
	var err error
	f.db, err = Open(f.ctx, root)
	if err != nil {
		f.t.Fatal(err)
	}
	f.store = NewRuntimePlatformStore(f.db)
}

func platformStoreSnapshot(t *testing.T, db *DB) string {
	t.Helper()
	tables := []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_lineages", "runtime_platform_requests", "tasks", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities", "runs", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities"}
	return platformTableSnapshot(t, db, tables)
}

func platformTableSnapshot(t *testing.T, db *DB, tables []string) string {
	t.Helper()
	out := map[string][][]any{}
	for _, table := range tables {
		rows, err := db.sql.Query("SELECT * FROM " + sqlIdentifier(table) + " ORDER BY 1")
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out[table] = [][]any{}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			out[table] = append(out[table], values)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestRuntimePlatformStoreInvalidationDoesNotResetGeneration(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	first, old := f.qualify()
	if _, err := f.store.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: old.QualificationID, Generation: old.Generation, RequestID: "invalidate-current", Reason: "replace revoked generation", ExpectedLineageVersion: 1}); err != nil {
		t.Fatal(err)
	}
	lineage, err := f.store.GetLineage(f.ctx, old.PlatformLineageID)
	if err != nil || lineage.HighWater != 1 || lineage.Version != 2 {
		t.Fatalf("lineage=%+v err=%v", lineage, err)
	}
	reuseGeneration := first
	reuseGeneration.QualificationID, reuseGeneration.IssuanceRequestID, reuseGeneration.ExpectedLineageVersion = "new-id-old-generation", "reuse-generation", lineage.Version
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Qualify(f.ctx, reuseGeneration); return err })
	reuseID := f.input(old.QualificationID, "reuse-id", 2)
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Qualify(f.ctx, reuseID); return err })
	next := f.input("qualification-next", "issue-next", 2)
	current, err := f.store.Qualify(f.ctx, next)
	if err != nil || current.State != rp.StateCurrent || current.Generation != 2 {
		t.Fatalf("new generation=%+v err=%v", current, err)
	}
	prior, err := f.store.GetQualification(f.ctx, old.QualificationID, old.Generation)
	if err != nil || prior.State != rp.StateInvalidated || prior.IssuancePayloadHash != old.IssuancePayloadHash {
		t.Fatalf("prior=%+v err=%v", prior, err)
	}
}

func TestRuntimePlatformV96UpgradePreservesBusinessFacts(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(root, "devbridge.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	// Only v96 is initialized. No current-schema DB is downgraded here.
	if err := ApplyMigrations(ctx, raw, migrationsThroughVersion(96)); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	writer, err := dataroot.AcquireWriterLock(filepath.Join(root, "core-writer.lock"))
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	db := &DB{sql: raw, stateRoot: root, writer: writer}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.allocateRuntimeEpoch(ctx); err != nil {
		t.Fatal(err)
	}
	var version, platformTables int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 96 {
		t.Fatalf("historical version=%d err=%v", version, err)
	}
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'runtime_platform_%'`).Scan(&platformTables); err != nil || platformTables != 0 {
		t.Fatalf("historical platform tables=%d err=%v", platformTables, err)
	}
	p := project.Project{ID: "platform-upgrade-project", Name: "Upgrade history", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewProjectStore(db).Create(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := repository.Repository{ID: "platform-upgrade-repo", ProjectID: p.ID, Name: "Source", Path: filepath.Join(root, "source"), CreatedAt: testNow, UpdatedAt: testNow}
	if _, _, err := NewRepositoryStore(db).PutIfAbsent(ctx, r); err != nil {
		t.Fatal(err)
	}
	m := module.Module{ID: "platform-upgrade-module", ProjectID: p.ID, Key: "core", Name: "Core", CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewModuleStore(db).Create(ctx, m); err != nil {
		t.Fatal(err)
	}
	session := aisession.Session{ID: "platform-upgrade-session", ProjectID: p.ID, CreatedAt: testNow, UpdatedAt: testNow}
	seedBoundSession(t, db, session)
	area := featurearea.FeatureArea{ID: "platform-upgrade-area", ProjectID: p.ID, CreatedByAISessionID: session.ID, Title: "Preserve task history", ProblemStatement: "A new catalog must not change prior business facts", Status: featurearea.StatusDiscovering, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewFeatureAreaStore(db).Create(ctx, area); err != nil {
		t.Fatal(err)
	}
	owner := task.Task{ID: "platform-upgrade-task", Title: "Preserve task history", Type: task.TypeDevelopment, Scope: task.ScopeFeatureArea, ProjectID: p.ID, FeatureAreaID: area.ID, ModuleID: m.ID, RepositoryID: r.ID, CodexConfigID: codexconfig.DefaultConfigID, Goal: "Preserve task history", ProblemStatement: area.ProblemStatement, Constraints: []string{}, BaseCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Status: task.StatusActive, Phase: task.PhaseReviewRequired, CreatedAt: testNow, UpdatedAt: testNow}
	if err := NewTaskStore(db).Create(ctx, owner); err != nil {
		t.Fatal(err)
	}
	owner, err = NewTaskStore(db).Get(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.sql.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('schema_migrations','runtime_epochs','runtime_authority') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	rows.Close()
	before := platformTableSnapshot(t, db, tables)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		db, err = Open(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		var applied97 int
		all := DefaultMigrations()
		if err := db.sql.QueryRow(`SELECT MAX(version),COUNT(CASE WHEN version=97 THEN 1 END) FROM schema_migrations`).Scan(&version, &applied97); err != nil || version != all[len(all)-1].Version || applied97 != 1 {
			t.Fatalf("version=%d v97=%d err=%v", version, applied97, err)
		}
		if after := platformTableSnapshot(t, db, tables); after != before {
			t.Fatal("v97 migration or cold reopen changed existing business data")
		}
		current, err := NewTaskStore(db).Get(ctx, owner.ID)
		if err != nil || !reflect.DeepEqual(current, owner) {
			t.Fatalf("Task changed after upgrade: err=%v", err)
		}
		for _, table := range []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_lineages", "runtime_platform_requests"} {
			var count int
			if err := db.sql.QueryRow(`SELECT COUNT(*) FROM ` + sqlIdentifier(table)).Scan(&count); err != nil || count != 0 {
				t.Fatalf("migration inferred platform facts: table=%s count=%d err=%v", table, count, err)
			}
		}
		if i == 0 {
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func platformRejectWithoutWrites(t *testing.T, f *platformStoreFixture, want error, call func() error) {
	t.Helper()
	before := platformStoreSnapshot(t, f.db)
	err := call()
	if err == nil || (want != nil && !errors.Is(err, want)) {
		t.Fatalf("rejection=%v want=%v", err, want)
	}
	if after := platformStoreSnapshot(t, f.db); after != before {
		t.Fatal("rejected operation changed durable platform or business facts")
	}
}

func TestRuntimePlatformStoreLifecycleColdReplay(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	first, q1 := f.qualify()
	if q1.State != rp.StateCurrent || q1.Generation != 1 || q1.Outcome != rp.OutcomeQualified || q1.IssuancePayloadHash != first.ComputePayloadHash() || q1.CreatedAt.IsZero() {
		t.Fatalf("first qualification=%+v", q1)
	}
	second := f.input("qualification-2", "issue-2", 2)
	second.ExpectedQualificationID, second.ExpectedGeneration = q1.QualificationID, q1.Generation
	q2, err := f.store.Supersede(f.ctx, second)
	if err != nil || q2.State != rp.StateCurrent {
		t.Fatalf("supersede=%+v err=%v", q2, err)
	}
	old, err := f.store.GetQualification(f.ctx, q1.QualificationID, 1)
	if err != nil || old.State != rp.StateSuperseded || old.SupersededAt.IsZero() || old.IssuancePayloadHash != q1.IssuancePayloadHash || old.CreatedAt != q1.CreatedAt {
		t.Fatalf("old qualification=%+v err=%v", old, err)
	}
	var invalidations []rp.InvalidationInput
	for _, q := range []rp.Qualification{old, q2} {
		lineage, err := f.store.GetLineage(f.ctx, q.PlatformLineageID)
		if err != nil {
			t.Fatal(err)
		}
		in := rp.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, RequestID: "invalidate-" + q.QualificationID, Reason: "test policy revocation", ExpectedLineageVersion: lineage.Version}
		out, err := f.store.Invalidate(f.ctx, in)
		if err != nil || out.State != rp.StateInvalidated || out.InvalidatedAt.IsZero() || out.InvalidationReason != in.Reason || out.InvalidationPayloadHash != in.ComputePayloadHash() || out.IssuancePayloadHash != q.IssuancePayloadHash {
			t.Fatalf("invalidate=%+v err=%v", out, err)
		}
		invalidations = append(invalidations, in)
	}
	before := platformStoreSnapshot(t, f.db)
	for i := 0; i < 2; i++ {
		f.reopen()
		if platformStoreSnapshot(t, f.db) != before {
			t.Fatal("cold reopen changed platform/business facts")
		}
		f.register()
		// Expected values are stale after supersede/invalidation. Replay must
		// identify the exact original request before checking fresh CAS fences.
		for _, replay := range []struct {
			in   rp.QualificationInput
			call func(context.Context, rp.QualificationInput) (rp.Qualification, error)
		}{{first, f.store.Qualify}, {second, f.store.Supersede}} {
			out, err := replay.call(f.ctx, replay.in)
			current, readErr := f.store.GetQualification(f.ctx, replay.in.QualificationID, replay.in.Generation)
			if err != nil || readErr != nil || out.State != rp.StateInvalidated || !reflect.DeepEqual(out, current) {
				t.Fatalf("replay=%+v read=%+v err=%v readErr=%v", out, current, err, readErr)
			}
		}
		for _, in := range invalidations {
			if _, err := f.store.Invalidate(f.ctx, in); err != nil {
				t.Fatal(err)
			}
		}
		if platformStoreSnapshot(t, f.db) != before {
			t.Fatal("exact cold replay wrote durable facts")
		}
	}
	lineage, err := f.store.GetLineage(f.ctx, f.descriptor.PlatformLineageID)
	if err != nil || lineage.HighWater != 2 || lineage.Version != 4 {
		t.Fatalf("lineage=%+v err=%v", lineage, err)
	}
	for _, table := range []string{"tasks", "execution_proposals", "runtime_scope_plans", "runtime_preparation_authorities", "runs", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities"} {
		var count int
		if err := f.db.sql.QueryRow("SELECT count(*) FROM " + sqlIdentifier(table)).Scan(&count); err != nil || count != 0 {
			t.Fatalf("platform lifecycle created business/runtime facts in %s: %d %v", table, count, err)
		}
	}
}

func TestRuntimePlatformStoreRejectsMissingMalformedAndIncompatibleEvidence(t *testing.T) {
	f := newPlatformStoreFixture(t)
	platformRejectWithoutWrites(t, f, rp.ErrDescriptorMissing, func() error { _, err := f.store.MaterializeCompatibilityEvidence(f.ctx, f.evidence); return err })
	if _, err := f.store.RegisterDescriptor(f.ctx, f.descriptor); err != nil {
		t.Fatal(err)
	}
	platformRejectWithoutWrites(t, f, rp.ErrBindingCorrupt, func() error {
		_, err := f.store.Qualify(f.ctx, f.input("missing-evidence", "missing-evidence", 1))
		return err
	})
	for _, tc := range []struct {
		name   string
		mutate func(*rp.CompatibilityEvidence)
	}{
		{"incompatible", func(e *rp.CompatibilityEvidence) { e.MeasuredCompatibility = false }},
		{"wrong-descriptor", func(e *rp.CompatibilityEvidence) { e.DescriptorID = "foreign-descriptor" }},
		{"wrong-lineage", func(e *rp.CompatibilityEvidence) { e.PlatformLineageID = "foreign-lineage" }},
		{"wrong-role", func(e *rp.CompatibilityEvidence) { e.RuntimeRole = "REVIEW" }},
		{"wrong-adapter", func(e *rp.CompatibilityEvidence) {
			e.AdapterImplementationDigest = platformTestDigest("foreign adapter")
		}},
		{"wrong-protocol", func(e *rp.CompatibilityEvidence) { e.ProtocolSchemaDigest = platformTestDigest("foreign protocol") }},
		{"missing-policy", func(e *rp.CompatibilityEvidence) { e.CompatibilityPolicyVersion = "" }},
		{"missing-collector", func(e *rp.CompatibilityEvidence) { e.CollectorIdentity = "" }},
		{"malformed-artifact", func(e *rp.CompatibilityEvidence) { e.CollectionArtifactHash = "not-a-digest" }},
		{"forbidden-source", func(e *rp.CompatibilityEvidence) { e.Source = "APPSERVER_OBSERVATION" }},
		{"malformed-evidence-hash", func(e *rp.CompatibilityEvidence) { e.EvidenceHash = "malformed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := f.evidence
			tc.mutate(&e)
			if tc.name != "malformed-evidence-hash" {
				e.EvidenceHash = e.ComputeHash()
			}
			platformRejectWithoutWrites(t, f, nil, func() error { _, err := f.store.MaterializeCompatibilityEvidence(f.ctx, e); return err })
		})
	}
	f.register()
	for _, tc := range []struct {
		name   string
		mutate func(*rp.QualificationInput)
	}{
		{"wrong-policy", func(in *rp.QualificationInput) { in.CompatibilityPolicyVersion = "foreign-policy" }},
		{"wrong-evidence", func(in *rp.QualificationInput) { in.CompatibilityEvidenceHash = platformTestDigest("missing evidence") }},
		{"wrong-descriptor", func(in *rp.QualificationInput) { in.DescriptorID = "foreign-descriptor" }},
		{"wrong-lineage", func(in *rp.QualificationInput) { in.PlatformLineageID = "foreign-lineage" }},
		{"zero-generation", func(in *rp.QualificationInput) { in.Generation = 0 }},
		{"skip-generation", func(in *rp.QualificationInput) { in.Generation = 2 }},
		{"stale-version", func(in *rp.QualificationInput) { in.ExpectedLineageVersion++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := f.input("reject", "reject", 1)
			tc.mutate(&in)
			platformRejectWithoutWrites(t, f, nil, func() error { _, err := f.store.Qualify(f.ctx, in); return err })
		})
	}
}

func TestRuntimePlatformStoreRequestIdentityRejectsWithoutWrites(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	first, q := f.qualify()
	for _, tc := range []struct {
		name   string
		mutate func(*rp.QualificationInput)
	}{
		{"same-request-changed-id", func(in *rp.QualificationInput) { in.QualificationID = "foreign" }},
		{"same-request-changed-lineage", func(in *rp.QualificationInput) { in.PlatformLineageID = "foreign" }},
		{"same-request-changed-evidence", func(in *rp.QualificationInput) { in.CompatibilityEvidenceHash = platformTestDigest("foreign") }},
		{"same-request-changed-descriptor", func(in *rp.QualificationInput) { in.DescriptorID = "foreign" }},
		{"same-request-changed-policy", func(in *rp.QualificationInput) { in.CompatibilityPolicyVersion = "foreign" }},
		{"same-request-changed-generation", func(in *rp.QualificationInput) { in.Generation++ }},
		{"foreign-request-same-generation", func(in *rp.QualificationInput) { in.IssuanceRequestID = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := first
			tc.mutate(&in)
			platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Qualify(f.ctx, in); return err })
		})
	}
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Supersede(f.ctx, first); return err })
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error {
		_, err := f.store.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, RequestID: first.IssuanceRequestID, Reason: "conflicting operation", ExpectedLineageVersion: 1})
		return err
	})
	d := f.descriptor
	d.CapabilityDigest = platformTestDigest("different capability")
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.RegisterDescriptor(f.ctx, d); return err })
	e := f.evidence
	e.CollectionArtifactHash = platformTestDigest("different artifact")
	platformRejectWithoutWrites(t, f, rp.ErrBindingCorrupt, func() error { _, err := f.store.MaterializeCompatibilityEvidence(f.ctx, e); return err })
	second := f.input("qualification-2", "issue-2", 2)
	second.ExpectedQualificationID, second.ExpectedGeneration = "foreign-owner", 1
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Supersede(f.ctx, second); return err })
	second.ExpectedQualificationID, second.ExpectedGeneration = q.QualificationID, 2
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Supersede(f.ctx, second); return err })
	lineage, err := f.store.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	invalidate := rp.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, RequestID: "invalidate", Reason: "test revocation", ExpectedLineageVersion: lineage.Version}
	if _, err := f.store.Invalidate(f.ctx, invalidate); err != nil {
		t.Fatal(err)
	}
	changedReason := invalidate
	changedReason.Reason = "different revocation"
	platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error { _, err := f.store.Invalidate(f.ctx, changedReason); return err })
	foreignRequest := invalidate
	foreignRequest.RequestID = "foreign-invalidation"
	platformRejectWithoutWrites(t, f, nil, func() error { _, err := f.store.Invalidate(f.ctx, foreignRequest); return err })
}

func TestRuntimePlatformStoreConcurrentCASHasOneCurrent(t *testing.T) {
	for _, sameRequest := range []bool{true, false} {
		name := "different-requests"
		if sameRequest {
			name = "same-request"
		}
		t.Run(name, func(t *testing.T) {
			f := newPlatformStoreFixture(t)
			f.register()
			_, old := f.qualify()
			a := f.input("qualification-next-a", "next-a", 2)
			a.ExpectedQualificationID, a.ExpectedGeneration = old.QualificationID, old.Generation
			b := a
			if !sameRequest {
				b.QualificationID, b.IssuanceRequestID = "qualification-next-b", "next-b"
			}
			start := make(chan struct{})
			var wg sync.WaitGroup
			outs := make([]rp.Qualification, 2)
			errs := make([]error, 2)
			for i, in := range []rp.QualificationInput{a, b} {
				wg.Add(1)
				go func(i int, in rp.QualificationInput) {
					defer wg.Done()
					<-start
					outs[i], errs[i] = f.store.Supersede(f.ctx, in)
				}(i, in)
			}
			close(start)
			wg.Wait()
			success := 0
			for _, err := range errs {
				if err == nil {
					success++
				} else if !errors.Is(err, rp.ErrSourceConflict) {
					t.Fatalf("unexpected CAS error: %v", err)
				}
			}
			wantSuccess := 1
			if sameRequest {
				wantSuccess = 2
			}
			if success != wantSuccess {
				t.Fatalf("success=%d errors=%v", success, errs)
			}
			if sameRequest && !reflect.DeepEqual(outs[0], outs[1]) {
				t.Fatal("same-request racers did not read one immutable winner")
			}
			var current, total int
			if err := f.db.sql.QueryRow(`SELECT COUNT(CASE WHEN state='CURRENT' THEN 1 END),COUNT(*) FROM runtime_platform_qualifications`).Scan(&current, &total); err != nil || current != 1 || total != 2 {
				t.Fatalf("current=%d total=%d err=%v", current, total, err)
			}
			lineage, err := f.store.GetLineage(f.ctx, f.descriptor.PlatformLineageID)
			if err != nil || lineage.HighWater != 2 || lineage.Version != 2 {
				t.Fatalf("lineage=%+v err=%v", lineage, err)
			}
		})
	}
}

func TestRuntimePlatformStoreSupersedeFailureRollsBackWholeTransaction(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	_, old := f.qualify()
	in := f.input("qualification-next", "next", 2)
	in.ExpectedQualificationID, in.ExpectedGeneration = old.QualificationID, old.Generation
	// This fails after the request INSERT and the predecessor's SUPERSEDED
	// UPDATE, so rollback must restore both rather than only reject preflight.
	if _, err := f.db.sql.Exec(`CREATE TRIGGER platform_qualification_write_fault BEFORE INSERT ON runtime_platform_qualifications BEGIN SELECT RAISE(ABORT,'injected next qualification write failure'); END`); err != nil {
		t.Fatal(err)
	}
	platformRejectWithoutWrites(t, f, nil, func() error { _, err := f.store.Supersede(f.ctx, in); return err })
	if _, err := f.db.sql.Exec(`DROP TRIGGER platform_qualification_write_fault`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Supersede(f.ctx, in); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePlatformStoreSQLRejectsIdentityChangesAndIllegalLifecycle(t *testing.T) {
	f := newPlatformStoreFixture(t)
	f.register()
	_, old := f.qualify()
	for _, tc := range []struct{ name, query string }{
		{"descriptor-update", `UPDATE runtime_platform_descriptors SET descriptor_version=descriptor_version+1`},
		{"descriptor-delete", `DELETE FROM runtime_platform_descriptors`},
		{"evidence-update", `UPDATE runtime_platform_compatibility_evidence SET collector_identity='foreign'`},
		{"evidence-delete", `DELETE FROM runtime_platform_compatibility_evidence`},
		{"request-update", `UPDATE runtime_platform_requests SET operation='INVALIDATE'`},
		{"request-delete", `DELETE FROM runtime_platform_requests`},
		{"qualification-delete", `DELETE FROM runtime_platform_qualifications`},
		{"qualification-generation", `UPDATE runtime_platform_qualifications SET generation=generation+1`},
		{"qualification-descriptor", `UPDATE runtime_platform_qualifications SET descriptor_id='foreign'`},
		{"qualification-policy", `UPDATE runtime_platform_qualifications SET compatibility_policy_version='foreign'`},
		{"qualification-request", `UPDATE runtime_platform_qualifications SET issuance_request_id='foreign'`},
		{"qualification-created-at", `UPDATE runtime_platform_qualifications SET created_at='2000-01-01T00:00:00Z'`},
		{"qualification-payload", `UPDATE runtime_platform_qualifications SET issuance_payload_hash=printf('%064d',0)`},
		{"lineage-delete", `DELETE FROM runtime_platform_lineages`},
		{"lineage-owner", `UPDATE runtime_platform_lineages SET runtime_role='REVIEW'`},
		{"lineage-advance-without-generation", `UPDATE runtime_platform_lineages SET high_water=high_water+1,version=version+1`},
		{"lineage-version-without-lifecycle", `UPDATE runtime_platform_lineages SET version=version+1`},
		{"supersede-without-operation", `UPDATE runtime_platform_qualifications SET state='SUPERSEDED',superseded_at='2000-01-01T00:00:00Z',lineage_version=lineage_version+1`},
		{"invalidate-without-operation", `UPDATE runtime_platform_qualifications SET state='INVALIDATED',invalidated_at='2000-01-01T00:00:00Z',invalidation_reason='foreign',invalidation_request_id=issuance_request_id,invalidation_payload_hash=issuance_payload_hash,lineage_version=lineage_version+1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platformRejectWithoutWrites(t, f, nil, func() error { _, err := f.db.sql.Exec(tc.query); return err })
		})
	}
	second := f.input("qualification-2", "issue-2", 2)
	second.ExpectedQualificationID, second.ExpectedGeneration = old.QualificationID, old.Generation
	if _, err := f.store.Supersede(f.ctx, second); err != nil {
		t.Fatal(err)
	}
	platformRejectWithoutWrites(t, f, nil, func() error {
		_, err := f.db.sql.Exec(`UPDATE runtime_platform_qualifications SET state='CURRENT',superseded_at='' WHERE qualification_id=?`, old.QualificationID)
		return err
	})
	lineage, err := f.store.GetLineage(f.ctx, old.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: old.QualificationID, Generation: old.Generation, RequestID: "invalidate-old", Reason: "test revocation", ExpectedLineageVersion: lineage.Version}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{rp.StateCurrent, rp.StateSuperseded} {
		t.Run("invalidated-to-"+state, func(t *testing.T) {
			platformRejectWithoutWrites(t, f, nil, func() error {
				_, err := f.db.sql.Exec(`UPDATE runtime_platform_qualifications SET state=?,invalidated_at='',invalidation_reason='',invalidation_request_id=NULL,invalidation_payload_hash='' WHERE qualification_id=?`, state, old.QualificationID)
				return err
			})
		})
	}
	rows, err := f.db.sql.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("platform operations left a foreign-key violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimePlatformStoreSQLRequiresCompleteLifecycleAtCommit(t *testing.T) {
	for _, operation := range []string{"SUPERSEDE", "INVALIDATE"} {
		t.Run(operation, func(t *testing.T) {
			f := newPlatformStoreFixture(t)
			f.register()
			_, old := f.qualify()
			var bindingColumns int
			if err := f.db.sql.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('runtime_platform_requests') WHERE name IN ('issuance_binding','invalidation_binding')`).Scan(&bindingColumns); err != nil {
				t.Fatal(err)
			}
			if bindingColumns != 0 && bindingColumns != 2 {
				t.Fatalf("partial binding schema: %d", bindingColumns)
			}
			platformRejectWithoutWrites(t, f, nil, func() error {
				tx, err := f.db.beginRuntimePlatformWrite(f.ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback()
				now := formatTime(time.Now().UTC())
				if operation == "SUPERSEDE" {
					in := rp.QualificationInput{QualificationID: old.QualificationID, PlatformLineageID: old.PlatformLineageID, Generation: 2, DescriptorID: old.DescriptorID, CompatibilityPolicyVersion: old.CompatibilityPolicyVersion, CompatibilityEvidenceHash: old.CompatibilityEvidenceHash, IssuanceRequestID: "partial-supersede", ExpectedLineageVersion: 1, ExpectedQualificationID: old.QualificationID, ExpectedGeneration: 1}
					column, value := "", ""
					args := []any{in.IssuanceRequestID, in.ComputePayloadHash(), old.QualificationID, old.PlatformLineageID, in.Generation, in.ExpectedLineageVersion, old.QualificationID, old.Generation, now}
					if bindingColumns == 2 {
						column, value = ",issuance_binding", ",?"
						args = append(args, in.IssuanceRequestID)
					}
					_, err = tx.Exec(`INSERT INTO runtime_platform_requests(request_id,operation,payload_hash,qualification_id,platform_lineage_id,generation,expected_lineage_version,predecessor_qualification_id,predecessor_generation,created_at`+column+`) VALUES(?,'SUPERSEDE',?,?,?,?,?,?,?,?`+value+`)`, args...)
					if err != nil {
						t.Fatalf("request setup rejected before COMMIT: %v", err)
					}
					_, err = tx.Exec(`UPDATE runtime_platform_qualifications SET state='SUPERSEDED',superseded_at=?,lineage_version=2 WHERE qualification_id=?`, now, old.QualificationID)
				} else {
					in := rp.InvalidationInput{QualificationID: old.QualificationID, Generation: old.Generation, RequestID: "phantom-invalidation", Reason: "never applied", ExpectedLineageVersion: 1}
					column, value := "", ""
					args := []any{in.RequestID, in.ComputePayloadHash(), old.QualificationID, old.PlatformLineageID, old.Generation, in.ExpectedLineageVersion, now}
					if bindingColumns == 2 {
						column, value = ",invalidation_binding", ",?"
						args = append(args, in.RequestID)
					}
					_, err = tx.Exec(`INSERT INTO runtime_platform_requests(request_id,operation,payload_hash,qualification_id,platform_lineage_id,generation,expected_lineage_version,created_at`+column+`) VALUES(?,'INVALIDATE',?,?,?,?,?,?`+value+`)`, args...)
				}
				if err != nil {
					t.Fatalf("lifecycle setup rejected before COMMIT: %v", err)
				}
				// Deferred source linkage must reject the incomplete operation
				// at COMMIT, not merely accept a request referencing any old ID.
				err = tx.Commit()
				if err == nil {
					t.Errorf("COMMIT accepted an incomplete %s operation", operation)
				}
				return err
			})
		})
	}
}

func TestRuntimePlatformStoreDeferredCommitFailureRollsBack(t *testing.T) {
	for _, supersede := range []bool{false, true} {
		name := "qualify"
		if supersede {
			name = "supersede"
		}
		t.Run(name, func(t *testing.T) {
			f := newPlatformStoreFixture(t)
			f.register()
			in := f.input("qualification-1", "issue-1", 1)
			call := f.store.Qualify
			if supersede {
				_, old := f.qualify()
				in = f.input("qualification-2", "issue-2", 2)
				in.ExpectedQualificationID, in.ExpectedGeneration = old.QualificationID, old.Generation
				call = f.store.Supersede
			}
			// A valid-shaped but unapplied invalidation request is inserted only
			// after the new qualification exists. Its deferred relation fails at
			// COMMIT, after all normal lifecycle writes have already succeeded.
			fault := `CREATE TRIGGER platform_deferred_commit_fault AFTER INSERT ON runtime_platform_qualifications BEGIN
INSERT INTO runtime_platform_requests(request_id,operation,payload_hash,qualification_id,platform_lineage_id,generation,expected_lineage_version,created_at,invalidation_binding)
VALUES('injected-phantom-invalidation','INVALIDATE','` + platformTestDigest("injected phantom invalidation") + `',NEW.qualification_id,NEW.platform_lineage_id,NEW.generation,NEW.lineage_version,NEW.created_at,'injected-phantom-invalidation'); END`
			if _, err := f.db.sql.Exec(fault); err != nil {
				t.Fatal(err)
			}
			platformRejectWithoutWrites(t, f, rp.ErrSourceConflict, func() error {
				_, err := call(f.ctx, in)
				if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY") {
					t.Fatalf("expected deferred foreign-key COMMIT failure, got %v", err)
				}
				return err
			})
			if _, err := f.db.sql.Exec(`DROP TRIGGER platform_deferred_commit_fault`); err != nil {
				t.Fatal(err)
			}
			// The connection must be usable and the original request unconsumed.
			out, err := call(f.ctx, in)
			if err != nil || out.QualificationID != in.QualificationID || out.State != rp.StateCurrent {
				t.Fatalf("retry after failed COMMIT=%+v err=%v", out, err)
			}
		})
	}
}
