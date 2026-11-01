package mcpmailbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

var request = []byte(`{"tool":"mutation","requestId":"original"}`)

func unexpectedSend(t *testing.T) func() ([]byte, error) {
	t.Helper()
	return func() ([]byte, error) { t.Error("unexpected dispatch"); return nil, errors.New("must never send") }
}
func writeRecord(t *testing.T, path string, r Record) {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
}

func TestDeliverFirstAndExactReplay(t *testing.T) {
	root := t.TempDir()
	j := Journal{root}
	var calls int
	original := []byte(" {\n  \"ok\": true, \"text\": \"<é>\"\n}\n")
	got, e := j.Deliver("one", request, func() ([]byte, error) { calls++; return original, nil })
	if e != nil || !bytes.Equal(got, original) {
		t.Fatalf("first %q %v", got, e)
	}
	got[0] = 'x' // caller-owned result cannot mutate persisted bytes.
	replay, e := (Journal{root}).Deliver("one", request, unexpectedSend(t))
	if e != nil || !bytes.Equal(replay, original) || calls != 1 {
		t.Fatalf("replay %q %v calls=%d", replay, e, calls)
	}
	for _, name := range []string{"one.claim.json", "one.response.json"} {
		st, e := os.Stat(filepath.Join(root, name))
		if e != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("private file %s: %v %v", name, st, e)
		}
	}
}

func TestChangedInputConflictsBothPendingAndRecorded(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "recorded"}[recorded], func(t *testing.T) {
			j := Journal{t.TempDir()}
			_, _ = j.Deliver("one", request, func() ([]byte, error) {
				if !recorded {
					return nil, errors.New("lost")
				}
				return []byte(`{"ok":true}`), nil
			})
			if _, e := j.Deliver("one", append(append([]byte(nil), request...), ' '), unexpectedSend(t)); !errors.Is(e, ErrInputConflict) {
				t.Fatalf("changed exact bytes: %v", e)
			}
		})
	}
}
func TestUncertainSendNeverRetries(t *testing.T) {
	cases := map[string]func() ([]byte, error){"error": func() ([]byte, error) { return []byte(`{"mayHaveCommitted":true}`), errors.New("socket EOF") }, "invalidJSON": func() ([]byte, error) { return []byte(`{"truncated":`), nil }, "empty": func() ([]byte, error) { return nil, nil }}
	for name, send := range cases {
		t.Run(name, func(t *testing.T) {
			j := Journal{t.TempDir()}
			got, e := j.Deliver("one", request, send)
			if got != nil || !errors.Is(e, ErrOutcomeUnknown) {
				t.Fatalf("first %s %v", got, e)
			}
			got, e = (Journal{j.Root}).Deliver("one", request, unexpectedSend(t))
			if got != nil || !errors.Is(e, ErrOutcomeUnknown) {
				t.Fatalf("reopen %s %v", got, e)
			}
		})
	}
}
func TestClaimPrecedesSendAndPanicStaysUnknown(t *testing.T) {
	j := Journal{t.TempDir()}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected callback panic")
			}
		}()
		_, _ = j.Deliver("one", request, func() ([]byte, error) {
			r, e := readRecord(filepath.Join(j.Root, "one.claim.json"), digest(request), false)
			if e != nil || r.RequestSHA256 != digest(request) {
				t.Fatalf("claim absent before send: %+v %v", r, e)
			}
			panic("after possible effect")
		})
	}()
	if _, e := j.Deliver("one", request, unexpectedSend(t)); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
}
func TestConcurrentDeliverCallsSenderOnce(t *testing.T) {
	j := Journal{t.TempDir()}
	const n = 32
	var calls atomic.Int64
	start := make(chan struct{})
	entered := make(chan struct{})
	release := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for k := 0; k < n; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, e := j.Deliver("same", request, func() ([]byte, error) {
				if calls.Add(1) != 1 {
					t.Error("duplicate sender")
				}
				close(entered)
				<-release
				return []byte(`{"receipt":"same"}`), nil
			})
			results <- e
		}()
	}
	close(start)
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("no sender")
	}
	close(release)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil && !errors.Is(e, ErrOutcomeUnknown) {
			t.Fatalf("unexpected race disposition %v", e)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	if _, e := j.Deliver("same", request, unexpectedSend(t)); e != nil {
		t.Fatal(e)
	}
}
func TestPartialAndCorruptRecordsFailClosed(t *testing.T) {
	for _, kind := range []string{"claim", "response"} {
		for _, name := range []string{"empty", "partial", "missingHash", "badHash", "unknownField", "missingResponse", "wrongShape"} {
			t.Run(kind+"/"+name, func(t *testing.T) {
				j := Journal{t.TempDir()}
				path := filepath.Join(j.Root, "one."+kind+".json")
				var b []byte
				switch name {
				case "empty":
					b = nil
				case "partial":
					b = []byte(`{"requestSha256":`)
				case "missingHash":
					b = []byte(`{}`)
				case "badHash":
					b = []byte(`{"requestSha256":"` + strings.Repeat("z", 64) + `"}`)
				case "unknownField":
					b = []byte(`{"requestSha256":"` + digest(request) + `","unexpected":true}`)
				case "missingResponse":
					if kind == "claim" {
						t.Skip("valid pending claim tested separately")
					}
					b, _ = json.Marshal(Record{RequestSHA256: digest(request)})
				case "wrongShape":
					if kind == "claim" {
						b, _ = json.Marshal(Record{RequestSHA256: digest(request), Response: []byte(`true`)})
					} else {
						b, _ = json.Marshal(Record{RequestSHA256: digest(request), Response: []byte(`bad-json`)})
					}
				}
				if e := os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := j.Deliver("one", request, unexpectedSend(t)); !errors.Is(e, ErrOutcomeUnknown) {
					t.Fatalf("must be unknown, got %v", e)
				}
			})
		}
	}
}
func TestValidPendingClaimBeforeDispatchNeverRuns(t *testing.T) {
	j := Journal{t.TempDir()}
	writeRecord(t, filepath.Join(j.Root, "one.claim.json"), Record{RequestSHA256: digest(request)})
	if _, e := j.Deliver("one", request, unexpectedSend(t)); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
}
func TestResponsePublicationFailureKeepsClaim(t *testing.T) {
	j := Journal{t.TempDir()}
	_, e := j.Deliver("one", request, func() ([]byte, error) {
		if e := os.Mkdir(filepath.Join(j.Root, "one.response.json"), 0700); e != nil {
			t.Fatal(e)
		}
		return []byte(`{"effect":true}`), nil
	})
	if !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	if e := os.Remove(filepath.Join(j.Root, "one.response.json")); e != nil {
		t.Fatal(e)
	}
	if _, e := j.Deliver("one", request, unexpectedSend(t)); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	files, e := filepath.Glob(filepath.Join(j.Root, ".response-*"))
	if e != nil || len(files) != 0 {
		t.Fatalf("temporary response files: %v %v", files, e)
	}
}
func TestInvalidLabelsAndSenderDoNotClaim(t *testing.T) {
	j := Journal{filepath.Join(t.TempDir(), "absent")}
	for _, label := range []string{"", "../escape", "a/b", "a\\b", "a.b", strings.Repeat("a", 129)} {
		if _, e := j.Deliver(label, request, unexpectedSend(t)); e == nil {
			t.Fatalf("accepted %q", label)
		}
	}
	if _, e := j.Deliver("ok", request, nil); e == nil {
		t.Fatal("nil sender accepted")
	}
	if _, e := os.Stat(j.Root); !os.IsNotExist(e) {
		t.Fatalf("created root on invalid input: %v", e)
	}
}

// The child owns only its temporary test directory and does no network access.
func TestJournalCrashChild(t *testing.T) {
	root := os.Getenv("MCP_MAILBOX_CRASH_ROOT")
	if root == "" {
		t.Skip("owned subprocess helper")
	}
	_, e := (Journal{root}).Deliver("one", request, func() ([]byte, error) {
		f, e := os.OpenFile(filepath.Join(root, "effects"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if e != nil {
			return nil, e
		}
		if _, e = f.WriteString("effect\n"); e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return nil, e
		}
		if e = os.WriteFile(filepath.Join(root, "effect-recorded"), []byte("ready"), 0600); e != nil {
			return nil, e
		}
		for {
			time.Sleep(time.Hour)
		}
	})
	t.Fatalf("child must be killed before response: %v", e)
}
func TestSIGKILLAfterEffectDoesNotRedispatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL evidence is Unix-only")
	}
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestJournalCrashChild$", "-test.count=1")
	cmd.Env = []string{"MCP_MAILBOX_CRASH_ROOT=" + root, "PATH=/usr/bin:/bin"}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	joined := false
	defer func() {
		if !joined {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, e := os.Stat(filepath.Join(root, "effect-recorded")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned child did not record effect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e := cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	e := cmd.Wait()
	joined = true
	var exit *exec.ExitError
	if !errors.As(e, &exit) {
		t.Fatalf("not killed: %v output=%s", e, output.String())
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL: %v", exit)
	}
	if _, e = os.Stat(filepath.Join(root, "one.response.json")); !os.IsNotExist(e) {
		t.Fatalf("unexpected persisted response: %v", e)
	}
	if _, e = (Journal{root}).Deliver("one", request, unexpectedSend(t)); !errors.Is(e, ErrOutcomeUnknown) {
		t.Fatal(e)
	}
	effects, e := os.ReadFile(filepath.Join(root, "effects"))
	if e != nil || string(effects) != "effect\n" {
		t.Fatalf("effect count changed: %q %v", effects, e)
	}
	t.Logf("owned child terminated by %s; one actual durable file effect; cold Journal refused dispatch", status.Signal())
}
