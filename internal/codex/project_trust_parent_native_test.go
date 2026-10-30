package codex

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/runtimepermission"
)

// This parent-owned acceptance probe uses the installed native runtime without
// a model Turn. Place its fixture outside global temporary/cache roots, since
// trusted execution intentionally authorizes temporary files.
func TestProjectTrustParentNativeCapabilities(t *testing.T) {
	exe, base := os.Getenv("DEVBRIDGE_TRUSTED_NATIVE"), os.Getenv("DEVBRIDGE_TRUSTED_NATIVE_ROOT")
	if exe == "" || base == "" {
		t.Skip("opt-in trusted native acceptance")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "parent-native-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	source, scratch, home := filepath.Join(root, "source"), filepath.Join(root, "scratch"), filepath.Join(root, "home")
	for _, dir := range []string{source, scratch, home, filepath.Join(source, "allowed"), filepath.Join(source, ".git"), filepath.Join(source, ".codex"), filepath.Join(source, ".agents")} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	targets := map[string]string{
		"worktreeOutsideLogicalPaths": filepath.Join(source, "elsewhere.txt"),
		"source":                      filepath.Join(source, "allowed", "marker.txt"),
		"outside":                     filepath.Join(root, "outside.txt"),
		"gitMetadata":                 filepath.Join(source, ".git", "parent-probe"),
		"codexMetadata":               filepath.Join(source, ".codex", "parent-probe"),
		"agentsMetadata":              filepath.Join(source, ".agents", "parent-probe"),
		"scratch":                     filepath.Join(scratch, "parent-probe"),
	}
	for _, temp := range []struct{ key, dir string }{{"systemTemp", ""}, {"slashTmp", "/tmp"}} {
		f, err := os.CreateTemp(temp.dir, "devbridge-parent-trust-")
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		path, err := filepath.EvalSymlinks(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		targets[temp.key] = path
		defer os.Remove(path)
	}
	for _, phase := range []string{"IMPLEMENTATION", "VERIFICATION", "NETWORK_ONLY", "DIAGNOSTIC"} {
		t.Run(phase, func(t *testing.T) {
			p := &runtimepermission.TaskPermissions{Version: TaskPermissionVersion, TaskID: "fixture-task", RevisionID: "fixture-revision", ExecutionID: "fixture-execution", RunID: "parent-" + phase, WorksetHash: strings.Repeat("a", 64), Purpose: phase, NetworkAccess: true, ScratchRoot: scratch}
			// JSON keeps this probe independent of the project's Go field names.
			snapshot, _ := json.Marshal(map[string]any{"projectId": "fixture-parent-" + runtimepermission.EnvironmentID(root), "authorizationRevision": 1, "trusted": phase != "NETWORK_ONLY", "networkFromAuthorization": true})
			if err := json.Unmarshal(snapshot, &p.TrustedExecution); err != nil {
				t.Fatal(err)
			}
			if phase != "VERIFICATION" {
				p.Purpose = "IMPLEMENTATION"
				p.Writes = []runtimepermission.WriteGrant{{Root: source, Kind: "PATHS", Paths: []string{"allowed"}}}
			}
			p.ContentHash = p.Hash()
			binding := RuntimeBinding{RuntimeWorkspaceRoots: []string{source, scratch}, WritableRoots: []string{source, scratch}, TaskPermissions: p}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			c, err := Start(ctx, Config{CodexPath: exe, TaskRuntimeBinding: &binding})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			profile := p.ProfileID()
			if phase == "DIAGNOSTIC" {
				c.diagnosticTaskPolicy = p.Clone()
				binding = RuntimeBinding{RuntimeWorkspaceRoots: []string{source}}
				profile = p.DiagnosticProfileID()
			}
			if _, err = c.StartThread(ctx, ThreadOptions{CWD: source, RuntimeBinding: binding}); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(targets)
			program := `import json,sys,socket
from pathlib import Path
out={}
for name,path in json.loads(sys.argv[1]).items():
 try:Path(path).write_text('native proof\n');out[name]='ALLOWED'
 except PermissionError:out[name]='DENIED'
host,port=sys.argv[2].rsplit(':',1)
try:
 s=socket.create_connection((host,int(port)),timeout=1);s.close();out['network']='ALLOWED'
except PermissionError:out['network']='DENIED'
except OSError as e:out['network']='DENIED' if e.errno in (1,13) else str(e)
print(json.dumps(out))`
			var result struct {
				ExitCode int    `json:"exitCode"`
				Stdout   string `json:"stdout"`
			}
			if err = c.rpc.Call(ctx, "command/exec", map[string]any{"command": []string{"/usr/bin/python3", "-c", program, string(raw), listener.Addr().String()}, "cwd": source, "permissionProfile": profile, "env": map[string]string{"TMPDIR": scratch}, "timeoutMs": 5000}, &result); err != nil || result.ExitCode != 0 {
				t.Fatalf("native probe: %v %+v", err, result)
			}
			var observed map[string]string
			if err = json.Unmarshal([]byte(result.Stdout), &observed); err != nil {
				t.Fatal(err, result.Stdout)
			}
			for _, key := range []string{"source", "worktreeOutsideLogicalPaths", "systemTemp", "slashTmp", "scratch", "network", "outside", "gitMetadata", "codexMetadata", "agentsMetadata"} {
				want := "ALLOWED"
				if key == "outside" || strings.HasSuffix(key, "Metadata") || phase == "DIAGNOSTIC" && key != "scratch" || phase == "NETWORK_ONLY" && (key == "worktreeOutsideLogicalPaths" || key == "systemTemp" || key == "slashTmp") {
					want = "DENIED"
				}
				if observed[key] != want {
					t.Errorf("%s: got %q want %s; complete observation=%s", key, observed[key], want, result.Stdout)
				}
			}
			t.Logf("actual native behavior: %s", result.Stdout)
			if phase == "IMPLEMENTATION" {
				testProjectTrustModuleDownload(t, ctx, c, binding, source, profile)
			}
		})
	}
}

// Proves a cold module download can write its cache under the native profile,
// using an in-process loopback Go proxy and no external dependency service.
func testProjectTrustModuleDownload(t *testing.T, ctx context.Context, c *Client, binding RuntimeBinding, cwd, profile string) {
	t.Helper()
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	module := "example.invalid/devbridge-parent-" + runtimepermission.EnvironmentID(cwd)
	mod := []byte("module " + module + "\n\ngo 1.20\n")
	var buffer bytes.Buffer
	z := zip.NewWriter(&buffer)
	for name, body := range map[string][]byte{"go.mod": mod, "proof.go": []byte("package fixture\nconst Answer = 42\n")} {
		f, err := z.Create(module + "@v1.0.0/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + module + "/@v/v1.0.0.info":
			w.Write([]byte(`{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`))
		case "/" + module + "/@v/v1.0.0.mod":
			w.Write(mod)
		case "/" + module + "/@v/v1.0.0.zip":
			w.Write(buffer.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxy.Close()
	config := c.taskEnvironmentConfig(binding, nil)
	environment := config["shell_environment_policy.set"].(map[string]any)
	cache, ok := environment["GOMODCACHE"].(string)
	if !ok || cache == "" {
		t.Fatal("trusted online module cache not configured")
	}
	defer func() {
		projectCache := filepath.Dir(cache)
		_ = filepath.Walk(projectCache, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
		if err := os.RemoveAll(projectCache); err != nil {
			t.Error("remove disposable module cache", err)
		}
	}()
	for key, value := range map[string]string{"GOPROXY": proxy.URL, "GOSUMDB": "off", "GONOSUMDB": "*", "GONOPROXY": "none", "GOPRIVATE": "", "GOENV": "/dev/null", "GOTOOLCHAIN": "local"} {
		environment[key] = value
	}
	var result struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := c.rpc.Call(ctx, "command/exec", map[string]any{"command": []string{goPath, "mod", "download", "-json", module + "@v1.0.0"}, "cwd": cwd, "permissionProfile": profile, "env": environment, "timeoutMs": 15000}, &result); err != nil || result.ExitCode != 0 {
		t.Fatalf("cold module download blocked: %v %+v", err, result)
	}
	if _, err := os.Stat(filepath.Join(cache, "cache", "download", filepath.FromSlash(module), "@v", "v1.0.0.zip")); err != nil {
		t.Fatal("module cache write not observed", err)
	}
	t.Log("cold Go module downloaded through isolated loopback proxy into project temporary cache")
}
