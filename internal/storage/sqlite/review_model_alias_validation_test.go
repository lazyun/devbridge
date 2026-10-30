package sqlite

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
)

// Exercise the production materialization gate: malformed model aliases must
// become RESULT_INVALID, never a canonical result eligible for approval.
func TestReviewMaterializationRejectsMalformedModelAliasAuthority(t *testing.T) {
	targets := []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	admitted, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"version":2,"reviewDisposition":"APPROVAL_READY","repositoryTargets":[{"targetAlias":"target-1","proposedWriteScope":{"kind":"REPOSITORY","paths":[]}}],"primaryTargetAlias":"target-1","summary":"Update exact target","workSpec":{"implementationMode":"CODING_AGENT","goal":"Update target","constraints":[],"codexInstruction":"Update target","executionScope":{"kind":"REPOSITORY_TARGETS","paths":[]}},"verificationSpec":{"mode":"RUN","scope":"target-1","checks":[{"targetAlias":"target-1","targetAliases":["target-1"],"workingDirectory":".","kind":"TEST_COMMAND","command":"go test ./...","instruction":"","paths":[],"packages":[],"required":true}],"acceptanceCriteria":["Tests pass"],"codexInstruction":"Verify target"}}`
	claim := reviewMaterializationClaim{ReviewID: "review", Raw: raw, Review: review.Review{ID: "review", ExecutorKind: review.ExecutorCodingAgent, RepositoryTargets: targets, PrimaryMemberID: "member"}, WorkspaceBound: true, AdmittedTargetsJSON: string(admitted)}
	valid, err := claim.computeReviewMaterialization()
	if err != nil || valid.Terminal != reviewMaterializationValid || valid.Kind != materializationKindApproval || valid.FormalJSON == "" || valid.DecisionID == "" {
		t.Fatalf("valid model result failed: %+v %v", valid, err)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"unknown target field", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"unexpected":true`},
		{"null scope", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":null`},
		{"empty scope", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{}`},
		{"unknown scope field", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{"kind":"REPOSITORY","paths":[],"unexpected":true}`},
		{"invalid primary alias", `"primaryTargetAlias":"target-1"`, `"primaryTargetAlias":false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := claim
			candidate.Raw = strings.Replace(raw, tc.before, tc.after, 1)
			if candidate.Raw == raw {
				t.Fatal("mutation did not apply")
			}
			outcome, err := candidate.computeReviewMaterialization()
			if err != nil || outcome.Terminal != reviewMaterializationInvalid || outcome.Kind != materializationKindInvalid || outcome.ReasonCode != "RESULT_INVALID" || outcome.FormalJSON != "" || outcome.DecisionID != "" {
				t.Fatalf("malformed result escaped invalid gate: %+v %v", outcome, err)
			}
		})
	}
}
