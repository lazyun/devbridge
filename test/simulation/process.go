package simulation

import (
	"bufio"
	"fmt"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type child struct {
	cmd  *exec.Cmd
	done chan struct{}
	log  *os.File
	once sync.Once
}

func (p *child) stop(crash bool) {
	p.once.Do(func() {
		select {
		case <-p.done:
		default:
			if crash {
				_ = p.cmd.Process.Kill()
			} else {
				_ = p.cmd.Process.Signal(os.Interrupt)
			}
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				_ = p.cmd.Process.Kill()
				<-p.done
			}
		}
		_ = p.log.Close()
	})
}
func isolatedEnvironment(home, root, selection string) []string {
	// Deliberate allowlist: no inherited tokens, proxy, model, Git hooks/config or
	// user's Codex/Pi preferences. Selection points only to the fail-fast tripwire.
	return []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "CODEX_HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"TMPDIR=" + root, "TMP=" + root, "TEMP=" + root, "LANG=C", "LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(home, "empty-gitconfig"), "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=DevBridge Simulation", "GIT_AUTHOR_EMAIL=simulation@example.invalid", "GIT_COMMITTER_NAME=DevBridge Simulation", "GIT_COMMITTER_EMAIL=simulation@example.invalid",
		codexlaunch.SelectionEnv + "=" + selection, "DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=0", "OTEL_SDK_DISABLED=true"}
}
func (s *scenario) prepareEnvironment() {
	home := filepath.Join(s.root, "home")
	s.must(os.Mkdir(home, 0700))
	s.must(os.WriteFile(filepath.Join(home, "empty-gitconfig"), nil, 0600))
	tripwire := filepath.Join(s.root, "native-must-not-run")
	// The path is supplied to the tripwire through an env variable, never shell interpolation.
	s.must(os.WriteFile(tripwire, []byte("#!/bin/sh\nprintf 'unexpected native invocation\\n' >> \"$DEVBRIDGE_SIM_TRIPWIRE\"\nexit 99\n"), 0700))
	selection := filepath.Join(s.root, "native-selection.json")
	s.must(codexlaunch.Write(selection, codexlaunch.Selection{Configured: tripwire, Executable: tripwire}))
	s.env = append(isolatedEnvironment(home, s.root, selection), "DEVBRIDGE_SIM_TRIPWIRE="+filepath.Join(s.root, "unexpected-native-invocation"))
}
func (s *scenario) launchOne(name string, args ...string) string {
	p := &child{cmd: exec.Command(filepath.Join(s.config.BinDir, name), args...), done: make(chan struct{})}
	p.cmd.Env = s.env
	p.cmd.Dir = s.root
	var err error
	p.log, err = os.OpenFile(filepath.Join(s.root, fmt.Sprintf("%02d-%s.log", s.launchNumber, name)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	s.must(err)
	pipe, err := p.cmd.StdoutPipe()
	if err != nil {
		_ = p.log.Close()
		s.must(err)
	}
	p.cmd.Stderr = p.log
	if err = p.cmd.Start(); err != nil {
		_ = p.log.Close()
		s.must(err)
	}
	s.processes = append(s.processes, p)
	ready := make(chan string, 1)
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(pipe)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		pattern := regexp.MustCompile(`http://127\.0\.0\.1:[0-9]+`)
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = fmt.Fprintln(p.log, line)
			if u := pattern.FindString(line); u != "" {
				select {
				case ready <- u:
				default:
				}
			}
		}
	}()
	go func() { _ = p.cmd.Wait(); <-scanDone; close(p.done) }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case u := <-ready:
		req, err := http.NewRequestWithContext(s.ctx, http.MethodGet, u+"/readyz", nil)
		s.must(err)
		resp, err := loopbackClient().Do(req)
		s.must(err)
		_ = resp.Body.Close()
		s.check(name+" ready", resp.StatusCode == http.StatusOK, resp.StatusCode)
		s.record(map[string]any{"kind": "owned-process", "name": name, "pid": p.cmd.Process.Pid, "url": u})
		return u
	case <-p.done:
		panic(scenarioFailure{name + " exited before readiness; inspect process log"})
	case <-timer.C:
		panic(scenarioFailure{name + " readiness timed out; inspect process log"})
	case <-s.ctx.Done():
		s.must(s.ctx.Err())
	}
	return ""
}
func loopbackClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return fmt.Errorf("redirects forbidden in isolated simulation")
	}}
}
func (s *scenario) launch() {
	s.launchNumber++
	data := filepath.Join(s.root, "data")
	s.core = s.launchOne("devbridge", "-mode", "server", "-data-root", data, "-listen", "127.0.0.1:0")
	gu := s.launchOne("devbridge-mcp", "-data-root", data, "-core-url", s.core, "-listen", "127.0.0.1:0")
	au := s.launchOne("devbridge-local-mcp", "-data-root", data, "-core-url", s.core, "-listen", "127.0.0.1:0")
	connect := func(role, url string) *actor {
		client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "devbridge-simulation-" + role, Version: "1"}, nil)
		session, err := client.Connect(s.ctx, &sdkmcp.StreamableClientTransport{Endpoint: url + "/mcp", HTTPClient: loopbackClient(), MaxRetries: -1, DisableStandaloneSSE: true}, nil)
		s.must(err)
		return &actor{s: s, role: role, session: session}
	}
	s.gpt = connect("upstream_gpt", gu)
	s.agent = connect("downstream_agent", au)
}
func (s *scenario) stop(crash bool) {
	// A crash scenario first SIGKILLs only its owned Core process, rather than
	// sending a graceful interrupt and calling that crash recovery.
	if crash && len(s.processes) > 0 {
		s.processes[0].stop(true)
	}
	if s.agent != nil {
		_ = s.agent.session.Close()
		s.agent = nil
	}
	if s.gpt != nil {
		_ = s.gpt.session.Close()
		s.gpt = nil
	}
	for i := len(s.processes) - 1; i >= 0; i-- {
		s.processes[i].stop(false)
	}
	s.processes = nil
}
func (s *scenario) git(path string, args ...string) string {
	cmd := exec.CommandContext(s.ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = path
	cmd.Env = s.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		s.must(fmt.Errorf("fixture git %v: %w: %s", args, err, out))
	}
	return strings.TrimSpace(string(out))
}
