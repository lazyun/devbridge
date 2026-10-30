package mcpserver_test

import (
	"context"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/mcpserver"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCanonicalInterfaceCatalogMatchesFreshProductionDiscovery(t *testing.T) {
	data, err := os.ReadFile("../../docs/interfaces.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	modelNames := documentedCatalogNames(t, doc, "Model-facing GPT MCP discovery", "Project Inspection also exposes")
	privateNames := documentedCatalogNames(t, doc, "The app-private actions", "One card click")
	_, _, baseURL := startTestServer(t, readyService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Independent client sessions must discover the same actual production
	// registration, including private visibility, rather than a cached catalog.
	for _, name := range []string{"canonical-interface-first", "canonical-interface-fresh"} {
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: name, Version: "1"}, nil)
		session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		listed, err := session.ListTools(ctx, nil)
		session.Close()
		if err != nil {
			t.Fatal(err)
		}
		var actualModel, actualPrivate []string
		for _, tool := range listed.Tools {
			ui, _ := tool.Meta["ui"].(map[string]any)
			visibility := strings.Join(testToolVisibility(ui), ",")
			if visibility == "app" {
				actualPrivate = append(actualPrivate, tool.Name)
			} else {
				actualModel = append(actualModel, tool.Name)
			}
			if tool.Name == "scope_expansion_propose" || tool.Name == "ui_scope_expansion_decide" {
				t.Fatalf("legacy ScopeExpansion mutation registered in fresh session: %s", tool.Name)
			}
		}
		sort.Strings(actualModel)
		sort.Strings(actualPrivate)
		if !reflect.DeepEqual(actualModel, modelNames) || !reflect.DeepEqual(actualPrivate, privateNames) {
			t.Fatalf("canonical catalog drift in %s: model docs=%v production=%v; private docs=%v production=%v", name, modelNames, actualModel, privateNames, actualPrivate)
		}
	}
	for _, statement := range []string{"ScopeExpansion is read-only legacy", "SCOPE_EXPANSION_LEGACY_ONLY", "Neither `scope_expansion_propose`", "nor `ui_scope_expansion_decide` is a production MCP tool"} {
		if !strings.Contains(doc, statement) {
			t.Fatalf("canonical interfaces omit retired ScopeExpansion contract %q", statement)
		}
	}
}

func documentedCatalogNames(t *testing.T, doc, start, end string) []string {
	t.Helper()
	_, section, ok := strings.Cut(doc, start)
	if !ok {
		t.Fatalf("canonical catalog missing %q", start)
	}
	section, _, ok = strings.Cut(section, end)
	if !ok {
		t.Fatalf("canonical catalog missing end %q", end)
	}
	rows := regexp.MustCompile("(?m)^\\| `([A-Za-z_]+)` \\|").FindAllStringSubmatch(section, -1)
	if len(rows) == 0 {
		t.Fatalf("canonical catalog %q is empty", start)
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row[1])
	}
	sort.Strings(names)
	return names
}
