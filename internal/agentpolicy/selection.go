// Package agentpolicy holds prospective execution-policy selections without
// depending on Core or a particular agent transport.
package agentpolicy

import "errors"

const (
	CooperativeAdapterIdentity  = "cooperative-trusted-agent-v1"
	CooperativeProtocolIdentity = "cooperative-trusted-policy-v1"
	PolicyTaskSandboxV1         = "TASK_SANDBOX_V1"
	PolicyCooperativeTrustedV1  = "COOPERATIVE_TRUSTED_V1"
)

func ValidateSelection(agentID, policy string) error {
	switch policy {
	case "", PolicyTaskSandboxV1:
		if agentID != "" && agentID != "codex" {
			return errors.New("native task policy currently requires codex")
		}
	case PolicyCooperativeTrustedV1:
		if agentID != "codex" && agentID != "pi" {
			return errors.New("cooperative policy requires an explicit registered agent identity")
		}
	default:
		return errors.New("unsupported execution policy")
	}
	return nil
}
