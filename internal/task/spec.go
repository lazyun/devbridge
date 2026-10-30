package task

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrInstructionRequired = errors.New("CONTRACT_INCOMPLETE: exact authored instruction is required")

var ErrUnobservableRequiredCheck = errors.New("UNOBSERVABLE_REQUIRED_CHECK")

const (
	MaxGoalBytes                   = 16 << 10
	MaxConstraintCount             = 32
	MaxConstraintBytes             = 2 << 10
	MaxCodexInstructionBytes       = 64 << 10
	MaxExecutionScopePathCount     = 128
	MaxExecutionScopePathBytes     = 4096
	MaxVerificationScopeBytes      = 16 << 10
	MaxVerificationCheckCount      = 32
	MaxAcceptanceCriterionCount    = 32
	MaxAcceptanceCriterionBytes    = 2 << 10
	MaxVerificationCheckValueBytes = 8 << 10
	MaxVerificationPathCount       = 64
	MaxVerificationPathBytes       = 2 << 10
	MaxWaiverReasonBytes           = 2 << 10
)

// WorkSpec is the confirmed implementation contract. CodexInstruction is
// authored by the Upper LLM/caller and is the entire natural-language prompt.
// Authorization and output schemas travel as separate machine fields.
type ImplementationMode string

const (
	ImplementationCodingAgent ImplementationMode = "CODING_AGENT"
	ImplementationDirectFile  ImplementationMode = "DIRECT_FILE"
)

// Empty mode is an unknown legacy contract, never an inferred executor.
// Omitting it preserves the immutable encoding of historical definitions.
type WorkSpec struct {
	// Omitted historical definitions retain their original immutable encoding.
	// A true network grant is part of the formal proposal, never an Agent reply.
	CommandPermissions *CommandPermissions `json:"commandPermissions,omitempty"`
	ImplementationMode ImplementationMode  `json:"implementationMode,omitempty"`
	Goal               string              `json:"goal"`
	Constraints        []string            `json:"constraints"`
	CodexInstruction   string              `json:"codexInstruction"`
	ExecutionScope     ExecutionScope      `json:"executionScope"`
}

type CommandPermissions struct {
	NetworkAccess       bool     `json:"networkAccess"`
	VerificationOutputs []string `json:"verificationOutputs,omitempty"`
}

type ExecutionScopeKind string

const (
	ExecutionScopeTargets           ExecutionScopeKind = "REPOSITORY_TARGETS"
	ExecutionScopePaths             ExecutionScopeKind = "PATHS"
	ExecutionScopeRepository        ExecutionScopeKind = "REPOSITORY"
	ExecutionScopeLegacyUnspecified ExecutionScopeKind = "LEGACY_UNSPECIFIED"
)

// ExecutionScope is the confirmed implementation mutation boundary. PATHS
// contains canonical repository-relative files or directories. REPOSITORY is
// an explicit repository-wide authorization and never carries paths.
type ExecutionScope struct {
	Kind  ExecutionScopeKind `json:"kind"`
	Paths []string           `json:"paths,omitempty"`
}

type VerificationMode string

const (
	VerificationModeRun  VerificationMode = "RUN"
	VerificationModeNone VerificationMode = "NONE"
)

type VerificationCheckKind string

const (
	VerificationCheckTestCommand    VerificationCheckKind = "TEST_COMMAND"
	VerificationCheckStaticAnalysis VerificationCheckKind = "STATIC_ANALYSIS"
	VerificationCheckDiff           VerificationCheckKind = "DIFF_CHECK"
	VerificationCheckGitAssertion   VerificationCheckKind = "GIT_ASSERTION"
	VerificationCheckCustom         VerificationCheckKind = "CUSTOM"
)

type VerificationCheck struct {
	RepositoryID     string                `json:"repositoryId,omitempty"`
	WorkingDirectory string                `json:"workingDirectory,omitempty"`
	RepositoryIDs    []string              `json:"repositoryIds,omitempty"`
	Kind             VerificationCheckKind `json:"kind"`
	Command          string                `json:"command,omitempty"`
	Instruction      string                `json:"instruction,omitempty"`
	Paths            []string              `json:"paths,omitempty"`
	Packages         []string              `json:"packages,omitempty"`
	Required         bool                  `json:"required"`
}

type VerificationWaiverSource string

const (
	VerificationWaiverUser   VerificationWaiverSource = "USER_WAIVED"
	VerificationWaiverLegacy VerificationWaiverSource = "LEGACY_UNSPECIFIED"
)

type VerificationWaiver struct {
	Source VerificationWaiverSource `json:"source"`
	Reason string                   `json:"reason"`
}

// VerificationSpec is always explicit. RUN carries the complete confirmed
// validation contract; NONE carries the durable user waiver and creates no
// Verification Run.
type VerificationSpec struct {
	Mode               VerificationMode    `json:"mode"`
	Scope              string              `json:"scope,omitempty"`
	Checks             []VerificationCheck `json:"checks,omitempty"`
	AcceptanceCriteria []string            `json:"acceptanceCriteria,omitempty"`
	CodexInstruction   string              `json:"codexInstruction,omitempty"`
	Waiver             *VerificationWaiver `json:"waiver,omitempty"`
}

func ValidateWorkSpec(value WorkSpec) error {
	return validateWorkSpec(value, false)
}

// ValidatePersistedWorkSpec additionally accepts the migration-only marker
// used for plans created before M5 introduced explicit implementation scope.
// Services must use ValidateWorkSpec for every newly-created plan.
func ValidatePersistedWorkSpec(value WorkSpec) error {
	return validateWorkSpec(value, true)
}

func validateWorkSpec(value WorkSpec, allowLegacy bool) error {
	if p := value.CommandPermissions; p != nil {
		if len(p.VerificationOutputs) > 32 {
			return errors.New("too many verification output paths")
		}
		for _, path := range p.VerificationOutputs {
			for _, part := range strings.Split(path, "/") {
				if part == ".git" || part == ".codex" || part == ".agents" {
					return errors.New("verification output must not include protected metadata")
				}
			}
			if path == "." || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "\\") {
				return errors.New("verification output must be a canonical relative directory")
			}
			if err := validateExecutionScopeShape(ExecutionScope{Kind: ExecutionScopePaths, Paths: []string{path}}, false); err != nil {
				return err
			}
		}
	}
	if value.ImplementationMode != "" && value.ImplementationMode != ImplementationCodingAgent && value.ImplementationMode != ImplementationDirectFile {
		return errors.New("implementation mode must be CODING_AGENT or DIRECT_FILE")
	}
	if strings.TrimSpace(value.CodexInstruction) == "" {
		return ErrInstructionRequired
	}
	if strings.TrimSpace(value.Goal) == "" || len(value.Goal) > MaxGoalBytes {
		return errors.New("work goal is required and must be bounded")
	}
	if strings.TrimSpace(value.CodexInstruction) == "" || len(value.CodexInstruction) > MaxCodexInstructionBytes {
		return errors.New("AI-authored implementation Codex instruction is required and must be bounded")
	}
	if err := validateExecutionScopeShape(value.ExecutionScope, allowLegacy); err != nil {
		return err
	}
	if len(value.Constraints) > MaxConstraintCount {
		return errors.New("work constraints exceed bounds")
	}
	for _, constraint := range value.Constraints {
		if len(constraint) > MaxConstraintBytes {
			return errors.New("work constraint exceeds bounds")
		}
	}
	return nil
}

func validateExecutionScopeShape(value ExecutionScope, allowLegacy bool) error {
	switch value.Kind {
	case ExecutionScopeRepository:
		if len(value.Paths) != 0 {
			return errors.New("REPOSITORY execution scope must not contain paths")
		}
		return nil
	case ExecutionScopePaths:
		if len(value.Paths) == 0 || len(value.Paths) > MaxExecutionScopePathCount {
			return errors.New("PATHS execution scope requires one or more bounded paths")
		}
		for _, value := range value.Paths {
			if value == "" || len(value) > MaxExecutionScopePathBytes || strings.ContainsRune(value, '\x00') {
				return errors.New("execution scope path is required and must be bounded")
			}
		}
		return nil
	case ExecutionScopeLegacyUnspecified:
		if allowLegacy && len(value.Paths) == 0 {
			return nil
		}
	}
	return errors.New("execution scope must explicitly be PATHS or REPOSITORY")
}

// CanonicalizeExecutionScope validates the confirmed scope against the
// repository state and returns stable repository-relative canonical targets.
func CanonicalizeExecutionScope(repositoryRoot string, scope ExecutionScope) (ExecutionScope, error) {
	if err := validateExecutionScopeShape(scope, false); err != nil {
		return ExecutionScope{}, err
	}
	if scope.Kind == ExecutionScopeRepository {
		return ExecutionScope{Kind: ExecutionScopeRepository, Paths: []string{}}, nil
	}
	root, err := filepath.EvalSymlinks(repositoryRoot)
	if err != nil {
		return ExecutionScope{}, fmt.Errorf("canonicalize execution repository: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return ExecutionScope{}, err
	}
	seen := make(map[string]struct{}, len(scope.Paths))
	result := ExecutionScope{Kind: ExecutionScopePaths, Paths: make([]string, 0, len(scope.Paths))}
	for _, supplied := range scope.Paths {
		if filepath.IsAbs(supplied) || strings.Contains(supplied, "\\") {
			return ExecutionScope{}, errors.New("execution scope paths must be repository-relative slash paths")
		}
		for _, component := range strings.Split(supplied, "/") {
			if component == ".." {
				return ExecutionScope{}, errors.New("execution scope paths must not contain parent traversal")
			}
		}
		clean := filepath.Clean(filepath.FromSlash(supplied))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return ExecutionScope{}, errors.New("execution scope paths must identify repository content")
		}
		target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
		if err != nil {
			return ExecutionScope{}, errors.New("execution scope path must exist")
		}
		target, err = filepath.Abs(target)
		if err != nil {
			return ExecutionScope{}, err
		}
		relative, err := filepath.Rel(root, target)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ExecutionScope{}, errors.New("execution scope path escapes the repository")
		}
		info, err := os.Stat(target)
		if err != nil || (!info.Mode().IsRegular() && !info.IsDir()) {
			return ExecutionScope{}, errors.New("execution scope path must be a file or directory")
		}
		normalized := filepath.ToSlash(relative)
		if _, duplicate := seen[normalized]; duplicate {
			return ExecutionScope{}, errors.New("execution scope contains duplicate canonical targets")
		}
		seen[normalized] = struct{}{}
		result.Paths = append(result.Paths, normalized)
	}
	return result, nil
}

func ValidateVerificationSpec(value VerificationSpec) error {
	switch value.Mode {
	case VerificationModeRun:
		if strings.TrimSpace(value.CodexInstruction) == "" {
			return ErrInstructionRequired
		}
		if value.Waiver != nil || strings.TrimSpace(value.Scope) == "" || len(value.Scope) > MaxVerificationScopeBytes ||
			len(value.Checks) == 0 || len(value.Checks) > MaxVerificationCheckCount ||
			len(value.AcceptanceCriteria) == 0 || len(value.AcceptanceCriteria) > MaxAcceptanceCriterionCount ||
			strings.TrimSpace(value.CodexInstruction) == "" || len(value.CodexInstruction) > MaxCodexInstructionBytes {
			return errors.New("RUN verification requires bounded scope, checks, acceptance criteria, and Codex instruction without a waiver")
		}
		for _, check := range value.Checks {
			if err := validateVerificationCheck(check); err != nil {
				return err
			}
		}
		for _, criterion := range value.AcceptanceCriteria {
			if strings.TrimSpace(criterion) == "" || len(criterion) > MaxAcceptanceCriterionBytes {
				return errors.New("verification acceptance criterion is required and must be bounded")
			}
		}
		return nil
	case VerificationModeNone:
		if value.Waiver == nil || value.Waiver.Source != VerificationWaiverUser || strings.TrimSpace(value.Waiver.Reason) == "" || len(value.Waiver.Reason) > MaxWaiverReasonBytes {
			return errors.New("NONE verification requires a bounded USER_WAIVED reason")
		}
		if value.Scope != "" || len(value.Checks) != 0 || len(value.AcceptanceCriteria) != 0 || value.CodexInstruction != "" {
			return errors.New("NONE verification cannot carry RUN fields")
		}
		return nil
	default:
		return errors.New("verification mode must be RUN or NONE")
	}
}

// ValidatePersistedVerificationSpec also accepts the one migration-only waiver
// used to make pre-v10 records explicit without falsely claiming user intent.
func ValidatePersistedVerificationSpec(value VerificationSpec) error {
	if value.Mode == VerificationModeNone && value.Waiver != nil && value.Waiver.Source == VerificationWaiverLegacy {
		if strings.TrimSpace(value.Waiver.Reason) == "" || len(value.Waiver.Reason) > MaxWaiverReasonBytes || value.Scope != "" || len(value.Checks) != 0 || len(value.AcceptanceCriteria) != 0 || value.CodexInstruction != "" {
			return errors.New("invalid legacy verification waiver")
		}
		return nil
	}
	return ValidateVerificationSpec(value)
}

// ValidateExecutableVerificationSpec is a new-admission gate, separate from
// persisted decoding. Command/Git checks need a deterministic observation path.
// CUSTOM instructions are mandatory analysis obligations when Required is true;
// their assessment remains an explicit GPT claim, never Bridge-observed proof.
func ValidateExecutableVerificationSpec(value VerificationSpec) error {
	if err := ValidateVerificationSpec(value); err != nil {
		return err
	}
	for _, check := range value.Checks {
		if !check.Required {
			continue
		}
		if !check.RequiresObservation() {
			continue
		}
		if strings.TrimSpace(check.Command) != "" {
			continue
		}
		if check.Kind != VerificationCheckGitAssertion {
			return fmt.Errorf("%w: required command check needs a command", ErrUnobservableRequiredCheck)
		}
		if check.RepositoryID != "" && check.WorkingDirectory != "." {
			return fmt.Errorf("%w: typed Git assertions require the repository root", ErrUnobservableRequiredCheck)
		}
		switch strings.ToLower(strings.TrimSpace(check.Instruction)) {
		case "worktree is unchanged", "no changes during verification", "worktree is clean", "worktree is dirty", "head equals base commit":
		default:
			return fmt.Errorf("%w: unsupported typed Git assertion", ErrUnobservableRequiredCheck)
		}
	}
	return nil
}

// RequiresObservation separates executable evidence from authored analysis.
// A CUSTOM command still requires its actual execution evidence. A CUSTOM
// instruction must be covered by the indexed analysis without inventing an
// observed exit code, pass verdict or command.
func (value VerificationCheck) RequiresObservation() bool {
	return value.Kind != VerificationCheckCustom || strings.TrimSpace(value.Command) != ""
}

func validateVerificationCheck(value VerificationCheck) error {
	switch value.Kind {
	case VerificationCheckTestCommand, VerificationCheckStaticAnalysis, VerificationCheckDiff, VerificationCheckGitAssertion, VerificationCheckCustom:
	default:
		return errors.New("unknown verification check kind")
	}
	if (strings.TrimSpace(value.Command) == "") == (strings.TrimSpace(value.Instruction) == "") {
		return errors.New("verification check requires exactly one command or instruction")
	}
	if len(value.Command) > MaxVerificationCheckValueBytes || len(value.Instruction) > MaxVerificationCheckValueBytes || len(value.Paths) > MaxVerificationPathCount || len(value.Packages) > MaxVerificationPathCount {
		return errors.New("verification check exceeds bounds")
	}
	for _, item := range append(append([]string(nil), value.Paths...), value.Packages...) {
		if strings.TrimSpace(item) == "" || len(item) > MaxVerificationPathBytes {
			return errors.New("verification check path or package is required and must be bounded")
		}
	}
	return nil
}

func CloneWorkSpec(value WorkSpec) WorkSpec {
	if value.CommandPermissions != nil {
		p := *value.CommandPermissions
		p.VerificationOutputs = append([]string(nil), p.VerificationOutputs...)
		value.CommandPermissions = &p
	}
	value.Constraints = append([]string(nil), value.Constraints...)
	value.ExecutionScope.Paths = append([]string(nil), value.ExecutionScope.Paths...)
	return value
}

func CloneVerificationSpec(value VerificationSpec) VerificationSpec {
	value.Checks = append([]VerificationCheck(nil), value.Checks...)
	for index := range value.Checks {
		value.Checks[index].RepositoryIDs = append([]string(nil), value.Checks[index].RepositoryIDs...)
		value.Checks[index].Paths = append([]string(nil), value.Checks[index].Paths...)
		value.Checks[index].Packages = append([]string(nil), value.Checks[index].Packages...)
	}
	value.AcceptanceCriteria = append([]string(nil), value.AcceptanceCriteria...)
	if value.Waiver != nil {
		waiver := *value.Waiver
		value.Waiver = &waiver
	}
	return value
}
