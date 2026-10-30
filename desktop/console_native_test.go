package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/desktop/internal/logbuffer"
	runtimecontrol "github.com/lazyun/devbridge/desktop/internal/runtime"
)

func nativeConsoleApp(t *testing.T, endpoint string) *App {
	t.Helper()
	supervisor, err := runtimecontrol.NewRuntimeSupervisor(runtimecontrol.Config{Core: runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "unused"}, Endpoint: endpoint}, MCP: runtimecontrol.ServiceConfig{Command: runtimecontrol.Command{Path: "unused"}, Endpoint: endpoint}}, &appTestFactory{}, &appTestProber{external: true, calls: map[runtimecontrol.Service]int{}}, logbuffer.New(10))
	if err != nil {
		t.Fatal(err)
	}
	if err = supervisor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { supervisor.Stop(context.Background()) })
	app := &App{supervisor: supervisor}
	app.settings.CoreEndpoint = "https://unrelated.invalid"
	return app
}
func TestNativeConsoleReadsAndExactActionsStayOnAttachedCore(t *testing.T) {
	calls := 0
	original := `{"action":"CONTINUE_TASK","target":{"taskId":"t","executionId":"e","taskActionGeneration":23},"requestId":"same-request","note":"保留原任务"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("project_id") != "p" {
			t.Error("project identity missing")
		}
		if r.Method == "POST" {
			body, _ := io.ReadAll(r.Body)
			if string(body) != original {
				t.Error("request identity changed")
			}
			w.WriteHeader(409)
			io.WriteString(w, `{"code":"CONFLICT","message":"stale target"}`)
		} else {
			if r.URL.Path != "/ui/api/tasks/t" {
				t.Error(r.URL.Path)
			}
			io.WriteString(w, `{"id":"t","title":"Task title"}`)
		}
	}))
	defer server.Close()
	app := nativeConsoleApp(t, server.URL)
	result, err := app.ConsoleRead("task", "p", "t", 0, "")
	if err != nil || !strings.Contains(result, "Task title") {
		t.Fatalf("read=%s %v", result, err)
	}
	for i := 0; i < 2; i++ {
		result, err = app.ConsoleTaskAction("p", "t", original)
		if err != nil || !strings.Contains(result, `"status":409`) {
			t.Fatalf("action=%s %v", result, err)
		}
	}
	if _, err = app.ConsoleTaskAction("p", "different", original); err == nil {
		t.Fatal("cross-task target accepted")
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}
func TestNativeConsoleCommandShapeAndReadPaths(t *testing.T) {
	for _, body := range []string{
		`{"action":"START_APPROVED","target":{"taskId":"t"},"requestId":"r"}`,
		`{"action":"CONTINUE_TASK","target":{"taskId":"other"},"requestId":"r"}`,
		`{"action":"CONTINUE_TASK","target":{"taskId":"t"}}`,
		`{"action":"COMPLETE_TASK","target":{"taskId":"t"},"requestId":"r"}`,
		`{"action":"CONTINUE_TASK","target":{"taskId":"t"},"requestId":"r","url":"https://elsewhere"}`,
	} {
		if validateConsoleTaskCommand("t", body) == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	path, err := consoleReadPath("task", "p&other=1", "a/b", 0, "")
	if err != nil || !strings.Contains(path, "a%2Fb") || !strings.Contains(path, "project_id=p%26other%3D1") {
		t.Fatalf("path %s %v", path, err)
	}
	runPath, runErr := consoleReadPath("runs", "p", "t", 23, "")
	if runErr != nil || !strings.Contains(runPath, "offset=23") || !strings.Contains(runPath, "limit=20") {
		t.Fatalf("run paging %s %v", runPath, runErr)
	}
	if _, err = consoleReadPath("arbitrary", "p", "t", 0, ""); err == nil {
		t.Fatal("arbitrary URL accepted")
	}
	if _, err = consoleReadPath("tasks", "p", "", -1, ""); err == nil {
		t.Fatal("negative page accepted")
	}
	if _, err = (&App{}).ConsoleRead("projects", "", "", 0, ""); err == nil {
		t.Fatal("unattached Core accepted")
	}
}
func TestNativeConsoleReadFailureIsNotAnEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		io.WriteString(w, `{"code":"UNAVAILABLE","message":"read failed"}`)
	}))
	defer server.Close()
	app := nativeConsoleApp(t, server.URL)
	if _, err := app.ConsoleRead("tasks", "p", "", 0, ""); err == nil {
		t.Fatal("failed list read returned success")
	}
}
