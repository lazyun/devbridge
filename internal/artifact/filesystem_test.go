package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilesystemAtomicWriteHashSizeAndUniquePaths(t *testing.T) {
	ids := []string{"artifact-one", "artifact-two"}
	index := 0
	fs, err := NewFilesystem(t.TempDir(), func() (string, error) { id := ids[index]; index++; return id, nil }, func() time.Time { return time.Unix(42, 0) })
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("durable diff")
	first, err := fs.Write("run-one", KindGitDiff, data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := fs.Write("run-one", KindGitDiff, data)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(data)
	if first.Path == second.Path || first.SizeBytes != int64(len(data)) || first.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	stored, err := os.ReadFile(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(data) {
		t.Fatalf("stored=%q", stored)
	}
	temps, err := filepath.Glob(filepath.Join(filepath.Dir(first.Path), ".artifact-*"))
	if err != nil || len(temps) != 0 {
		t.Fatalf("temporary files=%v err=%v", temps, err)
	}
}

func TestFilesystemRejectsTraversal(t *testing.T) {
	fs, err := NewFilesystem(t.TempDir(), func() (string, error) { return "artifact-ok", nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"../escape", "..", "run/path", ""} {
		if _, err := fs.Write(runID, KindDiagnostic, []byte("x")); err == nil {
			t.Fatalf("run id %q accepted", runID)
		}
	}
	bad, _ := NewFilesystem(t.TempDir(), func() (string, error) { return "../artifact", nil }, nil)
	if _, err := bad.Write("run-ok", KindDiagnostic, []byte("x")); err == nil {
		t.Fatal("unsafe artifact id accepted")
	}
}
