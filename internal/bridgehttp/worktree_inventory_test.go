package bridgehttp

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestWorktreeInventoryHTTPSharedFactsAndReadOnly(t *testing.T) {
	f := newConsoleFixture(t)
	defer f.plane.Close()
	api, err := controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	want, err := api.WorktreeInventory(context.Background(), f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/ui/api/worktree-inventory?project_id=" + f.projectID
	status, headers, body := getRaw(t, f.handler, path)
	var got worktreeinventory.Inventory
	if status != http.StatusOK || headers.Get("Cache-Control") != "no-store" || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("inventory %d %s", status, body)
	}
	if len(got.Items) != 1 || got.Items[0].Kind != "FORMAL" || got.Items[0].CleanupState != "PROTECTED_FORMAL" {
		t.Fatalf("formal missing: %+v", got)
	}
	if !reflect.DeepEqual(want.Items, got.Items) {
		t.Fatalf("shared API differs: got=%+v want=%+v", got, want)
	}
	status, _, body = getRaw(t, f.handler, path+"&format=text")
	for _, part := range []string{"gitRegistered=true", "directoryExists=true", "PROTECTED_FORMAL", got.Items[0].Path, got.Items[0].Head} {
		if status != http.StatusOK || !strings.Contains(body, part) {
			t.Fatalf("text parity missing %q: %d %s", part, status, body)
		}
	}
	for _, query := range []string{"&format=html", "&path=/tmp", "&cleanup=true"} {
		status, _, _ = getRaw(t, f.handler, path+query)
		if status != http.StatusBadRequest {
			t.Fatalf("unexpected query accepted %s: %d", query, status)
		}
	}
	status, _, _ = getRaw(t, f.handler, "/ui/api/worktree-inventory?project_id=unknown")
	if status != http.StatusNotFound {
		t.Fatalf("unknown project: %d", status)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete, http.MethodPut} {
		request := httptest.NewRequest(method, path, nil)
		response := httptest.NewRecorder()
		f.handler.ServeHTTP(response, request)
		if response.Code == http.StatusOK {
			t.Fatalf("mutation %s accepted", method)
		}
	}
}
