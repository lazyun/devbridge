package execution

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

type verificationScope struct {
	workset         workset.RuntimeWorkset
	gitByRepository map[string]evidence.GitEvidence
}

func (c *Coordinator) verificationScope(ctx context.Context, value run.Run, spec task.VerificationSpec, primary evidence.GitEvidence) *verificationScope {
	if value.RuntimeWorkset == nil || value.RuntimeWorkset.Validate() != nil || value.RuntimeWorkset.RunID != value.ID {
		return nil
	}
	scope := &verificationScope{workset: *value.RuntimeWorkset, gitByRepository: map[string]evidence.GitEvidence{}}
	for _, member := range scope.workset.Members {
		for _, check := range spec.Checks {
			if check.RepositoryID != member.RepositoryID || check.Kind != task.VerificationCheckGitAssertion || strings.TrimSpace(check.Command) != "" {
				continue
			}
			if member.MemberID == scope.workset.PrimaryMemberID {
				scope.gitByRepository[member.RepositoryID] = primary
			} else if c.Evidence != nil {
				end, err := c.Evidence.CaptureSnapshot(ctx, member.CanonicalRoot)
				if err == nil {
					facts, err := c.Evidence.CollectRun(ctx, member.CanonicalRoot, member.OriginCommit, member.StartSnapshot, end)
					if err == nil {
						scope.gitByRepository[member.RepositoryID] = facts
					}
				}
			}
			break
		}
	}
	return scope
}

func observeQualifiedCheck(check task.VerificationCheck, commands []ObservedCommand, scope *verificationScope) *run.VerificationObservedFact {
	if !check.RequiresObservation() {
		return nil
	}
	missing := func(reason string) *run.VerificationObservedFact {
		return &run.VerificationObservedFact{Status: run.VerificationCheckNotRun, Reason: reason, Summary: "no exact repository and working-directory observation was recorded"}
	}
	if scope == nil || scope.workset.Validate() != nil {
		return missing("MISSING_WORKSET_PROVENANCE")
	}
	w := scope.workset
	var source run.VerificationObservationSource
	for _, member := range w.Members {
		if member.RepositoryID == check.RepositoryID {
			source = run.VerificationObservationSource{RunID: w.RunID, RuntimeWorksetHash: w.ContentHash, RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: member.MemberVersion, CWD: filepath.Join(member.CanonicalRoot, filepath.FromSlash(check.WorkingDirectory))}
		}
	}
	if !source.Matches(w, check.RepositoryID, check.WorkingDirectory) {
		return missing("INVALID_WORKSET_PROVENANCE")
	}
	switch check.Kind {
	case task.VerificationCheckGitAssertion:
		if strings.TrimSpace(check.Command) == "" {
			facts, ok := scope.gitByRepository[check.RepositoryID]
			if !ok || check.WorkingDirectory != "." {
				return missing("MISSING_MEMBER_GIT_EVIDENCE")
			}
			source.Kind = "GIT"
			fact := observeGitAssertion(check.Instruction, facts)
			fact.Source = &source
			return fact
		}
	case task.VerificationCheckTestCommand, task.VerificationCheckStaticAnalysis, task.VerificationCheckDiff, task.VerificationCheckCustom:
	default:
		// Custom observations remain agent-reported, never Bridge proof.
		return nil
	}
	// The last exact observation wins. A later failed/pending invocation must
	// not be hidden by an earlier successful invocation of the same check.
	if p := w.TaskPermissions; p != nil && p.Purpose == "VERIFICATION" {
		source.TaskPermissionHash = p.ContentHash
		for _, member := range w.Members {
			if member.RepositoryID != check.RepositoryID {
				continue
			}
			for _, copy := range p.Copies {
				if copy.SourceRoot == member.CanonicalRoot {
					source.CWD = filepath.Join(copy.Root, filepath.FromSlash(check.WorkingDirectory))
					source.TaskPermissionHash = p.ContentHash
				}
			}
		}
	}
	for i := len(commands) - 1; i >= 0; i-- {
		observed := commands[i]
		if observed.CWD != source.CWD || !run.VerificationCommandMatches(check.Command, observed.Command) {
			continue
		}
		// Canonical workspace roots alone do not prove that a nested execution
		// directory did not escape through a symlink.
		actual, err := filepath.EvalSymlinks(observed.CWD)
		if err != nil || actual != source.CWD {
			return missing("UNPROVEN_COMMAND_DIRECTORY")
		}
		source.Kind, source.ItemID, source.CommandStatus = "COMMAND", observed.ItemID, observed.Status
		fact := observeCommand(check.Command, []ObservedCommand{observed})
		fact.Source = &source
		if observed.ItemID == "" || observed.Status != "completed" {
			fact.Status, fact.Reason, fact.Summary = run.VerificationCheckUnknown, "COMMAND_NOT_PROVEN_COMPLETE", "matching command lacks exact completed item evidence"
		}
		return fact
	}
	return missing("MISSING_MEMBER_COMMAND_EVIDENCE")
}
