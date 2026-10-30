package bridgehttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The authorization endpoints are the only local-user write surface for
// project-level trusted execution. Defaults must read as off, updates are
// CAS-guarded, and unknown projects are rejected.
func TestProjectExecutionAuthorizationEndpoints(t *testing.T) {
	plane, err := openTestPlane(t)
	if err != nil {
		t.Fatal(err)
	}
	defer plane.Close()
	server, err := New(plane, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := withTestLoopbackTransport(server.Handler())
	created, err := plane.Projects.Create(t.Context(), "Authorization fixture")
	if err != nil {
		t.Fatal(err)
	}
	projectID := created.ID

	status, _, body := getRaw(t, handler, "/v1/projects/"+projectID+"/execution-authorization")
	if status != http.StatusOK || !strings.Contains(body, `"trusted_execution":false`) || !strings.Contains(body, `"head_revision":0`) {
		t.Fatalf("default authorization must read off: %d %s", status, body)
	}

	status, _, body = putAuthorization(t, handler, projectID, `{"trusted_execution":true,"network_access":true,"expected_revision":0}`)
	if status != http.StatusOK || !strings.Contains(body, `"trusted_execution":true`) || !strings.Contains(body, `"network_access":true`) || !strings.Contains(body, `"head_revision":1`) {
		t.Fatalf("initial grant failed: %d %s", status, body)
	}
	if !strings.Contains(body, `"authorized_at":"`) || strings.Contains(body, `"authorized_at":"0001-01-01`) {
		t.Fatalf("initial grant must carry a real authorized_at: %s", body)
	}

	// GET after save must round-trip the persisted timestamps (catches direct
	// time scanning) and preserve the original authorized_at.
	status, _, body = getRaw(t, handler, "/v1/projects/"+projectID+"/execution-authorization")
	if status != http.StatusOK || !strings.Contains(body, `"authorized_at":"`) || strings.Contains(body, `"authorized_at":"0001-01-01`) {
		t.Fatalf("GET after save must return stored timestamps: %d %s", status, body)
	}

	// A stale expected_revision must be rejected without changing state.
	status, _, _ = putAuthorization(t, handler, projectID, `{"trusted_execution":false,"network_access":false,"expected_revision":0}`)
	if status != http.StatusConflict {
		t.Fatalf("stale CAS must fail with 409, got %d", status)
	}
	status, _, body = getRaw(t, handler, "/v1/projects/"+projectID+"/execution-authorization")
	if !strings.Contains(body, `"trusted_execution":true`) || !strings.Contains(body, `"head_revision":1`) {
		t.Fatalf("stale CAS must not change state: %s", body)
	}

	// Revoke with the correct revision; authorized_at stays frozen at the
	// first grant while updated_at advances.
	status, _, body = putAuthorization(t, handler, projectID, `{"trusted_execution":false,"network_access":false,"expected_revision":1}`)
	if status != http.StatusOK || !strings.Contains(body, `"trusted_execution":false`) || !strings.Contains(body, `"head_revision":2`) {
		t.Fatalf("revoke failed: %d %s", status, body)
	}
	status, _, body = getRaw(t, handler, "/v1/projects/"+projectID+"/execution-authorization")
	if !strings.Contains(body, `"authorized_at":"`) || strings.Contains(body, `"authorized_at":"0001-01-01`) {
		t.Fatalf("revoke must preserve the original authorized_at: %s", body)
	}

	// Unknown project is rejected on both read and write; a missing record
	// must not masquerade as default-off for a nonexistent project.
	status, _, _ = getRaw(t, handler, "/v1/projects/missing-project/execution-authorization")
	if status != http.StatusNotFound {
		t.Fatalf("unknown project GET must 404, got %d", status)
	}
	status, _, _ = putAuthorization(t, handler, "missing-project", `{"trusted_execution":true,"network_access":false,"expected_revision":0}`)
	if status != http.StatusNotFound {
		t.Fatalf("unknown project must 404, got %d", status)
	}
}

func putAuthorization(t *testing.T, handler http.Handler, projectID, body string) (int, http.Header, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/v1/projects/"+projectID+"/execution-authorization", strings.NewReader(body))
	setTestLoopbackTransport(request)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	defer response.Body.Close()
	raw := make([]byte, 4096)
	n, _ := response.Body.Read(raw)
	return response.StatusCode, response.Header, string(raw[:n])
}
