package git

import (
	"context"
	"fmt"
	"strings"
)

// ChangeType is the objective Git change kind between two commits.
type ChangeType string

const (
	ChangeAdded    ChangeType = "ADDED"
	ChangeModified ChangeType = "MODIFIED"
	ChangeDeleted  ChangeType = "DELETED"
	ChangeRenamed  ChangeType = "RENAMED"
)

// FileChange is one objective path-level Git fact. OldPath is populated only
// for RENAMED entries.
type FileChange struct {
	Change  ChangeType
	Path    string
	OldPath string
}

// ChangedFilesNamed returns the typed path-level changes between two commits
// with rename detection. The output is objective Git output: Bridge derives
// no semantics (staleness, relevance, importance) from it.
func (c *Client) ChangedFilesNamed(ctx context.Context, repoPath, fromCommit, toCommit string) ([]FileChange, error) {
	if strings.TrimSpace(fromCommit) == "" || strings.TrimSpace(toCommit) == "" {
		return nil, fmt.Errorf("named diff requires two commits")
	}
	// --name-status -z is deterministic NUL-delimited porcelain: status
	// letter (possibly score-bearing, e.g. R100), then NUL, then the path;
	// renames and copies carry the new path first, then the old path. -M
	// enables rename detection.
	out, err := c.run(ctx, repoPath, "named changes", "diff", "--name-status", "-z", "-M", fromCommit, toCommit, "--")
	if err != nil {
		return nil, err
	}
	return parseNameStatus(out), nil
}

func parseNameStatus(out []byte) []FileChange {
	fields := strings.Split(string(out), "\x00")
	changes := make([]FileChange, 0, len(fields)/2)
	for i := 0; i < len(fields); {
		status := strings.TrimSpace(fields[i])
		if status == "" {
			i++
			continue
		}
		letter := status[0]
		// Plain statuses (A/M/D/T) and score-bearing extended statuses
		// (R100, C75) consume one path; renames and copies consume a second.
		twoPaths := letter == 'R' || letter == 'C'
		if i+1 >= len(fields) || (twoPaths && i+2 >= len(fields)) {
			break
		}
		path := strings.TrimSpace(fields[i+1])
		change := ChangeModified
		switch letter {
		case 'A', 'C':
			change = ChangeAdded
		case 'D':
			change = ChangeDeleted
		case 'R':
			change = ChangeRenamed
		}
		i += 2
		if twoPaths {
			// Renames and copies list the old path first, then the new path.
			newPath := strings.TrimSpace(fields[i])
			i++
			changes = append(changes, FileChange{Change: change, Path: newPath, OldPath: path})
			continue
		}
		changes = append(changes, FileChange{Change: change, Path: path})
	}
	return changes
}
