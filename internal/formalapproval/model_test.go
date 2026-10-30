package formalapproval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

func approvalSnapshot(t *testing.T) BusinessSnapshot {
	t.Helper()
	definition := task.Definition{Version: 2, ProjectID: "project", FeatureAreaID: "area", ReviewApprovalID: "decision", PrimaryMemberID: "member", RepositoryTargets: []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: workset.WriteScope{Kind: "PATHS", Paths: []string{"src"}}}}, WorkSpec: task.WorkSpec{ImplementationMode: task.ImplementationCodingAgent, Goal: "update", CodexInstruction: "preserve exact\ntext", ExecutionScope: task.ExecutionScope{Kind: task.ExecutionScopeTargets}}, VerificationSpec: task.VerificationSpec{Mode: task.VerificationModeRun, Scope: "all", CodexInstruction: "check", AcceptanceCriteria: []string{"passes"}, Checks: []task.VerificationCheck{{Kind: task.VerificationCheckCustom, Instruction: "verify", Required: true, RepositoryID: "repo", WorkingDirectory: ".", RepositoryIDs: []string{"repo"}}}}}
	raw, hash, err := task.EncodeDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition, err = task.DecodeDefinition(raw, hash)
	if err != nil {
		t.Fatal(err)
	}
	parent := definition
	parent.ReviewApprovalID = "previous"
	parentRaw, parentHash, err := task.EncodeDefinition(parent)
	if err != nil {
		t.Fatal(err)
	}
	prepared := confirmation.PrepareInput{AISessionID: "session", RequestID: "prepare", Purpose: confirmation.Execution, Action: "APPROVE", Execution: &confirmation.ExecutionBody{Action: "APPROVE", TaskID: "task", FeatureAreaID: "area", ReviewID: "review", DecisionID: "decision", TaskDefinitionRevisionID: "parent"}, Variants: []confirmation.Variant{{ID: "chosen", ImplementationMode: task.ImplementationCodingAgent, PrimaryMemberID: definition.PrimaryMemberID, RepositoryTargets: definition.RepositoryTargets, WorkSpec: &definition.WorkSpec, VerificationSpec: &definition.VerificationSpec}}}
	prepareRaw, _ := json.Marshal(prepared)
	f := PrepareFence{AISessionID: "session", IntentID: "intent", ProjectID: "project", TaskID: "task", ReviewID: "review", DecisionID: "decision", DecisionVersion: HashBytes([]byte("independently-read-token")), ParentRevisionID: "parent", ParentRevisionHash: parentHash, ContentHash: HashBytes(prepareRaw), IntentVersion: 1, FrozenPrepareJSON: prepareRaw, ParentDefinitionJSON: json.RawMessage(parentRaw), Schema: SchemaReference{"TASK_DEFINITION", 2, "FORMAL_TASK_DEFINITION"}}
	c := confirmation.CommitInput{AISessionID: f.AISessionID, IntentID: f.IntentID, Version: f.IntentVersion, Purpose: confirmation.Execution, Action: "APPROVE", RequestID: "commit", ContentHash: f.ContentHash, VariantID: "chosen"}
	original, _ := json.Marshal(c)
	lineage := Lineage{ProjectID: f.ProjectID, TaskID: f.TaskID, ReviewID: f.ReviewID, DecisionID: f.DecisionID, DecisionVersion: f.DecisionVersion, IntentID: f.IntentID, IntentVersion: f.IntentVersion, VariantID: c.VariantID, CommittedResultID: f.DecisionID, TaskDefinitionRevisionID: "new-revision"}
	return BusinessSnapshot{Fence: f, Commit: c, TypedPayloadHash: HashCommit(c), SubmittedHash: HashBytes([]byte("original raw field presence")), OriginalRequestJSON: original, ApprovalStepRequestID: "confirmation_" + hashValue([]string{f.IntentID, "APPROVE"})[:32], ResultID: f.DecisionID, RevisionID: "new-revision", RevisionHash: hash, DefinitionJSON: json.RawMessage(raw), FrozenLineage: lineage, ResultLineage: lineage, Core: CoreSnapshot{ProjectID: f.ProjectID, TaskID: f.TaskID, TaskStatus: "ACTIVE", TaskPhase: string(task.PhaseReviewApprovalPending), CurrentRevisionID: f.ParentRevisionID, CurrentReviewID: f.ReviewID, ReviewID: f.ReviewID, ReviewProjectID: f.ProjectID, ReviewTaskID: f.TaskID, ReviewRevisionID: f.ParentRevisionID, ReviewOutcome: "VALID", DecisionID: f.DecisionID, DecisionState: "PENDING", CurrentConfirmationIntentID: f.IntentID, CurrentConfirmationSessionID: f.AISessionID, CurrentConfirmationVersionHash: hashValue([]any{f.IntentVersion, f.ContentHash})}}
}

func TestApprovalArchivePreservesHashDomainsAndPriorRole(t *testing.T) {
	s := approvalSnapshot(t)
	r, err := NewReceipt(s)
	if err != nil {
		t.Fatal(err)
	}
	if r.Snapshot.SubmittedHash == HashBytes(s.OriginalRequestJSON) {
		t.Fatal("fixture must retain an independent original submission hash")
	}
	raw, _ := json.Marshal(r)
	restored, err := DecodeReceipt(raw, HashBytes(raw))
	if err != nil {
		t.Fatal(err)
	}
	prior, err := restored.AsPriorApproval()
	if err != nil || prior.Role != PriorReceiptRole || prior.Receipt.Role != CurrentReceiptRole {
		t.Fatalf("prior role: %+v %v", prior, err)
	}
	prior.Receipt.Snapshot.DefinitionJSON[0] = '!'
	if restored.Validate() != nil {
		t.Fatal("prior read mutated original archive")
	}
	id := ReadIdentity{AISessionID: s.Fence.AISessionID, IntentID: s.Fence.IntentID, RequestID: s.Commit.RequestID, IntentVersion: s.Fence.IntentVersion, ContentHash: s.Fence.ContentHash, TypedPayloadHash: s.TypedPayloadHash, SubmittedHash: s.SubmittedHash}
	if err := id.MatchesReceipt(restored); err != nil {
		t.Fatal(err)
	}
	id.SubmittedHash = id.TypedPayloadHash
	if !errors.Is(id.MatchesReceipt(restored), ErrConflict) {
		t.Fatal("hash domains were interchangeable")
	}
}

func TestApprovalArchiveRejectsRehashedAuthorityMutation(t *testing.T) {
	cases := map[string]func(*BusinessSnapshot){
		"result-owner": func(s *BusinessSnapshot) {
			s.ResultID = "other"
			s.FrozenLineage.CommittedResultID = "other"
			s.ResultLineage = s.FrozenLineage
		},
		"lineage":          func(s *BusinessSnapshot) { s.ResultLineage.DecisionVersion = HashBytes([]byte("other")) },
		"review-owner":     func(s *BusinessSnapshot) { s.Core.ReviewRevisionID = "other" },
		"pending-decision": func(s *BusinessSnapshot) { s.Core.DecisionState = "CONSUMED" },
		"runtime-selection": func(s *BusinessSnapshot) {
			s.Commit.Selection.RuntimeConfig = &codexruntime.NextRunRuntimeConfig{Model: "model"}
			s.TypedPayloadHash = HashCommit(s.Commit)
		},
		"same-revision": func(s *BusinessSnapshot) {
			s.RevisionID = s.Fence.ParentRevisionID
			s.FrozenLineage.TaskDefinitionRevisionID = s.RevisionID
			s.ResultLineage = s.FrozenLineage
		},
		"subrequest":     func(s *BusinessSnapshot) { s.ApprovalStepRequestID = "other" },
		"variant":        func(s *BusinessSnapshot) { s.Commit.VariantID = "other"; s.TypedPayloadHash = HashCommit(s.Commit) },
		"unknown-schema": func(s *BusinessSnapshot) { s.Fence.Schema.FactSchemaID = "other" },
		"definition-instruction": func(s *BusinessSnapshot) {
			var d task.Definition
			_ = json.Unmarshal(s.DefinitionJSON, &d)
			d.WorkSpec.CodexInstruction = "changed"
			raw, hash, _ := task.EncodeDefinition(d)
			s.DefinitionJSON = json.RawMessage(raw)
			s.RevisionHash = hash
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := approvalSnapshot(t)
			mutate(&s)
			if _, err := NewReceipt(s); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("invalid approval accepted: %v", err)
			}
		})
	}
}

func TestApprovalArchiveRejectsAmbiguousJSONAfterRehash(t *testing.T) {
	r, err := NewReceipt(approvalSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	cases := map[string]string{
		"duplicate":     strings.Replace(string(raw), `"role":"CURRENT_FORMAL_COMMIT_RECEIPT"`, `"role":"PRIOR_APPROVAL_RECEIPT","role":"CURRENT_FORMAL_COMMIT_RECEIPT"`, 1),
		"case-fold":     strings.Replace(string(raw), `"role":`, `"ROLE":`, 1),
		"unknown":       strings.Replace(string(raw), `"role":`, `"platform":{},"role":`, 1),
		"unicode-alias": strings.Replace(string(raw), `"role":`, `"\u0072ole":`, 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeReceipt([]byte(raw), HashBytes([]byte(raw))); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("ambiguous JSON accepted: %v", err)
			}
		})
	}
}

func TestApprovalSchemaCatalogHasExactFiniteKey(t *testing.T) {
	ref := SchemaReference{"TASK_DEFINITION", 2, "FORMAL_TASK_DEFINITION"}
	entry, err := LookupSchema(ref, "EXECUTION", "APPROVE")
	if err != nil || entry.State != "ACTIVE" {
		t.Fatal(entry, err)
	}
	for _, change := range []struct {
		ref             SchemaReference
		purpose, action string
	}{{ref, "TASK", "APPROVE"}, {ref, "EXECUTION", "START_APPROVED"}, {SchemaReference{"TASK_DEFINITION", 1, "FORMAL_TASK_DEFINITION"}, "EXECUTION", "APPROVE"}, {SchemaReference{"TASK_DEFINITION", 2, "OTHER"}, "EXECUTION", "APPROVE"}} {
		if _, err := LookupSchema(change.ref, change.purpose, change.action); !errors.Is(err, ErrConflict) {
			t.Fatal("unregistered decoder accepted")
		}
	}
}
