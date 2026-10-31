package fileworker

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/directfile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type contracts map[string]*jsonschema.Resolved

var requiredTools = []string{"file_worker_claim", "file_worker_get", "file_list", "file_read", "file_write", "file_implementation_result_submit"}
var targetFields = []string{"aiSessionId", "featureAreaId", "taskId", "taskDefinitionRevisionId", "decisionId", "workflowId", "protocolVersion", "runtimeEpoch", "grantGeneration"}

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
	t := request.Claim.WorkerTarget
	probe := directfile.ImplementationResultInput{WorkerClaim: &worker, AISessionID: t.AISessionID, TaskID: t.TaskID, TaskDefinitionRevisionID: t.TaskDefinitionRevisionID, WorkflowID: t.WorkflowID, RequestID: request.ResultRequestID, Summary: "local schema probe only", VerificationNotes: "not an engineering result", ProtocolVersion: 2, RuntimeEpoch: t.RuntimeEpoch, GrantGeneration: t.GrantGeneration}
	base := map[string]any{"aiSessionId": t.AISessionID, "featureAreaId": t.FeatureAreaID, "workflowId": t.WorkflowID, "repositoryId": "schema-repo", "memberId": "schema-member", "memberVersion": 1, "runtimeEpoch": t.RuntimeEpoch, "grantGeneration": t.GrantGeneration, "path": "same.txt"}
	samples := map[string]any{"file_worker_claim": request.Claim, "file_worker_get": request.Claim, "file_implementation_result_submit": probe, "file_read": base, "file_list": base}
	for _, name := range requiredTools {
		if name == "file_write" {
			continue
		}
		if err := out.validate(name, samples[name]); err != nil {
			return nil, err
		}
		if err := rejectUnknown(out, name, samples[name]); err != nil {
			return nil, err
		}
	}
	for _, action := range []string{ActionCreate, ActionReplace, ActionDelete, ActionMove} {
		v := copyValue(base)
		v["action"] = action
		v["protocolVersion"] = 2
		v["requestId"] = "local-schema-probe"
		v["workerClaim"] = worker
		if action != ActionCreate {
			v["expectedDigest"] = digest([]byte("schema only"))
		}
		if action == ActionCreate || action == ActionReplace {
			v["content"] = "schema only"
		}
		if action == ActionMove {
			v["destination"] = "moved.txt"
			v["destinationRepositoryId"] = "schema-repo"
			v["destinationMemberId"] = "schema-member"
		}
		if err := out.validate("file_write", v); err != nil {
			return nil, err
		}
		if err := rejectUnknown(out, "file_write", v); err != nil {
			return nil, err
		}
		for _, required := range []string{"requestId", "runtimeEpoch", "grantGeneration", "repositoryId", "memberId", "memberVersion"} {
			bad := copyValue(v)
			delete(bad, required)
			if out.validate("file_write", bad) == nil {
				return nil, fmt.Errorf("%w: v2 mutation permits absent %s", ErrContract, required)
			}
		}
		// Current REPLACE schema shares CREATE content shape; Core and this
		// consumer enforce its observed digest locally. Do not pretend the
		// advertised schema already makes that field required.
		if action == ActionDelete || action == ActionMove {
			bad := copyValue(v)
			delete(bad, "expectedDigest")
			if out.validate("file_write", bad) == nil {
				return nil, fmt.Errorf("%w: mutation lacks source-digest fence", ErrContract)
			}
		}
	}
	for _, name := range []string{"file_read", "file_list"} {
		bad := copyValue(base)
		delete(bad, "workflowId")
		if out.validate(name, bad) == nil {
			return nil, fmt.Errorf("%w: unbound read", ErrContract)
		}
	}
	return out, nil
}
func rejectUnknown(c contracts, name string, v any) error {
	raw, _ := json.Marshal(v)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	fields["unexpectedAuthority"] = true
	if c[name].Validate(fields) == nil {
		return fmt.Errorf("%w: unknown fields permitted by %s", ErrContract, name)
	}
	return nil
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
	required := []string{}
	switch name {
	case "file_worker_claim", "file_worker_get":
		required = append(append([]string{}, targetFields...), "requestId", "workerId")
	case "file_read", "file_list":
		required = []string{"path"}
	case "file_implementation_result_submit":
		required = []string{"aiSessionId", "taskId", "taskDefinitionRevisionId", "workflowId", "requestId", "summary", "verificationNotes"}
	}
	p, err := objectSchema(s, required)
	if err != nil {
		return err
	}
	if name == "file_read" || name == "file_list" {
		for _, key := range []string{"aiSessionId", "featureAreaId", "workflowId", "repositoryId", "memberId", "memberVersion", "runtimeEpoch", "grantGeneration"} {
			if p[key] == nil {
				return fmt.Errorf("missing %s", key)
			}
		}
	}
	if name == "file_implementation_result_submit" {
		_, err = objectSchema(p["workerClaim"], []string{"requestId", "workerId"})
		return err
	}
	if name == "file_write" {
		branches := slice(s["oneOf"])
		if len(branches) != 4 {
			return fmt.Errorf("four closed action contracts required")
		}
		seen := map[string]bool{}
		for _, branch := range branches {
			fields, e := objectSchema(branch, []string{"action", "aiSessionId", "featureAreaId", "workflowId", "path"})
			if e != nil {
				return e
			}
			actionSchema, _ := fields["action"].(map[string]any)
			action, _ := actionSchema["const"].(string)
			if (action != ActionCreate && action != ActionReplace && action != ActionDelete && action != ActionMove) || seen[action] {
				return fmt.Errorf("exact four mutation actions required")
			}
			seen[action] = true
			if _, e = objectSchema(fields["workerClaim"], []string{"requestId", "workerId"}); e != nil {
				return e
			}
			for _, key := range []string{"protocolVersion", "requestId", "runtimeEpoch", "grantGeneration", "repositoryId", "memberId", "memberVersion", "expectedDigest"} {
				if fields[key] == nil {
					return fmt.Errorf("missing mutation %s", key)
				}
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

func toolCall(name string, args any) *sdkmcp.CallToolParams {
	return &sdkmcp.CallToolParams{Name: name, Arguments: args}
}
