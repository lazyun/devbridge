package codexlaunch

import (
	"reflect"
	"testing"
)

func TestExecutorArgsPreserveEndpointAndOverrideInheritedFeatures(t *testing.T) {
	original := []string{"app-server", "--listen", "unix:///fixture/rpc.sock", "-c", "features.apps=true"}
	args := ExecutorArgs(original)
	if !reflect.DeepEqual(args[:len(original)], original) {
		t.Fatal(args)
	}
	if len(original) != 5 || original[4] != "features.apps=true" {
		t.Fatal("mutated caller arguments")
	}
	want := []string{"-c", "features.apps=false", "-c", "features.plugins=false", "-c", "features.remote_plugin=false", "-c", "features.skill_mcp_dependency_install=false"}
	if !reflect.DeepEqual(args[len(original):], want) {
		t.Fatal(args)
	}
}
