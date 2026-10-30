package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/artifact"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/run"
)

type ArtifactFilesystem interface {
	Write(string, artifact.Kind, []byte) (artifact.Artifact, error)
}
type ArtifactMetadataStore interface {
	Create(context.Context, artifact.Artifact) error
}
type EvidenceStore interface {
	Save(context.Context, evidence.GitEvidence) error
}
type ResultRecorder interface {
	Record(context.Context, string, evidence.AgentClaim, evidence.GitEvidence) (run.Result, error)
}

type DurableResultRecorder struct {
	Filesystem ArtifactFilesystem
	Artifacts  ArtifactMetadataStore
	Evidence   EvidenceStore
	Now        func() time.Time
}

func (r *DurableResultRecorder) Record(ctx context.Context, runID string, claim evidence.AgentClaim, gitEvidence evidence.GitEvidence) (run.Result, error) {
	if r == nil || r.Filesystem == nil || r.Artifacts == nil || r.Evidence == nil {
		return run.Result{}, errors.New("durable result dependencies are required")
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	agentArtifact, err := r.Filesystem.Write(runID, artifact.KindAgentMessage, []byte(claim.Message))
	if err != nil {
		return run.Result{}, fmt.Errorf("store agent message bytes: %w", err)
	}
	if err := r.Artifacts.Create(ctx, agentArtifact); err != nil {
		return run.Result{}, fmt.Errorf("store agent message metadata: %w", err)
	}
	diffArtifact, err := r.Filesystem.Write(runID, artifact.KindGitDiff, []byte(gitEvidence.Diff))
	if err != nil {
		return run.Result{}, fmt.Errorf("store Git diff bytes: %w", err)
	}
	if err := r.Artifacts.Create(ctx, diffArtifact); err != nil {
		return run.Result{}, fmt.Errorf("store Git diff metadata: %w", err)
	}
	gitEvidence.RunID = runID
	gitEvidence.DiffArtifactID = diffArtifact.ID
	gitEvidence.CreatedAt = now().UTC()
	if err := r.Evidence.Save(ctx, gitEvidence); err != nil {
		return run.Result{}, fmt.Errorf("store Git evidence: %w", err)
	}
	return run.Result{AgentClaim: claim, GitEvidence: gitEvidence, Artifacts: evidence.Artifacts(claim, gitEvidence)}, nil
}

// RecordAgentPolicy persists exact prospective policy bytes before any adapter
// launch. Existing diagnostic artifacts require no schema or history migration.
func (r *DurableResultRecorder) RecordAgentPolicy(ctx context.Context, runID string, policy agentpolicy.Envelope) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.RunID != runID {
		return errors.New("cooperative policy Run mismatch")
	}
	return r.recordAgentDiagnostic(ctx, runID, "AGENT_POLICY_ENVELOPE", policy)
}

func (r *DurableResultRecorder) RecordAgentPolicyReceipt(ctx context.Context, runID string, receipt agentpolicy.LaunchReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if receipt.RunID != runID {
		return errors.New("cooperative receipt Run mismatch")
	}
	return r.recordAgentDiagnostic(ctx, runID, "AGENT_POLICY_LAUNCH_RECEIPT", receipt)
}

func (r *DurableResultRecorder) recordAgentDiagnostic(ctx context.Context, runID, kind string, value any) error {
	if r == nil || r.Filesystem == nil || r.Artifacts == nil {
		return errors.New("durable policy artifact dependencies are required")
	}
	raw, err := json.Marshal(struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	}{Type: kind, Value: value})
	if err != nil {
		return err
	}
	stored, err := r.Filesystem.Write(runID, artifact.KindDiagnostic, raw)
	if err != nil {
		return fmt.Errorf("store agent policy bytes: %w", err)
	}
	if err = r.Artifacts.Create(ctx, stored); err != nil {
		return fmt.Errorf("store agent policy metadata: %w", err)
	}
	return nil
}
