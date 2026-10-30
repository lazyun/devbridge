// Package simulation drives real, privately owned Core/MCP binaries with
// deterministic upstream and pull-worker actors. It never opens Core's database.
package simulation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"time"

	"github.com/lazyun/devbridge/internal/review"
)

// Config intentionally has no existing Core URL, DataRoot, repository or model
// option. Every run owns a fresh child directory and only disposable fixtures.
type Config struct {
	BinDir      string
	EvidenceDir string
	Scenario    string
	Timeout     time.Duration
}

type Check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail any    `json:"detail,omitempty"`
}
type ScenarioResult struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Directory  string    `json:"directory"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
	Checks     []Check   `json:"checks"`
	Error      string    `json:"error,omitempty"`
}
type Report struct {
	SchemaVersion int               `json:"schemaVersion"`
	Status        string            `json:"status"`
	Directory     string            `json:"directory"`
	StartedAt     time.Time         `json:"startedAt"`
	FinishedAt    time.Time         `json:"finishedAt"`
	Binaries      map[string]string `json:"binarySHA256"`
	DriverSHA256  string            `json:"driverSHA256"`
	Real          []string          `json:"real"`
	Simulated     []string          `json:"simulated"`
	NotCovered    []string          `json:"notCovered"`
	Scenarios     []ScenarioResult  `json:"scenarios"`
}

var scenarioNames = []string{"task-admission", "review-gates", "review-blocked", "review-further", "happy", "claim-race", "file-crash", "review-cancel"}

func Scenarios() []string { return append([]string(nil), scenarioNames...) }

func validateConfig(c Config) (Config, error) {
	if c.BinDir == "" {
		return c, errors.New("-bin-dir is required: select Core and both MCP binaries built from the revision under test")
	}
	if c.Scenario == "" {
		c.Scenario = "all"
	}
	valid := c.Scenario == "all"
	for _, n := range scenarioNames {
		valid = valid || c.Scenario == n
	}
	if !valid {
		return c, fmt.Errorf("unknown scenario %q", c.Scenario)
	}
	if c.Timeout == 0 {
		c.Timeout = 5 * time.Minute
	}
	if c.Timeout <= 0 {
		return c, errors.New("timeout must be positive")
	}
	var err error
	c.BinDir, err = filepath.Abs(c.BinDir)
	if err != nil {
		return c, err
	}
	if c.EvidenceDir != "" {
		c.EvidenceDir, err = filepath.Abs(c.EvidenceDir)
		if err != nil {
			return c, err
		}
	}
	for _, name := range []string{"devbridge", "devbridge-mcp", "devbridge-local-mcp"} {
		st, e := os.Stat(filepath.Join(c.BinDir, name))
		if e != nil {
			return c, e
		}
		if !st.Mode().IsRegular() || st.Mode().Perm()&0111 == 0 {
			return c, fmt.Errorf("%s must be an executable file", name)
		}
	}
	return c, nil
}

// Run returns a machine-readable report even when a scenario fails. Evidence is
// retained for inspection; a failure never silently becomes a skipped/pass gate.
func Run(ctx context.Context, config Config) (report Report, err error) {
	c, err := validateConfig(config)
	if err != nil {
		return report, err
	}
	if c.EvidenceDir != "" {
		if err = os.MkdirAll(c.EvidenceDir, 0700); err != nil {
			return report, err
		}
	}
	root, err := os.MkdirTemp(c.EvidenceDir, "devbridge-simulation-")
	if err != nil {
		return report, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return report, err
	}
	report = Report{SchemaVersion: 1, Status: "RUNNING", Directory: root, StartedAt: time.Now().UTC(), Binaries: map[string]string{},
		Real:       []string{"compiled Core and both MCP proxies", "public HTTP/MCP admission, confirmation and state transitions", "SQLite owned only by Core", "Git repositories, worktrees, file effects and delivery"},
		Simulated:  []string{"upstream GPT authorship", "explicit fixture-user choices and acceptance", "downstream deterministic neutral pull workers (not Codex/Pi)"},
		NotCovered: []string{"real model quality or hosted assistant execution", "native app-server qualification, implementation or command verification", "VERIFYING/run-analysis/native recovery", "live connector/tunnel deployment", "OS sandbox security against malicious executable code"}}
	for _, name := range []string{"devbridge", "devbridge-mcp", "devbridge-local-mcp"} {
		f, e := os.Open(filepath.Join(c.BinDir, name))
		if e != nil {
			return report, e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		_ = f.Close()
		if e != nil {
			return report, e
		}
		report.Binaries[name] = hex.EncodeToString(h.Sum(nil))
	}
	executable, e := os.Executable()
	if e != nil {
		return report, e
	}
	driver, e := os.Open(executable)
	if e != nil {
		return report, e
	}
	digest := sha256.New()
	_, e = io.Copy(digest, driver)
	_ = driver.Close()
	if e != nil {
		return report, e
	}
	report.DriverSHA256 = hex.EncodeToString(digest.Sum(nil))
	defer func() {
		report.FinishedAt = time.Now().UTC()
		if err != nil {
			report.Status = "FAILED"
		} else {
			report.Status = "PASSED"
		}
		b, e := json.MarshalIndent(report, "", "  ")
		if e == nil {
			e = os.WriteFile(filepath.Join(root, "report.json"), append(b, '\n'), 0600)
		}
		if e != nil {
			err = errors.Join(err, e)
		}
	}()
	for _, name := range scenarioNames {
		if c.Scenario != "all" && c.Scenario != name {
			continue
		}
		if e := ctx.Err(); e != nil {
			err = errors.Join(err, e)
			break
		}
		result := runScenario(ctx, c, root, name)
		report.Scenarios = append(report.Scenarios, result)
		if result.Status != "PASSED" {
			err = errors.Join(err, fmt.Errorf("%s: %s", name, result.Error))
		}
	}
	return report, err
}

type scenarioFailure struct{ message string }
type scenario struct {
	ctx          context.Context
	config       Config
	root         string
	result       *ScenarioResult
	mu           sync.Mutex
	trace        *os.File
	processes    []*child
	env          []string
	core         string
	gpt, agent   *actor
	fixture      *fixture
	launchNumber int
}

func runScenario(ctx context.Context, c Config, root, name string) (result ScenarioResult) {
	result = ScenarioResult{Name: name, Status: "RUNNING", Directory: filepath.Join(root, name), StartedAt: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	s := &scenario{ctx: ctx, config: c, root: result.Directory, result: &result}
	defer func() {
		if recovered := recover(); recovered != nil {
			switch p := recovered.(type) {
			case scenarioFailure:
				result.Error = p.message
			default:
				result.Error = fmt.Sprintf("simulator panic: %v\n%s", p, debug.Stack())
			}
		}
		s.stop(false)

		b, tripwireErr := os.ReadFile(filepath.Join(s.root, "unexpected-native-invocation"))
		result.Checks = append(result.Checks, Check{Name: "native runtime was never invoked", Passed: os.IsNotExist(tripwireErr)})
		if tripwireErr == nil {
			result.Error += "; unexpected native invocation: " + string(b)
		} else if !os.IsNotExist(tripwireErr) {
			result.Error += "; native tripwire check: " + tripwireErr.Error()
		}

		if s.trace != nil {
			if e := s.trace.Close(); e != nil {
				result.Error += "; close trace: " + e.Error()
			}
		}
		result.FinishedAt = time.Now().UTC()
		result.Status = "PASSED"
		if result.Error != "" {
			result.Status = "FAILED"
		}
	}()
	s.must(os.MkdirAll(s.root, 0700))
	var e error
	s.trace, e = os.OpenFile(filepath.Join(s.root, "transcript.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	s.must(e)
	s.prepareEnvironment()
	s.launch()
	s.fixture = s.provision()
	if name != "task-admission" {
		s.fixture.createTask()
	}
	switch name {
	case "task-admission":
		s.taskAdmission()
	case "review-gates":
		s.reviewGates()
	case "review-blocked":
		s.reviewNotReady(review.DispositionBlocked)
	case "review-further":
		s.reviewNotReady(review.DispositionFurtherReviewRequired)
	case "happy":
		s.happy()
	case "claim-race":
		s.claimRace()
	case "file-crash":
		s.fileCrash()
	case "review-cancel":
		s.reviewCancel()
	}
	s.fixture.checkSources()
	return result
}
func (s *scenario) must(err error) {
	if err != nil {
		panic(scenarioFailure{err.Error()})
	}
}
func (s *scenario) check(name string, ok bool, detail any) {
	s.mu.Lock()
	s.result.Checks = append(s.result.Checks, Check{name, ok, detail})
	s.mu.Unlock()
	s.record(map[string]any{"kind": "assertion", "name": name, "passed": ok, "detail": detail})
	if !ok {
		panic(scenarioFailure{fmt.Sprintf("%s: %s", name, encoded(detail))})
	}
}
func (s *scenario) record(v map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v["time"] = time.Now().UTC()
	b, e := json.Marshal(v)
	if e == nil {
		_, e = s.trace.Write(append(b, '\n'))
	}
	if e != nil {
		panic(scenarioFailure{"write evidence: " + e.Error()})
	}
}
func encoded(v any) string { b, _ := json.Marshal(v); return string(b) }
func object(v any) map[string]any {
	b, e := json.Marshal(v)
	if e != nil {
		panic(scenarioFailure{e.Error()})
	}
	var out map[string]any
	if e = json.Unmarshal(b, &out); e != nil {
		panic(scenarioFailure{e.Error()})
	}
	return out
}
func decode(v, out any) {
	b, e := json.Marshal(v)
	if e == nil {
		e = json.Unmarshal(b, out)
	}
	if e != nil {
		panic(scenarioFailure{e.Error()})
	}
}
func field(v map[string]any, key string) string {
	value, ok := v[key].(string)
	if !ok || value == "" {
		panic(scenarioFailure{fmt.Sprintf("missing %s: %s", key, encoded(v))})
	}
	return value
}
