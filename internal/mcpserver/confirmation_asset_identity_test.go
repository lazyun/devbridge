package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var confirmationMainScriptSHA = regexp.MustCompile(`^[a-f0-9]{64}$`)

// hashDeployedConfirmationScript recomputes the Card script identity directly
// from the embedded resource text, independent of confirmationScriptSHA256(), so
// a divergence between the bytes the server serves and the hash it advertises
// (a stale/old Card being loaded) is caught rather than assumed.
func hashDeployedConfirmationScript(html string) string {
	const start = `<script id="confirmation-main">`
	i := strings.Index(html, start)
	if i < 0 {
		return ""
	}
	rest := html[i+len(start):]
	end := strings.Index(rest, "</script>")
	if end < 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(rest[:end]))
	return hex.EncodeToString(sum[:])
}

// G16: the deployed Card resource, the runtime-advertised hash and the tool/host
// meta all carry one identical content identity, so the Host can never be
// served an older Card than the one it verifies against.
func TestConfirmationCardAssetIdentityMatchesDeployedResource(t *testing.T) {
	sourceHash := hashDeployedConfirmationScript(confirmationHTML)
	if !confirmationMainScriptSHA.MatchString(sourceHash) {
		t.Fatalf("deployed Card script hash not 64-hex: %q", sourceHash)
	}
	if runtimeHash := confirmationScriptSHA256(); runtimeHash != sourceHash {
		t.Fatalf("runtime advertised hash %q != hash of embedded source %q", runtimeHash, sourceHash)
	}

	base, err := NewStaticService(BridgeInfo{ApplicationName: "test", ApplicationVersion: "1", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "confirmation-asset-identity", Version: "1"}, nil)
	RegisterConfirmationTools(server, base)
	cs := connectPresentationTest(t, context.Background(), server)

	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	showFound := false
	for _, tool := range listed.Tools {
		if tool.Name != "confirmation_show" {
			continue
		}
		showFound = true
		if got := tool.Meta["devbridge/mainScriptSHA256"]; got != sourceHash {
			t.Fatalf("show tool meta hash %v != deployed source %s", got, sourceHash)
		}
		if got := tool.Meta["devbridge/componentVersion"]; got != confirmationComponentVersion {
			t.Fatalf("show tool componentVersion %v != %s", got, confirmationComponentVersion)
		}
	}
	if !showFound {
		t.Fatal("confirmation_show not registered")
	}

	resource, err := cs.ReadResource(context.Background(), &sdkmcp.ReadResourceParams{URI: confirmationResourceURI})
	if err != nil || len(resource.Contents) != 1 {
		t.Fatalf("confirmation resource=%+v err=%v", resource, err)
	}
	served := resource.Contents[0].Text
	if served != confirmationHTML {
		t.Fatal("served Card bytes differ from the embedded deployed resource")
	}
	if servedHash := hashDeployedConfirmationScript(served); servedHash != sourceHash {
		t.Fatalf("Host-recomputed hash of served bytes %s != advertised %s (stale Card)", servedHash, sourceHash)
	}
	if advertised, _ := resource.Contents[0].Meta["devbridge/mainScriptSHA256"].(string); advertised != sourceHash {
		t.Fatalf("resource meta advertised hash %q != served-bytes hash %s", advertised, sourceHash)
	}
	if !strings.Contains(served, `id="confirmation-main"`) {
		t.Fatal("served Card missing the confirmation-main script anchor")
	}
}
