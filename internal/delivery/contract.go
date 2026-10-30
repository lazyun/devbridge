// Package delivery describes durable member delivery facts, not authorization.
package delivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

type Member struct {
	RepositoryID   string `json:"repositoryId"`
	MemberID       string `json:"memberId"`
	MemberVersion  int    `json:"memberVersion"`
	WorktreeID     string `json:"worktreeId"`
	State          string `json:"state"`
	Commit         string `json:"commit,omitempty"`
	DevelopmentRef string `json:"developmentRef,omitempty"`
	ExpectedHead   string `json:"expectedHead,omitempty"`
	SourceHead     string `json:"sourceHead,omitempty"`
	Error          string `json:"error,omitempty"`
}
type Status struct {
	State     string   `json:"state"`
	RequestID string   `json:"requestId,omitempty"`
	Members   []Member `json:"members"`
}

// FeatureAreaPlan is the immutable, repository-qualified intent presented for
// explicit integration confirmation.  It is a plan, not a Git effect: callers
// must pass its exact digest back to the committing method.
type FeatureAreaPlan struct {
	PredecessorMergeRequestID string              `json:"predecessorMergeRequestId,omitempty"`
	CompletionTaskID          string              `json:"completionTaskId,omitempty"`
	Version                   int                 `json:"version"`
	ProjectID                 string              `json:"projectId"`
	FeatureAreaID             string              `json:"featureAreaId"`
	WorkspaceID               string              `json:"workspaceId"`
	Members                   []FeatureAreaMember `json:"members"`
	SHA256                    string              `json:"sha256"`
}

type FeatureAreaMember struct {
	RepositoryID       string   `json:"repositoryId"`
	MemberID           string   `json:"memberId"`
	MemberVersion      int      `json:"memberVersion"`
	WorktreeID         string   `json:"worktreeId"`
	SourcePath         string   `json:"sourcePath"`
	SourceBranch       string   `json:"sourceBranch"`
	SourceHead         string   `json:"sourceHead"`
	ExpectedDevHead    string   `json:"expectedDevHead"`
	DevelopmentRef     string   `json:"developmentRef"`
	DevelopmentCycleID string   `json:"developmentCycleId"`
	TargetPath         string   `json:"targetPath"`
	TaskCommits        []string `json:"taskCommits"`
}

func (p *FeatureAreaPlan) Canonicalize() error {
	p.Version = 1
	sort.Slice(p.Members, func(i, j int) bool { return p.Members[i].RepositoryID < p.Members[j].RepositoryID })
	for i := range p.Members {
		p.Members[i].TaskCommits = append([]string(nil), p.Members[i].TaskCommits...)
		sort.Strings(p.Members[i].TaskCommits)
	}
	p.SHA256 = ""
	b, err := json.Marshal(*p)
	if err != nil {
		return err
	}
	h := sha256.Sum256(b)
	p.SHA256 = hex.EncodeToString(h[:])
	return nil
}

func (p FeatureAreaPlan) Valid() bool {
	if p.PredecessorMergeRequestID != "" && p.CompletionTaskID == "" {
		return false
	}
	if p.Version != 1 || p.ProjectID == "" || p.FeatureAreaID == "" || p.WorkspaceID == "" || p.SHA256 == "" || len(p.Members) == 0 {
		return false
	}
	copy := p
	copy.Members = append([]FeatureAreaMember(nil), p.Members...)
	for i := range copy.Members {
		copy.Members[i].TaskCommits = append([]string(nil), p.Members[i].TaskCommits...)
	}
	want := p.SHA256
	if copy.Canonicalize() != nil || copy.SHA256 != want {
		return false
	}
	seen := map[string]bool{}
	for _, m := range p.Members {
		if m.RepositoryID == "" || m.MemberID == "" || m.MemberVersion < 1 || m.WorktreeID == "" || m.SourcePath == "" || m.SourceBranch == "" || m.SourceHead == "" || m.ExpectedDevHead == "" || m.DevelopmentRef == "" || m.DevelopmentCycleID == "" || m.TargetPath == "" || seen[m.RepositoryID] {
			return false
		}
		seen[m.RepositoryID] = true
	}
	return true
}
