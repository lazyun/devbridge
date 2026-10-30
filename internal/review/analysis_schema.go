package review

import (
	"encoding/json"
	"fmt"

	"github.com/lazyun/devbridge/internal/workset"
)

// AnalysisResultSchema describes the exact existing Review, not the current
// project configuration. Scalar Reviews have no member aliases; giving them a
// synthetic target would invent authority absent from their frozen contract.
func AnalysisResultSchema(frozen []workset.Target, primaryMemberID string) json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ResultJSONSchema), &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	targets := properties["repositoryTargets"].(map[string]any)
	primary := properties["primaryTargetAlias"].(map[string]any)
	work := properties["workSpec"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["properties"].(map[string]any)
	verification := properties["verificationSpec"].(map[string]any)["anyOf"].([]any)[0].(map[string]any)["properties"].(map[string]any)
	checks := verification["checks"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	version := 1
	aliases := make([]string, 0, len(frozen))
	optionalAlias := []string{""}
	description := "Frozen scalar Review (version 1): repository identity comes from review.RepositoryID and review.BaseCommit. There are no target aliases. Submit repositoryTargets=[], primaryTargetAlias=\"\", and each verification check with targetAlias=\"\", targetAliases=[], workingDirectory=\"\". Commands execute at the existing repository root. Use PATHS or REPOSITORY executionScope; never invent member identity or target-1."
	if len(frozen) == 0 {
		targets["maxItems"] = 0
		checks["targetAliases"].(map[string]any)["maxItems"] = 0
		checks["workingDirectory"].(map[string]any)["const"] = ""
		work["executionScope"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"] = []string{"PATHS", "REPOSITORY"}
	} else {
		version = 2
		description = "Frozen workset Review (version 2). Only the listed aliases may be submitted. Write scope is a proposal, not a grant; Core injects frozen identity."
		for i, target := range frozen {
			alias := TargetAlias(i)
			aliases = append(aliases, alias)
			optionalAlias = append(optionalAlias, alias)
			description += fmt.Sprintf(" %s: repositoryId=%s, memberId=%s, memberVersion=%d, originCommit=%s.", alias, target.RepositoryID, target.MemberID, target.MemberVersion, target.OriginCommit)
			if target.MemberID == primaryMemberID {
				primary["description"] = "Frozen primary target: " + alias + ". Select an included target for an implementation proposal; leave empty for findings without targets."
			}
		}
		targets["items"].(map[string]any)["properties"].(map[string]any)["targetAlias"].(map[string]any)["enum"] = aliases
		checks["targetAliases"].(map[string]any)["items"].(map[string]any)["enum"] = aliases
		work["executionScope"].(map[string]any)["properties"].(map[string]any)["kind"].(map[string]any)["enum"] = []string{"REPOSITORY_TARGETS"}
	}
	properties["version"].(map[string]any)["enum"] = []int{version}
	primary["enum"] = optionalAlias
	checks["targetAlias"].(map[string]any)["enum"] = optionalAlias
	schema["description"] = description
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return raw
}
