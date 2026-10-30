package codexruntime

// MergeCatalog updates capabilities while retaining existing enablement and
// supported defaults. New models are disabled until the user enables them.
// Previously configured models stay available when absent from this read.
func (a Allowlist) MergeCatalog(discovered []CodingAgentModelConfig) (Allowlist, error) {
	current, err := a.Normalize()
	if err != nil {
		return Allowlist{}, err
	}
	// Validate the whole read before replacing any settings. Enable a detached
	// copy only for validation; the actual new models remain disabled.
	catalog := cloneModelConfigs(discovered)
	for i := range catalog {
		catalog[i].Enabled = true
	}
	if err := validateModelConfigs(catalog); err != nil {
		return Allowlist{}, err
	}
	index := make(map[string]int, len(current.ModelConfigs))
	for i, config := range current.ModelConfigs {
		index[config.ModelID] = i
	}
	for _, config := range catalog {
		if i, exists := index[config.ModelID]; exists {
			previous := current.ModelConfigs[i]
			config.Enabled = previous.Enabled
			if contains(config.SupportedReasoningEfforts, previous.DefaultReasoningEffort) {
				config.DefaultReasoningEffort = previous.DefaultReasoningEffort
			}
			current.ModelConfigs[i] = config
		} else {
			config.Enabled = false
			current.ModelConfigs = append(current.ModelConfigs, config)
		}
	}
	return current.Normalize()
}
