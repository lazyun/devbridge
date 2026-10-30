package recoverystatus

import (
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func TestReviewPurposeRejectsInvalidPlanAndDepth(t *testing.T) {
	source := Source{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", OperationKind: "REVIEW", OperationID: "review", RunID: "run", TurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: strings.Repeat("a", 64), OwnerKind: "REVIEW", OwnerID: "review"}
	proposal := PurposeProposal{RecoveryID: "recovery", RecoveryGeneration: 1, OwnerKind: "REVIEW", OwnerID: "review", TaskID: "task", FeatureAreaID: "area", SourceReviewID: "review", TaskDefinitionRevisionID: "revision", SourceOperationID: "review", SourceRunID: "run", SourceTurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: source.RuntimeWorksetHash, AssessmentHash: strings.Repeat("b", 64), SourceAISessionID: "session", Variants: []PurposeVariant{{ID: "variant", Label: "Review revised scope", Summary: "Review revised target", Review: &ReviewPurposeVariant{SourceReviewID: "review", ReviewInstruction: "Inspect revised target", ReviewPlan: run.ReviewPlan{Mode: "INVALID"}, ReviewDepth: task.ReviewDepth("INVALID"), SupplementalPurpose: "Reassess the revised scope", RepositoryTargets: []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("c", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}}}}}}
	proposal.SubmittedHash = proposal.ComputeSubmittedHash()
	if err := proposal.Validate(source, proposal.RecoveryID, proposal.RecoveryGeneration, proposal.AssessmentHash); err == nil {
		t.Fatal("invalid Review plan and depth authorized")
	}
}

func TestReviewPurposeRejectsInvalidTypedVariants(t *testing.T) {
	source := Source{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", OperationKind: "REVIEW", OperationID: "review", RunID: "run", TurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: strings.Repeat("a", 64), OwnerKind: "REVIEW", OwnerID: "review"}
	target := workset.Target{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("c", 40), Read: true, Write: workset.WriteScope{Kind: "NONE"}}
	base := PurposeProposal{RecoveryID: "recovery", RecoveryGeneration: 1, OwnerKind: "REVIEW", OwnerID: "review", TaskID: "task", FeatureAreaID: "area", SourceReviewID: "review", TaskDefinitionRevisionID: "revision", SourceOperationID: "review", SourceRunID: "run", SourceTurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: source.RuntimeWorksetHash, AssessmentHash: strings.Repeat("b", 64), SourceAISessionID: "session", Variants: []PurposeVariant{{ID: "variant", Label: "Revised Review", Summary: "Review revised target", Review: &ReviewPurposeVariant{SourceReviewID: "review", ReviewInstruction: "Inspect revised target", ReviewPlan: run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: run.ReviewScope{Kind: run.ReviewScopeRepository}}, ReviewDepth: task.ReviewDepthDeep, SupplementalPurpose: "Reassess revised scope", RepositoryTargets: []workset.Target{target}}}}}
	base.SubmittedHash = base.ComputeSubmittedHash()
	if err := base.Validate(source, base.RecoveryID, 1, base.AssessmentHash); err != nil {
		t.Fatalf("valid Review purpose rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		edit func(*PurposeProposal)
	}{
		{"empty variants", func(p *PurposeProposal) { p.Variants = nil }},
		{"duplicate variant", func(p *PurposeProposal) { p.Variants = append(p.Variants, p.Variants[0]) }},
		{"wrong owner", func(p *PurposeProposal) { p.OwnerKind = "IMPLEMENTATION" }},
		{"empty instruction", func(p *PurposeProposal) { p.Variants[0].Review.ReviewInstruction = "" }},
		{"invalid plan", func(p *PurposeProposal) { p.Variants[0].Review.ReviewPlan.Mode = "INVALID" }},
		{"invalid target", func(p *PurposeProposal) { p.Variants[0].Review.RepositoryTargets[0].MemberVersion = 0 }},
		{"hash mismatch", func(p *PurposeProposal) { p.Variants[0].Summary = "changed without rehash" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Variants = append([]PurposeVariant(nil), base.Variants...)
			b := *base.Variants[0].Review
			b.RepositoryTargets = append([]workset.Target(nil), b.RepositoryTargets...)
			p.Variants[0].Review = &b
			tc.edit(&p)
			if tc.name != "hash mismatch" {
				p.SubmittedHash = p.ComputeSubmittedHash()
			}
			if err := p.Validate(source, p.RecoveryID, 1, p.AssessmentHash); !errors.Is(err, ErrIneligible) {
				t.Fatalf("invalid proposal accepted: %v", err)
			}
		})
	}
}

func TestExecutionPurposeRejectsInvalidOwnerSpecificVariants(t *testing.T) {
	for _, owner := range []string{"IMPLEMENTATION", "VERIFICATION"} {
		t.Run(owner, func(t *testing.T) {
			source := Source{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", OperationKind: "EXECUTION", OperationID: "execution", RunID: "run", TurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: strings.Repeat("a", 64)}
			work := task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "Implement revised target", CodexInstruction: "Implement revised target", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}
			verification := task.VerificationSpec{Mode: task.VerificationModeNone, Waiver: &task.VerificationWaiver{Source: task.VerificationWaiverUser, Reason: "Explicit waiver"}}
			target := workset.Target{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("c", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}}
			variant := PurposeVariant{ID: "variant", Label: "Revised scope", Summary: "Implement revised scope"}
			if owner == "IMPLEMENTATION" {
				variant.Implementation = &ImplementationPurposeVariant{SourceExecutionID: "execution", SourceRevisionID: "revision", WorkSpec: work, VerificationSpec: verification, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: "member", ImplementationMode: task.ImplementationCodingAgent}
			} else {
				variant.Verification = &VerificationPurposeVariant{SourceExecutionID: "execution", SourceRevisionID: "revision", WorkSpec: &work, VerificationSpec: verification, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: "member", ImplementationMode: task.ImplementationCodingAgent}
			}
			base := PurposeProposal{RecoveryID: "recovery", RecoveryGeneration: 1, OwnerKind: owner, OwnerID: "execution", TaskID: "task", FeatureAreaID: "area", SourceExecutionID: "execution", TaskDefinitionRevisionID: "revision", SourceOperationID: "execution", SourceRunID: "run", SourceTurnID: "turn", RuntimeEpoch: "epoch", RuntimeWorksetHash: source.RuntimeWorksetHash, AssessmentHash: strings.Repeat("b", 64), SourceAISessionID: "session", Variants: []PurposeVariant{variant}}
			base.SubmittedHash = base.ComputeSubmittedHash()
			if err := base.Validate(source, base.RecoveryID, 1, base.AssessmentHash); err != nil {
				t.Fatalf("valid %s purpose rejected: %v", owner, err)
			}
			for _, name := range []string{"empty variants", "duplicate variant", "wrong owner", "wrong payload", "invalid work", "invalid verification", "invalid target", "wrong primary", "hash mismatch"} {
				t.Run(name, func(t *testing.T) {
					p := base
					p.Variants = append([]PurposeVariant(nil), base.Variants...)
					b := p.Variants[0]
					if b.Implementation != nil {
						v := *b.Implementation
						v.RepositoryTargets = append([]workset.Target(nil), v.RepositoryTargets...)
						b.Implementation = &v
					} else {
						v := *b.Verification
						v.RepositoryTargets = append([]workset.Target(nil), v.RepositoryTargets...)
						w := *v.WorkSpec
						v.WorkSpec = &w
						b.Verification = &v
					}
					p.Variants[0] = b
					switch name {
					case "empty variants":
						p.Variants = nil
					case "duplicate variant":
						p.Variants = append(p.Variants, p.Variants[0])
					case "wrong owner":
						p.OwnerKind = "REVIEW"
					case "wrong payload":
						if owner == "IMPLEMENTATION" {
							p.Variants[0].Verification = &VerificationPurposeVariant{}
						} else {
							p.Variants[0].Implementation = &ImplementationPurposeVariant{}
						}
					case "invalid work":
						if owner == "IMPLEMENTATION" {
							p.Variants[0].Implementation.WorkSpec.CodexInstruction = ""
						} else {
							p.Variants[0].Verification.WorkSpec.CodexInstruction = ""
						}
					case "invalid verification":
						if owner == "IMPLEMENTATION" {
							p.Variants[0].Implementation.VerificationSpec.Waiver = nil
						} else {
							p.Variants[0].Verification.VerificationSpec.Waiver = nil
						}
					case "invalid target":
						if owner == "IMPLEMENTATION" {
							p.Variants[0].Implementation.RepositoryTargets[0].MemberVersion = 0
						} else {
							p.Variants[0].Verification.RepositoryTargets[0].MemberVersion = 0
						}
					case "wrong primary":
						if owner == "IMPLEMENTATION" {
							p.Variants[0].Implementation.PrimaryMemberID = "other"
						} else {
							p.Variants[0].Verification.PrimaryMemberID = "other"
						}
					case "hash mismatch":
						p.Variants[0].Summary = "changed without rehash"
					}
					if name != "hash mismatch" {
						p.SubmittedHash = p.ComputeSubmittedHash()
					}
					if err := p.Validate(source, p.RecoveryID, 1, p.AssessmentHash); !errors.Is(err, ErrIneligible) {
						t.Fatalf("invalid %s proposal accepted: %v", owner, err)
					}
				})
			}
		})
	}
}
