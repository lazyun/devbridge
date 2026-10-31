package fileworker

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/workset"
	"path"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	ActionCreate  = "CREATE"
	ActionReplace = "REPLACE"
	ActionDelete  = "DELETE"
	ActionMove    = "MOVE"
)

// Mutation never accepts a target, epoch, worker identity or completion flag.
// Persist each caller-owned request ID before Write; this client never replays.
type Mutation struct{ RequestID, Action, MemberID, Path, ExpectedDigest, ContentEncoding, Content, Destination string }

// MutationRecord preserves the exact dispatched wire input for caller diagnosis,
// not retry authority. A nil Response means the effect is not proven to this client.
type MutationRecord struct {
	Input    map[string]any
	Response *directfile.Result
}
type boundFiles struct {
	mu               sync.Mutex // Serialize all file calls, mutation IDs, and uncertainty latch.
	ctx              context.Context
	cancel           context.CancelFunc
	closed, poisoned bool
	mcp              MCP
	contracts        contracts
	claim            directfile.WorkerClaimInput
	members          map[string]directfile.MemberGrant
	used             map[string]bool
	history          []MutationRecord
}

func newFiles(ctx context.Context, mcp MCP, contracts contracts, claim directfile.WorkerClaimInput, a directfile.State) *boundFiles {
	ctx, cancel := context.WithCancel(ctx)
	f := &boundFiles{ctx: ctx, cancel: cancel, mcp: mcp, contracts: contracts, claim: claim, members: map[string]directfile.MemberGrant{}, used: map[string]bool{}}
	for _, m := range copyValue(a.RepositoryTargets) {
		f.members[m.MemberID] = m
	}
	return f
}
func (f *boundFiles) close()        { f.cancel(); f.mu.Lock(); f.closed = true; f.mu.Unlock() }
func (f *boundFiles) unknown() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.poisoned }
func (f *boundFiles) records() []MutationRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return copyValue(f.history)
}
func cleanPath(s string, dir bool) bool {
	return s != "" && len(s) <= directfile.MaxPathBytes && !strings.ContainsAny(s, "\\\x00") && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../") && (dir || s != ".") && s != ".git" && !strings.HasPrefix(s, ".git/")
}
func writeAllowed(t workset.Target, s string) bool {
	if !cleanPath(s, false) {
		return false
	}
	if t.Write.Kind == "REPOSITORY" {
		return true
	}
	if t.Write.Kind != "PATHS" {
		return false
	}
	for _, p := range t.Write.Paths {
		if s == p || strings.HasPrefix(s, p+"/") {
			return true
		}
	}
	return false
}
func (f *boundFiles) ready() error {
	if f.poisoned {
		return ErrMutationUnknown
	}
	if f.closed || f.ctx.Err() != nil {
		return ErrFilesClosed
	}
	return nil
}
func (f *boundFiles) context(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(f.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}
func (f *boundFiles) args(m directfile.MemberGrant, name string) map[string]any {
	return map[string]any{"aiSessionId": f.claim.AISessionID, "featureAreaId": f.claim.FeatureAreaID, "workflowId": f.claim.WorkflowID, "repositoryId": m.RepositoryID, "memberId": m.MemberID, "memberVersion": m.MemberVersion, "runtimeEpoch": f.claim.RuntimeEpoch, "grantGeneration": f.claim.GrantGeneration, "path": name}
}
func (f *boundFiles) Read(ctx context.Context, member, path string) (directfile.Result, error) {
	return f.read(ctx, "file_read", member, path)
}
func (f *boundFiles) List(ctx context.Context, member, path string) (directfile.Result, error) {
	return f.read(ctx, "file_list", member, path)
}
func (f *boundFiles) read(ctx context.Context, tool, member, name string) (out directfile.Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err = f.ready(); err != nil {
		return out, err
	}
	m, ok := f.members[member]
	if !ok || !cleanPath(name, tool == "file_list") {
		return out, errors.New("exact member and clean relative path required")
	}
	ctx, done := f.context(ctx)
	defer done()
	if err = f.contracts.call(ctx, f.mcp, tool, f.args(m, name), &out); err != nil {
		return directfile.Result{}, err
	}
	if err = validateFile(out, m, name); err != nil {
		return directfile.Result{}, err
	}
	if tool == "file_read" {
		data, e := contentBytes(out.Content, out.ContentEncoding, directfile.MaxReadBytes)
		if e != nil || int64(len(data)) != out.Size || digest(data) != out.Digest {
			return directfile.Result{}, ErrResponse
		}
	} else {
		if len(out.Entries) > directfile.MaxListEntries {
			return directfile.Result{}, ErrResponse
		}
		seen := map[string]bool{}
		for _, e := range out.Entries {
			if !cleanPath(e.Name, false) || strings.Contains(e.Name, "/") || seen[e.Name] || (e.Type != "file" && e.Type != "directory" && e.Type != "symlink") {
				return directfile.Result{}, ErrResponse
			}
			seen[e.Name] = true
		}
	}
	return out, nil
}
func contentBytes(content, encoding string, max int) ([]byte, error) {
	if len(content) > directfile.MaxBase64EncodedBytes {
		return nil, ErrResponse
	}
	data := []byte(content)
	switch encoding {
	case "", "utf8":
		if !utf8.ValidString(content) {
			return nil, ErrResponse
		}
	case "base64":
		var err error
		data, err = base64.StdEncoding.Strict().DecodeString(content)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrResponse
	}
	if len(data) > max {
		return nil, ErrResponse
	}
	return data, nil
}
func (f *boundFiles) Write(ctx context.Context, in Mutation) (out directfile.Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err = f.ready(); err != nil {
		return out, err
	}
	m, ok := f.members[in.MemberID]
	if !ok || !validID(in.RequestID) || !writeAllowed(m.Target, in.Path) {
		return out, errors.New("exact writable member, path and caller request required")
	}
	if f.used[in.RequestID] {
		return out, ErrMutationRepeated
	}
	args := f.args(m, in.Path)
	args["action"] = in.Action
	args["protocolVersion"] = 2
	args["requestId"] = in.RequestID
	args["workerClaim"] = f.claim.WorkerClaimIdentity
	var expected []byte
	responsePath := in.Path
	switch in.Action {
	case ActionCreate, ActionReplace:
		if in.Destination != "" || (in.Action == ActionCreate && in.ExpectedDigest != "") {
			return out, errors.New("invalid content mutation fields")
		}
		expected, err = contentBytes(in.Content, in.ContentEncoding, directfile.MaxWriteBytes)
		if err != nil {
			return out, err
		}
		args["content"] = in.Content
		if in.ContentEncoding != "" {
			args["contentEncoding"] = in.ContentEncoding
		}
	case ActionDelete:
		if in.Content != "" || in.ContentEncoding != "" || in.Destination != "" {
			return out, errors.New("delete carries no content/destination")
		}
	case ActionMove:
		if in.Content != "" || in.ContentEncoding != "" || !writeAllowed(m.Target, in.Destination) || in.Destination == in.Path {
			return out, errors.New("exact same-member move destination required")
		}
		args["destination"] = in.Destination
		args["destinationRepositoryId"] = m.RepositoryID
		args["destinationMemberId"] = m.MemberID
		responsePath = in.Destination
	default:
		return out, errors.New("unsupported mutation action")
	}
	if in.Action != ActionCreate {
		if !validDigest(in.ExpectedDigest) {
			return out, errors.New("observed source digest required")
		}
		args["expectedDigest"] = in.ExpectedDigest
	}
	if err = f.contracts.validate("file_write", args); err != nil {
		return out, err
	}
	ctx, done := f.context(ctx)
	defer done()
	if err = ctx.Err(); err != nil {
		return out, err
	}
	dispatched, settled := false, false
	defer func() {
		if dispatched && !settled {
			f.poisoned = true
		}
	}()
	f.used[in.RequestID] = true
	f.history = append(f.history, MutationRecord{Input: copyValue(args)})
	index := len(f.history) - 1
	// Once the transport is invoked, every error is conservative uncertainty.
	// Core may have committed an intent or effect even for an error response.
	dispatched = true
	response, callErr := f.mcp.CallTool(ctx, toolCall("file_write", args))
	err = callErr
	if err == nil {
		err = decode(response, &out)
	}
	if err == nil {
		err = validateFile(out, m, responsePath)
	}
	if err == nil {
		switch in.Action {
		case ActionCreate, ActionReplace:
			if out.Digest != digest(expected) || out.Size != int64(len(expected)) {
				err = ErrResponse
			}
		case ActionDelete:
			if out.Digest != "ABSENT" {
				err = ErrResponse
			}
		case ActionMove:
			if out.Digest != in.ExpectedDigest {
				err = ErrResponse
			}
		}
	}
	if err != nil {
		f.poisoned = true
		return directfile.Result{}, errors.Join(ErrMutationUnknown, err)
	}
	settled = true
	value := copyValue(out)
	f.history[index].Response = &value
	return out, nil
}

// Plain DIRECT_FILE replies currently omit member provenance/evidence IDs.
// Bind requests exactly, verify returned path/bytes, and reject contradictory
// optional metadata; do not manufacture UPPER-style provenance proof.
func validateFile(r directfile.Result, m directfile.MemberGrant, name string) error {
	if r.Path != name || r.WorkflowComplete || r.Size < 0 || r.Size > directfile.MaxReadBytes {
		return ErrResponse
	}
	if (r.RepositoryID != "" && r.RepositoryID != m.RepositoryID) || (r.MemberID != "" && r.MemberID != m.MemberID) || (r.MemberVersion != 0 && r.MemberVersion != m.MemberVersion) || (r.WorkspaceID != "" && r.WorkspaceID != m.WorkspaceID) || (r.WorktreeID != "" && r.WorktreeID != m.WorktreeID) || (r.OriginCommit != "" && r.OriginCommit != m.OriginCommit) || (r.StartSnapshot != "" && r.StartSnapshot != m.StartSnapshot) || (r.SourceKind != "" && r.SourceKind != "MANAGED_WORKTREE") {
		return ErrResponse
	}
	return nil
}
