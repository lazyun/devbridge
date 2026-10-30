package review

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/structuredschema"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestImplementationResultSchemaAndInvalidProposals(t *testing.T) {
	if !json.Valid([]byte(ImplementationResultJSONSchema)) {
		t.Fatal("invalid backend output schema")
	}
	if err := structuredschema.Validate([]byte(ImplementationResultJSONSchema)); err != nil {
		t.Fatal(err)
	}
	if err := structuredschema.Validate([]byte(ResultJSONSchema)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"summary only", `{"version":1,"summary":"No plan"}`, `{"version":1,"summary":"bad","extra":"unknown"}`, `{} {}`} {
		if _, err := ParseImplementationResult(raw); err == nil {
			t.Fatalf("invalid proposal accepted: %s", raw)
		}
	}
}

const closedAliasModelResult = `{"version":2,"reviewDisposition":"APPROVAL_READY","repositoryTargets":[{"targetAlias":"target-1","proposedWriteScope":{"kind":"REPOSITORY","paths":[]}}],"primaryTargetAlias":"target-1","summary":"Implement the bounded change.","workSpec":{"implementationMode":"CODING_AGENT","goal":"Implement","constraints":[],"codexInstruction":"Implement","executionScope":{"kind":"REPOSITORY_TARGETS","paths":[]}},"verificationSpec":{"mode":"RUN","scope":"target-1","checks":[{"targetAlias":"target-1","workingDirectory":".","targetAliases":["target-1"],"kind":"TEST_COMMAND","command":"go test ./...","instruction":"","paths":[],"packages":[],"required":true}],"acceptanceCriteria":["Tests pass"],"codexInstruction":"Verify"}}`

func TestModelResultRejectsLossyAliasConversion(t *testing.T) {
	frozen := []workset.Target{{RepositoryID: "repo-app", MemberID: "member-app", MemberVersion: 7, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle-1", Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	for _, tc := range []struct{ name, before, after string }{
		{"unknown target field", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"unexpected":true`},
		{"missing write scope", `,"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, ``},
		{"null write scope", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":null`},
		{"empty write scope", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{}`},
		{"scope without kind", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{"paths":[]}`},
		{"unknown scope field", `"proposedWriteScope":{"kind":"REPOSITORY","paths":[]}`, `"proposedWriteScope":{"kind":"REPOSITORY","paths":[],"unexpected":true}`},
		{"null primary alias", `"primaryTargetAlias":"target-1"`, `"primaryTargetAlias":null`},
		{"number primary alias", `"primaryTargetAlias":"target-1"`, `"primaryTargetAlias":1`},
		{"missing primary alias", `"primaryTargetAlias":"target-1",`, ``},
		{"null check alias", `"targetAlias":"target-1","workingDirectory"`, `"targetAlias":null,"workingDirectory"`},
		{"object check aliases", `"targetAliases":["target-1"]`, `"targetAliases":{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, disposition := range []string{"APPROVAL_READY", "FURTHER_REVIEW_REQUIRED"} {
				raw := strings.Replace(closedAliasModelResult, tc.before, tc.after, 1)
				if raw == closedAliasModelResult {
					t.Fatal("mutation did not apply")
				}
				raw = strings.Replace(raw, "APPROVAL_READY", disposition, 1)
				if result, err := ParseModelResult(raw, frozen, "member-app"); err == nil {
					t.Errorf("%s accepted malformed wire result: %+v", disposition, result)
				}
			}
		})
	}
}

func TestModelResultUsesClosedAliasesAndCoreInjectsFrozenIdentity(t *testing.T) {
	for _, forbidden := range []string{`"repositoryId"`, `"memberId"`, `"memberVersion"`, `"originCommit"`, `"originRef"`, `"developmentCycleId"`, `"repositoryIds"`, `"primaryMemberId"`} {
		if strings.Contains(ResultJSONSchema, forbidden) {
			t.Fatalf("model schema exposes authority field %s", forbidden)
		}
	}
	frozen := []workset.Target{{RepositoryID: "repo-app", MemberID: "member-app", MemberVersion: 7, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle-1", Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	raw := closedAliasModelResult
	result, err := ParseModelResult(raw, frozen, "member-app")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.RepositoryTargets, frozen) || result.PrimaryMemberID != "member-app" || result.VerificationSpec.Checks[0].RepositoryID != "repo-app" || !reflect.DeepEqual(result.VerificationSpec.Checks[0].RepositoryIDs, []string{"repo-app"}) {
		t.Fatalf("injected result=%+v", result)
	}
	for _, injected := range []string{`"repositoryId":"repo-app"`, `"memberId":"member-app"`, `"originCommit":"` + strings.Repeat("a", 40) + `"`} {
		if _, err = ParseModelResult(strings.Replace(raw, `"summary":"Implement the bounded change."`, `"summary":"Implement the bounded change.",`+injected, 1), frozen, "member-app"); err == nil {
			t.Fatalf("model authority injection accepted: %s", injected)
		}
	}
	if _, err = ParseModelResult(strings.ReplaceAll(raw, "target-1", "target-2"), frozen, "member-app"); err == nil {
		t.Fatal("unknown target alias accepted")
	}
}

func TestModelResultWriteProposalDoesNotInheritOrMutateFrozenScope(t *testing.T) {
	frozen := []workset.Target{{RepositoryID: "repo-app", MemberID: "member-app", MemberVersion: 7, OriginCommit: strings.Repeat("a", 40), OriginRef: "refs/heads/main", DevelopmentCycleID: "cycle-1", Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"original.txt"}}}}
	before := workset.CloneTargets(frozen)
	for _, scope := range []workset.WriteScope{{Kind: "NONE"}, {Kind: "PATHS", Paths: []string{"new.txt"}}} {
		encoded, err := json.Marshal(map[string]any{"kind": scope.Kind, "paths": append([]string{}, scope.Paths...)})
		if err != nil {
			t.Fatal(err)
		}
		raw := strings.Replace(closedAliasModelResult, `{"kind":"REPOSITORY","paths":[]}`, string(encoded), 1)
		result, err := ParseModelResult(raw, frozen, "member-app")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(result.RepositoryTargets[0].Write, scope) || !reflect.DeepEqual(frozen, before) {
			t.Fatalf("proposal inherited or mutated frozen scope: result=%+v frozen=%+v", result.RepositoryTargets, frozen)
		}
	}
	findings := `{"version":2,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryTargetAlias":"","summary":"More evidence required","workSpec":null,"verificationSpec":null}`
	if _, err := ParseModelResult(findings, frozen, "member-app"); err != nil {
		t.Fatalf("valid findings rejected: %v", err)
	}
}

func TestReviewSchemaAllowsFindingsWithoutProposal(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ResultJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"workSpec", "verificationSpec"} {
		choices := properties[key].(map[string]any)["anyOf"].([]any)
		if choices[1].(map[string]any)["type"] != "null" {
			t.Fatal("findings-only Review requires proposal")
		}
	}
	if _, err := ParseImplementationResult(`{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryMemberId":"","summary":"More investigation needed","workSpec":null,"verificationSpec":null}`); err == nil {
		t.Fatal("findings-only Review authorized implementation")
	}
}
