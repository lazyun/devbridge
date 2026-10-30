package directfile

import (
	"crypto/sha256"
	"encoding/json"
	"testing"
)

func TestWorkerOperationSnapshotsIdentityAndPreservesLegacyHash(t *testing.T) {
	in := Input{Scope: Scope{AISessionID: "session", FeatureAreaID: "area", WorkflowID: "workflow"}, RequestID: "mutation", RuntimeEpoch: "epoch", GrantGeneration: 3, ExpectedDigest: "digest", RepositoryID: "repo", MemberID: "member", MemberVersion: 2, Path: "same.txt", Content: "exact\nbytes"}
	op := operationFromInput("REPLACE", in)
	// This is the original durable Operation payload shape. The optional worker
	// identity must not rewrite old receipt or journal hashes when absent.
	type historicalOperation struct {
		RepositoryID, MemberID                       string
		MemberVersion                                int
		DestinationRepositoryID, DestinationMemberID string
		Kind, Path, Destination                      string
		RequestID, RuntimeEpoch, ExpectedDigest      string
		GrantGeneration                              int64
		Content, ContentEncoding                     string
	}
	old := historicalOperation{RepositoryID: in.RepositoryID, MemberID: in.MemberID, MemberVersion: in.MemberVersion, Kind: "REPLACE", Path: in.Path, RequestID: in.RequestID, RuntimeEpoch: in.RuntimeEpoch, ExpectedDigest: in.ExpectedDigest, GrantGeneration: in.GrantGeneration, Content: in.Content}
	a, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || sha256.Sum256(a) != sha256.Sum256(b) {
		t.Fatalf("historical payload changed\ngot %s\nold %s", a, b)
	}
	in.WorkerClaim = &WorkerClaimIdentity{RequestID: "dispatch", WorkerID: "worker"}
	for _, kind := range []string{"CREATE", "REPLACE", "DELETE", "MOVE", "READ", "LIST"} {
		got := operationFromInput(kind, in)
		if got.WorkerClaim == nil || *got.WorkerClaim != *in.WorkerClaim || got.WorkerClaim == in.WorkerClaim {
			t.Fatalf("%s did not snapshot token: %+v", kind, got)
		}
		got.WorkerClaim.WorkerID = "changed"
		if in.WorkerClaim.WorkerID != "worker" {
			t.Fatal("operation changed caller token")
		}
	}
}
