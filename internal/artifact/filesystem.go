package artifact

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type Filesystem struct {
	root  string
	newID func() (string, error)
	now   func() time.Time
}

func NewFilesystem(dataRoot string, newID func() (string, error), now func() time.Time) (*Filesystem, error) {
	if dataRoot == "" {
		return nil, errors.New("data root is required")
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve data root: %w", err)
	}
	if newID == nil {
		newID = randomID
	}
	if now == nil {
		now = time.Now
	}
	return &Filesystem{root: filepath.Join(filepath.Clean(absolute), "artifacts"), newID: newID, now: now}, nil
}

func (f *Filesystem) Write(runID string, kind Kind, data []byte) (Artifact, error) {
	if !safeID.MatchString(runID) || strings.Contains(runID, "..") {
		return Artifact{}, errors.New("unsafe run id")
	}
	id, err := f.newID()
	if err != nil {
		return Artifact{}, fmt.Errorf("allocate artifact id: %w", err)
	}
	if !safeID.MatchString(id) || strings.Contains(id, "..") {
		return Artifact{}, errors.New("unsafe artifact id")
	}
	directory := filepath.Join(f.root, runID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Artifact{}, fmt.Errorf("create artifact directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".artifact-*")
	if err != nil {
		return Artifact{}, fmt.Errorf("create artifact temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	finalPath := filepath.Join(directory, id)
	if _, err := os.Lstat(finalPath); err == nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return Artifact{}, errors.New("artifact id already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
		return Artifact{}, fmt.Errorf("inspect artifact destination: %w", err)
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryName)
		}
	}()
	hash := sha256.New()
	written, err := temporary.Write(data)
	if err == nil {
		_, err = hash.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("write artifact: %w", err)
	}
	if err := os.Rename(temporaryName, finalPath); err != nil {
		return Artifact{}, fmt.Errorf("finalize artifact: %w", err)
	}
	if directoryHandle, err := os.Open(directory); err == nil {
		_ = directoryHandle.Sync()
		_ = directoryHandle.Close()
	}
	committed = true
	return Artifact{ID: id, RunID: runID, Kind: kind, Path: finalPath, SizeBytes: int64(written), SHA256: hex.EncodeToString(hash.Sum(nil)), CreatedAt: f.now().UTC()}, nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "artifact_" + hex.EncodeToString(raw[:]), nil
}
