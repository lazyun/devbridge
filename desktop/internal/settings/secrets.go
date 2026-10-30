// Machine-local secret storage for Desktop. Secrets live in dedicated files
// under the Desktop state directory (never in settings.json, never in
// DataRoot) with owner-only permissions. The store never logs secret values.
package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const secretsDirName = "secrets"

// SecretStore persists named machine-local secrets as owner-only files.
type SecretStore struct{ dir string }

// NewSecretStore returns a store rooted at dir (the Desktop state directory).
func NewSecretStore(stateDir string) SecretStore {
	return SecretStore{dir: filepath.Join(stateDir, secretsDirName)}
}

// Dir returns the directory that holds secret files.
func (s SecretStore) Dir() string { return s.dir }

func (s SecretStore) path(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, '/') || strings.ContainsRune(name, '\\') || strings.ContainsRune(name, filepath.Separator) || strings.Contains(name, "..") {
		return "", errors.New("invalid secret name")
	}
	return filepath.Join(s.dir, name), nil
}

// Save atomically replaces a secret file with owner-only permissions. An
// empty trimmed secret is rejected; clearing uses Delete.
func (s SecretStore) Save(name, secret string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if strings.TrimSpace(secret) == "" {
		return errors.New("secret value must not be empty; use Delete to clear")
	}
	if strings.ContainsRune(secret, '\x00') {
		return errors.New("secret value contains a NUL byte")
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return fmt.Errorf("create secrets directory: %w", err)
	}
	tmp, err := os.CreateTemp(s.dir, ".secret-*")
	if err != nil {
		return fmt.Errorf("create temporary secret file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect temporary secret file: %w", err)
	}
	if _, err := tmp.WriteString(secret); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace secret: %w", err)
	}
	return os.Chmod(path, 0600)
}

// Load returns the stored secret value.
func (s SecretStore) Load(name string) (string, error) {
	path, err := s.path(name)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read secret: %w", err)
	}
	return string(data), nil
}

// Exists reports whether a secret is currently stored.
func (s SecretStore) Exists(name string) bool {
	path, err := s.path(name)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Delete removes a stored secret. Deleting an absent secret is a no-op.
func (s SecretStore) Delete(name string) error {
	path, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete secret: %w", err)
	}
	return nil
}

// ErrSecretNotFound is returned by Load when no secret is stored.
var ErrSecretNotFound = errors.New("secret is not configured")
