package mcpserver_test

import (
	"context"
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"testing"
	"time"
)

type memberFileCapture struct {
	directFileAdapterService
	captured directfile.Input
}

func (s *memberFileCapture) DirectFileFileCreate(_ context.Context, in directfile.Input) (directfile.Result, error) {
	s.captured = in
	return directfile.Result{}, nil
}
func (s *memberFileCapture) DirectFileFileMove(_ context.Context, in directfile.Input) (directfile.Result, error) {
	s.captured = in
	return directfile.Result{}, nil
}
func TestSurfaceFileWritePreservesExactMemberMachineFields(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(mcpserver.SurfaceFileWriteInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"action":"CREATE","aiSessionId":"session","featureAreaId":"area","workflowId":"grant","repositoryId":"app","memberId":"app-member","memberVersion":1,"path":"same.txt","content":"bytes"}`,
		`{"action":"MOVE","aiSessionId":"session","featureAreaId":"area","workflowId":"grant","repositoryId":"app","memberId":"app-member","memberVersion":1,"path":"same.txt","destination":"next.txt","destinationRepositoryId":"sdk","destinationMemberId":"sdk-member"}`,
	} {
		var object any
		if err = json.Unmarshal([]byte(raw), &object); err != nil {
			t.Fatal(err)
		}
		if err = resolved.Validate(object); err != nil {
			t.Fatal(err)
		}
		var input mcpserver.SurfaceFileWriteInput
		if err = json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		service := &memberFileCapture{}
		if _, err = mcpserver.SurfaceFileWriteCore(context.Background(), service, input); err != nil {
			t.Fatal(err)
		}
		got := service.captured
		if got.RepositoryID != "app" || got.MemberID != "app-member" || got.MemberVersion != 1 || got.DestinationRepositoryID != input.DestinationRepositoryID || got.DestinationMemberID != input.DestinationMemberID {
			t.Fatalf("identity lost: %+v", got)
		}
	}
}

func (s *memberFileCapture) DirectFileFileRead(_ context.Context, in directfile.Input) (directfile.Result, error) {
	s.captured = in
	return directfile.Result{Path: in.Path}, nil
}
func TestDirectFileMemberIdentityThroughMCPTransport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	service := &memberFileCapture{}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "isolated-direct-file", Version: "1"}, nil)
	mcpserver.RegisterDirectFileTools(server, service)
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "isolated-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == "file_read" || tool.Name == "file_list" {
			raw, _ := json.Marshal(tool.InputSchema)
			var schema map[string]any
			if err = json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			properties := schema["properties"].(map[string]any)
			for _, name := range []string{"repositoryId", "memberId", "memberVersion", "reviewTarget"} {
				if properties[name] == nil {
					t.Fatal(tool.Name + " lost " + name)
				}
			}
		}
	}
	for _, name := range []string{"file_read", "file_write"} {
		args := map[string]any{"aiSessionId": "session", "featureAreaId": "area", "workflowId": "grant", "repositoryId": "sdk", "memberId": "sdk-member", "memberVersion": 1, "path": "same.txt"}
		if name == "file_write" {
			args["action"] = "CREATE"
			args["content"] = "literal bytes"
		}
		result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s result=%+v err=%v", name, result, err)
		}
		if service.captured.RepositoryID != "sdk" || service.captured.MemberID != "sdk-member" || service.captured.MemberVersion != 1 {
			t.Fatal("transport dropped target identity")
		}
	}
}

func TestDirectFileV2MutationSchemaRequiresExactFences(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(mcpserver.SurfaceFileWriteInputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{"action": "CREATE", "protocolVersion": float64(2), "aiSessionId": "session", "featureAreaId": "area", "workflowId": "grant", "repositoryId": "app", "memberId": "app-member", "memberVersion": float64(1), "requestId": "write-1", "runtimeEpoch": "epoch-1", "grantGeneration": float64(2), "path": "new.txt", "content": "bytes"}
	if err := resolved.Validate(base); err != nil {
		t.Fatalf("v2 CREATE without a preexisting digest must validate: %v", err)
	}
	delete(base, "runtimeEpoch")
	if err := resolved.Validate(base); err == nil {
		t.Fatal("v2 mutation without runtime epoch was accepted")
	}
	base["runtimeEpoch"] = "epoch-1"
	base["action"] = "DELETE"
	delete(base, "content")
	if err := resolved.Validate(base); err == nil {
		t.Fatal("v2 DELETE without expectedDigest was accepted")
	}
	base["expectedDigest"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := resolved.Validate(base); err != nil {
		t.Fatalf("fenced v2 DELETE rejected: %v", err)
	}
}
