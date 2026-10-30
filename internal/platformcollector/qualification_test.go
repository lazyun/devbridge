package platformcollector_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/platformcollector"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

func TestInstalledNativeQualificationExactMapping(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; no online provider")
	}
	root, err := filepath.Abs("../../build/remaining-constraints-2026-10-02/collector-native")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "qualification-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("durable qualification:", root)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	adapterPath := filepath.Join(root, "adapter-build")
	adapter, err := os.OpenFile(adapterPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(adapter, source)
	_ = source.Close()
	closeErr := adapter.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("copy actual adapter artifact: %v %v", copyErr, closeErr)
	}
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
	config := codexconfig.Config{ID: "native-backend", Name: "Installed native offline qualification", CodexPath: native, Enabled: true, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err = configs.Create(ctx, config); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewRuntimePlatformStore(db)
	service := &runtimequalification.Service{Store: store, Configurations: configs, EvidenceRoot: filepath.Join(root, "evidence"), AdapterArtifactPath: adapterPath}
	selection := config.Selection("CORE_TEST")
	if _, err = service.ResolvePlatformReference(ctx, selection, "IMPLEMENTATION"); !errors.Is(err, runtimeplatform.ErrQualificationMissing) {
		t.Fatalf("unregistered backend authority: %v", err)
	}
	// Reserve before collection and fail the first durable descriptor write.
	// The immutable pending identity must survive and allow the exact retry.
	provider, _, err := platformcollector.New(ctx, platformcollector.Config{CodexPath: native, AdapterArtifactPath: adapterPath, EvidenceRoot: filepath.Join(root, "discovery")})
	if err != nil {
		t.Fatal(err)
	}
	d, err := provider.Descriptor("IMPLEMENTATION")
	if err != nil {
		t.Fatal(err)
	}
	identity := runtimequalification.RequestIdentity{BackendID: config.ID, BackendFingerprint: config.Fingerprint(), RuntimeRole: "IMPLEMENTATION", RequestID: "qualify-explicit", DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}
	if err = store.ReserveQualificationRequest(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if _, err = db.SQL().ExecContext(ctx, `CREATE TRIGGER qualification_fail_descriptor BEFORE INSERT ON runtime_platform_descriptors BEGIN SELECT RAISE(ABORT,'qualification descriptor fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.QualifyBackend(ctx, config.ID, "IMPLEMENTATION", "qualify-explicit"); err == nil {
		t.Fatal("descriptor fault was ignored")
	}
	if pending, e := store.ReadQualificationRequest(ctx, "qualify-explicit"); e != nil || pending != identity {
		t.Fatalf("pending request lost: %+v %v", pending, e)
	}
	if _, err = db.SQL().ExecContext(ctx, `DROP TRIGGER qualification_fail_descriptor`); err != nil {
		t.Fatal(err)
	}
	var attempts [2]runtimequalification.Mapping
	var attemptErrors [2]error
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			attempts[i], attemptErrors[i] = service.QualifyBackend(ctx, config.ID, "IMPLEMENTATION", "qualify-explicit")
		}(i)
	}
	wg.Wait()
	first := attempts[0]
	if attemptErrors[0] != nil || attemptErrors[1] != nil || attempts[0] != attempts[1] {
		t.Fatalf("concurrent exact retry: %+v %v", attempts, attemptErrors)
	}
	ref, err := service.ResolvePlatformReference(ctx, selection, "IMPLEMENTATION")
	if err != nil || ref != first.Platform {
		t.Fatalf("exact mapping: %+v %v", ref, err)
	}
	counts := func() []string {
		t.Helper()
		var out []string
		for _, table := range []string{"runtime_platform_descriptors", "runtime_platform_compatibility_evidence", "runtime_platform_qualifications", "runtime_platform_requests", "runtime_platform_lineages", "backend_platform_qualifications", "backend_platform_qualification_requests", "runs"} {
			rows, e := db.SQL().QueryContext(ctx, "SELECT * FROM "+table)
			if e != nil {
				t.Fatal(e)
			}
			columns, e := rows.Columns()
			if e != nil {
				t.Fatal(e)
			}
			var records []string
			for rows.Next() {
				values, destinations := make([]any, len(columns)), make([]any, len(columns))
				for i := range values {
					destinations[i] = &values[i]
				}
				if e = rows.Scan(destinations...); e != nil {
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
	before := counts()
	other := config
	other.ID, other.Name = "different-backend", "Different backend"
	if err = configs.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	for _, conflict := range []struct{ backend, role, request string }{
		{config.ID, "REVIEW", "qualify-explicit"},
		{other.ID, "IMPLEMENTATION", "qualify-explicit"},
		{config.ID, "IMPLEMENTATION", "unbound-new-request"},
	} {
		if _, err = service.QualifyBackend(ctx, conflict.backend, conflict.role, conflict.request); !errors.Is(err, runtimeplatform.ErrSourceConflict) {
			t.Fatalf("changed qualification identity accepted: %+v %v", conflict, err)
		}
		if !reflect.DeepEqual(before, counts()) {
			t.Fatal("conflicting request changed platform facts")
		}
	}
	bad := selection
	bad.ConfigFingerprint = strings.Repeat("0", 64)
	if _, err = service.ResolvePlatformReference(ctx, bad, "IMPLEMENTATION"); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
		t.Fatalf("bad fingerprint accepted: %v", err)
	}
	replay, err := service.QualifyBackend(ctx, config.ID, "IMPLEMENTATION", "qualify-explicit")
	if err != nil || replay != first {
		t.Fatalf("qualification replay drift: %+v %v", replay, err)
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("read/replay changed durable platform or business facts")
	}
	if _, err = db.SQL().ExecContext(ctx, "UPDATE backend_platform_qualifications SET request_id='rewrite'"); err == nil {
		t.Fatal("immutable mapping updated")
	}
	if _, err = db.SQL().ExecContext(ctx, "UPDATE backend_platform_qualification_requests SET runtime_role='REVIEW'"); err == nil {
		t.Fatal("immutable request reservation updated")
	}
	q, err := store.GetQualification(ctx, first.Platform.QualificationID, first.Platform.QualificationGeneration)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := store.GetLineage(ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Supersede(ctx, runtimeplatform.QualificationInput{QualificationID: "independent-next-generation", PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, IssuanceRequestID: "independent-next-request", Generation: q.Generation + 1, ExpectedLineageVersion: lineage.Version, ExpectedQualificationID: q.QualificationID, ExpectedGeneration: q.Generation})
	if err != nil {
		t.Fatal(err)
	}
	freshLineage, err := store.GetLineage(ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	exactWithCAS := first.Platform
	exactWithCAS.LineageVersion = freshLineage.Version
	ref, err = service.ResolvePlatformReference(ctx, selection, "IMPLEMENTATION")
	if err != nil || ref != exactWithCAS {
		t.Fatalf("exact mapping silently adopted newest CURRENT: %+v %v", ref, err)
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
	ref, err = service.ResolvePlatformReference(ctx, selection, "IMPLEMENTATION")
	if err != nil || ref != exactWithCAS {
		t.Fatalf("cold exact mapping drift: %+v %v", ref, err)
	}
	// Replace bytes at the same registered adapter path without changing the
	// backend ConfigID/path/fingerprint. New measured identity must not inherit
	// the old mapping or any latest CURRENT qualification.
	f, err := os.OpenFile(adapterPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte("different-build-bytes"))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	before = counts()
	if _, err = service.ResolvePlatformReference(ctx, selection, "IMPLEMENTATION"); !errors.Is(err, runtimeplatform.ErrQualificationMissing) {
		t.Fatalf("same path changed bytes borrowed qualification: %v", err)
	}
	if !reflect.DeepEqual(before, counts()) {
		t.Fatal("changed artifact lookup silently issued platform authority")
	}
	old, err := store.ReadBackendMapping(ctx, config.ID, config.Fingerprint(), "IMPLEMENTATION", first.Platform.DescriptorID)
	if err != nil || old != first {
		t.Fatalf("old mapping rewritten: %+v %v", old, err)
	}
	// A reused issuance request must reject changed artifact identity before
	// creating a descriptor/evidence/generation or superseding the current one.
	before = counts()
	if _, err = service.QualifyBackend(ctx, config.ID, "IMPLEMENTATION", "qualify-explicit"); err == nil {
		t.Fatal("same request accepted different artifact identity")
	}
	if after := counts(); !reflect.DeepEqual(before, after) {
		t.Fatalf("conflicting qualification request wrote platform facts: before=%v after=%v err=%v", before, after, err)
	}
}
