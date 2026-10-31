package upperworker

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/review"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type contracts map[string]*jsonschema.Resolved

var requiredTools = []string{"review_access_get", "review_worker_claim", "review_worker_get", "file_list", "file_read", "review_result_submit"}
var targetFields = []string{"aiSessionId", "featureAreaId", "taskId", "reviewId", "runId", "runtimeEpoch", "grantGeneration"}

// Preflight resolves advertised schemas without network loaders. We support the
// existing closed object contract, not merely familiar tool/version names. The
// representative report is ONLY a local schema probe, never submitted or given
// to a callback as engineering evidence.
func preflight(ctx context.Context, mcp MCP, request Request) (contracts, error) {
	schemas := map[string]map[string]any{}
	needed := map[string]bool{}
	for _, name := range requiredTools {
		needed[name] = true
	}
	cursor := ""
	cursors := map[string]bool{}
	names := map[string]bool{}
	for page := 0; ; page++ {
		if page >= 32 {
			return nil, fmt.Errorf("%w: tool catalog page bound", ErrContract)
		}
		listed, err := mcp.ListTools(ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("%w: tools/list: %v", ErrContract, err)
		}
		if listed == nil || len(listed.Tools) > 2048 {
			return nil, fmt.Errorf("%w: invalid catalog", ErrContract)
		}
		for _, tool := range listed.Tools {
			if tool == nil || tool.Name == "" || names[tool.Name] {
				return nil, fmt.Errorf("%w: duplicate/empty tool", ErrContract)
			}
			names[tool.Name] = true
			if !needed[tool.Name] {
				continue
			}
			raw, err := json.Marshal(tool.InputSchema)
			if err != nil || len(raw) > 1<<20 {
				return nil, fmt.Errorf("%w: schema bound", ErrContract)
			}
			var schema map[string]any
			if json.Unmarshal(raw, &schema) != nil || schema == nil {
				return nil, fmt.Errorf("%w: invalid schema %s", ErrContract, tool.Name)
			}
			schemas[tool.Name] = schema
		}
		cursor = listed.NextCursor
		if cursor == "" {
			break
		}
		if cursors[cursor] {
			return nil, fmt.Errorf("%w: repeated tool cursor", ErrContract)
		}
		cursors[cursor] = true
	}
	out := contracts{}
	for _, name := range requiredTools {
		schema := schemas[name]
		if err := schemaShape(name, schema); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrContract, name, err)
		}
		raw, _ := json.Marshal(schema)
		var typed jsonschema.Schema
		if err := json.Unmarshal(raw, &typed); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrContract, name, err)
		}
		resolved, err := typed.Resolve(nil)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrContract, name, err)
		}
		out[name] = resolved
	}
	worker := request.Claim.WorkerClaimIdentity
	probe := review.SubmitInput{UpperTarget: request.Claim.UpperTarget, WorkerClaim: &worker, RequestID: request.ReportRequestID, Report: review.ReviewReport{SchemaVersion: 2, Summary: "local schema probe only", Disposition: review.DispositionBlocked, Coverage: []string{"schema only"}, Findings: []review.Finding{}, Limitations: []string{"not an engineering report"}, RecommendedNextSteps: []string{}, EvidenceRefs: []review.EvidenceRef{}}}
	samples := map[string]any{
		"review_access_get":   map[string]any{"aiSessionId": request.Claim.AISessionID, "reviewId": request.Claim.ReviewID},
		"review_worker_claim": request.Claim, "review_worker_get": request.Claim, "review_result_submit": probe,
		"file_read": map[string]any{"reviewTarget": request.Claim.UpperTarget, "repositoryId": "schema-repository", "memberId": "schema-member", "memberVersion": 1, "path": "same.txt"},
		"file_list": map[string]any{"reviewTarget": request.Claim.UpperTarget, "repositoryId": "schema-repository", "memberId": "schema-member", "memberVersion": 1, "path": "."},
	}
	for _, name := range requiredTools {
		if err := out.validate(name, samples[name]); err != nil {
			return nil, err
		}
		normalized, _ := json.Marshal(samples[name])
		var fields map[string]any
		_ = json.Unmarshal(normalized, &fields)
		fields["unexpectedAuthority"] = true
		if out[name].Validate(fields) == nil {
			return nil, fmt.Errorf("%w: %s accepts unknown authority fields", ErrContract, name)
		}
		delete(fields, "unexpectedAuthority")
		if name == "file_read" || name == "file_list" {
			delete(fields, "reviewTarget")
			if out[name].Validate(fields) == nil {
				return nil, fmt.Errorf("%w: %s permits unbound reads", ErrContract, name)
			}
		}
	}
	return out, nil
}

func objectSchema(v any, required []string) (map[string]any, error) {
	s, ok := v.(map[string]any)
	if !ok || s["type"] != "object" || s["additionalProperties"] != false {
		return nil, fmt.Errorf("closed object schema required")
	}
	properties, ok := s["properties"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("object properties required")
	}
	fields := map[string]bool{}
	for _, v := range slice(s["required"]) {
		if name, ok := v.(string); ok {
			fields[name] = true
		}
	}
	for _, name := range required {
		if !fields[name] || properties[name] == nil {
			return nil, fmt.Errorf("required property %s missing", name)
		}
	}
	return properties, nil
}
func slice(v any) []any { values, _ := v.([]any); return values }
func schemaShape(name string, s map[string]any) error {
	required := []string{"aiSessionId", "reviewId"}
	switch name {
	case "review_worker_claim", "review_worker_get":
		required = append(append([]string{}, targetFields...), "requestId", "workerId")
	case "review_result_submit":
		required = append(append([]string{}, targetFields...), "requestId", "report")
	case "file_read", "file_list":
		required = []string{"path"}
	}
	p, err := objectSchema(s, required)
	if err != nil {
		return err
	}
	switch name {
	case "review_result_submit":
		if _, err = objectSchema(p["workerClaim"], []string{"requestId", "workerId"}); err != nil {
			return fmt.Errorf("workerClaim: %v", err)
		}
		if _, err = objectSchema(p["report"], []string{"schemaVersion", "summary", "disposition", "coverage", "findings", "limitations", "recommendedNextSteps", "evidenceRefs"}); err != nil {
			return err
		}
	case "file_read", "file_list":
		if _, err = objectSchema(p["reviewTarget"], targetFields); err != nil {
			return err
		}
		for _, name := range []string{"repositoryId", "memberId", "memberVersion"} {
			if p[name] == nil {
				return fmt.Errorf("missing member identity %s", name)
			}
		}
	}
	return nil
}
func (c contracts) validate(name string, args any) error {
	raw, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var input any
	if err = json.Unmarshal(raw, &input); err != nil {
		return err
	}
	schema := c[name]
	if schema == nil {
		return fmt.Errorf("%w: missing %s", ErrContract, name)
	}
	if err = schema.Validate(input); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrContract, name, err)
	}
	return nil
}
func (c contracts) call(ctx context.Context, mcp MCP, name string, args, out any) error {
	if err := c.validate(name, args); err != nil {
		return err
	}
	response, err := mcp.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err = decode(response, out); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
