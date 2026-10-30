package workset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/runtimepermission"
	"path/filepath"
	"strings"
	"time"
)

// RuntimeProtocolVersion is the historical FeatureArea workset encoding. It
// remains frozen because v77 rows persist its JSON and content hash.
const RuntimeProtocolVersion = 1

// RuntimeProtocolVersionProjectInspection is an owner-specific encoding. It
// deliberately does not reinterpret v1's FeatureArea fields.
const RuntimeProtocolVersionProjectInspection = 2

// RuntimeProtocolVersionRecoveryStatus binds an isolated Recovery child. Its
// owner identity is the Recovery record, never its source Execution or Review.
const RuntimeProtocolVersionRecoveryStatus = 3

type RuntimeOwnerKind string

const (
	RuntimeOwnerFeatureArea       RuntimeOwnerKind = "FEATURE_AREA"
	RuntimeOwnerProjectInspection RuntimeOwnerKind = "PROJECT_INSPECTION"
	RuntimeOwnerRecoveryStatus    RuntimeOwnerKind = "RECOVERY_STATUS"
)

// ReadScope is the frozen, repository-relative read boundary for a v2 Project
// Inspection member. It is intentionally separate from WriteScope: inspection
// has no write authority at all.
type ReadScope struct {
	Kind  string   `json:"kind"` // REPOSITORY, PATHS
	Paths []string `json:"paths,omitempty"`
}

// RuntimeMemberBinding is the immutable bridge from one approved target to the
// exact managed worktree used by a Run. It records intended engineering scope;
// it is not a claim that the backend provides a hostile-security boundary.
type RuntimeMemberBinding struct {
	RepositoryID     string `json:"repositoryId"`
	RepositoryAlias  string `json:"repositoryAlias"`
	MemberID         string `json:"memberId"`
	MemberVersion    int    `json:"memberVersion"`
	WorktreeID       string `json:"worktreeId"`
	CanonicalRoot    string `json:"canonicalRoot"`
	RootFileIdentity string `json:"rootFileIdentity"`
	OriginCommit     string `json:"originCommit"`
	StartHEAD        string `json:"startHead"`
	StartSnapshot    string `json:"startSnapshot"`
	Read             bool   `json:"read"`
	// IntendedReadScope is v2-only. v1 historical worksets deliberately omit
	// it from their validation and hash material.
	IntendedReadScope   ReadScope  `json:"intendedReadScope,omitempty"`
	Write               WriteScope `json:"write"`
	RuntimeRootIdentity string     `json:"runtimeRootIdentity"`
}

// RuntimeWorkset is the single durable runtime authority for a Review,
// Implementation or Verification Run. Project/Workspace inventories never
// expand Members after this value has been frozen.
type RuntimeWorkset struct {
	AgentID                string `json:"agentId,omitempty"`
	AgentConfigFingerprint string `json:"agentConfigFingerprint,omitempty"`
	ExecutionPolicy        string `json:"executionPolicy,omitempty"`
	// Execution environment has its own Run-bound hash. It never changes the
	// historical engineering-authority hash shared by both phases.
	TaskPermissions          *runtimepermission.TaskPermissions `json:"taskPermissions,omitempty"`
	ProtocolVersion          int                                `json:"protocolVersion"`
	OwnerKind                RuntimeOwnerKind                   `json:"ownerKind,omitempty"`
	ProjectID                string                             `json:"projectId"`
	FeatureAreaID            string                             `json:"featureAreaId"`
	TaskID                   string                             `json:"taskId,omitempty"`
	ReviewID                 string                             `json:"reviewId,omitempty"`
	TaskDefinitionRevisionID string                             `json:"taskDefinitionRevisionId,omitempty"`
	ExecutionID              string                             `json:"executionId,omitempty"`
	// InspectionOwnerTaskID remains the business owner while
	// InspectionOperationID is the isolated runtime owner. Both are v2-only.
	InspectionOwnerTaskID    string                 `json:"inspectionOwnerTaskId,omitempty"`
	InspectionOperationID    string                 `json:"inspectionOperationId,omitempty"`
	RecoveryStatusID         string                 `json:"recoveryStatusId,omitempty"`
	RecoverySourceRunID      string                 `json:"recoverySourceRunId,omitempty"`
	RunID                    string                 `json:"runId"`
	WorkspaceID              string                 `json:"workspaceId"`
	PrimaryMemberID          string                 `json:"primaryMemberId"`
	Members                  []RuntimeMemberBinding `json:"members"`
	ContentHash              string                 `json:"contentHash"`
	BackendConfigID          string                 `json:"backendConfigId"`
	BackendConfigFingerprint string                 `json:"backendConfigFingerprint"`
	Model                    string                 `json:"model"`
	ReasoningEffort          string                 `json:"reasoningEffort"`
	ServiceTier              string                 `json:"serviceTier,omitempty"`
	RuntimeAdapterIdentity   string                 `json:"runtimeAdapterIdentity"`
	RuntimeProtocolIdentity  string                 `json:"runtimeProtocolIdentity"`
	CreatedAt                time.Time              `json:"createdAt"`
}

func (w RuntimeWorkset) Validate() error {
	if err := agentpolicy.ValidateFrozenSelection(w.AgentID, w.ExecutionPolicy, w.AgentConfigFingerprint); err != nil {
		return err
	}
	if w.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (w.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || w.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
		return errors.New("cooperative workset requires explicit cooperative runtime identities")
	}
	if w.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && w.TaskPermissions != nil {
		return errors.New("cooperative workset cannot carry native task permissions")
	}

	if err := w.validateTaskPermissions(); err != nil {
		return err
	}
	if w.ProtocolVersion == RuntimeProtocolVersionProjectInspection {
		return w.validateProjectInspection()
	}
	if w.ProtocolVersion == RuntimeProtocolVersionRecoveryStatus {
		return w.validateRecoveryStatus()
	}
	if w.ProtocolVersion != RuntimeProtocolVersion || w.ProjectID == "" || w.FeatureAreaID == "" || w.RunID == "" || w.WorkspaceID == "" || w.PrimaryMemberID == "" || len(w.Members) == 0 || len(w.Members) > 128 {
		return errors.New("complete bounded RuntimeWorkset identity is required")
	}
	if w.RecoveryStatusID != "" || w.RecoverySourceRunID != "" {
		return errors.New("FeatureArea RuntimeWorkset cannot carry Recovery Status identity")
	}
	if (w.TaskID == "") == (w.ReviewID == "") {
		return errors.New("RuntimeWorkset must have exactly one Task or Review owner")
	}
	if w.TaskID != "" && (w.TaskDefinitionRevisionID == "" || w.ExecutionID == "") {
		return errors.New("Task RuntimeWorkset requires revision and Execution")
	}
	if w.BackendConfigID == "" || w.BackendConfigFingerprint == "" || w.Model == "" || w.ReasoningEffort == "" || w.RuntimeAdapterIdentity == "" || w.RuntimeProtocolIdentity == "" || w.CreatedAt.IsZero() {
		return errors.New("RuntimeWorkset runtime binding is incomplete")
	}
	seenRepository, seenMember, seenRoot := map[string]bool{}, map[string]bool{}, map[string]bool{}
	primary := false
	for _, member := range w.Members {
		if member.RepositoryID == "" || strings.TrimSpace(member.RepositoryAlias) == "" || member.MemberID == "" || member.MemberVersion < 1 || member.WorktreeID == "" || member.RootFileIdentity == "" || member.RuntimeRootIdentity == "" || !ValidCommit(member.OriginCommit) || !ValidCommit(member.StartHEAD) || !ValidCommit(member.StartSnapshot) || !member.Read {
			return errors.New("RuntimeWorkset member identity is incomplete")
		}
		if !filepath.IsAbs(member.CanonicalRoot) || filepath.Clean(member.CanonicalRoot) != member.CanonicalRoot || member.CanonicalRoot == string(filepath.Separator) {
			return errors.New("RuntimeWorkset member root must be a canonical absolute path")
		}
		if seenRepository[member.RepositoryID] || seenMember[member.MemberID] || seenRoot[member.CanonicalRoot] {
			return errors.New("RuntimeWorkset members must have distinct repository, member and root identities")
		}
		seenRepository[member.RepositoryID], seenMember[member.MemberID], seenRoot[member.CanonicalRoot] = true, true, true
		primary = primary || member.MemberID == w.PrimaryMemberID
		if err := ValidateTargets([]Target{{RepositoryID: member.RepositoryID, MemberID: member.MemberID, MemberVersion: member.MemberVersion, OriginCommit: member.OriginCommit, Read: member.Read, Write: member.Write}}); err != nil {
			return err
		}
	}
	if !primary {
		return errors.New("RuntimeWorkset primary member is outside the frozen member set")
	}
	expected, err := w.ComputeContentHash()
	if err != nil || expected != w.ContentHash {
		return errors.New("RuntimeWorkset content hash mismatch")
	}
	return nil
}

// ComputeContentHash excludes RunID, CreatedAt and per-Run start observations
// so Implementation and Verification can prove the same frozen authority while
// retaining distinct Runs and freshly observed start facts.
func (w RuntimeWorkset) ComputeContentHash() (string, error) {
	if w.ProtocolVersion == RuntimeProtocolVersionProjectInspection {
		return w.computeProjectInspectionContentHash()
	}
	if w.ProtocolVersion == RuntimeProtocolVersionRecoveryStatus {
		return w.computeRecoveryStatusContentHash()
	}
	return w.computeV1ContentHash()
}

// computeV1ContentHash intentionally retains v77's exact material layout.
func (w RuntimeWorkset) computeV1ContentHash() (string, error) {
	type memberAuthority struct {
		RepositoryID, RepositoryAlias, MemberID string
		MemberVersion                           int
		WorktreeID, CanonicalRoot               string
		RootFileIdentity, OriginCommit          string
		Read                                    bool
		Write                                   WriteScope
		RuntimeRootIdentity                     string
	}
	members := make([]memberAuthority, 0, len(w.Members))
	for _, member := range w.Members {
		members = append(members, memberAuthority{
			RepositoryID: member.RepositoryID, RepositoryAlias: member.RepositoryAlias,
			MemberID: member.MemberID, MemberVersion: member.MemberVersion,
			WorktreeID: member.WorktreeID, CanonicalRoot: member.CanonicalRoot,
			RootFileIdentity: member.RootFileIdentity, OriginCommit: member.OriginCommit,
			Read: member.Read, Write: member.Write, RuntimeRootIdentity: member.RuntimeRootIdentity,
		})
	}
	material := struct {
		ProtocolVersion                                                                                                                 int
		ProjectID, FeatureAreaID, TaskID, ReviewID, TaskDefinitionRevisionID, ExecutionID, WorkspaceID, PrimaryMemberID                 string
		Members                                                                                                                         []memberAuthority
		BackendConfigID, BackendConfigFingerprint, Model, ReasoningEffort, ServiceTier, RuntimeAdapterIdentity, RuntimeProtocolIdentity string
	}{w.ProtocolVersion, w.ProjectID, w.FeatureAreaID, w.TaskID, w.ReviewID, w.TaskDefinitionRevisionID, w.ExecutionID, w.WorkspaceID, w.PrimaryMemberID, members, w.BackendConfigID, w.BackendConfigFingerprint, w.Model, w.ReasoningEffort, w.ServiceTier, w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity}
	raw, err := marshalRuntimeAuthority(material, w.AgentID, w.ExecutionPolicy, w.AgentConfigFingerprint)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (w RuntimeWorkset) validateProjectInspection() error {
	if w.OwnerKind != RuntimeOwnerProjectInspection || w.ProjectID == "" || w.InspectionOwnerTaskID == "" || w.InspectionOperationID == "" || w.RunID == "" || w.WorkspaceID == "" || w.PrimaryMemberID == "" || len(w.Members) == 0 || len(w.Members) > 128 {
		return errors.New("complete Project Inspection RuntimeWorkset identity is required")
	}
	if w.FeatureAreaID != "" || w.TaskID != "" || w.ReviewID != "" || w.TaskDefinitionRevisionID != "" || w.ExecutionID != "" || w.RecoveryStatusID != "" || w.RecoverySourceRunID != "" {
		return errors.New("Project Inspection RuntimeWorkset cannot carry FeatureArea task review or execution identity")
	}
	if w.BackendConfigID == "" || w.BackendConfigFingerprint == "" || w.Model == "" || w.ReasoningEffort == "" || w.RuntimeAdapterIdentity == "" || w.RuntimeProtocolIdentity == "" || w.CreatedAt.IsZero() {
		return errors.New("RuntimeWorkset runtime binding is incomplete")
	}
	seenRepository, seenMember, seenRoot := map[string]bool{}, map[string]bool{}, map[string]bool{}
	primary := false
	for _, member := range w.Members {
		if member.RepositoryID == "" || strings.TrimSpace(member.RepositoryAlias) == "" || member.MemberID == "" || member.MemberVersion < 1 || member.WorktreeID == "" || member.RootFileIdentity == "" || member.RuntimeRootIdentity == "" || !ValidCommit(member.OriginCommit) || !ValidCommit(member.StartHEAD) || !ValidCommit(member.StartSnapshot) || !member.Read || member.Write.Kind != "NONE" || len(member.Write.Paths) != 0 || ValidateProjectInspectionReadScope(member.IntendedReadScope) != nil {
			return errors.New("Project Inspection RuntimeWorkset member identity or read-only scope is incomplete")
		}
		if !filepath.IsAbs(member.CanonicalRoot) || filepath.Clean(member.CanonicalRoot) != member.CanonicalRoot || member.CanonicalRoot == string(filepath.Separator) {
			return errors.New("RuntimeWorkset member root must be a canonical absolute path")
		}
		if seenRepository[member.RepositoryID] || seenMember[member.MemberID] || seenRoot[member.CanonicalRoot] {
			return errors.New("RuntimeWorkset members must have distinct repository, member and root identities")
		}
		seenRepository[member.RepositoryID], seenMember[member.MemberID], seenRoot[member.CanonicalRoot] = true, true, true
		primary = primary || member.MemberID == w.PrimaryMemberID
	}
	if !primary {
		return errors.New("RuntimeWorkset primary member is outside the frozen member set")
	}
	expected, err := w.ComputeContentHash()
	if err != nil || expected != w.ContentHash {
		return errors.New("RuntimeWorkset content hash mismatch")
	}
	return nil
}

func (w RuntimeWorkset) computeProjectInspectionContentHash() (string, error) {
	type memberAuthority struct {
		RepositoryID, RepositoryAlias, MemberID string
		MemberVersion                           int
		WorktreeID, CanonicalRoot               string
		RootFileIdentity, OriginCommit          string
		Read                                    bool
		IntendedReadScope                       ReadScope
		Write                                   WriteScope
		RuntimeRootIdentity                     string
	}
	members := make([]memberAuthority, 0, len(w.Members))
	for _, member := range w.Members {
		members = append(members, memberAuthority{member.RepositoryID, member.RepositoryAlias, member.MemberID, member.MemberVersion, member.WorktreeID, member.CanonicalRoot, member.RootFileIdentity, member.OriginCommit, member.Read, member.IntendedReadScope, member.Write, member.RuntimeRootIdentity})
	}
	material := struct {
		ProtocolVersion                                                                                                                 int
		OwnerKind                                                                                                                       RuntimeOwnerKind
		ProjectID, InspectionOwnerTaskID, InspectionOperationID, WorkspaceID, PrimaryMemberID                                           string
		Members                                                                                                                         []memberAuthority
		BackendConfigID, BackendConfigFingerprint, Model, ReasoningEffort, ServiceTier, RuntimeAdapterIdentity, RuntimeProtocolIdentity string
	}{w.ProtocolVersion, w.OwnerKind, w.ProjectID, w.InspectionOwnerTaskID, w.InspectionOperationID, w.WorkspaceID, w.PrimaryMemberID, members, w.BackendConfigID, w.BackendConfigFingerprint, w.Model, w.ReasoningEffort, w.ServiceTier, w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity}
	raw, err := marshalRuntimeAuthority(material, w.AgentID, w.ExecutionPolicy, w.AgentConfigFingerprint)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (w RuntimeWorkset) validateRecoveryStatus() error {
	if w.OwnerKind != RuntimeOwnerRecoveryStatus || w.ProjectID == "" || w.RecoveryStatusID == "" || w.RecoverySourceRunID == "" || w.RecoveryStatusID == w.RecoverySourceRunID || w.RunID == "" || w.WorkspaceID == "" || w.PrimaryMemberID == "" || len(w.Members) == 0 || len(w.Members) > 128 {
		return errors.New("complete Recovery Status RuntimeWorkset identity is required")
	}
	if w.ReviewID != "" || w.ExecutionID != "" || w.TaskDefinitionRevisionID != "" || w.InspectionOperationID != "" || w.InspectionOwnerTaskID != "" {
		return errors.New("Recovery Status RuntimeWorkset cannot carry source owner identity")
	}
	if w.BackendConfigID == "" || w.BackendConfigFingerprint == "" || w.Model == "" || w.ReasoningEffort == "" || w.RuntimeAdapterIdentity == "" || w.RuntimeProtocolIdentity == "" || w.CreatedAt.IsZero() {
		return errors.New("RuntimeWorkset runtime binding is incomplete")
	}
	seenRepository, seenMember, seenRoot := map[string]bool{}, map[string]bool{}, map[string]bool{}
	primary := false
	for _, member := range w.Members {
		if member.RepositoryID == "" || strings.TrimSpace(member.RepositoryAlias) == "" || member.MemberID == "" || member.MemberVersion < 1 || member.WorktreeID == "" || member.RootFileIdentity == "" || member.RuntimeRootIdentity == "" || !ValidCommit(member.OriginCommit) || !ValidCommit(member.StartHEAD) || !ValidCommit(member.StartSnapshot) || !member.Read || member.Write.Kind != "NONE" || len(member.Write.Paths) != 0 {
			return errors.New("Recovery Status RuntimeWorkset member identity or read-only scope is incomplete")
		}
		if !filepath.IsAbs(member.CanonicalRoot) || filepath.Clean(member.CanonicalRoot) != member.CanonicalRoot || member.CanonicalRoot == string(filepath.Separator) {
			return errors.New("RuntimeWorkset member root must be a canonical absolute path")
		}
		if seenRepository[member.RepositoryID] || seenMember[member.MemberID] || seenRoot[member.CanonicalRoot] {
			return errors.New("RuntimeWorkset members must have distinct repository, member and root identities")
		}
		seenRepository[member.RepositoryID], seenMember[member.MemberID], seenRoot[member.CanonicalRoot] = true, true, true
		primary = primary || member.MemberID == w.PrimaryMemberID
	}
	if !primary {
		return errors.New("RuntimeWorkset primary member is outside the frozen member set")
	}
	expected, err := w.ComputeContentHash()
	if err != nil || expected != w.ContentHash {
		return errors.New("RuntimeWorkset content hash mismatch")
	}
	return nil
}

func (w RuntimeWorkset) computeRecoveryStatusContentHash() (string, error) {
	// The v3 layout is separate from the historical v1 and v2 hash material.
	// Actual start observations remain in each Run's JSON, while the authority
	// hash covers the exact managed member roots and read-only scopes.
	type memberAuthority struct {
		RepositoryID, RepositoryAlias, MemberID string
		MemberVersion                           int
		WorktreeID, CanonicalRoot               string
		RootFileIdentity, OriginCommit          string
		Read                                    bool
		Write                                   WriteScope
		RuntimeRootIdentity                     string
	}
	members := make([]memberAuthority, 0, len(w.Members))
	for _, member := range w.Members {
		members = append(members, memberAuthority{member.RepositoryID, member.RepositoryAlias, member.MemberID, member.MemberVersion, member.WorktreeID, member.CanonicalRoot, member.RootFileIdentity, member.OriginCommit, member.Read, member.Write, member.RuntimeRootIdentity})
	}
	material := struct {
		ProtocolVersion                                      int
		OwnerKind                                            RuntimeOwnerKind
		ProjectID, FeatureAreaID, TaskID, RecoveryStatusID   string
		RecoverySourceRunID, WorkspaceID, PrimaryMemberID    string
		Members                                              []memberAuthority
		BackendConfigID, BackendConfigFingerprint, Model     string
		ReasoningEffort, ServiceTier, RuntimeAdapterIdentity string
		RuntimeProtocolIdentity                              string
	}{w.ProtocolVersion, w.OwnerKind, w.ProjectID, w.FeatureAreaID, w.TaskID, w.RecoveryStatusID, w.RecoverySourceRunID, w.WorkspaceID, w.PrimaryMemberID, members, w.BackendConfigID, w.BackendConfigFingerprint, w.Model, w.ReasoningEffort, w.ServiceTier, w.RuntimeAdapterIdentity, w.RuntimeProtocolIdentity}
	raw, err := marshalRuntimeAuthority(material, w.AgentID, w.ExecutionPolicy, w.AgentConfigFingerprint)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (w RuntimeWorkset) WithHash() (RuntimeWorkset, error) {
	w.TaskPermissions = w.TaskPermissions.Clone()
	w.Members = cloneRuntimeMembers(w.Members)
	value, err := w.ComputeContentHash()
	if err != nil {
		return RuntimeWorkset{}, err
	}
	w.ContentHash = value
	if err := w.Validate(); err != nil {
		return RuntimeWorkset{}, err
	}
	return w, nil
}

func cloneRuntimeMembers(values []RuntimeMemberBinding) []RuntimeMemberBinding {
	out := append([]RuntimeMemberBinding(nil), values...)
	for i := range out {
		out[i].IntendedReadScope.Paths = append([]string(nil), values[i].IntendedReadScope.Paths...)
		out[i].Write.Paths = append([]string(nil), values[i].Write.Paths...)
	}
	return out
}

// ValidateProjectInspectionReadScope checks receipt-normalized, repository
// relative paths without touching the filesystem. The confirmation layer has
// already resolved symlinks against the source repository before this durable
// v2 copy is made.
func ValidateProjectInspectionReadScope(scope ReadScope) error {
	switch scope.Kind {
	case "REPOSITORY":
		if len(scope.Paths) == 0 {
			return nil
		}
	case "PATHS":
		if len(scope.Paths) == 0 || len(scope.Paths) > 128 {
			break
		}
		seen := make(map[string]bool, len(scope.Paths))
		for _, value := range scope.Paths {
			if value == "" || value == "." || len(value) > 4096 || strings.ContainsAny(value, "\\\x00") || strings.HasPrefix(value, "/") || filepath.ToSlash(filepath.Clean(value)) != value || value == ".." || strings.HasPrefix(value, "../") || seen[value] {
				return errors.New("invalid Project Inspection read path")
			}
			seen[value] = true
		}
		return nil
	}
	return errors.New("Project Inspection read scope must be REPOSITORY or bounded PATHS")
}

// Empty prospective fields retain the exact historical material bytes. New
// selections bind both identity and policy without rewriting old workset hashes.
func marshalRuntimeAuthority(material any, agentID, policy, fingerprint string) ([]byte, error) {
	raw, err := json.Marshal(material)
	if err != nil || agentID == "" && policy == "" && fingerprint == "" {
		return raw, err
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	if agentID != "" {
		value["AgentID"], err = json.Marshal(agentID)
		if err != nil {
			return nil, err
		}
	}
	if policy != "" {
		value["ExecutionPolicy"], err = json.Marshal(policy)
		if err != nil {
			return nil, err
		}
	}
	if fingerprint != "" {
		value["AgentConfigFingerprint"], err = json.Marshal(fingerprint)
		if err != nil {
			return nil, err
		}
	}
	return json.Marshal(value)
}
