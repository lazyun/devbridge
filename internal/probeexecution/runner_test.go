package probeexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/contextprobe"
	"github.com/lazyun/devbridge/internal/projectcontext"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
)

func TestBuildPromptReturnsAuthoredInstructionByteExact(t *testing.T) {
	instruction := "  Keep leading whitespace.\nDo not reinterpret <symbols>.  "
	prompt := BuildPrompt(contextprobe.Probe{ProjectID: "project", RepositoryID: "repo", Scope: contextprobe.Scope{Kind: contextprobe.ScopePaths, Paths: []string{"AGENTS.md"}}, CodexInstruction: instruction}, "/repo")
	if prompt != instruction {
		t.Fatalf("Context Probe instruction changed: %q", prompt)
	}
}

type sessions map[string]aisession.Session

func (s sessions) Get(_ context.Context, id string) (aisession.Session, error) { return s[id], nil }

type repositories map[string]repository.Repository

func (r repositories) Get(_ context.Context, id string) (repository.Repository, error) {
	return r[id], nil
}

type configs map[string]codexconfig.Config

func (c configs) Get(_ context.Context, id string) (codexconfig.Config, error) { return c[id], nil }

type fakeFactory struct {
	mu      sync.Mutex
	starts  int
	session *fakeSession
}

func (f *fakeFactory) Start(context.Context, codexconfig.Config) (CodexSession, error) {
	f.mu.Lock()
	f.starts++
	f.mu.Unlock()
	return f.session, nil
}
func (f *fakeFactory) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.starts }

type fakeSession struct {
	done    chan struct{}
	prompt  string
	cwd     string
	threads int
	turns   int
}

func (s *fakeSession) StartThread(_ context.Context, cwd string) (string, error) {
	s.cwd = cwd
	s.threads++
	return "thread", nil
}
func (s *fakeSession) StartTurn(_ context.Context, _ string, prompt string) (CodexTurnResult, error) {
	s.prompt = prompt
	s.turns++
	return CodexTurnResult{TurnID: "turn", Message: "probe result", TokenUsage: &run.TokenUsageObservation{StartCumulative: run.TokenCounts{TotalTokens: 2}, EndCumulative: run.TokenCounts{TotalTokens: 5}, Reliable: true}}, nil
}
func (s *fakeSession) Close() error { close(s.done); return nil }

func TestCreateDoesNotLaunchAndRunnerUsesIndependentProbeLifecycle(t *testing.T) {
	for name, instruction := range map[string]string{"leading": " \tinspect", "trailing": "inspect \t", "newlines": "\r\ninspect\n\n", "unicode": "检查 e\u0301 🚀", "symbols": "<$&`{}\\> # +=", "long": " \n" + strings.Repeat("界", 21000) + "\n "} {
		t.Run(name, func(t *testing.T) { testProbePromptByteIdentity(t, instruction) })
	}
}
func testProbePromptByteIdentity(t *testing.T, instruction string) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Date(2026, 9, 4, 5, 0, 0, 0, time.UTC)
	repositoryValues := repositories{"repo": {ID: "repo", ProjectID: "project", Path: root}}
	configValues := configs{"config": {ID: "config", Enabled: true}}
	contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), func() time.Time { now = now.Add(time.Second); return now })
	probes, err := contextprobe.NewService(&ownedProbeStore{MemoryStore: contextprobe.NewMemoryStore()}, sessions{"session": {ID: "session", ProjectID: "project"}}, repositoryValues, configValues, contexts, func() (string, error) { return "probe", nil }, func() time.Time { now = now.Add(time.Second); return now })
	if err != nil {
		t.Fatal(err)
	}

	probe, err := probes.Create(ctx, contextprobe.CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: instruction})
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeSession{done: make(chan struct{})}
	factory := &fakeFactory{session: session}
	runner, err := NewRunner(probes, repositoryValues, configValues, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()
	if factory.count() != 0 || probe.Status != contextprobe.StatusCreated {
		t.Fatal("creating a probe launched Codex")
	}
	started, err := runner.Start(ctx, "session", probe.ID)
	if err != nil || started.Status != contextprobe.StatusRunning {
		t.Fatalf("start=%+v err=%v", started, err)
	}
	select {
	case <-session.done:
	case <-time.After(2 * time.Second):
		t.Fatal("probe execution did not finish")
	}
	finished, err := probes.Get(ctx, probe.ID)
	if err != nil {
		t.Fatal(err)
	}
	if factory.count() != 1 || finished.Status != contextprobe.StatusSucceeded || finished.ThreadID != "thread" || finished.TurnID != "turn" || finished.Result != "probe result" || finished.TokenUsage.Usage == nil || finished.TokenUsage.Usage.TotalTokens != 3 {
		t.Fatalf("finished=%+v starts=%d", finished, factory.count())
	}
	sum := sha256.Sum256([]byte(instruction))
	if finished.CodexInstruction != instruction || finished.PromptSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("Probe SHA or durable instruction mismatch")
	}
	if session.cwd != root || session.prompt != instruction || finished.FinalCodexPrompt != instruction || finished.PromptBytes != int64(len(instruction)) || finished.PromptSentAt == nil {
		t.Fatalf("cwd=%q prompt=%q", session.cwd, session.prompt)
	}
}

// ownedProbeStore is a recording test adapter. The production memory store
// deliberately has no durable-ownership capability and cannot start a backend.
type ownedProbeStore struct {
	*contextprobe.MemoryStore
	mu       sync.Mutex
	checks   int
	rejectAt int
}

func (s *ownedProbeStore) Update(ctx context.Context, id string, update func(*contextprobe.Probe) error) (contextprobe.Probe, error) {
	return s.MemoryStore.Update(ctx, id, func(p *contextprobe.Probe) error {
		if err := update(p); err != nil {
			return err
		}
		if p.Status == contextprobe.StatusRunning && p.RunID == "" {
			p.TaskID, p.RunID, p.RuntimeEpoch = "inspection-task", "inspection-run", "test-epoch"
		}
		return nil
	})
}

func (s *ownedProbeStore) ValidateInspectionExecution(ctx context.Context, p contextprobe.Probe) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	fence, ok := runtimeepoch.FromContext(ctx)
	if !ok || fence.RunID != p.RunID || fence.Epoch != p.RuntimeEpoch {
		return errors.New("inspection fence mismatch")
	}
	if s.rejectAt > 0 && s.checks >= s.rejectAt {
		return errors.New("inspection execution ownership rejected")
	}
	return nil
}

func TestInspectionOwnershipRevalidatedBeforeEveryBackendHandoff(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		rejectAt, starts, threads, turns int
	}{
		{"admission", 1, 0, 0, 0},
		{"factory", 2, 0, 0, 0},
		{"thread", 3, 1, 0, 0},
		{"turn", 4, 1, 1, 0},
		{"valid", 0, 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := &ownedProbeStore{MemoryStore: contextprobe.NewMemoryStore(), rejectAt: tc.rejectAt}
			repos := repositories{"repo": {ID: "repo", ProjectID: "project", Path: t.TempDir()}}
			cfg := configs{"config": {ID: "config", Enabled: true}}
			contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), time.Now)
			probes, err := contextprobe.NewService(store, sessions{"session": {ID: "session", ProjectID: "project"}}, repos, cfg, contexts, func() (string, error) { return "probe", nil }, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			p, err := probes.Create(ctx, contextprobe.CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "inspect"})
			if err != nil {
				t.Fatal(err)
			}
			session := &fakeSession{done: make(chan struct{})}
			factory := &fakeFactory{session: session}
			runner, err := NewRunner(probes, repos, cfg, factory)
			if err != nil {
				t.Fatal(err)
			}
			defer runner.Shutdown()
			_, err = runner.Start(ctx, "session", p.ID)
			if tc.rejectAt == 1 && err == nil {
				t.Fatal("invalid admission accepted")
			}
			if tc.rejectAt != 1 && err != nil {
				t.Fatal(err)
			}
			// Wait for the worker without canceling the test's valid operation.
			runner.wg.Wait()
			if factory.count() != tc.starts || session.threads != tc.threads || session.turns != tc.turns {
				t.Fatalf("backend calls factory/thread/turn = %d/%d/%d", factory.count(), session.threads, session.turns)
			}
		})
	}
}

func TestInspectionCompatibilityStoreCannotAuthorizeBackend(t *testing.T) {
	ctx := context.Background()
	repos := repositories{"repo": {ID: "repo", ProjectID: "project", Path: t.TempDir()}}
	cfg := configs{"config": {ID: "config", Enabled: true}}
	contexts := projectcontext.NewService(projectcontext.NewMemoryStore(), time.Now)
	probes, err := contextprobe.NewService(contextprobe.NewMemoryStore(), sessions{"session": {ID: "session", ProjectID: "project"}}, repos, cfg, contexts, func() (string, error) { return "probe", nil }, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := probes.Create(ctx, contextprobe.CreateInput{AISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", Scope: contextprobe.Scope{Kind: contextprobe.ScopeRepository}, CodexInstruction: "inspect"})
	if err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{}
	runner, err := NewRunner(probes, repos, cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.Shutdown()
	if _, err = runner.Start(ctx, "session", p.ID); err == nil {
		t.Fatal("compatibility store admitted backend work")
	}
	if factory.count() != 0 {
		t.Fatal("backend called without durable execution ownership")
	}
}
