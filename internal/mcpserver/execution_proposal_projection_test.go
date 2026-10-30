package mcpserver

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/executionproposal"
)

func TestExecutionProposalProjectionIncludesFrozenRuntimeLayout(t *testing.T) {
	value := executionProposalOutput(executionproposal.Proposal{
		ID:                      "execution-1",
		PrimaryMemberID:         "member-app",
		ApprovedWorksetHash:     "workset-hash",
		RuntimeAdapterIdentity:  "adapter-v1",
		RuntimeProtocolIdentity: "protocol-v1",
	}, false)
	if value.PrimaryMemberID != "member-app" || value.ApprovedWorksetHash != "workset-hash" || value.RuntimeAdapterIdentity != "adapter-v1" || value.RuntimeProtocolIdentity != "protocol-v1" {
		t.Fatalf("runtime layout omitted from projection: %+v", value)
	}
	if !json.Valid([]byte(executionProposalOutputSchema)) {
		t.Fatal("execution proposal schema is invalid")
	}
}
