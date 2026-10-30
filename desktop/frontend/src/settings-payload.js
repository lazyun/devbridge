// Pure settings serialization shared by the Desktop UI. Text settings must
// reach the Go backend as JSON strings; the configuration version remains a
// JSON number and the model inventory remains an array. Keeping this DOM-free
// makes the contract unit-testable.

export const ARRAY_FIELDS = ["codingAgentModels", "piAgentModels"];
export const NUMBER_FIELDS = ["codingAgentConfigVersion", "piAgentConfigVersion"];

export const SETTINGS_FIELDS = [
  "dataRoot",
  "coreBinary",
  "mcpBinary",
  "localMCPBinary",
  "codexExecutable",
  "piExecutable",
  "piModel",
  "piThinkingLevel",
  "openAITunnelExecutable",
  "openAITunnelID",
  "codingAgentModels",
  "codingAgentConfigVersion",
  "piAgentModels",
  "piAgentConfigVersion",
  "coreEndpoint",
  "mcpEndpoint",
  "localMCPEndpoint",
];

export function scalarFields() {
  return SETTINGS_FIELDS.filter((name) => !ARRAY_FIELDS.includes(name) && !NUMBER_FIELDS.includes(name));
}

// nextCheckboxSelection applies one checkbox transition without mutating the
// current selection. Models are returned in the backend-provided candidate
// order; the caller owns the non-empty invariant and user-facing error.
export function nextCheckboxSelection(name, selected, value, checked, modelCandidates = []) {
  const next = new Set(Array.isArray(selected) ? selected : []);
  if (checked) next.add(value);
  else next.delete(value);
  return name === "allowedModels"
    ? modelCandidates.filter((model) => next.has(model))
    : [...next];
}

// buildSettingsPayload returns the JSON-ready settings object. Scalar fields
// are always strings (never single-element arrays); array fields are
// deduplicated string arrays.
export function buildSettingsPayload(values) {
  const payload = {};
  for (const name of SETTINGS_FIELDS) {
    const raw = values?.[name];
    if (ARRAY_FIELDS.includes(name)) {
      payload[name] = Array.isArray(raw) ? raw.map((model) => ({
        modelId: String(model?.modelId ?? "").trim(),
        enabled: Boolean(model?.enabled),
        supportedReasoningEfforts: Array.isArray(model?.supportedReasoningEfforts) ? [...new Set(model.supportedReasoningEfforts.map((value) => String(value).trim()).filter(Boolean))] : [],
        defaultReasoningEffort: String(model?.defaultReasoningEffort ?? "").trim(),
        ...(model?.fastServiceTier != null ? { fastServiceTier: String(model.fastServiceTier) } : {}),
        ...(name === "piAgentModels" ? {
          provider: String(model?.provider ?? ""),
          contextWindow: String(model?.contextWindow ?? ""),
          maxOutput: String(model?.maxOutput ?? ""),
          supportsThinking: model?.supportsThinking === true,
          supportsImages: model?.supportsImages === true,
        } : {}),
      })) : [];
    } else if (NUMBER_FIELDS.includes(name)) {
      payload[name] = Number.isInteger(Number(raw)) ? Number(raw) : 0;
    } else {
      payload[name] = typeof raw === "string" ? raw : String(raw ?? "");
    }
  }
  return payload;
}

// applySaveSuccess reconciles local state after the backend accepted a save.
// It returns the authoritative settings plus the names of fields whose
// rendered inputs must be updated immediately (including checkbox groups) so
// the current view reflects the saved value without a section switch or poll.
export function applySaveSuccess(previousValues, payload) {
  const syncInputs = [];
  for (const name of SETTINGS_FIELDS) {
    // Config versions are metadata with no rendered input; they must never
    // appear in the sync list.
    if (NUMBER_FIELDS.includes(name)) continue;
    if (ARRAY_FIELDS.includes(name)) {
      if (JSON.stringify(previousValues?.[name] || []) !== JSON.stringify(payload?.[name] || [])) syncInputs.push(name);
      continue;
    }
    if (String(previousValues?.[name] ?? "") !== String(payload?.[name] ?? "")) {
      syncInputs.push(name);
    }
  }
  return { settings: payload, syncInputs };
}

// applySaveFailure keeps the previously persisted values: only the attempted
// change is rolled back, including an entire same-name checkbox group.
export function applySaveFailure(previousValues, changed) {
  const revertInputs = Object.keys(changed || {}).filter(
    (name) => SETTINGS_FIELDS.includes(name),
  );
  return { settings: previousValues, revertInputs };
}

export function updateCodingAgentModel(models, modelId, change) {
  return (Array.isArray(models) ? models : []).map((model) => model.modelId === modelId ? { ...model, ...change } : model);
}

export function updatePiAgentModel(models, modelId, change) {
  return (Array.isArray(models) ? models : []).map((model) => model.modelId === modelId ? { ...model, ...change } : model);
}
