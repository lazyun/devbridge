package run

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

const NaturalResultProtocol = "NATURAL_V1"

// ObservedCommand is transport evidence, independent of the final chat text.
type ObservedCommand struct {
	ItemID          string     `json:"itemId"`
	Command         string     `json:"command"`
	CWD             string     `json:"cwd"`
	Status          string     `json:"status"`
	Actions         []string   `json:"actions"`
	ExitCode        *int       `json:"exitCode"`
	Output          string     `json:"output"`
	OutputTruncated bool       `json:"outputTruncated"`
	DurationMillis  *int64     `json:"durationMillis"`
	StartedAt       *time.Time `json:"startedAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
}

type ExecutionEvidence struct {
	RecoveryAfter json.RawMessage             `json:"recoveryAfter,omitempty"`
	Commands      []ObservedCommand           `json:"commands"`
	Checks        []*VerificationObservedFact `json:"checks,omitempty"`
}

type executionRecordStore interface {
	PrepareNaturalResult(context.Context, string) error
	RecordExecutionCommands(context.Context, string, json.RawMessage) error
}

func (s *Service) PrepareNaturalResult(ctx context.Context, id string) error {
	st, ok := s.store.(executionRecordStore)
	if !ok {
		return errors.New("native execution result store unavailable")
	}
	return st.PrepareNaturalResult(ctx, id)
}
func (s *Service) RecordExecutionEvidence(ctx context.Context, id string, value ExecutionEvidence) error {
	st, ok := s.store.(executionRecordStore)
	if !ok {
		return errors.New("native execution evidence store unavailable")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return st.RecordExecutionCommands(ctx, id, b)
}
