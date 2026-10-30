package codex

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codex/wire"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestExperimentalPermissionWireMatchesGeneratedG0Fixture(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test fixture")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "wire", "testdata", "experimental-0.149.0-permission-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Provenance struct {
			CodexVersion       string `json:"codexVersion"`
			StartHash          string `json:"threadStartParamsSha256"`
			ResumeHash         string `json:"threadResumeParamsSha256"`
			TurnHash           string `json:"turnStartParamsSha256"`
			ProtocolBundleHash string `json:"protocolBundleSha256"`
		} `json:"provenance"`
		ThreadStart, ThreadResume, TurnStart struct {
			PermissionField      string `json:"permissionField"`
			ExclusiveLegacyField string `json:"exclusiveLegacyField"`
			RuntimeRootsField    string `json:"runtimeRootsField"`
		}
		ActivePermissionProfile struct {
			Fields                []string `json:"fields"`
			ContainsProfileDigest bool     `json:"containsProfileDigest"`
		} `json:"activePermissionProfile"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Provenance.CodexVersion != "0.149.0" || fixture.Provenance.StartHash == "" || fixture.Provenance.ResumeHash == "" || fixture.Provenance.TurnHash == "" {
		t.Fatalf("fixture lost G0 provenance: %+v", fixture.Provenance)
	}
	for name, want := range map[string]string{
		"ThreadStartParams.json":  fixture.Provenance.StartHash,
		"ThreadResumeParams.json": fixture.Provenance.ResumeHash,
		"TurnStartParams.json":    fixture.Provenance.TurnHash,
	} {
		actual, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "wire", "testdata", "generated-0.149.0", name))
		if err != nil {
			t.Fatalf("read G0 generated %s: %v", name, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(actual)); got != want {
			t.Fatalf("G0 generated %s hash=%s want=%s", name, got, want)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(actual, &schema); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"permissions", "runtimeWorkspaceRoots"} {
			if _, ok := schema.Properties[field]; !ok {
				t.Fatalf("G0 generated %s missing %s", name, field)
			}
		}
		legacy := "sandbox"
		if name == "TurnStartParams.json" {
			legacy = "sandboxPolicy"
		}
		if _, ok := schema.Properties[legacy]; !ok {
			t.Fatalf("G0 generated %s missing mutually-exclusive %s", name, legacy)
		}
		if name == "TurnStartParams.json" && (!strings.Contains(string(actual), `"SandboxPolicy"`) || !strings.Contains(string(actual), `"networkAccess"`) || !strings.Contains(string(actual), `"type": "boolean"`)) {
			t.Fatal("vendored generated turn schema lacks local boolean sandbox policy definitions")
		}
	}
	for name, fields := range map[string]struct{ permission, legacy, roots string }{
		"thread start":  {fixture.ThreadStart.PermissionField, fixture.ThreadStart.ExclusiveLegacyField, fixture.ThreadStart.RuntimeRootsField},
		"thread resume": {fixture.ThreadResume.PermissionField, fixture.ThreadResume.ExclusiveLegacyField, fixture.ThreadResume.RuntimeRootsField},
		"turn start":    {fixture.TurnStart.PermissionField, fixture.TurnStart.ExclusiveLegacyField, fixture.TurnStart.RuntimeRootsField},
	} {
		if fields.permission != "permissions" || fields.roots != "runtimeWorkspaceRoots" || (fields.legacy != "sandbox" && fields.legacy != "sandboxPolicy") {
			t.Fatalf("%s fixture is not the expected generated contract: %+v", name, fields)
		}
	}
	if fixture.ActivePermissionProfile.ContainsProfileDigest || len(fixture.ActivePermissionProfile.Fields) != 2 || fixture.ActivePermissionProfile.Fields[0] != "id" || fixture.ActivePermissionProfile.Fields[1] != "extends" {
		t.Fatalf("unexpected generated active permission profile facts: %+v", fixture.ActivePermissionProfile)
	}
	if err := (wire.ExperimentalActivePermissionProfile{ID: "exact"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (wire.ExperimentalActivePermissionProfile{}).Validate(); err == nil {
		t.Fatal("empty generated profile accepted")
	}
}

func TestExperimentalPermissionPlanDerivesExactWorksetAuthority(t *testing.T) {
	set := workset.Set{Version: workset.Version, WorkspaceID: "workspace", Targets: []workset.Target{
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 3, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src", "tests"}}},
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 4, OriginCommit: strings.Repeat("b", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}},
	}}
	plan, err := BuildExperimentalPermissionPlan(set, "devbridge-exact", "/work/app", "/scratch/run-1", []ExperimentalPermissionBinding{
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 4, Root: "/work/sdk"},
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 3, Root: "/work/app"},
	}, map[string]string{"executionId": "execution-1", "revisionId": "revision-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.members) != 2 || plan.members[0].RepositoryID != "app" || plan.members[0].Write.Kind != "PATHS" || plan.members[1].Write.Kind != "NONE" {
		t.Fatalf("members were not frozen from workset: %+v", plan.members)
	}
	if plan.scratchRoot != "/scratch/run-1" || plan.auditMetadata["executionId"] != "execution-1" || plan.worksetFingerprint == "" || plan.rootIdentityStatus != "UNVERIFIED" {
		t.Fatalf("missing auditable plan fields: %+v", plan)
	}
	if _, err := BuildExperimentalPermissionPlan(set, "devbridge-exact", "/work/app", "/scratch/run-1", []ExperimentalPermissionBinding{
		{RepositoryID: "sdk", MemberID: "sdk-member", MemberVersion: 4, Root: "/work/app/sdk"},
		{RepositoryID: "app", MemberID: "app-member", MemberVersion: 3, Root: "/work/app"},
	}, nil); err == nil {
		t.Fatal("nested repository roots widened the plan")
	}
	for _, serializer := range []func() (any, error){
		func() (any, error) { return plan.ThreadStartWire() },
		func() (any, error) { return plan.ThreadResumeWire() },
		func() (any, error) { return plan.TurnStartWire() },
	} {
		value, err := serializer()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"permissions":"devbridge-exact"`) || strings.Contains(string(raw), "sandbox") {
			t.Fatalf("profile wire contains wrong permission path: %s", raw)
		}
	}
}

func TestExperimentalPermissionPlanRejectsForgedOrMutatedPlan(t *testing.T) {
	if _, err := (ExperimentalPermissionPlan{}).ThreadStartWire(); err == nil {
		t.Fatal("forged exported plan serialized")
	}
	newPlan := func(t *testing.T) ExperimentalPermissionPlan {
		t.Helper()
		set := workset.Set{Version: workset.Version, WorkspaceID: "workspace", Targets: []workset.Target{{RepositoryID: "app", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}}}}
		plan, err := BuildExperimentalPermissionPlan(set, "exact", "/work/app", "/scratch/run", []ExperimentalPermissionBinding{{RepositoryID: "app", MemberID: "member", MemberVersion: 1, Root: "/work/app"}}, map[string]string{"execution": "one"})
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	cases := map[string]func(*ExperimentalPermissionPlan){
		"profile": func(p *ExperimentalPermissionPlan) { p.profileID = "wider" },
		"root":    func(p *ExperimentalPermissionPlan) { p.runtimeWorkspaceRoots[0] = "/other" },
		"scope": func(p *ExperimentalPermissionPlan) {
			p.members[0].Write.Kind = "REPOSITORY"
			p.members[0].Write.Paths = nil
		},
		"metadata":       func(p *ExperimentalPermissionPlan) { p.auditMetadata["execution"] = "other" },
		"identity state": func(p *ExperimentalPermissionPlan) { p.rootIdentityStatus = "VERIFIED" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			plan := newPlan(t)
			mutate(&plan)
			if _, err := plan.TurnStartWire(); err == nil {
				t.Fatal("mutated plan serialized")
			}
		})
	}
}

func TestExperimentalPermissionPlanRejectsAuthorityDrift(t *testing.T) {
	set := workset.Set{Version: workset.Version, WorkspaceID: "workspace", Targets: []workset.Target{{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}}
	valid := []ExperimentalPermissionBinding{{RepositoryID: "app", MemberID: "app-member", MemberVersion: 1, Root: "/work/app"}}
	cases := []struct {
		name, cwd, scratch string
		bindings           []ExperimentalPermissionBinding
	}{
		{"unknown member", "/work/app", "/scratch/run", []ExperimentalPermissionBinding{{RepositoryID: "other", MemberID: "other", MemberVersion: 1, Root: "/work/other"}}},
		{"version drift", "/work/app", "/scratch/run", []ExperimentalPermissionBinding{{RepositoryID: "app", MemberID: "app-member", MemberVersion: 2, Root: "/work/app"}}},
		{"outside cwd", "/work/other", "/scratch/run", valid},
		{"scratch overlap", "/work/app", "/work/app/tmp", valid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildExperimentalPermissionPlan(set, "exact", tc.cwd, tc.scratch, tc.bindings, nil); err == nil {
				t.Fatal("accepted authority drift")
			}
		})
	}
	for _, invalid := range []error{
		(wire.ExperimentalThreadStartPermissions{Sandbox: "bogus", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate(),
		(wire.ExperimentalThreadResumePermissions{Sandbox: "danger-full-access", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate(),
		(wire.ExperimentalTurnStartPermissions{SandboxPolicy: &wire.ExperimentalSandboxPolicy{Type: "externalSandbox"}, RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate(),
	} {
		if invalid == nil {
			t.Fatal("unsupported experimental sandbox path accepted")
		}
	}
}

func TestExperimentalWirePermissionPathsAreMutuallyExclusive(t *testing.T) {
	cases := []struct {
		name    string
		valid   func() error
		invalid func() error
	}{
		{"thread start", func() error {
			return (wire.ExperimentalThreadStartPermissions{Permissions: "exact", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}, func() error {
			return (wire.ExperimentalThreadStartPermissions{Permissions: "exact", Sandbox: "workspace-write", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}},
		{"thread resume", func() error {
			return (wire.ExperimentalThreadResumePermissions{Permissions: "exact", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}, func() error {
			return (wire.ExperimentalThreadResumePermissions{Permissions: "exact", Sandbox: "workspace-write", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}},
		{"turn start", func() error {
			return (wire.ExperimentalTurnStartPermissions{Permissions: "exact", RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}, func() error {
			return (wire.ExperimentalTurnStartPermissions{Permissions: "exact", SandboxPolicy: &wire.ExperimentalSandboxPolicy{Type: "readOnly"}, RuntimeWorkspaceRoots: []string{"/work/app"}}).Validate()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.valid(); err != nil {
				t.Fatalf("valid experimental profile: %v", err)
			}
			if err := tc.invalid(); err == nil {
				t.Fatal("combined permission paths accepted")
			}
		})
	}
}

func TestExperimentalSandboxPolicyUsesClosedLocalBooleanVariants(t *testing.T) {
	network := false
	policy := wire.ExperimentalSandboxPolicy{Type: "workspaceWrite", NetworkAccess: &network, WritableRoots: []string{"/work/app"}}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"networkAccess":false`) {
		t.Fatalf("local experimental sandbox did not serialize boolean network access: %s", raw)
	}
	for _, invalid := range []wire.ExperimentalSandboxPolicy{
		{Type: "externalSandbox"},
		{Type: "readOnly", WritableRoots: []string{"/work/app"}},
		{Type: "workspaceWrite", WritableRoots: []string{"/"}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("accepted unsupported or malformed experimental sandbox: %+v", invalid)
		}
	}
}
