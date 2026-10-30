package platformcollector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

// PolicyVersion names an explicit measured Trusted Executor contract. It does
// not assert hostile-host containment, provider availability, model quality,
// dynamic tools, user-decision recovery, or detached-server continuity.
const PolicyVersion = "CODEX_NATIVE_TRUSTED_EXECUTOR_CONFORMANCE_V1"

type Config struct {
	CodexPath           string
	AdapterArtifactPath string
	EvidenceRoot        string
	Timeout             time.Duration
}

type Manifest struct {
	LauncherPath        string `json:"launcherPath"`
	LauncherDigest      string `json:"launcherDigest"`
	NativePath          string `json:"nativePath"`
	NativeDigest        string `json:"nativeDigest"`
	AdapterArtifactPath string `json:"adapterArtifactPath"`
	AdapterDigest       string `json:"adapterDigest"`
	NativeVersion       string `json:"nativeVersion"`
	Platform            string `json:"platform"`
	ProtocolDigest      string `json:"protocolDigest"`
}

type Provider struct {
	config      Config
	manifest    Manifest
	descriptors map[string]runtimeplatform.Descriptor
	artifacts   *ArtifactStore
}
type Collector struct{ provider *Provider }

var _ runtimeplatform.DescriptorProvider = (*Provider)(nil)
var _ runtimeplatform.IndependentCollector = (*Collector)(nil)

// New resolves the configured installation, hashes actual artifacts and exports
// the native schema. These are registration facts, never a qualification.
// No user config/auth is read; every subprocess uses a new isolated home.
func New(ctx context.Context, cfg Config) (*Provider, *Collector, error) {
	if cfg.AdapterArtifactPath == "" {
		var err error
		cfg.AdapterArtifactPath, err = os.Executable()
		if err != nil {
			return nil, nil, err
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 90 * time.Second
	}
	store, err := NewArtifactStore(cfg.EvidenceRoot)
	if err != nil {
		return nil, nil, err
	}
	selection, err := codexlaunch.Runtime(cfg.CodexPath)
	if err != nil {
		return nil, nil, err
	}
	launcher, err := filepath.EvalSymlinks(selection.Executable)
	if err != nil {
		return nil, nil, err
	}
	native, err := appserver.ResolveExecutable(selection.Executable)
	if err != nil {
		return nil, nil, err
	}
	adapter, err := filepath.EvalSymlinks(cfg.AdapterArtifactPath)
	if err != nil {
		return nil, nil, err
	}
	if !filepath.IsAbs(adapter) {
		return nil, nil, errors.New("adapter artifact must be absolute")
	}
	p := &Provider{config: cfg, artifacts: store, manifest: Manifest{LauncherPath: launcher, NativePath: native, AdapterArtifactPath: adapter, Platform: runtime.GOOS + "/" + runtime.GOARCH}, descriptors: map[string]runtimeplatform.Descriptor{}}
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	m, err := p.measure(ctx)
	if err != nil {
		return nil, nil, err
	}
	p.manifest = m
	// A descriptor covers the actual adapter + launcher + native implementation
	// tuple. A mutable path or config fingerprint cannot stand in for these bytes.
	implementation, _ := canonical(map[string]string{"adapter": m.AdapterDigest, "launcher": m.LauncherDigest, "native": m.NativeDigest, "platform": m.Platform})
	capabilities, _ := canonical(map[string]any{"policy": PolicyVersion, "requiredChecks": requiredChecks})
	for _, role := range []string{"IMPLEMENTATION", "VERIFICATION", "REVIEW", "INSPECTION"} {
		d := runtimeplatform.Descriptor{PlatformLineageID: "core-codex-trusted-executor/" + m.Platform + "/" + role, RuntimeRole: role, AdapterID: "devbridge-codex", AdapterImplementationDigest: digest(implementation), ProtocolID: "codex-app-server/" + m.NativeVersion, ProtocolSchemaDigest: m.ProtocolDigest, CapabilityDigest: digest(capabilities), DescriptorVersion: 1}
		d.DescriptorID = "platform_" + d.ComputeHash()
		p.descriptors[role] = d
	}
	return p, &Collector{provider: p}, nil
}

func (p *Provider) Descriptor(role string) (runtimeplatform.Descriptor, error) {
	d, ok := p.descriptors[role]
	if !ok {
		return d, runtimeplatform.ErrIdentityMismatch
	}
	return d, nil
}
func (p *Provider) ResolveDescriptor(ctx context.Context, id string) (runtimeplatform.Descriptor, error) {
	for _, d := range p.descriptors {
		if d.DescriptorID == id {
			if err := p.revalidateArtifacts(); err != nil {
				return runtimeplatform.Descriptor{}, err
			}
			return d, ctx.Err()
		}
	}
	return runtimeplatform.Descriptor{}, runtimeplatform.ErrDescriptorMissing
}
func (p *Provider) Manifest() Manifest        { return p.manifest }
func (p *Provider) Artifacts() *ArtifactStore { return p.artifacts }

func isolatedEnv(home string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "CODEX_HOME=" + home, "TMPDIR=" + home, "LANG=C.UTF-8", "NO_PROXY=127.0.0.1,localhost", "HTTP_PROXY=", "HTTPS_PROXY=", "ALL_PROXY=", "OTEL_SDK_DISABLED=true"}
}
func nativeFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var b [4]byte
	if _, err = f.Read(b[:]); err != nil {
		return err
	}
	if string(b[:]) != "\x7fELF" && string(b[:]) != "\xcf\xfa\xed\xfe" && string(b[:]) != "\xfe\xed\xfa\xcf" && string(b[:2]) != "MZ" {
		return fmt.Errorf("%w: native executable required, scripts cannot self-attest", ErrIncompatible)
	}
	return nil
}
func (p *Provider) measure(ctx context.Context) (Manifest, error) {
	m := p.manifest
	if err := nativeFile(m.NativePath); err != nil {
		return m, err
	}
	var err error
	for _, item := range []struct {
		path        string
		destination *string
	}{{m.LauncherPath, &m.LauncherDigest}, {m.NativePath, &m.NativeDigest}, {m.AdapterArtifactPath, &m.AdapterDigest}} {
		*item.destination, err = fileDigest(item.path)
		if err != nil {
			return m, err
		}
	}
	home, err := os.MkdirTemp(p.artifacts.root, ".measurement-")
	if err != nil {
		return m, err
	}
	defer os.RemoveAll(home)
	cmd := exec.CommandContext(ctx, m.NativePath, "--version")
	cmd.Env = isolatedEnv(home)
	cmd.Dir = home
	version, err := cmd.Output()
	if err != nil {
		return m, err
	}
	m.NativeVersion = strings.TrimSpace(string(version))
	if m.NativeVersion != "codex-cli 0.149.0" && m.NativeVersion != "codex-cli 0.159.2" {
		return m, fmt.Errorf("%w: unreviewed native version %q", ErrIncompatible, m.NativeVersion)
	}
	out := filepath.Join(home, "schema")
	cmd = exec.CommandContext(ctx, m.NativePath, "app-server", "generate-json-schema", "--experimental", "--out", out)
	cmd.Env = isolatedEnv(home)
	cmd.Dir = home
	if raw, e := cmd.CombinedOutput(); e != nil {
		return m, fmt.Errorf("%w: schema export: %v %.300s", ErrIncompatible, e, raw)
	}
	files := map[string]json.RawMessage{}
	err = filepath.WalkDir(out, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrArtifactCorrupt
		}
		if filepath.Ext(path) != ".json" {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var v any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if e = dec.Decode(&v); e != nil {
			return e
		}
		raw, e = json.Marshal(v)
		if e != nil {
			return e
		}
		rel, e := filepath.Rel(out, path)
		if e != nil {
			return e
		}
		files[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil {
		return m, err
	}
	if err = validateSchema(files); err != nil {
		return m, err
	}
	raw, err := canonical(files)
	if err != nil {
		return m, err
	}
	m.ProtocolDigest, err = p.artifacts.Put(raw)
	return m, err
}

func validateSchema(files map[string]json.RawMessage) error {
	required := map[string][]string{
		"v2/ThreadStartParams.json":         {"cwd", "model", "sandbox", "runtimeWorkspaceRoots"},
		"v2/ThreadResumeParams.json":        {"threadId", "cwd", "sandbox", "runtimeWorkspaceRoots"},
		"v2/TurnStartParams.json":           {"threadId", "input", "outputSchema", "model", "effort", "serviceTier", "sandboxPolicy", "runtimeWorkspaceRoots"},
		"v2/TurnInterruptParams.json":       {"threadId", "turnId"},
		"v2/TurnCompletedNotification.json": {"threadId", "turn"},
	}
	for name, fields := range required {
		var schema struct {
			Type        string                     `json:"type"`
			Properties  map[string]json.RawMessage `json:"properties"`
			Definitions map[string]json.RawMessage `json:"definitions"`
		}
		if json.Unmarshal(files[name], &schema) != nil || schema.Type != "object" {
			return fmt.Errorf("%w: missing/malformed schema %s", ErrIncompatible, name)
		}
		for _, field := range fields {
			if len(schema.Properties[field]) == 0 {
				return fmt.Errorf("%w: schema %s lacks %s", ErrIncompatible, name, field)
			}
			if field == "outputSchema" {
				continue
			} // The native schema deliberately accepts arbitrary JSON Schema here.
			want := "string"
			switch field {
			case "runtimeWorkspaceRoots", "input":
				want = "array"
			case "turn", "sandboxPolicy":
				want = "object"
			}
			if !schemaType(schema.Properties[field], schema.Definitions, want, 0) {
				return fmt.Errorf("%w: schema %s field %s does not admit %s", ErrIncompatible, name, field, want)
			}
		}
	}
	return nil
}

func schemaType(raw json.RawMessage, definitions map[string]json.RawMessage, want string, depth int) bool {
	if depth > 12 {
		return false
	}
	var node map[string]json.RawMessage
	if json.Unmarshal(raw, &node) != nil {
		return false
	}
	var simple string
	if json.Unmarshal(node["type"], &simple) == nil && simple == want {
		return true
	}
	var types []string
	if json.Unmarshal(node["type"], &types) == nil {
		for _, typ := range types {
			if typ == want {
				return true
			}
		}
	}
	var ref string
	if json.Unmarshal(node["$ref"], &ref) == nil && strings.HasPrefix(ref, "#/definitions/") {
		return schemaType(definitions[strings.TrimPrefix(ref, "#/definitions/")], definitions, want, depth+1)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		var alternatives []json.RawMessage
		if json.Unmarshal(node[key], &alternatives) == nil {
			for _, option := range alternatives {
				if schemaType(option, definitions, want, depth+1) {
					return true
				}
			}
		}
	}
	return false
}
func (p *Provider) revalidateArtifacts() error {
	for _, item := range []struct{ path, want string }{{p.manifest.LauncherPath, p.manifest.LauncherDigest}, {p.manifest.NativePath, p.manifest.NativeDigest}, {p.manifest.AdapterArtifactPath, p.manifest.AdapterDigest}} {
		got, err := fileDigest(item.path)
		if err != nil {
			return err
		}
		if got != item.want {
			return runtimeplatform.ErrIdentityMismatch
		}
	}
	return nil
}

var requiredChecks = []string{"artifact_identity", "protocol_schema", "initialize", "two_root_command_write", "structured_result", "cold_thread_resume", "readonly_command_denied", "turn_interrupt"}

type TranscriptEntry struct {
	Direction string          `json:"direction"`
	Message   json.RawMessage `json:"message"`
}
type Report struct {
	Version            int               `json:"version"`
	Policy             string            `json:"policy"`
	DescriptorID       string            `json:"descriptorId"`
	Manifest           Manifest          `json:"manifest"`
	SchemaArtifactHash string            `json:"schemaArtifactHash"`
	Checks             map[string]bool   `json:"checks"`
	Transcript         []TranscriptEntry `json:"transcript"`
	Observations       map[string]string `json:"observations"`
	Failure            string            `json:"failure,omitempty"`
}

func (c *Collector) CollectCompatibility(ctx context.Context, d runtimeplatform.Descriptor) (runtimeplatform.CollectedCompatibility, error) {
	var zero runtimeplatform.CollectedCompatibility
	want, err := c.provider.Descriptor(d.RuntimeRole)
	if err != nil || !reflect.DeepEqual(want, d) {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	ctx, cancel := context.WithTimeout(ctx, c.provider.config.Timeout)
	defer cancel()
	m, err := c.provider.measure(ctx)
	if err != nil {
		return zero, err
	}
	if !reflect.DeepEqual(m, c.provider.manifest) {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	r := Report{Version: 1, Policy: PolicyVersion, DescriptorID: d.DescriptorID, Manifest: m, SchemaArtifactHash: m.ProtocolDigest, Checks: map[string]bool{"artifact_identity": true, "protocol_schema": true}, Observations: map[string]string{}}
	err = runConformance(ctx, c.provider, &r)
	if err == nil {
		err = c.provider.revalidateArtifacts()
	}
	if err == nil {
		for _, check := range requiredChecks {
			if !r.Checks[check] {
				err = fmt.Errorf("%w: missing check %s", ErrIncompatible, check)
				break
			}
		}
	}
	if err != nil {
		r.Failure = err.Error()
	}
	raw, encodeErr := canonical(r)
	if encodeErr != nil {
		return zero, encodeErr
	}
	hash, saveErr := c.provider.artifacts.Put(raw)
	if saveErr != nil {
		return zero, saveErr
	}
	if err != nil {
		return zero, fmt.Errorf("%w (report %s): %v", ErrIncompatible, hash, err)
	}
	return runtimeplatform.CollectedCompatibility{DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole, AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest, CompatibilityPolicyVersion: PolicyVersion, CollectorIdentity: "devbridge-core-independent-native-collector-v1", CollectionArtifactHash: hash, MeasuredCompatibility: true}, nil
}

func RequiredChecks() []string {
	out := append([]string(nil), requiredChecks...)
	sort.Strings(out)
	return out
}
