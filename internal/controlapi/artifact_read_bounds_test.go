package controlapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/lazyun/devbridge/internal/artifact"
)

type artifactReadMetadata struct {
	ArtifactStore
	value artifact.Artifact
}

func (s artifactReadMetadata) Get(context.Context, string) (artifact.Artifact, error) {
	return s.value, nil
}

// Use the production finalized-file writer: its metadata describes a fixed
// snapshot, not a live log. Only the metadata lookup is replaced in this unit
// fixture; no workflow, execution admission, or database rows are fabricated.
func artifactReadFixture(t *testing.T, content []byte) (*Facade, artifact.Artifact) {
	t.Helper()
	fs, err := artifact.NewFilesystem(t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := fs.Write("run-artifact-read", artifact.KindGitDiff, content)
	if err != nil {
		t.Fatal(err)
	}
	return &Facade{deps: Dependencies{Artifacts: artifactReadMetadata{value: stored}}}, stored
}

func TestReadArtifactRejectsRegisteredFileSizeDrift(t *testing.T) {
	original := []byte("registered diff")
	for _, tc := range []struct {
		name   string
		actual []byte
		offset int64
	}{
		{"truncated_first_page", []byte("reg"), 0},
		{"truncated_next_page", []byte("reg"), 3},
		{"truncated_to_empty", nil, 0},
		{"extended_first_page", []byte("registered diff UNREGISTERED"), 0},
		{"extended_recorded_eof", []byte("registered diff UNREGISTERED"), int64(len(original))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facade, stored := artifactReadFixture(t, original)
			if err := os.WriteFile(stored.Path, tc.actual, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := facade.ReadArtifact(context.Background(), stored.ID, tc.offset, 0)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != CodeInfrastructure {
				raw, _ := base64.StdEncoding.DecodeString(out.Data)
				t.Fatalf("registered=%d actual=%d: got data=%q offset=%d next=%d more=%v err=%v; want infrastructure error", stored.SizeBytes, len(tc.actual), raw, out.Offset, out.NextOffset, out.More, err)
			}
			if out != (ArtifactChunk{}) {
				t.Fatalf("unavailable evidence leaked partial success: %+v", out)
			}
			if err := os.WriteFile(stored.Path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := facade.ReadArtifact(context.Background(), stored.ID, 0, 0); err != nil {
				t.Fatalf("restored original snapshot remained unreadable: %v", err)
			}
		})
	}
}

func TestReadArtifactRejectsNonRegularSnapshot(t *testing.T) {
	facade, stored := artifactReadFixture(t, []byte("registered diff"))
	if err := os.Remove(stored.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stored.Path, 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := facade.ReadArtifact(context.Background(), stored.ID, 0, 0)
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != CodeInfrastructure || out != (ArtifactChunk{}) {
		t.Fatalf("directory replacement returned evidence: %+v err=%v", out, err)
	}
}

func TestReadArtifactExactSnapshotPagination(t *testing.T) {
	for _, size := range []int{0, 1, DefaultArtifactReadBytes, MaxArtifactReadBytes + 7} {
		content := bytes.Repeat([]byte{0xf1}, size)
		facade, stored := artifactReadFixture(t, content)
		var all []byte
		var offset int64
		for {
			out, err := facade.ReadArtifact(context.Background(), stored.ID, offset, 0)
			if err != nil {
				t.Fatal(err)
			}
			data, err := base64.StdEncoding.DecodeString(out.Data)
			if err != nil {
				t.Fatal(err)
			}
			if out.Offset != offset || out.NextOffset != offset+int64(len(data)) || out.More != (out.NextOffset < stored.SizeBytes) || len(data) > DefaultArtifactReadBytes || out.Artifact.SHA256 != stored.SHA256 || out.Artifact.SizeBytes != stored.SizeBytes || out.Encoding != "base64" {
				t.Fatalf("invalid pagination or changed metadata: %+v", out)
			}
			all = append(all, data...)
			if !out.More {
				break
			}
			if out.NextOffset <= offset {
				t.Fatal("successful nonterminal chunk made no progress")
			}
			offset = out.NextOffset
		}
		if !bytes.Equal(all, content) {
			t.Fatalf("pagination changed bytes: got %d want %d", len(all), size)
		}
		out, err := facade.ReadArtifact(context.Background(), stored.ID, stored.SizeBytes, MaxArtifactReadBytes)
		if err != nil || out.Data != "" || out.More || out.NextOffset != stored.SizeBytes {
			t.Fatalf("valid EOF: %+v err=%v", out, err)
		}
		for _, bad := range []struct {
			offset int64
			limit  int
		}{{-1, 1}, {stored.SizeBytes + 1, 1}, {0, -1}, {0, MaxArtifactReadBytes + 1}} {
			_, err := facade.ReadArtifact(context.Background(), stored.ID, bad.offset, bad.limit)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Code != CodeInvalidArgument {
				t.Fatalf("invalid bounds %+v: %v", bad, err)
			}
		}
	}
}

func TestReadArtifactConcurrentSnapshotReaders(t *testing.T) {
	content := bytes.Repeat([]byte("snapshot\n"), 100)
	facade, stored := artifactReadFixture(t, content)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := facade.ReadArtifact(context.Background(), stored.ID, 0, MaxArtifactReadBytes)
			if err != nil {
				t.Error(err)
				return
			}
			got, err := base64.StdEncoding.DecodeString(out.Data)
			if err != nil || !bytes.Equal(got, content) || out.More {
				t.Errorf("concurrent read: bytes=%d more=%v err=%v", len(got), out.More, err)
			}
		}()
	}
	wg.Wait()
}
