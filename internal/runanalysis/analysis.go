// Package runanalysis separates GPT-authored engineering analysis from native
// execution completion. Submitting analysis never runs commands or accepts work.
package runanalysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

var ErrStale = errors.New("RUN_ANALYSIS_TARGET_STALE")
var ErrConflict = errors.New("RUN_ANALYSIS_REQUEST_CONFLICT")
var ErrInvalid = errors.New("RUN_ANALYSIS_INVALID")

type Input struct {
	AISessionID string          `json:"aiSessionId"`
	RunID       string          `json:"runId"`
	RequestID   string          `json:"requestId"`
	SourceHash  string          `json:"sourceHash"`
	Report      json.RawMessage `json:"report"`
}
type Receipt struct {
	RunID         string    `json:"runId"`
	RequestID     string    `json:"requestId"`
	Kind          string    `json:"kind"`
	PayloadSHA256 string    `json:"payloadSHA256"`
	SubmittedAt   time.Time `json:"submittedAt"`
}
type Source struct {
	// Read the original persisted Review analysis without reinterpreting native
	// prose or confusing its frozen source hash with the current workflow.
	ReviewResult       *review.ImplementationResult    `json:"reviewResult,omitempty"`
	RecordedSourceHash string                          `json:"recordedSourceHash,omitempty"`
	Recovery           *recoverystatus.Record          `json:"recovery,omitempty"`
	RecoveryAfter      json.RawMessage                 `json:"recoveryAfter,omitempty"`
	Run                run.Run                         `json:"run"`
	Kind               string                          `json:"kind"`
	State              string                          `json:"state"`
	Commands           []run.ObservedCommand           `json:"commands"`
	ObservedChecks     []*run.VerificationObservedFact `json:"observedChecks,omitempty"`
	VerificationSpec   task.VerificationSpec           `json:"verificationSpec,omitempty"`
	SourceHash         string                          `json:"sourceHash"`
	ReportSchema       json.RawMessage                 `json:"reportSchema,omitempty"`
	Review             *review.Review                  `json:"review,omitempty"`
	Report             json.RawMessage                 `json:"report,omitempty"`
	Receipt            *Receipt                        `json:"receipt,omitempty"`
}
type Result struct {
	Inspection   json.RawMessage
	Recovery     json.RawMessage
	Review       json.RawMessage
	Verification *run.VerificationReport
}
type Store interface {
	Source(context.Context, string, string) (Source, error)
	Find(context.Context, Input) (Receipt, bool, error)
	Commit(context.Context, Input, Source, Result) (Receipt, error)
}
type Service struct {
	Store   Store
	Analyze func(Source, json.RawMessage) (Result, error)
}

func Hash(in Input) string {
	// Normalize object key order and whitespace while retaining JSON numbers.
	// The explicit report parser remains responsible for its closed contract.
	var report any
	decoder := json.NewDecoder(bytes.NewReader(in.Report))
	decoder.UseNumber()
	if decoder.Decode(&report) == nil {
		in.Report, _ = json.Marshal(report)
	}
	b, _ := json.Marshal(in)
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
func (s *Service) Get(ctx context.Context, session, id string) (Source, error) {
	return s.Store.Source(ctx, session, id)
}
func (s *Service) Submit(ctx context.Context, in Input) (Receipt, error) {
	if strings.TrimSpace(in.AISessionID) == "" || len(in.AISessionID) > 128 || strings.TrimSpace(in.RunID) == "" || len(in.RunID) > 128 || strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 128 || len(in.SourceHash) != 64 || len(in.Report) == 0 || len(in.Report) > 256<<10 || !json.Valid(in.Report) {
		return Receipt{}, ErrInvalid
	}
	if r, found, err := s.Store.Find(ctx, in); found || err != nil {
		return r, err
	}
	source, err := s.Store.Source(ctx, in.AISessionID, in.RunID)
	if err != nil {
		return Receipt{}, err
	}
	if (source.State != "REQUIRED" && source.State != "OPTIONAL") || in.SourceHash != source.SourceHash {
		return Receipt{}, ErrStale
	}
	result, err := s.Analyze(source, in.Report)
	if err != nil {
		return Receipt{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return s.Store.Commit(ctx, in, source, result)
}

// RecordedReport is a read-only projection of the original GPT submission.
// It contains no Session authority or ability to submit/continue an operation.
type RecordedReport struct {
	State   string          `json:"state"`
	Report  json.RawMessage `json:"report,omitempty"`
	Receipt *Receipt        `json:"receipt,omitempty"`
}
