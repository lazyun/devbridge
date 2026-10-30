package agentruntime

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/google/jsonschema-go/jsonschema"
)

//go:embed protocol.json
var protocolJSON []byte
var contract struct {
	Methods map[string]struct {
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
	} `json:"methods"`
	Notifications  []string `json:"notifications"`
	ServerRequests []string `json:"serverRequests"`
	ItemTypes      []string `json:"itemTypes"`
}
var contractOnce sync.Once
var contractErr error
var validators map[string]*jsonschema.Resolved

func initContract() error {
	contractOnce.Do(func() {
		contractErr = json.Unmarshal(protocolJSON, &contract)
		if contractErr != nil {
			return
		}
		validators = map[string]*jsonschema.Resolved{}
		for method, c := range contract.Methods {
			for kind, raw := range map[string]json.RawMessage{"request": c.Request, "response": c.Response} {
				var schema jsonschema.Schema
				if contractErr = json.Unmarshal(raw, &schema); contractErr != nil {
					return
				}
				var resolved *jsonschema.Resolved
				resolved, contractErr = schema.Resolve(nil)
				if contractErr != nil {
					return
				}
				validators[method+":"+kind] = resolved
			}
		}
	})
	return contractErr
}
func Supports(method string) bool {
	return initContract() == nil && validators[method+":request"] != nil
}
func Validate(method, kind string, raw json.RawMessage) error {
	if err := initContract(); err != nil {
		return err
	}
	schema := validators[method+":"+kind]
	if schema == nil {
		return ErrUnsupported
	}
	var v any
	if len(raw) > MaxPayload || json.Unmarshal(raw, &v) != nil {
		return ErrInvalid
	}
	if err := schema.Validate(v); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalid, err)
	}
	return nil
}
func WaitingState(method string) string {
	switch method {
	case "item/commandExecution/requestApproval":
		return "WAITING_FOR_COMMAND_APPROVAL"
	case "item/fileChange/requestApproval":
		return "WAITING_FOR_FILE_APPROVAL"
	case "item/permissions/requestApproval":
		return "WAITING_FOR_PERMISSION"
	case "item/tool/requestUserInput":
		return "WAITING_FOR_USER_INPUT"
	case "mcpServer/elicitation/request":
		return "WAITING_FOR_MCP_ELICITATION"
	default:
		return "UNSUPPORTED_AGENT_REQUEST"
	}
}
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func available(method string, payload json.RawMessage, readOnly bool) []json.RawMessage {
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	var result []json.RawMessage
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		decisions := []any{"accept", "acceptForSession", "decline", "cancel"}
		if values, ok := p["availableDecisions"].([]any); ok {
			decisions = values
		}
		if method == "item/commandExecution/requestApproval" && p["availableDecisions"] == nil {
			if amendment, ok := p["proposedExecpolicyAmendment"].([]any); ok && len(amendment) > 0 {
				decisions = append(decisions, map[string]any{"acceptWithExecpolicyAmendment": map[string]any{"execpolicy_amendment": amendment}})
			}
			if amendments, ok := p["proposedNetworkPolicyAmendments"].([]any); ok {
				for _, a := range amendments {
					decisions = append(decisions, map[string]any{"applyNetworkPolicyAmendment": map[string]any{"network_policy_amendment": a}})
				}
			}
		}
		for _, decision := range decisions {
			if readOnly && decision != "decline" && decision != "cancel" {
				continue
			}
			v := raw(map[string]any{"decision": decision})
			if Validate(method, "response", v) == nil {
				result = append(result, v)
			}
		}
	case "item/permissions/requestApproval":
		result = append(result, raw(map[string]any{"permissions": map[string]any{}, "scope": "turn"}))
		if !readOnly && knownPermissionShape(p["permissions"]) {
			for _, scope := range []string{"turn", "session"} {
				result = append(result, raw(map[string]any{"permissions": p["permissions"], "scope": scope}))
			}
		}
	case "mcpServer/elicitation/request":
		for _, action := range []string{"accept", "decline", "cancel"} {
			result = append(result, raw(map[string]any{"action": action}))
		}
	}
	return result
}
func validateResponse(request Request, response json.RawMessage, readOnly bool) error {
	if err := Validate(request.Method, "response", response); err != nil {
		return err
	}
	var p, v map[string]any
	_ = json.Unmarshal(request.Payload, &p)
	_ = json.Unmarshal(response, &v)
	switch request.Method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		for _, r := range request.AvailableResponses {
			var a any
			_ = json.Unmarshal(r, &a)
			if reflect.DeepEqual(a, v) {
				return nil
			}
		}
		return ErrInvalid
	case "item/permissions/requestApproval":
		if !keys(v, "permissions", "scope", "strictAutoReview") {
			return ErrInvalid
		}
		permissions, ok := v["permissions"].(map[string]any)
		if !ok {
			return ErrInvalid
		}
		if readOnly && len(permissions) > 0 {
			return ErrInvalid
		}
		if !knownPermissionShape(permissions) {
			return ErrInvalid
		}
		if !subset(permissions, p["permissions"]) {
			return fmt.Errorf("%w: permission grant exceeds request", ErrInvalid)
		}
		// Dropping a deny entry or a traversal limit can enlarge a filesystem grant.
		if !preservesFilesystemRestrictions(permissions, p["permissions"]) {
			return ErrInvalid
		}
	case "item/tool/requestUserInput":
		if !keys(v, "answers") {
			return ErrInvalid
		}
		answers := v["answers"].(map[string]any)
		questions, _ := p["questions"].([]any)
		if len(answers) != len(questions) {
			return ErrInvalid
		}
		for _, q := range questions {
			question := q.(map[string]any)
			a, ok := answers[question["id"].(string)].(map[string]any)
			if !ok || !keys(a, "answers") {
				return ErrInvalid
			}
			values := a["answers"].([]any)
			if len(values) == 0 {
				return ErrInvalid
			}
			options, _ := question["options"].([]any)
			if question["isOther"] == true || len(options) == 0 {
				continue
			}
			for _, value := range values {
				found := false
				for _, o := range options {
					if o.(map[string]any)["label"] == value {
						found = true
					}
				}
				if !found {
					return ErrInvalid
				}
			}
		}
	case "mcpServer/elicitation/request":
		if !keys(v, "action", "content", "_meta") {
			return ErrInvalid
		}
		if v["action"] == "accept" && p["mode"] == "form" {
			var schema jsonschema.Schema
			if json.Unmarshal(raw(p["requestedSchema"]), &schema) != nil {
				return ErrInvalid
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				return ErrInvalid
			}
			if err = resolved.Validate(v["content"]); err != nil {
				return fmt.Errorf("%w: %s", ErrInvalid, err)
			}
		} else if v["content"] != nil {
			return ErrInvalid
		}
	}
	return nil
}
func keys(v map[string]any, allowed ...string) bool {
	for k := range v {
		if !slices.Contains(allowed, k) {
			return false
		}
	}
	return true
}

// Incoming unknown fields remain readable, but may never be echoed into a
// permission grant. The generated schemas intentionally allow extra fields.
func knownPermissionShape(value any) bool {
	p, ok := value.(map[string]any)
	if !ok || !keys(p, "network", "fileSystem") {
		return false
	}
	if n, ok := p["network"].(map[string]any); ok && !keys(n, "enabled") {
		return false
	}
	f, _ := p["fileSystem"].(map[string]any)
	if !keys(f, "entries", "globScanMaxDepth", "read", "write") {
		return false
	}
	entries, _ := f["entries"].([]any)
	for _, entry := range entries {
		e, _ := entry.(map[string]any)
		if !keys(e, "access", "path") {
			return false
		}
		path, _ := e["path"].(map[string]any)
		switch path["type"] {
		case "path":
			if !keys(path, "type", "path") {
				return false
			}
		case "glob_pattern":
			if !keys(path, "type", "pattern") {
				return false
			}
		case "special":
			if !keys(path, "type", "value") {
				return false
			}
			special, _ := path["value"].(map[string]any)
			if !keys(special, "kind", "path", "subpath") {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func subset(granted, requested any) bool {
	if granted == nil {
		return true
	}
	switch g := granted.(type) {
	case map[string]any:
		r, ok := requested.(map[string]any)
		if !ok {
			return len(g) == 0
		}
		for k, v := range g {
			if !subset(v, r[k]) {
				return false
			}
		}
		return true
	case []any:
		r, ok := requested.([]any)
		if !ok {
			return len(g) == 0
		}
		for _, v := range g {
			if !slices.ContainsFunc(r, func(x any) bool { return reflect.DeepEqual(x, v) }) {
				return false
			}
		}
		return true
	case bool:
		return !g || requested == true
	default:
		return reflect.DeepEqual(granted, requested)
	}
}
func preservesFilesystemRestrictions(g map[string]any, requested any) bool {
	r, _ := requested.(map[string]any)
	rf, _ := r["fileSystem"].(map[string]any)
	gf, _ := g["fileSystem"].(map[string]any)
	if len(gf) == 0 {
		return true
	}
	if d := rf["globScanMaxDepth"]; d != nil && !reflect.DeepEqual(d, gf["globScanMaxDepth"]) {
		return false
	}
	entries, _ := rf["entries"].([]any)
	granted, _ := gf["entries"].([]any)
	for _, e := range entries {
		m, _ := e.(map[string]any)
		if m["access"] == "deny" && !slices.ContainsFunc(granted, func(v any) bool { return reflect.DeepEqual(v, e) }) {
			return false
		}
	}
	return true
}

func requestKey(value json.RawMessage) string {
	var v any
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	if decoder.Decode(&v) != nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return "s:" + x
	case json.Number:
		n, err := x.Int64()
		if err == nil {
			return fmt.Sprintf("n:%d", n)
		}
	}
	return ""
}
