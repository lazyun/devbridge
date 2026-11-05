package controlapi

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
)

type ArtifactChunk struct {
	Artifact   ArtifactMetadata
	Offset     int64
	NextOffset int64
	More       bool
	Encoding   string
	Data       string
}

type ArtifactMetadata struct {
	ID, RunID string
	Kind      string
	SizeBytes int64
	SHA256    string
}

func (f *Facade) ReadArtifact(ctx context.Context, id string, offset int64, maxBytes int) (ArtifactChunk, error) {
	if err := validID("artifactId", id); err != nil {
		return ArtifactChunk{}, err
	}
	if offset < 0 {
		return ArtifactChunk{}, invalid("offset must be non-negative")
	}
	if maxBytes == 0 {
		maxBytes = DefaultArtifactReadBytes
	}
	if maxBytes < 1 || maxBytes > MaxArtifactReadBytes {
		return ArtifactChunk{}, invalid("maxBytes must be between 1 and 65536")
	}
	value, err := f.deps.Artifacts.Get(ctx, id)
	if err != nil {
		return ArtifactChunk{}, MapError(err)
	}
	if offset > value.SizeBytes {
		return ArtifactChunk{}, invalid("offset exceeds artifact size")
	}
	file, err := os.Open(value.Path)
	if err != nil {
		return ArtifactChunk{}, &Error{Code: CodeInfrastructure, Message: "artifact bytes are unavailable", cause: err}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return ArtifactChunk{}, &Error{Code: CodeInfrastructure, Message: "artifact bytes could not be inspected", cause: err}
	}
	// Registered artifacts are finalized snapshots, not live files. A changed
	// size must not yield unregistered bytes or a non-progressing More=true page.
	if !info.Mode().IsRegular() || info.Size() != value.SizeBytes {
		return ArtifactChunk{}, &Error{Code: CodeInfrastructure, Message: "artifact bytes do not match the registered size"}
	}
	if remaining := value.SizeBytes - offset; remaining < int64(maxBytes) {
		maxBytes = int(remaining)
	}
	buffer := make([]byte, maxBytes)
	read, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return ArtifactChunk{}, &Error{Code: CodeInfrastructure, Message: "artifact bytes could not be read", cause: err}
	}
	if read != len(buffer) {
		return ArtifactChunk{}, &Error{Code: CodeInfrastructure, Message: "artifact bytes ended before the registered size", cause: io.ErrUnexpectedEOF}
	}
	next := offset + int64(read)
	return ArtifactChunk{Artifact: ArtifactMetadata{ID: value.ID, RunID: value.RunID, Kind: string(value.Kind), SizeBytes: value.SizeBytes, SHA256: value.SHA256}, Offset: offset, NextOffset: next, More: next < value.SizeBytes, Encoding: "base64", Data: base64.StdEncoding.EncodeToString(buffer[:read])}, nil
}
