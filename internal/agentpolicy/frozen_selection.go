package agentpolicy

import "errors"

// ValidateFrozenSelection binds cooperative trust to the exact configured
// launcher/arguments. Native configuration retains its original separate
// backend fingerprint; an optional cooperative fingerprint cannot affect it.
func ValidateFrozenSelection(agentID, policy, fingerprint string) error {
	if err := ValidateSelection(agentID, policy); err != nil {
		return err
	}
	if policy != PolicyCooperativeTrustedV1 {
		if fingerprint != "" {
			return errors.New("agent configuration fingerprint requires cooperative policy")
		}
		return nil
	}
	if !ValidConfigFingerprint(fingerprint) {
		return errors.New("cooperative policy requires an exact configured agent fingerprint")
	}
	return nil
}

func ValidConfigFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			if ch < 'a' || ch > 'f' {
				return false
			}
		}
	}
	return true
}
