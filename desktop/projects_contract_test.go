package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestCoreStatusCodesNeverCollapseToCoreUnavailable is the direct regression
// for the observed dogfood failure: a reachable Core answering HTTP 405 was
// surfaced as CORE_UNAVAILABLE. Reachability is not contract compatibility.
func TestCoreStatusCodesNeverCollapseToCoreUnavailable(t *testing.T) {
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusNotFound, desktopCodeNotFound},
		{http.StatusMethodNotAllowed, desktopCodeUnsupportedOperation},
		{http.StatusBadRequest, desktopCodeInvalidRequest},
		{http.StatusConflict, desktopCodeConflict},
		{http.StatusUnauthorized, desktopCodePermissionDenied},
		{http.StatusForbidden, desktopCodePermissionDenied},
		{http.StatusInternalServerError, desktopCodeCoreError},
		{http.StatusServiceUnavailable, desktopCodeCoreError},
	}
	for _, testCase := range cases {
		status, code := testCase.status, testCase.code
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			api := newHTTPProjectCoreAPI(client)

			_, listErr := api.ListProjects(context.Background(), "http://127.0.0.1:8081")
			assertMappedCode(t, listErr, code)

			_, registerErr := api.RegisterProject(context.Background(), "http://127.0.0.1:8081", RegisterProjectInput{Name: "One", RepositoryPath: "/source/one"})
			assertMappedCode(t, registerErr, code)
		})
	}
}

func assertMappedCode(t *testing.T, err error, wantCode string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	result := mapDesktopProjectError(err, false)
	if result.Code != wantCode {
		t.Fatalf("code = %s (message %q), want %s; err = %v", result.Code, result.Message, wantCode, err)
	}
	if wantCode != desktopCodeCoreUnavailable && result.Code == desktopCodeCoreUnavailable {
		t.Fatalf("reachable-Core HTTP status collapsed to CORE_UNAVAILABLE: %v", err)
	}
}

func TestConnectionRefusedMapsToCoreUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	api := newHTTPProjectCoreAPI(&http.Client{Timeout: 2 * time.Second})
	_, listErr := api.ListProjects(context.Background(), "http://"+endpoint)
	assertMappedCode(t, listErr, desktopCodeCoreUnavailable)

	_, registerErr := api.RegisterProject(context.Background(), "http://"+endpoint, RegisterProjectInput{Name: "One", RepositoryPath: "/source/one"})
	assertMappedCode(t, registerErr, desktopCodeCoreUnavailable)
}

func TestTimeoutBeforeCoreRespondsMapsToCoreUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	api := newHTTPProjectCoreAPI(&http.Client{Timeout: 50 * time.Millisecond})
	_, listErr := api.ListProjects(context.Background(), server.URL)
	assertMappedCode(t, listErr, desktopCodeCoreUnavailable)
}

func TestTypedCoreErrorsPassThroughWhileUnknownCodesMapToOperationFailure(t *testing.T) {
	app := projectTestApp(&projectAPIFake{}, true)
	app.projectAPI = &projectAPIFake{registerErr: &coreAPIError{Code: "INVALID_ARGUMENT", Message: "name is required"}}
	result := app.RegisterProject(RegisterProjectInput{Name: "One", RepositoryPath: "/source/one"})
	if result.Error == nil || result.Error.Code != "INVALID_ARGUMENT" || result.Error.Message != "name is required" {
		t.Fatalf("INVALID_ARGUMENT = %+v", result)
	}
	app.projectAPI = &projectAPIFake{registerErr: &coreAPIError{Code: "INFRASTRUCTURE_ERROR", Message: "storage failure"}}
	result = app.RegisterProject(RegisterProjectInput{Name: "One", RepositoryPath: "/source/one"})
	if result.Error == nil || result.Error.Code != desktopCodeRegistrationFailed {
		t.Fatalf("unknown typed code = %+v", result)
	}
}
