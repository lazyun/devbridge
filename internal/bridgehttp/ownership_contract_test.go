package bridgehttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLegacyHTTPTaskCreationEndpointIsRetired(t *testing.T) {
	fixture := newConsoleFixture(t)
	defer fixture.plane.Close()

	request := httptest.NewRequest(http.MethodPost, "/v1/tasks", nil)
	request.RemoteAddr = "127.0.0.1:40000"
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("legacy task creation status=%d, want 405", recorder.Code)
	}
	values, err := fixture.plane.Tasks.List(context.Background())
	if err != nil || len(values) != 0 {
		t.Fatalf("retired endpoint persisted Tasks: count=%d err=%v", len(values), err)
	}
}
