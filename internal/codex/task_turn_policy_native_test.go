package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/runtimepermission"
)

// Real native resume and sandbox, with a loopback Responses fixture instead of
// a paid model. The fixture captures the actual developer context sent by Codex.
// A compacted old-role record is seeded only in this isolated test CODEX_HOME.
func TestTaskTurnPolicyNativeCompactedResume(t *testing.T) {
	exe := os.Getenv("DEVBRIDGE_PERMISSION_NATIVE")
	if exe == "" {
		t.Skip("set DEVBRIDGE_PERMISSION_NATIVE for native role transition check")
	}
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home, source, copyRoot := filepath.Join(root, "home"), filepath.Join(root, "source"), filepath.Join(root, "copy")
	for _, p := range []string{home, source, copyRoot, filepath.Join(source, "allowed"), filepath.Join(copyRoot, "dist")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CODEX_HOME", home)
	t.Setenv("OPENAI_API_KEY", "loopback-fixture-only")
	var mu sync.Mutex
	var captured []json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("loopback fixture: %s %s", r.Method, r.URL.Path)
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		captured = append(captured, body)
		index := len(captured)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		item := map[string]any{"id": fmt.Sprintf("msg_%d", index), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "fixture complete", "annotations": []any{}}}}
		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": fmt.Sprintf("resp_%d", index), "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.done", "output_index": 0, "item": item},
			{"type": "response.completed", "response": map[string]any{"id": fmt.Sprintf("resp_%d", index), "status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 2, "total_tokens": 12}}},
		}
		for _, e := range events {
			raw, _ := json.Marshal(e)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e["type"], raw)
		}
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1")
	// Turn model is frozen and kept identical across all phases. No credentials
	// from the user's normal CODEX_HOME are read by this fixture.
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(fmt.Sprintf("model = \"gpt-5.6-sol\"\nmodel_provider = \"fixture\"\n[model_providers.fixture]\nname = \"Loopback fixture\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\n[features]\nresponses_websockets = false\n", server.URL+"/v1")), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	var thread string
	var oldInstructions string
	for phase, purpose := range []string{"VERIFICATION", "IMPLEMENTATION", "VERIFICATION"} {
		scratch := filepath.Join(root, fmt.Sprintf("scratch-%d", phase))
		if err := os.Mkdir(scratch, 0700); err != nil {
			t.Fatal(err)
		}
		binding := permissionBinding()
		p := binding.TaskPermissions
		p.RunID = fmt.Sprintf("run-%d", phase)
		p.Purpose = purpose
		p.ScratchRoot = scratch
		p.Copies = []VerificationCopy{{SourceRoot: source, Root: copyRoot, SourceDigest: strings.Repeat("b", 64), StartSnapshot: strings.Repeat("c", 40), RootIdentity: "fixture-copy", OutputPaths: []string{"dist"}}}
		binding.RuntimeWorkspaceRoots = []string{source, copyRoot, scratch}
		binding.WritableRoots = []string{copyRoot, scratch}
		cwd, ceiling := copyRoot, AuthorizationReadOnly
		if purpose == "IMPLEMENTATION" {
			p.Copies = nil
			p.Writes = []runtimepermission.WriteGrant{{Root: source, Kind: "PATHS", Paths: []string{"allowed"}}}
			binding.RuntimeWorkspaceRoots = []string{source, scratch}
			binding.WritableRoots = []string{source, scratch}
			cwd = source
			ceiling = AuthorizationWorkspaceWrite
		}
		p.ContentHash = p.Hash()
		client, err := Start(ctx, Config{CodexPath: exe, TaskRuntimeBinding: &binding})
		if err != nil {
			t.Fatal(err)
		}
		var effective struct {
			Config map[string]json.RawMessage `json:"config"`
		}
		if err = client.rpc.Call(ctx, "config/read", map[string]any{"cwd": cwd, "includeLayers": false}, &effective); err != nil {
			client.Close()
			t.Fatal(err)
		}
		providers := effective.Config["model_providers"]
		if !strings.Contains(string(providers), server.URL+"/v1") {
			client.Close()
			t.Fatalf("loopback model routing not observed: %s", providers)
		}
		if thread == "" {
			var started Thread
			started, err = client.StartThread(ctx, ThreadOptions{CWD: cwd, RuntimeBinding: binding, ReasoningEffort: "low"})
			thread = started.ID
			oldInstructions = executorInstructions + p.Instructions()
		} else {
			_, err = client.ResumeThread(ctx, ThreadResumeOptions{ThreadID: thread, CWD: cwd, RuntimeBinding: binding, ReasoningEffort: "low"})
		}
		if err != nil {
			client.Close()
			t.Fatal(err)
		}
		result, err := client.StartTurn(ctx, TurnOptions{RunID: p.RunID, ThreadID: thread, Prompt: "Report fixture complete.", Model: "gpt-5.6-sol", ReasoningEffort: "low", AuthorizationCeiling: ceiling, RuntimeBinding: binding})
		if err != nil {
			client.Close()
			t.Fatalf("phase %d native turn: %v", phase, err)
		}
		if result.FinalMessage != "fixture complete" {
			client.Close()
			t.Fatalf("unexpected fixture result: %q", result.FinalMessage)
		}
		mu.Lock()
		latest := append(json.RawMessage(nil), captured[len(captured)-1]...)
		mu.Unlock()
		var request struct {
			Input []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"input"`
		}
		if err := json.Unmarshal(latest, &request); err != nil {
			client.Close()
			t.Fatal(err)
		}
		current := ""
		oldRoleObserved := false
		lastOldRole, lastCurrentRole := -1, -1
		for itemIndex, item := range request.Input {
			if item.Role == "developer" {
				for _, c := range item.Content {
					if phase == 1 && strings.Contains(c.Text, oldInstructions) {
						oldRoleObserved = true
						lastOldRole = itemIndex
					}
					if strings.Contains(c.Text, "Core operation sandbox policy") {
						current = c.Text
						if strings.Contains(c.Text, `"purpose":"`+purpose+`"`) {
							lastCurrentRole = itemIndex
						}
					}
				}
			}
		}
		if phase == 1 && !oldRoleObserved {
			client.Close()
			t.Fatal("native fixture did not retain the injected old developer role; role replacement was not exercised")
		}
		if phase == 1 && lastCurrentRole <= lastOldRole {
			client.Close()
			t.Fatal("current developer policy must follow stale compacted role in model input")
		}
		if !strings.Contains(current, `"purpose":"`+purpose+`"`) || !strings.Contains(current, p.ScratchRoot) || (phase > 0 && !strings.Contains(current, "supersedes earlier host operation roles")) {
			client.Close()
			t.Fatalf("phase %d current role missing from native model request: %s", phase, current)
		}
		if purpose == "IMPLEMENTATION" && !strings.Contains(current, `"writes"`) {
			client.Close()
			t.Fatal("native implementation context lost write grants")
		}
		var command struct {
			ExitCode int    `json:"exitCode"`
			Stdout   string `json:"stdout"`
		}
		payload := `import json,sys
from pathlib import Path
out={}
for name,p in [('allowed',Path(sys.argv[1])/'allowed'/'proof.txt'),('outside',Path(sys.argv[1])/'forbidden.txt')]:
 try:p.write_text('fixture');out[name]='ALLOWED'
 except PermissionError:out[name]='DENIED'
print(json.dumps(out))`
		err = client.rpc.Call(ctx, "command/exec", map[string]any{"command": []string{"/usr/bin/python3", "-c", payload, source}, "cwd": cwd, "permissionProfile": p.ProfileID(), "env": map[string]string{"TMPDIR": scratch}, "timeoutMs": 5000}, &command)
		if err != nil || command.ExitCode != 0 {
			client.Close()
			t.Fatalf("phase %d sandbox command: %v %+v", phase, err, command)
		}
		var observed map[string]string
		json.Unmarshal([]byte(command.Stdout), &observed)
		want := "DENIED"
		if purpose == "IMPLEMENTATION" {
			want = "ALLOWED"
		}
		if observed["allowed"] != want || observed["outside"] != "DENIED" {
			client.Close()
			t.Fatalf("phase %d wrong sandbox: %s", phase, command.Stdout)
		}
		if err = client.Close(); err != nil {
			t.Fatal(err)
		}
		{
			paths, err := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "*"+thread+".jsonl"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("persisted fixture rollout: %v %v", paths, err)
			}
			entry := map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "compacted", "payload": map[string]any{"message": "", "replacement_history": []any{map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": oldInstructions}}}, map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Previous verification fixture."}}}, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Previous verification completed."}}}}}}
			rollout, err := os.ReadFile(paths[0])
			if err != nil {
				t.Fatal(err)
			}
			mode, err := client.executorTurnMode(TurnOptions{Model: "gpt-5.6-sol", ReasoningEffort: "low"}, binding)
			if err != nil {
				t.Fatal(err)
			}
			policyPersisted := false
			for _, line := range strings.Split(strings.TrimSpace(string(rollout)), "\n") {
				var entry struct {
					Type    string `json:"type"`
					Payload struct {
						Role    string `json:"role"`
						Content []struct {
							Text string `json:"text"`
						} `json:"content"`
					} `json:"payload"`
				}
				if json.Unmarshal([]byte(line), &entry) != nil {
					continue
				}
				if entry.Type == "response_item" && entry.Payload.Role == "developer" {
					for _, content := range entry.Payload.Content {
						if content.Text == mode.Settings.DeveloperInstructions {
							policyPersisted = true
						}
					}
				}
			}
			if !policyPersisted {
				t.Fatal("current developer policy missing from persisted native history")
			}
			if phase == 0 {
				lines := strings.Split(strings.TrimSpace(string(rollout)), "\n")
				var last map[string]any
				if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
					t.Fatal(err)
				}
				if ordinal, ok := last["ordinal"].(float64); ok {
					entry["ordinal"] = int64(ordinal) + 1
				}
				raw, _ := json.Marshal(entry)
				file, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.Write(append(raw, '\n'))
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Logf("phase %d %s: exact thread retained, native developer context current, allowed write=%s, outside write=DENIED", phase, purpose, want)
	}
	mu.Lock()
	count := len(captured)
	mu.Unlock()
	if count != 3 {
		t.Fatalf("unexpected model fixture request count: %d", count)
	}
}
