package localmcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/localmcp"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func executionproposalStatusPending() executionproposal.Status {
	return executionproposal.StatusPendingConfirmation
}

// heldCodexSession satisfies the execution Codex port without Codex.
type heldCodexSession struct {
	mu       sync.Mutex
	threads  int
	turns    int
	release  chan struct{}
	released bool
}

func (s *heldCodexSession) StartThread(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.threads++
	return "thread-fake", nil
}

func (s *heldCodexSession) StartTurn(context.Context, string, string) (execution.CodexTurnResult, error) {
	s.mu.Lock()
	s.turns++
	if !s.released {
		s.mu.Unlock()
		<-s.release
		return execution.CodexTurnResult{}, context.Canceled
	}
	s.mu.Unlock()
	return execution.CodexTurnResult{}, context.Canceled
}

func (s *heldCodexSession) Close() error { return nil }

type heldCodexFactory struct {
	mu      sync.Mutex
	session *heldCodexSession
}

func (f *heldCodexFactory) Start(context.Context, codexconfig.Config) (execution.CodexSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.session, nil
}

// reservedRun returns the durably reserved Run.

func readyService(t *testing.T) *mcpserver.StaticService {
	t.Helper()
	service, err := mcpserver.NewStaticService(mcpserver.BridgeInfo{ApplicationName: "Dev Bridge", ApplicationVersion: "test", Ready: true})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func startLocalMCPTestServer(t *testing.T, service mcpserver.BridgeService) string {
	t.Helper()
	server, err := localmcp.New(localmcp.Config{
		ListenAddress:   "127.0.0.1:0",
		RequestTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
	}, service)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Wait()
	})
	return "http://" + server.Addr().String()
}

func connectLocalMCP(t *testing.T, baseURL string) *sdkmcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "local-mcp-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + localmcp.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSessionTransport.Store(session, fixtureTransportLocal)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func listToolNames(t *testing.T, session *sdkmcp.ClientSession) []string {
	t.Helper()
	response, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(response.Tools))
	for _, tool := range response.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func projectValue(id, name string, now time.Time) project.Project {
	return project.Project{ID: id, Name: name, CreatedAt: now, UpdatedAt: now}
}

func repositoryValue(id, projectID, name, path string, now time.Time) repository.Repository {
	return repository.Repository{ID: id, ProjectID: projectID, Name: name, Path: path, CreatedAt: now, UpdatedAt: now}
}

func connectGPT(t *testing.T, baseURL string) *sdkmcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "gpt-adapter", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	fixtureSessionTransport.Store(session, fixtureTransportGPT)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func startGPTTestServer(t *testing.T, service mcpserver.BridgeService) (*mcpserver.Server, context.CancelFunc, string) {
	t.Helper()
	server, err := mcpserver.New(mcpserver.Config{
		ListenAddress:   "127.0.0.1:0",
		RequestTimeout:  2 * time.Second,
		ShutdownTimeout: time.Second,
	}, service)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := server.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownCtx)
		_ = server.Wait()
	})
	return server, cancel, "http://" + server.Addr().String()
}

// commitAll stages and commits the working tree, returning the new HEAD.
func commitAll(t *testing.T, repoPath, message string) string {
	t.Helper()
	command := exec.Command("git", "-C", repoPath, "add", "-A")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", repoPath, "-c", "user.name=Dev Bridge Test", "-c", "user.email=test@example.invalid", "commit", "-qm", message)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	head, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(head[:len(head)-1])
}

func (s *heldCodexSession) ResumeThread(_ context.Context, threadID, _ string, _ *run.TokenCounts) (string, error) {
	return threadID, nil
}

func (s *heldCodexSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (execution.CodexTurnResult, error) {
	result, err := s.StartTurn(ctx, thread, prompt)
	if string(schema) == execution.ImplementationReportJSONSchema && err == nil {
		raw, _ := json.Marshal(execution.ImplementationReport{ResultDisposition: "COMPLETED", Summary: result.Message})
		result.Message = string(raw)
	}
	return result, err
}
