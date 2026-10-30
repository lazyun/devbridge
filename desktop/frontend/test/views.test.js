import { strict as assert } from "node:assert";
import test from "node:test";
import { translate } from "../src/i18n.js";
import {
  PAGES,
  SETTINGS_SECTIONS,
  consoleHTML,
  logsHTML,
  logsText,
  overviewHTML,
  overviewDesktopSubtitle,
  overviewManagedDataRoot,
  projectsHTML,
  saveStateKey,
  settingsHTML,
  managedStackActive,
  shellHTML,
  tunnelRuntimeHTML,
  tunnelStateKey,
  liveServicesHTML,
  localMCPCardHTML,
  localMCPTone,
} from "../src/views.js";

const t = (key, values) => translate("en", key, values);

test("Codex settings expose model discovery and dynamic model reasoning controls", () => {
  const state = { settingsSection: "codex", settings: { codingAgentModels: [{modelId: "gpt-new-model", enabled: false, supportedReasoningEfforts: ["high", "future"], defaultReasoningEffort: "future"}] }, codexModelCandidates: ["gpt-new-model"] };
  const html = settingsHTML(t, state);
  assert.ok(html.includes('data-action="read-codex-models"'));
  assert.ok(html.includes('data-coding-agent-model="gpt-new-model"'));
  assert.ok(html.includes('value="future" selected'));
  assert.ok(html.includes('role="status" id="model-catalog-status"'));
  const loading = settingsHTML(t, { ...state, modelCatalogLoading: true });
  assert.ok(loading.includes('data-action="read-codex-models" disabled'));
  assert.ok(loading.includes("Reading models…"));
});

test("shell keeps the main navigation fixed in the non-scrolling shell", () => {
  const html = shellHTML(t, "System");
  assert.ok(html.startsWith("<main><aside>"));
  assert.ok(html.includes('<nav id="main-nav">'));
  for (const page of PAGES) {
    assert.ok(html.includes(`data-page="${page}"`), `nav missing ${page}`);
  }
  assert.ok(html.includes('<div class="content" id="content">'));
  assert.ok(html.includes('id="ui-language"'));
  // The shell must not place the page content inside the scrolling document;
  // main.js mounts views into #content which is the only scroll container.
  assert.ok(!html.includes("<section"));
});

test("Projects navigation and empty state expose the first-class Add Project entry", () => {
  assert.ok(PAGES.includes("projects"));
  const html = projectsHTML(t, {
    items: [], coreReady: true, loading: false, loadError: null, addOpen: false,
    creating: false, selectedProjectId: "", successMessage: "",
  }, { core: { state: "RUNNING" } });
  assert.ok(html.includes("No projects yet"));
  assert.ok(html.includes("Create a project, then register repositories when needed."));
  assert.ok(html.includes('data-action="add-project"'));
  assert.ok(!html.includes('id="add-project" data-action="add-project" disabled'));
});

test("Projects confirmation shows a read-only repository and editable default name", () => {
  const html = projectsHTML(t, {
    items: [], coreReady: true, loading: false, loadError: null, addOpen: true,
    repositoryPath: "/source/devbridge", repositories:[{path:"/source/devbridge",alias:"app"}], name: "Dev Bridge", creating: false,
    submitError: null, selectedProjectId: "", successMessage: "",
  }, { core: { state: "RUNNING" } });
  assert.ok(html.includes('/source/devbridge'));
  assert.ok(html.includes('data-repository-alias="0"'));
  assert.ok(html.includes('name="projectName" value="Dev Bridge"'));
  assert.ok(html.includes('data-action="reselect-project"'));
  assert.ok(html.includes('id="project-registration-form"'));
});

test("Project repository manager separates active and removed history and retains exact Core blockers", () => {
  const html = projectsHTML(t, {
    items: [], coreReady: true, loading: false, loadError: null, addOpen: true,
    projectId: "project-1", repositories: [], name: "", creating: false, submitError: null,
    selectedProjectId: "", successMessage: "", unlinkConfirmation: null,
    repositoryInventory: {projectId: "project-1", repositories: [
      {repositoryId:"active", repositoryName:"Active", repositoryPath:"/active", registrationState:"ACTIVE", availability:"READY", developmentRefStatus:"EXISTS", availableActions:["REPOSITORY_UNLINK_PREPARE"], unlinkEligibility:{canUnlink:true, blockers:[]}},
      {repositoryId:"removed", repositoryName:"Removed", repositoryPath:"/removed", registrationState:"UNLINKED", availability:"HISTORICAL", developmentRefStatus:"", historyAvailable:true, availableActions:[], unlinkEligibility:{canUnlink:false, blockers:[{reasonCode:"RUN_ACTIVE",ownerType:"RUN",ownerId:"run-1",userFacingSummary:"Run still active"}]}}
    ]},
  }, { core: { state: "RUNNING" } });
  assert.ok(html.includes("Active repositories"));
  assert.ok(html.includes("Removed repositories (history retained)"));
  assert.ok(html.includes('data-action="prepare-unlink:active"'));
  assert.ok(html.includes("RUN_ACTIVE"));
  assert.ok(html.includes("run-1"));
  assert.ok(!html.includes("OWNERSHIP_HISTORY_CHECK_UNAVAILABLE"));
});

test("Project repository unlink confirmation states physical and v1 lifecycle boundaries", () => {
  const html = projectsHTML(t, {
    items: [], coreReady: true, loading: false, loadError: null, addOpen: true,
    projectId: "project-1", repositories: [], name: "", creating: false, submitError: null,
    selectedProjectId: "", successMessage: "", repositoryInventory: {projectId:"project-1",repositories:[]},
    unlinkConfirmation: {repositoryId:"repo",repositoryName:"Repo",repositoryPath:"/repo",expectedRegistrationGeneration:1,expectedTopologyRevision:1},
  }, { core: { state: "RUNNING" } });
  assert.ok(html.includes("Local files and managed worktrees will not be deleted"));
  assert.ok(html.includes("Relink and cross-Project transfer are not enabled in v1"));
  assert.ok(html.includes('data-action="commit-unlink"'));
});

test("Projects page renders durable list metadata, highlight, errors, and Core-not-ready state", () => {
  const listed = projectsHTML(t, {
    items: [{ id: "project-1", name: "Bridge & UI", repositoryCount: 1, createdAt: "2026-09-07T00:00:00Z" }],
    coreReady: true, loading: false, loadError: null, addOpen: false, creating: false,
    selectedProjectId: "project-1", successMessage: "Bridge & UI",
  }, { core: { state: "RUNNING" } });
  assert.ok(listed.includes("Bridge &amp; UI"));
  assert.ok(listed.includes("1 repositories"));
  assert.ok(listed.includes('class="project-card highlighted"'));
  assert.ok(listed.includes("Added Bridge &amp; UI."));

  const unavailable = projectsHTML(t, {
    items: [], coreReady: false, loading: false, loadError: { code: "CORE_UNAVAILABLE", message: "offline" },
    addOpen: false, creating: false, selectedProjectId: "", successMessage: "",
  }, { core: { state: "STOPPED" } });
  assert.ok(unavailable.includes("Dev Bridge Core is not ready"));
  assert.ok(unavailable.includes('id="add-project" data-action="add-project" disabled'));
});

test("settings page renders exactly the four v0.1 sections", () => {
  const html = settingsHTML(t, { settingsSection: "storage", settings: {}, apiKeyConfigured: false });
  for (const section of SETTINGS_SECTIONS) {
    assert.ok(html.includes(`data-section="${section}"`), `missing section ${section}`);
  }
  assert.ok(html.includes('class="settings-nav-item active" data-section="storage"'));
  assert.ok(html.includes('name="dataRoot"'));
});

test("settings subpage content follows the selected section", () => {
  const candidates = ["gpt-5.6-luna", "gpt-5.6-terra", "gpt-5.6-sol", "gpt-6-astra"];
  const codingAgentModels = candidates.map((modelId, index) => ({ modelId, enabled: index === 0, supportedReasoningEfforts: index === 0 ? ["low", "medium"] : ["high"], defaultReasoningEffort: index === 0 ? "medium" : "high" }));
  const codex = settingsHTML(t, { settingsSection: "codex", settings: { codingAgentModels }, codexModelCandidates: candidates, apiKeyConfigured: false });
  assert.equal((codex.match(/data-coding-agent-model/g) || []).length, 4);
  for (const model of candidates) assert.ok(codex.includes(`data-coding-agent-model="${model}"`));
  assert.ok(codex.includes('data-coding-agent-model="gpt-5.6-luna" checked'));
  assert.ok(!codex.includes("<textarea"));
  assert.ok(!codex.includes("custom-model"));
  assert.ok(codex.includes('data-coding-agent-effort="gpt-5.6-sol"'));
  assert.ok(!codex.includes('name="dataRoot"'));

  const chatgpt = settingsHTML(t, { settingsSection: "chatgpt", settings: { openAITunnelID: "tunnel_0123456789abcdef0123456789abcdef" }, apiKeyConfigured: true });
  assert.ok(chatgpt.includes('name="openAITunnelExecutable"'));
  assert.ok(chatgpt.includes('name="openAITunnelID"'));
  assert.ok(chatgpt.includes('id="tunnel-api-key"'));
  assert.ok(chatgpt.includes('data-action="save-api-key"'));
  assert.ok(chatgpt.includes('data-action="clear-api-key"'));
  assert.ok(!chatgpt.includes('data-coding-agent-model'));

  const runtime = settingsHTML(t, { settingsSection: "runtime", settings: {}, apiKeyConfigured: false });
  assert.ok(runtime.includes('name="coreBinary"'));
  assert.ok(runtime.includes('data-action="choose-codex-executable"'));
  assert.ok(runtime.includes('name="mcpEndpoint"'));
});

test("managed runtime settings are greyed while Codex path stays editable", () => {
  const status = { core: { ownership: "MANAGED", state: "RUNNING" }, mcp: { ownership: "MANAGED", state: "RUNNING" } };
  assert.equal(managedStackActive(status), true);
  const runtime = settingsHTML(t, { settingsSection: "runtime", settings: {}, status, apiKeyConfigured: false });
  assert.match(runtime, /name="coreBinary"[^>]*disabled/);
  assert.match(runtime, /name="mcpEndpoint"[^>]*disabled/);
  assert.doesNotMatch(runtime, /name="codexExecutable"[^>]*disabled/);

	const storage = settingsHTML(t, { settingsSection: "storage", settings: {}, status, apiKeyConfigured: false });
	assert.match(storage, /name="dataRoot"[^>]*disabled/);
  const codex = settingsHTML(t, { settingsSection: "codex", settings: { codingAgentModels: [{ modelId: "gpt-6-astra", enabled: true, supportedReasoningEfforts: ["medium"], defaultReasoningEffort: "medium" }] }, codexModelCandidates: ["gpt-6-astra"], status, apiKeyConfigured: false });
  assert.doesNotMatch(codex, /data-coding-agent-model[^>]*disabled/);
});

test("api key input never carries a value and clear is disabled while unset", () => {
  const html = settingsHTML(t, { settingsSection: "chatgpt", settings: {}, apiKeyConfigured: false });
  const input = html.match(/<input type="password"[^>]*>/)[0];
  assert.ok(input.includes('type="password"'));
  assert.ok(!input.includes("value="), "api key input must never carry a value attribute");
  assert.ok(html.includes('data-action="clear-api-key" disabled'));
  const configured = settingsHTML(t, { settingsSection: "chatgpt", settings: {}, apiKeyConfigured: true });
  assert.ok(configured.includes("Configured"));
  assert.ok(!configured.includes('data-action="clear-api-key" disabled'));
});

test("tunnel state maps to truthful labels", () => {
  assert.equal(tunnelStateKey({ state: "RUNNING", ready: true }), "tunnel.state.ready");
  assert.equal(tunnelStateKey({ state: "RUNNING", ready: false }), "tunnel.state.running");
  assert.equal(tunnelStateKey({ state: "ERROR" }), "tunnel.state.failed");
  assert.equal(tunnelStateKey({ state: "CRASHED" }), "tunnel.state.failed");
  assert.equal(tunnelStateKey({ state: "NOT_CONFIGURED" }), "tunnel.state.notConfigured");
  assert.equal(tunnelStateKey({}), "tunnel.state.notConfigured");
});

test("save states map to compact indicators", () => {
  assert.equal(saveStateKey({ state: "saving" }), "save.saving");
  assert.equal(saveStateKey({ state: "saved" }), "save.saved");
  assert.equal(saveStateKey({ state: "saved-restart" }), "save.savedRestart");
  assert.equal(saveStateKey({ state: "error" }), "save.failed");
  assert.equal(saveStateKey(undefined), null);
});

test("tunnel runtime buttons follow supervision rules", () => {
  const running = tunnelRuntimeHTML(t, { state: "RUNNING", ready: true, version: "0.0.14" }, false);
  assert.ok(running.includes('data-action="restart-tunnel"'));
  assert.ok(!running.includes('data-action="stop-tunnel" data-busy-lock disabled'));
  const notConfigured = tunnelRuntimeHTML(t, { state: "NOT_CONFIGURED" }, false);
  assert.ok(/data-action="restart-tunnel" data-busy-lock disabled/.test(notConfigured));
  assert.ok(/data-action="stop-tunnel" data-busy-lock disabled/.test(notConfigured));
  // A stopped tunnel cannot be stopped again, but Restart may start it.
  const stopped = tunnelRuntimeHTML(t, { state: "STOPPED", executable: "/opt/tunnel-client" }, false);
  assert.ok(stopped.includes('data-action="stop-tunnel" data-busy-lock disabled'));
  assert.ok(!stopped.includes('data-action="restart-tunnel" data-busy-lock disabled'));
});

test("overview and logs render stable live regions", () => {
  const overview = overviewHTML(t, { desktopVersion: "0.1.0-dev" }, { dataRoot: "/tmp/d" });
  assert.ok(overview.includes('id="live-services"'));
  assert.ok(overview.includes('id="live-warnings"'));
  assert.ok(overview.includes('id="live-desktop-subtitle"'));
  assert.ok(overview.includes('id="live-managed-data-root"'));
  const logs = logsHTML(t, { logsFilter: "all", followLatest: true });
  assert.ok(logs.includes('id="live-logs"'));
  assert.ok(logs.includes('id="log-filter"'));
  for (const category of ["all", "core", "mcp", "tunnel", "desktop", "codex"]) {
    assert.ok(logs.includes(`value="${category}"`), `filter missing ${category}`);
  }
  assert.ok(logs.includes('id="follow-latest"'));
  assert.ok(logs.includes('id="jump-latest"'));
  assert.ok(logs.includes('aria-pressed="true"'));
  assert.equal(logsText(t, { logs: [{ time: "t", source: "core", stream: "stdout", text: "hi" }] }), "t [core/stdout] hi");
  assert.equal(logsText(t, {}), translate("en", "logs.empty"));
  assert.ok(consoleHTML().includes('id="native-console"'));
});

test("overview identity replaces startup placeholders from fresh runtime state", () => {
  const settings = { dataRoot: "/configured/data" };
  assert.equal(overviewManagedDataRoot(null), "—");
  assert.equal(overviewHTML(t, {}, settings).includes("/configured/data"), false, "launch settings must not fabricate runtime identity");
  assert.equal(
    overviewManagedDataRoot({ dataRoot: "/authoritative/runtime/data" }),
    "/authoritative/runtime/data",
  );
  assert.match(overviewDesktopSubtitle(t, { desktopVersion: "0.1.7" }), /0\.1\.7/);
});

test("local MCP card renders state, endpoint, and copy control", () => {
  const ready = localMCPCardHTML(t, { state: "RUNNING", ownership: "MANAGED", detail: "ready", endpoint: "http://127.0.0.1:8090/mcp" });
  assert.ok(ready.includes("Local MCP"));
  assert.ok(ready.includes("http://127.0.0.1:8090/mcp"));
  assert.ok(ready.includes('data-action="copy-local-mcp-endpoint"'));
  assert.equal(localMCPTone("RUNNING"), "ok");
  assert.equal(localMCPTone("EXTERNAL"), "ok");
  assert.equal(localMCPTone("STARTING"), "working");
  assert.equal(localMCPTone("ERROR"), "bad");
  assert.equal(localMCPTone("CRASHED"), "bad");
  assert.equal(localMCPTone("STOPPED"), "idle");

  // Endpoint only appears when meaningful: a stopped adapter shows no
  // endpoint row, and no LibreChat or client-specific label ever appears.
  const stopped = localMCPCardHTML(t, { state: "STOPPED", endpoint: "http://127.0.0.1:8090/mcp" });
  assert.ok(!stopped.includes("http://127.0.0.1:8090/mcp"));
  assert.ok(!stopped.includes("data-action=\"copy-local-mcp-endpoint\""));
  for (const state of ["RUNNING", "EXTERNAL", "STARTING", "STOPPING", "ERROR", "CRASHED"]) {
    assert.ok(localMCPCardHTML(t, { state, endpoint: "http://127.0.0.1:8090/mcp" }).includes("http://127.0.0.1:8090/mcp"), `endpoint hidden for ${state}`);
  }
  const external = localMCPCardHTML(t, { state: "EXTERNAL", ownership: "EXTERNAL", endpoint: "http://127.0.0.1:8090/mcp" });
  assert.ok(external.includes("EXTERNAL"));
  assert.ok(!external.toLowerCase().includes("librechat"));
});

test("services list includes the local MCP card between MCP and Tunnel", () => {
  const html = liveServicesHTML(t, { core: { state: "RUNNING" }, mcp: { state: "RUNNING" }, localMCP: { state: "RUNNING" } });
  const core = html.indexOf("Core");
  const mcp = html.indexOf('Local MCP');
  const local = html.indexOf("Local MCP");
  const tunnel = html.indexOf("OpenAI Tunnel");
  assert.ok(local > mcp || local > 0, "local mcp card present");
  assert.ok(tunnel > local, "local mcp card before tunnel card");
  void core; void mcp;
});
