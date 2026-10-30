package runtimepermission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const TaskPermissionVersion = "TASK_SANDBOX_V1"

func EnvironmentID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

// TaskPermissions is a Core-derived dispatch policy, never an Agent request or
// a command-prefix allowlist. The execution checkpoint retains its exact hash.
// Ordinary commands execute inside the native sandbox; no native approval may
// expand it. A changed scope requires a new formal execution.
type TaskPermissions struct {
	Version       string             `json:"version"`
	TaskID        string             `json:"taskId,omitempty"`
	ReviewID      string             `json:"reviewId,omitempty"`
	RevisionID    string             `json:"revisionId,omitempty"`
	ExecutionID   string             `json:"executionId,omitempty"`
	RunID         string             `json:"runId"`
	WorksetHash   string             `json:"worksetHash"`
	Purpose       string             `json:"purpose"`
	NetworkAccess bool               `json:"networkAccess,omitempty"`
	ScratchRoot   string             `json:"scratchRoot"`
	Copies        []VerificationCopy `json:"copies,omitempty"`
	Writes        []WriteGrant       `json:"writes,omitempty"`
	// TrustedExecution freezes an explicit project-level authorization into
	// this Run at first binding. Nil on every legacy Run: their JSON encoding
	// and hash stay byte-identical. It is Core-derived only; a manifest that
	// claims it without the matching durable authorization is rejected at
	// binding by the SQLite authority guard.
	TrustedExecution *TrustedExecutionSnapshot `json:"trustedExecution,omitempty"`
	ContentHash      string                    `json:"contentHash"`
}

// TrustedExecutionSnapshot freezes the exact authorization revision a Run was
// bound under. ProjectID is the authorization's owner, not a model assertion.
// The two project toggles are independent: Trusted means the project granted
// worktree-level trusted execution (physical sandbox relaxation); a snapshot
// with Trusted=false and NetworkFromAuthorization=true freezes a network-only
// grant and changes no filesystem semantics. Legacy Runs keep a nil snapshot,
// so their JSON encoding and hash stay byte-identical.
type TrustedExecutionSnapshot struct {
	ProjectID                string `json:"projectId"`
	AuthorizationRevision    int    `json:"authorizationRevision"`
	Trusted                  bool   `json:"trusted"`
	NetworkFromAuthorization bool   `json:"networkFromAuthorization"`
}

// HasTrustedExecution reports whether this Run actually carries the project's
// trusted-execution grant. A network-only snapshot is not trusted execution;
// every physical relaxation and original-worktree verification branch must
// check this, never a non-nil pointer alone.
func (p TaskPermissions) HasTrustedExecution() bool {
	return p.TrustedExecution != nil && p.TrustedExecution.Trusted
}

type VerificationCopy struct {
	SourceRoot    string   `json:"sourceRoot"`
	Root          string   `json:"root"`
	SourceDigest  string   `json:"sourceDigest"`
	StartSnapshot string   `json:"startSnapshot"`
	RootIdentity  string   `json:"rootIdentity"`
	OutputPaths   []string `json:"outputPaths,omitempty"`
}

type WriteGrant struct {
	Root  string   `json:"root"`
	Kind  string   `json:"kind"`
	Paths []string `json:"paths,omitempty"`
}

func (p TaskPermissions) Hash() string {
	p.ContentHash = ""
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (p TaskPermissions) ProfileID() string { return "devbridge_task_" + p.ContentHash }

func (p TaskPermissions) DiagnosticProfileID() string { return p.ProfileID() + "_diagnostic" }

// ProfileArgs configures only this Bridge-owned child. It neither writes the
// user's Codex config nor installs a reusable command rule. Legacy sandbox
// fields are insufficient on runtimes that select a named default profile.
func (p TaskPermissions) ProfileArgs(roots, writable []string) ([]string, error) {
	if err := p.Validate(roots, writable); err != nil {
		return nil, err
	}
	rules := map[string]string{":root": "read", ":minimal": "read", ":tmpdir": "deny", ":slash_tmp": "deny"}
	if p.HasTrustedExecution() {
		// Trusted runs still sandbox: exact member worktrees plus scratch and
		// generic temp/cache stay writable, the rest of the filesystem stays
		// read-only or denied. :root never becomes writable. Trusted execution
		// skips the legacy per-PATHS narrowing: the native ACL grants the whole
		// worktree, while Core's business scope end checks still use the frozen
		// write grants. Network-only snapshots keep the legacy filesystem.
		rules[":tmpdir"] = "write"
		rules[":slash_tmp"] = "write"
	}
	for _, root := range roots {
		rules[root] = "read"
	}
	for _, root := range writable {
		rules[root] = "write"
	}
	if !p.HasTrustedExecution() {
		for _, grant := range p.Writes {
			if grant.Kind == "PATHS" {
				rules[grant.Root] = "read"
				for _, path := range grant.Paths {
					rules[filepath.Join(grant.Root, filepath.FromSlash(path))] = "write"
				}
			}
		}
	}
	for _, copy := range p.Copies {
		rules[copy.Root] = "read"
		for _, path := range copy.OutputPaths {
			rules[filepath.Join(copy.Root, filepath.FromSlash(path))] = "write"
		}
	}
	for _, root := range roots {
		for _, name := range []string{".git", ".codex", ".agents"} {
			rules[filepath.Join(root, name)] = "read"
		}
	}
	keys := make([]string, 0, len(rules))
	for key := range rules {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, strconv.Quote(key)+"="+strconv.Quote(rules[key]))
	}
	id := p.ProfileID()
	args := []string{"-c", "default_permissions=" + strconv.Quote(id), "-c", "approval_policy=\"never\"", "-c", "permissions." + id + ".filesystem={" + strings.Join(entries, ",") + "}", "-c", "permissions." + id + ".network.enabled=" + strconv.FormatBool(p.NetworkAccess), "-c", "features.hooks=false", "-c", "features.multi_agent=false", "-c", "features.multi_agent_v2=false", "-c", "web_search=\"disabled\""}
	// A recovery child may observe this process without inheriting implementation writes or network.
	diagnostic := p.DiagnosticProfileID()
	readonly := `{":root"="read",":minimal"="read",":tmpdir"="deny",":slash_tmp"="deny",` + strconv.Quote(p.ScratchRoot) + `="write"}`
	args = append(args, "-c", "permissions."+diagnostic+".filesystem="+readonly, "-c", "permissions."+diagnostic+".network.enabled=false")
	return args, nil
}

func (p TaskPermissions) Validate(roots, writable []string) error {
	task := p.TaskID != "" && p.RevisionID != "" && p.ExecutionID != "" && p.ReviewID == "" && (p.Purpose == "IMPLEMENTATION" || p.Purpose == "VERIFICATION")
	review := p.ReviewID != "" && p.TaskID == "" && p.RevisionID == "" && p.ExecutionID == "" && p.Purpose == "SCOPE_REVIEW"
	if review && p.NetworkAccess {
		return errors.New("TASK_PERMISSION_REVIEW_NETWORK_NOT_AUTHORIZED")
	}
	if p.Version != TaskPermissionVersion || (!task && !review) || p.RunID == "" || len(p.WorksetHash) != 64 || p.ContentHash != p.Hash() || !validRuntimePath(p.ScratchRoot) {
		return errors.New("TASK_PERMISSION_BINDING_INVALID")
	}
	if p.Purpose != "IMPLEMENTATION" && p.Purpose != "VERIFICATION" && p.Purpose != "SCOPE_REVIEW" {
		return errors.New("TASK_PERMISSION_PURPOSE_INVALID")
	}
	hasScratch := false
	for _, root := range writable {
		hasScratch = hasScratch || root == p.ScratchRoot
	}
	if !hasScratch {
		return errors.New("TASK_PERMISSION_SCRATCH_MISSING")
	}
	if t := p.TrustedExecution; t != nil {
		// Any project snapshot, including network-only or runs whose task
		// definition already granted network, requires the exact owner project
		// and a durable revision; the SQLite authority guard verifies it
		// against the immutable revision history. Trusted execution never
		// applies to reviews or diagnostics, and a network flag can only ride
		// along when the Run's effective network is on.
		if p.Purpose == "SCOPE_REVIEW" || t.ProjectID == "" || t.AuthorizationRevision < 1 || (t.NetworkFromAuthorization && !p.NetworkAccess) {
			return errors.New("TASK_PERMISSION_TRUSTED_INVALID")
		}
	}
	if p.HasTrustedExecution() {
		members := map[string]bool{}
		for _, root := range roots {
			if !validRuntimePath(root) || members[root] {
				return errors.New("TASK_PERMISSION_TRUSTED_ROOT_INVALID")
			}
			members[root] = true
		}
		for _, root := range writable {
			if !members[root] {
				return errors.New("TASK_PERMISSION_EXTRA_WRITE_ROOT")
			}
		}
	}
	if p.Purpose == "IMPLEMENTATION" {
		if len(p.Copies) != 0 {
			return errors.New("TASK_PERMISSION_UNEXPECTED_COPY")
		}
		seen := map[string]bool{}
		for _, grant := range p.Writes {
			if !validRuntimePath(grant.Root) || seen[grant.Root] || grant.Root == p.ScratchRoot || (grant.Kind != "PATHS" && grant.Kind != "REPOSITORY") || (grant.Kind == "REPOSITORY" && len(grant.Paths) != 0) || (grant.Kind == "PATHS" && len(grant.Paths) == 0) {
				return errors.New("TASK_PERMISSION_WRITE_SCOPE_INVALID")
			}
			seen[grant.Root] = true
			for _, path := range grant.Paths {
				if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\\\x00") {
					return errors.New("TASK_PERMISSION_WRITE_PATH_INVALID")
				}
			}
		}
		if !p.HasTrustedExecution() {
			// Legacy implementation runs bind write grants one path at a time.
			for _, root := range writable {
				if root != p.ScratchRoot && !seen[root] {
					return errors.New("TASK_PERMISSION_UNDECLARED_WRITE_ROOT")
				}
			}
			for root := range seen {
				found := false
				for _, write := range writable {
					found = found || root == write
				}
				if !found {
					return errors.New("TASK_PERMISSION_UNBOUND_WRITE_GRANT")
				}
			}
		} else {
			// Trusted widening still only covers declared member roots; the
			// frozen write grants remain the business scope for end checks.
			for _, root := range writable {
				if root != p.ScratchRoot && !seen[root] {
					return errors.New("TASK_PERMISSION_UNDECLARED_WRITE_ROOT")
				}
			}
		}
		return nil
	}
	if len(p.Writes) != 0 {
		return errors.New("TASK_PERMISSION_READ_ONLY_SOURCE_GRANT")
	}
	if p.HasTrustedExecution() {
		if len(p.Copies) != 0 {
			return errors.New("TASK_PERMISSION_UNEXPECTED_COPY")
		}
		// Trusted verification uses the original task worktree directly: no
		// copies, no per-path output whitelist. Every member worktree plus
		// scratch is writable; mutations of tracked or non-ignored source are
		// detected by the end-of-run snapshot checks, not by this profile.
		rootSet := map[string]bool{}
		for _, root := range roots {
			rootSet[root] = true
		}
		for _, root := range writable {
			if root != p.ScratchRoot && !rootSet[root] {
				return errors.New("TASK_PERMISSION_EXTRA_WRITE_ROOT")
			}
		}
		for _, root := range roots {
			if root != p.ScratchRoot {
				found := false
				for _, writable := range writable {
					found = found || writable == root
				}
				if !found {
					return errors.New("TASK_PERMISSION_TRUSTED_WORKTREE_NOT_WRITABLE")
				}
			}
		}
		return nil
	}
	if len(p.Copies) == 0 {
		return errors.New("TASK_PERMISSION_VERIFICATION_COPY_MISSING")
	}
	sources, copies := map[string]bool{}, map[string]bool{}
	for _, copy := range p.Copies {
		if !validRuntimePath(copy.SourceRoot) || !validRuntimePath(copy.Root) || len(copy.SourceDigest) != 64 || copy.RootIdentity == "" || copy.StartSnapshot == "" || sources[copy.SourceRoot] || copies[copy.Root] {
			return errors.New("TASK_PERMISSION_COPY_INVALID")
		}
		for _, path := range copy.OutputPaths {
			for _, part := range strings.Split(path, "/") {
				if part == ".git" || part == ".codex" || part == ".agents" {
					return errors.New("TASK_PERMISSION_OUTPUT_METADATA_FORBIDDEN")
				}
			}
			if path == "" || path == "." || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") || strings.ContainsAny(path, "\\\x00") {
				return errors.New("TASK_PERMISSION_OUTPUT_PATH_INVALID")
			}
		}
		sources[copy.SourceRoot], copies[copy.Root] = true, true
	}
	for source := range sources {
		for _, root := range writable {
			if pathsOverlap(source, root) {
				return errors.New("TASK_PERMISSION_SOURCE_WRITE_FORBIDDEN")
			}
		}
	}
	for _, root := range writable {
		if root != p.ScratchRoot && !copies[root] {
			return errors.New("TASK_PERMISSION_EXTRA_WRITE_ROOT")
		}
	}
	for _, root := range roots {
		if root != p.ScratchRoot && !copies[root] && !sources[root] {
			return errors.New("TASK_PERMISSION_EXTRA_READ_ROOT")
		}
	}
	for source := range sources {
		found := false
		for _, root := range roots {
			found = found || source == root
		}
		if !found {
			return errors.New("TASK_PERMISSION_SOURCE_MISSING")
		}
	}
	for root := range copies {
		found := false
		for _, writable := range writable {
			found = found || writable == root
		}
		if !found {
			return errors.New("TASK_PERMISSION_COPY_NOT_WRITABLE")
		}
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && rel != ".." && !startsWithParent(rel) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && rel != ".." && !startsWithParent(rel)
}

func startsWithParent(path string) bool {
	return len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}

func (p TaskPermissions) Instructions() string {
	b, _ := json.Marshal(struct {
		Purpose string             `json:"purpose"`
		Scratch string             `json:"scratch"`
		Copies  []VerificationCopy `json:"copies,omitempty"`
		Writes  []WriteGrant       `json:"writes,omitempty"`
	}{p.Purpose, p.ScratchRoot, p.Copies, p.Writes})
	network := "Network access is disabled."
	if p.NetworkAccess {
		network = "Network access was explicitly included in the frozen task proposal and is enabled only for this operation."
		if p.TrustedExecution != nil && p.TrustedExecution.NetworkFromAuthorization {
			network = "Network access is enabled only for this frozen operation."
		}
	}
	role := "This is a read-only operation. Product source worktrees and copied source are read-only. Run tests/builds in the listed independent copies, not the source worktrees. Copies include the uncommitted implementation; changes to copies are not implementation deliveries. Only the listed outputPaths (Git-ignored output directories) and scratch allow writes. Core additionally checks unchanged source snapshots after this run."
	if p.Purpose == "IMPLEMENTATION" {
		role = "This is an IMPLEMENTATION turn. Implement the approved engineering task within the listed writes grants: REPOSITORY permits product changes in that repository; PATHS permits changes only at the listed relative paths. Other registered repositories remain read-only. Prior read-only review or verification roles do not apply to this implementation turn. Preserve existing user changes and report this turn's actual implementation diff."
	}
	networkNote := ""
	if p.TrustedExecution != nil {
		if p.HasTrustedExecution() {
			if p.Purpose == "IMPLEMENTATION" {
				scope := "the approved REPOSITORY write grant permits product changes in that repository"
				for _, grant := range p.Writes {
					if grant.Kind == "PATHS" {
						scope = "the approved PATHS write grant still permits changes only at: " + strings.Join(grant.Paths, ", ") + " (in " + grant.Root + ")"
					}
				}
				role = "This is a trusted IMPLEMENTATION turn under an explicit project-level authorization. The sandbox permits the exact engineering worktrees at worktree level (Git metadata under .git/.codex/.agents stays host-owned and read-only); temporary files and tool caches may use the system temp directory or the dedicated scratch. Other projects' repositories and Core data stay outside this sandbox. The sandbox relaxation does not widen the approved engineering scope: " + scope + ", and Core's end-of-run scope checks enforce it — deliver only within that scope. Prior read-only review or verification roles do not apply to this implementation turn. Preserve existing user changes and report this turn's actual implementation diff."
			} else {
				role = "This is a trusted VERIFICATION turn under an explicit project-level authorization. Run checks directly in the original task worktrees and build caches; no source copy is made. Git-ignored build outputs may be written freely. Tracked and non-ignored source files must not be changed: any such change invalidates this verification at the end-of-run source checks, so if a check appears to require editing source, report BLOCKED instead. Git metadata stays host-owned and read-only."
			}
		}
		if p.NetworkAccess && p.TrustedExecution.NetworkFromAuthorization {
			networkNote = " The explicit project-level authorization grants network access; the frozen task definition is unchanged."
		} else if p.NetworkAccess {
			networkNote = " Network access was granted by the frozen task definition; the project-level authorization did not add or change it."
		}
	}
	return fmt.Sprintf("\nCore operation sandbox policy: %s\nCommands within this frozen sandbox are already authorized. Do not request unsandboxed execution, persistent command rules or broader permissions. %s%s Use the dedicated scratch directory for temporary files and tool caches. %s Record each actual command, cwd, exit code and limitations. If a check cannot run within these permissions, report BLOCKED and continue independent checks; do not wait for approval or claim it passed. The original engineering instruction and required checks remain unchanged.\n", b, network, networkNote, role)
}

func (p *TaskPermissions) Clone() *TaskPermissions {
	if p == nil {
		return nil
	}
	copy := *p
	if p.TrustedExecution != nil {
		value := *p.TrustedExecution
		copy.TrustedExecution = &value
	}
	copy.Copies = append([]VerificationCopy(nil), p.Copies...)
	for i := range copy.Copies {
		copy.Copies[i].OutputPaths = append([]string(nil), p.Copies[i].OutputPaths...)
	}
	copy.Writes = append([]WriteGrant(nil), p.Writes...)
	for i := range copy.Writes {
		copy.Writes[i].Paths = append([]string(nil), p.Writes[i].Paths...)
	}
	return &copy
}

func validRuntimePath(path string) bool {
	return path != "" && path != string(filepath.Separator) && filepath.IsAbs(path) && filepath.Clean(path) == path
}
