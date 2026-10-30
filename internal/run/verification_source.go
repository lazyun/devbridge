package run

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/lazyun/devbridge/internal/workset"
)

// Matches checks persisted provenance against the immutable Run workset, not
// today's filesystem or a Task's latest workspace. Historical readback cannot
// acquire authority from a directory that has since moved or changed.
func (s *VerificationObservationSource) Matches(w workset.RuntimeWorkset, repositoryID, directory string) bool {
	if s == nil || w.Validate() != nil || s.RunID != w.RunID || s.RuntimeWorksetHash != w.ContentHash || s.RepositoryID != repositoryID || directory == "" || path.Clean(directory) != directory || strings.HasPrefix(directory, "/") || directory == ".." || strings.HasPrefix(directory, "../") || strings.ContainsAny(directory, "\\\x00") {
		return false
	}
	for _, member := range w.Members {
		if member.RepositoryID == repositoryID {
			root := member.CanonicalRoot
			p := w.TaskPermissions
			if p != nil && p.HasTrustedExecution() && p.Purpose == "VERIFICATION" && s.Kind == "COMMAND" && s.TaskPermissionHash == "" {
				return false
			}
			if s.TaskPermissionHash != "" {
				if p == nil || p.Purpose != "VERIFICATION" || s.Kind != "COMMAND" || s.TaskPermissionHash != p.ContentHash {
					return false
				}
				found := p.HasTrustedExecution()
				if !found {
					for _, copy := range p.Copies {
						if copy.SourceRoot == root {
							root = copy.Root
							found = true
							break
						}
					}
				}
				if !found {
					return false
				}
			}
			return s.MemberID == member.MemberID && s.MemberVersion == member.MemberVersion && s.CWD == filepath.Join(root, filepath.FromSlash(directory))
		}
	}
	return false
}

// VerificationCommandMatches does not treat a commandActions substring as an
// execution: a surrounding shell program may change directories or skip it.
// Exact POSIX shell wrappers retain the authored command's original semantics.
func VerificationCommandMatches(declared, observed string) bool {
	declared, observed = strings.TrimSpace(declared), strings.TrimSpace(observed)
	if declared == "" {
		return false
	}
	if declared == observed {
		return true
	}
	quoted := "'" + strings.ReplaceAll(declared, "'", "'\\''") + "'"
	for _, shell := range []string{"/bin/sh", "/bin/bash", "/bin/zsh", "sh", "bash", "zsh"} {
		for _, flag := range []string{"-c", "-lc"} {
			if observed == shell+" "+flag+" "+quoted {
				return true
			}
		}
	}
	return false
}
