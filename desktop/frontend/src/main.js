import "./style.css";
import { normalizePiCatalog, addPiCatalogModel, piPickerOptionsHTML, piSelectionChange } from "./pi-model-picker.js";
import { reloadWorkbenchDocument } from "./console-workbench.js";
import { createNativeConsole } from "./native-console.js";
import { resolveLang, translate } from "./i18n.js";
import {
  applySaveFailure,
  applySaveSuccess,
  buildSettingsPayload,
  nextCheckboxSelection,
  updateCodingAgentModel,
  updatePiAgentModel,
} from "./settings-payload.js";
import { logsViewText } from "./log-view.js";
import {
  chooseProjectDirectory,
  closeProjectRegistration,
  coreReadyFromStatus,
  createProjectsState,
  loadProjects,
  manageProjectRepositories,
  prepareRepositoryUnlink,
  commitRepositoryUnlink,
  cancelRepositoryUnlink,
  submitProjectRegistration,
} from "./project-registration.js";
import {
  SETTINGS_SECTIONS,
  consoleHTML,
  liveServicesHTML,
  logsHTML,
  overviewHTML,
  overviewDesktopSubtitle,
  overviewManagedDataRoot,
  projectsHTML,
  piRuntimeSettingsHTML,
  saveStateKey,
  sessionsHTML,
  settingsHTML,
  shellHTML,
  tunnelRuntimeHTML,
  warningsHTML,
} from "./views.js";

const fallbackConsoleURL = "http://127.0.0.1:8081/ui/";
const defaultSettings = {
  dataRoot: "",
  coreBinary: "devbridge",
  mcpBinary: "devbridge-mcp",
  localMCPBinary: "devbridge-local-mcp",
  codexExecutable: "codex",
  openAITunnelExecutable: "",
  openAITunnelID: "",
  codingAgentModels: [],
	  codingAgentConfigVersion: 2,
  coreEndpoint: "http://127.0.0.1:8081",
  mcpEndpoint: "http://127.0.0.1:8080",
  localMCPEndpoint: "http://127.0.0.1:8090",
};

const state = {
  page: "overview",
  settingsSection: "storage",
  status: null,
  settings: { ...defaultSettings },
  settingsLoaded: false,
  codexModelCandidates: [],
  piModelCandidates: [],
  piModelCatalog: [], piModelCatalogExecutable: null, piSelectedModel: "", piModelSearch: "", piPickerOpen: false,
  modelCatalogLoading: false,
  piModelCatalogLoading: false,
  modelCatalogMessage: "",
  modelCatalogError: "",
  apiKeyConfigured: false,
  busy: false,
  actionError: "",
  saveStates: {},
  langPreference: localStorage.getItem("devbridgeDesktopLang") || "system",
  logsFilter: "all",
  followLatest: true,
  projects: createProjectsState(),
};

// Signature cache for live regions: a region is only patched when its
// rendered content actually changed, so polling never destroys text
// selection, focus, scroll position, or open dropdowns.
const signatures = new Map();

const app = document.querySelector("#app");
const nativeConsole = createNativeConsole({backend, storage:window.localStorage});

function lang() {
  return resolveLang(state.langPreference, navigator.language);
}

function t(key, values) {
  return translate(lang(), key, values);
}

function backend() {
  return window.go?.main?.App;
}

function onSettingsPage() {
  return state.page === "settings";
}

// ---------- rendering ----------

function renderShell() {
  const systemLang = lang() === "zh-CN" ? "跟随系统" : "System";
  app.innerHTML = shellHTML(t, systemLang);
  document.querySelectorAll("#main-nav .nav-item").forEach((button) => {
    button.classList.toggle("active", button.dataset.page === state.page);
    button.addEventListener("click", () => {
      if (state.page === button.dataset.page) return;
      state.page = button.dataset.page;
      document.querySelectorAll("#main-nav .nav-item").forEach((item) => item.classList.toggle("active", item === button));
      renderContent();
      if (state.page === "projects") void refreshProjectList();
    });
  });
  const languageSelect = document.querySelector("#ui-language");
  languageSelect.value = state.langPreference;
  languageSelect.addEventListener("change", () => {
    state.langPreference = languageSelect.value;
    localStorage.setItem("devbridgeDesktopLang", languageSelect.value);
    renderNavLabels();
    renderContent();
  });
}

function renderNavLabels() {
  document.querySelectorAll("#main-nav .nav-item").forEach((button) => {
    button.textContent = t("nav." + button.dataset.page);
  });
}

function renderContent() {
  signatures.clear();
  const content = document.querySelector("#content");
  content.classList.toggle("sessions-content", state.page === "sessions");
  content.classList.toggle("console-content", state.page === "console");
  content.innerHTML = viewHTML();
  bindView();
  // First live fill for regions that templates leave empty on purpose.
  renderLive(true);
}

function viewHTML() {
  switch (state.page) {
    case "overview":
      return overviewHTML(t, state.status, state.settings);
    case "sessions":
      return sessionsHTML(t);
    case "console":
      return consoleHTML(t);
    case "projects":
      return projectsHTML(t, state.projects, state.status);
    case "logs":
      return logsHTML(t, state);
    case "settings":
      return settingsHTML(t, state);
    default:
      return "";
  }
}

let piPickerOutsideCleanup = () => {};
function bindView() {
  piPickerOutsideCleanup();
  const content = document.querySelector("#content");
  content.querySelectorAll("[data-action]").forEach((element) => {
    element.addEventListener("click", () => invokeAction(element.dataset.action));
  });
  content.querySelector("[data-embed-reload]")?.addEventListener("click", () => reloadWorkbenchDocument(content.querySelector("iframe")));
  if (state.page === "console") {
    nativeConsole.mount(content.querySelector("#native-console"),lang());
    nativeConsole.status(state.status?.core,lang());
    return;
  }
  if (state.page === "logs") {
    bindLogControls();
    return;
  }
  if (state.page === "projects") {
    const form = document.querySelector("#project-registration-form");
    form?.addEventListener("submit", (event) => {
      event.preventDefault();
      void registerProject();
    });
    const name = form?.querySelector('input[name="projectName"]');
    name?.addEventListener("input", () => { state.projects.name = name.value; });
    form?.querySelectorAll('[data-repository-alias]').forEach(input=>input.addEventListener("input",()=>{state.projects.repositories[Number(input.dataset.repositoryAlias)].alias=input.value;}));
    return;
  }
  if (state.page !== "settings") return;
  document.querySelectorAll(".settings-nav-item").forEach((button) => {
    button.addEventListener("click", () => {
      if (state.settingsSection === button.dataset.section) return;
      state.settingsSection = button.dataset.section;
      renderContent();
    });
  });
  const section = document.querySelector("#settings-section");
  if (!section) return;
  section.querySelectorAll("[data-persist]").forEach((input) => {
    if (input.type === "checkbox") {
      input.addEventListener("change", () => onCheckboxChange(input));
    } else {
      input.addEventListener("change", () => onScalarChange(input));
    }
  });
  section.querySelectorAll("[data-coding-agent-model]").forEach((input) => {
    input.addEventListener("change", () => onCodingAgentModelChange(input.dataset.codingAgentModel, { enabled: input.checked }));
  });
  section.querySelectorAll("[data-coding-agent-effort]").forEach((input) => {
    input.addEventListener("change", () => onCodingAgentModelChange(input.dataset.codingAgentEffort, { defaultReasoningEffort: input.value }));
  });
  if (state.settingsSection === "pi") bindPiPicker(section);
  section.querySelectorAll("[data-pi-agent-model]").forEach((input) => {
    input.addEventListener("change", () => onPiAgentModelChange(input.dataset.piAgentModel, { enabled: input.checked }));
  });
  section.querySelectorAll("[data-pi-agent-effort]").forEach((input) => {
    input.addEventListener("change", () => onPiAgentModelChange(input.dataset.piAgentEffort, { defaultReasoningEffort: input.value }));
  });
}

function bindPiPicker(section) {
  const picker=section.querySelector(".pi-model-picker");if(!picker)return;
  const trigger=picker.querySelector("[data-pi-picker-toggle]"),menu=picker.querySelector(".pi-picker-menu"),input=picker.querySelector("[data-pi-picker-search]"),options=picker.querySelector("#pi-model-options");
  function open(value){state.piPickerOpen=value;menu.hidden=!value;trigger.setAttribute("aria-expanded",String(value));if(value){const rect=trigger.getBoundingClientRect();menu.classList.toggle("opens-up",window.innerHeight-rect.bottom<300&&rect.top>300);input.focus();}}
  trigger.addEventListener("click",()=>open(!state.piPickerOpen));
  input.addEventListener("input",()=>{state.piModelSearch=input.value;options.innerHTML=piPickerOptionsHTML(t,state);input.removeAttribute("aria-activedescendant");});
  picker.addEventListener("click",event=>{const option=event.target.closest("[data-pi-picker-option]");if(!option||option.disabled)return;state.piSelectedModel=option.dataset.piPickerOption;state.piPickerOpen=false;renderContent();document.querySelector('[data-action="add-pi-model"]')?.focus();});
  picker.addEventListener("keydown",event=>{
    if(event.key==="Escape"){event.preventDefault();open(false);trigger.focus();return;}
    const choices=[...options.querySelectorAll('[data-pi-picker-option]:not(:disabled)')];
    if(event.key==="ArrowDown"||event.key==="ArrowUp"){
      event.preventDefault();if(menu.hidden){open(true);return;}if(!choices.length)return;
      const current=choices.indexOf(document.activeElement),next=event.key==="ArrowDown"?(current+1)%choices.length:(current<=0?choices.length-1:current-1);choices[next].focus();
    }else if(event.key==="Enter"&&document.activeElement===input){event.preventDefault();choices[0]?.click();}
  });
  picker.addEventListener("focusout",event=>{if(event.relatedTarget&&!picker.contains(event.relatedTarget))open(false);});
  if(state.piPickerOpen)open(true);
  const outside=event=>{if(!picker.contains(event.target))open(false);};document.addEventListener("pointerdown",outside);piPickerOutsideCleanup=()=>document.removeEventListener("pointerdown",outside);
}
async function addPiModel() {
  if(state.piModelCatalogLoading||state.saveStates.pi?.state==="saving"||state.piModelCatalogExecutable!==(state.settings.piExecutable||""))return;
  const current=state.settings.piAgentModels||[],next=addPiCatalogModel(current,state.piModelCatalog,state.piSelectedModel);
  if(next===current)return;
  if(await persistSection("pi",{piAgentModels:next})){state.piSelectedModel="";state.piModelSearch="";state.piPickerOpen=false;if(onSettingsPage())renderContent();}
}

async function clearPiModels() {
  if(state.piModelCatalogLoading||state.saveStates.pi?.state==="saving")return;
  if(await persistSection("pi",{piAgentModels:[],piModel:""})){
    state.piSelectedModel="";state.piModelSearch="";state.piPickerOpen=false;
    if(onSettingsPage())renderContent();
  }
}

// ---------- immediate save ----------

function setSaveState(section, saveState, message = "") {
  state.saveStates = { ...state.saveStates, [section]: { state: saveState, message } };
  updateAllSaveStates();
}

function updateSaveState(section) {
  const node = document.querySelector(`[data-save-state="${section}"]`);
  if (!node) return;
  const value = state.saveStates[section];
  const key = saveStateKey(value);
  node.className = `save-state ${value?.state || ""}`;
  node.textContent = key ? t(key) + (value?.state === "error" && value.message ? `: ${value.message}` : "") : "";
  if (value?.state === "error" && value.message) {
    node.title = value.message;
  } else {
    node.removeAttribute("title");
  }
}

function updateAllSaveStates() {
  for (const section of SETTINGS_SECTIONS) updateSaveState(section);
  const locked = state.busy || state.modelCatalogLoading || state.piModelCatalogLoading || state.saveStates.codex?.state === "saving" || state.saveStates.pi?.state === "saving";
  document.querySelectorAll('[data-coding-agent-model], [data-coding-agent-effort], [data-action="read-codex-models"], [data-pi-agent-model], [data-pi-agent-effort], [data-action="read-pi-models"]').forEach((input) => { input.disabled = locked; });
  document.querySelectorAll('[data-persist="piExecutable"], [data-persist="piModel"], [data-persist="piThinkingLevel"], [data-action="choose-pi-executable"]').forEach(input => {
    input.disabled = locked || (["piModel","piThinkingLevel"].includes(input.dataset.persist) && !(state.settings.piAgentModels || []).some(model => model.enabled));
  });
  const piClear=document.querySelector('[data-action="clear-pi-models"]');if(piClear)piClear.disabled=locked;
  const piAdd=document.querySelector('[data-action="add-pi-model"]');if(piAdd)piAdd.disabled=locked||!state.piSelectedModel||state.piModelCatalogExecutable!==(state.settings.piExecutable||"")||(state.settings.piAgentModels||[]).some(m=>m.enabled&&m.modelId===state.piSelectedModel);
  const piToggle=document.querySelector('[data-pi-picker-toggle]');if(piToggle)piToggle.disabled=locked||!state.piModelCatalog.length||state.piModelCatalogExecutable!==(state.settings.piExecutable||"");
  const catalogStatus = document.querySelector("#model-catalog-status");
  if (catalogStatus) {
    catalogStatus.textContent = state.modelCatalogError ? t("settings.modelsReadFailed", { message: state.modelCatalogError }) : state.modelCatalogMessage ? t(state.modelCatalogMessage) : "";
    catalogStatus.setAttribute("role", state.modelCatalogError ? "alert" : "status");
  }
  const piCatalogStatus = document.querySelector("#pi-model-catalog-status");
  if (piCatalogStatus) {
    piCatalogStatus.textContent = state.piModelCatalogError ? t("settings.modelsReadFailed", { message: state.piModelCatalogError }) : state.piModelCatalogMessage ? t(state.piModelCatalogMessage) : "";
    piCatalogStatus.setAttribute("role", state.piModelCatalogError ? "alert" : "status");
  }
}

async function persistSection(section, changed) {
  const previous = { ...state.settings };
  const payload = buildSettingsPayload({ ...state.settings, ...changed });
  setSaveState(section, "saving");
  try {
    await backend()?.SaveSettings?.(payload);
    const applied = applySaveSuccess(previous, payload);
    state.settings = applied.settings;
    if (section === "codex") state.codexModelCandidates = payload.codingAgentModels.map((model) => model.modelId);
    if (section === "pi") state.piModelCandidates = payload.piAgentModels.map((model) => model.modelId);
    setSaveState(section, "saved");
    // The current view must reflect the saved values immediately: inputs are
    // synced in place (no section switch, no focus/scroll loss) and a single
    // status fetch pulls the fresh version/status projection right away.
    syncInputs(applied.syncInputs);
    renderLive(true);
    void refresh();
    if ((section === "codex" || section === "pi") && onSettingsPage()) renderContent();
    return true;
  } catch (error) {
    // Preserve the previously persisted values on failure.
    const reverted = applySaveFailure(previous, changed);
    state.settings = reverted.settings;
    setSaveState(section, "error", String(error));
    syncInputs(reverted.revertInputs);
    if ((section === "codex" || section === "pi") && onSettingsPage()) renderContent();
    return false;
  }
}

// syncInputs updates every rendered input for each setting name. Array fields
// use multiple same-name checkboxes and must be reconciled as one group.
function syncInputs(names) {
  for (const name of names) {
    document.querySelectorAll(`[data-persist="${name}"]`).forEach((input) => {
      if (input.type === "checkbox") input.checked = state.settings[name]?.includes(input.value);
      else input.value = state.settings[name] ?? "";
    });
  }
}

function onCheckboxChange(input) {
  const name = input.dataset.persist;
  const selected = nextCheckboxSelection(
    name,
    state.settings[name],
    input.value,
    input.checked,
    state.codexModelCandidates,
  );
  if (!selected.length) {
    setSaveState("codex", "error", t("message.allowlistRequired"));
    input.checked = state.settings[name]?.includes(input.value);
    return;
  }
  persistSection("codex", { [name]: selected });
}

function onCodingAgentModelChange(modelId, change) {
  const next = updateCodingAgentModel(state.settings.codingAgentModels, modelId, change);
  if (!next.some((model) => model.enabled)) {
    setSaveState("codex", "error", t("message.allowlistRequired"));
    renderContent();
    return;
  }
  persistSection("codex", { codingAgentModels: next });
}

function onPiAgentModelChange(modelId, change) {
  const next = updatePiAgentModel(state.settings.piAgentModels, modelId, change);
  persistSection("pi", piSelectionChange(state.settings,next));
}

function onScalarChange(input) {
  const name = input.dataset.persist;
  if ((state.settings[name] ?? "") === input.value) return;
  const section =
    name === "dataRoot" ? "storage" :
    ["piExecutable","piModel","piThinkingLevel"].includes(name) ? "pi" :
    name === "openAITunnelExecutable" || name === "openAITunnelID" ? "chatgpt" : "runtime";
  persistSection(section, { [name]: input.value });
}

// ---------- logs: filter, jump, follow ----------

const FOLLOW_BOTTOM_THRESHOLD = 48;

function isNearBottom(viewport) {
  return viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight <= FOLLOW_BOTTOM_THRESHOLD;
}

function updateFollowButton() {
  const button = document.querySelector("#follow-latest");
  if (!button) return;
  button.setAttribute("aria-pressed", state.followLatest ? "true" : "false");
  button.classList.toggle("active", state.followLatest);
}

function scrollToLatest() {
  const viewport = document.querySelector("#live-logs");
  if (viewport) viewport.scrollTop = viewport.scrollHeight;
}

function bindLogControls() {
  const filter = document.querySelector("#log-filter");
  if (filter) {
    filter.value = state.logsFilter;
    filter.addEventListener("change", () => {
      state.logsFilter = filter.value;
      renderContent();
      if (state.followLatest) scrollToLatest();
    });
  }
  const followButton = document.querySelector("#follow-latest");
  followButton?.addEventListener("click", () => {
    state.followLatest = true;
    updateFollowButton();
    scrollToLatest();
  });
  const jumpButton = document.querySelector("#jump-latest");
  jumpButton?.addEventListener("click", () => {
    state.followLatest = true;
    updateFollowButton();
    scrollToLatest();
  });
  const viewport = document.querySelector("#live-logs");
  viewport?.addEventListener("scroll", () => {
    // Scrolling upward away from the bottom pauses following; polling must
    // then never pull the reader back down. Resuming is explicit via the
    // Follow latest / Jump to latest controls.
    if (!isNearBottom(viewport) && state.followLatest) {
      state.followLatest = false;
      updateFollowButton();
    }
  });
  if (state.followLatest) scrollToLatest();
}

// ---------- API key ----------

function updateAPIKeyUI() {
  const chip = document.querySelector("#api-key-state");
  if (chip) chip.textContent = state.apiKeyConfigured ? t("settings.apiKeyConfigured") : t("settings.apiKeyNotConfigured");
  const saveButton = document.querySelector('[data-action="save-api-key"]');
  if (saveButton) saveButton.textContent = state.apiKeyConfigured ? t("settings.apiKeyReplace") : t("settings.apiKeySave");
  const clearButton = document.querySelector('[data-action="clear-api-key"]');
  if (clearButton) clearButton.disabled = !state.apiKeyConfigured;
}

async function saveAPIKey() {
  const input = document.querySelector("#tunnel-api-key");
  const secret = input?.value.trim() || "";
  if (!secret) {
    setSaveState("chatgpt", "error", t("settings.apiKeyRequired"));
    return;
  }
  setSaveState("chatgpt", "saving");
  try {
    const result = await backend()?.SetTunnelAPIKey?.(secret);
    if (input) input.value = "";
    state.apiKeyConfigured = true;
    updateAPIKeyUI();
    applySaveResult("chatgpt", result);
  } catch (error) {
    setSaveState("chatgpt", "error", String(error));
  }
}

async function clearAPIKey() {
  setSaveState("chatgpt", "saving");
  try {
    const result = await backend()?.ClearTunnelAPIKey?.();
    state.apiKeyConfigured = false;
    updateAPIKeyUI();
    applySaveResult("chatgpt", result);
  } catch (error) {
    setSaveState("chatgpt", "error", String(error));
  }
}

function applySaveResult(section, result) {
  if (result?.restartRequired) setSaveState(section, "saved-restart");
  else setSaveState(section, "saved");
}

// ---------- actions ----------

async function readCodexModels() {
  if (state.modelCatalogLoading || state.saveStates.codex?.state === "saving") return;
  state.modelCatalogLoading = true;
  state.modelCatalogError = "";
  state.modelCatalogMessage = "settings.readingModels";
  setSaveState("codex", "");
  renderContent();
  try {
    const models = await backend()?.ReadCodexModels?.();
    if (!Array.isArray(models) || !models.length) throw new Error(t("message.modelCatalogEmpty"));
    const saved = await persistSection("codex", { codingAgentModels: models });
    state.modelCatalogMessage = saved ? "settings.modelsRead" : "";
  } catch (error) {
    state.modelCatalogMessage = "";
    state.modelCatalogError = String(error);
  } finally {
    state.modelCatalogLoading = false;
    if (onSettingsPage()) renderContent();
  }
}

async function readPiModels() {
  if (state.piModelCatalogLoading || state.saveStates.pi?.state === "saving") return;
  state.piModelCatalogLoading = true;
  state.piModelCatalogError = "";
  state.piModelCatalogMessage = "settings.readingModels";
  setSaveState("pi", "");
  renderContent();
  const executable=state.settings.piExecutable||"";
  try {
    const models = normalizePiCatalog(await backend()?.ReadPiModels?.());
    if (!models.length) throw new Error(t("message.modelCatalogEmpty"));
    if(executable!==(state.settings.piExecutable||""))throw new Error(t("settings.piCatalogChanged"));
    state.piModelCatalog=models;state.piModelCatalogExecutable=executable;
    state.piSelectedModel="";state.piModelSearch="";state.piPickerOpen=true;
    state.piModelCatalogMessage = "settings.piCatalogReady";
  } catch (error) {
    state.piModelCatalogMessage = "";
    state.piModelCatalogError = String(error);
  } finally {
    state.piModelCatalogLoading = false;
    if (onSettingsPage()) {renderContent();if(state.settingsSection==="pi"&&state.piPickerOpen)document.querySelector('[data-pi-picker-search]')?.focus();}
  }
}

async function invokeAction(action) {
  if (action === "read-codex-models") return readCodexModels();
  if (action === "read-pi-models") return readPiModels();
  if (action === "add-pi-model") return addPiModel();
  if (action === "clear-pi-models") return clearPiModels();
  if(action.startsWith("manage-project:")) {await manageProjectRepositories(state.projects,backend(),action.slice("manage-project:".length));renderContent();return;}
  if(action.startsWith("prepare-unlink:")) {const repository=(state.projects.repositoryInventory?.repositories||[]).find(r=>r.repositoryId===action.slice("prepare-unlink:".length));await prepareRepositoryUnlink(state.projects,backend(),repository);renderContent();return;}
  if(action === "commit-unlink") {await commitRepositoryUnlink(state.projects,backend());renderContent();return;}
  if(action === "cancel-unlink") {cancelRepositoryUnlink(state.projects);renderContent();return;}
  if(action.startsWith("remove-repository:")) {if(!state.projects.pendingRegistration)state.projects.repositories.splice(Number(action.split(":")[1]),1);renderContent();return;}
  if(action === "add-project") {closeProjectRegistration(state.projects);state.projects.repositoryInventory=null;state.projects.addOpen=true;renderContent();return;}
  if (action === "reselect-project") {
    await chooseProjectDirectory(state.projects, backend());
    if (state.page === "projects") renderContent();
    return;
  }
  if (action === "cancel-add-project") {
    closeProjectRegistration(state.projects);
    renderContent();
    return;
  }
  if (action === "retry-projects") {
    await refreshProjectList();
    return;
  }
  if (action === "choose-data-root") {
    const input = document.querySelector('input[data-persist="dataRoot"]');
    try {
      const selected = await backend()?.ChooseDataRoot?.(input?.value || state.settings.dataRoot || "");
      if (selected) await persistSection("storage", { dataRoot: selected });
      else setSaveState("storage", "saved");
    } catch (error) {
      setSaveState("storage", "error", String(error));
    }
    return;
  }
  if (action === "choose-tunnel-executable") {
    const input = document.querySelector('input[data-persist="openAITunnelExecutable"]');
    const current = input?.value || state.settings.openAITunnelExecutable || "";
    try {
      const selected = await backend()?.ChooseTunnelExecutable?.(current);
      if (selected) await persistSection("chatgpt", { openAITunnelExecutable: selected });
      else setSaveState("chatgpt", "saved");
    } catch (error) {
      // Invalid selection is rejected; the previous valid path stays.
      setSaveState("chatgpt", "error", String(error));
    }
    return;
  }
  if (action === "choose-codex-executable") {
    const input = document.querySelector('input[data-persist="codexExecutable"]');
    const current = input?.value || state.settings.codexExecutable || "codex";
    try {
      const selected = await backend()?.ChooseCodexExecutable?.(current);
      if (selected) await persistSection("runtime", { codexExecutable: selected });
      else setSaveState("runtime", "saved");
    } catch (error) {
      setSaveState("runtime", "error", String(error));
    }
    return;
  }
  if (action === "choose-pi-executable") {
    const input = document.querySelector('input[data-persist="piExecutable"]');
    const current = input?.value || state.settings.piExecutable || "";
    try {
      const selected = await backend()?.ChoosePiExecutable?.(current);
      if (selected) await persistSection("pi", { piExecutable: selected });
      else setSaveState("pi", "saved");
    } catch (error) {
      setSaveState("pi", "error", String(error));
    }
    return;
  }
  if (action === "save-api-key") return saveAPIKey();
  if (action === "clear-api-key") return clearAPIKey();
  if (action === "open-console") {
    if (backend()?.OpenConsole) await backend().OpenConsole();
    else window.open(state.status?.consoleURL || fallbackConsoleURL, "_blank", "noopener,noreferrer");
    return;
  }
  if (action === "copy-local-mcp-endpoint") {
    return copyLocalMCPEndpoint();
  }
  const method = {
    start: "Start",
    stop: "Stop",
    restart: "Restart",
    "restart-tunnel": "RestartTunnel",
    "stop-tunnel": "StopTunnel",
  }[action];
  if (!method || state.busy || Object.values(state.saveStates).some(value => value.state === "saving")) return;
  state.busy = true;
  state.actionError = "";
  updateBusyLocks();
  updateAllSaveStates();
  try {
    await backend()?.[method]?.();
  } catch (error) {
    state.actionError = String(error);
  } finally {
    state.busy = false;
    await refresh();
  }
}

async function refreshProjectList() {
  if (state.projects.loading) return;
  await loadProjects(state.projects, backend());
  if (state.page === "projects") renderContent();
}

async function registerProject() {
  await submitProjectRegistration(state.projects, backend());
  if (state.page === "projects") renderContent();
}

// copyLocalMCPEndpoint copies the effective Local MCP endpoint from the
// current status projection; the button carries the same value as a fallback
// for environments without an async clipboard API.
async function copyLocalMCPEndpoint() {
  const endpoint = state.status?.localMCP?.endpoint || "";
  if (!endpoint) return;
  try {
    await navigator.clipboard.writeText(endpoint);
  } catch {
    // Clipboard permission refused: the endpoint stays visible and
    // selectable in the card, so no further error surface is needed.
  }
}

function updateBusyLocks() {
  document.querySelectorAll("[data-busy-lock]").forEach((button) => {
    button.disabled = state.busy || button.dataset.disabledWhenIdle === "true";
  });
}

// ---------- live patching ----------

function patchRegion(id, key, html) {
  const node = document.querySelector(`#${id}`);
  if (!node) return false;
  if (signatures.get(key) === html) return false;
  signatures.set(key, html);
  node.innerHTML = html;
  return true;
}

function patchText(id, key, text) {
  const node = document.querySelector(`#${id}`);
  if (!node) return false;
  if (signatures.get(key) === text) return false;
  signatures.set(key, text);
  node.textContent = text;
  return true;
}

function renderLive(force = false) {
  if (force) signatures.clear();
  if (state.page === "console") nativeConsole.status(state.status?.core,lang());
  if (state.page === "overview") {
    patchText("live-desktop-subtitle", "desktop-subtitle", overviewDesktopSubtitle(t, state.status));
    patchText("live-managed-data-root", "managed-data-root", overviewManagedDataRoot(state.status));
    patchRegion("live-services", "services", liveServicesHTML(t, state.status));
    patchRegion("live-warnings", "warnings", warningsHTML(t, state.status));
  }
  if (state.page === "logs") {
    // Only the log text is patched when it actually changed; the viewport is
    // pulled to the newest entry only while Follow latest is active, so a
    // paused reader keeps a stable scroll position.
    const changed = patchText("live-logs", "logs", logsViewText(state.status?.logs, state.logsFilter, t("logs.empty")));
    const viewport = document.querySelector("#live-logs");
    if (changed && state.followLatest && viewport) viewport.scrollTop = viewport.scrollHeight;
  }
  if (state.page === "settings" && state.settingsSection === "pi") {
    if (patchRegion("live-pi-runtime-settings", "pi-runtime-settings", piRuntimeSettingsHTML(t,state.status,state.busy))) {
      document.querySelectorAll('#live-pi-runtime-settings [data-action]').forEach(button => button.addEventListener("click", () => invokeAction(button.dataset.action)));
    }
  }
  if (state.page === "settings" && state.settingsSection === "chatgpt") {
    const previous = signatures.get("tunnel-runtime");
    patchRegion("live-tunnel-runtime", "tunnel-runtime", tunnelRuntimeHTML(t, state.status?.tunnel, state.busy));
    if (signatures.get("tunnel-runtime") !== previous) bindView();
  }
  if (state.page === "projects") {
    const ready = state.status ? coreReadyFromStatus(state.status) : state.projects.coreReady;
    const wasReady = state.projects.coreReady;
    state.projects.coreReady = ready;
    const add = document.querySelector("#add-project");
    if (add) add.disabled = !ready || state.projects.creating;
    const coreState = document.querySelector("#project-core-state");
    if (coreState) {
      coreState.className = ready ? "project-ready" : "warning";
      coreState.textContent = ready ? t("projects.coreReady") : t("projects.coreNotReady");
    }
    if (ready && !wasReady && !state.projects.loading) void refreshProjectList();
  }
  updateBusyLocks();
  updateAllSaveStates();
  if (state.page === "settings") updateAPIKeyUI();
}

// ---------- polling ----------

async function refresh() {
  try {
    let changed = false;
    let rerenderSettings = false;
    const status = await backend()?.Status?.();
    if (status) {
      state.status = status;
      changed = true;
    }
    // Settings are synced from the backend only outside the Settings page;
    // inside Settings the locally persisted state is authoritative so an
    // in-flight edit is never clobbered by a poll tick.
    if ((!onSettingsPage() || !state.settingsLoaded) && !state.busy) {
      const initialSettingsLoad = !state.settingsLoaded;
      const settings = await backend()?.GetSettings?.();
      if (settings) {
        state.codexModelCandidates = Array.isArray(settings.codexModelCandidates) ? [...settings.codexModelCandidates] : [];
        state.piModelCandidates = Array.isArray(settings.piModelCandidates) ? [...settings.piModelCandidates] : [];
        const { codexModelCandidates: _candidates, piModelCandidates: _piCandidates, tunnelApiKeyConfigured: _configured, ...persistedSettings } = settings;
        state.settings = { ...defaultSettings, ...persistedSettings };
        state.settingsLoaded = true;
        state.apiKeyConfigured = Boolean(settings.tunnelApiKeyConfigured);
        rerenderSettings = initialSettingsLoad && onSettingsPage();
        changed = true;
      }
    }
    // Patch once from the coherent result of this refresh. This is also the
    // post-ready path used by Start/Restart, so startup-time placeholders are
    // replaced immediately with the authoritative runtime identity.
    if (rerenderSettings) renderContent();
    else if (changed) renderLive();
  } catch (error) {
    state.actionError = String(error);
  }
}

// ---------- boot ----------

renderShell();
renderContent();
void refresh();
window.setInterval(refresh, 1000);
