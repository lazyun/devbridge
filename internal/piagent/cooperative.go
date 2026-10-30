package piagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/lazyun/devbridge/internal/agent"
	"github.com/lazyun/devbridge/internal/agentpolicy"
)

// StartAgent is an explicitly selected cooperative launch, never a fallback for
// a native task sandbox. Policy bytes travel in Pi's system-context channel;
// the engineering instruction is supplied later as an unchanged RPC prompt.
func (f *Factory) StartAgent(ctx context.Context, request agent.LaunchRequest) (agent.Session, error) {
	if request.Policy == nil || request.Policy.Validate() != nil || request.Policy.AgentID != "pi" || request.Policy.RunID != request.RunID || request.Policy.CWD != request.CWD {
		return nil, errors.New("pi requires an exact validated cooperative launch policy")
	}
	if request.RuntimeBinding.TaskPermissions != nil {
		return nil, errors.New("pi cooperative launch cannot downgrade native task permissions")
	}
	if request.RuntimeBinding.RuntimeWorkspaceRoots != nil && !reflect.DeepEqual(request.RuntimeBinding.RuntimeWorkspaceRoots, request.Policy.ReadableRoots) || request.RuntimeBinding.WritableRoots != nil && !reflect.DeepEqual(request.RuntimeBinding.WritableRoots, request.Policy.WritableRoots) {
		return nil, errors.New("pi cooperative scope differs from explicit runtime binding")
	}
	if request.OnPolicyConfigured == nil {
		return nil, errors.New("pi launch policy recording callback is required")
	}
	if request.Runtime.Model == "" || request.Runtime.ReasoningEffort == "" {
		return nil, errors.New("pi cooperative launch requires frozen model and thinking level")
	}
	if request.Runtime.ServiceTier != "" {
		return nil, &agent.CapabilityError{AgentID: "pi", Missing: []agent.Capability{agent.CapabilityServiceTier}}
	}
	snapshot := *f
	snapshot.ExtraArgs = append([]string(nil), f.ExtraArgs...)
	fingerprint, configured, err := snapshot.configurationIdentity()
	if err != nil || fingerprint == "" || request.AgentConfigFingerprint != fingerprint || request.Policy.AgentConfigFingerprint != fingerprint || f.configurationFingerprint() != fingerprint {
		return nil, errors.New("pi configured launcher differs from frozen selection")
	}
	if request.Backend.Executable != "" && Resolve(request.Backend.Executable) != configured {
		return nil, errors.New("pi executable differs from explicitly selected launcher")
	}
	policy, err := request.Policy.Seal()
	if err != nil {
		return nil, err
	}
	factory := snapshot
	factory.PiPath = configured
	factory.Model = request.Runtime.Model
	factory.ThinkingLevel = request.Runtime.ReasoningEffort
	s := factory.newSession()
	s.policy = &policy
	s.launchConfig = &snapshot
	s.launchConfigFingerprint = fingerprint
	if _, err := s.StartThread(ctx, request.CWD); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := s.verifyLaunchConfiguration(); err != nil || f.configurationFingerprint() != fingerprint {
		return nil, errors.Join(errors.New("pi configuration changed during startup"), err, s.Close())
	}
	receipt := agentpolicy.LaunchReceipt{RunID: request.RunID, AgentID: "pi", AgentConfigFingerprint: fingerprint, PolicySHA256: policy.SHA256, Mechanism: "PI_APPEND_SYSTEM_PROMPT_FILE", Status: agentpolicy.LaunchConfigured, Executable: s.client.cmd.Path, Args: append([]string(nil), s.client.cmd.Args[1:]...)}
	if err := request.OnPolicyConfigured(receipt); err != nil {
		return nil, errors.Join(err, s.Close())
	}
	if err := s.verifyLaunchConfiguration(); err != nil || f.configurationFingerprint() != fingerprint {
		return nil, errors.Join(errors.New("pi configuration changed before task dispatch"), err, s.Close())
	}
	return s, nil
}

// A transport-owned input file is not a writable engineering root. Its exact
// bytes are already in Core's frozen envelope; this file only carries them to
// Pi's documented --append-system-prompt channel before task execution.
type policyInput struct{ directory, path, digest string }

func preparePolicyInput(policy *agentpolicy.Envelope) (policyInput, error) {
	if policy == nil {
		return policyInput{}, nil
	}
	if err := policy.Validate(); err != nil {
		return policyInput{}, err
	}
	directory, err := os.MkdirTemp("", "devbridge-pi-policy-")
	if err != nil {
		return policyInput{}, err
	}
	input := policyInput{directory: directory, path: filepath.Join(directory, "system-context.txt"), digest: policy.SHA256}
	if err := os.WriteFile(input.path, []byte(policy.SystemText), 0600); err != nil {
		_ = input.remove()
		return policyInput{}, err
	}
	if err := input.verify(); err != nil {
		_ = input.remove()
		return policyInput{}, err
	}
	return input, nil
}

func (p policyInput) verify() error {
	if p.path == "" {
		return nil
	}
	info, err := os.Lstat(p.path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("pi system-policy input is not a private regular file")
	}
	data, err := os.ReadFile(p.path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != p.digest {
		return errors.New("pi system-policy input differs from frozen bytes")
	}
	return nil
}

func (p policyInput) remove() error {
	if p.path == "" {
		return nil
	}
	fileErr := os.Remove(p.path)
	if errors.Is(fileErr, os.ErrNotExist) {
		fileErr = nil
	}
	dirErr := os.Remove(p.directory)
	if errors.Is(dirErr, os.ErrNotExist) {
		dirErr = nil
	}
	return errors.Join(fileErr, dirErr)
}

// Preserve documented resource/tool/diagnostic configuration while rejecting
// arguments that replace frozen model/session/policy inputs or start a hidden
// prompt. Unknown extension flags are ambiguous and require explicit support.
func validateExtraArgs(args []string) error {
	flags := map[string]bool{"--verbose": true, "--offline": true, "--no-extensions": true, "-ne": true, "--no-mcp": true, "--no-skills": true, "-ns": true, "--no-prompt-templates": true, "-np": true, "--no-themes": true, "--no-context-files": true, "-nc": true, "--no-approve": true, "-na": true}
	values := map[string]bool{"--extension": true, "-e": true, "--skill": true, "--prompt-template": true, "--tools": true, "-t": true, "--exclude-tools": true, "-xt": true, "--theme": true, "--use-theme": true}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if flags[arg] {
			continue
		}
		key, value, inline := strings.Cut(arg, "=")
		if !values[key] {
			return fmt.Errorf("pi extra arguments contain unsupported or conflicting option %q", arg)
		}
		if inline {
			return fmt.Errorf("pi extra arguments require separate option/value tokens for %s", key)
		}
		if !inline {
			i++
			if i == len(args) {
				return fmt.Errorf("pi extra arguments omit value for %s", key)
			}
			value = args[i]
		}
		toolList := key == "--tools" || key == "-t" || key == "--exclude-tools" || key == "-xt"
		if value == "" || strings.HasPrefix(value, "--") || !toolList && strings.HasPrefix(value, "-") || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("pi extra arguments contain invalid value for %s", key)
		}
	}
	return nil
}

var _ agent.Factory = (*Factory)(nil)
