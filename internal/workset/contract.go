// Package workset defines explicit operation authorization independently of
// Project registration and Workspace resource membership.
package workset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

const Version = 1

type WriteScope struct {
	Kind  string   `json:"kind"` // NONE, PATHS, REPOSITORY
	Paths []string `json:"paths,omitempty"`
}

type Target struct {
	RepositoryID       string `json:"repositoryId"`
	MemberID           string `json:"memberId"`
	MemberVersion      int    `json:"memberVersion"`
	OriginCommit       string `json:"originCommit"`
	OriginRef          string `json:"originRef,omitempty"`
	DevelopmentCycleID string `json:"developmentCycleId,omitempty"`
	ObservedCommit     string `json:"observedCommit,omitempty"`
	Read               bool   `json:"read"`
	// ReadScope is reserved for Project Inspection v2. Existing v1 callers
	// leave it empty, retaining their historical serialized material.
	ReadScope ReadScope  `json:"readScope,omitempty"`
	Write     WriteScope `json:"write"`
}

type Set struct {
	Version     int      `json:"version"`
	WorkspaceID string   `json:"workspaceId"`
	Targets     []Target `json:"targets"`
}

func ValidCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}
func ValidateTargets(targets []Target) error {
	if len(targets) == 0 || len(targets) > 128 {
		return errors.New("exact nonempty bounded repository target set required")
	}
	repos, members := map[string]bool{}, map[string]bool{}
	for _, t := range targets {
		if t.RepositoryID == "" || t.MemberID == "" || t.MemberVersion < 1 || !ValidCommit(t.OriginCommit) || !t.Read {
			return errors.New("target requires repository, immutable member, origin commit and explicit read authorization")
		}
		if repos[t.RepositoryID] || members[t.MemberID] {
			return errors.New("duplicate repository or member identity")
		}
		repos[t.RepositoryID] = true
		members[t.MemberID] = true
		if t.ObservedCommit != "" && !ValidCommit(t.ObservedCommit) {
			return errors.New("invalid observed commit")
		}
		switch t.Write.Kind {
		case "NONE", "REPOSITORY":
			if len(t.Write.Paths) != 0 {
				return errors.New("non-path scope must not contain paths")
			}
		case "PATHS":
			if len(t.Write.Paths) == 0 || len(t.Write.Paths) > 128 {
				return errors.New("bounded write paths required")
			}
			seen := map[string]bool{}
			for _, p := range t.Write.Paths {
				if p == "" || p == "." || len(p) > 4096 || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || seen[p] {
					return fmt.Errorf("invalid repository-relative write path %q", p)
				}
				seen[p] = true
			}
		default:
			return errors.New("write scope must be NONE, PATHS or REPOSITORY")
		}
	}
	return nil
}
func (s Set) Validate() error {
	if s.Version != Version || s.WorkspaceID == "" {
		return errors.New("unsupported or missing exact workspace workset")
	}
	return ValidateTargets(s.Targets)
}
func CloneTargets(v []Target) []Target {
	if v == nil {
		return nil
	}
	out := append([]Target{}, v...)
	for i := range out {
		out[i].ReadScope.Paths = append([]string(nil), v[i].ReadScope.Paths...)
		out[i].Write.Paths = append([]string(nil), v[i].Write.Paths...)
	}
	return out
}

// Fingerprint freezes identities and permissions; it never includes prompts.
func (s Set) Fingerprint() (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	s.Targets = CloneTargets(s.Targets)
	sort.Slice(s.Targets, func(i, j int) bool { return s.Targets[i].RepositoryID < s.Targets[j].RepositoryID })
	for i := range s.Targets {
		sort.Strings(s.Targets[i].Write.Paths)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
