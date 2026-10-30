package codex

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Local native protocol and actual sandbox only. No turn/start, model request,
// credentials, production task or externally addressed network traffic.
func TestTaskPermissionsNative(t *testing.T)             { testTaskPermissionsNative(t, false, false) }
func TestTaskPermissionsNativeNetworkGrant(t *testing.T) { testTaskPermissionsNative(t, true, false) }
func TestTaskPermissionsNativeImplementationPaths(t *testing.T) {
	testTaskPermissionsNative(t, false, true)
}

func TestTaskPermissionsNativeTemporaryPlacement(t *testing.T) {
	if os.Getenv("DEVBRIDGE_PERMISSION_NATIVE") == "" {
		t.Skip("opt-in native permission test")
	}
	t.Setenv("DEVBRIDGE_PERMISSION_NATIVE_ROOT", t.TempDir())
	testTaskPermissionsNative(t, false, false)
}

func TestTaskPermissionsNativeDiagnostic(t *testing.T) {
	testTaskPermissionsNative(t, true, true, true)
}

func testTaskPermissionsNative(t *testing.T, network, implementation bool, diagnostic ...bool) {
	isDiagnostic := len(diagnostic) > 0 && diagnostic[0]
	exe, parent := os.Getenv("DEVBRIDGE_PERMISSION_NATIVE"), os.Getenv("DEVBRIDGE_PERMISSION_NATIVE_ROOT")
	if exe == "" || parent == "" {
		t.Skip("opt-in native permission test")
	}
	if err := os.MkdirAll(parent, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(parent, "task-native-")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	// Preserve fixtures for an independent post-process content check.
	for _, name := range []string{"source", "copy", "scratch", "outside", "home"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"source", "copy", "outside"} {
		if err = os.WriteFile(filepath.Join(root, name, "marker.txt"), []byte(name+" original\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Symlink(filepath.Join(root, "source"), filepath.Join(root, "copy", "escape")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", filepath.Join(root, "home"))
	binding := permissionBinding()
	binding.RuntimeWorkspaceRoots = []string{filepath.Join(root, "source"), filepath.Join(root, "copy"), filepath.Join(root, "scratch")}
	binding.WritableRoots = binding.RuntimeWorkspaceRoots[1:]
	p := binding.TaskPermissions
	p.ScratchRoot = filepath.Join(root, "scratch")
	p.Copies[0].SourceRoot = filepath.Join(root, "source")
	p.Copies[0].Root = filepath.Join(root, "copy")
	if err = os.Mkdir(filepath.Join(root, "copy", "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	p.NetworkAccess = network
	if implementation {
		p.Purpose = "IMPLEMENTATION"
		p.Copies = nil
		p.Writes = []runtimepermission.WriteGrant{{Root: filepath.Join(root, "source"), Kind: "PATHS", Paths: []string{"allowed"}}}
		binding.RuntimeWorkspaceRoots = []string{filepath.Join(root, "source"), p.ScratchRoot}
		binding.WritableRoots = append([]string(nil), binding.RuntimeWorkspaceRoots...)
	}
	if err = os.Mkdir(filepath.Join(root, "source", "allowed"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(root, "copy", ".agents"), 0700); err != nil {
		t.Fatal(err)
	}
	p.ContentHash = p.Hash()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := Start(ctx, Config{CodexPath: exe, TaskRuntimeBinding: &binding})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	profileID := p.ProfileID()
	if isDiagnostic {
		client.diagnosticTaskPolicy = p.Clone()
		binding = RuntimeBinding{RuntimeWorkspaceRoots: []string{filepath.Join(root, "source")}}
		profileID = p.DiagnosticProfileID()
	}
	cwd := filepath.Join(root, "copy")
	if implementation {
		cwd = filepath.Join(root, "source")
	}
	thread, err := client.StartThread(ctx, ThreadOptions{CWD: cwd, RuntimeBinding: binding})
	if err != nil {
		t.Fatal(err)
	}
	// Empty native threads have no persisted rollout until their first model
	// Turn. Resume serialization/recovery are covered by protocol tests; this
	// offline probe deliberately does not manufacture a paid Turn.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	baseline, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	baseline.Close()
	payload := `import json,sys,socket
from pathlib import Path
r=Path(sys.argv[1]);result={'sourceRead':(r/'source'/'marker.txt').read_text()=='source original\n'}
for name,path in [('scopedWrite',r/'source'/'allowed'/'generated.txt'),('metadataWrite',r/'copy'/'.agents'/'generated.txt'),('copySourceWrite',r/'copy'/'marker.txt'),('copyFileCacheWrite',r/'copy'/'tsconfig.tsbuildinfo'),('copyWrite',r/'copy'/'dist'/'generated.txt'),('scratchWrite',r/'scratch'/'temporary.txt'),('sourceWrite',r/'source'/'marker.txt'),('outsideWrite',r/'outside'/'marker.txt'),('symlinkWrite',r/'copy'/'escape'/'marker.txt')]:
 try:path.write_text('probe\n');result[name]='ALLOWED'
 except PermissionError as e:result[name]='DENIED';result[name+'Error']=str(e)
 except OSError as e:result[name]='OTHER_ERROR';result[name+'Error']=str(e)
try:
 s=socket.create_connection(('127.0.0.1',int(sys.argv[2])),timeout=2);s.close();result['network']='ALLOWED'
except PermissionError as e:result['network']='DENIED';result['networkError']=str(e)
except OSError as e:result['network']='OTHER_ERROR';result['networkError']=str(e)
print(json.dumps(result))`
	var response struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	err = client.rpc.Call(ctx, "command/exec", map[string]any{"command": []string{"/usr/bin/python3", "-c", payload, root, port}, "cwd": cwd, "permissionProfile": profileID, "env": map[string]string{"TMPDIR": p.ScratchRoot}, "timeoutMs": 10000}, &response)
	data, _ := json.MarshalIndent(map[string]any{"root": root, "profileHash": p.ContentHash, "threadId": thread.ID, "response": response, "rpcError": errString(err), "modelInvoked": false, "networkAccess": network, "implementationPaths": implementation, "diagnosticReadOnly": isDiagnostic, "nativeResume": "NOT_RUN_EMPTY_THREAD"}, "", "  ")
	if e := os.WriteFile(filepath.Join(root, "observations.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	if err != nil || response.ExitCode != 0 {
		t.Fatalf("native command: %v %+v", err, response)
	}
	var observed map[string]any
	if json.Unmarshal([]byte(response.Stdout), &observed) != nil {
		t.Fatal(response.Stdout)
	}
	copyExpected, scopeExpected := "ALLOWED", "DENIED"
	if implementation {
		copyExpected, scopeExpected = "DENIED", "ALLOWED"
	}
	if isDiagnostic {
		copyExpected, scopeExpected = "DENIED", "DENIED"
	}
	if observed["sourceRead"] != true || observed["copyWrite"] != copyExpected || observed["copyFileCacheWrite"] != copyExpected || observed["scopedWrite"] != scopeExpected || observed["scratchWrite"] != "ALLOWED" {
		t.Fatalf("allowed controls: %s", response.Stdout)
	}
	for _, name := range []string{"sourceWrite", "outsideWrite", "symlinkWrite", "metadataWrite", "copySourceWrite"} {
		if observed[name] != "DENIED" {
			t.Fatalf("denial not proven: %s %s", name, response.Stdout)
		}
	}
	networkExpected := "DENIED"
	if network && !isDiagnostic {
		networkExpected = "ALLOWED"
	}
	if observed["network"] != networkExpected {
		t.Fatalf("network grant ignored: %s", response.Stdout)
	}
	for _, name := range []string{"source", "copy", "outside"} {
		b, e := os.ReadFile(filepath.Join(root, name, "marker.txt"))
		if e != nil || string(b) != name+" original\n" {
			t.Fatalf("%s mutated", name)
		}
	}
	if strings.Contains(response.Stdout, "OTHER_ERROR") {
		t.Fatal("environment error is not a denial")
	}
	t.Logf("native sandbox observations: %s", filepath.Join(root, "observations.json"))
}

func errString(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}
