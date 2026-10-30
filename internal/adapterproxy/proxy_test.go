package adapterproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyVerifiesIdentityAndForwardsAdapterSurface(t *testing.T) {
	var gotPath string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == IdentityPath {
			_, _ = w.Write([]byte(`{"dataRoot":"/data"}`))
			return
		}
		gotPath = r.URL.Path
		_, _ = w.Write([]byte("adapter"))
	}))
	defer core.Close()
	p, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/gpt"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	p.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if r.Code != http.StatusOK || gotPath != "/_adapters/gpt/readyz" {
		t.Fatalf("status/path = %d/%q", r.Code, gotPath)
	}
}

func TestTwoAdapterProxiesShareOneCore(t *testing.T) {
	var paths []string
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == IdentityPath {
			_, _ = w.Write([]byte(`{"dataRoot":"/data"}`))
			return
		}
		paths = append(paths, r.URL.Path)
		_, _ = w.Write([]byte("adapter"))
	}))
	defer core.Close()
	gpt, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/gpt"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/local"})
	if err != nil {
		t.Fatal(err)
	}
	for _, proxy := range []*Proxy{gpt, local} {
		r := httptest.NewRecorder()
		proxy.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if r.Code != http.StatusOK {
			t.Fatalf("proxy status = %d", r.Code)
		}
	}
	if len(paths) != 2 || paths[0] != "/_adapters/gpt/healthz" || paths[1] != "/_adapters/local/healthz" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestProxyRejectsNonLocalUnavailableAndMismatchedCore(t *testing.T) {
	for _, test := range []Config{
		{CoreURL: "http://192.0.2.1:8081", DataRoot: "/data", AdapterPath: "/_adapters/gpt"},
		{CoreURL: "http://127.0.0.1:1", DataRoot: "/data", AdapterPath: "/_adapters/gpt"},
	} {
		if _, err := New(context.Background(), test); err == nil {
			t.Fatalf("New(%+v) succeeded", test)
		}
	}
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"dataRoot":"/other"}`)) }))
	defer core.Close()
	if _, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/local"}); err == nil {
		t.Fatal("mismatched root accepted")
	}
}
