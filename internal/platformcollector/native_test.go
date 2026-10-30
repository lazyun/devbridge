package platformcollector_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/platformcollector"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// The actual installed native executes commands. Only its Responses provider is
// local deterministic input; no fixture result is accepted as qualification.
func TestInstalledNativeCollector(t *testing.T) {
	path := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if path == "" {
		t.Skip("explicit installed native binary required; no online provider")
	}
	root, err := filepath.Abs("../../build/remaining-constraints-2026-10-02/collector-native")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "measurement-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("durable measurement:", root)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provider, collector, err := platformcollector.New(ctx, platformcollector.Config{CodexPath: path, EvidenceRoot: filepath.Join(root, "artifacts"), Timeout: 90 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	d, err := provider.Descriptor("IMPLEMENTATION")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(ctx, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := sqlite.NewRuntimePlatformStore(db)
	service := runtimeplatform.NewService(store, provider, collector)
	if _, err = service.RegisterDescriptor(ctx, d.DescriptorID); err != nil {
		t.Fatal(err)
	}
	e, err := service.CollectCompatibilityEvidence(ctx, d.DescriptorID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := provider.Artifacts().ReadReport(e.CollectionArtifactHash)
	if err != nil {
		t.Fatal(err)
	}
	for _, check := range platformcollector.RequiredChecks() {
		if !report.Checks[check] {
			t.Fatalf("unmeasured check %s", check)
		}
	}
	lineage, err := store.GetLineage(ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.Qualify(ctx, runtimeplatform.QualificationInput{QualificationID: "native-measurement", PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, CompatibilityEvidenceHash: e.EvidenceHash, IssuanceRequestID: "native-measurement-request", Generation: 1, ExpectedLineageVersion: lineage.Version})
	if err != nil {
		t.Fatal(err)
	}
	if q.State != runtimeplatform.StateCurrent {
		t.Fatal(q.State)
	}
	var runs int
	if err = db.SQL().QueryRowContext(ctx, "SELECT count(*) FROM runs").Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("collection created business Run: %d %v", runs, err)
	}
	t.Logf("native=%s descriptor=%s evidence=%s artifact=%s qualification=%s checks=%v", provider.Manifest().NativeVersion, d.DescriptorID, e.EvidenceHash, e.CollectionArtifactHash, q.QualificationID, report.Checks)
}
