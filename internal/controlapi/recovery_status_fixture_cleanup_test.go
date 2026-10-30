package controlapi_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/appserver"
)

// Native App Server may start a background plugin-catalog Git child. The
// fixture owns the isolated session created by appserver.Start (Setsid=true),
// so stop that verified session before testing.TempDir removes its home. Never
// signal a process group based only on a stale PID or an unrelated binding.
func stopRecoveryFixtureServers(f *upperLifecycleFixture) {
	if f.plane == nil {
		return
	}
	_ = f.plane.Supervisor.Shutdown()
	root, err := filepath.EvalSymlinks(f.dataRoot)
	if err != nil {
		return
	}
	rows, err := f.plane.DB.SQL().Query(`SELECT binding_json FROM app_server_run_bindings`)
	if err != nil {
		return
	}
	defer rows.Close()
	seen := map[int]bool{}
	for rows.Next() {
		var raw []byte
		var b appserver.Binding
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &b) != nil || seen[b.PID] || b.DataRoot != root {
			continue
		}
		seen[b.PID] = true
		if appserver.Verify(context.Background(), b) != nil {
			continue
		}
		group, err := syscall.Getpgid(b.PID)
		if err != nil || group != b.PID {
			continue
		}
		if err = syscall.Kill(-group, syscall.SIGKILL); err != nil {
			continue
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && syscall.Kill(-group, 0) == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
}
