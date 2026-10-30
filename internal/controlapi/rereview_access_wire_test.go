package controlapi_test

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestReReviewSuccessorAccessRemainsReadableAndPreparableOverMCP(t *testing.T) {
	s := newCardLifecycleStack(t)
	pending := s.prepareReviewDecision(s.nextID("prepare"), "")
	response, result := s.commit(pending, s.nextID("confirm"))
	if response.IsError || result.Intent == nil || result.Intent.State != "COMPLETED" {
		t.Fatalf("confirmation %s", s.lastRaw)
	}
	successor := result.Intent.Steps[0].ReviewID
	var stored string
	if e := s.fixture.plane.DB.SQL().QueryRow(`SELECT forbidden_paths_json FROM review_grants WHERE review_id=?`, successor).Scan(&stored); e != nil || stored != "[]" {
		t.Fatalf("successor forbidden representation %q %v", stored, e)
	}
	call := func(name string, input any) review.UpperAccess {
		t.Helper()
		r, e := s.session.CallTool(s.ctx, &sdkmcp.CallToolParams{Name: name, Arguments: input})
		if e != nil {
			t.Fatal(e)
		}
		raw, e := json.Marshal(r.StructuredContent)
		if e != nil {
			t.Fatal(e)
		}
		if r.IsError {
			t.Fatalf("%s failed: %s %+v", name, raw, r.Content)
		}
		var envelope struct {
			Result review.UpperAccess `json:"result"`
		}
		if e = json.Unmarshal(raw, &envelope); e != nil {
			t.Fatal(e)
		}
		return envelope.Result
	}
	access := call("review_access_get", map[string]any{"aiSessionId": s.fixture.session, "reviewId": successor})
	if access.State == "PREPARING" {
		access = call("review_access_prepare", access.Target)
	}
	if access.State != "ACTIVE" || access.ProtocolVersion != 2 || access.ForbiddenPaths == nil || access.ReviewCreatedAt == nil {
		t.Fatalf("usable successor access %+v", access)
	}
	replay := call("review_access_prepare", access.Target)
	if replay.Target != access.Target || replay.State != "ACTIVE" {
		t.Fatalf("preparation replay %+v", replay)
	}
}
