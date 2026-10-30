package runtimequalification_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// A and B are ordinary independent backend configurations selecting the same
// installed native artifact. Compatibility is actually measured once through
// the offline loopback collector; this test never supplies evidence assertions.
func TestInstalledNativeMultiBackendQualificationAndExplicitRenewal(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; no online provider")
	}
	base, err := filepath.Abs("../../build/constraint-gap-fixes-2026-10-02/native-multi-backend")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "measurement-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("durable native multi-backend evidence:", root)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db, err := sqlite.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			_ = db.Close()
		}
	}()
	configs := sqlite.NewCodexConfigStore(db)
	a := codexconfig.Config{ID: "backend-a", Name: "Backend A", CodexPath: native, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	b := a
	b.ID, b.Name = "backend-b", "Backend B"
	for _, c := range []codexconfig.Config{a, b} {
		if err = configs.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	adapter, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewRuntimePlatformStore(db)
	service := &runtimequalification.Service{Store: store, Configurations: configs, AdapterArtifactPath: adapter, EvidenceRoot: filepath.Join(root, "evidence")}
	first, err := service.QualifyBackend(ctx, a.ID, "IMPLEMENTATION", "map-a-original")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.QualifyBackend(ctx, b.ID, "IMPLEMENTATION", "map-b-original")
	if err != nil {
		t.Fatal(err)
	}
	if first.Platform != second.Platform {
		t.Fatalf("same descriptor split qualifications: A=%+v B=%+v", first.Platform, second.Platform)
	}
	q1, err := store.GetQualification(ctx, first.Platform.QualificationID, first.Platform.QualificationGeneration)
	if err != nil || q1.State != runtimeplatform.StateCurrent {
		t.Fatalf("second backend retired A: %+v %v", q1, err)
	}
	for table, want := range map[string]int{"runtime_platform_descriptors": 1, "runtime_platform_compatibility_evidence": 1, "runtime_platform_qualifications": 1, "backend_platform_qualifications": 2, "backend_platform_mapping_heads": 2, "runtime_selection_bindings": 0, "runs": 0} {
		var count int
		if err = db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
			t.Fatalf("%s=%d want=%d err=%v", table, count, want, err)
		}
	}
	snapshot := func() []string {
		t.Helper()
		var out []string
		for _, table := range []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_lineages", "runtime_platform_requests", "backend_platform_qualification_requests", "backend_platform_qualifications", "backend_platform_mapping_heads", "runtime_selection_bindings", "runs"} {
			rows, e := db.SQL().QueryContext(ctx, "SELECT * FROM "+table)
			if e != nil {
				t.Fatal(e)
			}
			cols, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			var records []string
			for rows.Next() {
				values, ptrs := make([]any, len(cols)), make([]any, len(cols))
				for i := range values {
					ptrs[i] = &values[i]
				}
				if e = rows.Scan(ptrs...); e != nil {
					t.Fatal(e)
				}
				raw, e := json.Marshal(values)
				if e != nil {
					t.Fatal(e)
				}
				records = append(records, string(raw))
			}
			if e = rows.Err(); e != nil {
				t.Fatal(e)
			}
			_ = rows.Close()
			sort.Strings(records)
			out = append(out, table+":"+strings.Join(records, "\n"))
		}
		return out
	}
	before := snapshot()
	for _, c := range []struct{ backend, role, request string }{{b.ID, "IMPLEMENTATION", first.RequestID}, {a.ID, "REVIEW", first.RequestID}, {a.ID, "IMPLEMENTATION", "unsolicited-redundant-request"}} {
		if _, err = service.QualifyBackend(ctx, c.backend, c.role, c.request); !errors.Is(err, runtimeplatform.ErrSourceConflict) {
			t.Fatalf("identity conflict accepted: %+v %v", c, err)
		}
		if !reflect.DeepEqual(before, snapshot()) {
			t.Fatal("conflict changed durable facts")
		}
	}
	// Reproduce the old release's historical lifecycle through the real Store.
	// q2 uses the original actual collector evidence, not a fabricated report.
	lineage, err := store.GetLineage(ctx, q1.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	q2, err := store.Supersede(ctx, runtimeplatform.QualificationInput{QualificationID: "historical-q2", PlatformLineageID: q1.PlatformLineageID, DescriptorID: q1.DescriptorID, CompatibilityPolicyVersion: q1.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q1.CompatibilityEvidenceHash, IssuanceRequestID: "historical-supersede", Generation: q1.Generation + 1, ExpectedLineageVersion: lineage.Version, ExpectedQualificationID: q1.QualificationID, ExpectedGeneration: q1.Generation})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []codexconfig.Config{a, b} {
		ref, e := service.ResolvePlatformReference(ctx, c.Selection("CORE_TEST"), "IMPLEMENTATION")
		if e != nil || ref.QualificationID != q1.QualificationID {
			t.Fatalf("read silently used latest CURRENT: %+v %v", ref, e)
		}
	}
	renewed, err := service.QualifyBackend(ctx, a.ID, "IMPLEMENTATION", "map-a-explicit-renewal")
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Platform.QualificationID != q2.QualificationID || renewed.Platform.CompatibilityEvidenceHash != first.Platform.CompatibilityEvidenceHash {
		t.Fatalf("renewal did not explicitly select exact q2: %+v", renewed)
	}
	if ref, e := service.ResolvePlatformReference(ctx, a.Selection("CORE_TEST"), "IMPLEMENTATION"); e != nil || ref != renewed.Platform {
		t.Fatalf("A explicit head=%+v %v", ref, e)
	}
	if ref, e := service.ResolvePlatformReference(ctx, b.Selection("CORE_TEST"), "IMPLEMENTATION"); e != nil || ref.QualificationID != q1.QualificationID {
		t.Fatalf("A renewal changed B: %+v %v", ref, e)
	}
	before = snapshot()
	for _, m := range []runtimequalification.Mapping{first, second, renewed} {
		got, e := service.QualifyBackend(ctx, m.BackendID, m.RuntimeRole, m.RequestID)
		if e != nil || got != m {
			t.Fatalf("original receipt replay drift: %+v %v", got, e)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("historical replay changed facts")
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil
	db, err = sqlite.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	store = sqlite.NewRuntimePlatformStore(db)
	service.Store = store
	service.Configurations = sqlite.NewCodexConfigStore(db)
	before = snapshot()
	for _, m := range []runtimequalification.Mapping{first, second, renewed} {
		got, e := service.QualifyBackend(ctx, m.BackendID, m.RuntimeRole, m.RequestID)
		if e != nil || got != m {
			t.Fatalf("cold historical receipt drift: %+v %v", got, e)
		}
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("cold exact replay changed facts")
	}
	for _, q := range []runtimeplatform.Qualification{q1, q2} {
		stored, e := store.GetQualification(ctx, q.QualificationID, q.Generation)
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("qualification=%s generation=%d state=%s", stored.QualificationID, stored.Generation, stored.State)
	}
	t.Logf("A original=%s, B original=%s, A explicit renewal=%s; original receipts retained", first.Platform.QualificationID, second.Platform.QualificationID, renewed.Platform.QualificationID)
}
