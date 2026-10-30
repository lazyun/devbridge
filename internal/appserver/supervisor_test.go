package appserver

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func init() {
	if os.Getenv("DEVBRIDGE_APPSERVER_TEST_CHILD") != "1" {
		return
	}
	args := os.Args[1:]
	if len(args) == 1 && args[0] == "--version" {
		version := os.Getenv("DEVBRIDGE_APPSERVER_TEST_VERSION")
		if version == "" {
			version = "0.149.0"
		}
		fmt.Println("codex-cli " + version)
		os.Exit(0)
	}
	for i, a := range args {
		if a == "--out" && i+1 < len(args) {
			os.WriteFile(filepath.Join(args[i+1], "schema.json"), []byte(`{"type":"object"}`), 0600)
			os.Exit(0)
		}
	}
	for i, a := range args {
		if a == "--listen" && i+1 < len(args) {
			if os.Getenv("DEVBRIDGE_APPSERVER_TEST_EXIT") == "1" {
				os.Exit(9)
			}
			listener, e := net.Listen("unix", strings.TrimPrefix(args[i+1], "unix://"))
			if e != nil {
				fmt.Fprintln(os.Stderr, e)
				os.Exit(2)
			}
			defer listener.Close()
			for {
				c, e := listener.Accept()
				if e != nil {
					os.Exit(3)
				}
				c.Close()
			}
		}
	}
	os.Exit(4)
}
func TestSupervisorDetachedIdentityAndNoReplacement(t *testing.T) {
	t.Setenv("DEVBRIDGE_APPSERVER_TEST_CHILD", "1")
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), strings.Repeat("long-root-", 15))
	b, e := Launch(ctx, Spec{DataRoot: root, ExecutablePath: exe, CodexHome: t.TempDir(), BackendConfigID: "fixture", BackendFingerprint: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = Stop(context.Background(), b) })
	if b.CodexVersion != "0.149.0" || len(b.ProtocolIdentity) != 64 || b.CodexHomeIdentity == "" {
		t.Fatalf("incomplete identity: %+v", b)
	}
	if e = Verify(ctx, b); e != nil {
		t.Fatal(e)
	}
	if e = VerifyOwner(b, root); e != nil {
		t.Fatal(e)
	}
	if e = VerifyOwner(b, t.TempDir()); e == nil {
		t.Fatal("copied database owner accepted")
	}
	alias := filepath.Join(t.TempDir(), "owner-alias")
	if e = os.Symlink(root, alias); e != nil {
		t.Fatal(e)
	}
	if e = VerifyOwner(b, alias); e != nil {
		t.Fatalf("same canonical owner rejected: %v", e)
	}
	loaded, e := Load(root, b.ID)
	if e != nil || loaded != b {
		t.Fatalf("receipt mismatch: %+v %v", loaded, e)
	}
	for _, mutate := range []func(*Binding){func(b *Binding) { b.PID++ }, func(b *Binding) { b.DataRootIdentity += "wrong" }, func(b *Binding) { b.ProcessStartIdentity += "wrong" }, func(b *Binding) { b.SocketIdentity += "wrong" }, func(b *Binding) { b.ExecutableSHA256 = "wrong" }} {
		copy := b
		mutate(&copy)
		if Verify(ctx, copy) == nil {
			t.Fatal("identity corruption accepted")
		}
		if e = Verify(ctx, b); e != nil {
			t.Fatalf("bad identity changed live process: %v", e)
		}
	}
	if e = Stop(ctx, b); e != nil {
		t.Fatal(e)
	}
	time.Sleep(25 * time.Millisecond)
	if Verify(ctx, b) == nil {
		t.Fatal("dead instance accepted")
	}
	again, e := Load(root, b.ID)
	if e != nil || again.ID != b.ID || again.PID != b.PID {
		t.Fatal("dead instance replaced")
	}
}
func TestSupervisorRejectsUnvalidatedVersionBeforeLaunch(t *testing.T) {
	_, e := Launch(context.Background(), Spec{CodexVersion: "0.150.0"})
	if e == nil {
		t.Fatal("accepted unvalidated version")
	}
}

func TestSupervisorReportsExitBeforeSocketStartup(t *testing.T) {
	t.Setenv("DEVBRIDGE_APPSERVER_TEST_CHILD", "1")
	t.Setenv("DEVBRIDGE_APPSERVER_TEST_EXIT", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Launch(context.Background(), Spec{DataRoot: t.TempDir(), ExecutablePath: executable, CodexHome: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "appserver exited before socket startup") || !strings.Contains(err.Error(), "exit status 9") {
		t.Fatalf("missing child startup failure: %v", err)
	}
}

func TestSupervisorContinuityVersionIsMeasuredAndPinned(t *testing.T) {
	t.Setenv("DEVBRIDGE_APPSERVER_TEST_CHILD", "1")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, reported, requested string
		allowed                   bool
	}{
		{"native-01592", "0.159.2", "0.159.2", true},
		{"native-01630", "0.163.0", "0.163.0", true},
		{"detect-01630", "0.163.0", "", true},
		{"detect-01592", "0.159.2", "", true},
		{"wrong-pinned-version", "0.159.2", "0.149.0", false},
		{"unvalidated-release", "0.159.3", "", false},
		{"unvalidated-build-suffix", "0.159.2-dev", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEVBRIDGE_APPSERVER_TEST_VERSION", tc.reported)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			binding, launchErr := Launch(ctx, Spec{DataRoot: root, ExecutablePath: executable, CodexHome: t.TempDir(), CodexVersion: tc.requested})
			if !tc.allowed {
				if launchErr == nil {
					_ = Stop(context.Background(), binding)
					t.Fatal("unvalidated or mismatched version launched")
				}
				if _, err := os.Stat(filepath.Join(root, "appservers")); !os.IsNotExist(err) {
					t.Fatalf("refused version created an instance: %v", err)
				}
				return
			}
			if launchErr != nil {
				t.Fatal(launchErr)
			}
			t.Cleanup(func() { _ = Stop(context.Background(), binding) })
			if binding.CodexVersion != tc.reported || Verify(ctx, binding) != nil {
				t.Fatalf("binding did not preserve measured version: %+v", binding)
			}
		})
	}
}

func TestVerifyOwnerRejectsDirectoryReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, identity, err := dataRootIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	binding := Binding{DataRoot: canonical, DataRootIdentity: identity}
	if err = os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if VerifyOwner(binding, root) == nil {
		t.Fatal("replacement at identical path accepted")
	}
}
