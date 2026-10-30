package appserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/lazyun/devbridge/internal/codexlaunch"
)

func digest(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), nil
}
func socketIdentity(path string) (string, error) {
	s, e := os.Lstat(path)
	if e != nil {
		return "", e
	}
	if s.Mode()&os.ModeSymlink != 0 {
		// Codex 0.159.2 exposes the requested listener path as an alias of a
		// socket in its private daemon directory. Pin both the alias itself and
		// the resolved socket; following Stat alone would accept alias changes.
		link, ok := s.Sys().(*syscall.Stat_t)
		if !ok || link.Uid != uint32(os.Getuid()) {
			return "", errors.New("unsafe appserver socket alias owner")
		}
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", err
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return "", err
		}
		for _, dir := range []string{filepath.Dir(path), filepath.Dir(target)} {
			parent, err := os.Stat(dir)
			if err != nil {
				return "", err
			}
			owner, ok := parent.Sys().(*syscall.Stat_t)
			if !ok || !parent.IsDir() || owner.Uid != uint32(os.Getuid()) || parent.Mode().Perm()&0077 != 0 {
				return "", errors.New("unsafe appserver socket alias directory")
			}
		}
		endpoint, err := os.Lstat(target)
		if err != nil {
			return "", err
		}
		if endpoint.Mode()&os.ModeSocket == 0 {
			return "", errors.New("appserver socket alias target is not a socket")
		}
		native, ok := endpoint.Sys().(*syscall.Stat_t)
		if !ok || native.Uid != uint32(os.Getuid()) {
			return "", errors.New("unsafe appserver socket target owner")
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(s, current) {
			return "", errors.New("appserver socket alias changed during verification")
		}
		identity := fmt.Sprintf("%d:%d:%s:%d:%d", link.Dev, link.Ino, target, native.Dev, native.Ino)
		sum := sha256.Sum256([]byte(identity))
		return "symlink-v1:" + hex.EncodeToString(sum[:]), nil
	}
	if s.Mode()&os.ModeSocket == 0 {
		return "", errors.New("endpoint is not a socket")
	}
	st, ok := s.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("socket identity unavailable")
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}

// Verify observes an existing identity; it never launches or replaces a server.
func Verify(ctx context.Context, b Binding) error {
	if err := VerifyOwner(b, b.DataRoot); err != nil {
		return err
	}
	if b.ID == "" || b.PID <= 0 || b.Epoch <= 0 || b.EndpointKind != "UNIX_WEBSOCKET" || b.Endpoint != "unix://"+b.SocketPath {
		return errors.New("invalid appserver identity")
	}
	p, e := startIdentity(ctx, b.PID)
	if e != nil || p != b.ProcessStartIdentity {
		return errors.New("appserver process identity mismatch")
	}
	actual, e := processExecutable(ctx, b.PID)
	if e != nil || actual != b.ExecutablePath {
		return errors.New("appserver live executable mismatch")
	}
	s, e := socketIdentity(b.SocketPath)
	if e != nil || s != b.SocketIdentity {
		return errors.New("appserver socket identity mismatch")
	}
	h, e := digest(b.ExecutablePath)
	if e != nil || h != b.ExecutableSHA256 {
		return errors.New("appserver executable identity mismatch")
	}
	return nil
}

// Launch is only for explicit new operations. The child has its own session and
// file-backed stdio, so neither Core exit nor a client disconnect owns its life.
func Launch(ctx context.Context, s Spec) (Binding, error) {
	var b Binding
	if s.CodexVersion != "" && !validatedContinuityVersion(s.CodexVersion) {
		return b, errors.New("unsupported App Server continuity version")
	}
	if s.CodexHome == "" {
		s.CodexHome = os.Getenv("CODEX_HOME")
		if s.CodexHome == "" {
			home, e := os.UserHomeDir()
			if e != nil {
				return b, e
			}
			s.CodexHome = filepath.Join(home, ".codex")
		}
	}
	home, e := filepath.Abs(s.CodexHome)
	if e != nil {
		return b, e
	}
	s.CodexHome = home
	if !filepath.IsAbs(s.DataRoot) || !filepath.IsAbs(s.ExecutablePath) {
		return b, errors.New("absolute data root and executable required")
	}
	if e = os.MkdirAll(s.DataRoot, 0700); e != nil {
		return b, e
	}
	canonicalRoot, rootIdentity, e := dataRootIdentity(s.DataRoot)
	if e != nil {
		return b, e
	}
	s.DataRoot = canonicalRoot
	executable, e := filepath.EvalSymlinks(s.ExecutablePath)
	if e != nil {
		return b, e
	}
	versionCtx, cancelVersion := context.WithTimeout(ctx, 5*time.Second)
	versionRaw, versionErr := exec.CommandContext(versionCtx, executable, "--version").Output()
	cancelVersion()
	version := strings.TrimPrefix(strings.TrimSpace(string(versionRaw)), "codex-cli ")
	if versionErr != nil || strings.TrimSpace(string(versionRaw)) != "codex-cli "+version || !validatedContinuityVersion(version) {
		return b, errors.New("App Server continuity requires validated codex-cli 0.149.0, 0.159.2 or 0.163.0")
	}
	if s.CodexVersion != "" && s.CodexVersion != version {
		return b, errors.New("App Server continuity version differs from requested version")
	}
	s.CodexVersion = version
	protocolIdentity, e := schemaIdentity(ctx, executable, s.DataRoot)
	if e != nil {
		return b, e
	}
	if s.ProtocolIdentity != "" && s.ProtocolIdentity != protocolIdentity {
		return b, errors.New("App Server schema identity mismatch")
	}
	s.ProtocolIdentity = protocolIdentity
	hash, e := digest(executable)
	if e != nil {
		return b, e
	}
	idBytes := make([]byte, 8)
	if _, e = rand.Read(idBytes); e != nil {
		return b, e
	}
	id := hex.EncodeToString(idBytes)
	dir := filepath.Join(s.DataRoot, "appservers", id)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return b, e
	}
	rootHash := sha256.Sum256([]byte(s.DataRoot))
	socketDir := filepath.Join("/tmp", fmt.Sprintf("db-as-%d", os.Getuid()), hex.EncodeToString(rootHash[:8]), id)
	if e = os.MkdirAll(socketDir, 0700); e != nil {
		return b, e
	}
	socket := filepath.Join(socketDir, "rpc.sock")
	if len(socket) > 100 {
		return b, errors.New("appserver Unix socket path too long")
	}
	if st, e := os.Stat(socketDir); e != nil || st.Mode().Perm()&0077 != 0 {
		return b, errors.New("unsafe appserver socket directory")
	}

	log, e := os.OpenFile(filepath.Join(dir, "process.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if e != nil {
		return b, e
	}
	defer log.Close()
	args := append([]string{"app-server", "--listen", "unix://" + socket}, s.Args...)
	cmd := exec.Command(executable, codexlaunch.ExecutorArgs(args)...)
	cmd.Env = append(os.Environ(), s.Env...)
	if s.CodexHome != "" {
		cmd.Env = append(cmd.Env, "CODEX_HOME="+s.CodexHome)
	}
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if e = cmd.Start(); e != nil {
		return b, e
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	b = Binding{DataRoot: canonicalRoot, DataRootIdentity: rootIdentity, ID: id, Epoch: time.Now().UTC().UnixMicro(), Endpoint: "unix://" + socket, EndpointKind: "UNIX_WEBSOCKET", SocketPath: socket, SupervisorID: id, PID: cmd.Process.Pid, ExecutablePath: executable, ExecutableSHA256: hash, CodexVersion: s.CodexVersion, ProtocolIdentity: s.ProtocolIdentity, BackendConfigID: s.BackendConfigID, BackendFingerprint: s.BackendFingerprint, LaunchedAt: time.Now().UTC(), Status: "ALIVE"}
	home, _ = filepath.Abs(s.CodexHome)
	b.TaskPermissionHash = s.TaskPermissionHash
	sum := sha256.Sum256([]byte(home))
	b.CodexHomeIdentity = hex.EncodeToString(sum[:])
	b.ProcessStartIdentity, e = startIdentity(ctx, b.PID)
	if e != nil {
		_ = cmd.Process.Kill()
		return Binding{}, e
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		b.SocketIdentity, e = socketIdentity(socket)
		if e == nil {
			break
		}
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			return Binding{}, fmt.Errorf("appserver socket startup canceled: %w", ctx.Err())
		}
		select {
		case exitErr := <-exited:
			return Binding{}, fmt.Errorf("appserver exited before socket startup: process=%v; socket=%w", exitErr, e)
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			return Binding{}, fmt.Errorf("appserver socket startup timed out: %w", e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e = Verify(ctx, b); e != nil {
		_ = cmd.Process.Kill()
		return Binding{}, e
	}
	b.LastConfirmedAliveAt = time.Now().UTC()
	raw, _ := json.Marshal(b)
	receipt, createErr := os.OpenFile(filepath.Join(dir, "binding.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if createErr != nil {
		_ = cmd.Process.Kill()
		return Binding{}, createErr
	}
	_, e = receipt.Write(raw)
	if e == nil {
		e = receipt.Sync()
	}
	closeErr := receipt.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		_ = cmd.Process.Kill()
		return Binding{}, e
	}
	return b, nil
}

// These exact releases have native same-process active-Turn reconnect evidence.
// A newer version still needs its own protocol and Core recovery validation.
func validatedContinuityVersion(version string) bool {
	return version == "0.149.0" || version == "0.159.2" || version == "0.163.0"
}

func Load(dataRoot, id string) (Binding, error) {
	var b Binding
	if id == "" || filepath.Base(id) != id {
		return b, errors.New("invalid instance id")
	}
	raw, e := os.ReadFile(filepath.Join(dataRoot, "appservers", id, "binding.json"))
	if e != nil {
		return b, e
	}
	e = json.Unmarshal(raw, &b)
	return b, e
}

// Stop is explicit lifecycle cleanup, fenced by all captured instance identities.
func Stop(ctx context.Context, b Binding) error {
	if e := Verify(ctx, b); e != nil {
		return e
	}
	p, e := os.FindProcess(b.PID)
	if e != nil {
		return e
	}
	return p.Kill()
}

func processExecutable(ctx context.Context, pid int) (string, error) {
	if runtime.GOOS == "linux" {
		return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	}
	raw, e := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if e != nil {
		return "", e
	}
	return filepath.EvalSymlinks(strings.TrimSpace(string(raw)))
}

func schemaIdentity(ctx context.Context, executable, dataRoot string) (string, error) {
	if e := os.MkdirAll(dataRoot, 0700); e != nil {
		return "", e
	}
	dir, e := os.MkdirTemp(dataRoot, ".appserver-schema-")
	if e != nil {
		return "", e
	}
	defer os.RemoveAll(dir)
	probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if output, e := exec.CommandContext(probeCtx, executable, "app-server", "generate-json-schema", "--experimental", "--out", dir).CombinedOutput(); e != nil {
		return "", fmt.Errorf("App Server schema generation failed: %w: %.300s", e, output)
	}
	var paths []string
	e = filepath.WalkDir(dir, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			paths = append(paths, path)
		}
		return nil
	})
	if e != nil {
		return "", e
	}
	if len(paths) == 0 {
		return "", errors.New("empty App Server schema")
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		raw, e := os.ReadFile(path)
		if e != nil {
			return "", e
		}
		var decoded any
		if e = json.Unmarshal(raw, &decoded); e != nil {
			return "", e
		}
		canonical, e := json.Marshal(decoded)
		if e != nil {
			return "", e
		}
		relative, _ := filepath.Rel(dir, path)
		fmt.Fprintf(hash, "%s\x00", filepath.ToSlash(relative))
		hash.Write(canonical)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyOwner rejects a copied database or replaced data-root directory even
// when it still contains a valid binding to an independently live server.
// The caller must supply its actual locked Core data root, never a DTO value.
func VerifyOwner(b Binding, dataRoot string) error {
	if b.DataRoot == "" || b.DataRootIdentity == "" || dataRoot == "" {
		return errors.New("appserver data-root owner identity missing")
	}
	canonical, identity, err := dataRootIdentity(dataRoot)
	if err != nil {
		return err
	}
	if canonical != b.DataRoot || identity != b.DataRootIdentity {
		return errors.New("appserver data-root owner mismatch")
	}
	return nil
}
func dataRootIdentity(path string) (string, string, error) {
	if !filepath.IsAbs(path) {
		return "", "", errors.New("absolute appserver data root required")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	stat, err := os.Stat(canonical)
	if err != nil {
		return "", "", err
	}
	if !stat.IsDir() {
		return "", "", errors.New("appserver data root is not a directory")
	}
	identity, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", errors.New("appserver data-root identity unavailable")
	}
	return canonical, fmt.Sprintf("%d:%d", identity.Dev, identity.Ino), nil
}
