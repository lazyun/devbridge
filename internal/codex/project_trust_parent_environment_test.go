package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/runtimepermission"
)

func TestTrustedNetworkingUsesProjectScopedTemporaryModuleCache(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		trusted, network, diagnostic bool
	}{{"trusted-online", true, true, false}, {"trusted-offline", true, false, false}, {"network-only", false, true, false}, {"diagnostic", true, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			binding := permissionBinding()
			p := binding.TaskPermissions
			p.TrustedExecution = &runtimepermission.TrustedExecutionSnapshot{ProjectID: "fixture-project", AuthorizationRevision: 1, Trusted: tc.trusted, NetworkFromAuthorization: tc.network}
			p.NetworkAccess = tc.network
			c := &Client{}
			if tc.diagnostic {
				c.diagnosticTaskPolicy = p.Clone()
			}
			config := c.taskEnvironmentConfig(binding, nil)
			environment := config["shell_environment_policy.set"].(map[string]any)
			cache, set := environment["GOMODCACHE"]
			if tc.trusted && tc.network && !tc.diagnostic {
				want := filepath.Join(os.TempDir(), "devbridge-tool-cache", runtimepermission.EnvironmentID("fixture-project"), "go-mod")
				if cache != want {
					t.Fatalf("module downloads would escape the permitted temporary cache: %v", cache)
				}
			} else if set {
				t.Fatal("offline/diagnostic/legacy run lost its existing readable module cache")
			}
		})
	}
}
