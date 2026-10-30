package recoverystatus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	gitrepo "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/workset"
)

type WorktreeObservation struct {
	RuntimeAlias   string   `json:"runtimeAlias"`
	RepositoryID   string   `json:"repositoryId"`
	MemberID       string   `json:"memberId"`
	WorktreeID     string   `json:"worktreeId"`
	CanonicalRoot  string   `json:"canonicalRoot"`
	RootIdentity   string   `json:"rootIdentity"`
	HEAD           string   `json:"head"`
	Snapshot       string   `json:"snapshot"`
	DiffDigest     string   `json:"diffDigest"`
	MetadataDigest string   `json:"metadataDigest"`
	Branch         string   `json:"branch"`
	ChangedPaths   []string `json:"changedPaths"`
	Available      bool     `json:"available"`
	State          string   `json:"state"`
	Error          string   `json:"error,omitempty"`
}

func sortStrings(v []string) { sort.Strings(v) }
func RootIdentity(root string) (string, string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return canonical, "", err
	}
	if !info.IsDir() || canonical == string(filepath.Separator) {
		return canonical, "", errors.New("invalid managed root")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return canonical, "", errors.New("root identity unavailable")
	}
	return canonical, digest([]byte(fmt.Sprintf("%s\x00%d\x00%d", canonical, st.Dev, st.Ino))), nil
}

// ObserveWorkset retains observations for every member even when one root is
// missing. Snapshot uses Git's private index; it never resets source files.
func ObserveWorkset(ctx context.Context, w workset.RuntimeWorkset) ([]WorktreeObservation, error) {
	out := make([]WorktreeObservation, 0, len(w.Members))
	var failures []error
	if len(w.Members) == 0 {
		return out, errors.New("empty recovery workset")
	}
	for _, m := range w.Members {
		o, e := observeMember(ctx, m)
		out = append(out, o)
		if e != nil {
			failures = append(failures, e)
		}
	}
	return out, errors.Join(failures...)
}
func observeMember(ctx context.Context, m workset.RuntimeMemberBinding) (o WorktreeObservation, err error) {
	o = WorktreeObservation{RuntimeAlias: m.RepositoryAlias, RepositoryID: m.RepositoryID, MemberID: m.MemberID, WorktreeID: m.WorktreeID, CanonicalRoot: m.CanonicalRoot, ChangedPaths: []string{}, State: "UNKNOWN"}
	defer func() {
		if err != nil {
			o.Error = err.Error()
		}
	}()
	root, id, e := RootIdentity(m.CanonicalRoot)
	if e != nil {
		return o, e
	}
	o.CanonicalRoot, o.RootIdentity, o.Available = root, id, true
	if root != m.CanonicalRoot || id != m.RootFileIdentity || id != m.RuntimeRootIdentity {
		o.State = "SCOPE_CONFLICT"
		return o, errors.New("managed root identity changed")
	}
	c := gitrepo.NewClient(gitrepo.Config{})
	if o.HEAD, err = c.ResolveHEAD(ctx, root); err != nil {
		return o, err
	}
	if o.Snapshot, err = c.Snapshot(ctx, root); err != nil {
		return o, err
	}
	if o.Branch, err = c.CurrentBranch(ctx, root); err != nil {
		return o, err
	}
	if o.ChangedPaths, err = c.ChangedFilesBetween(ctx, root, m.StartSnapshot, o.Snapshot); err != nil {
		return o, err
	}
	diff, e := c.DiffSnapshots(ctx, root, m.StartSnapshot, o.Snapshot)
	if e != nil {
		return o, e
	}
	o.DiffDigest = digest([]byte(diff))
	// Inspect owned-worktree metadata and shared security configuration separately:
	// snapshots do not expose staging-only changes. HEAD and Branch above bind the
	// owned branch semantically. Shared refs/packed-refs are deliberately excluded:
	// another FeatureArea may create a branch or Git may repack refs without changing
	// this worktree. Also exclude our private-index object writes and transient locks.
	metadata := map[string]string{}
	for _, name := range []string{"HEAD", "index", "config", "config.worktree"} {
		cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--git-path", name)
		raw, e := cmd.Output()
		if e != nil {
			return o, e
		}
		p := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		e = filepath.WalkDir(p, func(path string, d os.DirEntry, walkErr error) error {
			if os.IsNotExist(walkErr) {
				metadata[name] = "ABSENT"
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(path, ".lock") {
				return nil
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			relative, _ := filepath.Rel(p, path)
			metadata[name+"/"+relative] = digest(b)
			return nil
		})
		if e != nil {
			return o, e
		}
	}
	raw, _ := json.Marshal(metadata)
	o.MetadataDigest = digest(raw)
	_, endID, e := RootIdentity(root)
	if e != nil || endID != id {
		return o, errors.New("root changed during observation")
	}
	o.State = "OBSERVED"
	return o, nil
}
func ObservationsHash(v []WorktreeObservation) string { b, _ := json.Marshal(v); return digest(b) }
