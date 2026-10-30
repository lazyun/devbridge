package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSmokeLauncherBuildMetadataContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	launcher := filepath.Join(filepath.Dir(filename), "devbridge-smoke.sh")
	contents, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`PRODUCT_VERSION="0.1.0-dev"`,
		`git -C "${REPO_ROOT}" rev-parse HEAD`,
		`internal/buildinfo.ProductVersion=${PRODUCT_VERSION}`,
		`internal/buildinfo.GitCommit=${source_head}`,
		`Build: ${build}`,
		`local build="unavailable"`,
		`DATA_ROOT="${DEVBRIDGE_DATA_ROOT:-${DEFAULT_DATA_ROOT}}"`,
		`-data-root "${DATA_ROOT}"`,
		`-core-url "${SERVER_URL}"`,
		`DataRoot: ${DATA_ROOT}`,
		`Refusing to reset a user-selected DataRoot`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("launcher omits %q", required)
		}
	}
	for _, obsolete := range []string{`-state-root`, `-worktree-root`, `-log-dir`} {
		if strings.Contains(text, obsolete) {
			t.Fatalf("launcher retains obsolete storage option %q", obsolete)
		}
	}
	if strings.Contains(text, strings.Join([]string{"a7c1", "cba"}, "")) || strings.Contains(text, strings.Join([]string{"m3", "b3"}, "-")) {
		t.Fatal("launcher hardcodes a milestone or source commit")
	}
	command := exec.Command("bash", "-n", launcher)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v: %s", err, output)
	}
}
