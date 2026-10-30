export const CORE_UNAVAILABLE = "CORE_UNAVAILABLE";
export const PROJECT_ALREADY_REGISTERED = "PROJECT_ALREADY_REGISTERED";

// Reachable-Core contract failures must not be reported as Core
// unavailability. The backend keeps HTTP-status errors in-band as
// result.error; a throw across the Wails boundary is unexpected, so report it
// as a Desktop failure (or structured HTTP status when one is embedded)
// rather than blaming Core availability. This is the exact mislabeling that
// turned the observed HTTP 405 regression into CORE_UNAVAILABLE.
export function errorFromException(exception) {
  const detail = String(exception);
  const match = detail.match(/HTTP (\d{3})/);
  if (match) {
    const status = Number(match[1]);
    if (status === 404 || status === 405) {
      return { code: "UNSUPPORTED_OPERATION", message: "Dev Bridge Core does not support this request. Update Desktop to match the installed Core." };
    }
    if (status >= 500) {
      return { code: "CORE_ERROR", message: `Dev Bridge Core failed the request (HTTP ${status}).` };
    }
    if (status >= 400) {
      return { code: "INVALID_REQUEST", message: `Dev Bridge Core rejected the request (HTTP ${status}).` };
    }
  }
  return { code: "DESKTOP_ERROR", message: `Unexpected Desktop failure: ${detail}` };
}

export function createProjectsState() {
  const pendingRegistration=readPendingRegistration();
  return {
    items: [],
    coreReady: false,
    loading: false,
    loadError: null,
    addOpen: false,
    repositoryPath: "",
    repositories: pendingRegistration?.repositories?.map(r=>({...r})) || [],
    projectId: pendingRegistration?.projectId || "",
    pendingRegistration,
    repositoryInventory: null,
    unlinkConfirmation: null,
    defaultName: "",
    name: pendingRegistration?.name || "",
    creating: false,
    submitError: null,
    selectedProjectId: "",
    successMessage: "",
  };
}

export function coreReadyFromStatus(status) {
  return ["RUNNING", "EXTERNAL"].includes(status?.core?.state);
}

const unavailable = (message = "Dev Bridge Core is not ready.") => ({ code: CORE_UNAVAILABLE, message });

export async function loadProjects(state, api) {
  state.loading = true;
  state.loadError = null;
  try {
    const result = await api?.ListProjects?.();
    state.items = Array.isArray(result?.items) ? result.items : [];
    state.coreReady = Boolean(result?.coreReady);
    state.loadError = result?.error || null;
  } catch (error) {
    state.coreReady = false;
    state.loadError = errorFromException(error);
  } finally {
    state.loading = false;
  }
  return state;
}

export async function chooseProjectDirectory(state, api) {
  if(state.pendingRegistration)return false;
  state.submitError = null;
  try {
    const selection = await api?.ChooseProjectDirectory?.(state.repositoryPath || "");
    if (!selection?.repositoryPath) return false;
    const shouldUseDefault = !state.name || state.name === state.defaultName;
    state.repositoryPath = selection.repositoryPath;
    if (!state.repositories.some(r => r.path === selection.repositoryPath)) state.repositories.push({path: selection.repositoryPath, alias: ""});
    state.defaultName = selection.defaultName || pathBase(selection.repositoryPath);
    if (shouldUseDefault) state.name = state.defaultName;
    state.addOpen = true;
    return true;
  } catch (error) {
    state.submitError = { code: "INVALID_PATH", message: String(error) };
    state.addOpen = true;
    return false;
  }
}

export function closeProjectRegistration(state) {
  state.addOpen = false;
  state.repositoryPath = "";
  state.repositories = [];
  state.projectId = "";
  state.defaultName = "";
  state.name = "";
  state.creating = false;
  state.submitError = null;
  if(state.pendingRegistration){state.projectId=state.pendingRegistration.projectId||"";state.name=state.pendingRegistration.name||"";state.repositories=state.pendingRegistration.repositories.map(r=>({...r}));}
}

export async function submitProjectRegistration(state, api) {
  const name = state.name.trim();
  if (!state.pendingRegistration && !name && !state.projectId) {
    state.submitError = { code: "INVALID_ARGUMENT", message: "Project name is required." };
    return false;
  }
  state.creating = true;
  state.submitError = null;
  state.successMessage = "";
  try {
    if (!state.pendingRegistration) {
      state.pendingRegistration = { requestId: globalThis.crypto.randomUUID(), name: state.projectId ? "" : name, projectId: state.projectId || "", repositories: state.repositories.map(r=>({...r})) };
      savePendingRegistration(state.pendingRegistration);
    }
    const result = await api?.RegisterProject?.(state.pendingRegistration);
    if (result?.error) {
      state.submitError = result.error;
      if (["INVALID_ARGUMENT","INVALID_PATH","NOT_GIT_REPOSITORY","HEAD_UNAVAILABLE","PERMISSION_DENIED","CONFLICT","PROJECT_ALREADY_REGISTERED"].includes(result.error.code)) {
        state.pendingRegistration = null; savePendingRegistration(null);
      }
      if (result.error.code === PROJECT_ALREADY_REGISTERED && result.projectId) {
        state.selectedProjectId = result.projectId;
      }
      return false;
    }
    if (!result?.created || !result.projectId) {
      state.submitError = { code: "REGISTRATION_FAILED", message: "Project registration failed." };
      return false;
    }
    state.pendingRegistration = null; savePendingRegistration(null);
    state.selectedProjectId = result.projectId;
    state.successMessage = result.name || name;
    closeProjectRegistration(state);
    await loadProjects(state, api);
    return true;
  } catch (error) {
    state.submitError = errorFromException(error);
    return false;
  } finally {
    state.creating = false;
  }
}

function pathBase(path) {
  const normalized = String(path || "").replace(/[\\/]+$/, "");
  return normalized.split(/[\\/]/).pop() || "Project";
}

const PENDING_KEY = "devbridge.project-registration.pending.v2";
function readPendingRegistration() { try {const raw=globalThis.localStorage?.getItem(PENDING_KEY);return raw?JSON.parse(raw):null;} catch {return null;} }
function savePendingRegistration(value) {if(!globalThis.localStorage)return;if(value)globalThis.localStorage.setItem(PENDING_KEY,JSON.stringify(value));else globalThis.localStorage.removeItem(PENDING_KEY);}
export async function manageProjectRepositories(state,api,projectId) {
 closeProjectRegistration(state); state.unlinkConfirmation=null; state.projectId=state.pendingRegistration?.projectId||(!state.pendingRegistration?projectId:""); state.addOpen=true; state.repositoryInventory={loading:true};
 if(!state.projectId){state.repositoryInventory=null;return;}
 projectId=state.projectId;
 try {const result=await api?.ListProjectRepositories?.(projectId);state.repositoryInventory=result||{error:{code:"UNAVAILABLE",message:"Repository inventory unavailable."}};}
 catch(error){state.repositoryInventory={error:errorFromException(error)};}
}

export async function prepareRepositoryUnlink(state, api, repository) {
  if (!state.projectId || !repository?.repositoryId) return false;
  state.submitError = null;
  const requestId = globalThis.crypto.randomUUID();
  try {
    const result = await api?.PrepareRepositoryUnlink?.({ projectId: state.projectId, repositoryId: repository.repositoryId, requestId });
    if (result?.error) { state.submitError = result.error; return false; }
    state.unlinkConfirmation = result;
    return true;
  } catch (error) { state.submitError = errorFromException(error); return false; }
}

export async function commitRepositoryUnlink(state, api) {
  const confirmation = state.unlinkConfirmation;
  if (!confirmation) return false;
  try {
    const result = await api?.CommitRepositoryUnlink?.(confirmation);
    if (result?.error) { state.submitError = result.error; return false; }
    state.unlinkConfirmation = null;
    await manageProjectRepositories(state, api, state.projectId);
    return true;
  } catch (error) { state.submitError = errorFromException(error); return false; }
}

export function cancelRepositoryUnlink(state) { state.unlinkConfirmation = null; }
