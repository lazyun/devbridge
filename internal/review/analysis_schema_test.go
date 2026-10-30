package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestAnalysisSchemaMatchesFrozenReviewProtocol(t *testing.T) {
	frozen := []workset.Target{{RepositoryID: "repo-app", MemberID: "member-app", MemberVersion: 7, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "REPOSITORY"}}}
	var scalar map[string]any
	if err := json.Unmarshal([]byte(closedAliasModelResult), &scalar); err != nil {
		t.Fatal(err)
	}
	scalar["version"], scalar["repositoryTargets"], scalar["primaryTargetAlias"] = 1, []any{}, ""
	scalar["workSpec"].(map[string]any)["executionScope"] = map[string]any{"kind": "REPOSITORY", "paths": []any{}}
	check := scalar["verificationSpec"].(map[string]any)["checks"].([]any)[0].(map[string]any)
	check["targetAlias"], check["targetAliases"], check["workingDirectory"] = "", []any{}, ""
	scalarRaw, _ := json.Marshal(scalar)
	global := ResultJSONSchema
	for _, tc := range []struct {
		name    string
		targets []workset.Target
		raw     string
	}{
		{"scalar", nil, string(scalarRaw)},
		{"workset", frozen, closedAliasModelResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rawSchema := AnalysisResultSchema(tc.targets, "member-app")
			var schema jsonschema.Schema
			if err := json.Unmarshal(rawSchema, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			_ = json.Unmarshal([]byte(tc.raw), &value)
			if err := resolved.Validate(value); err != nil {
				t.Fatalf("advertised contract rejected valid report: %v", err)
			}
			result, err := ParseModelResult(tc.raw, tc.targets, "member-app")
			if err != nil || result.ReviewDisposition != DispositionApprovalReady {
				t.Fatalf("advertised report cannot be submitted: %+v %v", result, err)
			}
			for _, mutation := range []string{
				strings.Replace(tc.raw, `"primaryTargetAlias":"`+value["primaryTargetAlias"].(string)+`"`, `"primaryTargetAlias":"target-999"`, 1),
				strings.Replace(tc.raw, `"targetAlias":"`+value["primaryTargetAlias"].(string)+`"`, `"targetAlias":"target-999"`, 1),
			} {
				var invalid any
				_ = json.Unmarshal([]byte(mutation), &invalid)
				if err := resolved.Validate(invalid); err == nil {
					t.Fatal("schema permits an unfrozen alias")
				}
			}
			value["version"] = 3 - result.Version
			if err := resolved.Validate(value); err == nil {
				t.Fatal("schema permits the wrong frozen protocol")
			}
			if len(tc.targets) > 0 && (!strings.Contains(string(rawSchema), "repositoryId=repo-app") || !strings.Contains(string(rawSchema), "Frozen primary target: target-1")) {
				t.Fatal("alias mapping or primary missing from source contract")
			}
		})
	}
	if global != ResultJSONSchema || frozen[0].MemberVersion != 7 {
		t.Fatal("source-specific schema mutated the shared protocol or frozen target")
	}
}
