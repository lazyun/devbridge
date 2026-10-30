// Pure, DOM-free view templates for the Desktop shell. Every function takes
// its inputs explicitly so the rendered structure is unit-testable with
// node:test. Event binding and live patching live in main.js.

import { LOG_CATEGORIES } from "./log-view.js";
import { piModelPickerHTML } from "./pi-model-picker.js";

const escapeHTML = (value) =>
  String(value ?? "").replace(/[&<>'"]/g, (char) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "'": "&#39;", '"': "&quot;",
  })[char]);

export const PAGES = ["overview", "projects", "sessions", "console", "logs", "settings"];
export const SETTINGS_SECTIONS = ["storage", "codex", "pi", "chatgpt", "runtime"];

export const REASONING_OPTIONS = [
  ["low", "Low"],
  ["medium", "Medium"],
  ["high", "High"],
  ["xhigh", "XHigh"],
];

const TUNNEL_STATE_KEYS = {
  NOT_CONFIGURED: "tunnel.state.notConfigured",
  STARTING: "tunnel.state.starting",
  STOPPING: "tunnel.state.stopping",
  RUNNING: null,
  STOPPED: "tunnel.state.stopped",
  CRASHED: "tunnel.state.failed",
  ERROR: "tunnel.state.failed",
  EXTERNAL: "tunnel.state.external",
};

export function tunnelStateKey(tunnel) {
  const tunnelState = tunnel?.state || "NOT_CONFIGURED";
  if (tunnelState === "RUNNING") return tunnel?.ready ? "tunnel.state.ready" : "tunnel.state.running";
  return TUNNEL_STATE_KEYS[tunnelState] || "tunnel.state.stopped";
}

export function tunnelTone(tunnel) {
  const tunnelState = tunnel?.state || "NOT_CONFIGURED";
  if (["RUNNING", "EXTERNAL"].includes(tunnelState)) return "ok";
  if (["STARTING", "STOPPING"].includes(tunnelState)) return "working";
  if (["CRASHED", "ERROR"].includes(tunnelState)) return "bad";
  return "idle";
}

const SAVE_STATE_KEYS = {
  saving: "save.saving",
  saved: "save.saved",
  "saved-restart": "save.savedRestart",
  error: "save.failed",
};

export function saveStateKey(saveState) {
  return SAVE_STATE_KEYS[saveState?.state] ?? null;
}

export function shellHTML(t, langLabel) {
  return `<main><aside><div class="brand">DEV BRIDGE <small>DESKTOP</small></div><nav id="main-nav">${PAGES.map((page) => `<button class="nav-item" data-page="${page}">${escapeHTML(t("nav." + page))}</button>`).join("")}</nav><div class="sidebar-foot"><label class="sidebar-lang">${escapeHTML(t("settings.language"))}<select id="ui-language">${["system", "en", "zh-CN"].map((value) => `<option value="${value}" ${value === "system" ? `data-label="${escapeHTML(langLabel)}"` : ""}>${escapeHTML(value === "system" ? langLabel : value === "en" ? "English" : "简体中文")}</option>`).join("")}</select></label></div></aside><div class="content" id="content"></div></main>`;
}

function serviceCard(name, service) {
  const serviceState = service?.state || "STOPPED";
  const detail = [service?.ownership, serviceState, service?.detail].filter(Boolean).join(" · ");
  const build = service?.build || "—";
  const tone = ["RUNNING", "EXTERNAL"].includes(serviceState) ? "ok" : ["STARTING", "STOPPING"].includes(serviceState) ? "working" : ["ERROR", "CRASHED"].includes(serviceState) ? "bad" : "idle";
  return `<article class="service"><div><h2><span class="status ${tone}"></span>${escapeHTML(name)}</h2><p>${escapeHTML(detail)}</p></div><code>${escapeHTML(build)}</code></article>`;
}

export function tunnelCardHTML(t, tunnel) {
  const label = t(tunnelStateKey(tunnel));
  const tone = tunnelTone(tunnel);
  const detail = [label, tunnel?.lastError || tunnel?.detail].filter(Boolean).join(" · ");
  return `<article class="service"><div><h2><span class="status ${tone}"></span>${escapeHTML(t("service.tunnel"))}</h2><p>${escapeHTML(detail)}</p></div><code>${escapeHTML(tunnel?.version || "—")}</code></article>`;
}

// localMCPCardHTML renders the Local MCP Adapter runtime card. The endpoint
// their last configured endpoint; an unconfigured component shows none).
export function localMCPCardHTML(t, localMCP) {
  const state = localMCP?.state || "STOPPED";
  const detail = [localMCP?.ownership, state, localMCP?.detail].filter(Boolean).join(" · ");
  const endpoint = localMCP?.endpoint && endpointMeaningful(state) ? `<code class="endpoint">${escapeHTML(localMCP.endpoint)}</code><button type="button" class="copy-endpoint" data-action="copy-local-mcp-endpoint" data-endpoint="${escapeHTML(localMCP.endpoint)}">${escapeHTML(t("action.copy"))}</button>` : "";
  return `<article class="service"><div><h2><span class="status ${localMCPTone(state)}"></span>${escapeHTML(t("service.localMCP"))}</h2><p>${escapeHTML(detail)}</p><p class="muted">${escapeHTML(t("service.localMCP.copy"))}</p>${endpoint}</article>`;
}

function endpointMeaningful(state) {
  return ["RUNNING", "EXTERNAL", "STARTING", "STOPPING", "ERROR", "CRASHED"].includes(state);
}

export function localMCPTone(state) {
  if (["RUNNING", "EXTERNAL"].includes(state)) return "ok";
  if (["STARTING", "STOPPING"].includes(state)) return "working";
  if (["ERROR", "CRASHED"].includes(state)) return "bad";
  return "idle";
}

export function chatgptCardHTML(t, integration) {
  const available = integration === "Available";
  const tone = available ? "ok" : "idle";
  const label = available ? t("chatgpt.available") : t("chatgpt.unavailable");
  return `<article class="service"><div><h2><span class="status ${tone}"></span>${escapeHTML(t("service.chatgpt"))}</h2><p>${escapeHTML(label)} · ${escapeHTML(t("service.tunnel.copy"))}</p></div><code>—</code></article>`;
}

function codexCard(t, codex) {
  const label = codex?.status === "available" ? t("codex.available") : codex?.status === "error" ? t("codex.error") : t("codex.missing");
  const tone = codex?.status === "available" ? "ok" : codex?.status === "error" ? "bad" : "idle";
  return `<article class="service"><div><h2><span class="status ${tone}"></span>${escapeHTML(t("service.codex"))}</h2><p>${escapeHTML(label)}${codex?.error ? ` · ${escapeHTML(codex.error)}` : ""}</p></div><code>${escapeHTML(codex?.version || "—")}</code></article>`;
}

export function overviewHTML(t, status, settings) {
  const warnings = [
    status?.buildMismatch ? t("warning.buildMismatch") : "",
    status?.lastError,
  ].filter(Boolean);
  return `<section class="panel">
    <div class="title-row"><div><h1>${escapeHTML(t("app.title"))}</h1><p class="muted" id="live-desktop-subtitle" data-live="desktop-subtitle">${escapeHTML(overviewDesktopSubtitle(t, status))}</p></div><div class="actions"><button data-action="start" data-busy-lock>${escapeHTML(t("action.start"))}</button><button data-action="stop" data-busy-lock>${escapeHTML(t("action.stop"))}</button><button class="primary" data-action="restart" data-busy-lock>${escapeHTML(t("action.restart"))}</button></div></div>
    <div id="live-warnings" data-live="warnings">${warnings.map((warning) => `<p class="warning">${escapeHTML(warning)}</p>`).join("")}</div>
    <div class="data-root"><strong>${escapeHTML(t("overview.managedDataRoot"))}</strong><code id="live-managed-data-root" data-live="managed-data-root">${escapeHTML(overviewManagedDataRoot(status))}</code></div>
    <div class="service-list" id="live-services" data-live="services"></div>
    <p class="hint">${escapeHTML(t("overview.ownershipHint"))}</p>
  </section>`;
}

export function overviewDesktopSubtitle(t, status) {
  return t("app.subtitle", { version: status?.desktopVersion || "0.1.0-dev" });
}

export function overviewManagedDataRoot(status) {
  // Status.dataRoot is published only from a successful managed-runtime
  // identity readback. Settings are launch intent, not runtime identity.
  return status?.dataRoot || "—";
}

// Desktop renders its own task workbench; Core supplies data and admission.
export function consoleHTML() {
  return `<section id="native-console" class="nc-shell" aria-label="Task workbench"></section>`;
}

// The Run sessions tab keeps the embedded web workbench: it is an
// interactive stream surface, not a summary.
export function sessionsHTML(t) {
  const url = "/ui/?workbench=sessions&layout=embedded";
  return `<section class="sessions-panel"><div class="sessions-embed-container"><iframe class="sessions-embed-frame" title="${escapeHTML(t("nav.sessions"))}" src="${escapeHTML(url)}" referrerpolicy="no-referrer"></iframe></div><div class="sessions-recovery"><button type="button" data-embed-reload>${escapeHTML(t("sessions.reload"))}</button></div></section>`;
}

function projectCardHTML(t, project, highlighted) {
  const context = project.projectContextStatus ? `<span>${escapeHTML(t("projects.context"))}: ${escapeHTML(project.projectContextStatus)}</span>` : "";
  const created = project.createdAt ? String(project.createdAt).slice(0, 10) : "";
  return `<article class="project-card ${highlighted ? "highlighted" : ""}" data-project-id="${escapeHTML(project.id)}"><div><h2>${escapeHTML(project.name)}</h2><p class="muted">${escapeHTML(t("projects.repositories", { count: project.repositoryCount ?? 0 }))}</p></div><div class="project-meta">${context}<button data-action="manage-project:${escapeHTML(project.id)}">${escapeHTML(t("projects.manage"))}</button>${created ? `<span>${escapeHTML(t("projects.added"))}: ${escapeHTML(created)}</span>` : ""}</div></article>`;
}

function projectRegistrationHTML(t, state) {
  if (!state.addOpen) return "";
  const error = state.submitError ? `<div class="form-error" role="alert"><strong>${escapeHTML(state.submitError.code || t("projects.error"))}</strong><span>${escapeHTML(state.submitError.message || t("projects.registrationFailed"))}</span></div>` : "";
  const pending = !!state.pendingRegistration;
  const inventory = state.repositoryInventory;
  const repositoryRow = (r) => {
    const eligibility = r.unlinkEligibility || {};
    const blockers = Array.isArray(eligibility.blockers) ? eligibility.blockers : [];
    const canUnlink = eligibility.canUnlink === true && (r.availableActions || []).includes("REPOSITORY_UNLINK_PREPARE");
    const blockerDetail = blockers.map(b => `<li><code>${escapeHTML(b.reasonCode || b.blockerKind)}</code> · ${escapeHTML(b.ownerType || b.entityType)} <code>${escapeHTML(b.ownerId || b.entityId)}</code>${b.userFacingSummary ? `: ${escapeHTML(b.userFacingSummary)}` : ""}</li>`).join("");
    return `<li><strong>${escapeHTML(r.repositoryName)}</strong> <code>${escapeHTML(r.repositoryId)}</code><p>${escapeHTML(r.repositoryPath)}</p><p>${escapeHTML(r.registrationState || "ACTIVE")} · ${escapeHTML(r.availability)} · ${escapeHTML(r.developmentRef || t("projects.unconfigured"))} · ${escapeHTML(r.developmentRefStatus)}</p>${r.currentUsage ? `<p>${escapeHTML(r.currentUsage)}</p>` : ""}<button type="button" data-action="prepare-unlink:${escapeHTML(r.repositoryId)}" ${canUnlink ? "" : "disabled"}>${escapeHTML(t("projects.unlink"))}</button>${blockerDetail ? `<small>${escapeHTML(t("projects.unlinkBlocked"))}</small><ul>${blockerDetail}</ul>` : ""}${r.unlinkedAt ? `<small>${escapeHTML(r.unlinkedAt)}</small>` : ""}</li>`;
  };
  const repositoryRows = inventory?.repositories || [];
  const activeRows = repositoryRows.filter(r => (r.registrationState || "ACTIVE") === "ACTIVE").map(repositoryRow).join("");
  const removedRows = repositoryRows.filter(r => r.registrationState === "UNLINKED").map(repositoryRow).join("");
  const rows = state.projectId ? inventory?.error ? `<p role="alert">${escapeHTML(inventory.error.code)}: ${escapeHTML(inventory.error.message)}</p>` : inventory?.loading ? `<p>${escapeHTML(t("projects.loading"))}</p>` : `${activeRows ? `<h3>${escapeHTML(t("projects.activeRepositories"))}</h3><ul>${activeRows}</ul>` : ""}${removedRows ? `<h3>${escapeHTML(t("projects.removedRepositories"))}</h3><ul>${removedRows}</ul>` : ""}` : "";
  const confirmation = state.unlinkConfirmation;
  const unlinkConfirmation = confirmation ? `<section class="unlink-confirmation"><p>${escapeHTML(t("projects.unlinkConfirm"))}</p><p><strong>${escapeHTML(confirmation.repositoryName || confirmation.repositoryId)}</strong> <code>${escapeHTML(confirmation.repositoryId)}</code></p><p>${escapeHTML(confirmation.repositoryPath || "")}</p><div class="form-actions"><button type="button" data-action="cancel-unlink">${escapeHTML(t("projects.unlinkCancel"))}</button><button type="button" class="primary" data-action="commit-unlink">${escapeHTML(t("projects.unlinkConfirmAction"))}</button></div></section>` : "";
  const selected = (state.repositories || []).map((r,i)=>`<li>${escapeHTML(r.path)} <input aria-label="${escapeHTML(t("projects.alias"))}" data-repository-alias="${i}" value="${escapeHTML(r.alias)}" placeholder="${escapeHTML(t("projects.alias"))}" ${pending?"disabled":""} /><button type="button" data-action="remove-repository:${i}" ${pending?"disabled":""}>×</button></li>`).join("");
  return `<div class="modal-backdrop"><form class="project-form" id="project-registration-form"><div class="title-row"><div><h2>${escapeHTML(t(state.projectId?"projects.manage":"projects.add"))}</h2><p class="muted">${escapeHTML(t("projects.confirm"))}</p></div><button type="button" class="icon-button" data-action="cancel-add-project" aria-label="${escapeHTML(t("action.cancel"))}">×</button></div>${error}${state.projectId?`<p>${escapeHTML(t("projects.title"))}: <code>${escapeHTML(state.projectId)}</code></p>`:""}${rows}${unlinkConfirmation}${pending?`<p>${escapeHTML(t("projects.retryPending"))}</p>`:""}<ul>${selected}</ul><button type="button" data-action="reselect-project" ${pending?"disabled":""}>${escapeHTML(t("projects.reselect"))}</button>${state.projectId?"":`<label>${escapeHTML(t("projects.name"))}<input name="projectName" value="${escapeHTML(state.name)}" autocomplete="off" ${pending?"disabled":"required"} /></label>`}<div class="form-actions"><button type="button" data-action="cancel-add-project">${escapeHTML(t("action.cancel"))}</button><button type="submit" class="primary" ${state.creating ? "disabled" : ""}>${escapeHTML(t(pending?"action.retry":state.projectId?"projects.addRepositories":"projects.create"))}</button></div></form></div>`;
}

export function projectsHTML(t, state, status) {
  const ready = status ? ["RUNNING", "EXTERNAL"].includes(status?.core?.state) : state.coreReady;
  const unavailable = !ready ? `<p class="warning" id="project-core-state">${escapeHTML(t("projects.coreNotReady"))}</p>` : `<p class="project-ready" id="project-core-state">${escapeHTML(t("projects.coreReady"))}</p>`;
  const loadError = state.loadError && state.loadError.code !== "CORE_UNAVAILABLE" ? `<div class="form-error" role="alert"><span>${escapeHTML(state.loadError.message)}</span><button data-action="retry-projects">${escapeHTML(t("action.retry"))}</button></div>` : "";
  const success = state.successMessage ? `<p class="success" role="status">${escapeHTML(t("projects.success", { name: state.successMessage }))}</p>` : "";
  let body = "";
  if (state.loading) body = `<p class="muted">${escapeHTML(t("projects.loading"))}</p>`;
  else if (!loadError && state.items.length === 0) body = `<div class="empty-state"><h2>${escapeHTML(t("projects.emptyTitle"))}</h2><p>${escapeHTML(t("projects.emptyBody"))}</p><button class="primary" data-action="add-project" ${ready ? "" : "disabled"}>${escapeHTML(t("projects.add"))}</button></div>`;
  else body = `<div class="project-list">${state.items.map((project) => projectCardHTML(t, project, state.selectedProjectId === project.id)).join("")}</div>`;
  return `<section class="panel projects-panel"><div class="title-row"><div><h1>${escapeHTML(t("projects.title"))}</h1><p class="muted">${escapeHTML(t("projects.subtitle"))}</p></div><button class="primary" id="add-project" data-action="add-project" ${ready && !state.creating ? "" : "disabled"}>${escapeHTML(t("projects.add"))}</button></div>${unavailable}${success}${loadError}<div id="project-list-content">${body}</div>${projectRegistrationHTML(t, state)}</section>`;
}

export function logsHTML(t, state) {
  const options = LOG_CATEGORIES.map((category) => `<option value="${category}" ${state.logsFilter === category ? "selected" : ""}>${escapeHTML(t("logs.filter." + category))}</option>`).join("");
  return `<section class="panel logs-panel"><div class="title-row"><div><h1>${escapeHTML(t("logs.title"))}</h1><p class="muted">${escapeHTML(t("logs.subtitle"))}</p></div><div class="actions"><select id="log-filter" aria-label="${escapeHTML(t("logs.filter.all"))}">${options}</select><button type="button" id="follow-latest" aria-pressed="${state.followLatest ? "true" : "false"}" class="${state.followLatest ? "active" : ""}">${escapeHTML(t("logs.followLatest"))}</button><button type="button" id="jump-latest">${escapeHTML(t("logs.jumpLatest"))}</button></div></div><pre class="process-logs" id="live-logs" data-live="logs"></pre></section>`;
}

export function logsText(t, status) {
  const entries = status?.logs || [];
  return entries.length ? entries.map((entry) => `${entry.time || ""} [${entry.source}/${entry.stream}] ${entry.text}`).join("\n") : t("logs.empty");
}

function textField(name, label, value, disabled = false) {
  return `<label>${escapeHTML(label)}<input name="${name}" value="${escapeHTML(value || "")}" autocomplete="off" data-persist="${name}" ${disabled ? "disabled" : ""} /></label>`;
}

function checkboxGroup(name, label, options, selected, disabled = false) {
  const set = new Set(Array.isArray(selected) ? selected : []);
  const boxes = options.map(([value, text]) => `<label class="checkbox ${disabled ? "disabled" : ""}"><input type="checkbox" name="${name}" value="${escapeHTML(value)}" ${set.has(value) ? "checked" : ""} data-persist="${name}" autocomplete="off" ${disabled ? "disabled" : ""} /> ${escapeHTML(text)}</label>`).join("");
  return `<label class="group-label">${escapeHTML(label)}</label><div class="checkbox-grid">${boxes}</div>`;
}

function saveStateHTML(section) {
  return `<span class="save-state" data-save-state="${section}" data-live="save-${section}"></span>`;
}

function lockedNote(t, locked) {
  return locked ? `<p class="field-note locked-note">${escapeHTML(t("settings.stopToEdit"))}</p>` : "";
}

function storageSection(t, settings, locked) {
  return `<fieldset><legend>${escapeHTML(t("settings.section.storage"))}</legend><label>${escapeHTML(t("settings.dataRoot"))}<div class="picker-field"><input name="dataRoot" value="${escapeHTML(settings.dataRoot || "")}" autocomplete="off" data-persist="dataRoot" ${locked ? "disabled" : ""} /><button type="button" data-action="choose-data-root" ${locked ? "disabled" : ""}>${escapeHTML(t("settings.choose"))}</button></div></label>${lockedNote(t, locked)}<p class="field-note">${escapeHTML(t("settings.dataRootNote"))}</p><p class="field-note">${escapeHTML(t("settings.noSecrets"))}</p></fieldset>`;
}

function codexSection(t, settings, modelCandidates, reading = false) {
  const byID = new Map((settings.codingAgentModels || []).map((model) => [model.modelId, model]));
  const models = (Array.isArray(modelCandidates) ? modelCandidates : []).map((modelId) => byID.get(modelId)).filter(Boolean);
  const rows = models.map((model) => {
    const options = (model.supportedReasoningEfforts || []).map((effort) => `<option value="${escapeHTML(effort)}" ${effort === model.defaultReasoningEffort ? "selected" : ""}>${escapeHTML(REASONING_OPTIONS.find(([id]) => id === effort)?.[1] || effort)}</option>`).join("");
    return `<div class="coding-agent-model"><label><input type="checkbox" data-coding-agent-model="${escapeHTML(model.modelId)}" ${model.enabled ? "checked" : ""} />${escapeHTML(model.modelId)}</label> <label>${escapeHTML(t("settings.defaultReasoning"))}<select data-coding-agent-effort="${escapeHTML(model.modelId)}">${options}</select></label></div>`;
  }).join("");
  return `<fieldset><legend>${escapeHTML(t("settings.section.codex"))}</legend><div class="form-actions"><button type="button" data-action="read-codex-models" ${reading ? "disabled" : ""}>${escapeHTML(t(reading ? "settings.readingModels" : "settings.readModels"))}</button></div><p class="field-note">${escapeHTML(t("settings.modelsNote"))}</p><p class="field-note" role="status" id="model-catalog-status"></p><div class="coding-agent-models">${rows}</div><p class="field-note">${escapeHTML(t("settings.allowlistChangeNote"))}</p></fieldset>`;
}

const PI_THINKING_LEVELS = [["", ""], ["off", "off"], ["minimal", "minimal"], ["low", "low"], ["medium", "medium"], ["high", "high"], ["xhigh", "xhigh"], ["max", "max"]];

export function piRuntimeSettingsHTML(t, status, busy = false) {
  return status?.piRestartRequired
    ? `<p class="field-note" role="status">${escapeHTML(t("settings.piRestartPending"))}</p><button type="button" data-action="restart" data-busy-lock ${busy ? "disabled" : ""}>${escapeHTML(t("settings.piApplyRestart"))}</button>`
    : `<p class="field-note">${escapeHTML(t("settings.piSaveBehavior"))}</p>`;
}

function piSection(t, state) {
  const settings=state.settings,readingPi=state.piModelCatalogLoading;
  const models = settings.piAgentModels || [];
  const rows = models.filter(model=>model.enabled).map((model) => {
    const options = (model.supportedReasoningEfforts || []).map((effort) => `<option value="${escapeHTML(effort)}" ${effort === model.defaultReasoningEffort ? "selected" : ""}>${escapeHTML(REASONING_OPTIONS.find(([id]) => id === effort)?.[1] || effort)}</option>`).join("");
    return `<div class="coding-agent-model"><label><input type="checkbox" data-pi-agent-model="${escapeHTML(model.modelId)}" ${model.enabled ? "checked" : ""} />${escapeHTML(model.modelId)}</label> <label>${escapeHTML(t("settings.defaultReasoning"))}<select data-pi-agent-effort="${escapeHTML(model.modelId)}">${options}</select></label></div>`;
  }).join("");
  const enabledModels = models.filter((model) => model.enabled);
  const modelOptions = enabledModels.map((model) => `<option value="${escapeHTML(model.modelId)}" ${model.modelId === (settings.piModel || "") ? "selected" : ""}>${escapeHTML(model.modelId)}</option>`).join("");
  const thinkingOptions = PI_THINKING_LEVELS.map(([id, label]) => `<option value="${escapeHTML(id)}" ${id === (settings.piThinkingLevel || "") ? "selected" : ""}>${escapeHTML(label)}</option>`).join("");
  return `<fieldset><legend>${escapeHTML(t("settings.section.pi"))}</legend><div id="live-pi-runtime-settings">${piRuntimeSettingsHTML(t,state.status,state.busy)}</div>
  <section class="pi-setup-step" aria-labelledby="pi-model-selection-title"><h3 id="pi-model-selection-title"><span>1</span>${escapeHTML(t("settings.piSelectUsed"))}</h3><div class="form-actions"><button type="button" data-action="read-pi-models" ${readingPi ? "disabled" : ""}>${escapeHTML(t(readingPi ? "settings.readingModels" : "settings.piReadModels"))}</button></div><p class="field-note">${escapeHTML(t("settings.piModelsNote"))}</p><p class="field-note" role="status" id="pi-model-catalog-status"></p>${piModelPickerHTML(t,state)}<details class="pi-configured-models" ${models.length<=6?"open":""}><summary>${escapeHTML(t("settings.piUsedModels",{selected:enabledModels.length,total:models.length}))}</summary><div class="coding-agent-models">${rows||`<p class="field-note">${escapeHTML(t("settings.piNoSelection"))}</p>`}</div>${enabledModels.length?`<button type="button" class="pi-clear-models" data-action="clear-pi-models">${escapeHTML(t("settings.piClearSelection"))}</button>`:""}</details><p class="field-note">${escapeHTML(t("settings.piSelectionNote"))}</p></section>
  <section class="pi-setup-step" aria-labelledby="pi-default-settings-title"><h3 id="pi-default-settings-title"><span>2</span>${escapeHTML(t("settings.piConfigureDefaults"))}</h3><label>${escapeHTML(t("settings.piModel"))}<select name="piModel" data-persist="piModel" ${!enabledModels.length?"disabled":""}><option value="">${escapeHTML(t("settings.piChooseDefault"))}</option>${modelOptions}</select></label><p class="field-note">${escapeHTML(t(enabledModels.length?"settings.piDefaultFromSelection":"settings.piSelectFirst"))}</p><label>${escapeHTML(t("settings.piThinkingLevel"))}<select name="piThinkingLevel" data-persist="piThinkingLevel" ${!enabledModels.length?"disabled":""}>${thinkingOptions}</select></label><p class="field-note">${escapeHTML(t("settings.piThinkingLevelNote"))}</p></section><details class="pi-executable-details"><summary>${escapeHTML(t("settings.piExecutable"))}</summary><label>${escapeHTML(t("settings.piExecutable"))}<div class="picker-field"><input name="piExecutable" value="${escapeHTML(settings.piExecutable || "")}" autocomplete="off" data-persist="piExecutable" /><button type="button" data-action="choose-pi-executable">${escapeHTML(t("settings.choose"))}</button></div></label><p class="field-note">${escapeHTML(t("settings.piExecutableNote"))}</p></details></fieldset>`;
}

function chatgptSection(t, settings, apiKeyConfigured) {
  const configured = Boolean(settings.openAITunnelExecutable);
  return `<fieldset><legend>${escapeHTML(t("settings.section.chatgpt"))}</legend><p class="field-note">${escapeHTML(t("service.tunnel.copy"))}</p><label>${escapeHTML(t("settings.tunnelExecutable"))}<div class="picker-field"><input name="openAITunnelExecutable" value="${escapeHTML(settings.openAITunnelExecutable || "")}" autocomplete="off" data-persist="openAITunnelExecutable" /><button type="button" data-action="choose-tunnel-executable">${escapeHTML(t("settings.choose"))}</button></div></label>${textField("openAITunnelID", t("settings.tunnelID"), settings.openAITunnelID)}<p class="field-note">${escapeHTML(t("settings.tunnelIDNote"))}</p><label>${escapeHTML(t("settings.apiKey"))}<div class="picker-field"><input type="password" id="tunnel-api-key" placeholder="${escapeHTML(t("settings.apiKeyPlaceholder"))}" autocomplete="new-password" /><button type="button" data-action="save-api-key">${escapeHTML(apiKeyConfigured ? t("settings.apiKeyReplace") : t("settings.apiKeySave"))}</button><button type="button" data-action="clear-api-key" ${apiKeyConfigured ? "" : "disabled"}>${escapeHTML(t("settings.apiKeyClear"))}</button></div></label><p class="field-note"><span id="api-key-state">${escapeHTML(apiKeyConfigured ? t("settings.apiKeyConfigured") : t("settings.apiKeyNotConfigured"))}</span> · ${escapeHTML(t("settings.apiKeyNote"))}</p><div class="tunnel-runtime" id="live-tunnel-runtime" data-live="tunnel-runtime"></div></fieldset>`;
}

function runtimeSection(t, settings, locked) {
  return `<fieldset><legend>${escapeHTML(t("settings.section.runtime"))}</legend>${textField("coreBinary", t("settings.coreBinary"), settings.coreBinary, locked)}${textField("mcpBinary", t("settings.mcpBinary"), settings.mcpBinary, locked)}<label>${escapeHTML(t("settings.codexExecutable"))}<div class="picker-field"><input name="codexExecutable" value="${escapeHTML(settings.codexExecutable || "")}" autocomplete="off" data-persist="codexExecutable" /><button type="button" data-action="choose-codex-executable">${escapeHTML(t("settings.choose"))}</button></div></label><p class="field-note">${escapeHTML(t("settings.codexExecutableNote"))}</p>${textField("coreEndpoint", t("settings.coreEndpoint"), settings.coreEndpoint, locked)}${textField("mcpEndpoint", t("settings.mcpEndpoint"), settings.mcpEndpoint, locked)}${lockedNote(t, locked)}</fieldset>`;
}

export function managedStackActive(status) {
  return [status?.core, status?.mcp].some((service) =>
    service?.ownership === "MANAGED" && !["STOPPED", "CRASHED"].includes(service?.state),
  );
}

export function settingsHTML(t, state) {
  const locked = managedStackActive(state.status);
  const nav = SETTINGS_SECTIONS.map((section) => `<button class="settings-nav-item ${state.settingsSection === section ? "active" : ""}" data-section="${section}">${escapeHTML(t("settings.section." + section))}</button>`).join("");
  const body = {
    storage: () => storageSection(t, state.settings, locked),
    codex: () => codexSection(t, state.settings, state.codexModelCandidates, state.modelCatalogLoading),
    pi: () => piSection(t, state),
    chatgpt: () => chatgptSection(t, state.settings, state.apiKeyConfigured),
    runtime: () => runtimeSection(t, state.settings, locked),
  }[state.settingsSection]();
  return `<section class="panel settings"><div class="title-row"><div><h1>${escapeHTML(t("settings.title"))}</h1><p class="muted">${escapeHTML(t("settings.subtitle"))}</p></div></div><div class="settings-nav">${nav}</div><p class="field-note">${escapeHTML(t("settings.sectionsNote"))}</p><div class="settings-section" id="settings-section">${body}<p class="field-note">${saveStateHTML(state.settingsSection)}</p></div></section>`;
}

export function liveServicesHTML(t, status) {
  return `${serviceCard(t("service.core"), status?.core)}${serviceCard(t("service.mcp"), status?.mcp)}${localMCPCardHTML(t, status?.localMCP)}${tunnelCardHTML(t, status?.tunnel)}${chatgptCardHTML(t, status?.chatgptIntegration)}${codexCard(t, status?.codex)}`;
}

export function tunnelRuntimeHTML(t, tunnel, busy) {
  const configured = Boolean(tunnel && tunnel.state !== "NOT_CONFIGURED") || Boolean(tunnel?.executable);
  const managedRunning = ["RUNNING", "STARTING"].includes(tunnel?.state);
  return `<span>${escapeHTML(t("settings.tunnelVersion"))}: <code>${escapeHTML(tunnel?.version || "—")}</code></span><span>${escapeHTML(t("settings.tunnelStatus"))}: <code>${escapeHTML(t(tunnelStateKey(tunnel)))}</code></span><button type="button" data-action="restart-tunnel" data-busy-lock ${!configured || busy ? "disabled" : ""}>${escapeHTML(t("action.restart"))}</button><button type="button" data-action="stop-tunnel" data-busy-lock ${!configured || !managedRunning || busy ? "disabled" : ""}>${escapeHTML(t("action.stop"))}</button>${tunnel?.lastError ? `<span class="field-note">${escapeHTML(tunnel.lastError)}</span>` : ""}`;
}

export function warningsHTML(t, status) {
  const warnings = [
    status?.buildMismatch ? t("warning.buildMismatch") : "",
    status?.lastError,
  ].filter(Boolean);
  return warnings.map((warning) => `<p class="warning">${escapeHTML(warning)}</p>`).join("");
}

export { escapeHTML };
