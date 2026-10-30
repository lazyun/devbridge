package piagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// configurationIdentity names the actual configured launcher, not a Codex
// config row and not an independently qualified runtime. Imported packages,
// interpreter dependencies and inherited account configuration are not hashed.
func (f *Factory) configurationIdentity() (string, string, error) {
	if f == nil {
		return "", "", errors.New("pi factory is unavailable")
	}
	if err := validateExtraArgs(f.ExtraArgs); err != nil {
		return "", "", err
	}
	for _, value := range append([]string{f.PiPath, f.Model, f.ThinkingLevel, f.SessionDir}, f.ExtraArgs...) {
		if strings.ContainsRune(value, '\x00') {
			return "", "", errors.New("pi configuration contains NUL")
		}
	}
	resolved := Resolve(f.PiPath)
	path, err := exec.LookPath(resolved)
	if err != nil {
		return "", "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", "", err
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0111 == 0 {
		return "", "", errors.New("pi launcher must be an executable regular file")
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", "", err
	}
	after, err := os.Stat(path)
	if err != nil {
		return "", "", err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return "", "", errors.New("pi launcher changed while measuring configuration")
	}
	material := struct {
		Version, ConfiguredPath, ResolvedPath, CanonicalPath, ExecutableSHA256 string
		Model, ThinkingLevel, SessionDir                                       string
		ExtraArgs                                                              []string
	}{"PI_AGENT_CONFIG_V1", f.PiPath, path, canonical, hex.EncodeToString(digest.Sum(nil)), f.Model, f.ThinkingLevel, f.SessionDir, append([]string{}, f.ExtraArgs...)}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), path, nil
}

func (f *Factory) configurationFingerprint() string {
	fingerprint, _, err := f.configurationIdentity()
	if err != nil {
		return ""
	}
	return fingerprint
}

func (s *Session) verifyLaunchConfiguration() error {
	if s.launchConfig == nil {
		return nil
	}
	actual := s.launchConfig.configurationFingerprint()
	if actual == "" || actual != s.launchConfigFingerprint {
		return errors.New("pi configured launcher differs from frozen selection")
	}
	return nil
}
