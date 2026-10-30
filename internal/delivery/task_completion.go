package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// TaskCompletionPlan freezes the verified source and every development target
// before the user confirms acceptance and integration together. Commit IDs
// created afterwards must come from that exact Task's delivery receipt.
type TaskCompletionPlan struct {
	ProjectID     string                 `json:"projectId"`
	FeatureAreaID string                 `json:"featureAreaId"`
	TaskID        string                 `json:"taskId"`
	ExecutionID   string                 `json:"executionId"`
	RevisionID    string                 `json:"taskDefinitionRevisionId"`
	WorkspaceID   string                 `json:"workspaceId"`
	Members       []TaskCompletionMember `json:"members"`
	SHA256        string                 `json:"sha256"`
}

type TaskCompletionMember struct {
	FeatureAreaMember
	VerifiedSnapshot string `json:"verifiedSnapshot"`
	RepositoryName   string `json:"repositoryName"`
}

func (p *TaskCompletionPlan) Canonicalize() error {
	sort.Slice(p.Members, func(i, j int) bool { return p.Members[i].RepositoryID < p.Members[j].RepositoryID })
	for i := range p.Members {
		p.Members[i].TaskCommits = append([]string{}, p.Members[i].TaskCommits...)
		sort.Strings(p.Members[i].TaskCommits)
	}
	p.SHA256 = ""
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	p.SHA256 = hex.EncodeToString(sum[:])
	return nil
}

func (p TaskCompletionPlan) Valid() bool {
	if p.ProjectID == "" || p.FeatureAreaID == "" || p.TaskID == "" || p.ExecutionID == "" || p.RevisionID == "" || p.WorkspaceID == "" || len(p.Members) == 0 || len(p.Members) > 128 || p.SHA256 == "" {
		return false
	}
	copy := p
	copy.Members = append([]TaskCompletionMember(nil), p.Members...)
	if copy.Canonicalize() != nil || copy.SHA256 != p.SHA256 {
		return false
	}
	seen := map[string]bool{}
	for _, m := range p.Members {
		if m.RepositoryID == "" || seen[m.RepositoryID] || m.MemberID == "" || m.MemberVersion < 1 || m.WorktreeID == "" || m.SourcePath == "" || m.SourceBranch == "" || m.SourceHead == "" || m.VerifiedSnapshot == "" || m.DevelopmentRef == "" || m.DevelopmentCycleID == "" || m.TargetPath == "" || m.ExpectedDevHead == "" {
			return false
		}
		seen[m.RepositoryID] = true
	}
	return true
}

type TaskCompletionStatus struct {
	Plan           TaskCompletionPlan `json:"plan"`
	State          string             `json:"state"`
	MergeRequestID string             `json:"mergeRequestId,omitempty"`
	Error          string             `json:"error,omitempty"`
}
