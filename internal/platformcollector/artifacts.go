// Package platformcollector measures registered native Codex artifacts. It is
// an internal Core dependency, not a transport API or a runtime callback.
package platformcollector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

var (
	ErrIncompatible    = errors.New("RUNTIME_PLATFORM_NATIVE_CONFORMANCE_FAILED")
	ErrArtifactCorrupt = errors.New("RUNTIME_PLATFORM_COLLECTION_ARTIFACT_CORRUPT")
)

// ArtifactStore retains the original bytes before an evidence row may refer to
// their hash. Platform artifacts have no Run owner and never create a fake Run.
type ArtifactStore struct{ root string }

func NewArtifactStore(root string) (*ArtifactStore, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("platform evidence root must be absolute")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	return &ArtifactStore{root: resolved}, nil
}

func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func canonical(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *ArtifactStore) Put(raw []byte) (string, error) {
	hash := digest(raw)
	target := filepath.Join(s.root, hash+".json")
	if existing, err := s.Read(hash); err == nil {
		if !bytes.Equal(existing, raw) {
			return "", ErrArtifactCorrupt
		}
		return hash, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	f, err := os.CreateTemp(s.root, ".platform-artifact-")
	if err != nil {
		return "", err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	// Link is an atomic no-replace publication, including concurrent collectors.
	if err = os.Link(name, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, e := s.Read(hash)
			if e == nil && bytes.Equal(existing, raw) {
				return hash, nil
			}
		}
		return "", err
	}
	dir, err := os.Open(s.root)
	if err != nil {
		return "", err
	}
	err = dir.Sync()
	_ = dir.Close()
	return hash, err
}

func (s *ArtifactStore) Read(hash string) ([]byte, error) {
	if !runtimeplatform.ValidDigest(hash) {
		return nil, ErrArtifactCorrupt
	}
	p := filepath.Join(s.root, hash+".json")
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, ErrArtifactCorrupt
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if digest(raw) != hash {
		return nil, ErrArtifactCorrupt
	}
	return raw, nil
}

func (s *ArtifactStore) ReadReport(hash string) (Report, error) {
	var r Report
	raw, err := s.Read(hash)
	if err != nil {
		return r, err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&r); err != nil {
		return r, fmt.Errorf("%w: %v", ErrArtifactCorrupt, err)
	}
	if r.Version != 1 || r.Policy != PolicyVersion || r.DescriptorID == "" || r.Manifest.NativeDigest == "" {
		return r, ErrArtifactCorrupt
	}
	schema, err := s.Read(r.SchemaArtifactHash)
	if err != nil {
		return r, err
	}
	if digest(schema) != r.Manifest.ProtocolDigest {
		return r, ErrArtifactCorrupt
	}
	return r, nil
}
