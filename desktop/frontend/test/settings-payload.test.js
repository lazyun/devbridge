import { strict as assert } from "node:assert";
import test from "node:test";
import { ARRAY_FIELDS, buildSettingsPayload, nextCheckboxSelection, scalarFields, updateCodingAgentModel } from "../src/settings-payload.js";

test("scalar settings are serialized as strings", () => {
  const payload = buildSettingsPayload({
    dataRoot: "/tmp/bridge-data",
    coreBinary: "devbridge",
    mcpBinary: "devbridge-mcp",
    codexExecutable: "codex",
    openAITunnelExecutable: "/opt/bin/tunnel-client",
    openAITunnelID: "tunnel_0123456789abcdef0123456789abcdef",
    coreEndpoint: "http://127.0.0.1:8081",
    mcpEndpoint: "http://127.0.0.1:8080",
  });
  for (const name of scalarFields()) {
    assert.equal(typeof payload[name], "string", `${name} must be a string`);
  }
  assert.equal(payload.dataRoot, "/tmp/bridge-data");
  assert.equal(payload.openAITunnelExecutable, "/opt/bin/tunnel-client");
  assert.equal(payload.openAITunnelID, "tunnel_0123456789abcdef0123456789abcdef");
});

test("scalar values coming from a form are never arrays", () => {
  // Regression: the old FormData collector turned every field into an array,
  // producing "json: cannot unmarshal array into Go struct field
  // Settings.dataRoot of type string".
  const payload = buildSettingsPayload({ dataRoot: ["/tmp/from-array"] });
  assert.equal(payload.dataRoot, "/tmp/from-array");
  assert.ok(!Array.isArray(payload.dataRoot));
  const serialized = JSON.stringify(payload);
  assert.ok(!serialized.includes('"/tmp/from-array"],'), "no scalar field may serialize as an array");
});

test("model configurations preserve independent defaults and supported efforts", () => {
	const payload = buildSettingsPayload({
		codingAgentConfigVersion: 2,
		codingAgentModels: [{ modelId: "gpt-5.6-luna", enabled: true, supportedReasoningEfforts: ["low", "high", "low"], defaultReasoningEffort: "high" }],
	});
	assert.equal(payload.codingAgentConfigVersion, 2);
	assert.equal(typeof payload.codingAgentConfigVersion, "number");
	assert.deepEqual(payload.codingAgentModels, [{ modelId: "gpt-5.6-luna", enabled: true, supportedReasoningEfforts: ["low", "high"], defaultReasoningEffort: "high" }]);
  for (const name of ARRAY_FIELDS) {
    assert.ok(Array.isArray(payload[name]));
  }
});

test("model inventory accepts arrays only", () => {
	const payload = buildSettingsPayload({ codingAgentModels: "gpt-5.6-luna" });
	assert.deepEqual(payload.codingAgentModels, []);
});

test("discovered model Fast capabilities survive settings serialization", () => {
  for (const tier of ["priority", ""]) {
    const payload = buildSettingsPayload({ codingAgentModels: [{ modelId: "gpt-new-model", enabled: true, supportedReasoningEfforts: ["high", "future"], defaultReasoningEffort: "high", fastServiceTier: tier }] });
    assert.equal(payload.codingAgentModels[0].fastServiceTier, tier);
    assert.deepEqual(payload.codingAgentModels[0].supportedReasoningEfforts, ["high", "future"]);
  }
});

test("model checkbox transitions follow backend candidate order and expose empty selection", () => {
  const candidates = ["gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"];
  assert.deepEqual(
    nextCheckboxSelection("allowedModels", ["gpt-5.6-sol"], "gpt-5.6-luna", true, candidates),
    ["gpt-5.6-luna", "gpt-5.6-sol"],
  );
  assert.deepEqual(nextCheckboxSelection("allowedModels", ["gpt-5.6-sol"], "gpt-5.6-sol", false, candidates), []);
  assert.deepEqual(nextCheckboxSelection("allowedReasoningEfforts", ["low"], "high", true, candidates), ["low", "high"]);
});

test("secret material is never part of the settings payload", () => {
  const payload = buildSettingsPayload({
    dataRoot: "/tmp/bridge-data",
    codexModelCandidates: ["gpt-6-astra"],
    tunnelApiKey: "sk-plaintext-never",
    tunnelApiKeyConfigured: true,
  });
  const serialized = JSON.stringify(payload);
  assert.ok(!serialized.includes("sk-plaintext-never"));
  assert.ok(!("tunnelApiKey" in payload));
  assert.ok(!("tunnelApiKeyConfigured" in payload));
  assert.ok(!("codexModelCandidates" in payload));
});

test("empty model configuration serializes as an empty array for backend validation", () => {
	const payload = buildSettingsPayload({ codingAgentModels: [] });
	assert.deepEqual(payload.codingAgentModels, []);
});

test("changing one model does not change another model default", () => {
	const models = [{ modelId: "a", enabled: true, supportedReasoningEfforts: ["low", "medium"], defaultReasoningEffort: "medium" }, { modelId: "b", enabled: true, supportedReasoningEfforts: ["high"], defaultReasoningEffort: "high" }];
	assert.deepEqual(updateCodingAgentModel(models, "a", { defaultReasoningEffort: "low" })[1], models[1]);
});

import { applySaveFailure, applySaveSuccess } from "../src/settings-payload.js";

const fullSettings = {
  dataRoot: "/tmp/bridge-data",
  coreBinary: "devbridge",
  mcpBinary: "devbridge-mcp",
  codexExecutable: "codex",
  openAITunnelExecutable: "/old/tunnel-client",
  openAITunnelID: "tunnel_0123456789abcdef0123456789abcdef",
	codingAgentModels: [{ modelId: "gpt-5.6-luna", enabled: true, supportedReasoningEfforts: ["low", "medium"], defaultReasoningEffort: "medium" }],
  codingAgentConfigVersion: 2,
  coreEndpoint: "http://127.0.0.1:8081",
  mcpEndpoint: "http://127.0.0.1:8080",
};

test("successful save reconciles state and names the inputs to sync immediately", () => {
  const previous = { ...fullSettings };
  const payload = buildSettingsPayload({ ...fullSettings, openAITunnelExecutable: "/new/tunnel-client" });
  const applied = applySaveSuccess(previous, payload);
  assert.equal(applied.settings.openAITunnelExecutable, "/new/tunnel-client");
  assert.deepEqual(applied.syncInputs, ["openAITunnelExecutable"]);
});

test("successful save synchronizes the model configuration", () => {
  const payload = buildSettingsPayload({
    ...fullSettings,
		codingAgentModels: [{ modelId: "gpt-5.6-luna", enabled: true, supportedReasoningEfforts: ["low", "medium"], defaultReasoningEffort: "low" }],
  });
  const applied = applySaveSuccess(fullSettings, payload);
	assert.deepEqual(applied.syncInputs, ["codingAgentModels"]);
});

test("unchanged saves name no inputs, so the view is not disturbed", () => {
  const payload = buildSettingsPayload(fullSettings);
  const applied = applySaveSuccess(fullSettings, payload);
  assert.deepEqual(applied.syncInputs, []);
});

test("failed save rolls back only the attempted scalar change", () => {
  const applied = applySaveFailure(fullSettings, { openAITunnelID: "tunnel_bad" });
  assert.equal(applied.settings, fullSettings);
  assert.deepEqual(applied.revertInputs, ["openAITunnelID"]);
});

test("failed save reverts the editable model inventory", () => {
  const applied = applySaveFailure(fullSettings, {
		codingAgentModels: [],
  });
	assert.deepEqual(applied.revertInputs, ["codingAgentModels"]);
  assert.equal(applied.settings, fullSettings);
});
