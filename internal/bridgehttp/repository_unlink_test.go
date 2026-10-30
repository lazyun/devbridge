package bridgehttp

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepositoryUnlinkClosedDTORejectsBypassFields(t *testing.T) {
	for _, field := range []string{"force", "outcome", "blocker_bypass", "targets", "registration_state"} {
		t.Run(field, func(t *testing.T) {
			body := `{"request_id":"request","project_id":"project","repository_id":"repository","expected_registration_generation":1,"expected_topology_revision":1,"reason":"","` + field + `":true}`
			req := httptest.NewRequest("POST", "/v1/repository-unlink/commit", strings.NewReader(body))
			recorder := httptest.NewRecorder()
			var input repositoryUnlinkCommitInput
			if decode(recorder, req, &input) {
				t.Fatalf("closed DTO accepted %q", field)
			}
		})
	}
}
