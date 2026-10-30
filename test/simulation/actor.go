package simulation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type actor struct {
	s        *scenario
	role     string
	session  *sdkmcp.ClientSession
	schemaMu sync.Mutex
	schemas  map[string]any
	resolved map[string]*jsonschema.Resolved
}
type callOutcome struct {
	Value     map[string]any
	Denied    bool
	ErrorText string
	Err       error
}

// invoke never panics, so concurrent claim races can be gathered deterministically.
func (a *actor) invoke(role, name string, args any) (out callOutcome) {
	defer func() {
		if p := recover(); p != nil {
			out.Err = fmt.Errorf("simulation call %s failed: %v", name, p)
		}
	}()
	if err := a.validateInput(name, args); err != nil {
		a.s.record(map[string]any{"kind": "contract-error", "actor": role, "tool": name, "error": err.Error()})
		return callOutcome{Err: err}
	}
	start := time.Now()
	result, err := a.session.CallTool(a.s.ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	entry := map[string]any{"kind": "mcp", "actor": role, "tool": name, "arguments": args, "durationMillis": time.Since(start).Milliseconds()}
	out = callOutcome{Err: err}
	if err != nil {
		entry["transportError"] = err.Error()
	} else if result == nil {
		out.Err = fmt.Errorf("%s returned nil result", name)
	} else {
		entry["response"] = result
		out.Denied = result.IsError
		out.ErrorText = encoded(result.Content)
		if result.StructuredContent != nil {
			b, e := json.Marshal(result.StructuredContent)
			if e == nil {
				e = json.Unmarshal(b, &out.Value)
			}
			if e != nil {
				out.Err = e
			}
			if nested, ok := out.Value["result"].(map[string]any); ok {
				out.Value = nested
			}
		}
	}
	a.s.record(entry)
	return out
}
func (a *actor) call(name string, args any) map[string]any { return a.callAs(a.role, name, args) }
func (a *actor) callAs(role, name string, args any) map[string]any {
	out := a.invoke(role, name, args)
	a.s.must(out.Err)
	if out.Denied {
		panic(scenarioFailure{fmt.Sprintf("%s %s denied: %s", role, name, out.ErrorText)})
	}
	if out.Value == nil {
		panic(scenarioFailure{name + " returned no structured object"})
	}
	return out.Value
}
func (a *actor) deny(name string, args any, code string) {
	out := a.invoke(a.role, name, args)
	a.s.must(out.Err)
	a.s.check("domain refusal: "+name, out.Denied && (code == "" || strings.Contains(out.ErrorText, code)), map[string]any{"expectedCode": code, "error": out.ErrorText})
}
func (s *scenario) http(method, path string, input any) map[string]any {
	b, err := json.Marshal(input)
	s.must(err)
	req, err := http.NewRequestWithContext(s.ctx, method, s.core+path, bytes.NewReader(b))
	s.must(err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := loopbackClient().Do(req)
	s.must(err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	s.must(err)
	var out map[string]any
	s.must(json.Unmarshal(raw, &out))
	s.record(map[string]any{"kind": "http", "actor": "fixture_user", "method": method, "path": path, "request": input, "status": resp.StatusCode, "response": out})
	s.check("HTTP "+method+" "+path, resp.StatusCode >= 200 && resp.StatusCode < 300, resp.StatusCode)
	return out
}

// Validate against tools/list before dispatch. A schema rejection must never
// count as evidence that a semantic authorization fence was exercised.
func (a *actor) validateInput(name string, args any) error {
	a.schemaMu.Lock()
	defer a.schemaMu.Unlock()
	if a.schemas == nil {
		a.schemas = map[string]any{}
		a.resolved = map[string]*jsonschema.Resolved{}
		cursor := ""
		seen := map[string]bool{}
		for {
			listed, err := a.session.ListTools(a.s.ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
			if err != nil {
				return fmt.Errorf("CONTRACT_DRIFT tools/list: %w", err)
			}
			for _, t := range listed.Tools {
				a.schemas[t.Name] = t.InputSchema
			}
			cursor = listed.NextCursor
			if cursor == "" {
				break
			}
			if seen[cursor] {
				return fmt.Errorf("CONTRACT_DRIFT repeated tool cursor")
			}
			seen[cursor] = true
		}
	}
	resolved := a.resolved[name]
	if resolved == nil {
		raw, ok := a.schemas[name]
		if !ok {
			return fmt.Errorf("CONTRACT_DRIFT missing tool %s", name)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		var schema jsonschema.Schema
		if err = json.Unmarshal(b, &schema); err != nil {
			return err
		}
		resolved, err = schema.Resolve(nil)
		if err != nil {
			return fmt.Errorf("CONTRACT_DRIFT resolve %s: %w", name, err)
		}
		a.resolved[name] = resolved
	}
	b, err := json.Marshal(args)
	if err != nil {
		return err
	}
	var normalized any
	if err = json.Unmarshal(b, &normalized); err != nil {
		return err
	}
	if err = resolved.Validate(normalized); err != nil {
		return fmt.Errorf("CONTRACT_DRIFT input %s: %w", name, err)
	}
	return nil
}
