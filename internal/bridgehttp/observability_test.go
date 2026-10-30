package bridgehttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/observability"
)

func TestLocalRequestLoggingReplacesHostileCorrelationHeader(t *testing.T) {
	var output bytes.Buffer
	ctx := observability.WithLogger(context.Background(), observability.New(observability.Options{Writer: &output}))
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/logs?token=query-secret", nil).WithContext(ctx)
	request.Header.Set("X-Request-ID", "Bearer header-secret")
	response := httptest.NewRecorder()
	localRequestLogging(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })).ServeHTTP(response, request)
	if strings.Contains(output.String(), "header-secret") || strings.Contains(output.String(), "query-secret") {
		t.Fatalf("request secret leaked: %s", output.String())
	}
	var requestID string
	scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
	count := 0
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		id, _ := entry["requestId"].(string)
		if !observability.ValidRequestID(id) {
			t.Fatalf("generated request ID is unsafe: %#v", entry)
		}
		if requestID != "" && requestID != id {
			t.Fatalf("request/result correlation changed: %q != %q", requestID, id)
		}
		requestID = id
		count++
	}
	if err := scanner.Err(); err != nil || count != 2 {
		t.Fatalf("entries=%d err=%v logs=%s", count, err, output.String())
	}
}
