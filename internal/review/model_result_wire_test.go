package review

import (
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/workset"
)

func TestModelResultValidatesOriginalWireBeforeConversion(t *testing.T) {
	frozen := []workset.Target{{RepositoryID: "repo-app", MemberID: "member-app", MemberVersion: 7, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle-1", Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	for _, tc := range []struct{ name, before, after string }{
		{"case folded scope field", `"kind":"REPOSITORY"`, `"Kind":"REPOSITORY"`},
		{"case folded work field", `"goal":"Implement"`, `"Goal":"Implement"`},
		{"missing work constraints", `"constraints":[],`, ``},
		{"null work constraints", `"constraints":[]`, `"constraints":null`},
		{"missing scope paths", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{"kind":"REPOSITORY"}`},
		{"null scope paths", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{"kind":"REPOSITORY","paths":null}`},
		{"missing check instruction", `"instruction":"",`, ``},
		{"null check packages", `"packages":[]`, `"packages":null`},
		{"duplicate disposition", `"reviewDisposition":"APPROVAL_READY"`, `"reviewDisposition":"FURTHER_REVIEW_REQUIRED","reviewDisposition":"APPROVAL_READY"`},
		{"duplicate target alias", `"targetAlias":"target-1"`, `"targetAlias":"outside","targetAlias":"target-1"`},
		{"duplicate scope kind", `"kind":"REPOSITORY"`, `"kind":"NONE","kind":"REPOSITORY"`},
		{"escaped duplicate key", `"primaryTargetAlias":"target-1"`, `"primaryTargetAlias":"outside","primaryTarget\u0041lias":"target-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := strings.Replace(closedAliasModelResult, tc.before, tc.after, 1)
			if raw == closedAliasModelResult {
				t.Fatal("mutation did not apply")
			}
			if _, err := ParseModelResult(raw, frozen, "member-app"); err == nil {
				t.Fatal("nonconforming original model result accepted")
			}
		})
	}
}

func TestModelResultScalarAliasesAndHistoricalCanonicalResult(t *testing.T) {
	findings := `{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryTargetAlias":"","summary":"More evidence required","workSpec":null,"verificationSpec":null}`
	if result, err := ParseModelResult(findings, nil, ""); err != nil || result.ReviewDisposition != DispositionFurtherReviewRequired {
		t.Fatalf("schema-valid scalar findings rejected: %+v %v", result, err)
	}
	legacy := `{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","summary":"Historical findings","workSpec":null,"verificationSpec":null}`
	if _, err := ParseModelResult(legacy, nil, ""); err != nil {
		t.Fatalf("historical scalar result became unreadable: %v", err)
	}
	if _, err := ParseModelResult(strings.Replace(findings, `"primaryTargetAlias":""`, `"primaryTargetAlias":"target-1"`, 1), nil, ""); err == nil {
		t.Fatal("scalar result invented frozen target authority")
	}
}

func TestModelResultFindingsDoNotInventPrimaryWithoutTargets(t *testing.T) {
	frozen := []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}
	for _, version := range []string{"1", "2"} {
		raw := `{"version":` + version + `,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryTargetAlias":"","summary":"More evidence required","workSpec":null,"verificationSpec":null}`
		result, err := ParseModelResult(raw, frozen, "member")
		if err != nil || result.PrimaryMemberID != "" || len(result.RepositoryTargets) != 0 {
			t.Fatalf("v%s findings gained primary authority without targets: %+v %v", version, result, err)
		}
	}
}
