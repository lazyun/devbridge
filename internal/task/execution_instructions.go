package task

import (
	"errors"
	"strings"
)

// ExecutionInstructions carries exact authored execution text, not a prompt
// override. ApplyExecutionInstructions stores it in the canonical spec fields.
type ExecutionInstructions struct {
	Implementation string  `json:"implementation"`
	Verification   *string `json:"verification,omitempty"`
}

// InstructionSource is immutable submission provenance read alongside a
// definition revision. It contains no second copy of executable instructions.
type InstructionSource struct {
	Kind             string `json:"kind"`
	SourceRevisionID string `json:"sourceRevisionId,omitempty"`
	ReviewID         string `json:"reviewId,omitempty"`
	DecisionID       string `json:"decisionId"`
	RequestID        string `json:"requestId"`
	AISessionID      string `json:"aiSessionId"`
	PayloadSHA256    string `json:"payloadSha256"`
}

// ApplyExecutionInstructions validates only the narrow structural contract.
// It does not interpret engineering meaning or alter any authored text bytes.
func ApplyExecutionInstructions(work WorkSpec, verification VerificationSpec, in *ExecutionInstructions) (WorkSpec, VerificationSpec, error) {
	work, verification = CloneWorkSpec(work), CloneVerificationSpec(verification)
	if in == nil {
		return work, verification, nil
	}
	if strings.TrimSpace(in.Implementation) == "" || len(in.Implementation) > MaxCodexInstructionBytes {
		return WorkSpec{}, VerificationSpec{}, ErrInstructionRequired
	}
	switch verification.Mode {
	case VerificationModeRun:
		if in.Verification == nil || strings.TrimSpace(*in.Verification) == "" || len(*in.Verification) > MaxCodexInstructionBytes {
			return WorkSpec{}, VerificationSpec{}, ErrInstructionRequired
		}
		verification.CodexInstruction = *in.Verification
	case VerificationModeNone:
		if in.Verification != nil {
			return WorkSpec{}, VerificationSpec{}, errors.New("verification instruction is not permitted for NONE")
		}
	default:
		return WorkSpec{}, VerificationSpec{}, errors.New("unsupported verification mode")
	}
	work.CodexInstruction = in.Implementation
	return work, verification, nil
}
