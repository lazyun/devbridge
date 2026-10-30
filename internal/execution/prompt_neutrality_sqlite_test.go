package execution_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	exec "github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

type neutralProtocolSession struct{ protocolSession }

func (s *neutralProtocolSession) StartTurnWithSchema(ctx context.Context, thread, prompt string, schema json.RawMessage) (exec.CodexTurnResult, error) {
	result, err := s.protocolSession.StartTurnWithSchema(ctx, thread, prompt, schema)
	if string(schema) == exec.VerificationReportV2JSONSchema {
		result.Message = `{"version":2,"summary":"observations","checks":[{"index":0,"status":"PASSED","summary":"pass","evidence":"","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"pass","evidence":"","reason":""}]}`
		zero := 0
		result.Commands = []exec.ObservedCommand{{Command: "go test ./...", ExitCode: &zero}}
	}
	return result, err
}

func TestPromptNeutralityFrozenRevisionByteIdentityAndSHA(t *testing.T) {
	// Keep the prerequisite Review result within its bounded evidence envelope;
	// the unit-level prompt test covers the full instruction byte limit.
	for name, instruction := range map[string]string{"leading": " \tinspect", "trailing": "inspect \t", "newlines": "\r\ninspect\n\n", "unicode": "检查 e\u0301 🚀", "symbols": "<$&`{}\\> # +=", "long": " \n" + strings.Repeat("界", 9000) + "\n "} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newAuthorizedPlanPanicFixture(t, instruction)
			defer f.db.Close()
			defer f.supervisor.Shutdown()
			revision, err := f.coordinator.Tasks.(interface {
				GetDefinitionRevision(context.Context, string) (task.DefinitionRevision, error)
			}).GetDefinitionRevision(ctx, f.proposalValue.TaskDefinitionRevisionID)
			if err != nil {
				t.Fatal(err)
			}
			frozen := revision.Definition.ApplyTo(f.taskValue)
			if frozen.WorkSpec.CodexInstruction != instruction || frozen.VerificationSpec.CodexInstruction != instruction {
				t.Fatal("durable revision altered authored bytes")
			}
			backend := &neutralProtocolSession{}
			f.coordinator.Codex = neutralProtocolFactory{backend}
			reserved := f.implementationRun
			prepared, err := f.coordinator.PrepareReservedRun(ctx, reserved.ID, exec.Instruction{CodexInstruction: instruction, ExecutionScope: f.work.ExecutionScope})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.coordinator.ExecuteRun(ctx, prepared, nil); err != nil {
				t.Fatal(err)
			}
			_, verificationRun, err := f.proposals.ReserveVerificationRun(ctx, f.proposalValue.ID, "neutral-verification", f.now)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err = f.coordinator.PrepareReservedExecution(ctx, verificationRun.ID, exec.Instruction{CodexInstruction: instruction}, &f.verification)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.coordinator.ExecuteRun(ctx, prepared, nil); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(instruction))
			for i, id := range []string{reserved.ID, verificationRun.ID} {
				saved, err := f.runs.Get(ctx, id)
				if err != nil || saved.Status != run.StatusSucceeded || saved.FinalCodexPrompt != instruction || saved.PromptSHA256 != hex.EncodeToString(sum[:]) || saved.PromptBytes != int64(len(instruction)) || saved.PromptSentAt == nil || backend.prompts[i] != instruction {
					t.Fatalf("durable prompt mismatch: %s %v", id, err)
				}
			}
			if backend.starts != 0 || backend.resumes != 2 || backend.lastResume == "" {
				t.Fatalf("implementation and verification did not reuse the authoritative Feature Area thread: starts=%d resumes=%d lastResume=%q", backend.starts, backend.resumes, backend.lastResume)
			}
		})
	}
}

type neutralProtocolFactory struct{ session exec.CodexSession }

func (f neutralProtocolFactory) Start(context.Context, codexconfig.Config) (exec.CodexSession, error) {
	return f.session, nil
}

func TestPromptNeutralityRejectsRevisionInstructionSubstitution(t *testing.T) {
	f := newAuthorizedPlanPanicFixture(t)
	defer f.db.Close()
	defer f.supervisor.Shutdown()
	ctx := context.Background()
	reserved := f.implementationRun
	prepared, err := f.coordinator.PrepareReservedRun(ctx, reserved.ID, exec.Instruction{CodexInstruction: "different authored instruction"})
	if err == nil || prepared.Run.Failure == nil || prepared.Run.Failure.Code != "CONTRACT_INCOMPLETE" {
		t.Fatalf("substitution accepted: %+v %v", prepared.Run, err)
	}
	select {
	case <-f.factory.created:
		t.Fatal("substitution launched backend")
	default:
	}
}
