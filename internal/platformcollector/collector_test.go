package platformcollector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../build/remaining-constraints-2026-10-02/collector-tests")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	p, err := os.MkdirTemp(root, t.Name()+"-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(p) })
	return p
}

func TestCollectorRejectsMalformedSchema(t *testing.T) {
	for name, files := range map[string]map[string]json.RawMessage{"empty": {}, "invalid-json": {"v2/ThreadStartParams.json": json.RawMessage(`{`)}, "self-assertion": {"compatible.json": json.RawMessage(`{"measuredCompatibility":true,"fixture":true}`)}} {
		t.Run(name, func(t *testing.T) {
			if err := validateSchema(files); !errors.Is(err, ErrIncompatible) {
				t.Fatalf("schema accepted: %v", err)
			}
		})
	}
	if schemaType(json.RawMessage(`{"type":"number"}`), nil, "string", 0) {
		t.Fatal("wrong scalar schema accepted")
	}
	if schemaType(json.RawMessage(`{"$ref":"https://example.com/untrusted"}`), nil, "string", 0) {
		t.Fatal("external schema reference accepted")
	}
	if schemaType(json.RawMessage(`{"$ref":"#/definitions/loop"}`), map[string]json.RawMessage{"loop": json.RawMessage(`{"$ref":"#/definitions/loop"}`)}, "string", 0) {
		t.Fatal("recursive empty schema accepted")
	}
}

func TestCollectorRejectsChangedRegisteredArtifact(t *testing.T) {
	root := testRoot(t)
	path := filepath.Join(root, "artifact")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	hash := digest([]byte("original"))
	d := runtimeplatform.Descriptor{DescriptorID: "registered", RuntimeRole: "IMPLEMENTATION"}
	p := &Provider{manifest: Manifest{LauncherPath: path, LauncherDigest: hash, NativePath: path, NativeDigest: hash, AdapterArtifactPath: path, AdapterDigest: hash}, descriptors: map[string]runtimeplatform.Descriptor{"IMPLEMENTATION": d}}
	if _, err := p.ResolveDescriptor(context.Background(), d.DescriptorID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ResolveDescriptor(context.Background(), d.DescriptorID); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
		t.Fatalf("changed artifact accepted: %v", err)
	}
}

func TestCollectorRejectsScriptSelfAttestation(t *testing.T) {
	root := testRoot(t)
	script := filepath.Join(root, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'codex-cli 0.159.2\\n'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := New(context.Background(), Config{CodexPath: script, EvidenceRoot: filepath.Join(root, "artifacts")}); err == nil {
		t.Fatal("script self-attestation accepted")
	}
}

func TestCollectorArtifactIntegrity(t *testing.T) {
	root := testRoot(t)
	store, err := NewArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.Put([]byte(`{"actual":"observation"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Put([]byte(`{"actual":"observation"}`)); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, hash+".json"), []byte(`{"claimed":"success"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(hash); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("corrupt raw artifact accepted: %v", err)
	}
	if _, err = store.Put([]byte(`{"actual":"observation"}`)); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("corrupt artifact overwritten: %v", err)
	}
	if _, err = store.Read("../../other"); !errors.Is(err, ErrArtifactCorrupt) {
		t.Fatalf("unsafe content address accepted: %v", err)
	}
}

func TestCollectorRejectsUnregisteredDescriptor(t *testing.T) {
	collector := &Collector{provider: &Provider{descriptors: map[string]runtimeplatform.Descriptor{}}}
	if _, err := collector.CollectCompatibility(context.Background(), runtimeplatform.Descriptor{DescriptorID: "self-attested", RuntimeRole: "IMPLEMENTATION"}); !errors.Is(err, runtimeplatform.ErrIdentityMismatch) {
		t.Fatalf("unregistered descriptor accepted: %v", err)
	}
}
