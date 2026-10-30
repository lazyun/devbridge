package review

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lazyun/devbridge/internal/structuredschema"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

const TargetAliasPrefix = "target-"

// TargetAlias is the only repository identity a Coding Review model may emit.
// It is positional within the exact frozen Review target set and carries no
// authority outside materialization of that Review.
func TargetAlias(index int) string { return TargetAliasPrefix + fmt.Sprint(index+1) }

// ParseModelResult converts the closed model wire DTO into the canonical Core
// result. Repository/member/origin/read identity comes from the frozen Review;
// write scope is an explicit proposal, never inherited from decoder defaults.
// Model-authored authority keys are rejected recursively.
func ParseModelResult(raw string, frozen []workset.Target, primaryMemberID string) (ImplementationResult, error) {
	var value map[string]any
	d := json.NewDecoder(strings.NewReader(raw))
	if err := d.Decode(&value); err != nil || d.Decode(new(any)) != io.EOF {
		return ImplementationResult{}, errors.New("Review result must be one JSON object")
	}
	forbidden := map[string]bool{"repositoryId": true, "memberId": true, "memberVersion": true, "originCommit": true, "originRef": true, "developmentCycleId": true, "repositoryIds": true, "primaryMemberId": true}
	var rejectAuthority func(any) error
	rejectAuthority = func(v any) error {
		switch typed := v.(type) {
		case map[string]any:
			for key, child := range typed {
				if forbidden[key] {
					return fmt.Errorf("model result contains forbidden target authority %s", key)
				}
				if err := rejectAuthority(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := rejectAuthority(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := rejectAuthority(value); err != nil {
		return ImplementationResult{}, err
	}
	// Pre-workset scalar archives used the canonical v1 result directly.
	// Current model DTOs always carry primaryTargetAlias, including when empty.
	if _, modelWire := value["primaryTargetAlias"]; len(frozen) == 0 && !modelWire {
		return ParseResult(raw)
	}
	if err := validateModelResultWire(raw, value); err != nil {
		return ImplementationResult{}, err
	}
	aliases := make(map[string]workset.Target, len(frozen))
	primaryAlias := ""
	for i, target := range frozen {
		alias := TargetAlias(i)
		aliases[alias] = target
		if target.MemberID == primaryMemberID {
			primaryAlias = alias
		}
	}
	rawTargets, ok := value["repositoryTargets"].([]any)
	if !ok {
		return ImplementationResult{}, errors.New("repositoryTargets must be an array of closed target aliases")
	}
	canonical := make([]workset.Target, 0, len(rawTargets))
	seen := map[string]bool{}
	for _, item := range rawTargets {
		object, ok := item.(map[string]any)
		alias, aliasOK := object["targetAlias"].(string)
		if !ok || !aliasOK || len(object) != 2 || aliases[alias].RepositoryID == "" || seen[alias] {
			return ImplementationResult{}, errors.New("repositoryTargets contains an unknown or duplicate target alias")
		}
		seen[alias] = true
		target := aliases[alias]
		proposed, ok := object["proposedWriteScope"].(map[string]any)
		if !ok {
			return ImplementationResult{}, errors.New("proposedWriteScope must be an explicit object")
		}
		rawScope, err := json.Marshal(proposed)
		if err != nil {
			return ImplementationResult{}, err
		}
		// Decode into a fresh value: omitted fields must not inherit frozen
		// write authority, and unknown fields must not disappear in conversion.
		var scope workset.WriteScope
		scopeDecoder := json.NewDecoder(strings.NewReader(string(rawScope)))
		scopeDecoder.DisallowUnknownFields()
		if err = scopeDecoder.Decode(&scope); err != nil {
			return ImplementationResult{}, errors.New("invalid proposedWriteScope")
		}
		target.Write = scope
		canonical = append(canonical, target)
	}
	selectedPrimary, ok := value["primaryTargetAlias"].(string)
	if !ok {
		return ImplementationResult{}, errors.New("primaryTargetAlias must be a string")
	}
	if selectedPrimary != "" && (aliases[selectedPrimary].MemberID == "" || !seen[selectedPrimary]) {
		return ImplementationResult{}, errors.New("primaryTargetAlias must select an included frozen target")
	}
	if selectedPrimary == "" && len(canonical) > 0 {
		selectedPrimary = primaryAlias
	}
	encodedTargets := make([]any, len(canonical))
	for i := range canonical {
		encodedTargets[i] = canonical[i]
	}
	value["repositoryTargets"] = encodedTargets
	delete(value, "primaryTargetAlias")
	if target, ok := aliases[selectedPrimary]; ok {
		value["primaryMemberId"] = target.MemberID
	} else {
		value["primaryMemberId"] = ""
	}
	if spec, ok := value["verificationSpec"].(map[string]any); ok {
		if checks, ok := spec["checks"].([]any); ok {
			for _, rawCheck := range checks {
				check, ok := rawCheck.(map[string]any)
				if !ok {
					continue
				}
				alias, ok := check["targetAlias"].(string)
				if !ok {
					return ImplementationResult{}, errors.New("verification targetAlias must be a string")
				}
				if alias != "" {
					target, exists := aliases[alias]
					if !exists || !seen[alias] {
						return ImplementationResult{}, errors.New("verification check references an unauthorized target alias")
					}
					check["repositoryId"] = target.RepositoryID
				}
				delete(check, "targetAlias")
				list, ok := check["targetAliases"].([]any)
				if !ok {
					return ImplementationResult{}, errors.New("verification targetAliases must be an array")
				}
				ids := make([]string, 0, len(list))
				for _, item := range list {
					alias, ok := item.(string)
					target, exists := aliases[alias]
					if !ok || !exists || !seen[alias] {
						return ImplementationResult{}, errors.New("verification check references an unauthorized target alias")
					}
					ids = append(ids, target.RepositoryID)
				}
				check["repositoryIds"] = ids
				delete(check, "targetAliases")
			}
		}
	}
	canonicalRaw, err := json.Marshal(value)
	if err != nil {
		return ImplementationResult{}, err
	}
	return ParseResult(string(canonicalRaw))
}

// ImplementationResult is authored by the reviewing agent and shown verbatim
// before approval. Bridge validates structure, never invents engineering scope.
type Disposition string

const (
	DispositionApprovalReady         Disposition = "APPROVAL_READY"
	DispositionFurtherReviewRequired Disposition = "FURTHER_REVIEW_REQUIRED"
)

type ImplementationResult struct {
	ReviewDisposition Disposition           `json:"reviewDisposition"`
	Version           int                   `json:"version"`
	RepositoryTargets []workset.Target      `json:"repositoryTargets,omitempty"`
	PrimaryMemberID   string                `json:"primaryMemberId,omitempty"`
	Summary           string                `json:"summary"`
	WorkSpec          task.WorkSpec         `json:"workSpec"`
	VerificationSpec  task.VerificationSpec `json:"verificationSpec"`
}

func ParseResult(raw string) (ImplementationResult, error) {
	var result ImplementationResult
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&result); err != nil {
		return result, err
	}
	if d.Decode(new(any)) != io.EOF {
		return result, errors.New("Review result must be one JSON object")
	}
	if (result.Version != 1 && result.Version != 2) || strings.TrimSpace(result.Summary) == "" || len(result.Summary) > 16384 {
		return result, errors.New("Review implementation result requires version 1 or 2 and a bounded summary")
	}
	if result.Version == 1 {
		definition := task.Definition{Version: 1, RepositoryTargets: result.RepositoryTargets, PrimaryMemberID: result.PrimaryMemberID, WorkSpec: result.WorkSpec, VerificationSpec: result.VerificationSpec}
		if err := task.ValidateDefinitionTargets(definition); err != nil {
			return result, err
		}
	}
	if result.Version == 2 && (len(result.RepositoryTargets) > 0 || result.ReviewDisposition == DispositionApprovalReady) {
		if err := workset.ValidateTargets(result.RepositoryTargets); err != nil {
			return result, err
		}
	}
	if result.ReviewDisposition == DispositionFurtherReviewRequired || result.ReviewDisposition == DispositionBlocked {
		return result, nil
	}
	if result.ReviewDisposition != DispositionApprovalReady {
		return result, errors.New("Review result requires an explicit reviewDisposition")
	}
	if result.Version == 1 {
		if err := task.ValidateWorkSpec(result.WorkSpec); err != nil {
			return result, err
		}
	}
	if err := task.ValidateVerificationSpec(result.VerificationSpec); err != nil {
		return result, err
	}
	// Review is not a user waiver channel. Every proposed implementation verifies.
	if result.VerificationSpec.Mode != task.VerificationModeRun {
		return result, errors.New("Review proposal requires verification")
	}
	if result.Version == 2 {
		definition := task.Definition{Version: 2, RepositoryTargets: result.RepositoryTargets, PrimaryMemberID: result.PrimaryMemberID, WorkSpec: result.WorkSpec, VerificationSpec: result.VerificationSpec}
		if err := task.ValidateDefinitionTargets(definition); err != nil {
			return result, err
		}
		if err := task.ValidateExecutablePrimary(definition); err != nil {
			return result, err
		}
	}
	return result, nil
}

func ParseImplementationResult(raw string) (ImplementationResult, error) {
	result, err := ParseResult(raw)
	if err == nil && result.ReviewDisposition != DispositionApprovalReady {
		err = errors.New("Review result is not APPROVAL_READY")
	}
	return result, err
}

var ImplementationResultJSONSchema = string(structuredschema.Marshal(implementationResultSchema()))

func implementationResultSchema() structuredschema.Schema {
	stringArray := func() structuredschema.Schema { return structuredschema.Array(structuredschema.String()) }
	readScope := structuredschema.Object(map[string]any{
		"kind":  structuredschema.Enum("", "REPOSITORY", "PATHS"),
		"paths": stringArray(),
	})
	writeScope := structuredschema.Object(map[string]any{
		"kind":  structuredschema.Enum("NONE", "PATHS", "REPOSITORY"),
		"paths": stringArray(),
	})
	target := structuredschema.Object(map[string]any{
		"targetAlias":        structuredschema.String(),
		"proposedWriteScope": writeScope,
	})
	_ = readScope
	executionScope := structuredschema.Object(map[string]any{
		"kind":  structuredschema.Enum("PATHS", "REPOSITORY", "REPOSITORY_TARGETS"),
		"paths": stringArray(),
	})
	workSpec := structuredschema.Object(map[string]any{
		"implementationMode": structuredschema.Enum("CODING_AGENT", "DIRECT_FILE"),
		"goal":               structuredschema.String(),
		"constraints":        stringArray(),
		"codexInstruction":   structuredschema.String(),
		"executionScope":     executionScope,
	})
	check := structuredschema.Object(map[string]any{
		"targetAlias":      structuredschema.String(),
		"workingDirectory": structuredschema.String(),
		"targetAliases":    stringArray(),
		"kind":             structuredschema.Enum("TEST_COMMAND", "STATIC_ANALYSIS", "DIFF_CHECK", "GIT_ASSERTION", "CUSTOM"),
		"command":          structuredschema.String(),
		"instruction":      structuredschema.String(),
		"paths":            stringArray(),
		"packages":         stringArray(),
		"required":         structuredschema.Boolean(),
	})
	verificationSpec := structuredschema.Object(map[string]any{
		"mode":               structuredschema.Schema{"type": "string", "const": "RUN"},
		"scope":              structuredschema.String(),
		"checks":             structuredschema.Array(check),
		"acceptanceCriteria": stringArray(),
		"codexInstruction":   structuredschema.String(),
	})
	summary := structuredschema.String()
	summary["description"] = "Explain the implementation plan, file scope, risks and adjustments from the original user goal or constraints so the user can decide. Pending approval discussion belongs here, not in executable instruction fields. Bridge validates the structured protocol; it does not judge semantic equivalence or require verbatim inheritance."
	return structuredschema.Object(map[string]any{
		"reviewDisposition":  structuredschema.Enum("APPROVAL_READY", "FURTHER_REVIEW_REQUIRED"),
		"version":            structuredschema.Schema{"type": "integer", "enum": []any{1, 2}},
		"repositoryTargets":  structuredschema.Array(target),
		"primaryTargetAlias": structuredschema.String(),
		"summary":            summary,
		"workSpec":           workSpec,
		"verificationSpec":   verificationSpec,
	})
}

// ResultJSONSchema permits findings without a final implementation proposal.
// Approval still validates a complete ImplementationResult separately.
var ResultJSONSchema = findingsResultSchema()

func findingsResultSchema() string {
	var schema map[string]any
	if err := json.Unmarshal([]byte(ImplementationResultJSONSchema), &schema); err != nil {
		panic(err)
	}
	properties := schema["properties"].(map[string]any)
	for _, key := range []string{"workSpec", "verificationSpec"} {
		properties[key] = map[string]any{"anyOf": []any{properties[key], map[string]any{"type": "null"}}}
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
