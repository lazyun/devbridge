package upperworker

import (
	"context"
	"encoding/base64"
	"errors"
	"path"
	"strings"
	"sync"

	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
)

type boundFiles struct {
	mu        sync.RWMutex
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	mcp       MCP
	contracts contracts
	target    review.UpperTarget
	workspace string
	members   map[string]workset.Target
}

func newFiles(ctx context.Context, mcp MCP, contracts contracts, access review.UpperAccess) *boundFiles {
	ctx, cancel := context.WithCancel(ctx)
	out := &boundFiles{ctx: ctx, cancel: cancel, mcp: mcp, contracts: contracts, target: access.Target, workspace: access.WorkspaceID, members: map[string]workset.Target{}}
	for _, m := range workset.CloneTargets(access.RepositoryTargets) {
		out.members[m.MemberID] = m
	}
	return out
}
func (f *boundFiles) close() {
	f.cancel() // Interrupt in-flight scoped reads before waiting to revoke access.
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
}
func (f *boundFiles) Read(ctx context.Context, memberID, name string) (directfile.Result, error) {
	return f.read(ctx, "file_read", memberID, name)
}
func (f *boundFiles) List(ctx context.Context, memberID, name string) (directfile.Result, error) {
	return f.read(ctx, "file_list", memberID, name)
}
func (f *boundFiles) read(ctx context.Context, tool, memberID, name string) (out directfile.Result, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed || f.ctx.Err() != nil {
		return out, ErrReaderClosed
	}
	member, ok := f.members[memberID]
	if !ok || name == "" || len(name) > 4096 || strings.ContainsAny(name, "\\\x00") || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return out, errors.New("exact frozen member and clean relative path required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(f.ctx, cancel)
	defer stop()
	args := map[string]any{"reviewTarget": f.target, "repositoryId": member.RepositoryID, "memberId": member.MemberID, "memberVersion": member.MemberVersion, "path": name}
	if err = f.contracts.call(ctx, f.mcp, tool, args, &out); err != nil {
		return directfile.Result{}, err
	}
	if out.RepositoryID != member.RepositoryID || out.MemberID != member.MemberID || out.MemberVersion != member.MemberVersion || out.Path != name || out.WorkflowComplete || out.WorkspaceID != f.workspace || out.OriginCommit != member.OriginCommit || out.SourceKind != "MANAGED_WORKTREE" || !validID(out.WorktreeID) || out.StartSnapshot == "" {
		return directfile.Result{}, ErrResponse
	}
	if tool == "file_read" {
		content := []byte(out.Content)
		switch out.ContentEncoding {
		case "utf8", "":
		case "base64":
			content, err = base64.StdEncoding.DecodeString(out.Content)
		default:
			err = ErrResponse
		}
		if err != nil || len(content) > directfile.MaxReadBytes || int64(len(content)) != out.Size || out.EvidenceID == "" || !validDigest(out.Digest) || digest(content) != out.Digest {
			return directfile.Result{}, ErrResponse
		}
	}
	return out, nil
}
