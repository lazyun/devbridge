package adapterproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type duplexResultWriter struct {
	*httptest.ResponseRecorder
	err    error
	called bool
}

func (w *duplexResultWriter) EnableFullDuplex() error { w.called = true; return w.err }

func TestProxyFullDuplexEnableResults(t *testing.T) {
	for _, test := range []struct {
		name            string
		major           int
		err             error
		status          int
		forward, enable bool
	}{
		{"enabled", 1, nil, 204, true, true},
		{"unsupported-writer", 1, http.ErrNotSupported, 204, true, true},
		{"enable-error", 1, errors.New("writer failure"), 500, false, true},
		{"http2-already-duplex", 2, errors.New("must not call"), 204, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			forwarded := false
			p := &Proxy{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded = true; w.WriteHeader(http.StatusNoContent) })}
			w := &duplexResultWriter{ResponseRecorder: httptest.NewRecorder(), err: test.err}
			r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			r.ProtoMajor = test.major
			p.ServeHTTP(w, r)
			if w.Code != test.status || forwarded != test.forward || w.called != test.enable {
				t.Fatalf("status=%d forwarded=%t enabled=%t", w.Code, forwarded, w.called)
			}
		})
	}
}

// The transport may deliver all Content-Length bytes before it makes its final
// EOF probe. Hold that probe until the proxy starts flushing the response. This
// is a scheduling barrier, not an injected I/O error: Read still delegates to
// the original net/http request body.
type finalReadBarrier struct {
	io.ReadCloser
	remaining int64
	flushed   <-chan struct{}
	result    chan<- error
}

func (b *finalReadBarrier) Read(p []byte) (int, error) {
	final := b.remaining == 0
	if final {
		select {
		case <-b.flushed:
		case <-time.After(5 * time.Second):
			return 0, fmt.Errorf("response headers were not flushed")
		}
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if final {
		b.result <- err
	}
	return n, err
}

type headerFlushBarrier struct {
	http.ResponseWriter
	once    sync.Once
	flushed chan struct{}
}

func (w *headerFlushBarrier) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *headerFlushBarrier) WriteHeader(status int) {
	w.ResponseWriter.WriteHeader(status)
	_ = http.NewResponseController(w.ResponseWriter).Flush()
	w.once.Do(func() { close(w.flushed) })
}

func TestProxyPreservesResponseDuringFinalRequestBodyRead(t *testing.T) {
	finalRead := make(chan error, 1)
	observed := make(chan error, 1)
	response := strings.Repeat("complete response ", 700)
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == IdentityPath {
			fmt.Fprint(w, `{"dataRoot":"/data"}`)
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response[:1024])
		_ = http.NewResponseController(w).Flush()
		// Delay the response tail until the final EOF probe. Without full
		// duplex, net/http's response writer closes the request body, causing
		// that real probe to fail and the transport to close this connection.
		select {
		case err := <-finalRead:
			observed <- err
			if err == http.ErrBodyReadAfterClose {
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
				}
			}
		case <-time.After(5 * time.Second):
			observed <- fmt.Errorf("final body read was not observed")
			return
		}
		_, _ = io.WriteString(w, response[1024:])
	}))
	defer core.Close()
	p, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/local"})
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flushed := make(chan struct{})
		r.Body = &finalReadBarrier{ReadCloser: r.Body, remaining: r.ContentLength, flushed: flushed, result: finalRead}
		p.ServeHTTP(&headerFlushBarrier{ResponseWriter: w, flushed: flushed}, r)
	}))
	defer public.Close()
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Post(public.URL+"/mcp", "application/json", strings.NewReader(`{"method":"server/discover"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		t.Fatalf("response truncated after %d bytes: %v", len(body), readErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != response {
		t.Fatalf("response status=%d length=%d, want 200/%d", resp.StatusCode, len(body), len(response))
	}
	select {
	case err := <-observed:
		if err != io.EOF {
			t.Fatalf("final request body read=%v, want EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("missing final body read observation")
	}
}

func TestProxyFullDuplexStillPropagatesRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == IdentityPath {
			fmt.Fprint(w, `{"dataRoot":"/data"}`)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		close(started)
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-time.After(5 * time.Second):
		}
	}))
	defer core.Close()
	p, err := New(context.Background(), Config{CoreURL: core.URL, DataRoot: "/data", AdapterPath: "/_adapters/local"})
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(p)
	defer public.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, public.URL+"/mcp", strings.NewReader(`{"method":"server/discover"}`))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started
	cancel()
	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("canceled response unexpectedly completed")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream request was not canceled")
	}
}
