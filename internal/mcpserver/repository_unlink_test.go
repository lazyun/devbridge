package mcpserver

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRepositoryUnlinkMCPCommitSchemaIsClosed(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(repositoryUnlinkCommitSchema), &schema); err != nil {
		t.Fatal(err)
	}
	if schema["additionalProperties"] != false {
		t.Fatal("commit schema must reject unknown fields")
	}
	properties := schema["properties"].(map[string]any)
	for _, forbidden := range []string{"force", "outcome", "blockerBypass", "targets", "registrationState"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("commit schema exposes %s", forbidden)
		}
	}
	for _, required := range []string{"requestId", "projectId", "repositoryId", "expectedRegistrationGeneration", "expectedTopologyRevision", "reason"} {
		if _, ok := properties[required]; !ok {
			t.Fatalf("commit schema misses %s", required)
		}
	}
}

func TestRepositoryUnlinkMCPToolsRegisterWithValidOutputSchema(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "repository-unlink", Version: "1"}, nil)
	registerRepositoryUnlinkTools(server, &StaticService{})
}
