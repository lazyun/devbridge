package workset

import (
	"errors"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"path/filepath"
	"reflect"
	"sort"
)

func (w RuntimeWorkset) validateTaskPermissions() error {
	p := w.TaskPermissions
	if p == nil {
		return nil
	}
	if w.ProtocolVersion != RuntimeProtocolVersion || len(w.Members) == 0 || p.TaskID != w.TaskID || p.ReviewID != w.ReviewID || p.RevisionID != w.TaskDefinitionRevisionID || p.ExecutionID != w.ExecutionID || p.RunID != w.RunID || p.WorksetHash != w.ContentHash {
		return errors.New("task sandbox owner differs from frozen Run")
	}
	if p.TrustedExecution != nil && p.TrustedExecution.ProjectID != w.ProjectID {
		return errors.New("project authorization owner differs from frozen Run")
	}
	base := filepath.Dir(p.ScratchRoot)
	if filepath.Base(p.ScratchRoot) != "scratch" || filepath.Base(base) != "run-"+runtimepermission.EnvironmentID(w.RunID) || filepath.Base(filepath.Dir(base)) != ".task-runtime" || filepath.Dir(filepath.Dir(base)) != filepath.Dir(w.Members[0].CanonicalRoot) {
		return errors.New("task sandbox is outside its Run namespace")
	}
	sources := map[string]RuntimeMemberBinding{}
	for _, m := range w.Members {
		sources[m.CanonicalRoot] = m
	}
	if p.Purpose == "IMPLEMENTATION" {
		expected := make([]runtimepermission.WriteGrant, 0)
		for _, m := range w.Members {
			if m.Write.Kind != "NONE" {
				expected = append(expected, runtimepermission.WriteGrant{Root: m.CanonicalRoot, Kind: m.Write.Kind, Paths: m.Write.Paths})
			}
		}
		if len(expected) != len(p.Writes) {
			return errors.New("task sandbox write scope differs from approved workset")
		}
		for i := range expected {
			if expected[i].Root != p.Writes[i].Root || expected[i].Kind != p.Writes[i].Kind || (len(expected[i].Paths) > 0 && !reflect.DeepEqual(expected[i].Paths, p.Writes[i].Paths)) {
				return errors.New("task sandbox write scope differs from approved workset")
			}
		}
	}
	if p.Purpose != "IMPLEMENTATION" {
		if !p.HasTrustedExecution() {
			if len(p.Copies) != len(w.Members) {
				return errors.New("verification copies must cover exact workset")
			}
			for _, copy := range p.Copies {
				m, ok := sources[copy.SourceRoot]
				if !ok || copy.Root != filepath.Join(base, "copy-"+runtimepermission.EnvironmentID(m.MemberID)) || copy.StartSnapshot != m.StartSnapshot {
					return errors.New("verification copy differs from frozen source")
				}
			}
		}
	}
	_, roots, writes := w.TaskPermissionBinding()
	return p.Validate(roots, writes)
}

// TaskPermissionBinding only projects a frozen policy; it performs no effects.
func (w RuntimeWorkset) TaskPermissionBinding() (cwd string, roots, writes []string) {
	for _, m := range w.Members {
		roots = append(roots, m.CanonicalRoot)
		if m.MemberID == w.PrimaryMemberID {
			cwd = m.CanonicalRoot
		}
		if m.Write.Kind != "NONE" {
			writes = append(writes, m.CanonicalRoot)
		}
	}
	if p := w.TaskPermissions; p != nil {
		if p.Purpose != "IMPLEMENTATION" {
			if p.HasTrustedExecution() {
				// Trusted verification runs in the original member worktrees.
				for _, m := range w.Members {
					if m.Write.Kind == "NONE" {
						writes = append(writes, m.CanonicalRoot)
					}
				}
			} else {
				writes = nil
				for _, copy := range p.Copies {
					roots = append(roots, copy.Root)
					writes = append(writes, copy.Root)
					if copy.SourceRoot == cwd {
						cwd = copy.Root
					}
				}
			}
		}
		roots = append(roots, p.ScratchRoot)
		writes = append(writes, p.ScratchRoot)
	}
	sort.Strings(roots)
	sort.Strings(writes)
	return
}
