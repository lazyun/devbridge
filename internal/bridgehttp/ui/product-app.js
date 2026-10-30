"use strict";

(function () {
  const query = new URLSearchParams(window.location.search);
  const embeddedLayout = query.get("workbench") === "sessions" && query.get("layout") === "embedded";
  document.documentElement.classList.toggle("embedded-layout", embeddedLayout);
  document.body.classList.toggle("embedded-layout", embeddedLayout);
  const consoleShell = document.querySelector("#console-shell");
  if (embeddedLayout) consoleShell.remove();
  else document.body.replaceChildren(consoleShell.content.cloneNode(true), consoleShell);
  const {createI18n} = window.DevBridgeI18n;
  const C = window.DevBridgeComponents;
  const W = window.DevBridgeWorkbench;
  const TW = window.DevBridgeTaskWorkbench;
  const detailRoute = TW.createRoute();
  const LF = window.DevBridgeLogFormat;
  const LV = window.DevBridgeLogView;
  const DB = window.DevBridgeDevelopment;
  const i18n = createI18n({storage: window.localStorage, navigator: window.navigator});
  const t = (key, values) => i18n.t(key, values);
  const NAV_KEY = "devbridge.console.navigation";
  const PROJECT_KEY = "devbridge.console.selectedProject";
  const TAB_KEY = "devbridge.console.projectTab";
  const WORK_ITEM_KEY = "devbridge.console.workItemFilters";
  const allowedViews = new Set(["overview", "projects", "logs", "diagnostics"]);
  const savedView = window.localStorage.getItem(NAV_KEY);
  const savedProjectID = window.localStorage.getItem(PROJECT_KEY) || "";
  const savedProjectTab = window.localStorage.getItem(TAB_KEY);
  const savedNavigation = embeddedLayout
    ? W.normalizeNavigation("projects", "sessions")
    : W.normalizeNavigation(savedView, query.get("workbench") === "sessions" ? "sessions" : savedProjectTab);
  const savedWorkItemFilters = parseWorkItemFilters(window.localStorage.getItem(WORK_ITEM_KEY));
  const state = {
    ...savedNavigation, taskFilters:{area:savedWorkItemFilters.area},taskPage:0,taskPageResult:null,taskListError:"",taskListLoading:false,areaTaskPages:{},areaTaskFilters:{},projectDataErrors:{},areaReadErrors:{},
    view: savedNavigation.view, projectTab: savedNavigation.projectTab, loading: true, error: "", health: false, lastSync: null,
    selectedProjectID: savedProjectID, projects: [], overview: null, repositories: [], featureAreas: [], modules: [], tasks: [], runs: [], sessions: [], probes: [], approvals: [], logs: [],
    developmentContext: null, developmentReadVersion: 0, developmentProjectVersion: 0, developmentLoading: false, developmentError: "", developmentPendingRepository: "", projectWorkspace: null, projectLoading: false, projectError: "", projectSearch: "", projectDropdownOpen: false, executionAuthorization: null, executionAuthorizationSaving: false, executionAuthorizationError: "", executionAuthorizationNotice: "", executionAuthorizationDraft: null, executionAuthorizationConflict: false, executionAuthorizationReading: false, executionAuthorizationGeneration: 0, executionAuthorizationSequence: 0, executionAuthorizationRequest: null, moduleSearch: "", moduleExpanded: {}, taskFilter: "all", taskKindFilter: "all", featureAreaStatusFilter: "active", workItemAreaFilter: savedWorkItemFilters.area, workItemBranchFilter: savedWorkItemFilters.branch,
    manualRefreshing: false, logsRefreshing: false, worktreeInventory: null, worktreeLoading: false, worktreeError: "",
    logFilter: {kind: "", id: "", label: ""}, logFilters: {level: "", component: "", event: "", text: ""}, logVerbose: false, logsRaw: [], logView: LV.initialState(), logTimer: null, reviewPrompt: ""
  };

  const $ = (selector) => document.querySelector(selector);
  const $$ = (selector) => Array.from(document.querySelectorAll(selector));
  const qs = (values) => new URLSearchParams(Object.entries(values).filter(([, value]) => value !== "" && value !== null && value !== undefined)).toString();
  let toastTimer = null;
  let detailReturnFocus=null;
  let embeddedDeepLinkHandled=false,embeddedRunTarget="";

  let workspaceReadVersion=0,projectReadController=null,taskListController=null,taskListGeneration=0,searchTimer=null,linkGeneration=0;
  const areaTaskControllers=new Map();
  let worktreeReadVersion=0;
  let workDrawerTaskID="",workDrawerExecutionID="";
  let runReadTimer=null;
  const revisionSnapshots=new Map();
  let detailReadBusy=false,detailReadError="",mutationSubmitted=false,runSnapshot=null,runPromptSnapshot=null,taskRunsPage=null;
  const workRefresh = window.DevBridgeWorkRefresh.create({
    hidden:()=>document.hidden,setTimer:(fn,ms)=>window.setTimeout(fn,ms),clearTimer:id=>window.clearTimeout(id),
    read:async target=>{
      const offset=taskRunsPage?.offset||0,purpose=detailRoute.get()?.runFilter||"";
      const [task,runs]=await Promise.all([
        api(`/ui/api/tasks/${encodeURIComponent(target.taskId)}?${qs({project_id:target.project})}`,{signal:target.signal}),
        api(`/ui/api/tasks/${encodeURIComponent(target.taskId)}/runs?${qs({project_id:target.project,limit:20,offset,purpose})}`,{signal:target.signal})
      ]);
      return {task,runs};
    },
    selectTaskView:payload=>{const view=payload.task?.presentation?.taskView;if(!view||typeof view!=="object"||Array.isArray(view))throw new Error(t("workbench.taskViewUnavailable"));return view;},
    apply:(target,payload)=>{const route=detailRoute.get();if(route?.view==="TASK"&&route.project===target.project&&route.rootTask===target.taskId){taskRunsPage=payload.runs;paintTaskDetail(payload.task);}},
    changed:()=>updateDetailReadState()
  });
  function enterWorkTab(){if(detailRoute.get()?.view==="TASK")workRefresh.refresh("INTERACTIVE");else workRefresh.stop();}
  function workRefreshControl(){return "";}

  async function api(path, options = {}) {
    const embedToken=document.querySelector('meta[name="devbridge-desktop-embed"]')?.content;
    const response = await fetch(path, {...options,headers: {Accept: "application/json", ...(options.body ? {"Content-Type": "application/json"} : {}),...(options.headers||{}),...(embedToken?{"X-Devbridge-Desktop-Embed":embedToken}:{})}});
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {const error=new Error(payload.message || `${response.status} ${response.statusText}`);error.status=response.status;error.code=payload.code;throw error;}
    return payload;
  }

  const sessionWorkbench = window.DevBridgeSessionWorkbench.create({api, t, layout: embeddedLayout ? "embedded" : "console", resourceVersion: "session-results-20261022", filterHost: embeddedLayout ? $("#session-filter-slot") : null});

  function clearProjectData() {
    sessionWorkbench.stop();
    resetExecutionAuthorization();
 workspaceReadVersion++;worktreeReadVersion++;taskListGeneration++;taskListController?.abort();projectReadController?.abort();for(const controller of areaTaskControllers.values())controller.abort();areaTaskControllers.clear();state.taskPageResult=null;state.areaTaskPages={};state.projectDataErrors={};state.areaReadErrors={};workRefresh.stop();detailRoute.close();
 state.worktreeInventory=null;state.worktreeError="";state.worktreeLoading=false;
    state.developmentProjectVersion++;
    state.developmentReadVersion++;
    Object.assign(state, {developmentContext: null, developmentError: "", developmentPendingRepository: "", overview: null, repositories: [], featureAreas: [], modules: [], tasks: [], runs: [], sessions: [], probes: [], approvals: [], projectWorkspace: null, moduleExpanded: {}});
  }

  async function readHealth() {
    try { await api("/readyz"); state.health = true; }
    catch (_) { state.health = false; }
    $("#health-dot").className = `health-dot ${state.health ? "ok" : "bad"}`;
    $("#health-text").textContent = t(state.health ? "health.ready" : "health.unavailable");
  }

  async function loadProjectData() {
    if (!state.selectedProjectID) { clearProjectData(); return; }
    const projectID = state.selectedProjectID;
 const readVersion=++workspaceReadVersion;
    const authorizationSequence = ++state.executionAuthorizationSequence;
    const project = qs({project_id: projectID, limit: 100});
    projectReadController?.abort();const controller=new AbortController();projectReadController=controller;
    const paths={overview:`/v1/project-overview?${project}`,repositories:`/v1/repositories?${project}`,featureAreas:`/ui/api/featureAreas?${qs({project_id:projectID})}`,modules:`/ui/api/modules?${qs({project_id:projectID})}`,tasks:`/v1/tasks?${project}`,runs:`/v1/runs?${project}`,sessions:`/v1/ai-sessions?${project}`,probes:`/v1/context-probes?${project}`,approvals:`/v1/approvals?${project}`,projectWorkspace:`/ui/api/project-workspace?${qs({project_id:projectID})}`,executionAuthorization:`/v1/projects/${encodeURIComponent(projectID)}/execution-authorization`};
    const keys=Object.keys(paths),results=await Promise.allSettled(keys.map(key=>api(paths[key],{signal:controller.signal})));
    if(state.selectedProjectID!==projectID||readVersion!==workspaceReadVersion||controller.signal.aborted)return;
    for(let i=0;i<keys.length;i++){const key=keys[i],result=results[i];if(key==="executionAuthorization"){if(authorizationSequence===state.executionAuthorizationSequence&&!state.executionAuthorizationSaving){state.executionAuthorizationReading=false;applyExecutionAuthorizationRead(result,projectID);}continue;}if(result.status==="fulfilled"){state[key]=["overview","projectWorkspace"].includes(key)?result.value:result.value.items||[];delete state.projectDataErrors[key];}else state.projectDataErrors[key]=result.reason.message;}
    state.projectError=state.projectDataErrors.projectWorkspace||"";
    const projectWorkspace=state.projectWorkspace||{featureAreas:[],tasks:[]};
    if(!state.projectDataErrors.projectWorkspace){
      const normalized=W.normalizeTaskFilters(projectWorkspace,{...state.taskFilters,branch:state.workItemBranchFilter});
      state.taskFilterNotice=normalized.area!==state.taskFilters.area||normalized.branch!==state.workItemBranchFilter;
      if(state.taskFilterNotice)state.taskPage=0;
      state.taskFilters=normalized;state.workItemAreaFilter=normalized.area||"";state.workItemBranchFilter=normalized.branch||"";persistWorkItemFilters();
    }
    if(state.projectTab==="tasks")await loadTaskList();
    if(state.projectTab==="diagnostics"&&state.diagnosticTab==="workspaces")await loadWorktreeInventory();
    if(state.selectedProjectID!==projectID||readVersion!==workspaceReadVersion)return;
    if (state.workItemAreaFilter && !(projectWorkspace.featureAreas || []).some((featureArea) => featureArea.id === state.workItemAreaFilter)) state.workItemAreaFilter = "";
    const visibleBranches = new Set(workItems(projectWorkspace).flatMap((item) => item.development_branches || []));
    if (state.workItemBranchFilter && state.workItemBranchFilter !== "__unknown__" && !visibleBranches.has(state.workItemBranchFilter)) state.workItemBranchFilter = "";
  }

  const LOG_QUERY_FIELDS = {featureArea: "feature_area_id", task: "task_id", execution: "execution_id", run: "run_id"};

  async function loadLogs() {
    const field = LOG_QUERY_FIELDS[state.logFilter.kind] || "";
    const query = {limit: 100};
    if (field && state.logFilter.id) query[field] = state.logFilter.id;
    const payload = await api(`/v1/logs?${qs(query)}`);
    const items = payload.items || [];
    state.logView = LV.nextView(state.logView, items);
    // The server returns newest-first; the Console shows the classic
    // chronological tail so newer entries append at the bottom. A paused
    // view keeps its frozen snapshot and only counts the newer entries.
    if (!state.logView.paused) state.logsRaw = [...items].reverse();
    applyLogFilters();
  }

  function applyLogFilters() {
    const filters = state.logFilters;
    state.logs = LF.filterLogs(state.logsRaw, {level: filters.level, component: filters.component, event: filters.event, text: filters.text, verbose: state.logVerbose});
  }

  function pollLogs() {
    if (state.view !== "logs" && !(state.projectTab==="diagnostics"&&state.diagnosticTab==="logs")) return;
    refreshLogs();
  }

  function scheduleLogPoll() {
    stopLogPoll();
    state.logTimer = window.setTimeout(() => { state.logTimer = null; pollLogs(); }, 3000);
  }

  function stopLogPoll() {
    if (state.logTimer) { window.clearTimeout(state.logTimer); state.logTimer = null; }
  }

  async function loadAll(manual = false) {
    if (embeddedLayout) return loadEmbedded(manual);
    if (manual && state.manualRefreshing) return;
    if (manual) { state.manualRefreshing = true; $("#refresh").disabled = true; }
    const initialLoad = !state.lastSync && !state.projects.length;
    state.loading = initialLoad; state.error = ""; render();
    await readHealth();
    try {
      const [selection, projects] = await Promise.all([api("/v1/project-selection"), api("/v1/projects?limit=100")]);
      state.projects = projects.items || [];
      // Refresh and language switches must not jump the viewer to another
      // project or tab; the server selection only applies when nothing is
      // selected yet or the previously selected project disappeared.
      if (!state.selectedProjectID || !state.projects.some((project) => project.id === state.selectedProjectID)) {
        resetExecutionAuthorization();
        state.selectedProjectID = selection.selected_project_id || "";
        persistProject(state.selectedProjectID);
      }
      await Promise.all([loadProjectData(), loadDevelopmentContext(), loadLogs()]);
      state.lastSync = new Date();
      if (state.view === "logs" || (state.projectTab==="diagnostics"&&state.diagnosticTab==="logs")) scheduleLogPoll();
      if (manual) toast(t("refresh.success"));
 if(manual&&detailRoute.get()?.view==="TASK")await workRefresh.refresh("INTERACTIVE");else if(manual&&detailRoute.get()?.view==="RUN")await readTaskRun();
    } catch (error) {
      if (initialLoad) state.error = error.message;
      if (manual) toast(t("refresh.failed"), true);
    } finally {
      state.loading = false; state.manualRefreshing = false; $("#refresh").disabled = false; render();
    }
  }

  async function loadEmbeddedProjectData() {
    if (!state.selectedProjectID) { clearProjectData(); return; }
    const projectID = state.selectedProjectID;
    const readVersion = ++workspaceReadVersion;
    const payload = await api(`/ui/api/featureAreas?${qs({project_id: projectID})}`);
    if (!payload || !Array.isArray(payload.items) || payload.items.some(item => !item || typeof item !== "object" || typeof item.id !== "string" || !item.id)) throw new Error("Invalid feature-area response.");
    if (state.selectedProjectID === projectID && readVersion === workspaceReadVersion) state.featureAreas = payload.items;
  }

  async function loadEmbedded(manual = false) {
    if (manual && state.manualRefreshing) return;
    if (manual) { state.manualRefreshing = true; $("#refresh").disabled = true; }
    const initialLoad = !state.lastSync && !state.projects.length;
    state.loading = initialLoad; state.error = ""; state.projectError = ""; render();
    await readHealth();
    try {
      const [selection, projects] = await Promise.all([api("/v1/project-selection"), api("/v1/projects?limit=100")]);
      if (!selection || typeof selection.selected !== "boolean" || selection.selected && (typeof selection.selected_project_id !== "string" || !selection.selected_project_id)) throw new Error("Invalid project-selection response.");
      if (!projects || !Array.isArray(projects.items) || typeof projects.has_more !== "boolean" || projects.limit !== 100 || projects.offset !== 0 || projects.items.some(item => !item || typeof item !== "object" || typeof item.id !== "string" || !item.id)) throw new Error("Invalid projects response.");
      state.projects = projects.items;
      if (!state.selectedProjectID || !state.projects.some((project) => project.id === state.selectedProjectID)) {
        resetExecutionAuthorization();
        state.selectedProjectID = selection && typeof selection.selected_project_id === "string" && state.projects.some((project) => project.id === selection.selected_project_id)
          ? selection.selected_project_id
          : "";
        persistProject(state.selectedProjectID);
      }
      if (!embeddedDeepLinkHandled) {
        embeddedDeepLinkHandled = true;
        const target = parseDetailLink(window.location.hash);
        if (target) {
          if (!target.run || !state.projects.some((project) => project.id === target.project)) {
            resetExecutionAuthorization();
            state.selectedProjectID = "";
            persistProject("");
            throw new Error(t("workbench.ownerMismatch"));
          }
          if(state.selectedProjectID!==target.project)resetExecutionAuthorization();
          state.selectedProjectID = target.project;
          persistProject(target.project);
          embeddedRunTarget = target.run;
        }
      }
      await Promise.all([
        loadEmbeddedProjectData(),
        manual && state.selectedProjectID ? sessionWorkbench.refreshList({includeSelectedSnapshot: true}) : Promise.resolve(),
      ]);
      state.lastSync = new Date();
      if (manual) toast(t("refresh.success"));
    } catch (error) {
      if (initialLoad) state.error = error.message;
      if (manual) toast(t("refresh.failed"), true);
    } finally {
      state.loading = false; state.manualRefreshing = false; $("#refresh").disabled = false; render();
    }
  }

  function currentProject() {
    return state.projects.find((project) => project.id === state.selectedProjectID) || (state.overview && state.overview.project) || null;
  }

  function persistProject(projectID) {
    if (projectID) window.localStorage.setItem(PROJECT_KEY, projectID);
    else window.localStorage.removeItem(PROJECT_KEY);
  }

  function parseWorkItemFilters(raw) {
    try {
      const value = JSON.parse(raw || "{}");
      return {area: typeof value.area === "string" ? value.area : "", branch: typeof value.branch === "string" ? value.branch : ""};
    } catch (_) { return {area: "", branch: ""}; }
  }

  function persistWorkItemFilters() {
    window.localStorage.setItem(WORK_ITEM_KEY, JSON.stringify({area: state.workItemAreaFilter, branch: state.workItemBranchFilter}));
  }

  function setProjectTab(tab) {
    const normalized=W.normalizeNavigation("projects",tab);Object.assign(state,normalized);
    window.localStorage.setItem(TAB_KEY,state.projectTab);window.localStorage.setItem(NAV_KEY,"projects");
    closeDetail();enterWorkTab();
    if(state.projectTab==="tasks")loadTaskList();
    if(state.projectTab==="diagnostics"&&state.diagnosticTab==="workspaces")loadWorktreeInventory();
  }
  function workbenchContext(){return {state,C,t,locale:i18n.locale,legacy:{documents:renderDocuments,modules:renderModules,workspaces:renderWorkspaces,logs:renderLogs,diagnostics:renderDiagnostics,areaTaskPanel,areaCommitPanel,featureAreaMenu,documentRows:renderDocumentRows}};}
  async function loadTaskList(){
    taskListController?.abort();const controller=new AbortController();taskListController=controller;const generation=++taskListGeneration,project=state.selectedProjectID,filters={...state.taskFilters};
    if(!project)return;state.taskListLoading=true;state.taskListError="";renderPreservingSearch();
    try{const page=await api(`/ui/api/work-tabs/tasks?${qs({project_id:project,limit:25,offset:state.taskPage*25,search:filters.search,feature_area_id:filters.area,status:filters.status,phase:filters.phase,branch:state.workItemBranchFilter,kind:state.taskKindFilter})}`,{signal:controller.signal});if(generation===taskListGeneration&&project===state.selectedProjectID)state.taskPageResult=page;}
    catch(e){if(generation===taskListGeneration&&e.name!=="AbortError")state.taskListError=e.message;}
    finally{if(generation===taskListGeneration&&project===state.selectedProjectID){state.taskListLoading=false;renderPreservingSearch();}}
  }
  function renderPreservingSearch(){const field=document.activeElement,search=field?.dataset?.workbenchFilter==="search",position=search?field.selectionStart:null;render();if(search){const next=$('[data-workbench-filter="search"]');next?.focus({preventScroll:true});if(next&&position!==null)next.setSelectionRange(position,position);}}
  async function loadAreaTasks(areaID,page=0){
    areaTaskControllers.get(areaID)?.abort();const controller=new AbortController();areaTaskControllers.set(areaID,controller);const project=state.selectedProjectID,filter=state.areaTaskFilters[areaID]||"active";
    const prior=state.areaTaskPages[areaID];state.areaTaskPages[areaID]={...prior,loading:true,error:""};openTopicDetail(areaID,true);
    try{const value=await api(`/ui/api/work-tabs/tasks?${qs({project_id:project,feature_area_id:areaID,status:filter==="active"?"ACTIVE":filter==="completed"?"__terminal__":"",limit:25,offset:page*25})}`,{signal:controller.signal});if(project===state.selectedProjectID&&!controller.signal.aborted)state.areaTaskPages[areaID]=value;}
    catch(e){if(project===state.selectedProjectID&&!controller.signal.aborted)state.areaTaskPages[areaID]={...prior,error:e.message};}
    finally{if(project===state.selectedProjectID&&!controller.signal.aborted&&workflowDrawerTarget===areaID){state.areaTaskPages[areaID].loading=false;openTopicDetail(areaID,true);}}
  }

  function activeTasks() {
    const tasks = state.projectWorkspace ? state.projectWorkspace.tasks : state.tasks;
    return tasks.filter((task) => !["COMPLETED", "SUCCEEDED", "CANCELED"].includes(task.state || task.status));
  }

  function renderPageHeading(title, subtitle, action = "") {
    return `<div class="page-heading"><div><h1>${C.esc(title)}</h1><p>${C.esc(subtitle)}</p></div>${action}</div>`;
  }

  function renderCompactEmpty(title, body) {
    return `<div class="compact-empty-state"><strong>${C.esc(title)}</strong><p>${C.esc(body)}</p></div>`;
  }

  function metric(label, value) {
    return `<article class="metric"><span class="metric-label">${C.esc(label)}</span><strong>${C.esc(value)}</strong></article>`;
  }

  function renderOverview() {
    const project = currentProject();
    const active = activeTasks();
    const recentSource = state.projectWorkspace ? state.projectWorkspace.tasks : state.tasks;
    const recent = [...recentSource].sort((a, b) => new Date(b.updated_at) - new Date(a.updated_at)).slice(0, 4);
    const currentBody = !project ? t("overview.chooseProject") : active.length ? (active[0].title || active[0].goal || active[0].problem_statement) : t("overview.noCurrent");
    const readiness = project ? C.statusBadge("project", project.project_context_status, t) : C.statusBadge("project", "", t);
    return `${renderPageHeading(t("overview.title"), t("overview.subtitle"))}
      <div class="metric-grid">${metric(t("overview.bridge"), state.health ? t("health.ready") : t("health.unavailable"))}${metric(t("overview.projectCount"), state.projects.length)}${metric(t("overview.activeCount"), active.length)}</div>
      ${C.sectionHeader(t("overview.currentTitle"), "")}
      <div class="current-work"><article class="panel"><span class="eyebrow">${C.esc(project ? project.name : t("common.project"))}</span><p class="lead">${C.esc(currentBody)}</p>${project ? `<button class="text-button" type="button" data-open-project>${C.esc(t("action.viewProject"))}</button>` : ""}</article><article class="panel"><h3>${C.esc(t("project.readiness"))}</h3>${readiness}</article></div>
      ${C.sectionHeader(t("overview.recentTitle"), "")}
      <article class="panel">${recent.length ? `<div class="goal-list">${recent.map(renderGoal).join("")}</div>` : C.emptyState(t("overview.noRecent"), t("project.noGoals"))}</article>`;
  }

  function renderGoal(task) {
    const goal = task.title || task.goal || task.problem_statement || t("common.unknown");
    const area = task.feature_area_title || task.module_name || t("common.unknown");
    return `<div class="goal"><div><h3>${C.esc(goal)}</h3><p class="muted">${C.esc(t("project.goalModule", {name: area}))}</p></div>${C.statusBadge(task.state ? "product" : "task", task.state || task.status, t)}</div>`;
  }

  function renderProjectSelector() {
    const project = currentProject();
    const needle = state.projectSearch.trim().toLocaleLowerCase(i18n.locale);
    const projects = state.projects.filter((candidate) => C.projectMatches(candidate, needle));
    const readiness = project ? project.project_context_status : "";
    const refreshState = (candidate) => candidate.project_context_refreshing ? `<span class="refreshing">${C.esc(t("project.refreshing"))}</span>` : candidate.project_context_refresh_failed ? `<span class="refresh-failed">${C.esc(t("project.refreshFailed"))}</span>` : "";
    const trigger = project ? `<span class="project-trigger-name">${C.esc(project.name)}</span><span class="project-trigger-meta">${C.statusBadge("project", readiness, t)}${refreshState(project)}<span>${C.esc(t("projects.repositories", {count: project.repository_count || 0}))}</span></span>` : `<span class="muted">${C.esc(t("projects.choose"))}</span>`;
    return `<div class="project-selector" data-testid="project-selector">
      <label class="project-selector-label" for="project-search-input" data-i18n="projects.selector">${C.esc(t("projects.selector"))}</label>
      <div class="project-combobox"><button type="button" class="project-trigger" aria-haspopup="listbox" aria-expanded="${state.projectDropdownOpen}" data-project-trigger>${trigger}<span class="project-chevron" aria-hidden="true">▾</span></button>
      <div class="project-dropdown${state.projectDropdownOpen ? "" : " hidden"}" role="listbox" aria-label="${C.esc(t("projects.selector"))}" data-project-dropdown>
        <input id="project-search-input" type="search" value="${C.esc(state.projectSearch)}" placeholder="${C.esc(t("projects.searchPlaceholder"))}" autocomplete="off" data-project-search>
        <div class="project-options">${projects.length ? projects.map((candidate) => `<button type="button" role="option" class="project-option${candidate.id === state.selectedProjectID ? " selected" : ""}" data-select-project="${C.esc(candidate.id)}" aria-selected="${candidate.id === state.selectedProjectID}"><span class="project-option-name">${C.esc(candidate.name)}</span><span class="project-option-meta">${C.statusBadge("project", candidate.project_context_status, t)}${refreshState(candidate)}<span>${C.esc(t("projects.repositories", {count: candidate.repository_count || 0}))}</span></span></button>`).join("") : `<p class="compact-empty">${C.esc(t("projects.searchEmpty"))}</p>`}</div>
        <p class="project-selection-note">${C.esc(t("projects.selectionNote"))}</p>
      </div></div>
    </div>`;
  }

  function renderProjects() {
    if (!state.projects.length) return `${renderPageHeading(t("projects.title"), t("projects.subtitle"))}${renderCompactEmpty(t("projects.empty.title"), t("projects.empty.body"))}`;
    return `
      <div class="project-workspace">${state.selectedProjectID ? renderProjectDetail() : renderCompactEmpty(t("projects.choose"), t("projects.chooseBody"))}</div>`;
  }

  function renderEmbeddedSessions() {
    if (state.loading || state.projectLoading) return C.emptyState(t("common.loading"), "");
    if (state.error || state.projectError) return `${C.emptyState(t("error.load.title"), state.error || state.projectError || t("error.load.body"))}<button type="button" data-project-retry>${C.esc(t("workbench.retry"))}</button>`;
    if (!state.selectedProjectID) return C.emptyState(t("projects.choose"), t("projects.selectionNote"));
    return `<div class="embedded-session-workbench"><div id="session-workbench-slot"></div></div>`;
  }

  function renderProjectDetail() {
    const project = currentProject();
    if (!project) return "";
    if (state.projectLoading) return `<div class="project-inline-state"><span class="spinner" aria-hidden="true">◌</span>${C.esc(t("project.loading"))}</div>`;
    if (state.projectError && !state.projectWorkspace && !["diagnostics","sessions"].includes(state.projectTab)) return `<p role="alert">${C.esc(t("workbench.readFailed"))}: ${C.esc(state.projectError)}</p><button type="button" data-project-retry>${C.esc(t("workbench.retry"))}</button>`;
    if (state.projectTab === "sessions") return `<div class="project-detail project-session-detail">${renderProjectTab()}</div>`;
    const workspace = state.projectWorkspace || {featureAreas: [], documents: [], tasks: [], executions: [], workspaces: [], project_context: {module_count: state.modules.length}};
    const activeFeatureAreas = workspace.featureAreas.filter((featureArea) => C.featureAreaProductStatus(featureArea) === "ACTIVE").length;
    return `<div class="project-detail"><header class="project-header"><div><h2>${C.esc(project.name)}</h2><p>${C.esc(t("project.headerMeta", {repositories: project.repository_count || state.repositories.length, modules: workspace.project_context.module_count || 0, featureAreas: activeFeatureAreas, tasks: workspace.tasks.length}))}</p></div>${C.statusBadge("project", workspace.project_context.state || project.project_context_status, t)}</header>
      ${W.renderProjectContext(workbenchContext())}
      <div>${renderProjectTab()}</div></div>`;
  }

  function renderProjectTab() {
    const ctx=workbenchContext();
    if(state.projectTab==="sessions")return `<div id="session-workbench-slot"></div>`;
    if(state.projectTab==="resources")return W.renderResources(ctx);
    if(state.projectTab==="diagnostics")return W.renderDiagnostics(ctx);
    if(state.projectTab==="featureAreas")return W.renderFeatureAreas(ctx);
    if(state.projectTab==="tasks")return W.renderTasks(ctx);
    return renderProjectOverview();
  }
  function renderProjectOverview(){return `${W.renderOverview(workbenchContext())}${renderExecutionAuthorization()}`;}

  function resetExecutionAuthorization() {
    state.executionAuthorizationGeneration++;
    state.executionAuthorizationSequence++;
    Object.assign(state,{executionAuthorization:null,executionAuthorizationDraft:null,executionAuthorizationRequest:null,executionAuthorizationSaving:false,executionAuthorizationReading:false,executionAuthorizationConflict:false,executionAuthorizationError:"",executionAuthorizationNotice:""});
  }
  function validExecutionAuthorization(value,projectID) {
    return !!(value&&value.project_id===projectID&&Number.isSafeInteger(value.head_revision)&&value.head_revision>=0&&typeof value.trusted_execution==="boolean"&&typeof value.network_access==="boolean");
  }
  function executionAuthorizationReady() {
    return validExecutionAuthorization(state.executionAuthorization,state.selectedProjectID)&&!state.projectDataErrors.executionAuthorization&&!state.executionAuthorizationReading&&!state.executionAuthorizationConflict;
  }
  function authorizationDraft(value) {
    return {project_id:value.project_id,base_revision:value.head_revision,trusted_execution:value.trusted_execution,network_access:value.network_access};
  }
  function applyExecutionAuthorizationRead(result,projectID) {
    if(projectID!==state.selectedProjectID)return;
    if(result.status!=="fulfilled"||!validExecutionAuthorization(result.value,projectID)){
      state.projectDataErrors.executionAuthorization=result.reason?.message||t("authorization.readFailed");
      return;
    }
    state.executionAuthorizationError="";
    const value=result.value,draft=state.executionAuthorizationDraft,prior=state.executionAuthorization;
    const dirty=draft&&prior&&draft.project_id===projectID&&(draft.trusted_execution!==prior.trusted_execution||draft.network_access!==prior.network_access);
    if(dirty&&draft.base_revision!==value.head_revision){state.executionAuthorizationConflict=true;state.executionAuthorizationError=t("authorization.changed");state.executionAuthorizationNotice="";}
    else if(!dirty){state.executionAuthorizationDraft=authorizationDraft(value);state.executionAuthorizationConflict=false;state.executionAuthorizationError="";}
    state.executionAuthorization=value;
    delete state.projectDataErrors.executionAuthorization;
  }
  async function refreshExecutionAuthorization(discardDraft=false) {
    if(!state.selectedProjectID||state.executionAuthorizationSaving)return;
    const projectID=state.selectedProjectID,generation=state.executionAuthorizationGeneration,sequence=++state.executionAuthorizationSequence;
    if(discardDraft){state.executionAuthorizationDraft=null;state.executionAuthorizationConflict=false;state.executionAuthorizationError="";state.executionAuthorizationNotice="";}
    state.executionAuthorizationReading=true;render();
    const current=()=>projectID===state.selectedProjectID&&generation===state.executionAuthorizationGeneration&&sequence===state.executionAuthorizationSequence;
    try{const value=await api(`/v1/projects/${encodeURIComponent(projectID)}/execution-authorization`);if(current())applyExecutionAuthorizationRead({status:"fulfilled",value},projectID);}
    catch(error){if(current())applyExecutionAuthorizationRead({status:"rejected",reason:error},projectID);}
    finally{if(current()){state.executionAuthorizationReading=false;render();}}
  }
  function rememberExecutionAuthorizationDraft(form) {
    if(!executionAuthorizationReady()||form.dataset.authorizationProject!==state.selectedProjectID||Number(form.dataset.authorizationGeneration)!==state.executionAuthorizationGeneration)return;
    state.executionAuthorizationDraft={project_id:state.selectedProjectID,base_revision:Number(form.elements.expected_revision.value),trusted_execution:form.elements.trusted_execution.checked,network_access:form.elements.network_access.checked};
    state.executionAuthorizationNotice="";
  }
  function renderExecutionAuthorization() {
    if(!state.selectedProjectID)return "";
    const auth=validExecutionAuthorization(state.executionAuthorization,state.selectedProjectID)?state.executionAuthorization:{project_id:state.selectedProjectID,trusted_execution:false,network_access:false,head_revision:0};
    const draft=state.executionAuthorizationDraft?.project_id===state.selectedProjectID?state.executionAuthorizationDraft:authorizationDraft(auth);
    const saving=state.executionAuthorizationSaving,disabled=saving||!executionAuthorizationReady();
    const message=state.executionAuthorizationError||state.projectDataErrors.executionAuthorization||"";
    return `<section class="execution-authorization" data-execution-authorization>
      <h3>${C.esc(t("authorization.title"))}</h3><p class="authorization-description">${C.esc(t("authorization.description"))}</p>
      ${message?`<p role="alert" class="authorization-error">${C.esc(message)}</p>`:""}
      ${state.executionAuthorizationNotice?`<p class="authorization-notice">${C.esc(state.executionAuthorizationNotice)}</p>`:""}
      ${!state.executionAuthorization&&!message?`<p role="status">${C.esc(t("authorization.loading"))}</p>`:""}
      <form data-authorization-form data-authorization-project="${C.esc(state.selectedProjectID)}" data-authorization-generation="${state.executionAuthorizationGeneration}">
        <label><input type="checkbox" name="trusted_execution" ${draft.trusted_execution?"checked":""} ${disabled?"disabled":""}> ${C.esc(t("authorization.trusted"))}</label>
        <label><input type="checkbox" name="network_access" ${draft.network_access?"checked":""} ${disabled?"disabled":""}> ${C.esc(t("authorization.network"))}</label>
        <input type="hidden" name="expected_revision" value="${draft.base_revision}">
        <button type="submit" ${disabled?"disabled":""}>${C.esc(saving?t("authorization.saving"):t("authorization.save"))}</button>
        <button type="button" data-authorization-refresh ${saving||state.executionAuthorizationReading?"disabled":""}>${C.esc(t("authorization.reload"))}</button>
      </form><p class="authorization-meta">${C.esc(t("authorization.meta",{revision:auth.head_revision,updated:auth.updated_at||"—"}))}</p></section>`;
  }
  async function saveExecutionAuthorization(form) {
    if(!executionAuthorizationReady()||state.executionAuthorizationSaving||form.isConnected===false||form.dataset.authorizationProject!==state.selectedProjectID||Number(form.dataset.authorizationGeneration)!==state.executionAuthorizationGeneration)return;
    const projectID=state.selectedProjectID,generation=state.executionAuthorizationGeneration,revision=Number(form.elements.expected_revision.value);
    if(revision!==state.executionAuthorization.head_revision){state.executionAuthorizationError=t("authorization.changed");state.projectDataErrors.executionAuthorization=state.executionAuthorizationError;render();return;}
    const body=JSON.stringify({trusted_execution:form.elements.trusted_execution.checked,network_access:form.elements.network_access.checked,expected_revision:revision});
    const request={projectID,generation};state.executionAuthorizationRequest=request;state.executionAuthorizationSequence++;
    state.executionAuthorizationSaving=true;state.executionAuthorizationError="";state.executionAuthorizationNotice="";render();
    const current=()=>projectID===state.selectedProjectID&&generation===state.executionAuthorizationGeneration&&state.executionAuthorizationRequest===request;
    try{
      const value=await api(`/v1/projects/${encodeURIComponent(projectID)}/execution-authorization`,{method:"PUT",body});
      if(!current())return;
      if(!validExecutionAuthorization(value,projectID)||value.head_revision!==revision+1)throw new Error(t("authorization.unknown"));
      state.executionAuthorizationSequence++;
      state.executionAuthorization=value;state.executionAuthorizationDraft=authorizationDraft(value);state.executionAuthorizationConflict=false;
      delete state.projectDataErrors.executionAuthorization;
      state.executionAuthorizationNotice=t("authorization.saved");
    }catch(error){if(current()){state.executionAuthorizationSequence++;state.executionAuthorizationError=error.status===409?t("authorization.changed"):error.message;state.projectDataErrors.executionAuthorization=state.executionAuthorizationError;}}
    finally{if(current()){state.executionAuthorizationRequest=null;state.executionAuthorizationSaving=false;render();}}
  }

  function renderActiveAgentOperations(operations) {
    if (!operations.length) return "";
    return `<section class="compact-section"><div class="compact-section-head"><h3>${C.esc(t("project.activeAgentOperations"))}</h3></div><div class="detail-rows">${operations.map((operation) => `<div class="detail-row"><strong>${C.esc(operation.feature_area_title || t("common.unknown"))}</strong>${C.agentOperationStateBadge(operation.state, t)}${C.keyValueDetails([[t("agentOperation.kind"), C.esc(operation.kind || "—")], [t("agentOperation.operation"), technicalValue(operation.operation_id, t("agentOperation.operation"))], [t("agentOperation.run"), technicalValue(operation.run_id, t("agentOperation.run"))], [t("agentOperation.started"), C.localizedTimestamp(operation.started_at, i18n.locale, "—")], [t("workItems.blocker"), C.esc(operation.message || "—")]])}</div>`).join("")}</div></section>`;
  }

  function renderFeatureAreas() {
    const all = state.projectWorkspace && state.projectWorkspace.featureAreas || [];
    const featureAreas = state.featureAreaStatusFilter === "all" ? all : all.filter((featureArea) => C.featureAreaProductStatus(featureArea) === state.featureAreaStatusFilter.toUpperCase());
    const filter = `<label class="inline-filter"><span>${C.esc(t("topics.statusFilter"))}</span><select id="feature-area-status-filter"><option value="active"${state.featureAreaStatusFilter === "active" ? " selected" : ""}>${C.esc(t("topics.filterActive"))}</option><option value="closed"${state.featureAreaStatusFilter === "closed" ? " selected" : ""}>${C.esc(t("topics.filterClosed"))}</option><option value="all"${state.featureAreaStatusFilter === "all" ? " selected" : ""}>${C.esc(t("topics.filterAll"))}</option></select></label>`;
    return `${C.projectTabToolbar(t("topics.subtitle"), filter)}${featureAreas.length ? renderTopicRows(featureAreas) : renderCompactEmpty(t("topics.empty.title"), t("topics.empty.body"))}`;
  }

  function renderDocuments() {
    const documents = state.projectWorkspace && state.projectWorkspace.documents || [];
    return `${C.projectTabToolbar(t("documents.subtitle"))}${documents.length ? renderDocumentRows(documents) : renderCompactEmpty(t("documents.empty.title"), t("documents.empty.body"))}`;
  }

  function renderDocumentRows(documents) {
    return `<div class="dense-table document-table" role="table"><div class="dense-head" role="row"><span>${C.esc(t("documents.columnTitle"))}</span><span>${C.esc(t("documents.topic"))}</span><span>${C.esc(t("documents.preview"))}</span><span>${C.esc(t("common.updated"))}</span></div>${documents.map((document) => `<button class="dense-row document-row" type="button" role="row" data-document-detail="${C.esc(document.id)}"><strong data-label="${C.esc(t("documents.columnTitle"))}">${C.esc(document.title)}</strong><span data-label="${C.esc(t("documents.topic"))}">${C.esc(document.feature_area_title || t("documents.projectLevel"))}</span><span class="muted" data-label="${C.esc(t("documents.preview"))}">${C.esc(document.content_preview || "")}</span><span class="muted" data-label="${C.esc(t("common.updated"))}">${C.localizedTimestamp(document.updated_at, i18n.locale, t("common.unknown"))}</span></button>`).join("")}</div>`;
  }

  function workItems(workspace) {
    return (workspace.tasks || []).map(task => ({id:task.id,name:task.title,feature_area_id:task.feature_area_id,feature_area_title:task.feature_area_title,
      phase:task.phase,phaseLabel:task.phaseLabel,phaseDescription:task.phaseDescription,state:task.status,work_kind:task.work_kind,
      development_branches:task.development_branches||[],updated_at:task.updated_at,commit:task})).sort((a,b)=>String(b.updated_at).localeCompare(String(a.updated_at))||a.id.localeCompare(b.id));
  }

  function renderWorkItemRows(items) {
    if(!items.length)return "";
    const headers=["Task","Phase","Status","Code","Dev",t("common.updated")];
    return `<div class="dense-table task-first-table" role="table"><div class="dense-head" role="row">${headers.map(h=>`<span>${C.esc(h)}</span>`).join("")}</div>${items.map(item=>`<button class="dense-row" type="button" role="row" data-task-detail="${C.esc(item.id)}"><strong data-label="Task">${C.esc(item.name)}</strong><span data-label="Phase" title="${C.esc(item.phase)}">${C.esc(item.phaseLabel||item.phase)}<small>${C.esc(item.phaseDescription||"")}</small></span><span data-label="Status">${C.statusBadge("product",item.state,t)}</span><span data-label="Code">${C.esc(item.commit.commitSummary||(item.commit.commitActivity==="RUNNING"?"正在提交…":item.commit.commitActivity==="FAILED"?"提交失败":item.commit.commitSha?"已提交 "+item.commit.commitSha.slice(0,7):"未提交"))}</span><span data-label="Dev">${C.esc(item.commit.devSummary||(item.commit.mergedCommit?"已合入 "+item.commit.mergedCommit.slice(0,7):"—"))}</span><span data-label="${C.esc(t("common.updated"))}">${C.localizedTimestamp(item.updated_at,i18n.locale,"—")}</span></button>`).join("")}</div>`;
  }

  function renderTasks() {
    const items=workItems(state.projectWorkspace||{}),terminal=item=>["SUCCEEDED","FAILED","CANCELED"].includes(item.state);
    const filtered=items.filter(item=>(!state.workItemAreaFilter||item.feature_area_id===state.workItemAreaFilter)&&
      (!state.workItemBranchFilter||(state.workItemBranchFilter==="__unknown__"?!item.development_branches.length:item.development_branches.includes(state.workItemBranchFilter)))&&
      (state.taskFilter==="all"||(state.taskFilter==="active"?!terminal(item):terminal(item)))&&
      (state.taskKindFilter==="all"||(state.taskKindFilter==="unclassified"?!item.work_kind:item.work_kind===state.taskKindFilter)));
    const areas=(state.projectWorkspace?.featureAreas||[]).map(area=>`<option value="${C.esc(area.id)}"${state.workItemAreaFilter===area.id?" selected":""}>${C.esc(area.title)}</option>`).join("");
    const branches=[...new Set(items.flatMap(item=>item.development_branches))].sort().map(branch=>`<option value="${C.esc(branch)}"${state.workItemBranchFilter===branch?" selected":""}>${C.esc(branch.replace(/^refs\/heads\//,""))}</option>`).join("");
    const kinds=["FEATURE","BUG_FIX","REFACTOR","MAINTENANCE","TEST_CHANGE","DOCUMENTATION","OTHER"].map(kind=>`<option value="${kind}"${state.taskKindFilter===kind?" selected":""}>${C.esc(t(`workKind.${kind}`))}</option>`).join("");
    const filters=`<div class="task-filters"><label class="inline-filter">${C.esc(t("tasks.topic"))}<select id="work-item-area-filter"><option value="">${C.esc(t("workItems.areaAll"))}</option>${areas}</select></label><label class="inline-filter">${C.esc(t("branches.development"))}<select id="work-item-branch-filter"><option value="">${C.esc(t("branches.all"))}</option>${branches}<option value="__unknown__"${state.workItemBranchFilter==="__unknown__"?" selected":""}>${C.esc(t("branches.unknown"))}</option></select></label><label class="inline-filter">Status<select id="task-filter">${["all","active","completed"].map(value=>`<option value="${value}"${state.taskFilter===value?" selected":""}>${C.esc(t({all:"tasks.filterAll",active:"tasks.filterActive",completed:"tasks.filterCompleted"}[value]))}</option>`).join("")}</select></label><label class="inline-filter">${C.esc(t("tasks.kindFilter"))}<select id="task-kind-filter"><option value="all">${C.esc(t("tasks.kindFilterAll"))}</option><option value="unclassified"${state.taskKindFilter==="unclassified"?" selected":""}>${C.esc(t("workKind.UNCLASSIFIED"))}</option>${kinds}</select></label></div>`;
    return `${C.projectTabToolbar(t("tasks.subtitle"),filters)}${filtered.length?renderWorkItemRows(filtered):renderCompactEmpty(t("tasks.empty.title"),t("workItems.empty.body"))}`;
  }


  function renderExecutions() {
    const executions = state.projectWorkspace && state.projectWorkspace.executions || [];
    return `${C.projectTabToolbar(t("executions.subtitle"),workRefreshControl("executions"))}${executions.length ? renderExecutionRows(executions) : renderCompactEmpty(t("executions.empty.title"), t("executions.empty.body"))}`;
  }

  function renderCompactSection(title, tab, body, empty) {
    return `<section class="compact-section"><div class="compact-section-head"><h3>${C.esc(title)}</h3><button class="text-button" type="button" data-project-tab="${tab}">${C.esc(t("action.viewAll"))}</button></div>${body || `<p class="compact-empty">${C.esc(empty)}</p>`}</section>`;
  }

  function featureAreaRetireControl(topic) {
    if(!topic.can_retire||["RETIRING","RETIRED","DECOMPOSED"].includes(topic.status))return "";
    return `<button class="text-button destructive" type="button" data-close-feature-area="${C.esc(topic.id)}">关闭功能领域</button>`;
  }
  function featureAreaMenu(topic) {
    return `<details class="feature-menu" data-row-action><summary aria-label="${C.esc(t("topics.menu"))}">⋯</summary><div class="feature-menu-items">${featureAreaRetireControl(topic)}</div></details>`;
  }
  function renderTopicRows(topics, compact = false) {
    if(!topics.length)return "";
    return `<div class="dense-table topic-table${compact?" compact":""}" role="table"><div class="dense-head" role="row"><span>功能领域</span><span>领域状态</span><span>Current Task</span><span>Code / Dev</span><span>Tasks</span><span>${C.esc(t("common.updated"))}</span></div>${topics.map(topic=>{const p=topic.presentation,current=topic.product?.currentTask,task=(state.projectWorkspace?.tasks||[]).find(v=>v.id===current?.id);return `<div class="dense-row topic-row" role="row" tabindex="0" data-topic-row="${C.esc(topic.id)}" aria-label="${C.esc(topic.title)}"><div class="topic-primary" data-label="功能领域"><button class="topic-detail-button" type="button" data-topic-detail="${C.esc(topic.id)}">${C.esc(topic.title)}</button>${featureAreaMenu(topic)}</div><span data-label="领域状态">${C.featureAreaStatusBadge(topic,t)}</span><span data-label="Current Task">${task?`<button class="text-button" type="button" data-task-detail="${C.esc(task.id)}">${C.esc(task.title)}</button>`:"—"}</span><span data-label="Code / Dev">${C.esc(p?.commitView?.summary||"—")}<small>${C.esc(p?.mergedCommit?"Dev · "+p.mergedCommit.slice(0,7):"")}</small></span><span data-label="Tasks">${C.esc(topic.task_count||0)}</span><span data-label="${C.esc(t("common.updated"))}">${C.localizedTimestamp(topic.updated_at,i18n.locale,"—")}</span></div>`;}).join("")}</div>`;
  }
  function renderTaskRows(tasks) {return renderWorkItemRows(workItems({tasks}));}


  const executionCancelRequests=new Map();
  function executionCancelControl(execution){
    const cap=execution.cancellation_capability,cancel=execution.cancellation;
    if(cancel && cancel.state==="CANCEL_REQUESTED")return `<button disabled>正在终止…</button>`;
    if(!cap || !cap.available || !cap.target || cap.target.executionId!==execution.id || (cap.target.runId||"")!==(execution.run_id||""))return "";
    return `<button class="text-button" type="button" data-execution-cancel="${C.esc(execution.id)}" data-run-id="${C.esc(cap.target.runId||"")}" data-upper-version="${C.esc(JSON.stringify(cap.target.expectedUpperVersion||null))}">终止执行</button>`;
  }
  async function cancelExecution(button){
    if(button.disabled)return;
    const project=state.selectedProjectID,execution=button.dataset.executionCancel,run=button.dataset.runId;
    const expectedUpperVersion=JSON.parse(button.dataset.upperVersion||"null"),versionInput=expectedUpperVersion?{expectedUpperVersion}:{};
    const drawerTask=workDrawerTaskID,drawerVersion=workflowDrawerVersion;
    if(!window.confirm("终止当前执行？\n\nDev Bridge 将请求 Codex 停止当前 Execution。\n已有运行记录和历史会保留。"))return;
    const key=taskActionIdentity([project,execution,run,versionInput]);if(!executionCancelRequests.has(key))executionCancelRequests.set(key,window.crypto.randomUUID());
    button.disabled=true;button.textContent="正在终止…";
    try {await api(`/ui/api/executions/${encodeURIComponent(execution)}/cancel?${qs({project_id:project})}`,{method:"POST",body:JSON.stringify({runId:run,...versionInput,requestId:executionCancelRequests.get(key)})});}
    catch(error){if(state.selectedProjectID===project)toast(error.message,true);}
    finally {
      if(state.selectedProjectID===project){
        await workRefresh.freshAfterAction();
        if(state.view==="diagnostics"){
          try{
            const payload=await api(`/ui/api/work-tabs/executions?${qs({project_id:project})}`);
            if(state.selectedProjectID===project){workspaceReadVersion++;state.projectWorkspace.executions=payload.items||[];render();if(workDrawerExecutionID)openExecutionDetail(workDrawerExecutionID);}
          }catch(error){if(state.selectedProjectID===project)toast(error.message,true);}
        }
        if(drawerTask){
          try{
            const task=await api(`/ui/api/tasks/${encodeURIComponent(drawerTask)}?${qs({project_id:project})}`);
            if(state.selectedProjectID===project && workflowDrawerVersion===drawerVersion)openTaskDetail(drawerTask,task);
          }catch(error){if(state.selectedProjectID===project)toast(error.message,true);}
        }
      }
    }
  }

  function openExecutionDetail(id){
    const execution=(state.projectWorkspace && state.projectWorkspace.executions || []).find(v=>v.id===id);if(!execution)return;
    workRefresh.stop();workDrawerExecutionID=id;workDrawerTaskID="";agentRequests.clear();nativeApprovalRuntime.clear();
    $("#detail-title").textContent=execution.task_title;
    $("#detail-body").innerHTML=`${execution.run_id&&execution.executorKind!=="UPPER_LLM"?`<button type="button" data-open-run-session="${C.esc(execution.run_id)}">查看会话</button>`:""}<button type="button" class="text-button" data-parent-task="${C.esc(execution.task_id)}">查看 Task · ${C.esc(execution.task_id)}</button>${C.keyValueDetails([["AgentExecution",C.esc(execution.id)],["Kind",C.esc(execution.kind)],["Domain",C.esc(execution.domain_result || execution.domain_status || "—")],["Backend",C.esc(execution.backend_disposition || "—")],["Run",C.esc(execution.run_id)],["Phase",C.esc(execution.phase)],["状态",C.statusBadge("product",execution.state,t)]])}${C.keyValueDetails([["执行方式",C.esc(execution.reviewActivity?.executionMethod || (execution.agent_runtime?"Codex":"未报告执行方"))],...(execution.executorKind==="UPPER_LLM"?[["活动",C.esc(execution.reviewActivity?.notice || execution.reviewActivity?.summary || "等待发起对话继续")]]:[["Codex 会话",C.esc(execution.agent_runtime?.agent?.sessionName || "未报告")]])])}${C.detailDisclosure("运行诊断",C.keyValueDetails([["Thread ID",C.esc(execution.agent_runtime && execution.agent_runtime.threadId || "—")],["Turn ID",C.esc(execution.agent_runtime && execution.agent_runtime.turnId || "—")],["Process",execution.agent_runtime && execution.agent_runtime.agent && execution.agent_runtime.agent.pid ? `PID ${C.esc(execution.agent_runtime.agent.pid)}`:"UNAVAILABLE"],["Launch",C.esc(execution.agent_runtime && execution.agent_runtime.agent && execution.agent_runtime.agent.processLaunchId || "UNAVAILABLE")]]))}<pre>${C.esc(JSON.stringify(execution,null,2))}</pre>`;
    openDetail();
    $$("[data-agent-answer],[data-agent-recovery]").forEach(button=>button.addEventListener("click",()=>answerAgentRequest(button)));
  }
  async function openParentTask(id){
    const project=state.selectedProjectID,version=workflowDrawerVersion;
    try{const task=await api(`/ui/api/tasks/${encodeURIComponent(id)}?${qs({project_id:project})}`);if(project===state.selectedProjectID && version===workflowDrawerVersion)openTaskDetail(id,task);}
    catch(error){if(project===state.selectedProjectID && version===workflowDrawerVersion)toast(error.message,true);}
  }
  function renderExecutionRows(executions, compact = false) {
    if (!executions.length) return "";
    return `<div class="dense-table execution-table${compact ? " compact" : ""}" role="table"><div class="dense-head" role="row"><span>${C.esc(t("executions.task"))}</span><span>${C.esc(t("common.status"))}</span><span>${C.esc(t("executions.verification"))}</span><span>${C.esc(t("executions.commit"))}</span><span>${C.esc(t("executions.time"))}</span></div>${executions.map((execution) => `<div class="execution-entry"><button class="dense-row execution-row" type="button" role="row" data-execution-detail="${C.esc(execution.id)}"><strong data-label="${C.esc(t("executions.task"))}">${C.esc(execution.task_title)}<small>${C.esc(execution.reviewActivity?.executionMethod || execution.kind || "DEVELOPMENT")} · ${C.esc(execution.reviewActivity?.notice || execution.reviewActivity?.summary || execution.domain_result || execution.domain_status || execution.phase || "")}</small></strong><span data-label="${C.esc(t("common.status"))}">${C.statusBadge("product", execution.state, t)}</span><span data-label="${C.esc(t("executions.verification"))}">${C.esc(execution.verification ? t(`verification.${execution.verification}`) : t("common.none"))}</span><code data-label="${C.esc(t("executions.commit"))}">${C.esc(execution.commit_ref ? execution.commit_ref.slice(0, 10) : "—")}</code><span class="muted" data-label="${C.esc(t("executions.time"))}">${C.localizedTimestamp(execution.updated_at, i18n.locale, t("common.unknown"))}</span></button></div>`).join("")}</div>`;
  }

  function renderContextSummary(context = {}) {
    const stateLabel = context.state && context.state !== "UNAVAILABLE" ? C.statusBadge("project", context.state, t) : C.statusBadge("product", "UNAVAILABLE", t);
    const refreshing = context.refreshing ? `<span class="refreshing">${C.esc(t("project.refreshing"))}</span>` : "";
    const refreshFailed = context.refresh_failed ? `<span class="refresh-failed">${C.esc(t("project.refreshFailed"))}</span>` : "";
    return `<section class="compact-section context-summary"><div><h3>${C.esc(t("project.context"))}</h3><p>${stateLabel}${refreshing}${refreshFailed}<span>${C.esc(t("project.contextModules", {count: context.module_count || 0}))}</span>${context.refreshed_at ? `<span>${C.esc(t("project.refreshed"))} ${C.localizedTimestamp(context.refreshed_at, i18n.locale, t("common.unknown"))}</span>` : ""}</p></div><button class="text-button" type="button" data-project-tab="modules">${C.esc(t("action.viewModules"))}</button></section>`;
  }

  function renderModules() {
    const needle = state.moduleSearch.trim().toLocaleLowerCase(i18n.locale);
    const filtered = state.modules.filter((module) => C.moduleMatches(module, needle));
    const search = `<label class="module-search"><span>${C.esc(t("modules.search"))}</span><input id="module-search" type="search" value="${C.esc(state.moduleSearch)}" placeholder="${C.esc(t("modules.searchPlaceholder"))}" autocomplete="off"></label>`;
    const collapse = filtered.some((module) => state.moduleExpanded[module.name]) ? `<button class="text-button" type="button" data-modules-collapse>${C.esc(t("modules.collapseAll"))}</button>` : "";
    return `${C.projectTabToolbar(t("modules.subtitle"), collapse)}${search}${filtered.length ? `<div class="card-grid module-grid">${filtered.map((module) => C.moduleCard(module, t, state.moduleExpanded[module.name] === true)).join("")}</div>` : renderCompactEmpty(t("modules.searchEmpty"), t("modules.searchEmptyBody"))}`;
  }

  function renderWorkspaces() {
    const status = state.worktreeLoading ? `<p role="status">${C.esc(t("common.loading"))}</p>` : state.worktreeError ? `<p role="alert">${C.esc(state.worktreeError)}</p>` : "";
    return `${C.projectTabToolbar(t("workspaces.subtitle"), `<button type="button" class="button secondary" data-worktree-refresh${state.worktreeLoading ? " disabled" : ""}>${C.esc(t("action.refresh"))}</button>`)}${status}${C.worktreeInventory(state.worktreeInventory, t, i18n.locale)}`;
  }

  async function loadWorktreeInventory() {
    const projectID=state.selectedProjectID, version=++worktreeReadVersion;
    if(!projectID)return;
    state.worktreeLoading=true;state.worktreeError="";
    const current=()=>state.selectedProjectID===projectID && version===worktreeReadVersion;
    try {
      const result=await api(`/ui/api/worktree-inventory?${qs({project_id:projectID})}`);
      if(current())state.worktreeInventory=result;
    } catch(error) { if(current()){state.worktreeInventory=null;state.worktreeError=t("inventory.error");} }
    finally { if(current()){state.worktreeLoading=false;render();} }
  }

  function knownLogObjects(kind) {
    if (kind === "featureArea") return state.featureAreas.map((value) => ({id: value.id, label: value.title}));
    if (kind === "task") return state.tasks.map((value) => ({id: value.id, label: value.goal || value.problem_statement || t("common.unknown")}));
    if (kind === "run") return state.runs.map((value) => ({id: value.id, label: value.task_goal || `${t("logs.run")} ${value.attempt || ""}`}));
    if (kind === "execution") return (state.projectWorkspace && state.projectWorkspace.executions || []).map((value) => ({id: value.id, label: `${value.kind} · ${value.task_title}`}));
    return [];
  }

  function renderLogObjectSelect() {
    const values = knownLogObjects(state.logFilter.kind);
    const all = `<option value="">${C.esc(t("logs.allObjects"))}</option>`;
    return `${all}${values.map((value) => `<option value="${C.esc(value.id)}"${value.id === state.logFilter.id ? " selected" : ""}>${C.esc(value.label)}</option>`).join("")}`;
  }

  function renderLogToolbar() {
    const facets = LF.logFacets(state.logsRaw);
    const option = (value, label, current) => `<option value="${C.esc(value)}"${value === current ? " selected" : ""}>${C.esc(label)}</option>`;
    const levelOptions = [option("", t("logs.allLevels"), state.logFilters.level), ...facets.levels.map((value) => option(value, value, state.logFilters.level))].join("");
    const componentOptions = [option("", t("logs.allComponents"), state.logFilters.component), ...facets.components.map((value) => option(value, value, state.logFilters.component))].join("");
    const eventOptions = [option("", t("logs.allEvents"), state.logFilters.event), ...facets.events.map((value) => option(value, value, state.logFilters.event))].join("");
    const pauseLabel = state.logView.paused ? t("logs.resume") : t("logs.pause");
    const pending = state.logView.pendingCount ? `<button id="logs-jump" class="log-jump" type="button">${C.esc(t("logs.pending", {count: state.logView.pendingCount}))}</button>` : "";
    return `<div class="log-toolbar"><form id="log-filters" class="log-filters">
      <div class="field"><label for="log-kind">${C.esc(t("logs.objectType"))}</label><select id="log-kind" name="kind"><option value="">${C.esc(t("logs.allObjects"))}</option>${["featureArea","task","execution","run"].map((kind) => `<option value="${kind}"${state.logFilter.kind === kind ? " selected" : ""}>${C.esc(t(`logs.${kind}`))}</option>`).join("")}</select></div>
      <div class="field"><label for="log-object">${C.esc(t("logs.object"))}</label><select id="log-object" name="object"${state.logFilter.kind ? "" : " disabled"}>${renderLogObjectSelect()}</select></div>
      <div class="field"><label for="log-level">${C.esc(t("logs.level"))}</label><select id="log-level">${levelOptions}</select></div>
      <div class="field"><label for="log-component">${C.esc(t("logs.component"))}</label><select id="log-component">${componentOptions}</select></div>
      <div class="field"><label for="log-event">${C.esc(t("logs.event"))}</label><select id="log-event">${eventOptions}</select></div>
      <div class="field"><label for="log-text">${C.esc(t("logs.search"))}</label><input id="log-text" type="search" value="${C.esc(state.logFilters.text)}" placeholder="${C.esc(t("logs.searchPlaceholder"))}" autocomplete="off"></div>
      <button class="button secondary" type="submit">${C.esc(t("action.apply"))}</button><button id="logs-clear" class="text-button" type="button">${C.esc(t("action.clear"))}</button>
    </form>
    <div class="log-controls">
      <label class="inline-filter"><input id="log-verbose" type="checkbox"${state.logVerbose ? " checked" : ""}><span>${C.esc(t("logs.verbose"))}</span></label>
      <button id="logs-pause" class="button secondary" type="button">${C.esc(pauseLabel)}</button>
      ${pending}<span class="follow-state${state.logView.follow ? "" : " off"}">${C.esc(state.logView.follow ? t("logs.following") : t("logs.pausedLabel"))}</span>
    </div></div>`;
  }

  function renderLogs() {
    const filterNotice = state.logFilter.id ? `<p class="selection-note">${C.esc(t("logs.filtered", {type: t(`logs.${state.logFilter.kind}`), name: state.logFilter.label || state.logFilter.id}))}</p>` : "";
    const body = state.logs.length ? `<div class="table-wrap"><table><thead><tr><th>${C.esc(t("logs.timestamp"))}</th><th>${C.esc(t("logs.level"))}</th><th>${C.esc(t("logs.component"))}</th><th>${C.esc(t("logs.event"))}</th><th>${C.esc(t("logs.detail"))}</th></tr></thead><tbody>${state.logs.map((entry, index) => `<tr class="log-row"><td>${C.localizedTimestamp(entry.timestamp, i18n.locale, t("common.unknown"))}</td><td><span class="level ${C.esc(String(entry.level || "").toLowerCase())}">${C.esc(entry.level || "—")}</span></td><td>${C.esc(entry.component || "Bridge")}</td><td>${C.esc(C.eventSummary(entry, t))}</td><td><button type="button" class="text-button log-detail" data-log-index="${index}">${LF.formatLogLine(entry, t)}</button></td></tr>`).join("")}</tbody></table></div>` : C.emptyState(t("logs.empty.title"), t("logs.empty.body"));
    return `${renderPageHeading(t("logs.title"), t("logs.subtitle"), `<button id="logs-refresh" class="button secondary" type="button">${C.esc(t("action.refreshLogs"))}</button>`)}
      ${renderLogToolbar()}${filterNotice}${body}`;
  }

  function technicalValue(value, label) {
    if (!value) return "—";
    return `<span class="mono truncate" title="${C.esc(value)}">${C.esc(value)}</span>${C.copyButton(value, label, t)}`;
  }

  function diagnosticItems(values, renderer, emptyText = "") {
    return values.length ? `<div class="diagnostic-list">${values.map(renderer).join("")}</div>` : `<span class="muted">${C.esc(emptyText || t("common.none"))}</span>`;
  }

  function renderDiagnostics() {
    if (!state.selectedProjectID) return `${renderPageHeading(t("diagnostics.title"), t("diagnostics.subtitle"))}${C.emptyState(t("diagnostics.noProject"), t("projects.selectionNote"))}`;
    const projectDetails = C.keyValueDetails([[t("diagnostics.internalId"), technicalValue(state.selectedProjectID, t("diagnostics.internalId"))], [t("diagnostics.rawStatus"), `<code>${C.esc(state.overview && state.overview.project_context && state.overview.project_context.status || "—")}</code>`]]);
    const repositories = diagnosticItems(state.repositories, (item) => `<div class="diagnostic-item"><strong>${C.esc(item.name)}</strong>${C.keyValueDetails([[t("diagnostics.repositoryId"), technicalValue(item.id, t("diagnostics.repositoryId"))], [t("diagnostics.head"), technicalValue(item.current_head, t("diagnostics.head"))], [t("diagnostics.rawStatus"), `<code>${C.esc(item.head_status || "—")}</code>`]])}</div>`);
    const sessions = diagnosticItems(state.sessions, (item) => `<div class="diagnostic-item"><strong>${C.esc(t("diagnostics.aiSession"))}</strong>${technicalValue(item.id, t("diagnostics.aiSession"))}<p class="muted">${C.esc(t("diagnostics.recent"))}: ${C.localizedTimestamp(item.recent_activity_at, i18n.locale, "—")}</p></div>`);
    const probes = diagnosticItems(state.probes, (item) => `<div class="diagnostic-item"><strong>${C.esc(t("diagnostics.contextProbe"))}</strong>${technicalValue(item.id, t("diagnostics.contextProbe"))}${C.keyValueDetails([[t("diagnostics.rawStatus"), `<code>${C.esc(item.status || "—")}</code>`], [t("diagnostics.scope"), `<code>${C.esc(item.owner_kind || item.scope_kind || "—")}</code>`], ["Repositories", C.esc((item.selected_repository_ids || []).join(", ") || "—")], ["Primary member", C.esc(item.primary_member_id || "—")], ["Workspace", C.esc(item.workspace_id || "—")], ["Thread / Turn", C.esc([item.thread_id,item.turn_id].filter(Boolean).join(" / ") || "—")], ["Result", C.esc(item.result_summary || item.failure_summary || "—")], ["Runtime", C.esc(item.runtime_limitation || "canonical")], [t("diagnostics.duration"), C.esc(item.duration_ms == null ? "—" : `${item.duration_ms} ms`)]])}${item.prompt_available ? `<button class="text-button" type="button" data-view-probe-prompt="${C.esc(item.id)}">${C.esc(t("action.viewPrompt"))}</button>` : ""}</div>`);
    const tasks = diagnosticItems(state.tasks, (item) => `<div class="diagnostic-item"><strong>${C.esc(item.goal || item.problem_statement || t("diagnostics.task"))}</strong>${C.keyValueDetails([[t("diagnostics.internalId"), technicalValue(item.id, t("diagnostics.task"))], [t("diagnostics.rawStatus"), `<code>${C.esc(item.status || "—")}</code>`], [t("diagnostics.run"), item.latest_run ? technicalValue(item.latest_run.id, t("diagnostics.run")) : "—"]])}<button class="text-button" type="button" data-log-kind="task" data-log-id="${C.esc(item.id)}" data-log-label="${C.esc(item.goal || item.problem_statement || t("diagnostics.task"))}">${C.esc(t("action.viewLogs"))}</button> <button class="text-button" type="button" data-review-prompts="${C.esc(item.id)}">${C.esc(t("action.viewPrompts"))}</button>${item.latest_run && item.latest_run.prompt_available ? ` <button class="text-button" type="button" data-view-run-prompt="${C.esc(item.latest_run.id)}">${C.esc(t("action.viewPrompt"))}</button>` : ""}</div>`);
    const approvals = diagnosticItems(state.approvals, (item) => `<div class="diagnostic-item"><strong>${C.esc(item.kind || t("diagnostics.approvals"))}</strong>${C.keyValueDetails([[t("diagnostics.internalId"), technicalValue(item.id, t("diagnostics.approvals"))], [t("diagnostics.rawStatus"), `<code>${C.esc(item.status || "—")}</code>`], [t("diagnostics.run"), technicalValue(item.run_id, t("diagnostics.run"))]])}</div>`);
    return `${renderPageHeading(t("diagnostics.title"), t("diagnostics.subtitle"))}${C.detailDisclosure("Executions · runtime diagnostics",renderExecutions())}${C.detailDisclosure("Runs",`<pre>${C.esc(JSON.stringify(state.runs,null,2))}</pre>`)}${C.detailDisclosure(t("diagnostics.project"), projectDetails, true)}${C.detailDisclosure(t("diagnostics.repositories"), repositories)}${C.detailDisclosure(t("diagnostics.sessions"), sessions)}${C.detailDisclosure(t("diagnostics.probes"), probes)}${C.detailDisclosure(t("diagnostics.tasks"), tasks)}${C.detailDisclosure(t("diagnostics.approvals"), approvals)}${C.detailDisclosure(t("diagnostics.raw"), `<pre>${C.esc(JSON.stringify({overview: state.overview, repositories: state.repositories, sessions: state.sessions, probes: state.probes, tasks: state.tasks, runs: state.runs, approvals: state.approvals}, null, 2))}</pre>`)}`;
  }

  function renderLoading() {
    return `${renderPageHeading(t(`nav.${state.view}`), "")}<div class="empty-state"><span class="empty-mark" aria-hidden="true">◌</span><h2>${C.esc(t("common.loading"))}</h2></div>`;
  }

  function renderError() {
    return `${renderPageHeading(t(`nav.${state.view}`), "")}${C.emptyState(t("error.load.title"), t("error.load.body"))}${C.detailDisclosure(t("error.detail"), `<pre>${C.esc(state.error)}</pre>`)}`;
  }

  function render() {
    const showingSessions = embeddedLayout || state.projectTab === "sessions";
    const sessionElement = showingSessions ? $("#session-workbench-slot") : null;
    if(!showingSessions)sessionWorkbench.stop();
    i18n.applyDocument(document);
    if (embeddedLayout) {
      $("#health-dot").className = `health-dot ${state.health ? "ok" : "bad"}`;
      $("#health-text").textContent = t(state.health ? "health.ready" : "health.unavailable");
      $("#last-sync").textContent = state.lastSync ? t("sync.at", {time: state.lastSync.toLocaleTimeString(i18n.locale, {hour: "2-digit", minute: "2-digit"})}) : t("sync.never");
      $("#project-selector-slot").innerHTML = renderProjectSelector();
      $("#page").innerHTML = renderEmbeddedSessions();
      bindRenderedEvents();
      const sessionSlot = $("#session-workbench-slot");
      if(sessionSlot){if(sessionElement&&sessionElement!==sessionSlot)sessionSlot.replaceWith(sessionElement);sessionWorkbench.mount(sessionElement||sessionSlot,state.selectedProjectID,state.featureAreas);if(embeddedRunTarget){const runID=embeddedRunTarget;embeddedRunTarget="";sessionWorkbench.select(runID);}}
      return;
    }
    $("#language-select").value = i18n.requestedLanguage;
    $("#health-dot").className = `health-dot ${state.health ? "ok" : "bad"}`;
    $("#health-text").textContent = t(state.health ? "health.ready" : "health.unavailable");
    $("#last-sync").textContent = state.lastSync ? t("sync.at", {time: state.lastSync.toLocaleTimeString(i18n.locale, {hour: "2-digit", minute: "2-digit"})}) : t("sync.never");
    $("#project-selector-slot").innerHTML = renderProjectSelector();
    $("#development-selector-slot").innerHTML = DB.render(state.developmentContext, t, C.esc, {loading: state.developmentLoading, error: state.developmentError, pendingRepository: state.developmentPendingRepository});
    $$(".nav-item").forEach((item) => { const active=item.dataset.productNav?state.view==="projects"&&item.dataset.productNav===state.projectTab:item.dataset.view===state.view;item.classList.toggle("active",active);item.setAttribute("aria-current",active?"page":"false"); });
    if (state.loading) $("#page").innerHTML = renderLoading();
    else if (state.error) $("#page").innerHTML = renderError();
    else $("#page").innerHTML = ({overview: renderOverview, projects: renderProjects, logs: renderLogs, diagnostics: renderDiagnostics})[state.view]();
    bindRenderedEvents();
    const sessionSlot = $("#session-workbench-slot");
    if(sessionSlot){if(sessionElement&&sessionElement!==sessionSlot)sessionSlot.replaceWith(sessionElement);sessionWorkbench.mount(sessionElement||sessionSlot,state.selectedProjectID,state.featureAreas);}
  }

  function openRunSession(runID){
    if(!runID)return;closeDetail();setProjectTab("sessions");render();sessionWorkbench.select(runID);
  }

  async function selectProject(projectID) {
    if (!projectID || projectID === state.selectedProjectID) return;
    const previousProjectID = state.selectedProjectID;
    const selectionGeneration=++linkGeneration;
    state.projectLoading = true; state.projectError = ""; state.selectedProjectID = projectID; persistProject(projectID); if (!embeddedLayout) setProjectTab("overview"); clearProjectData(); render();
    try {
      if (embeddedLayout) await loadEmbeddedProjectData();
      else await Promise.all([loadProjectData(), loadDevelopmentContext()]);
      state.lastSync = new Date();
    } catch (error) {
      if(selectionGeneration!==linkGeneration)return;state.projectError = error.message;
      if (!currentProject()) { state.selectedProjectID = previousProjectID; persistProject(previousProjectID); }
    }
    finally { if(selectionGeneration===linkGeneration){state.projectLoading = false; render();} }
  }

  async function loadDevelopmentContext() {
    const projectID = state.selectedProjectID;
    const readVersion = ++state.developmentReadVersion;
    const isCurrent = () => state.selectedProjectID === projectID && state.developmentReadVersion === readVersion;
    if (!projectID) { state.developmentContext = null; state.developmentLoading = false; return; }
    state.developmentLoading = true;
    try {
      const value = await api(`/v1/development-context?${qs({project_id: projectID})}`);
      if (isCurrent()) { state.developmentContext = value; state.developmentError = ""; }
    } catch (error) {
      if (isCurrent()) { state.developmentContext = null; state.developmentError = error.message; }
    } finally {
      if (isCurrent()) { state.developmentLoading = false; render(); }
    }
  }

  async function selectDevelopmentBranch(repositoryID, developmentRef) {
    const projectID = state.selectedProjectID;
    const projectVersion = state.developmentProjectVersion;
    const isCurrent = () => state.selectedProjectID === projectID && state.developmentProjectVersion === projectVersion;
    if (!projectID || !developmentRef || state.developmentPendingRepository) return;
    const repo = (state.developmentContext && state.developmentContext.repositories || []).find((item) => item.repositoryId === repositoryID);
    if (!repo || !(repo.branches || []).some((branch) => branch.fullRef === developmentRef)) return;
    state.developmentPendingRepository = repositoryID;
    render();
    try {
      const command = DB.command(window.localStorage, projectID, repositoryID, developmentRef, () => window.crypto.randomUUID());
      await api("/v1/development-branch-selection", {method: "POST", body: JSON.stringify(command.body)});
      window.localStorage.removeItem(command.key);
      if (!isCurrent()) return;
      await loadDevelopmentContext();
      toast(t("development.note"));
    } catch (error) {
      if (isCurrent()) toast(error.message, true);
    } finally {
      if (isCurrent()) { state.developmentPendingRepository = ""; render(); }
    }
  }

  async function refreshLogs(manual = false) {
    if (state.logsRefreshing) return;
    state.logsRefreshing = true;
    try { await loadLogs(); state.lastSync = new Date(); render(); if (manual) toast(t("refresh.success")); }
    catch (error) { if (manual) toast(t("refresh.failed"), true); }
    finally { state.logsRefreshing = false;
      if (state.view === "logs" || (state.projectTab==="diagnostics"&&state.diagnosticTab==="logs")) scheduleLogPoll(); }
  }

  function switchView(view) {
    if (!allowedViews.has(view)) return;
    if (view === "logs" || view === "diagnostics") {state.projectTab="diagnostics";state.diagnosticTab=view==="logs"?"logs":"runs";view="projects";}
    if (state.projectTab==="diagnostics"&&state.diagnosticTab==="logs") scheduleLogPoll(); else stopLogPoll();
    state.view = view; window.localStorage.setItem(NAV_KEY, view); closeDetail(); render();enterWorkTab();
  }

  function openLog(entry) {
    $("#detail-title").textContent = LF.formatLogLine(entry, t);
    const ids = [[t("logs.request"), technicalValue(entry.requestId, t("logs.request"))], [t("common.project"), technicalValue(entry.projectId, t("common.project"))], [t("diagnostics.aiSession"), technicalValue(entry.aiSessionId, t("diagnostics.aiSession"))], [t("logs.featureArea"), technicalValue(entry.featureAreaId, t("logs.featureArea"))], [t("logs.task"), technicalValue(entry.taskId, t("logs.task"))], [t("logs.execution"), technicalValue(entry.executionId, t("logs.execution"))], [t("logs.run"), technicalValue(entry.runId, t("logs.run"))], [t("diagnostics.approvals"), technicalValue(entry.approvalId, t("diagnostics.approvals"))]].filter(([, value]) => value !== "—");
    $("#detail-body").innerHTML = C.keyValueDetails([[t("logs.canonicalEvent"), `<code>${C.esc(entry.event || "—")}</code>`], [t("logs.errorCode"), `<code>${C.esc(entry.errorCode || "—")}</code>`], [t("logs.message"), C.esc(entry.message || "—")], ...ids]) + (entry.data ? `<h3>${C.esc(t("logs.safeData"))}</h3><pre>${C.esc(JSON.stringify(entry.data, null, 2))}</pre>` : "");
    openDetail();
  }

  let workflowDrawerTarget = "";
  let workflowDrawerVersion = 0;
  function openTopicDetail(topicID, refreshed=false) {
    selectActivityContext("feature:"+topicID);workDrawerTaskID="";workDrawerExecutionID="";
    const workspace=state.projectWorkspace||{featureAreas:[],tasks:[],documents:[]};
    const topic=workspace.featureAreas.find(value=>value.id===topicID);if(!topic)return;
    const tasks=workspace.tasks.filter(value=>value.feature_area_id===topicID),documents=(workspace.documents||[]).filter(value=>value.feature_area_id===topicID);
    $("#detail-title").textContent=topic.title;
    const scroll=refreshed?$("#detail").scrollTop:0;
    const stale=state.projectDataErrors?.projectWorkspace||state.areaReadErrors?.[topicID];
    const displayed=stale?{...topic,can_retire:false,presentation:{...topic.presentation,canCommit:false,capabilities:[]}}:topic;
    $("#detail-body").innerHTML=(stale?`<p role="alert">${C.esc(t("workbench.stale"))}: ${C.esc(stale)}</p>`:"")+W.renderFeatureAreaDetail(workbenchContext(),displayed);
    $("#detail").scrollTop=scroll;
    openDetail(topicID);scheduleGitArea(topicID);
    if(!refreshed)loadAreaTasks(topicID);
    if(!refreshed&&topic.presentation?.commitStatus)refreshGitArea(state.selectedProjectID,topicID,true).catch(()=>toast("刷新失败，请重试",true));
    $$('[data-create-area-task]').forEach(form=>form.addEventListener('submit',event=>{event.preventDefault();const fields=new FormData(form);areaAction(topicID,'CREATE_TASK',{title:fields.get('title'),problemStatement:fields.get('goal'),repositoryId:fields.get('repository'),constraints:[]},form.querySelector('button'));}));
    $$('[data-git-area-action]').forEach(button=>button.addEventListener('click',()=>areaAction(topicID,button.dataset.gitAreaAction,{},button)));
    $$('[data-reset-area]').forEach(button=>button.addEventListener('click',()=>areaAction(topicID,'RESET_AGENT_CONTEXT',{},button)));
  }


  const gitAreaRequests=new Map(),gitAreaActions=new Set(),gitAreaErrors=new Map();
  let gitAreaTimer=null;
  function gitFailure(message){return /MERGE_CONFLICT|代码冲突/.test(message)?"无法提交到 dev：存在代码冲突":/RECOVERY|INCOMPLETE/.test(message)?"提交状态需要恢复":"提交未完成，请查看详情后重试";}
  function deliveryStateLabel(value){const key="delivery."+value,label=t(key);return label===key||label===`[${key}]`?value:label;}
  function deliveryMembers(status){
    const members=status?.members;if(!Array.isArray(members)||!members.length)return "";
    return `<div class="table-wrap"><table><thead><tr><th>${C.esc(t("delivery.repository"))}</th><th>${C.esc(t("workbench.status"))}</th><th>${C.esc(t("delivery.commit"))}</th></tr></thead><tbody>${members.map(m=>`<tr><td title="${C.esc(m.repositoryId)}">${C.esc((state.repositories||[]).find(r=>r.id===m.repositoryId)?.name||m.repositoryId||"—")}</td><td>${C.esc(deliveryStateLabel(m.state)||"—")}</td><td>${m.commit?`<code>${C.esc(m.commit.slice(0,12))}</code>`:"—"}${m.error?`<small> ${C.esc(m.error)}</small>`:""}</td></tr>`).join("")}</tbody></table></div>`;
  }
  function areaCommitPanel(topic){
    const p=topic.presentation;if(!p?.commitStatus)return "";const c=p.commit||{},v=p.commitView||{},esc=C.esc,key=state.selectedProjectID+":"+topic.id,busy=gitAreaActions.has(key)||v.activity==="RUNNING",error=gitAreaErrors.get(key);
    const available=action=>(p.capabilities||[]).some(v=>v.action===action&&v.available);
    const recovery=c.canRecoverMerge&&available("RECOVER_FEATURE_AREA_MERGE")?`<button data-git-area-action="RECOVER_FEATURE_AREA_MERGE" data-merge-request-id="${esc(c.requestId)}"${busy?" disabled":""}>继续原交付</button>`:"";
    const retry=c.mergeRetryOperation,reprepare=retry&&available("REPREPARE_FEATURE_AREA_MERGE")?`<button data-git-area-action="REPREPARE_FEATURE_AREA_MERGE" data-merge-request-id="${esc(retry.mergeRequestId)}" data-repository-id="${esc(retry.repositoryId)}" data-predecessor-operation-id="${esc(retry.operationId)}"${busy?" disabled":""}>重新授权合入</button>`:"";
    const release=retry?.canRelease&&available("REPREPARE_FEATURE_AREA_MERGE")?`<p>旧计划无法继续时，可解除剩余合入安排，再重新确认。已合入的代码会保留。</p><button data-git-area-action="REPREPARE_FEATURE_AREA_MERGE" data-release-plan="true" data-merge-request-id="${esc(retry.mergeRequestId)}" data-repository-id="${esc(retry.repositoryId)}" data-predecessor-operation-id="${esc(retry.operationId)}"${busy?" disabled":""}>解除旧计划</button>`:"";
    return `<section class="task-surface"><p>${esc(v.summary||"提交：未提交")}</p>${v.details?.delivery?.state?`<p>${esc(t("delivery.title"))}：${esc(deliveryStateLabel(v.details.delivery.state))}</p>${deliveryMembers(v.details.delivery)}`:""}${v.reason?`${C.resultPreview(v.reason,t)}`:""}${error?`<p role="alert">${esc(gitFailure(error))}</p>`:""}${busy?`<p role="status">正在提交…</p>`:""}${!state.projectDataErrors?.projectWorkspace&&!state.areaReadErrors?.[topic.id]?recovery+reprepare+release:""}${c.canRetry&&c.retryTaskId&&!state.projectDataErrors?.projectWorkspace&&!state.areaReadErrors?.[topic.id]?`<p>Task 已验收 · 代码提交失败</p><button data-git-area-action="RETRY_TASK_COMMIT"${busy?" disabled":""}>重试提交</button>`:""}<details><summary>${esc(t("workbench.technicalDetails"))}</summary><pre>${esc(JSON.stringify({capability:c,...v.details,error},null,2))}</pre></details></section>`;
  }
  async function refreshGitArea(project,areaID,related=false){
    const version=workflowDrawerVersion,current=()=>project===state.selectedProjectID&&version===workflowDrawerVersion&&workflowDrawerTarget===areaID;
    let p;try{p=await api(`/ui/api/featureAreas/${encodeURIComponent(areaID)}/presentation?${qs({project_id:project})}`);}catch(e){if(current()){state.areaReadErrors[areaID]=e.message;openTopicDetail(areaID,true);}throw e;}
    if(!current())return;
    const topic=state.projectWorkspace.featureAreas.find(v=>v.id===areaID);if(!topic)return;if(state.areaReadErrors)delete state.areaReadErrors[areaID];topic.presentation=p;
    if(p.commitView?.details?.lastError?.includes("MERGE_CONFLICT")&&!p.commitView.details.pending)gitAreaRequests.delete(project+":"+areaID);
    if(related&&p.commitView?.activity!=="RUNNING"&&![p.taskView,p.latestTaskView].some(v=>v?.commitActivity==="RUNNING")){const tasks=state.projectWorkspace.tasks.filter(v=>v.feature_area_id===areaID);const results=await Promise.all(tasks.map(v=>api(`/ui/api/tasks/${encodeURIComponent(v.id)}?${qs({project_id:project})}`)));if(!current())return;for(let i=0;i<tasks.length;i++)Object.assign(tasks[i],results[i]);}
    openTopicDetail(areaID,true);
  }
  function scheduleGitArea(areaID){
    window.clearTimeout(gitAreaTimer);const project=state.selectedProjectID,version=workflowDrawerVersion;
    const p=state.projectWorkspace?.featureAreas.find(v=>v.id===areaID)?.presentation;
    const running=p?.commitView?.activity==="RUNNING"||[p?.taskView,p?.latestTaskView].some(v=>v?.commitActivity==="RUNNING");
    if(!running||document.hidden)return;
    gitAreaTimer=window.setTimeout(async()=>{if(project!==state.selectedProjectID||version!==workflowDrawerVersion||workflowDrawerTarget!==areaID)return;try{await refreshGitArea(project,areaID,true);}catch(e){if(version===workflowDrawerVersion){toast("刷新失败，请重试",true);scheduleGitArea(areaID);}}},5000);
  }
  function areaTaskPanel(topic,contextOnly=false){
    const p=topic.presentation;if(!p)return "";
    const caps=p.capabilities||[],create=caps.some(c=>c.action==="CREATE_TASK"&&c.available),reset=caps.some(c=>c.action==="RESET_AGENT_CONTEXT"&&c.available);
    if(contextOnly)return reset?`<button type="button" data-reset-area="${C.esc(topic.id)}">${C.esc(t("workbench.resetContext"))}</button>`:"";
    if(!create)return "";
    return `<section class="task-surface">${create?`<details><summary>创建任务</summary><form data-create-area-task="${C.esc(topic.id)}"><label>${C.esc(t("workbench.taskTitle"))}<input name="title" required maxlength="2048"></label><label>${C.esc(t("workbench.taskGoal"))}<textarea name="goal" required maxlength="16000"></textarea></label><label>${C.esc(t("workbench.repository"))}<select name="repository">${(p.developmentContext?.repositories||[]).map(r=>`<option value="${C.esc(r.repository.id)}">${C.esc(r.repository.name)}</option>`).join("")}</select></label><button type="submit">创建任务</button></form></details>`:""}</section>`;
  }


  async function areaAction(areaID,action,task={},button){
    const project=state.selectedProjectID,version=workflowDrawerVersion,key=project+":"+areaID;
    if(button.disabled||gitAreaActions.has(key)||state.projectDataErrors?.projectWorkspace||state.areaReadErrors?.[areaID])return;
    const git=["COMMIT_FEATURE_AREA","RETRY_TASK_COMMIT","RECOVER_FEATURE_AREA_MERGE","REPREPARE_FEATURE_AREA_MERGE"].includes(action);
    gitAreaActions.add(key);button.disabled=true;const label=button.textContent;button.textContent=button.dataset.releasePlan==="true"?"正在解除…":git?"正在提交…":label;
    const mergeAction=action==="RECOVER_FEATURE_AREA_MERGE"||action==="REPREPARE_FEATURE_AREA_MERGE";
    const requestKey=mergeAction?key+":"+action+":"+button.dataset.mergeRequestId+":"+(button.dataset.predecessorOperationId||"")+":"+(button.dataset.releasePlan||"false"):key;
    const requestId=button.dataset.requestId||gitAreaRequests.get(requestKey)||window.crypto.randomUUID();gitAreaRequests.set(requestKey,requestId);
    const mergeIdentity=mergeAction?{mergeRequestId:button.dataset.mergeRequestId,repositoryId:button.dataset.repositoryId,predecessorOperationId:button.dataset.predecessorOperationId,...(button.dataset.releasePlan==="true"?{releasePlan:true}:{})}:{};
    const current=()=>project===state.selectedProjectID&&version===workflowDrawerVersion&&workflowDrawerTarget===areaID;
    try{await api(`/ui/api/featureAreas/${encodeURIComponent(areaID)}/action?${qs({project_id:project})}`,{method:"POST",body:JSON.stringify({action,task,requestId,...mergeIdentity})});gitAreaRequests.delete(requestKey);gitAreaErrors.delete(key);}
    catch(e){gitAreaErrors.set(key,e.message);if(current())toast(git?gitFailure(e.message):e.message,true);}
    finally{gitAreaActions.delete(key);if(current()){button.textContent=label;try{if(git)await refreshGitArea(project,areaID,true);else{await loadProjectData();if(current())openTopicDetail(areaID);}}catch(e){if(current()){button.disabled=false;toast("刷新失败，请重试",true);}}}}
  }
  function usageFactsBlock(usage) {
    return C.keyValueDetails([...C.buildUsageFacts(usage, t, i18n.locale).map(([label, value]) => [label, C.esc(value)]), [t("usage.duration"), C.esc(C.durationLabel(usage && usage.duration_ms) || "—")]]);
  }

  function renderReviewDetail(review, topicTitle) {
    const usage = review.usage;
    const focus = review.focus;
    const focusList = (values) => (values || []).length ? `<ul>${values.map((value) => `<li>${C.esc(value)}</li>`).join("")}</ul>` : C.esc(t("common.none"));
    const focusBlock = focus ? `<h3>${C.esc(t("review.frozenFocus"))}</h3>${C.keyValueDetails([
      [t("review.focusMode"), C.esc(["FOCUSED", "REPOSITORY"].includes(focus.mode) ? t(`review.focusMode.${focus.mode}`) : t("common.unknown"))],
      [t("common.summary"), C.esc(focus.summary || t("common.none"))],
      [t("review.primaryModules"), focusList(focus.primary_modules)],
      [t("review.supportingModules"), focusList(focus.supporting_modules)],
      [t("review.focusPaths"), focusList(focus.production_roots)]
    ])}<p class="muted">${C.esc(t("review.focusBoundary"))}</p>` : "";
    const selectionBlock = review.model || review.reasoning_effort ? `<h3>${C.esc(t("review.executionSelection"))}</h3>${C.keyValueDetails([
      [t("review.model"), C.esc(C.modelLabel(review.model, t) || "—")],
      [t("review.reasoning"), C.esc(C.reasoningLabel(review.reasoning_effort, t) || "—")]
    ])}` : "";
    const resultBlock = review.result ? `<h3>${C.esc(t("workItems.result"))}</h3>${C.resultPreview(review.result,t,{title:t("review.fullResult"),reviewID:review.id})}` : "";
    const blockerBlock = review.blocker_reason ? `<h3>${C.esc(t("workItems.blocker"))}</h3>${C.resultPreview(review.blocker_reason,t,{title:t("workItems.blocker")})}` : "";
    const provenance = review.repository_provenance;
    const provenanceBlock = provenance ? `<h3>${C.esc(t("workItems.provenance"))}</h3>${C.keyValueDetails([
      ["Review baseline", `<code>${C.esc(provenance.review_baseline || "—")}</code>`],
      ["Repository HEAD at admission", `<code>${C.esc(provenance.repository_head_at_admission || "—")}</code>`],
      ["Branch/ref", C.esc(provenance.branch_kind === "BRANCH" ? (provenance.branch_ref || "—") : (provenance.branch_kind || "UNKNOWN"))],
      ["Source working tree dirty", provenance.working_tree_dirty_at_admission == null ? "—" : C.esc(String(provenance.working_tree_dirty_at_admission))],
      ["Inspection baseline", C.esc(provenance.inspection_mode || "—")]
    ])}` : review.provenance ? `<h3>${C.esc(t("workItems.provenance"))}</h3>${C.keyValueDetails([[t("workItems.provenance"), `<code>${C.esc(review.provenance)}</code>`]])}` : "";
    const observed = review.observed_evidence;
    const evidenceBlock = observed ? `<h3>${C.esc(t("review.observedEvidence"))}</h3>${C.keyValueDetails([
      [t("review.authorization"), C.esc(observed.authorization_status === "GRANTED" ? t("review.authorizationGranted") : t("common.unknown"))],
      [t("review.inspectionObserved"), C.esc(observed.repository_inspection_observed === true ? t("review.inspectionYes") : observed.repository_inspection_observed === false ? t("review.inspectionNo") : t("common.unknown"))],
      [t("review.successfulOperations"), C.esc(observed.successful_host_tool_operations == null ? "—" : String(observed.successful_host_tool_operations))],
      [t("review.failedOperations"), C.esc(observed.failed_host_tool_operations == null ? "—" : String(observed.failed_host_tool_operations))],
      [t("review.forbiddenOperations"), C.esc(observed.forbidden_host_tool_operations == null ? "—" : String(observed.forbidden_host_tool_operations))],
      [t("review.observationBoundary"), C.esc(observed.observation_boundary === "BRIDGE_HOST_TOOLS_ONLY" ? t("review.hostToolsOnly") : observed.observation_boundary || t("common.unknown"))]
    ])}<p class="muted">${C.esc(t("review.evidenceBoundary"))}</p>` : "";
    const usageBlock = reviewTokenBlock(usage);
    const activity=review.activity;
    const executionBlock=activity?`<section class="review-execution"><h3>${C.esc(activity.executionMethod)}</h3><p>${C.esc(activity.summary)}</p>${activity.notice?`<p role="status" class="notice">${C.esc(activity.notice)}</p>`:""}${activity.lastActivityAt?`<p class="muted">最后可验证活动：${C.localizedTimestamp(activity.lastActivityAt,i18n.locale,"—")}</p>`:""}</section>`:"";
    return `${executionBlock}<div class="detail-summary">${C.statusBadge("product", review.state, t)}<p>${C.esc(topicTitle || t("common.unknown"))}</p></div>${review.run_id&&review.executorKind!=="UPPER_LLM"?`<button type="button" data-open-run-session="${C.esc(review.run_id)}">查看会话</button>`:""}${review.executorKind==="UPPER_LLM"?"":renderAgentRuntime(review.agent_runtime,["COMPLETE","BLOCKED","CANCELED"].includes(review.state))}${focusBlock}${resultBlock}${blockerBlock}${provenanceBlock}${evidenceBlock}${selectionBlock}${usageBlock}<p class="muted">${C.esc(t("common.created"))}: ${C.localizedTimestamp(review.created_at, i18n.locale, "—")}</p>${review.prompt_available ? `<button class="text-button" type="button" data-view-review-prompt="${C.esc(review.id)}">${C.esc(t("action.viewPrompt"))}</button>` : ""}`;
  }

  function renderReviewDrift(drift) {
    if (!drift) return "";
    if (!drift.available) return `<h3>${C.esc(t("review.repositoryState"))}</h3><p class="muted">${C.esc(t("review.driftUnavailable"))}${drift.unavailable_reason ? ` · ${C.esc(drift.unavailable_reason)}` : ""}</p>`;
    const short = (value) => `<code>${C.esc(String(value || "").slice(0, 7))}</code>`;
    const changes = (drift.focusPathsChanged || []).map((change) => `<li>${C.esc(change.change)} ${C.esc(change.path)}${change.oldPath ? ` (${C.esc(change.oldPath)} →)` : ""}</li>`).join("");
    const changed = drift.reviewCommit && drift.currentCommit && drift.reviewCommit !== drift.currentCommit ? `<p class="workflow-blocker">${C.esc(t("review.baselineChanged"))} ${short(drift.reviewCommit)} → ${short(drift.currentCommit)}</p>` : "";
    return `<h3>${C.esc(t("review.repositoryState"))}</h3>${changed}${C.keyValueDetails([
      [t("review.reviewedAt"), short(drift.reviewCommit)],
      [t("review.currentCommit"), short(drift.currentCommit)],
      [t("review.changedFocusPaths"), `${C.esc(drift.changedFocusPathCount == null ? "—" : String(drift.changedFocusPathCount))}${drift.truncated ? "+" : ""}`]
    ])}${changes ? `<ul class="review-drift-changes">${changes}</ul>` : ""}`;
  }

  function openReviewDetail(taskID,reviewID){
    if(detailRoute.get()?.rootTask!==taskID){openTaskDetail(taskID).then(()=>{detailRoute.tab("reviews");if(openedTask)paintTaskDetail(openedTask);});return;}
    saveTaskState();detailRoute.tab("reviews");if(openedTask)paintTaskDetail(openedTask);
    const section=[...$("#detail-body").querySelectorAll("[data-section-key]")].find(n=>n.dataset.sectionKey==="review:"+reviewID);if(section){section.open=true;section.scrollIntoView({block:"nearest"});}
  }

  async function openDocumentDetail(documentID) {
    workflowDrawerTarget = ""; workflowDrawerVersion++;
    try {
      const value = await api(`/ui/api/documents/${encodeURIComponent(documentID)}?${qs({project_id: state.selectedProjectID})}`);
      $("#detail-title").textContent = value.title;
      $("#detail-body").innerHTML = `${C.keyValueDetails([[t("documents.topic"), C.esc(value.feature_area_title || t("documents.projectLevel"))], [t("common.created"), C.localizedTimestamp(value.created_at, i18n.locale, t("common.unknown"))], [t("common.updated"), C.localizedTimestamp(value.updated_at, i18n.locale, t("common.unknown"))]])}<pre class="document-content">${C.esc(value.content || "")}</pre>`;
      openDetail();
    } catch (error) { toast(error.message, true); }
  }




  function relativeActivity(at) {const age=Math.max(0,Date.now()-Date.parse(at));if(!Number.isFinite(age))return "—";return age<60000?`${Math.floor(age/1000)} 秒前`:age<3600000?`${Math.floor(age/60000)} 分钟前`:`${Math.floor(age/3600000)} 小时前`;}
  const agentRequests = new Map(), nativeApprovalRuntime = new Map(), nativeApprovalSubmitted = new Set();
  let activityContext = "", activityExpanded = false;
  function selectActivityContext(key) {if(activityContext!==key){activityContext=key;activityExpanded=false;}}
  function toggleActivity(button) {
    activityExpanded=!activityExpanded;
    button.setAttribute("aria-expanded",String(activityExpanded));
    const body=document.getElementById(button.getAttribute("aria-controls"));
    if(body)body.hidden=!activityExpanded;
  }
  function reviewTokenBlock(usage) {
    const values=usage && usage.status==="AVAILABLE" && usage.usage;
    const rows=values ? [["输入",values.input_tokens],["缓存输入",values.cached_input_tokens],["输出",values.output_tokens],["推理输出",values.reasoning_output_tokens],["总计",values.total_tokens],["上下文窗口",usage.model_context_window]].filter(([,v])=>v!=null).map(([k,v])=>[k,C.esc(Number(v).toLocaleString("en-US"))]) : [];
    return `<section><h3>Token 使用</h3>${values ? C.keyValueDetails(rows) : `<p>${usage && usage.status==="PENDING" ? "统计中" : "不可用"}</p>`}</section>`;
  }
  function renderAgentRuntime(runtime, terminal=false) {
    if (!runtime || runtime.availability !== "AVAILABLE") return `<section><h3>Codex</h3><p class="muted">Codex 尚未报告执行计划</p></section>`;
    const plan = runtime.latestPlan;
    const planHTML = plan ? `<h4>Codex 执行计划 · ${C.esc(plan.completed)} / ${C.esc(plan.total)}</h4><ol>${plan.steps.map((step) => `<li>${C.esc(({completed:"✓", inProgress:"●", pending:"○"})[step.status] || "?")} ${C.esc(step.step)}</li>`).join("")}</ol>` : `<p class="muted">Codex 尚未报告执行计划</p>`;
    const item = runtime.currentItem;
    const labels={commandExecution:"Command",fileChange:"File change",mcpToolCall:"MCP Tool",dynamicToolCall:"Tool",webSearch:"Web search",agentMessage:"Agent message"};
    const metadata=item && item.metadata || {};
    const summary=metadata.command || metadata.tool || metadata.query || metadata.text || (metadata.changes || []).map(v=>v.path).join(", ") || "";
    const activityID="agent-current-activity"+(runtime.runId ? "-"+encodeURIComponent(runtime.runId) : "");
    const diagnosticMetadata={...metadata};if(item && item.type==="agentMessage")delete diagnosticMetadata.text;
    const activity = item ? `<h4><button type="button" data-agent-activity aria-expanded="${activityExpanded}" aria-controls="${C.esc(activityID)}">${terminal ? "最后活动" : "当前活动"} · ${C.esc(labels[item.type] || item.type)}</button></h4><div id="${C.esc(activityID)}" ${activityExpanded ? "" : "hidden"}>${item.type==="agentMessage"?C.resultPreview(summary,t):`<pre>${C.esc(summary)}</pre>`}</div><details><summary>活动诊断 · ${C.esc(item.status)}</summary><pre>${C.esc(JSON.stringify(diagnosticMetadata,null,2))}</pre></details>${item.outputTail ? `<details><summary>Command output tail</summary><pre>${C.esc(item.outputTail)}</pre></details>`:""}` : "";
    const requests=renderAgentRequests(runtime,terminal);
    const diagnostic = [runtime.lastWarning,runtime.lastError].filter(Boolean).map((v) => `<p>${C.esc(v.method)} · ${C.esc(v.summary)}</p>`).join("");
    const waiting={WAITING_FOR_PERMISSION:"等待权限确认",WAITING_FOR_COMMAND_APPROVAL:"等待命令批准",WAITING_FOR_FILE_APPROVAL:"等待文件修改批准",WAITING_FOR_USER_INPUT:"等待用户输入",WAITING_FOR_MCP_ELICITATION:"等待 MCP 回答"};
    const health=terminal ? "已结束" : runtime.runtimeObservation?.instanceStatus==="LOST"?"Codex 实时会话已丢失，原问题无法继续响应":runtime.agentHealth==="POSSIBLY_STALLED"?`可能已停滞 · Codex 已 ${Math.floor(Math.max(0,Date.now()-Date.parse(runtime.lastActivityAt))/60000)} 分钟没有新的活动`:runtime.runtimeObservation?.summary || waiting[runtime.waitingState] || runtime.waitingState;
    return `<section class="agent-runtime"><h3>Codex · ${C.esc(health)}</h3><p>最后活动 · ${C.esc(relativeActivity(runtime.lastActivityAt))}</p>${planHTML}${activity}${requests}${diagnostic}${runtime.turnDiff ? `<details><summary>${C.esc(runtime.diffSource)}</summary><pre>${C.esc(runtime.turnDiff)}</pre></details>` : ""}</section>`;
  }
  function renderAgentRequests(runtime,terminal=false){
 if(terminal&&!(runtime.requests||[]).some(r=>r.projection?.responseDisposition==="UNKNOWN"))return "";
    const requests = (runtime.requests || []).filter((request) => ["PENDING", "INTENDED", "SENT", "WAITING_FOR_USER", "USER_DECISION_RECORDED", "RESPONDING", "UNKNOWN"].includes(request.state)).map((request) => {
      const key = String(agentRequests.size); agentRequests.set(key, request);
      const payload = request.payload || {};
      if(request.kind==='RUNTIME_APPROVAL'){
        nativeApprovalRuntime.set(key,runtime);
        const helpers=window.DevBridgeSessionWorkbench,buttons=(request.availableResponses||[]).map((response,index)=>helpers?.nativeApprovalResolution?.(request,runtime,index)?`<button type="button" data-agent-native-approval="${key}" data-native-choice="${index}">${C.esc(helpers.nativeApprovalLabel(response))}</button>`:'').join('');
        return `<section data-agent-request="${key}"><h4>Codex · 等待命令或文件权限批准</h4><pre>${C.esc(JSON.stringify({command:payload.command,cwd:payload.cwd,reason:payload.reason,changes:payload.changes,permissions:payload.permissions},null,2))}</pre><details><summary>审批原文与选项</summary><pre>${C.esc(JSON.stringify({payload,availableResponses:request.availableResponses},null,2))}</pre>${buttons||'<p>只读 · 当前请求身份不可确认。</p>'}</section>`;
      }
      const network = payload.networkApprovalContext;
      const title = network ? `Network access request · ${network.host || ""} · ${network.protocol || ""}${network.port ? `:${network.port}` : ""}` : request.waitingState;
      let fields = "";
      if (request.method === "item/tool/requestUserInput") {
        fields = (payload.questions || []).map((q,index) => {
          const label = C.esc(q.question);
          const options = (q.options || []);
          if (options.length && !q.isOther) {
            const choices = options.map((o) => `<option value="${C.esc(o.id || o.label)}">${C.esc(o.label || o.id)} — ${C.esc(o.description)}</option>`).join("");
            return `<label>${label}<select data-agent-question="${index}" required><option value="" disabled selected>请选择</option>${choices}</select></label>`;
          }
          return `<label>${label}<input data-agent-question="${index}" type="${q.isSecret ? "password" : "text"}" required></label>`;
        }).join("");
      }
      if (request.method === "mcpServer/elicitation/request") {
        if (payload.mode === "url") {
          const url = String(payload.url || "");
          if (/^https?:\/\//i.test(url)) fields = `<a href="${C.esc(url)}" target="_blank" rel="noopener noreferrer">${C.esc(payload.message || url)}</a>`;
        } else if (payload.mode === "form") {
          fields = `<label>原始 JSON 内容<textarea data-agent-elicitation-content required maxlength="16384" autocomplete="off"></textarea></label>`;
        }
      }
      const labels={CONTINUE_WAITING:"继续等待",RESPOND:"提交回答",STOP_AND_REVISE:"停止并修订",CANCEL_OPERATION:"取消操作"};
      const responseSupported=request.method!=="mcpServer/elicitation/request"||payload.mode==="form";
      const projection=request.projection||{}, unknown=projection.responseDisposition==="UNKNOWN";
      const actions=request.kind==="CODEX_REQUEST"&&(projection.lifecycleState==="WAITING_FOR_USER"||unknown)&&Array.isArray(projection.availableActions)?projection.availableActions.filter(action=>action!=="RESPOND"||responseSupported):[];
      const actionable=actions.length>0;
      const requestSummary=projection.displayTitle||projection.title||"运行时请求状态待确认";
      const buttons=actionable?actions.map(action=>`<button type="button" ${unknown?"data-agent-recovery":"data-agent-answer"}="${key}" data-decision-action="${C.esc(action)}">${C.esc(labels[action]||action)}</button>`).join(""):`<p>${C.esc(requestSummary)} · 只读；不会自动答复、重试或替换原请求</p>`;
      return `<section data-agent-request="${key}"><h4>${C.esc(title)}</h4><p>${C.esc(payload.reason || payload.message || "")}</p><details><summary>原始请求、选项与 schema</summary><pre>${C.esc(JSON.stringify({payload,responseSchema:request.responseSchema},null,2))}</pre></details>${!unknown&&actionable?fields:""}<p>${C.esc(requestSummary)}</p><p>${C.esc(projection.displayMessage||projection.summary||"")}</p>${projection.userDecisionRecorded?`<pre>${C.esc(projection.userDecisionSummary||"")}</pre>`:""}<div>${buttons}</div></section>`;
    }).join("");
    return requests;
  }
  async function answerNativeApproval(button){
    const key=button.dataset.agentNativeApproval,request=agentRequests.get(key),runtime=nativeApprovalRuntime.get(key),helpers=window.DevBridgeSessionWorkbench;
    const body=helpers?.nativeApprovalResolution?.(request,runtime,Number(button.dataset.nativeChoice));
    const record=request?.runtimeRequestId+':'+request?.runtimeEpoch;
    if(!body||nativeApprovalSubmitted.has(record)||body.projectId!==state.selectedProjectID||!confirm(helpers.nativeApprovalLabel(body.response)+'？\n'+JSON.stringify({command:request.payload?.command,cwd:request.payload?.cwd,response:body.response},null,2)))return;
    nativeApprovalSubmitted.add(record);button.disabled=true;
    try{await api(`/ui/api/runs/${encodeURIComponent(body.runId)}/agent-requests/resolve`,{method:'POST',body:JSON.stringify(body)});toast('审批已提交；以原运行后续状态为准。');await loadProjectData();}catch(error){toast(error.message+'；请回读原请求，不自动重发。',true);}
  }
  async function answerAgentRequest(button) {
    const key=button.dataset.agentRecovery??button.dataset.agentAnswer, request=agentRequests.get(key);
    if (!request) return;
    const projectID=state.selectedProjectID, drawerVersion=workflowDrawerVersion;
    const isCurrent=()=>state.selectedProjectID===projectID && workflowDrawerVersion===drawerVersion && button.isConnected!==false && agentRequests.get(key)===request;

    if(request.projection?.responseDisposition==="UNKNOWN"){
      const action=button.dataset.decisionAction;if(!request.projection.availableActions.includes(action))return;
      button.disabled=true;
      try {if(action!=="CONTINUE_WAITING"){
        const recovery={};for(const name of ["operationKind","operationId","taskDefinitionRevisionId","reviewId","executionId","agentConversationId","runtimeEpoch","appServerInstanceId","appServerInstanceEpoch","projectId","featureAreaId","taskId","runId","threadId","turnId","connectionId","runtimeRequestId"])if(request[name]!==undefined)recovery[name]=request[name];
        recovery.action=action;recovery.commandRequestId=crypto.randomUUID();
        await api(`/ui/api/runs/${encodeURIComponent(request.runId)}/agent-requests/recovery`,{method:"POST",body:JSON.stringify(recovery)});
      }await loadProjectData();if(isCurrent())openTaskDetail(request.taskId);
      }catch(error){if(isCurrent())toast(error.message,true);}return;
    }
    const section=button.closest("[data-agent-request]");
    if(request.kind!=="CODEX_REQUEST"||request.projection?.lifecycleState!=="WAITING_FOR_USER"||!Array.isArray(request.projection.availableActions)||!request.projection.availableActions.includes(button.dataset.decisionAction))return;
    const payload=request.payload||{}, action=button.dataset.decisionAction;
    const decision={operationKind:request.operationKind,operationId:request.operationId,taskDefinitionRevisionId:request.taskDefinitionRevisionId,reviewId:request.reviewId,executionId:request.executionId,agentConversationId:request.agentConversationId,runtimeEpoch:request.runtimeEpoch,appServerInstanceId:request.appServerInstanceId,appServerInstanceEpoch:request.appServerInstanceEpoch,projectId:request.projectId,featureAreaId:request.featureAreaId,taskId:request.taskId,runId:request.runId,threadId:request.threadId,turnId:request.turnId,connectionId:request.connectionId,runtimeRequestId:request.runtimeRequestId,submissionRequestId:crypto.randomUUID(),expectedState:request.state,action,answers:[],sourceSurface:"PRODUCT_CONSOLE"};
    if(action==="RESPOND"&&request.method==="item/tool/requestUserInput")for(const field of section.querySelectorAll("[data-agent-question]")){if(!field.reportValidity())return;const q=(payload.questions||[])[Number(field.dataset.agentQuestion)]||{},option=(q.options||[]).find(v=>(v.id||v.label)===field.value);decision.answers.push({questionId:q.id,selectedOptionId:option?(option.id||option.label):"",rawText:option?"":field.value});}
    if(action==="RESPOND"&&request.method==="mcpServer/elicitation/request"){
      const content=section.querySelector("[data-agent-elicitation-content]");
      if(!content||!content.reportValidity())return;
      decision.rawText=content.value;
    }
    const hash=window.DevBridgeSessionWorkbench?.decisionPayloadSHA256;if(typeof hash!=="function"){toast("决策完整性摘要不可用；请刷新。",true);return;}decision.payloadSHA256=await hash(decision);
    button.disabled=true;let submitted=false;
    try {
      await api(`/ui/api/runs/${encodeURIComponent(request.runId)}/agent-user-decisions`,{method:"POST",body:JSON.stringify(decision)});
      submitted=true;
      if (!isCurrent()) return;
      await loadProjectData();
      if (!isCurrent()) return;
      openTaskDetail(request.taskId);
      toast("回答已受理；Codex 的确认状态以最新读取结果为准");
    } catch(error) {if (isCurrent()) {button.disabled=submitted;toast(submitted?t("workbench.submittedReadFailed")+" · "+error.message:error.message,true);if(submitted){mutationSubmitted=true;detailReadError=error.message;updateDetailReadState();}}}
  }

  let openedTask=null;
  const taskActionRequests=new Map();
  let taskActionNavigation=0;
  function directFileActivityBadge(presentation) {
    const file = presentation?.directFile;
    if (!file || (!file.workflowId && !file.taskId)) return "";
    const members = Array.isArray(file.repositoryTargets) ? file.repositoryTargets : [];
    const label = file.enabled ? "ACTIVE" : (file.state || "NOT_STARTED");
    const details = members.map(m => `${m.repositoryId}: ${m.write?.kind === "NONE" ? "只读" : "可写 " + (m.write?.kind || "")} · ${m.state || ""}`).join("；");
    return `<span class="status-badge direct-file-badge" data-direct-file-active="${file.enabled ? "true" : "false"}" role="status" title="${C.esc([file.workflowId, details, file.failureReason].filter(Boolean).join(" · "))}">DIRECT_FILE ${C.esc(label)}</span>`;
  }
  // Canonical serialization retains the complete target without depending on
  // object property order, labels, or the position of a capability in the list.
  function taskActionIdentity(value) {
    const normalize=v=>Array.isArray(v)?v.map(normalize):v&&typeof v==="object"?Object.fromEntries(Object.keys(v).sort().map(k=>[k,normalize(v[k])])):v;
    return JSON.stringify(normalize(value));
  }
  function taskPresentationActions(p) {
    return Array.isArray(p?.taskView?.actions)?p.taskView.actions:[];
  }
  function operatorActionCapability(cap) {
    const values=Array.isArray(openedTask?.operatorActions)?openedTask.operatorActions:[];
    const identity=taskActionIdentity(cap?.target);
    return values.find(value=>value?.action===cap?.action&&taskActionIdentity(value?.target)===identity)||null;
  }
  function taskActionKey(cap){return taskActionIdentity([cap.action,cap.target]);}
  function taskActionUnavailable(p,cap) {
    const v=p?.taskView,target=cap?.target;
    if(v?.actionsStatus!=="AVAILABLE")return v?.actionsReadError||t("workbench.actionsUnconfirmed");
    if(cap?.available!==true)return cap?.reason||t("workbench.actionUnavailable");
    if(!target||target.taskId!==v.id||target.featureAreaId!==(p.details?.featureAreaId||""))return t("workbench.actionTargetMismatch");
    const operator=operatorActionCapability(cap);
    if(!operator)return t("workbench.operatorFactsUnconfirmed");
    if(operator.domainAvailable!==cap.available)return t("workbench.operatorFactsMismatch");
    if(operator.channelAvailable!==true)return !operator.reason||operator.reason==="OPERATOR_ACTION_IDENTITY_UNAVAILABLE"?t("workbench.operatorChannelUnavailable"):operator.reason;
    // This is channel/shape validation only. Core remains the authorization owner.
    switch(cap.action){
      case "REQUEST_TASK_REVIEW": { const observed=target.expectedReviewRequest; return target.subjectId===v.id&&observed&&["phase","taskDefinitionRevisionId","currentReviewId","currentExecutionId","pendingDecisionId"].every(k=>Object.prototype.hasOwnProperty.call(observed,k)&&typeof observed[k]==="string")?"":t("workbench.actionTargetMismatch"); }
      case "START_TASK_REVIEW": return target.subjectId===v.id?"":t("workbench.reviewTargetUnsupported");
      case "START_EXECUTION": case "START_VERIFICATION": return target.taskDefinitionRevisionId?"":t("workbench.actionRevisionMissing");
      case "APPROVE": case "APPROVE_AND_START": case "REVISE": case "REJECT": case "REQUEST_RE_REVIEW": case "ABANDON_TASK": return target.decisionId&&target.subjectId?"":t("workbench.decisionTargetMissing");
      case "OPEN_PROJECT_REPOSITORIES": return target.subjectId===v.id?"":t("workbench.actionTargetMismatch");
      case "CONTINUE_TASK": case "COMPLETE_TASK": case "DEFER_TASK": case "ACCEPT": case "REQUEST_CHANGES": case "REQUEST_REVERIFICATION": case "RETRY_IMPLEMENTATION": return target.decisionId&&target.taskDefinitionRevisionId&&target.executionId&&target.subjectId===target.executionId?"":t("workbench.acceptanceTargetMissing");
      case "CANCEL_EXECUTION": return ["TASK","REVIEW","EXECUTION"].includes(target.operationKind)&&target.operationId?"":t("workbench.cancelTargetUnsupported");
      case "RECOVER": return ["REVIEW","EXECUTION"].includes(target.operationKind)&&target.operationId&&target.runId?"":t("workbench.recoveryTargetUnsupported");
      case "RETRY_TASK_COMMIT": return "";
      case "APPROVE_ONCE": case "APPROVE_FOR_SESSION": case "DECLINE": case "CANCEL": case "GRANT_REQUESTED_PERMISSIONS": return target.decisionId&&target.subjectId?"":t("workbench.decisionTargetMissing");
      default: return t("workbench.actionUnsupported");
    }
  }
  function taskRequestContext(record,p) {
    const route=detailRoute.get();
    return route?.view==="TASK"&&route.activeTab==="overview"&&route.project===record.project&&route.rootTask===record.taskId&&state.selectedProjectID===record.project&&p?.taskView?.id===record.taskId;
  }
  function taskRequestReplayable(record) {
    return ["CONTINUE_TASK","COMPLETE_TASK","DEFER_TASK","REQUEST_TASK_REVIEW","APPROVE","APPROVE_AND_START","REVISE","REJECT","REQUEST_RE_REVIEW","ABANDON_TASK","ACCEPT","REQUEST_CHANGES","REQUEST_REVERIFICATION","RETRY_IMPLEMENTATION","START_TASK_REVIEW","START_EXECUTION","START_VERIFICATION","CANCEL_EXECUTION","RECOVER"].includes(record.action);
  }
  const localApproveStartReceiptVisibilityCodes=new Set(["LOCAL_APPROVE_AND_START_REQUEST_CONFLICT","LOCAL_APPROVE_AND_START_OUTCOME_CONFLICT"]);
  const localApproveStartReceiptNotFoundCode="LOCAL_APPROVE_AND_START_RECEIPT_NOT_FOUND";
  function localApproveAndStartReceiptNotFound(result){return result?.failureCode===localApproveStartReceiptNotFoundCode;}
  function localApproveAndStartConfirmedSnapshot(record){
    return record.lastConfirmedEvidenceReadable===true&&record.lastConfirmedEvidence?.command?.state?record.lastConfirmedEvidence:null;
  }
  function localApproveAndStartEvidence(record){
    if(record.evidenceReadable===false){
      const failure=record.evidenceFailure||{};
      return `<section role="alert" class="local-approve-start-evidence"><p>本次读取：访问或身份核验失败${failure.code?` · ${C.esc(failure.code)}`:""}${failure.message?` · ${C.esc(failure.message)}`:""}</p><p>为保护当前访问边界，未显示缓存的父命令或关联证据。</p><button type="button" data-task-approve-start-refresh="${C.esc(record.requestId)}"${record.evidenceRefreshing?" disabled":""}>${C.esc(record.evidenceRefreshing?"正在刷新接管证据…":"刷新接管证据")}</button></section>`;
    }
    const result=record.evidence||{},command=result.command||{},handoff=result.start?.handoff||{},run=result.start?.run||{},esc=C.esc;
    if(localApproveAndStartReceiptNotFound(result)){
      const previous=localApproveAndStartConfirmedSnapshot(record),previousCommand=previous?.command;
      const observed=`本次未查到原父命令回执；不能据此判定原请求未提交。 · ${result.failureCode}${result.failureStage?` · ${result.failureStage}`:""}${result.failureSource?` · ${result.failureSource}`:""}`;
      const prior=previousCommand?`<p>此前已确认：原命令记录为 ${esc(`${previousCommand.state}${previousCommand.errorCode?` · ${previousCommand.errorCode}`:""}`)}（此前完整服务端快照；不代表本次读取成功）</p>`:"";
      return `<section role="status" class="local-approve-start-evidence"><p>本次读取：${esc(observed)}</p>${prior}<button type="button" data-task-approve-start-refresh="${esc(record.requestId)}"${record.evidenceRefreshing?" disabled":""}>${esc(record.evidenceRefreshing?"正在刷新接管证据…":"刷新接管证据")}</button></section>`;
    }
    const commandState=command.state||record.commandState||"UNKNOWN",parent=`${commandState}${command.errorCode?` · ${command.errorCode}`:""}`;
    const applicability=result.handoffApplicability||"APPLICABLE",inapplicable=applicability==="NOT_APPLICABLE";
    const hasRun=!!(run.id||handoff.runId||command.runId);
    const epochMatches=handoff.recordEpochMatchesRun===true;
    const explicitRejection=handoff.recordedState==="REJECTED";
    const evidenceUnconfirmed=result.failureCode==="HANDOFF_ACCEPTANCE_UNCONFIRMED"&&!explicitRejection;
    const structuredFailure=result.failureCode?`${result.failureCode}${result.failureStage?` · ${result.failureStage}`:""}${result.failureSource?` · ${result.failureSource}`:""}`:"";
    let supervisor="未取得关联接管证据";
    if(inapplicable)supervisor=result.handoffInapplicableReason==="DIRECT_FILE_NO_CODEX_RUN"?"不适用（Direct File 未创建 Codex Run）":result.handoffInapplicableReason==="REJECTED_BEFORE_RUN_ADMISSION"?"不适用（已在 Run 准入前拒绝）":"不适用";
    else if(hasRun&&handoff.recordExists){
      if(handoff.recordedState==="ACCEPTED"&&handoff.acceptedAt&&epochMatches)supervisor="已确认（原 Run 的持久接管回执）";
      else if(explicitRejection)supervisor=`明确拒绝${handoff.recordedCode?` · ${handoff.recordedCode}`:""}`;
      else supervisor=`未确认（持久记录：${handoff.recordedState||"UNKNOWN"}${handoff.recordedCode?` · ${handoff.recordedCode}`:""}${handoff.acceptedAt?"；epoch 关联不一致":""}）`;
    }else if(evidenceUnconfirmed)supervisor="未确认（尚未取得准确接管证据）";
    const runStatus=!hasRun?(inapplicable?"不适用":"未取得关联 Run"):run.status||handoff.runStatus||"未取得";
    const currentFailure=record.evidenceFailure?`本次读取失败（显示最后已知父回执） · ${record.evidenceFailure.code||"READ_FAILED"} · ${record.evidenceFailure.message||"接管证据读取失败"}`:evidenceUnconfirmed?"读取完成，接管证据尚未确认":structuredFailure?`父回执已读取，关联证据读取失败 · ${structuredFailure}`:"读取完成";
    return `<section role="status" class="local-approve-start-evidence"><p>原命令记录：${esc(parent)}（此前持久记录）</p><p>Supervisor 接管：${esc(supervisor)}</p><p>原 Run 结果：${esc(runStatus)}</p><p>本次读取：${esc(currentFailure)}</p><button type="button" data-task-approve-start-refresh="${esc(record.requestId)}"${record.evidenceRefreshing?" disabled":""}>${esc(record.evidenceRefreshing?"正在刷新接管证据…":"刷新接管证据")}</button></section>`;
  }
  function renderUnconfirmedTaskRequests(p) {
    return [...taskActionRequests.values()].filter(record=>taskRequestContext(record,p)&&(record.action==="APPROVE_AND_START"||record.state==="UNKNOWN"||record.state==="PARTIAL"||record.state==="REJECTED"&&(!record.reviewApprovalIdentity||record.reviewApprovalIdentity===reviewApprovalIdentity(p)))).map(record=>{
      if(record.action==="APPROVE_AND_START")return localApproveAndStartEvidence(record);
      if(record.state==="REJECTED")return `<section role="status"><p>${C.esc(`${record.action} · ${record.commandState||"REJECTED"} · ${record.rejection.code} · ${record.rejection.message}`)}</p></section>`;
      const summary=record.commandState?`${record.action} · ${record.commandState}${record.commandCode?` · ${record.commandCode}`:""}${record.commandFailureStage?` · ${record.commandFailureStage}`:""}${record.commandFailureSource?` · ${record.commandFailureSource}`:""}`:t("workbench.commandUnknown",{action:record.action});
      return `<section role="status"><p>${C.esc(summary)}</p>${taskRequestReplayable(record)?`<button type="button" data-task-replay="${C.esc(record.requestId)}">${C.esc(t("workbench.replayRequest"))}</button>`:`<p class="muted">${C.esc(t("workbench.replayUnavailable"))}</p>`}</section>`;
    }).join("");
  }
  function taskImplementationFacts(v){
    const mode=v.implementationMode,label=mode==="DIRECT_FILE"?"GPT 文件接口":mode==="CODING_AGENT"?"Coding Agent":"实施渠道尚未确认";
    let result=`<p>执行方式：${C.esc(label)}</p>`;
    if(mode!=="DIRECT_FILE")return result;
    const grant=v.directFile;
    if(!grant||grant.taskId!==v.id||grant.projectId!==state.selectedProjectID||grant.taskDefinitionRevisionId!==v.details?.taskDefinitionRevisionId||!grant.workflowId)return result;
    const ready=grant.state==="ACTIVE"&&grant.enabled===true;
    result+=`<section role="status"><strong>${C.esc(ready?"方案已批准 · 文件权限已就绪":grant.state==="PREPARING"?"方案已批准 · 文件工作区准备中":grant.state==="CLOSED"?"文件授权已关闭":"文件权限当前不可用")}</strong>${ready?"<p>可由 GPT 直接开始文件操作</p>":""}${grant.failureReason||grant.failureCode?`<p>${C.esc(grant.failureReason||grant.failureCode)}</p>`:""}<details><summary>文件授权详情</summary><pre>${C.esc(JSON.stringify(grant,null,2))}</pre></details></section>`;
    return result;
  }
  function taskExecutionStateFacts(v){
    const e=v.currentExecution,implementation=v.implementationRun,verification=v.verificationRun,acceptance=v.currentExecutionAcceptance,a=v.activity||{},unknown=t("workbench.unavailable");
    const receipt=acceptance?.receipt,receiptOption=receipt?.option||receipt?.Option,receiptResultKind=receipt?.resultKind||receipt?.ResultKind,receiptResultId=receipt?.resultId||receipt?.ResultID,receiptActor=receipt?.actorKind||receipt?.ActorKind;
    return `<section class="workbench-section"><h3>任务、Execution 与 Run</h3>${C.keyValueDetails([
      ["Task",`${C.esc(v.status||unknown)} · ${C.esc(v.phaseLabel||v.phase||unknown)}`],
      ["Current Execution",e?`<code>${C.esc(e.id)}</code> · ${C.esc(e.state||unknown)} · ${C.esc(e.outcome||unknown)}${e.failureCode?` · ${C.esc(e.failureCode)}`:""}`:C.esc(unknown)],
      ["Implementation Run",implementation?`<code>${C.esc(implementation.id)}</code> · ${C.esc(implementation.transportStatus||unknown)}${implementation.failureCode?` · ${C.esc(implementation.failureCode)}`:""}`:C.esc(unknown)],
      ["Verification Run",verification?`<code>${C.esc(verification.id)}</code> · ${C.esc(verification.transportStatus||unknown)}${verification.failureCode?` · ${C.esc(verification.failureCode)}`:""}`:C.esc(unknown)],
      ["Decision receipt",acceptance?`${C.esc(acceptance.status||unknown)}${receipt?` · ${C.esc(receiptOption||"RECORDED")}${receiptResultKind?` · ${C.esc(receiptResultKind)}${receiptResultId?` / ${C.esc(receiptResultId)}`:""}`:""}${receiptActor?` · ${C.esc(receiptActor)}`:""}`:""}`:C.esc(unknown)],
      ["Current activity source",`${C.esc(a.operationKind||unknown)} · ${C.esc(a.operationId||unknown)} · ${C.esc(a.runId||unknown)}${a.failureCode?` · ${C.esc(a.failureCode)}`:""}`]
    ])}</section>`;
  }
  const taskActionLabels={STOP_AND_REVISE:"停止并修订原任务",CONTINUE_TASK:"延续任务",COMPLETE_TASK:"完成并合入",DEFER_TASK:"暂存任务",CONFIRM_IMPLEMENTATION:"确认实施方案",REQUEST_TASK_REVIEW:"请求补充审查",REQUEST_RE_REVIEW:"按当前授权重新审查",OPEN_PROJECT_REPOSITORIES:"管理项目仓库",ABANDON_TASK:"放弃任务",START_TASK_REVIEW:"开始审查",START_EXECUTION:"启动已批准实施",START_VERIFICATION:"继续验证",APPROVE:"仅确认方案",APPROVE_AND_START:"确认并开始实施",REVISE:"重新审查",REJECT:"放弃任务",ACCEPT:"验收任务",REQUEST_CHANGES:"要求修改",REQUEST_REVERIFICATION:"重新验证",RETRY_IMPLEMENTATION:"创建实施后继",CANCEL_EXECUTION:"终止执行",CANCEL_REVIEW:"终止审查",CANCEL_TASK:"取消任务",RECOVER:"恢复",RETRY_TASK_COMMIT:"继续已确认交付"};
  function taskActionLabel(c){if(c.action==="RECOVER"&&c.target?.expectedUpperVersion&&c.target?.operationKind==="REVIEW")return "重新授权 GPT 审查";return c.action==="CANCEL_EXECUTION"?({TASK:t("workbench.cancelTask"),REVIEW:t("workbench.cancelReview"),EXECUTION:t("workbench.cancelExecution")}[c.target?.operationKind]||taskActionLabels[c.action]):taskActionLabels[c.action]||c.action;}
  function taskActionConsequence(c){
    const acceptance=String(c.target?.decisionId||"").startsWith("TASK_ACCEPTANCE:");
    if(c.action==="RECOVER"&&c.target?.expectedUpperVersion&&c.target?.operationKind==="REVIEW")return "为原 GPT 审查重新授权，保留原任务与审查记录；完成后在原 GPT 对话继续读取文件并提交报告。";
    return ({
      STOP_AND_REVISE:"原执行已在派发前取消。保留原审批及部分步骤，转入同任务方案修订；不会启动运行。",
      CONTINUE_TASK:"保留原 Task 和全部历史，解除暂存并准备下一轮工作；不会立即启动 Codex。",
      COMPLETE_TASK:"确认当前结果验收、提交和逐仓合入；全部合入成功后才完成任务。",
      DEFER_TASK:"保存进度、验证结果和剩余待办，停止后续自动推进，不执行 Git stash。",
      CONFIRM_IMPLEMENTATION:"已跳过审查；请回到 AI 对话准备并确认完整实施与验证方案。",
      REQUEST_TASK_REVIEW:"仅记录准确补审请求并阻止旧候选继续推进；不会自动生成审查内容或启动 Review。",
      REQUEST_RE_REVIEW:"保留无效结果历史，并按当前正式授权范围在同一 Task 下创建和自动启动后继 Review。",
      OPEN_PROJECT_REPOSITORIES:"打开当前 Project 的 Repository 管理，不修改 Review、Task 或授权。",
      ABANDON_TASK:"放弃当前 Task；无效 raw result、Run 和 materialization failure 保持不可变。",
      START_TASK_REVIEW:"为当前准确 Task 启动新的审查工作；配置在提交时冻结。",
      APPROVE:"批准当前准确 Review 方案，但不创建或派发实施 Run。",
      APPROVE_AND_START:"在同一 Core 编排中批准当前方案并启动新的实施 Execution；不得拆成两个客户端命令。",
      REVISE:"提交不可变反馈，在同一 Task 下创建并授权下一轮 Review；Core worker 自动准备和启动。",
      REJECT:acceptance?"拒绝当前准确 Execution 结果并结束该 Task；历史 Run 和回执保留。":"拒绝当前准确 Review 方案；不会取消其他 Run。",
      START_EXECUTION:(c.requiredInputs||[]).includes("nextRunRuntimeConfig")?"使用本次选择的模型和后端启动已批准的任务定义。":"启动当前已批准的执行，沿用其已确认的模型和后端。",
      START_VERIFICATION:"只启动当前准确 Execution 的 Verification，并继承其冻结配置。",
      ACCEPT:"验收当前准确 Execution 结果并结束 Task；基础设施失败不具备此资格。",
      REQUEST_CHANGES:"使用下面完整的新定义创建技术后继；原定义、Execution 和失败历史保留。",
      REQUEST_REVERIFICATION:"创建 verification-only 后继并继承原 Execution 的冻结配置。",
      RETRY_IMPLEMENTATION:"创建新的实施 Execution 身份；不重置旧 Run。",
      CANCEL_EXECUTION:"请求取消准确的当前操作；受理回执不是终态，已有历史保留。",
      RECOVER:"沿原 Core recovery 合同恢复准确操作；不会重建 Task。",
      RETRY_TASK_COMMIT:"继续已确认交付的原提交及合入步骤，沿用冻结目标，不重新执行实施或验证。"
    })[c.action]||"Core 将重新核对准确目标、当前资格和请求身份。";
  }
  function runtimeChoices(options){
    if(Array.isArray(options?.runtimeOptions)&&options.runtimeOptions.length)return options.runtimeOptions.filter(value=>value&&value.enabled!==false);
    return (options?.modelConfigs||[]).filter(value=>value&&value.enabled!==false).map(value=>({modelId:value.modelId,supportedReasoningEfforts:value.supportedReasoningEfforts||[],codexFastModeSupported:false}));
  }
  function backendChoices(options){
    const values=options?.backendCandidates||[];
    return Array.isArray(values)?values.filter(v=>v&&v.configId&&v.configFingerprint):[];
  }
  function runtimeSelectionFields(options,includeBackend=false){
    const values=runtimeChoices(options);
    const readable=includeBackend&&!options?.backendInventoryReadError&&options?.backendInventoryComplete===true,backends=readable?backendChoices(options):[],backendMessage=options?.backendContinuity?.reason|| (options?.backendInventoryReadError?"后端清单读取失败":options?.backendInventoryComplete!==true?"后端清单不完整，请刷新":"未配置可用后端");
    const locked=options?.lockedBackend,backend=locked?`<p class="muted">Codex 后端沿用当前领域会话或原 Execution：${C.esc(locked.configName||locked.configId)} (${C.esc(locked.configId)})，不可更改。</p><select name="backendConfigId" data-runtime-backend hidden><option value="${C.esc(locked.configId)}" data-backend-config-id="${C.esc(locked.configId)}" data-backend-fingerprint="${C.esc(locked.configFingerprint)}" selected>${C.esc(locked.configName||locked.configId)}</option></select>`:includeBackend?`<label>Codex 后端<select name="backendConfigId" data-runtime-backend required><option value="" ${backends.length===1?"":"selected"} disabled>${backends.length?"选择后端":C.esc(backendMessage)}</option>${backends.map((value,index)=>`<option value="${C.esc(value.configId)}" data-backend-config-id="${C.esc(value.configId)}" data-backend-fingerprint="${C.esc(value.configFingerprint)}" ${backends.length===1&&index===0?"selected":""}>${C.esc(value.configName||value.configId)} (${C.esc(value.configId)})</option>`).join("")}</select></label>`:"";
    const source=options?.conversationModelDefault,defaultModel=source?.state==="KNOWN"?source.initialModel:"";
    const defaultNotice=source?.state==="KNOWN"?`<p class="muted">默认模型来自当前领域会话首次 Thread：${C.esc(defaultModel)}。可为本次工作改选，下一项新工作仍默认此值。</p>`:source?.state==="HISTORICAL_UNKNOWN"?`<p class="muted">当前会话已有 Thread，但初始模型未记录；请明确选择模型。</p>`:source?.state==="READ_UNAVAILABLE"?`<p class="muted">会话初始模型暂不可读取；仍可明确选择当前合法模型。</p>`:"";
    return `${backend}${defaultNotice}<label>Model<select name="model" data-runtime-model required><option value="" ${defaultModel?"":"selected"}>选择模型</option>${values.map(value=>`<option value="${C.esc(value.modelId)}" ${value.modelId===defaultModel?"selected":""}>${C.esc(value.modelId)}</option>`).join("")}${defaultModel&&!values.some(value=>value.modelId===defaultModel)?`<option value="${C.esc(defaultModel)}" selected disabled>${C.esc(defaultModel)} · 当前不可用</option>`:""}</select></label><label>Reasoning<select name="reasoningEffort" data-runtime-effort required><option value="" selected>先选择模型</option></select></label><label><input type="checkbox" name="codexFastMode" data-runtime-fast disabled> Fast Mode（仅在所选模型支持时可用）</label><p role="alert" data-runtime-selection-conflict hidden></p>`;
  }
  function backendOptionIdentity(option){
    const configId=option?.dataset.backendConfigId,configFingerprint=option?.dataset.backendFingerprint;
    return configId&&configFingerprint?{configId,configFingerprint}:null;
  }
  function backendIdentityIsUnavailable(saved){return saved?.backendIdentityUnavailable===true;}
  function selectedBackendIdentity(select){
    const option=select?.selectedOptions[0],identity=backendOptionIdentity(option);
    return identity&&select.dataset.backendIdentityUnavailable!=="true"&&option.dataset.draftUnavailable!=="true"?identity:null;
  }
  function runtimeSelectionFromForm(form){
    const config={model:form.elements.model.value,reasoningEffort:form.elements.reasoningEffort.value,codexFastMode:form.elements.codexFastMode.checked};
    const select=form.elements.backendConfigId;
    const backendSelection=selectedBackendIdentity(select);
    return {nextRunRuntimeConfig:config,...(backendSelection?{backendSelection}:{})};
  }
  function executionInstructionFields(){return `<label>完整 Implementation instruction（可选；填写时不可为空白）<textarea name="implementationInstruction" maxlength="65536"></textarea></label><label>完整 Verification instruction（Verification mode 为 RUN 时与上一项一起必填）<textarea name="verificationInstruction" maxlength="65536"></textarea></label>`;}
  function executionInstructionsFromForm(form,task){
    const implementation=form.elements.implementationInstruction.value.trim(),verification=form.elements.verificationInstruction.value.trim(),mode=task?.presentation?.taskView?.currentVerificationSpec?.mode;
    if(!implementation&&!verification)return {};
    if(!implementation||(mode!=="NONE"&&!verification)){toast("自定义执行说明不完整：需要完整 Implementation instruction，Verification mode 为 RUN 时还需要完整 Verification instruction。",true);return null;}
    if(new TextEncoder().encode(implementation).byteLength>65536||new TextEncoder().encode(verification).byteLength>65536){toast("执行说明超过 Core 的 65,536 字节上限。",true);return null;}
    return {executionInstructions:{implementation,...(mode==="NONE"?{}:{verification})}};
  }
  function renderTaskActionControl(p,v,c){
    const esc=C.esc,options=p.taskOptions||{},unavailable=taskActionUnavailable(p,c),key=esc(taskActionKey(c)),label=taskActionLabel(c),consequence=taskActionConsequence(c),required=new Set(c.requiredInputs||[]),form=(kind,attributes="")=>`data-task-action-key="${key}" data-task-form-kind="${kind}" ${attributes}`;
    if(c.action==="CONFIRM_IMPLEMENTATION")return `<section class="task-action-form"><strong>待实施方案确认</strong><p>已跳过审查。请回到创建任务的 AI 对话，准备完整的实施范围、指令和验证标准，并打开实施确认卡片。</p></section>`;
    if(unavailable)return `<span class="task-action-control"><button type="button" disabled title="${esc(unavailable)}">${esc(label)}</button><small>${esc(unavailable==="REVIEW_IN_PROGRESS"?"当前审查尚未结束，请先继续或终止该审查。":unavailable)}</small></span>`;
    if(c.action==="OPEN_PROJECT_REPOSITORIES")return `<span class="task-action-control"><button type="button" data-open-project-repositories>${esc(label)}</button><small>${esc(consequence)}</small></span>`;
    if(c.action==="REQUEST_TASK_REVIEW")return `<form ${form("TASK_REVIEW_REQUEST","data-task-review-request")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p><label>补审目的与新增问题<textarea name="reviewPurpose" required maxlength="65536"></textarea></label><button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    if(c.action==="REVISE"||c.action==="REQUEST_RE_REVIEW")return `<form ${form("REQUEST_RE_REVIEW","data-re-review")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p><label>重新审查反馈<textarea name="reviewFeedback" required maxlength="8192"></textarea></label><button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    if(c.action==="DEFER_TASK")return `<details class="task-action-form"><summary>${esc(label)}</summary><form ${form("DEFER_TASK","data-defer-task")}><p>${esc(consequence)}</p><label>当前进度与剩余待办<textarea name="note" required maxlength="8192"></textarea></label><button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">确认暂存任务</button></form></details>`;
    if(c.action==="REQUEST_CHANGES")return `<details data-action-popover data-section-key="action:changes" class="task-action-form"><summary>${esc(label)}</summary><form ${form("REQUEST_CHANGES","data-request-changes data-runtime-form")}><p>${esc(consequence)}</p><label>新的 Implementation specification<textarea name="nextWorkSpec" required placeholder="输入完整 JSON；不会从旧定义自动复制"></textarea></label><label>新的 Verification specification<textarea name="nextVerificationSpec" required placeholder="输入完整 JSON；不会从旧定义自动复制"></textarea></label>${runtimeSelectionFields(options)}<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form></details>`;
    if(c.action==="START_TASK_REVIEW")return `<form ${form("START_TASK_REVIEW","data-review-start data-runtime-form")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p><label>审查执行方<select name="executorKind"><option value="CODING_AGENT">Codex 审查（后台执行）</option>${options?.piAgentEnabled?'<option value="PI_AGENT">Pi Agent 审查（本地 pi 进程执行）</option>':""}<option value="UPPER_LLM" disabled>GPT 审查（请在原 GPT 对话发起）</option></select></label><label>Engineering goal<textarea name="reviewEngineeringGoal" required maxlength="65536"></textarea></label><label>Review instruction<textarea name="reviewInstruction" required maxlength="65536"></textarea></label><label>Focus mode<select name="reviewFocusKind" required><option value="REPOSITORY">Repository-wide</option><option value="FOCUSED">Focused modules and paths</option></select></label><label>Focus summary<textarea name="reviewFocusSummary" maxlength="4096"></textarea></label><label>Focus paths（JSON 数组；Repository-wide 必须为 []）<textarea name="reviewFocusPaths" required>[]</textarea></label><label>Selected modules（JSON 数组；Focused 需含 PRIMARY）<textarea name="reviewFocusModules" required>[]</textarea></label>${runtimeSelectionFields(options,true)}${(v.allowedReviewDepths||[]).map(depth=>`<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}" name="reviewDepth" value="${esc(depth)}">${esc(({FAST:"快速审查",DEEP:"深度审查"})[depth]||depth)}</button>`).join("")}</form>`;
    if(c.action==="APPROVE"&&v.implementationMode==="CODING_AGENT")return `<form ${form("REVIEW_APPROVE","data-review-approve data-runtime-form")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p><p class="muted">本次只冻结后续实施配置，不启动 Run。</p>${runtimeSelectionFields(options,true)}<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    if(c.action==="RETRY_IMPLEMENTATION"&&required.has("nextRunRuntimeConfig"))return `<form ${form("IMPLEMENTATION_RETRY","data-implementation-retry data-runtime-form")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p>${runtimeSelectionFields(options)}<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    if(c.action==="APPROVE_AND_START")return `<form ${form("APPROVE_AND_START","data-approve-and-start data-runtime-form")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p>${runtimeSelectionFields(options,true)}${executionInstructionFields()}<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    if(c.action==="START_EXECUTION"&&required.has("nextRunRuntimeConfig"))return `<form ${form("START_EXECUTION","data-approved-task-start data-runtime-form")} class="task-action-form"><strong>${esc(label)}</strong><p>${esc(consequence)}</p>${runtimeSelectionFields(options,true)}<button type="submit" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button></form>`;
    return `<span class="task-action-control"><button type="button" data-task-action="${esc(c.action)}" data-task-action-key="${key}">${esc(label)}</button><small>${esc(consequence)}</small></span>`;
  }
  function renderTaskPresentation(p) {
    const v=p.taskView;if(!v)return `<p role="alert">${C.esc(t("workbench.taskViewUnavailable"))}</p>`;
    const esc=C.esc,a=v.activity||{},actions=taskPresentationActions(p),options=p.taskOptions||{},decision=p.pendingDecision;
    const decisionSummary=decision?`<p role="status">${v.actionsStatus==="AVAILABLE"?t("workbench.currentDecision"):t("workbench.decisionUnconfirmed")}</p>${C.resultPreview(decision.summary,t)}`:"";
    const nextStep=C.taskNextStep(v);
    const choices=v.verificationDecisions;
    const primaryChoices=choices?actions.filter(c=>['CONTINUE_TASK','COMPLETE_TASK','DEFER_TASK'].includes(c.action)):[];
    const continued=choices?.continuationConfirmed&&!choices.hold&&!choices.completion;
    const continuationActions=continued?actions.filter(c=>["REQUEST_TASK_REVIEW","REQUEST_REVERIFICATION","REQUEST_CHANGES","RETRY_IMPLEMENTATION"].includes(c.action)&&!taskActionUnavailable(p,c)):[];
    const continuationHint=continued?`<section class="task-continuation-next" aria-label="延续任务下一步"><p role="status">已确认延续原任务，无需重复确认。</p><p>${continuationActions.some(c=>c.action==="REQUEST_TASK_REVIEW")?"如需修订实施方案，请填写下方补审目的并提交；提交后在本页配置并启动补充审查，沿用当前任务。":continuationActions.length?"请选择下方可用的后续操作，沿用当前任务及历史。":"当前没有可用的后续操作，请查看下方具体限制并刷新资格。"}</p>${continuationActions.map(c=>renderTaskActionControl(p,v,c)).join("")}</section>`:"";
    const choiceButtons=primaryChoices.map(c=>continued&&c.action==="CONTINUE_TASK"?`<span class="task-action-control" role="status"><strong>已确认延续</strong><small>在下方选择下一步</small></span>`:renderTaskActionControl(p,v,c)).join("");
    const rawActionButtons=actions.filter(c=>(!v.terminal||c.available===true)&&!primaryChoices.includes(c)&&!continuationActions.includes(c)&&(!choices||c.action!=='ACCEPT')).map(c=>renderTaskActionControl(p,v,c)).join("");
    const otherActions=choices&&rawActionButtons?`<details class="task-other-actions"><summary>其他操作与运行记录</summary>${rawActionButtons}</details>`:rawActionButtons;
    const actionButtons=choices?`<div class="task-verification-choices" aria-label="验证阶段任务操作">${choiceButtons}</div>${continuationHint}${otherActions}`:nextStep&&rawActionButtons&&!decision?`<details class="task-other-actions"><summary>其他操作</summary>${rawActionButtons}</details>`:rawActionButtons;
    const rail=(v.progress||[]).map(x=>`<li class="task-phase" data-status="${esc(x.status)}"><span>${esc(({APPROVED:"方案批准",DONE:"完成",REVIEW:"审查",IMPLEMENTATION:"实施",VERIFICATION:"验证"})[x.phase]||x.phase)}</span><small>${esc(C.progressLabel(x.status,t,x.phase))}</small></li>`).join("");
    const runs=v.details?.runs||[],page=taskRunsPage||{items:runs,total:v.details?.runsTotal??runs.length,offset:0,limit:20,truncated:v.details?.runsTruncated===true};
    const listOptions={C,t,locale:i18n.locale,total:page.total,offset:page.offset,limit:page.limit,truncated:page.truncated===true,filter:detailRoute.get()?.runFilter||""};
    const source=v.originalRequirement;
    const originalRequirement=`<details class="workbench-section" data-section-key="task:original-requirement"><summary>原始需求</summary>${source?.status==="SOURCE_AVAILABLE"?`<p>${esc(source.goal)}</p><ul>${(source.constraints||[]).map(constraint=>`<li>${esc(constraint)}</li>`).join("")}</ul>`:`<p class="muted">原始需求不可追溯；不以当前方案替代历史。</p>`}</details>`;
    const activeTab=detailRoute.get()?.activeTab||"overview",unconfirmed=renderUnconfirmedTaskRequests(p);
    let content="";
    if(activeTab==="overview")content=`${nextStep?`<section class="workbench-section task-next-step" aria-label="当前下一步"><h3>${esc(nextStep.label)}</h3><p>${esc(nextStep.summary)}</p><button type="button" data-task-run="${esc(nextStep.runId)}" data-run-open-tab="evidence">查看${({IMPLEMENTATION:"实施",VERIFICATION:"验证",SCOPE_REVIEW:"审查"})[nextStep.purpose]||""}${nextStep.code==="ANALYZE_RUN_OUTPUT"?"输出与分析":"运行记录"}</button><p class="muted">${continued&&continuationActions.length?"延续已确认，可在下方「延续任务下一步」直接办理后续操作；旧运行结果保持原样。":nextStep.code==="ANALYZE_RUN_OUTPUT"?"请在原 ChatGPT 对话点击对应分析入口。":"请在对话中要求核对这次运行结果，再决定后续处理。"}</p>${nextStep.background?`<details><summary>运行背景</summary><p>${esc(nextStep.background)}</p></details>`:""}</section>`:""}${v.actionNode?.blocking?`<section class="workbench-section" role="status" aria-label="任务当前操作"><h3>待处理当前操作</h3><p>${esc(v.actionNode.reason)}</p></section>`:""}${TW.taskOutcome(v,{C,t})}${TW.taskResults(v,{C,t})}<section class="workbench-section">${C.keyValueDetails([[t("workbench.taskGoal"),esc(v.goal||t("workbench.unavailable"))],[t("workbench.nextAction"),esc(nextStep?.summary||((p.nextAction?.code||v.currentAction?.code)==="WORKFLOW_COMPLETE"?t("workbench.workflowComplete"):p.nextAction?.message||p.nextAction?.code||t("workbench.unavailable")))],...(!v.terminal?[[t("workbench.currentActivity"),esc(nextStep?.summary||a.summary||a.status||t("workbench.noActivity"))]]:[])])}</section>${originalRequirement}<details class="workbench-section" data-section-key="task:execution-diagnostics"><summary>${esc(t("workbench.technicalDetails"))}</summary>${taskExecutionStateFacts(v)}${taskImplementationFacts(v)}</details>${!v.terminal||actionButtons||decision||unconfirmed?`<section class="workbench-section task-operation-area"><h3>审批与任务操作</h3>${decisionSummary}${v.actionsStatus!=="AVAILABLE"?`<p role="alert">${esc(v.actionsReadError||t("workbench.actionsUnconfirmed"))}</p>`:""}<div data-task-command-status>${unconfirmed}</div><div class="detail-actions">${actionButtons||(!decision&&v.actionsStatus==="AVAILABLE"?`<p class="muted">当前无需审批或操作</p>`:"")}</div>${a.summary&&!nextStep?`<p role="status">${esc(a.summary)}</p>`:""}${a.runtime?renderAgentRequests(a.runtime,v.terminal||v.readOnly):""}</section>`:""}`;
    if(activeTab==="reviews")content=TW.reviewHistory(v,{C,t});
    if(activeTab==="executions")content=TW.executionHistory(v,{C,t});
    if(activeTab==="runs")content=`<section class="workbench-section" data-task-run-list>${TW.runList(page.items,listOptions)}</section>`;
    return `<article class="task-dashboard task-workbench" data-task-id="${esc(v.id)}"><header class="task-heading task-fixed-header"><span class="task-eyebrow">${esc(p.featureArea?.name||"Task")} / ${esc(v.title)}</span>${directFileActivityBadge(p)}${v.currentReview?`<p class="task-review-method" data-review-executor="${esc(v.currentReview.executorKind)}">审查方式：${esc(({UPPER_LLM:"GPT 审查",CODING_AGENT:"Codex 审查",PI_AGENT:"Pi Agent 审查"})[v.currentReview.executorKind]||"执行方未记录")}</p>`:""}<div class="task-primary-facts"><strong title="${esc(v.phase)}">${esc(nextStep?.label||C.taskPhaseLabel(v,t))}</strong>${!nextStep&&C.taskPhaseLabel(v,t)!==C.statusText("task",v.status,t)?C.taskStatusBadge(v,t):""}</div><ol class="task-progress" aria-label="Task progress">${rail}</ol><button type="button" class="text-button" data-task-refresh>${esc(t("workbench.refresh"))}</button></header>${TW.tabs("TASK",activeTab,esc,t)}<section role="tabpanel" id="task-tab-${activeTab}" aria-labelledby="task-tab-button-${activeTab}" data-task-panel="${activeTab}">${content}</section></article>`;
  }
  function taskFormDraftAuthority(form){return form?.dataset?.taskActionKey&&form?.dataset?.taskFormKind?taskActionIdentity([form.dataset.taskActionKey,form.dataset.taskFormKind]):"";}
  function taskFormFields(form){return [...form.querySelectorAll('input[name]:not([type="hidden"]),textarea[name],select[name]:not([hidden])')];}
  function snapshotTaskForm(form){
    const fields={};
    for(const input of taskFormFields(form)){
      if(Object.hasOwn(fields,input.name))continue;
      const group=taskFormFields(form).filter(value=>value.name===input.name);
      if(input.type==="radio")fields[input.name]={present:true,kind:"radio",value:group.find(value=>value.checked)?.value??null};
      else if(input.type==="checkbox"&&group.length>1)fields[input.name]={present:true,kind:"checkbox-group",value:group.filter(value=>value.checked).map(value=>value.value)};
      else if(input.type==="checkbox")fields[input.name]={present:true,kind:"checkbox",checked:input.checked};
      else if(input.multiple)fields[input.name]={present:true,kind:"multiple",value:Array.from(input.selectedOptions).map(value=>value.value)};
      else if(input.matches("[data-runtime-backend]")){
        const option=input.selectedOptions[0],identity=backendOptionIdentity(option);
        fields[input.name]=input.dataset.backendIdentityUnavailable==="true"?{present:true,kind:"value",value:"",backendFingerprint:"",backendIdentityUnavailable:true,backendIdentityVersion:1}:identity?{present:true,kind:"value",value:identity.configId,backendFingerprint:identity.configFingerprint,backendIdentityVersion:1}:{present:true,kind:"value",value:"",backendFingerprint:"",backendIdentityUnavailable:option?.value!==""||!option,backendIdentityVersion:1};
      }else fields[input.name]={present:true,kind:"value",value:input.value};
    }
    return fields;
  }
  function restoreTaskField(form,input,saved){
    if(!saved?.present)return;
    const group=taskFormFields(form).filter(value=>value.name===input.name);
    if(saved.kind==="radio"){for(const value of group)value.checked=saved.value!==null&&value.value===saved.value;return;}
    if(saved.kind==="checkbox-group"){const selected=new Set(saved.value||[]);for(const value of group)value.checked=selected.has(value.value);return;}
    if(saved.kind==="checkbox"){input.checked=saved.checked===true;return;}
    if(saved.kind==="multiple"){const selected=new Set(saved.value||[]);for(const option of input.options)option.selected=selected.has(option.value);return;}
    input.value=saved.value??"";
  }
  function selectDraftValue(select,saved,label){
    if(!saved?.present)return false;
    if(select.matches("[data-runtime-backend]"))return selectBackendDraftValue(select,saved,label);
    const value=saved.value??"";
    const exact=[...select.options].find(option=>option.value===value);
    if(exact){select.value=exact.value;exact.selected=true;return false;}
    if(value===""){select.value="";return false;}
    const option=document.createElement("option");
    option.value=value;option.textContent=`${value} · ${label}`;option.disabled=true;option.selected=true;option.dataset.draftUnavailable="true";
    select.append(option);return true;
  }
  function selectBackendDraftValue(select,saved,label){
    const configId=saved.value??"",configFingerprint=saved.backendFingerprint||"",trusted=saved.backendIdentityVersion===1;
    if(backendIdentityIsUnavailable(saved)){
      select.dataset.backendIdentityUnavailable="true";
      const option=document.createElement("option");
      option.value="draft:backend-unavailable";option.disabled=true;option.selected=true;option.dataset.draftUnavailable="true";option.textContent=`后端身份不可恢复 · ${label}`;
      select.append(option);
      const clear=document.createElement("option");
      clear.value="";clear.textContent="清空后端选择";clear.dataset.backendDraftClear="true";
      select.append(clear);return true;
    }
    delete select.dataset.backendIdentityUnavailable;
    const exact=[...select.options].find(option=>{const identity=backendOptionIdentity(option);return identity?.configId===configId&&identity?.configFingerprint===configFingerprint;});
    if(exact){select.value=exact.value;exact.selected=true;return false;}
    if(configId===""&&configFingerprint===""){select.value="";return false;}
    const option=document.createElement("option");
    option.value="draft:backend-unavailable";option.disabled=true;option.selected=true;option.dataset.draftUnavailable="true";
    if(trusted&&configId&&configFingerprint){option.dataset.backendConfigId=configId;option.dataset.backendFingerprint=configFingerprint;option.textContent=`${configId} · ${label}`;}
    else option.textContent=`后端身份不可恢复 · ${label}`;
    select.append(option);return true;
  }
  function restoreTaskForm(form,fields,options){
    const model=form.elements.model,effort=form.elements.reasoningEffort,fast=form.elements.codexFastMode,backend=form.elements.backendConfigId;
    if(model){
      selectDraftValue(model,fields?.model,"当前不可用");
      const desiredEffort=fields?.reasoningEffort?.present?fields.reasoningEffort.value??"":effort.value;
      const desiredFast=fields?.codexFastMode?.present?fields.codexFastMode.checked===true:fast.checked;
      if(backend)selectDraftValue(backend,fields?.backendConfigId,"原指纹已不可用");
      const refresh=()=>{
        const conflicts=[],config=runtimeChoices(options).find(value=>value.modelId===model.value),values=config?.supportedReasoningEfforts||[],previous=effort.value;
        effort.innerHTML='<option value="">选择推理强度</option>'+values.map(value=>`<option value="${C.esc(value)}">${C.esc(value)}</option>`).join("");
        const saved={present:true,value:previous};if(selectDraftValue(effort,saved,"当前模型不支持"))conflicts.push(`Reasoning ${previous} 当前不可用`);
        if(model.value&&!config)conflicts.push(`Model ${model.value} 当前不可用`);
        if(fast.checked&&!config?.codexFastModeSupported)conflicts.push("当前 Model 不支持已保存的 Fast Mode；请主动关闭");
        fast.disabled=!config?.codexFastModeSupported&&!fast.checked;
        if(backend){const selected=backend.selectedOptions[0];if(backend.dataset.backendIdentityUnavailable==="true")conflicts.push("后端逻辑身份不可恢复；请主动重选或清空选择");else if(selected?.dataset.draftUnavailable==="true"){const identity=backendOptionIdentity(selected);conflicts.push(identity?`后端 ${identity.configId} 的原指纹当前不可用`:"后端逻辑身份不可恢复；请主动重选");}else if(!selectedBackendIdentity(backend))conflicts.push("请选择后端");}
        form.dataset.runtimeSelectionConflict=conflicts.length?"true":"false";
        const message=form.querySelector("[data-runtime-selection-conflict]");if(message){message.hidden=!conflicts.length;message.textContent=conflicts.join("；");}
        for(const button of form.querySelectorAll('[type="submit"]'))button.disabled=conflicts.length>0;
      };
      effort.value="";selectDraftValue(effort,{present:true,value:desiredEffort},"当前模型不支持");fast.checked=desiredFast;
      model.addEventListener("change",()=>{const value=effort.value;const config=runtimeChoices(options).find(item=>item.modelId===model.value),values=config?.supportedReasoningEfforts||[];effort.innerHTML='<option value="">选择推理强度</option>'+values.map(item=>`<option value="${C.esc(item)}">${C.esc(item)}</option>`).join("");selectDraftValue(effort,{present:true,value},"当前模型不支持");refresh();});
      effort.addEventListener("change",refresh);fast.addEventListener("change",refresh);backend?.addEventListener("change",event=>{if(event.isTrusted&&backend.dataset.backendIdentityUnavailable==="true"){const option=backend.selectedOptions[0];if(backendOptionIdentity(option)||option?.dataset.backendDraftClear==="true")delete backend.dataset.backendIdentityUnavailable;}refresh();});refresh();
    }
    for(const input of taskFormFields(form))if(!["model","reasoningEffort","codexFastMode","backendConfigId"].includes(input.name))restoreTaskField(form,input,fields?.[input.name]);
  }
  function saveTaskState(){
    if(detailRoute.get()?.view!=="TASK"||!$("#detail-body").querySelector(".task-workbench[data-task-id]"))return;
    const body=$("#detail-body"),route=detailRoute.get();
    if(openedTask&&body.querySelector('[data-task-panel="overview"]'))for(const form of body.querySelectorAll("form[data-task-action-key][data-task-form-kind]")){const authority=taskFormDraftAuthority(form);if(authority)detailRoute.drafts.set(detailRoute.draftKey(authority),snapshotTaskForm(form));}
    const panel=body.querySelector("[data-task-panel]")?.dataset.taskPanel;
    const tabStates={...route.tabStates};if(panel)tabStates[panel]={expanded:[...body.querySelectorAll("details[data-section-key][open]")].map(n=>n.dataset.sectionKey),scrollTop:$("#detail").scrollTop};
    detailRoute.save({runFilter:route.runFilter||"",tabStates});
  }
  function paintTaskDetail(task){
    const p=task.presentation;if(!p?.taskView){detailReadError=t("workbench.taskViewUnavailable");updateDetailReadState();return;}
    saveTaskState();openedTask=task;agentRequests.clear();nativeApprovalRuntime.clear();$("#detail-title").textContent=p.taskView.title;
    const body=$("#detail-body"),route=detailRoute.get(),focused=document.activeElement,focusForm=body.contains(focused)?taskFormDraftAuthority(focused.closest("form")):"",focusName=focusForm?focused.name:null,focusValue=focused?.type==="radio"?focused.value:"",selection=focusName&&typeof focused.selectionStart==="number"?[focused.selectionStart,focused.selectionEnd]:null;
    body.innerHTML=renderTaskPresentation(p);
    const tabState=route?.tabStates?.[route.activeTab]||{};
    for(const n of body.querySelectorAll("details[data-section-key]"))n.open=(tabState.expanded||[]).includes(n.dataset.sectionKey);
    const currentDraftKeys=new Set();for(const form of body.querySelectorAll("form[data-task-action-key][data-task-form-kind]")){const authority=taskFormDraftAuthority(form),key=detailRoute.draftKey(authority);currentDraftKeys.add(key);restoreTaskForm(form,detailRoute.drafts.get(key),p.taskOptions||{});}
    if(route?.activeTab==="overview"){const prefix=JSON.stringify([route?.project,route?.rootTask]).slice(0,-1);const previous=[...detailRoute.drafts.entries()].filter(([key,value])=>key.startsWith(prefix)&&!currentDraftKeys.has(key)&&Object.values(value).some(Boolean));if(previous.length)body.insertAdjacentHTML("beforeend",`<details class="workbench-section" data-section-key="task:retained-drafts"><summary>${C.esc(t("workbench.draftInvalid"))}</summary><pre>${C.esc(JSON.stringify(previous.map(([,value])=>value),null,2))}</pre></details>`);}
    $("#detail").scrollTop=tabState.scrollTop||0;
    if(focused?.dataset?.taskTab)body.querySelector(`[data-task-tab="${focused.dataset.taskTab}"]`)?.focus({preventScroll:true});
    if(focusName){const form=[...body.querySelectorAll("form[data-task-action-key][data-task-form-kind]")].find(value=>taskFormDraftAuthority(value)===focusForm),field=form&&taskFormFields(form).find(value=>value.name===focusName&&(!focusValue||value.value===focusValue));field?.focus({preventScroll:true});if(selection&&field?.setSelectionRange)field.setSelectionRange(...selection);}
    body.querySelectorAll("[data-agent-native-approval]").forEach(b=>b.addEventListener("click",()=>answerNativeApproval(b)));
    body.querySelectorAll("[data-agent-answer],[data-agent-recovery]").forEach(b=>b.addEventListener("click",()=>answerAgentRequest(b)));
    body.querySelectorAll("[data-task-action]").forEach(b=>{if(b.type!=="submit")b.addEventListener("click",()=>invokeTaskAction(task,b));});
    body.querySelectorAll("[data-task-replay]").forEach(b=>b.addEventListener("click",()=>replayTaskAction(task,b)));
    body.querySelectorAll("[data-review-start]").forEach(form=>{const executorSelect=form.elements.executorKind,backendSelect=form.elements.backendConfigId;const syncExecutor=()=>{if(executorSelect&&backendSelect){const pi=executorSelect.value==="PI_AGENT";backendSelect.required=!pi;const backendLabel=backendSelect.closest("label");if(backendLabel)backendLabel.style.display=pi?"none":"";}};executorSelect?.addEventListener("change",syncExecutor);syncExecutor();form.addEventListener("submit",event=>{event.preventDefault();const button=event.submitter;if(!button)return;try{const paths=JSON.parse(form.elements.reviewFocusPaths.value),modules=JSON.parse(form.elements.reviewFocusModules.value),runtime=runtimeSelectionFromForm(form),pi=form.elements.executorKind.value==="PI_AGENT";if(!Array.isArray(paths)||!Array.isArray(modules))throw new Error("focus paths/modules 必须是 JSON 数组");invokeTaskAction(task,button,{executorKind:form.elements.executorKind.value,reviewEngineeringGoal:form.elements.reviewEngineeringGoal.value,reviewInstruction:form.elements.reviewInstruction.value,reviewFocus:{kind:form.elements.reviewFocusKind.value,summary:form.elements.reviewFocusSummary.value,paths,modules},model:runtime.nextRunRuntimeConfig.model,reasoningEffort:runtime.nextRunRuntimeConfig.reasoningEffort,codexFastMode:runtime.nextRunRuntimeConfig.codexFastMode,backendSelection:pi?null:runtime.backendSelection,reviewDepth:button.value});}catch(error){toast("请提供完整有效的 Review focus："+error.message,true);}});});
    body.querySelectorAll("[data-defer-task]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();invokeTaskAction(task,event.submitter,{note:new FormData(form).get("note")});}));
    body.querySelectorAll("[data-request-changes]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();try{const fields=new FormData(form);invokeTaskAction(task,event.submitter,{nextWorkSpec:JSON.parse(fields.get("nextWorkSpec")),nextVerificationSpec:JSON.parse(fields.get("nextVerificationSpec")),nextRunRuntimeConfig:{model:form.elements.model.value,reasoningEffort:form.elements.reasoningEffort.value,codexFastMode:form.elements.codexFastMode.checked}});}catch(error){toast("请提供有效的 specification JSON："+error.message,true);}}));
    body.querySelectorAll("[data-task-review-request]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();invokeTaskAction(task,form.querySelector("button"),{reviewPurpose:new FormData(form).get("reviewPurpose")});}));
    body.querySelectorAll("[data-re-review]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();const reviewFeedback=String(new FormData(form).get("reviewFeedback")||"");if(!reviewFeedback||reviewFeedback.includes("\0")||new TextEncoder().encode(reviewFeedback).byteLength>8192){toast("重新审查反馈必须为非空 UTF-8 文本，且不超过 8,192 字节。",true);return;}invokeTaskAction(task,event.submitter,{reviewFeedback});}));
    body.querySelectorAll("[data-open-project-repositories]").forEach(button=>button.addEventListener("click",()=>{setProjectTab("resources");state.resourceTab="repositories";detailRoute.set(null);render();}));
    body.querySelectorAll("[data-review-approve]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();invokeTaskAction(task,event.submitter,runtimeSelectionFromForm(form));}));
    body.querySelectorAll("[data-approved-task-start]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();invokeTaskAction(task,event.submitter,runtimeSelectionFromForm(form));}));
    body.querySelectorAll("[data-implementation-retry]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();invokeTaskAction(task,event.submitter,{nextRunRuntimeConfig:{model:form.elements.model.value,reasoningEffort:form.elements.reasoningEffort.value,codexFastMode:form.elements.codexFastMode.checked}});}));
    body.querySelectorAll("[data-approve-and-start]").forEach(form=>form.addEventListener("submit",event=>{event.preventDefault();const instructions=executionInstructionsFromForm(form,task);if(instructions===null)return;invokeTaskAction(task,event.submitter,{...runtimeSelectionFromForm(form),...instructions});}));
    updateDetailReadState();
  }
  function updateDetailReadState(){
    if(!detailRoute.get())return;
    const body=$("#detail-body"),busy=detailReadBusy||workRefresh.busy(),error=detailReadError||workRefresh.error();
    body.inert=busy;body.setAttribute("aria-busy",String(busy));body.classList.toggle("detail-interactive-loading",busy);
    let status=$("#detail-read-status");if(!status){status=document.createElement("div");status.id="detail-read-status";status.className="detail-read-state";body.before(status);}
    status.textContent=busy?"Loading…":error?`${t(mutationSubmitted?"workbench.submittedReadFailed":"workbench.stale")} · ${error}`:"";status.setAttribute("role",error&&!busy?"alert":"status");status.hidden=!busy&&!error;status.classList.toggle("is-loading",busy);
    for(const button of body.querySelectorAll("[data-task-action],[data-agent-answer],[data-agent-recovery],[data-agent-native-approval],[data-task-replay]")){
      const cap=openedTask&&taskPresentationActions(openedTask.presentation).find(c=>taskActionKey(c)===button.dataset.taskActionKey);
      const submitting=[...taskActionRequests.values()].some(record=>record.state==="SUBMITTING"&&taskRequestContext(record,openedTask?.presentation));
      button.disabled=busy||submitting||!!error||button.closest("form")?.dataset.runtimeSelectionConflict==="true"||(button.dataset.taskAction!==undefined&&(!cap||!!taskActionUnavailable(openedTask?.presentation,cap)));
    }
    if(error&&!openedTask&&detailRoute.get()?.view==="TASK")body.innerHTML=`<p role="alert">${C.esc(error)}</p><button type="button" data-task-refresh>${C.esc(t("workbench.retry"))}</button>`;
  }
  async function invokeTaskAction(task,button,input={}){
    const cap=taskPresentationActions(task.presentation).find(c=>taskActionKey(c)===button.dataset.taskActionKey);
    if(!cap||task!==openedTask||button.isConnected===false||button.disabled||detailReadBusy||detailReadError||workRefresh.busy()||workRefresh.error()||taskActionUnavailable(task.presentation,cap))return;
    const project=state.selectedProjectID,taskId=task.id;
    if(!taskRequestContext({project,taskId},task.presentation))return;
    let completionDescription="";
    if(cap.action==='COMPLETE_TASK'){
      button.disabled=true;
      try{const plan=await api(`/ui/api/tasks/${encodeURIComponent(taskId)}/completion-plan?${qs({project_id:project})}`);if(task!==openedTask||!taskRequestContext({project,taskId},task.presentation))return;input={...input,completionPlan:plan};completionDescription="\n\n合入目标：\n"+(plan.members||[]).map(m=>`${m.repositoryName||m.repositoryId} → ${m.developmentRef}\n目标提交 ${m.expectedDevHead}\n验证快照 ${m.verifiedSnapshot}`).join("\n\n");}
      catch(e){toast(e.message,true);return;}finally{if(button.isConnected)button.disabled=false;}
    }
    if(!window.confirm(taskActionLabel(cap)+"\n\n"+taskActionConsequence(cap)+completionDescription+"\n\nCore 将再次核对准确目标和当前资格。"))return;
    const payload=JSON.parse(JSON.stringify({...input,action:cap.action,target:cap.target}));
    const key=taskActionIdentity([project,taskId,payload]);
    let record=taskActionRequests.get(key);
    if(record?.state==="SUBMITTING")return;
    if(record?.state==="UNKNOWN"){
      // A repeated click after response loss is the original intent, even when
      // the refreshed projection still offers the same button.
      if(!taskRequestReplayable(record)){toast(t("workbench.repeatUnknown"),true);return;}
    }else if(!record){
      const requestId=window.crypto.randomUUID();
      record={project,taskId,action:cap.action,requestId,key,body:JSON.stringify({...payload,requestId}),state:"READY",reviewApprovalIdentity:["APPROVE","REVISE","REJECT","REQUEST_RE_REVIEW","ABANDON_TASK"].includes(cap.action)?reviewApprovalIdentity(task.presentation):null};
      taskActionRequests.set(key,record);
    }
    await submitTaskAction(task,button,record);
  }
  async function replayTaskAction(task,button){
    const record=[...taskActionRequests.values()].find(value=>value.requestId===button.dataset.taskReplay);
    if(!record||!['UNKNOWN','PARTIAL'].includes(record.state)||!taskRequestReplayable(record)||task!==openedTask||button.isConnected===false||button.disabled||!taskRequestContext(record,task.presentation))return;
    // This only replays the saved body. It never substitutes a newly projected
    // target or authorizes a new request against a resolved Decision.
    await submitTaskAction(task,button,record);
  }
  async function refreshLocalApproveAndStartEvidence(task,button){
    const record=[...taskActionRequests.values()].find(value=>value.action==="APPROVE_AND_START"&&value.requestId===button.dataset.taskApproveStartRefresh);
    if(!task||!record||record.evidenceRefreshing||task!==openedTask||button.isConnected===false||!taskRequestContext(record,task.presentation))return;
    const route=detailRoute.get(),navigation=taskActionNavigation,sequence=(record.evidenceReadSequence||0)+1;
    const current=()=>taskActionRequests.get(record.key)===record&&detailRoute.get()===route&&taskActionNavigation===navigation&&sequence===record.evidenceReadSequence&&taskRequestContext(record,openedTask?.presentation);
    record.evidenceReadSequence=sequence;record.evidenceRefreshing=true;paintTaskDetail(openedTask);
    try{
      // This dedicated endpoint only reads the durable parent and exact Run
      // evidence. The original body is retained solely for identity checking;
      // it is not sent to the action resolver and cannot advance a command.
      const result=await api(`/ui/api/tasks/${encodeURIComponent(record.taskId)}/local-approve-and-start-receipt?${qs({project_id:record.project})}`,{method:"POST",body:record.body});
      if(!current())return;
      if(result?.command?.state){record.lastConfirmedEvidence=result;record.lastConfirmedEvidenceReadable=true;}
      record.evidence=result;record.evidenceFailure=null;record.evidenceReadable=true;
      if(result?.command?.state){record.commandState=result.command.state;record.commandCode=result.command.errorCode||"";record.commandFailureStage=result.failureStage||"";record.commandFailureSource=result.failureSource||"";}
    }catch(error){if(current()){record.evidenceFailure={code:error.code||"LOCAL_RECEIPT_READ",message:error.message||"接管证据读取失败"};if([400,403,404,409].includes(error.status)||localApproveStartReceiptVisibilityCodes.has(error.code)){record.evidenceReadable=false;record.evidence=null;}}}
    finally{if(taskActionRequests.get(record.key)===record&&sequence===record.evidenceReadSequence){record.evidenceRefreshing=false;if(current())paintTaskDetail(openedTask);}}
  }
  const reviewAdmissionCodes=new Set(["INVALID_ARGUMENT","INVALID_PATH","NOT_FOUND","CONFLICT","INVALID_STATE","PERMISSION_DENIED","REVIEW_SCOPE_FORBIDDEN","FEATURE_AREA_RETIRING","FEATURE_AREA_BUSY","STALE_REVIEW_APPROVAL","REQUEST_CONFLICT","TASK_ALREADY_HAS_ACTIVE_RUN"]);
  function reviewApprovalIdentity(p){const g=p?.workflowGuidance,a=g?.reviewApproval;return a?.decisionId&&a?.reviewId&&a?.resultSha256&&a?.provenanceSha256?taskActionIdentity([g.projectId,g.featureAreaId,g.taskId,a.decisionId,a.reviewId,a.resultSha256,a.provenanceSha256]):null;}
  async function submitTaskAction(task,button,record){
    const route=detailRoute.get(),navigation=taskActionNavigation,authority=button.dataset.taskActionKey;
    const current=()=>detailRoute.get()===route&&taskActionNavigation===navigation&&taskRequestContext(record,openedTask?.presentation);
    const capability=taskPresentationActions(task.presentation).find(value=>taskActionKey(value)===authority);
    const replay=button.dataset.taskReplay===record.requestId&&taskActionRequests.get(record.key)===record&&['UNKNOWN','PARTIAL'].includes(record.state)&&taskRequestReplayable(record);
    if(!current()||openedTask!==task||(!capability&&!replay)||button.isConnected===false||button.disabled)return;
    if([...taskActionRequests.values()].some(value=>value!==record&&value.state==="SUBMITTING"&&value.project===record.project&&value.taskId===record.taskId))return;
    saveTaskState();record.state="SUBMITTING";detailReadBusy=true;detailReadError="";mutationSubmitted=false;updateDetailReadState();workRefresh.stop();
    let accepted=false,error="",rejection=null;
    try{
      const result=await api(`/ui/api/tasks/${encodeURIComponent(record.taskId)}/action?${qs({project_id:record.project})}`,{method:"POST",body:record.body});
      accepted=true;
      if(record.action==="APPROVE_AND_START"&&result?.command?.state){
        record.evidence=result;record.evidenceReadable=true;record.lastConfirmedEvidence=result;record.lastConfirmedEvidenceReadable=true;
        record.commandState=result.command.state;record.commandCode=result.command.errorCode||"";record.commandFailureStage=result.failureStage||"";record.commandFailureSource=result.failureSource||"";
        if(['STARTED','DIRECT_FILE_READY'].includes(record.commandState)&&!result.failureCode){record.state="READABLE";record.evidence=result;}
        else if(record.commandState==="REJECTED"){record.state="REJECTED";record.rejection={code:record.commandCode||record.commandState,message:"Core 已持久化该终态"};rejection=record.rejection;}
        else if(record.commandState==="PARTIAL")record.state="PARTIAL";
        else record.state="UNKNOWN";
      }else{record.state="CONFIRMED";taskActionRequests.delete(record.key);}
    }catch(e){record.state="UNKNOWN";error=e.message;if(record.reviewApprovalIdentity&&[400,403,404,409].includes(e.status)&&reviewAdmissionCodes.has(e.code))rejection={code:e.code,message:e.message};}
    if(!current())return;
    mutationSubmitted=accepted;detailReadBusy=false;
    // Every response, including a lost one, is followed by authoritative readback.
    // Never infer acceptance or a phase transition from transport success.
    const fresh=await workRefresh.enter(record.project,record.taskId);
    if(!current())return;
    if(fresh?.ok){mutationSubmitted=false;detailReadError="";}
    else detailReadError=fresh?.error||"Readback unavailable";
    if(fresh?.ok&&rejection&&(!record.reviewApprovalIdentity||reviewApprovalIdentity(openedTask?.presentation)===record.reviewApprovalIdentity)){record.state="REJECTED";record.rejection=rejection;toast(`${record.action} · ${record.commandState||"REJECTED"} · ${rejection.code} · ${rejection.message}`,true);}
    else if(!accepted)toast(t("workbench.commandUnknownNotice")+(error?" · "+error:""),true);
    const commandStatus=$("#detail-body").querySelector("[data-task-command-status]");if(commandStatus)commandStatus.innerHTML=renderUnconfirmedTaskRequests(openedTask?.presentation);
    updateDetailReadState();
  }
  function openTaskDetail(taskID,providedTask) {
    window.clearTimeout(runReadTimer);saveTaskState();detailRoute.task(state.selectedProjectID,taskID,state.projectTab);writeDetailLink();workDrawerTaskID=taskID;workDrawerExecutionID="";openedTask=null;taskRunsPage=null;runSnapshot=null;detailReadBusy=false;detailReadError="";mutationSubmitted=false;
    selectActivityContext(String(state.selectedProjectID)+":"+taskID);agentRequests.clear();nativeApprovalRuntime.clear();
    $("#detail-title").textContent=providedTask?.title||"Task";$("#detail-body").innerHTML='<p role="status">Loading Task…</p>';
    openDetail("",true);return workRefresh.enter(state.selectedProjectID,taskID);
  }
  async function openDiagnosticRun(runID){
    saveTaskState();workRefresh.stop();detailRoute.diagnostic(state.selectedProjectID,runID);writeDetailLink();workDrawerTaskID="";runSnapshot=null;runPromptSnapshot=null;detailReadError="";openDetail("",true);return readTaskRun();
  }
  async function openTaskRun(runID,tab="overview"){
    saveTaskState();workRefresh.stop();detailRoute.run(runID);if(TW.RUN_TABS.includes(tab))detailRoute.tab(tab);writeDetailLink();runSnapshot=null;runPromptSnapshot=null;detailReadError="";
    await readTaskRun();
    if(runSnapshot&&detailRoute.get()?.view==="RUN"&&detailRoute.get()?.target===runID&&detailRoute.get()?.activeTab==="instructions")await readRunPrompt();
  }
  function scheduleRunRead(){
    window.clearTimeout(runReadTimer);runReadTimer=null;
    if(document.hidden||detailRoute.get()?.view!=="RUN"||["SUCCEEDED","FAILED","INFRASTRUCTURE_FAILED","CANCELED"].includes(runSnapshot?.run?.status)&&!C.needsAnalysisRefresh(runSnapshot))return;
    runReadTimer=window.setTimeout(()=>{if(detailReadBusy)scheduleRunRead();else readTaskRun("BACKGROUND");},5000);
  }
  async function readTaskRun(mode="INTERACTIVE"){
    const read=detailRoute.read(),route=read.route;if(route?.view!=="RUN")return;
    detailReadBusy=mode==="INTERACTIVE";detailReadError="";$("#detail-title").textContent="Run · "+route.target;if(mode==="INTERACTIVE"&&!runSnapshot)$("#detail-body").innerHTML='<p role="status">Loading Run…</p>';updateDetailReadState();
    try{const detail=await api(`/v1/runs/${encodeURIComponent(route.target)}?${qs({project_id:route.project,task_id:route.rootTask})}`,{signal:read.signal});if(!read.current())return;runSnapshot=detail;paintRunDetail();}
    catch(e){if(read.current()&&e.name!=="AbortError"){detailReadError=e.message;if(!runSnapshot)$("#detail-body").innerHTML=`<button type="button" data-run-back>${C.esc(t(route.rootTask?"workbench.backTask":"workbench.backDiagnostics"))}</button><p role="alert">${C.esc(e.message)}</p><button type="button" data-run-retry>${C.esc(t("workbench.retry"))}</button>`;}}
    finally{if(read.current()){detailReadBusy=false;updateDetailReadState();scheduleRunRead();}}
  }
  function paintRunDetail(){if(!runSnapshot)return;const scroll=$("#detail").scrollTop,focusTab=document.activeElement?.dataset?.runTab;$("#detail-body").innerHTML=TW.renderRun(runSnapshot,{C,t,locale:i18n.locale,activeTab:detailRoute.get()?.activeTab,rootTask:detailRoute.get()?.rootTask,source:detailRoute.get()?.source,prompt:runPromptSnapshot,promptError:detailReadError});$("#detail").scrollTop=scroll;if(focusTab)$("#detail-body").querySelector(`[data-run-tab="${focusTab}"]`)?.focus({preventScroll:true});}
  async function readRunPrompt(){
    const read=detailRoute.read(),route=read.route;detailReadBusy=true;detailReadError="";updateDetailReadState();
    try{const prompt=await api(`/v1/runs/${encodeURIComponent(route.target)}/prompt?${qs({project_id:route.project,task_id:route.rootTask})}`,{signal:read.signal});if(read.current())runPromptSnapshot=prompt;}
    catch(e){if(read.current()&&e.name!=="AbortError")detailReadError=e.message;}
    finally{if(read.current()){detailReadBusy=false;paintRunDetail();updateDetailReadState();scheduleRunRead();}}
  }
  function returnToTask(){
    window.clearTimeout(runReadTimer);if(!detailRoute.get()?.rootTask){closeDetail();return;}const route=detailRoute.back();writeDetailLink();if(!route)return;detailReadBusy=false;detailReadError="";workDrawerTaskID=route.rootTask;
    if(openedTask)paintTaskDetail(openedTask);return workRefresh.enter(route.project,route.rootTask);
  }
  async function readDefinitionRevision(revisionID){
    saveTaskState();workRefresh.stop();const read=detailRoute.read(),route=read.route;if(route?.view!=="TASK")return;
    detailReadBusy=true;detailReadError="";updateDetailReadState();
    try{const value=await api(`/ui/api/tasks/${encodeURIComponent(route.rootTask)}/definition-revisions/${encodeURIComponent(revisionID)}?${qs({project_id:route.project})}`,{signal:read.signal});if(read.current()){revisionSnapshots.set(JSON.stringify([route.project,route.rootTask,revisionID]),value);paintTaskDetail(openedTask);}}
    catch(e){if(read.current()&&e.name!=="AbortError")detailReadError=e.message;}
    finally{if(read.current()){detailReadBusy=false;updateDetailReadState();workRefresh.enter(route.project,route.rootTask,"BACKGROUND");}}
  }
  async function readTaskRuns(offset){
    saveTaskState();workRefresh.stop();const read=detailRoute.read(),route=read.route;detailReadBusy=true;detailReadError="";updateDetailReadState();
    try{const page=await api(`/ui/api/tasks/${encodeURIComponent(route.rootTask)}/runs?${qs({project_id:route.project,limit:20,offset,purpose:route.runFilter})}`,{signal:read.signal});if(read.current()){taskRunsPage=page;paintTaskDetail(openedTask);}}
    catch(e){if(read.current()&&e.name!=="AbortError")detailReadError=e.message;}
    finally{if(read.current()){detailReadBusy=false;updateDetailReadState();workRefresh.enter(route.project,route.rootTask,"BACKGROUND");}}
  }

  async function openReviewPrompts(taskID) {
    workDrawerTaskID="";workDrawerExecutionID="";
    workflowDrawerTarget = ""; workflowDrawerVersion++;
    try {
      const detail = await api(`/v1/tasks/${encodeURIComponent(taskID)}?${qs({project_id: state.selectedProjectID})}`);
      const reviews = detail.reviews || [];
      $("#detail-title").textContent = t("review.prompts");
      const usageFacts = (usage) => C.buildUsageFacts(usage, t, i18n.locale).map(([label, value]) => [label, C.esc(value)]).concat([[t("usage.duration"), C.esc(C.durationLabel(usage && usage.duration_ms) || "—")]]);
      $("#detail-body").innerHTML = reviews.length ? `<div class="diagnostic-list">${reviews.map((review) => `<div class="diagnostic-item"><strong>${technicalValue(review.id, t("review.prompts"))}</strong>${review.usage ? C.keyValueDetails([[t("usage.title"), ""], ...usageFacts(review.usage)]) : ""}${C.keyValueDetails([[t("diagnostics.rawStatus"), `<code>${C.esc(review.state || "—")}</code>`], [t("review.focus"), C.esc(review.focus && (review.focus.summary || review.focus.mode) || "—")], [t("review.sha256"), technicalValue(review.prompt_sha256, t("review.sha256"))], [t("review.bytes"), C.esc(review.prompt_bytes || 0)]])}<button class="text-button" type="button" data-view-review-prompt="${C.esc(review.id)}"${review.prompt_available ? "" : " disabled"}>${C.esc(t("action.viewPrompt"))}</button></div>`).join("")}</div>` : `<p class="muted">${C.esc(t("review.none"))}</p>`;
      openDetail();
    } catch (error) { toast(error.message, true); }
  }

  async function viewReviewPrompt(reviewID) {
    workDrawerTaskID="";workDrawerExecutionID="";
    try {
      const value = await api(`/v1/reviews/${encodeURIComponent(reviewID)}/prompt?${qs({project_id: state.selectedProjectID})}`);
      state.reviewPrompt = value.prompt || "";
      $("#detail-title").textContent = t("review.prompt");
      $("#detail-body").innerHTML = `${C.keyValueDetails([[t("diagnostics.internalId"), technicalValue(value.review_id, t("review.prompts"))], [t("review.sha256"), technicalValue(value.sha256, t("review.sha256"))], [t("review.bytes"), C.esc(value.bytes || 0)], [t("review.sent"), C.localizedTimestamp(value.sent_at, i18n.locale, "—")]])}<p class="muted">${C.esc(t("review.localOnly"))}</p><button class="text-button" type="button" data-copy-review-prompt>${C.esc(t("action.copyPrompt"))}</button><pre>${C.esc(value.prompt || t("review.unavailable"))}</pre>`;
    } catch (error) { toast(error.message, true); }
  }

  async function viewRunPrompt(runID) {
    const project=state.selectedProjectID;await openDiagnosticRun(runID);
    if(detailRoute.get()?.project!==project||detailRoute.get()?.target!==runID)return;
    detailRoute.tab("instructions");writeDetailLink();paintRunDetail();return readRunPrompt();
  }

  async function viewContextProbePrompt(probeID) {
    try {
      const value = await api(`/v1/context-probes/${encodeURIComponent(probeID)}/prompt?${qs({project_id: state.selectedProjectID})}`);
      state.reviewPrompt = value.prompt || "";
      $("#detail-title").textContent = `${t("diagnostics.contextProbe")} · ${t("review.prompt")}`;
      $("#detail-body").innerHTML = `${C.keyValueDetails([[t("diagnostics.contextProbe"), technicalValue(value.context_probe_id, t("diagnostics.contextProbe"))], [t("review.sha256"), technicalValue(value.sha256, t("review.sha256"))], [t("review.bytes"), C.esc(value.bytes || 0)], [t("review.sent"), C.localizedTimestamp(value.sent_at, i18n.locale, "—")]])}<p class="muted">${C.esc(t("review.localOnly"))}</p><button class="text-button" type="button" data-copy-review-prompt>${C.esc(t("action.copyPrompt"))}</button><pre>${C.esc(value.prompt || t("review.unavailable"))}</pre>`;
    } catch (error) { toast(error.message, true); }
  }

  let resultReturnFocus=null,resultVersion=0;
  function showResultModal(content,title,trigger=document.activeElement) {
    resultVersion++;resultReturnFocus=trigger;
    $("#result-modal-title").textContent=title;
    $("#result-modal-body").innerHTML=`<pre class="full-result-content">${C.esc(content)}</pre>`;
    $("#result-modal").classList.remove("hidden");
    $("#result-modal-close").focus();
  }
  async function openFullReviewResult(reviewID,trigger=document.activeElement) {
    const read=detailRoute.read(),version=++resultVersion;detailReadBusy=true;detailReadError="";updateDetailReadState();
    try {
      const value=await api(`/ui/api/reviews/${encodeURIComponent(reviewID)}/result?${qs({project_id:state.selectedProjectID})}`,{signal:read.signal});if(!read.current()||version!==resultVersion)return;
      showResultModal(value.result||"",t("review.fullResult"),trigger);
      if(value.analysis||value.analysis_read_error)$("#result-modal-body").innerHTML=C.runOutput({native_output:value.result||"",analysis:value.analysis,analysis_read_error:value.analysis_read_error},t);
    }catch(error){if(read.current()&&error.name!=="AbortError"){detailReadError=error.message;toast(error.message,true);}}
    finally{if(read.current()){detailReadBusy=false;updateDetailReadState();}}
  }

  let retireFocus=null;
  const retire=window.DevBridgeRetire.create({
    requestId:()=>window.crypto.randomUUID(),
    mutate:async topic=>{
      const result=await api(`/ui/api/featureAreas/${encodeURIComponent(topic.id)}/close`,{method:"POST",body:JSON.stringify({project_id:topic.projectId,request_id:topic.requestId})});
      if(result?.id!==topic.id||!["RETIRING","RETIRED"].includes(result.status))throw new Error(t("topics.closeReadbackFailed"));
      return result;
    },
    refresh:async topic=>{
      if(state.selectedProjectID!==topic.projectId)return;
      const drawer=workflowDrawerTarget,version=workflowDrawerVersion;
      await loadProjectData();if(state.selectedProjectID!==topic.projectId)return;
      render();if(drawer===topic.id&&workflowDrawerTarget===drawer&&workflowDrawerVersion===version)openTopicDetail(topic.id,true);
      if(workDrawerTaskID)await workRefresh.freshAfterAction();
    },
    changed:()=>paintRetireDialog()
  });
  function closeFeatureArea(featureAreaID) {
    const topic=state.projectWorkspace?.featureAreas.find(value=>value.id===featureAreaID);
    if(!topic?.can_retire||["RETIRING","RETIRED","DECOMPOSED"].includes(topic.status))return;
    retireFocus=document.activeElement;
    retire.open({projectId:state.selectedProjectID,id:topic.id,title:topic.title});
  }
  function paintRetireDialog() {
    const dialog=$("#retire-dialog"),value=retire.state();
    if(!value.topic){if(dialog.open)dialog.close();if(retireFocus?.isConnected)retireFocus.focus();else $("#detail-close").focus();return;}
    $("#retire-name").textContent=`“${value.topic.title}” 将进入关闭流程。`;
    $("#retire-error").textContent=value.error;
    $("#retire-confirm").disabled=value.busy;$("#retire-confirm").textContent=value.busy?"正在关闭…":"确认关闭";
    if(!dialog.open)dialog.showModal();
  }


  function filterToObject(kind, id, label) {
    state.logFilter = {kind, id, label}; state.view = "logs"; window.localStorage.setItem(NAV_KEY, "logs");
    refreshLogs();
  }

  function toggleLogPause() {
    state.logView = LV.togglePause(state.logView);
    if (!state.logView.paused) refreshLogs();
    else render();
  }

  function jumpToLatestLogs() {
    state.logView = LV.jumpToLatest(state.logView);
    refreshLogs();
  }

  function parseDetailLink(hash){
    const q=new URLSearchParams(String(hash||"").replace(/^#/,"")),project=q.get("project"),task=q.get("task"),run=q.get("run"),tab=q.get("tab");
    if(!project||(!task&&!run))return null;
    return {project,task:task||"",run:run||"",tab:tab||"overview",source:q.get("source")==="diagnostics"?"diagnostics":"tasks"};
  }
  let restoringDetailLink=false,detailLinkGeneration=0;
  function writeDetailLink(){
    if(restoringDetailLink)return;const route=detailRoute.get();
    const hash=route?qs({project:route.project,task:route.rootTask,run:route.view==="RUN"?route.target:"",tab:route.activeTab,source:route.source==="diagnostics"?"diagnostics":"tasks"}):"";
    window.history.replaceState(null,"",window.location.pathname+window.location.search+(hash?"#"+hash:""));
  }
  async function restoreDetailLink(){
    const target=parseDetailLink(window.location.hash),generation=++detailLinkGeneration;if(!target)return;
    restoringDetailLink=true;
    try{
      if(!state.projects.some(p=>p.id===target.project)){toast(t("workbench.ownerMismatch"),true);return;}
      if(state.selectedProjectID!==target.project)await selectProject(target.project);
      if(generation!==detailLinkGeneration)return;
      state.view="projects";state.projectTab=target.source==="diagnostics"?"diagnostics":"tasks";render();
      if(target.task){await openTaskDetail(target.task);if(generation!==detailLinkGeneration||detailRoute.get()?.rootTask!==target.task)return;if(target.run)await openTaskRun(target.run);}
      else await openDiagnosticRun(target.run);
      if(generation!==detailLinkGeneration)return;
      detailRoute.tab(target.tab);if(detailRoute.get()?.view==="RUN"){paintRunDetail();if(target.tab==="instructions")await readRunPrompt();}else if(openedTask)paintTaskDetail(openedTask);
    }finally{if(generation===detailLinkGeneration)restoringDetailLink=false;}
  }
  async function openTaskLink(button){const project=state.selectedProjectID,id=button.dataset.taskDetail;await openTaskDetail(id);if(project===state.selectedProjectID&&detailRoute.get()?.view==="TASK"&&detailRoute.get()?.rootTask===id&&button.dataset.taskOpenTab){detailRoute.tab(button.dataset.taskOpenTab);if(openedTask)paintTaskDetail(openedTask);}writeDetailLink();}
  function bindWorkbenchEvents(){
    $$('#page [data-workbench-filter]').forEach(field=>field.addEventListener(field.type==="search"?"input":"change",()=>{
      state.taskFilters[field.dataset.workbenchFilter]=field.value;state.taskPage=0;
      if(field.dataset.workbenchFilter==="area"){state.workItemAreaFilter=field.value;persistWorkItemFilters();}
      window.clearTimeout(searchTimer);if(field.type==="search")searchTimer=window.setTimeout(loadTaskList,200);else loadTaskList();
    }));
    $$('#page [data-workbench-page]').forEach(button=>button.addEventListener('click',()=>{state.taskPage=Math.max(0,Number(button.dataset.workbenchPage));loadTaskList();}));
    $$('#page [data-task-list-retry]').forEach(button=>button.addEventListener('click',loadTaskList));
    $$('#page [data-project-retry]').forEach(button=>button.addEventListener('click',()=>loadAll(true)));
    $$('#page [data-resource-tab]').forEach(button=>button.addEventListener('click',()=>{state.resourceTab=button.dataset.resourceTab;render();}));
    $$('#page [data-diagnostic-tab]').forEach(button=>button.addEventListener('click',()=>{state.diagnosticTab=button.dataset.diagnosticTab;if(state.diagnosticTab==="workspaces")loadWorktreeInventory();if(state.diagnosticTab==="logs")refreshLogs();else stopLogPoll();render();}));
    $$('#page [data-diagnostic-run]').forEach(button=>button.addEventListener('click',()=>openDiagnosticRun(button.dataset.diagnosticRun)));
  }

  function bindRenderedEvents() {
    bindWorkbenchEvents();
    $$('[data-development-form]').forEach((form) => form.addEventListener("submit", (event) => { event.preventDefault(); selectDevelopmentBranch(form.dataset.repositoryId, form.elements.developmentRef.value); }));
    $$('[data-select-project]').forEach((button) => button.addEventListener("click", () => selectProject(button.dataset.selectProject)));
    $$('[data-open-project]').forEach((button) => button.addEventListener("click", () => switchView("projects")));
    $$('[data-worktree-refresh]').forEach(button=>button.addEventListener("click",()=>loadWorktreeInventory()));
    $$('[data-authorization-form]').forEach((form) => { form.addEventListener("change",()=>rememberExecutionAuthorizationDraft(form)); form.addEventListener("submit",(event)=>{event.preventDefault();saveExecutionAuthorization(form);}); });
    $$('[data-authorization-refresh]').forEach(button=>button.addEventListener("click",()=>refreshExecutionAuthorization(true)));
    $$('[data-project-tab]').forEach((button) => button.addEventListener("click", () => { setProjectTab(button.dataset.projectTab); render(); }));
    $$('[data-topic-row]').forEach(row=>{
      row.addEventListener("click",event=>{if(event.target.closest("button,a,input,select,textarea,summary,details,[data-row-action]"))return;openTopicDetail(row.dataset.topicRow);});
      row.addEventListener("keydown",event=>{if(event.target!==row || !["Enter"," "].includes(event.key))return;event.preventDefault();openTopicDetail(row.dataset.topicRow);});
      row.querySelectorAll("button,a,[data-row-action]").forEach(action=>action.addEventListener("click",event=>event.stopPropagation()));
    });
    $$('#page [data-menu-commit]').forEach(button=>button.addEventListener('click',()=>{const id=button.dataset.menuCommit;openTopicDetail(id);areaAction(id,'COMMIT_FEATURE_AREA',{},button);}));
    $$('[data-work-refresh]').forEach(select=>select.addEventListener("change",()=>workRefresh.refresh()));
    $$('[data-execution-detail]').forEach(button=>button.addEventListener("click",()=>openExecutionDetail(button.dataset.executionDetail)));
    $$('#page [data-execution-cancel]').forEach(button=>button.addEventListener("click",event=>{event.stopPropagation();cancelExecution(button);}));
    $$('[data-topic-detail]').forEach((button) => button.addEventListener("click", () => openTopicDetail(button.dataset.topicDetail)));
    $$('#page [data-close-feature-area]').forEach((button) => button.addEventListener("click", () => closeFeatureArea(button.dataset.closeFeatureArea)));
    $$('[data-review-detail]').forEach((button) => button.addEventListener("click", () => openReviewDetail(button.dataset.taskId, button.dataset.reviewDetail)));
    $$('[data-document-detail]').forEach((button) => button.addEventListener("click", () => openDocumentDetail(button.dataset.documentDetail)));
    $$('#page [data-task-detail]').forEach(button=>button.addEventListener('click',()=>openTaskLink(button)));
    $$('[data-log-kind][data-log-id]').forEach((button) => button.addEventListener("click", () => filterToObject(button.dataset.logKind, button.dataset.logId, button.dataset.logLabel)));
    $$('[data-review-prompts]').forEach((button) => button.addEventListener("click", () => openReviewPrompts(button.dataset.reviewPrompts)));
    $$(".log-detail").forEach((button) => button.addEventListener("click", () => openLog(state.logs[Number(button.dataset.logIndex)])));
    const refresh = $("#logs-refresh"); if (refresh) refresh.addEventListener("click", () => refreshLogs(true));
    const clear = $("#logs-clear"); if (clear) clear.addEventListener("click", () => { state.logFilter = {kind: "", id: "", label: ""}; refreshLogs(); });
    const kind = $("#log-kind"); if (kind) kind.addEventListener("change", () => { state.logFilter = {kind: kind.value, id: "", label: ""}; render(); });
    const form = $("#log-filters"); if (form) form.addEventListener("submit", (event) => { event.preventDefault(); const object = $("#log-object"); state.logFilter.id = object && object.value || ""; state.logFilter.label = object && object.selectedOptions[0] ? object.selectedOptions[0].textContent : ""; refreshLogs(); });
    const pause = $("#logs-pause"); if (pause) pause.addEventListener("click", toggleLogPause);
    const jump = $("#logs-jump"); if (jump) jump.addEventListener("click", jumpToLatestLogs);
    const verbose = $("#log-verbose"); if (verbose) verbose.addEventListener("change", () => { state.logVerbose = verbose.checked; applyLogFilters(); render(); });
    const level = $("#log-level"); if (level) level.addEventListener("change", () => { state.logFilters.level = level.value; applyLogFilters(); render(); });
    const component = $("#log-component"); if (component) component.addEventListener("change", () => { state.logFilters.component = component.value; applyLogFilters(); render(); });
    const eventSelect = $("#log-event"); if (eventSelect) eventSelect.addEventListener("change", () => { state.logFilters.event = eventSelect.value; applyLogFilters(); render(); });
    const text = $("#log-text"); if (text) text.addEventListener("input", () => { state.logFilters.text = text.value; applyLogFilters(); render(); const next = $("#log-text"); if (next) { next.focus(); next.setSelectionRange(next.value.length, next.value.length); } });
    const trigger = $("[data-project-trigger]");
    const dropdown = $("[data-project-dropdown]");
    if (trigger && dropdown) {
      trigger.addEventListener("click", (event) => { event.stopPropagation(); state.projectDropdownOpen = !state.projectDropdownOpen; render(); if (state.projectDropdownOpen) { const search = $("[data-project-search]"); if (search) { search.focus(); search.select(); } } });
      $$("[data-select-project]").forEach((option) => option.addEventListener("click", () => { state.projectDropdownOpen = false; selectProject(option.dataset.selectProject); }));
      const search = $("[data-project-search]");
      if (search) search.addEventListener("input", () => { state.projectSearch = search.value; render(); const next = $("[data-project-search]"); if (next) { next.focus(); next.setSelectionRange(next.value.length, next.value.length); } });
    }
    const moduleSearch = $("#module-search"); if (moduleSearch) moduleSearch.addEventListener("input", () => { state.moduleSearch = moduleSearch.value; render(); const next = $("#module-search"); if (next) { next.focus(); next.setSelectionRange(next.value.length, next.value.length); } });
    $$("[data-module-toggle]").forEach((toggle) => toggle.addEventListener("click", () => { const card = toggle.closest("[data-module-name]"); if (!card) return; const name = card.dataset.moduleName; const expanded = state.moduleExpanded[name] !== true; state.moduleExpanded[name] = expanded; render(); }));
    const collapse = $("[data-modules-collapse]"); if (collapse) collapse.addEventListener("click", () => { state.moduleExpanded = {}; render(); });
    const taskFilter = $("#task-filter"); if (taskFilter) taskFilter.addEventListener("change", () => { state.taskFilter = taskFilter.value; render(); });
    const taskKindFilter = $("#task-kind-filter"); if (taskKindFilter) taskKindFilter.addEventListener("change", () => { state.taskKindFilter = taskKindFilter.value;state.taskPage=0;loadTaskList(); });
    const clearTaskFilters=$("[data-task-filters-clear]");if(clearTaskFilters)clearTaskFilters.addEventListener("click",()=>{state.taskFilters={area:""};state.workItemAreaFilter="";state.workItemBranchFilter="";state.taskKindFilter="all";state.taskPage=0;state.taskFilterNotice=false;persistWorkItemFilters();loadTaskList();});
    const workItemAreaFilter = $("#work-item-area-filter"); if (workItemAreaFilter) workItemAreaFilter.addEventListener("change", () => { state.workItemAreaFilter = workItemAreaFilter.value; persistWorkItemFilters(); render(); });
    const workItemBranchFilter = $("#work-item-branch-filter"); if (workItemBranchFilter) workItemBranchFilter.addEventListener("change", () => { state.workItemBranchFilter = workItemBranchFilter.value; persistWorkItemFilters();state.taskPage=0;loadTaskList(); });
    const featureAreaStatusFilter = $("#feature-area-status-filter"); if (featureAreaStatusFilter) featureAreaStatusFilter.addEventListener("change", () => { state.featureAreaStatusFilter = featureAreaStatusFilter.value; render(); });
    $$('[data-copy]').forEach((button) => button.addEventListener("click", async () => { try { await navigator.clipboard.writeText(button.dataset.copy); toast(t("action.copied")); } catch (_) { toast(t("error.load.body"), true); } }));
  }

  function openDetail(topicID = "", work = false) { if($("#detail-layer").classList.contains("hidden"))detailReturnFocus=document.activeElement;if(!work){saveTaskState();detailRoute.close();workRefresh.stop();workDrawerTaskID="";workDrawerExecutionID="";$("#detail-body").inert=false;$("#detail-read-status")?.remove();} workflowDrawerTarget = topicID; workflowDrawerVersion++; $("#detail-layer").classList.remove("hidden"); }
  function closeDetail() { if(!restoringDetailLink)detailLinkGeneration++;window.clearTimeout(runReadTimer);saveTaskState();detailRoute.close();writeDetailLink();detailReadBusy=false;$("#detail-body").inert=false;$("#detail-read-status")?.remove();workRefresh.stop(); selectActivityContext(""); workDrawerTaskID="";workDrawerExecutionID=""; workflowDrawerTarget=""; workflowDrawerVersion++; closeResultModal(); $("#detail-layer").classList.add("hidden");if(detailReturnFocus?.isConnected)detailReturnFocus.focus(); }
  function closeResultModal() { resultVersion++;$("#result-modal").classList.add("hidden");$("#result-modal-body").textContent="";if(resultReturnFocus?.isConnected)resultReturnFocus.focus();else if(!$("#detail-layer").classList.contains("hidden"))$("#detail-close").focus(); }
  function toast(message, isError = false) {
    const target = $("#toast");
    if (toastTimer) window.clearTimeout(toastTimer);
    target.textContent = message; target.className = `toast${isError ? " error" : ""}`;
    toastTimer = window.setTimeout(() => { target.classList.add("hidden"); toastTimer = null; }, 2600);
  }

  if (embeddedLayout) {
    $("#refresh").addEventListener("click", () => loadEmbedded(true));
    i18n.applyDocument(document);
    loadEmbedded();
    return;
  }

  $$(".nav-item[data-view]").forEach((button) => button.addEventListener("click", () => switchView(button.dataset.view)));
  $$("[data-product-nav]").forEach(button=>button.addEventListener("click",()=>{setProjectTab(button.dataset.productNav);switchView("projects");}));
  $("#language-select").addEventListener("change", (event) => { i18n.setLanguage(event.target.value); render(); readHealth(); });
  $("#refresh").addEventListener("click", () => loadAll(true));
  $("#settings-open").addEventListener("click",()=>$("#settings-dialog").showModal());
  $("#settings-close").addEventListener("click",()=>$("#settings-dialog").close());
  $("#settings-repository").addEventListener("click",()=>{$("#settings-dialog").close();setProjectTab("resources");state.resourceTab="repositories";render();});
  $("#settings-dialog").addEventListener("click",event=>{if(event.target===$("#settings-dialog")){event.preventDefault();event.stopPropagation();$("#settings-dialog").close();}});
  $("#detail-close").addEventListener("click", closeDetail);
  $("#detail-backdrop").addEventListener("click", closeDetail);
  $("#retire-cancel").addEventListener("click",()=>retire.cancel());
  $("#retire-confirm").addEventListener("click",()=>retire.confirm());
  $("#retire-dialog").addEventListener("cancel",event=>{event.preventDefault();retire.cancel();});
  $("#retire-dialog").addEventListener("keydown",event=>{
    if(event.key!=="Tab")return;
    const dialog=event.currentTarget,focusable=[...dialog.querySelectorAll('button:not(:disabled),[href],input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex="0"]')];
    const first=focusable[0],last=focusable[focusable.length-1];
    if(event.shiftKey&&(document.activeElement===first||!dialog.contains(document.activeElement))){event.preventDefault();last?.focus();}
    else if(!event.shiftKey&&(document.activeElement===last||!dialog.contains(document.activeElement))){event.preventDefault();first?.focus();}
  });
  $("#result-modal-close").addEventListener("click", closeResultModal);
  $("#result-modal-backdrop").addEventListener("click", (event) => { event.stopPropagation(); closeResultModal(); });
  $("#detail").addEventListener("click", (event) => {
    const approveStartRefresh=event.target.closest("[data-task-approve-start-refresh]");if(approveStartRefresh&&$("#detail").contains(approveStartRefresh)){refreshLocalApproveAndStartEvidence(openedTask,approveStartRefresh);return;}
    const session=event.target.closest("[data-open-run-session]");if(session){openRunSession(session.dataset.openRunSession);return;}
    const revision=event.target.closest("[data-definition-revision]");if(revision){readDefinitionRevision(revision.dataset.definitionRevision);return;}
    const areaPage=event.target.closest("[data-area-task-page]");if(areaPage){loadAreaTasks(areaPage.dataset.areaId,Number(areaPage.dataset.areaTaskPage));return;}
    const tab=event.target.closest("[data-task-tab]");if(tab){saveTaskState();taskActionNavigation++;detailRoute.tab(tab.dataset.taskTab);writeDetailLink();if(openedTask)paintTaskDetail(openedTask);return;}
    const run=event.target.closest("[data-task-run]");if(run){openTaskRun(run.dataset.taskRun,run.dataset.runOpenTab||"overview");return;}
    if(event.target.closest("[data-run-back]")){returnToTask();return;}
    const runTab=event.target.closest("[data-run-tab]");if(runTab){detailRoute.tab(runTab.dataset.runTab);writeDetailLink();paintRunDetail();if(runTab.dataset.runTab==="instructions"&&!runPromptSnapshot)readRunPrompt();return;}
    if(event.target.closest("[data-run-retry]")){if(detailRoute.get()?.activeTab==="instructions")readRunPrompt();else readTaskRun();return;}
    if(event.target.closest("[data-task-refresh]")){detailReadError="";workRefresh.refresh("INTERACTIVE");return;}
    const page=event.target.closest("[data-task-runs-page]");if(page){readTaskRuns(Number(page.dataset.taskRunsPage));return;}

    const feature=event.target.closest("[data-topic-detail]");if(feature){openTopicDetail(feature.dataset.topicDetail);return;}
    const commit=event.target.closest("[data-menu-commit]");if(commit){areaAction(commit.dataset.menuCommit,"COMMIT_FEATURE_AREA",{},commit);return;}
    const activity=event.target.closest("[data-agent-activity]");if(activity){toggleActivity(activity);return;}
    const cancel=event.target.closest("[data-execution-cancel]");if(cancel){event.stopPropagation();cancelExecution(cancel);return;}
    const parent=event.target.closest("[data-parent-task]");if(parent){openParentTask(parent.dataset.parentTask);return;}
    const execution=event.target.closest("[data-execution-detail]");if(execution){openExecutionDetail(execution.dataset.executionDetail);return;}
    const documentDetail = event.target.closest("[data-document-detail]");
    if (documentDetail) { openDocumentDetail(documentDetail.dataset.documentDetail); return; }
    const prompt = event.target.closest("[data-view-review-prompt]");
    if (prompt) { viewReviewPrompt(prompt.dataset.viewReviewPrompt); return; }
    const runPrompt = event.target.closest("[data-view-run-prompt]");
    if (runPrompt) { if(detailRoute.get()?.rootTask){openTaskRun(runPrompt.dataset.viewRunPrompt).then(()=>{if(detailRoute.get()?.view==="RUN"){detailRoute.tab("instructions");paintRunDetail();readRunPrompt();}});}else viewRunPrompt(runPrompt.dataset.viewRunPrompt); return; }
    const probePrompt = event.target.closest("[data-view-probe-prompt]");
    if (probePrompt) { viewContextProbePrompt(probePrompt.dataset.viewProbePrompt); return; }
    const projectOption = event.target.closest("[data-select-project]");
    if (projectOption) { state.projectDropdownOpen = false; selectProject(projectOption.dataset.selectProject); return; }
    const reviewDetail = event.target.closest("[data-review-detail]");
    if (reviewDetail) { openReviewDetail(reviewDetail.dataset.taskId, reviewDetail.dataset.reviewDetail); return; }
    const reviewPrompts = event.target.closest("[data-review-prompts]");
    if (reviewPrompts) { openReviewPrompts(reviewPrompts.dataset.reviewPrompts); return; }
    const taskDetail = event.target.closest("[data-task-detail]");
    if (taskDetail) { openTaskLink(taskDetail); return; }
    const closeFeatureAreaAction = event.target.closest("[data-close-feature-area]");
    if (closeFeatureAreaAction) { closeFeatureArea(closeFeatureAreaAction.dataset.closeFeatureArea); return; }
    if (event.target.closest("[data-copy-review-prompt]")) navigator.clipboard.writeText(state.reviewPrompt).then(() => toast(t("action.copied"))).catch(() => toast(t("error.load.body"), true));
  });
  document.addEventListener("keydown", (event) => { if (event.key !== "Escape"||$("#retire-dialog").open||$("#settings-dialog").open) return;event.preventDefault();const popover=$("[data-action-popover][open],.feature-menu[open]");if(!$("#result-modal").classList.contains("hidden"))closeResultModal();else if(popover){popover.open=false;popover.querySelector("summary")?.focus();}else if(state.projectDropdownOpen){state.projectDropdownOpen=false;render();$("[data-project-trigger]")?.focus();}else if(detailRoute.get()?.view==="RUN")returnToTask();else closeDetail(); });
  $("#detail").addEventListener("change",event=>{if(event.target.matches("[data-area-task-filter]")){state.areaTaskFilters[event.target.dataset.areaId]=event.target.value;loadAreaTasks(event.target.dataset.areaId);return;}if(event.target.matches("[data-run-filter]")){saveTaskState();detailRoute.get().runFilter=event.target.value;readTaskRuns(0);}});
  document.addEventListener("click",event=>{for(const popover of $$("[data-action-popover][open],.feature-menu[open]")){if(!popover.contains(event.target)){event.preventDefault();event.stopImmediatePropagation();popover.open=false;popover.querySelector("summary")?.focus();}}},true);
  document.addEventListener("click",event=>{
    const session=event.target.closest("#page [data-open-run-session]");if(session){openRunSession(session.dataset.openRunSession);return;}
    const preview=event.target.closest("[data-view-full-result]");
    if(preview){showResultModal(JSON.parse(preview.dataset.resultContent),preview.dataset.resultTitle||t("result.full"),preview);return;}
    const review=event.target.closest("[data-view-full-review-result]");
    if(review)openFullReviewResult(review.dataset.viewFullReviewResult,review);
  });
  // Keep focus inside while allowing keyboard scrolling of the full content.
  document.addEventListener("keydown",event=>{if(event.key==="Tab"&&!$("#result-modal").classList.contains("hidden")){event.preventDefault();(document.activeElement===$("#result-modal-close")?$("#result-modal-body"):$("#result-modal-close")).focus();}});
  window.addEventListener("pagehide",()=>{window.clearTimeout(runReadTimer);saveTaskState();detailRoute.close();workRefresh.stop();});
  document.addEventListener("click", (event) => { if (state.projectDropdownOpen && !event.target.closest("[data-project-trigger], [data-project-dropdown]")) {event.preventDefault();event.stopImmediatePropagation();state.projectDropdownOpen = false; render();$("[data-project-trigger]")?.focus();} },true);
  i18n.applyDocument(document);
  $("#language-select").value = i18n.requestedLanguage;
  document.addEventListener("visibilitychange",()=>{workRefresh.visibility();window.clearTimeout(runReadTimer);if(detailRoute.get()?.view==="RUN"){if(document.hidden){detailRoute.read().signal;detailReadBusy=false;updateDetailReadState();}else scheduleRunRead();}if(document.hidden)window.clearTimeout(gitAreaTimer);else if(workflowDrawerTarget)scheduleGitArea(workflowDrawerTarget);});
  window.addEventListener("hashchange",restoreDetailLink);
  loadAll().then(restoreDetailLink);
})();
