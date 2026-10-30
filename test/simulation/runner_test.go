package simulation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureBin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"devbridge", "devbridge-mcp", "devbridge-local-mcp"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nexit 77\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}
func TestConfigFailsClosed(t *testing.T) {
	bin := fixtureBin(t)
	for _, c := range []Config{{}, {BinDir: bin, Scenario: "live"}, {BinDir: bin, Timeout: -time.Second}, {BinDir: t.TempDir()}} {
		if _, err := validateConfig(c); err == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
	if _, err := validateConfig(Config{BinDir: bin, Scenario: "all"}); err != nil {
		t.Fatal(err)
	}
}
func TestOrderedScenariosHaveIndependentNamesAndValidConfiguration(t *testing.T) {
	names := Scenarios()
	if len(names) != 15 || names[0] != "task-admission" || names[1] != "review-gates" || names[2] != "review-blocked" || names[3] != "review-further" || names[4] != "execution-runtime-gate" || names[5] != "direct-start-gates" || names[6] != "file-mutation-gates" || names[7] != "mutation-cancel-race" || names[8] != "file-result-gates" || names[9] != "file-result-no-change" || names[10] != "file-result-after-cancel" {
		t.Fatalf("initial Task admission must precede existing regressions: %v", names)
	}
	bin := fixtureBin(t)
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate scenario %q", name)
		}
		seen[name] = true
		if _, err := validateConfig(Config{BinDir: bin, Scenario: name}); err != nil {
			t.Fatalf("registered scenario %q cannot run: %v", name, err)
		}
	}
	names[0] = "caller mutation"
	if Scenarios()[0] != "task-admission" {
		t.Fatal("caller changed registered scenario order")
	}
}

func TestReviewDigestPreservesTypedJSONFieldOrder(t *testing.T) {
	value := struct {
		Z string `json:"z"`
		A string `json:"a"`
	}{Z: "first", A: "second"}
	if got := reviewPayloadDigest(value); got != digestText(`{"z":"first","a":"second"}`) || got == digestText(`{"a":"second","z":"first"}`) {
		t.Fatalf("Review receipt digest used another JSON ordering: %s", got)
	}
}
func TestEnvironmentDoesNotInheritAuthority(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("HTTPS_PROXY", "must-not-leak")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY", "1")
	env := strings.Join(isolatedEnvironment("/private/home", "/private/root", "/private/selection"), "\n")
	for _, bad := range []string{"must-not-leak", "GIT_CONFIG_COUNT", "OPENAI_API_KEY", "HTTPS_PROXY"} {
		if strings.Contains(env, bad) {
			t.Fatalf("inherited %s", bad)
		}
	}
	for _, want := range []string{"HOME=/private/home", "CODEX_HOME=/private/home", "GIT_CONFIG_NOSYSTEM=1", "DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=0"} {
		if !strings.Contains(env, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
func TestProcessFailureLeavesReportAndFreshOwnedDirectory(t *testing.T) {
	parent := t.TempDir()
	sentinel := filepath.Join(parent, "existing.txt")
	if err := os.WriteFile(sentinel, []byte("do not mutate"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Config{BinDir: fixtureBin(t), EvidenceDir: parent, Scenario: "happy", Timeout: 5 * time.Second})
	if err == nil || report.Status != "FAILED" || len(report.Scenarios) != 1 || !strings.Contains(report.Scenarios[0].Error, "exited before readiness") {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if filepath.Dir(report.Directory) != parent || report.Directory == parent {
		t.Fatalf("not a fresh child: %s", report.Directory)
	}
	if _, err = os.Stat(filepath.Join(report.Directory, "report.json")); err != nil {
		t.Fatal(err)
	}
	if b, e := os.ReadFile(sentinel); e != nil || string(b) != "do not mutate" {
		t.Fatalf("existing evidence changed: %q %v", b, e)
	}
}

// Opt-in packaged integration: unlike unit tests this starts the actual selected
// Core/proxy binaries and checks every scenario. Without binaries it is skipped.
func TestPublicBinarySimulation(t *testing.T) {
	bin := os.Getenv("DEVBRIDGE_SIM_BIN")
	if bin == "" {
		t.Skip("set DEVBRIDGE_SIM_BIN to compiled Core and both MCP proxies")
	}
	report, err := Run(context.Background(), Config{BinDir: bin, EvidenceDir: t.TempDir(), Timeout: 3 * time.Minute})
	t.Logf("public binary simulation report: %s", report.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != len(Scenarios()) {
		t.Fatalf("missing scenarios: %+v", report.Scenarios)
	}
	for _, s := range report.Scenarios {
		if s.Status != "PASSED" || len(s.Checks) == 0 {
			t.Fatalf("scenario not checked: %+v", s)
		}
	}
}

func TestEvidenceDirectoryBecomesAbsolute(t *testing.T) {
	c, err := validateConfig(Config{BinDir: fixtureBin(t), EvidenceDir: "relative-evidence"})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(c.EvidenceDir) {
		t.Fatalf("evidence path is relative: %s", c.EvidenceDir)
	}
}

func TestUnexpectedNativeInvocationFailsEvidence(t *testing.T) {
	bin := fixtureBin(t)
	script := "#!/bin/sh\nprintf unexpected > \"$DEVBRIDGE_SIM_TRIPWIRE\"\nexit 77\n"
	if err := os.WriteFile(filepath.Join(bin, "devbridge"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), Config{BinDir: bin, EvidenceDir: t.TempDir(), Scenario: "happy", Timeout: 5 * time.Second})
	if err == nil || report.Status != "FAILED" || !strings.Contains(report.Scenarios[0].Error, "unexpected native invocation") {
		t.Fatalf("native tripwire not enforced: %+v %v", report, err)
	}
	last := report.Scenarios[0].Checks[len(report.Scenarios[0].Checks)-1]
	if last.Name != "native runtime was never invoked" || last.Passed {
		t.Fatalf("false tripwire proof: %+v", last)
	}
}
