package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
)

// ValidateRecoveryOwnerFacts validates the original owner's contract using
// canonical definitions and independently observed source command/file facts.
// It neither finalizes the old Run nor treats the new report as a receipt for
// an old answer. A missing contract leaves the assessment insufficient.
func (c *Coordinator) ValidateRecoveryOwnerFacts(ctx context.Context, source run.Run, report recoverystatus.Report, facts *recoverystatus.FactualObservation) error {
	facts.OwnerContractValidated = false
	facts.SourcePurpose = string(source.Purpose)
	if source.RuntimeWorkset == nil || source.RuntimeWorkset.Validate() != nil {
		return nil
	}
	w := *source.RuntimeWorkset
	if w.ContentHash != facts.Source.RuntimeWorksetHash || w.TaskDefinitionRevisionID != facts.Source.TaskDefinitionRevisionID || source.OperationID != facts.Source.OperationID || source.ID != facts.Source.RunID || source.TurnID != facts.Source.TurnID {
		return recoverystatus.ErrConflict
	}
	var definition task.Task
	if source.TaskID != "" {
		var err error
		definition, err = c.Tasks.Get(ctx, source.TaskID)
		if err != nil {
			return err
		}
		if definition.ProjectID != source.ProjectID || definition.FeatureAreaID != source.FeatureAreaID {
			return recoverystatus.ErrConflict
		}
		// Review worksets are owned by Review ID and may precede an approved
		// Task revision. Explicit frozen revisions still have to match.
		if (source.Purpose != run.PurposeScopeReview || facts.Source.TaskDefinitionRevisionID != "") && definition.CurrentDefinitionRevisionID != facts.Source.TaskDefinitionRevisionID {
			return recoverystatus.ErrConflict
		}
		if goals, ok := c.Tasks.(taskGoalLister); ok {
			values, e := goals.ListGoals(ctx, source.TaskID)
			if e != nil {
				return e
			}
			for _, g := range values {
				facts.ApprovedGoalIDs = append(facts.ApprovedGoalIDs, g.ID)
			}
		}
		if len(facts.ApprovedGoalIDs) == 0 && source.Purpose != run.PurposeScopeReview && definition.WorkSpec.Goal != "" {
			facts.ApprovedGoalIDs = []string{definition.WorkSpec.Goal}
		}
	}
	switch source.Purpose {
	case run.PurposeImplementation:
		if definition.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
			return nil
		}
		if len(definition.RepositoryTargets) > 0 {
			if task.ValidateExecutablePrimary(task.Definition{Version: 2, RepositoryTargets: definition.RepositoryTargets, PrimaryMemberID: definition.PrimaryMemberID, WorkSpec: definition.WorkSpec}) != nil {
				return nil
			}
		} else if task.ValidateWorkSpec(definition.WorkSpec) != nil {
			return nil
		}
		if report.OverallStatus != "COMPLETED" {
			return nil
		}
		// All members, scope, goals and claimed paths are independently compared by
		// Assess. The recovered envelope satisfies the same implementation result
		// contract as ordinary execution; it does not certify semantic correctness.
		raw, _ := json.Marshal(ImplementationReport{ResultDisposition: "COMPLETED", Summary: report.Summary})
		if _, err := parseImplementationReport(string(raw)); err != nil {
			return err
		}
		facts.OwnerResult = raw
	case run.PurposeVerification:
		if definition.VerificationSpec.Mode != task.VerificationModeRun || task.ValidateVerificationSpec(definition.VerificationSpec) != nil {
			return nil
		}
		var commands []ObservedCommand
		if source.AgentRuntime != nil {
			for _, item := range source.AgentRuntime.Items {
				if item.Type != "commandExecution" {
					continue
				}
				// An unreadable invocation may be a later execution of a required
				// check. Omitting it would let an older PASS become authoritative.
				if item.Truncated || item.ID == "" {
					return nil
				}
				var wire struct {
					Command        string `json:"command"`
					CWD            string `json:"cwd"`
					Status         string `json:"status"`
					ExitCode       *int   `json:"exitCode"`
					CommandActions []struct {
						Command string `json:"command"`
					} `json:"commandActions"`
				}
				if json.Unmarshal(item.Metadata, &wire) != nil || strings.TrimSpace(wire.Command) == "" || strings.TrimSpace(wire.CWD) == "" {
					return nil
				}
				var actions []string
				for _, action := range wire.CommandActions {
					if strings.TrimSpace(action.Command) != "" {
						actions = append(actions, action.Command)
					}
				}
				commands = append(commands, ObservedCommand{ItemID: item.ID, Command: wire.Command, Actions: actions, CWD: wire.CWD, Status: wire.Status, ExitCode: wire.ExitCode, Output: item.OutputTail, StartedAt: item.StartedAt, FinishedAt: item.CompletedAt})
			}
		}
		var gitFacts evidence.GitEvidence
		if source.Result != nil {
			gitFacts = source.Result.GitEvidence
		}
		raw := report.VerificationReport
		if len(raw) == 0 && source.ResultProtocol != run.NaturalResultProtocol && source.AgentRuntime != nil {
			for _, item := range source.AgentRuntime.Items {
				if item.Type == "agentMessage" && !item.Truncated && json.Valid([]byte(item.Text)) {
					var candidate map[string]json.RawMessage
					if json.Unmarshal([]byte(item.Text), &candidate) == nil && len(candidate["checks"]) > 0 {
						raw = []byte(item.Text)
					}
				}
			}
		}
		if len(raw) == 0 {
			return nil
		}
		scope := c.verificationScope(ctx, source, definition.VerificationSpec, gitFacts)
		verified, err := buildVerificationReportWithScope(definition.VerificationSpec, string(raw), commands, gitFacts, scope)
		if err != nil {
			return nil
		}
		// Only commands from the declared member and exact cwd may substantiate a
		// required check; an identical command in another repository is not proof.
		for index, check := range definition.VerificationSpec.Checks {
			if !check.Required {
				continue
			}
			id := fmt.Sprintf("check-%d", index+1)
			facts.RequiredChecks = append(facts.RequiredChecks, id)
			if check.RepositoryID == "" {
				for _, member := range w.Members {
					if member.MemberID == w.PrimaryMemberID {
						check.RepositoryID = member.RepositoryID
					}
				}
				if check.WorkingDirectory == "" {
					check.WorkingDirectory = "."
				}
			}
			observed := observeQualifiedCheck(check, commands, scope)
			if observed == nil || observed.Status != run.VerificationCheckPassed {
				return nil
			}
			rawFact, _ := json.Marshal(observed)
			facts.CheckEvidence = append(facts.CheckEvidence, recoverystatus.CheckEvidence{CheckID: id, Command: check.Command, ExpectedCommand: check.Command, Passed: true, ExitCode: observed.ExitCode, EvidenceHash: recoverystatus.Hash(rawFact)})
			verified.Checks[index].Observed = observed
			copyObservedCompatibility(&verified.Checks[index], observed)
		}
		facts.OwnerResult, _ = json.Marshal(verified)
	case run.PurposeScopeReview:
		if w.ReviewID != source.ReviewID || definition.CurrentReviewID != source.ReviewID {
			return recoverystatus.ErrConflict
		}
		if report.ReviewReport == nil || report.ReviewReport.SchemaVersion != 2 || report.ReviewReport.Validate() != nil || c.Reviews == nil {
			return nil
		}
		original, err := c.Reviews.Get(ctx, source.ReviewID)
		if err != nil {
			return err
		}
		if original.RunID != source.ID || original.TaskID != source.TaskID || original.ExecutorKind.Effective() != review.ExecutorCodingAgent {
			return recoverystatus.ErrConflict
		}
		if report.ReviewReport.Disposition == review.DispositionApprovalReady && review.ValidateAdmittedTargets(original.RepositoryTargets, report.ReviewReport.RepositoryTargets) != nil {
			return nil
		}
		for _, target := range report.ReviewReport.RepositoryTargets {
			found := false
			for _, member := range w.Members {
				if target.RepositoryID == member.RepositoryID && target.MemberID == member.MemberID && target.MemberVersion == member.MemberVersion && target.OriginCommit == member.OriginCommit {
					found = true
				}
			}
			if !found {
				return nil
			}
		}
		refs := append([]review.EvidenceRef{}, report.ReviewReport.EvidenceRefs...)
		for _, finding := range report.ReviewReport.Findings {
			refs = append(refs, finding.EvidenceRefs...)
		}
		for _, ref := range refs {
			root := ""
			for _, member := range w.Members {
				if member.RepositoryID == ref.RepositoryID && member.MemberID == ref.MemberID {
					root = member.CanonicalRoot
				}
			}
			if root == "" || ref.Path == "" || ref.Digest == "" {
				return nil
			}
			path, err := filepath.EvalSymlinks(filepath.Join(root, ref.Path))
			if err != nil {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil || recoverystatus.Hash(data) != ref.Digest {
				return nil
			}
			// Use the same line-count convention as canonical Review reads,
			// including their final empty line after a trailing newline.
			if ref.EndLine > 0 && ref.EndLine > bytes.Count(data, []byte{'\n'})+1 {
				return nil
			}
		}
		facts.ReviewReport = report.ReviewReport
		facts.OwnerResult, _ = json.Marshal(report.ReviewReport.CompatibilityResult())
	default:
		return nil
	}
	if len(facts.OwnerResult) == 0 {
		return nil
	}
	material, _ := json.Marshal(struct {
		Definition    task.Task
		WorksetHash   string
		Before, After []recoverystatus.WorktreeObservation
		Checks        []recoverystatus.CheckEvidence
		Result        json.RawMessage
	}{definition, w.ContentHash, facts.Before, facts.After, facts.CheckEvidence, facts.OwnerResult})
	facts.OwnerContractEvidenceHash = recoverystatus.Hash(material)
	facts.OwnerContractValidated = true
	return nil
}
