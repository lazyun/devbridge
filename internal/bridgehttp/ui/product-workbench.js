"use strict";

// Stateless project/area presentation. The application owns navigation, requests,
// snapshots and drafts. These helpers never infer or mutate workflow authority.
(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  root.DevBridgeWorkbench = api;
})(typeof window !== "undefined" ? window : globalThis, function () {
  const projectTabs = ["overview", "featureAreas", "tasks", "sessions", "resources", "diagnostics"];
  const resourceTabs = ["documents", "modules", "repositories"];
  const diagnosticTabs = ["runs", "runtime", "logs", "workspaces"];
  const terminal = task => ["SUCCEEDED", "FAILED", "CANCELED"].includes(task.status);
  const list = value => Array.isArray(value) ? value : [];
  function normalizeNavigation(view, tab) {
    if (view === "logs") return {view: "projects", projectTab: "diagnostics", diagnosticTab: "logs", resourceTab: "documents"};
    if (view === "diagnostics" || tab === "workspaces" || tab === "executions" || tab === "runs") return {view: "projects", projectTab: "diagnostics", diagnosticTab: tab === "workspaces" ? "workspaces" : "runs", resourceTab: "documents"};
    return {view: "projects", projectTab: ["documents", "modules", "repositories"].includes(tab) ? "resources" : projectTabs.includes(tab) ? tab : "overview", resourceTab: resourceTabs.includes(tab) ? tab : "documents", diagnosticTab: "runs"};
  }
  function taskView(task) {
    const p = task.presentation || {};
    return [p.taskView, p.latestTaskView].find(value => value && value.id === task.id) || task;
  }
  function activity(task) { return task.nextStep?.taskId===task.id ? {summary:task.nextStep.summary} : taskView(task).activity || task.currentActivity || {}; }
  function featureAreaStatus(area, tasks) {
    const p=area?.product||{},fact=p.currentTask;
    const listed=fact?.id?list(tasks).find(task=>task.id===fact.id&&task.feature_area_id===area.id):null;
    const current=listed||(fact?.id?{id:fact.id,title:fact.id,phase:fact.phase,status:fact.status}:null);
    const phase=current?p.taskPhase||"":"";
    return {current,phase:listed&&listed.phase===phase?(listed.phaseLabel||phase):phase,activity:current?(current.nextStep?.summary||p.operationActivity?.message||p.operationActivity?.state||""):""};
  }
  function normalizeTaskFilters(workspace,filters={}) {
    const next={...filters},areas=list(workspace?.featureAreas),branches=new Set(list(workspace?.tasks).flatMap(task=>list(task.development_branches)));
    if(next.area&&!areas.some(area=>area.id===next.area))next.area="";
    if(next.branch&&next.branch!=="__unknown__"&&workspace?.tasks_truncated!==true&&!branches.has(next.branch))next.branch="";
    return next;
  }
  function taskPage(tasks, filters = {}, page = 0, pageSize = 25) {
    const size = Math.max(1, Math.min(100, Number(pageSize) || 25));
    const needle = String(filters.search || "").trim().toLocaleLowerCase();
    const filtered = list(tasks).filter(task => task.id &&
      (!needle || String(task.title || "").toLocaleLowerCase().includes(needle)) &&
      (!filters.area || task.feature_area_id === filters.area) &&
      (!filters.status || task.status === filters.status) &&
      (!filters.phase || task.phase === filters.phase || task.businessPhase === filters.phase) &&
      (!filters.branch || (filters.branch === "__unknown__" ? !list(task.development_branches).length : list(task.development_branches).includes(filters.branch))) &&
      (!filters.kind || filters.kind === "all" || (filters.kind === "unclassified" ? !task.work_kind : task.work_kind === filters.kind)));
    const count = Math.max(1, Math.ceil(filtered.length / size));
    const current = Math.max(0, Math.min(count - 1, Math.floor(Number(page) || 0)));
    return {items: filtered.slice(current * size, (current + 1) * size), total: filtered.length, page: current, pages: count, pageSize: size};
  }
  function empty(ctx, key = "workbench.empty") { return `<p class="compact-empty" role="status">${ctx.C.esc(ctx.t(key))}</p>`; }
  function section(ctx, key, content, attrs = "") { return `<section class="workbench-section" ${attrs}><h3>${ctx.C.esc(ctx.t(key))}</h3>${content || empty(ctx)}</section>`; }
  function taskLink(ctx, task, label, tab) {
    return `<button class="text-button" type="button" data-task-detail="${ctx.C.esc(task.id)}"${tab ? ` data-task-open-tab="${ctx.C.esc(tab)}"` : ""}>${ctx.C.esc(label || task.title || ctx.t("common.unknown"))}</button>`;
  }
  function renderTaskRows(ctx, tasks, {showArea=true}={}) {
    const {C, t, locale} = ctx;
    if (!tasks.length) return empty(ctx, "tasks.empty.title");
    return `<div class="table-wrap"><table class="workbench-task-table"><thead><tr>${["tasks.columnTask", ...(showArea?["tasks.topic"]:[]), "workbench.phase", "workbench.status", "workbench.activity", "common.updated"].map(key => `<th scope="col">${C.esc(t(key))}</th>`).join("")}</tr></thead><tbody>${tasks.map(task => `<tr><td>${taskLink(ctx, task)}</td>${showArea?`<td>${C.esc(task.feature_area_title || list(ctx.state.projectWorkspace?.featureAreas).find(area => area.id === task.feature_area_id)?.title || t("common.unknown"))}</td>`:""}<td title="${C.esc(task.phase || "")}">${C.esc(C.taskPhaseLabel(task,t))}</td><td>${C.taskStatusBadge(task,t)}</td><td>${C.esc(activity(task).summary || activity(task).status || "—")}</td><td>${C.localizedTimestamp(task.updated_at || task.updatedAt, locale, "—")}</td></tr>`).join("")}</tbody></table></div>`;
  }
  function renderTasks(ctx) {
    const {state, C, t} = ctx, tasks = list(state.projectWorkspace?.tasks);
    const filters = {...state.taskFilters, branch: state.workItemBranchFilter, kind: state.taskKindFilter};
    const remote = state.taskPageResult;
    const result = remote ? {items:list(remote.items),total:remote.total,page:Math.floor(remote.offset / (remote.limit || 25)),pages:Math.max(1,Math.ceil(remote.total / (remote.limit || 25)))} : taskPage(tasks, filters, state.taskPage);
    const options = (values, selected) => {if(selected&&selected!=="all"&&!values.some(value=>value.value===selected))values=[...values,{value:selected,label:selected}];return `<option value="">${C.esc(t("tasks.filterAll"))}</option>${values.map(value => `<option value="${C.esc(value.value)}"${selected === value.value ? " selected" : ""}>${C.esc(value.label)}</option>`).join("")}`;};
    const select = (name, key, values) => `<label class="inline-filter">${C.esc(t(key))}<select data-workbench-filter="${name}">${options(values, filters[name])}</select></label>`;
    const values = key => (key === "status" ? ["ACTIVE","SUCCEEDED","FAILED","CANCELED"] : ["REVIEW_REQUIRED","REVIEWING","REVIEW_APPROVAL_PENDING","IMPLEMENTATION_READY","NO_RUN_SUCCESSOR_REQUIRED","IMPLEMENTATION_PREPARING","IMPLEMENTING","VERIFICATION_READY","VERIFYING","ACCEPTANCE_PENDING","DELIVERING","RECOVERY_REQUIRED"]).map(value => ({value, label:key === "status" ? C.statusText("task",value,t) : C.taskPhaseLabel({phase:value},t)}));
    const branches = [...new Set(tasks.flatMap(task => list(task.development_branches)))].sort();
    const extra = `<label class="inline-filter">${C.esc(t("branches.development"))}<select id="work-item-branch-filter">${options([...branches.map(value => ({value,label:value})),{value:"__unknown__",label:t("branches.unknown")}], state.workItemBranchFilter)}</select></label><label class="inline-filter">${C.esc(t("tasks.kindFilter"))}<select id="task-kind-filter">${options(["unclassified","FEATURE","BUG_FIX","REFACTOR","MAINTENANCE","TEST_CHANGE","DOCUMENTATION","OTHER"].map(value=>({value,label:t(`workKind.${value === "unclassified" ? "UNCLASSIFIED" : value}`)})), state.taskKindFilter)}</select></label>`;
    return `${state.taskListError ? `<p role="alert" class="detail-read-state">${C.esc(t("workbench.readFailed"))}: ${C.esc(state.taskListError)} <button type="button" class="text-button" data-task-list-retry>${C.esc(t("workbench.retry"))}</button></p>` : ""}${state.taskFilterNotice?`<p role="status">${C.esc(t("workbench.filtersReset"))}</p>`:""}<div class="workbench-task-list" aria-busy="${!!state.taskListLoading}"><div class="task-filters workbench-filters"><label class="inline-filter">${C.esc(t("workbench.search"))}<input type="search" data-workbench-filter="search" value="${C.esc(filters.search || "")}" autocomplete="off"></label>${select("area", "tasks.topic", list(state.projectWorkspace?.featureAreas).map(area => ({value: area.id, label: area.title})))}${select("status", "workbench.status", values("status"))}${select("phase", "workbench.phase", values("phase"))}${extra}<button type="button" class="text-button" data-task-filters-clear>${C.esc(t("workbench.resetFilters"))}</button></div><div${state.taskListLoading ? ' inert aria-busy="true"' : ""}>${state.taskListLoading ? `<p role="status">${C.esc(t("common.loading"))}</p>` : ""}${result.items.length?renderTaskRows(ctx, result.items):empty(ctx,"workbench.noMatches")}<nav class="workbench-pagination" aria-label="${C.esc(t("workbench.pagination"))}"><button type="button" class="button" data-workbench-page="${result.page - 1}"${result.page === 0 ? " disabled" : ""}>${C.esc(t("workbench.previous"))}</button><span role="status">${C.esc(t("workbench.page", {page:result.page + 1, pages:result.pages, total:result.total}))}</span><button type="button" class="button" data-workbench-page="${result.page + 1}"${result.page + 1 >= result.pages ? " disabled" : ""}>${C.esc(t("workbench.next"))}</button></nav></div></div>`;
  }
  function overviewFacts(workspace, approvals = [], runs = []) {
    const decisions = [], requests = [], blockers = [], tasks = list(workspace?.tasks), seenRequests = new Set();
    tasks.forEach(task => {
      const p = task.presentation || {}, v = taskView(task), decision = p.pendingDecision;
      if (decision && ["REVIEW_APPROVAL", "TASK_ACCEPTANCE"].includes(decision.kind) && p.details?.taskId === task.id) decisions.push({task, kind: decision.kind, summary: decision.summary, id: decision.id});
      else if (task.status === "ACTIVE" && ["REVIEW_APPROVAL_PENDING", "ACCEPTANCE_PENDING"].includes(task.phase)) decisions.push({task,kind:task.phase === "REVIEW_APPROVAL_PENDING" ? "REVIEW_APPROVAL" : "TASK_ACCEPTANCE",summary:task.currentAction?.message || task.phaseDescription});
      const runtime = v.activity?.runtime;
      list(runtime?.requests).filter(request => request.state === "PENDING" && request.taskId === task.id && request.runId === runtime.runId).forEach(request => {
        const key = JSON.stringify([request.runId,request.requestId]);
        if (!seenRequests.has(key)) {seenRequests.add(key); requests.push({task, runId:request.runId, summary:request.method});}
      });
      const messages = [...list(p.blockers)];
      if (v.recovery?.required&&!task.nextStep) messages.push(v.recovery.summary || v.recovery.status);
      if (task.commitActivity === "FAILED") messages.push(task.commitErrorSummary || task.commitSummary || "GIT_FINALIZATION_FAILED");
      if (["FAILED", "UNKNOWN_ORPHANED", "INTERRUPTED", "INFRASTRUCTURE_FAILED"].includes(activity(task).status)) messages.push(activity(task).summary || activity(task).status);
      const review = v.currentReview;
      if (task.phase === "REVIEWING" && ["FAILED", "CANCELED"].includes(review?.run?.transportStatus)) messages.push(review.failureSummary || "REVIEW_RUN_INTERRUPTED");
      if (messages.length) blockers.push({task, summary:[...new Set(messages.filter(Boolean))].join(" · ")});
    });
    list(approvals).filter(approval => approval.status === "PENDING" && approval.run_id).forEach(approval => {
      if (!requests.some(request => request.runId === approval.run_id)) requests.push({task:tasks.find(task=>task.id===approval.task_id) || (approval.task_id ? {id:approval.task_id,title:""} : null),runId:approval.run_id,summary:approval.kind});
    });
    list(runs).forEach(run => {
      const runtime=run.agent_runtime;
      list(runtime?.requests).filter(request=>request.state==="PENDING" && request.runId===run.id && runtime.runId===run.id).forEach(request=>{
        const key=JSON.stringify([run.id,request.requestId]);
        if (!seenRequests.has(key) && !requests.some(item=>item.runId===run.id)) {
          seenRequests.add(key);
          requests.push({task:request.taskId && request.taskId===run.task_id ? tasks.find(task=>task.id===request.taskId) || {id:request.taskId,title:""} : null,runId:run.id,summary:request.method});
        }
      });
    });
    // Inspection without a Task remains an operation with a Run target.
    list(workspace?.featureAreas).forEach(area => {
      const p = area.presentation || {}, d = p.pendingDecision;
      if (d && !["REVIEW_APPROVAL", "TASK_ACCEPTANCE"].includes(d.kind) && p.currentOperation?.runId) {
        const owner = tasks.find(task => task.id === p.details?.taskId);
        if (!requests.some(request => request.runId === p.currentOperation.runId)) requests.push({task:owner, runId:p.currentOperation.runId, summary:d.summary || d.kind});
      }
      if (!p.details?.taskId && list(p.blockers).length) blockers.push({area, runId:p.currentOperation?.runId, summary:p.blockers.join(" · ")});
    });
    return {decisions, requests, blockers, active:tasks.filter(task => !terminal(task)), delivered:tasks.filter(task => task.status === "SUCCEEDED").slice().sort((a,b) => String(b.updated_at).localeCompare(String(a.updated_at)) || a.id.localeCompare(b.id)).slice(0,10)};
  }
  function renderOverview(ctx) {
    const {state,C,t} = ctx, facts = overviewFacts(state.projectWorkspace,state.approvals,state.runs);
    const rows = (items, kind) => items.length ? `<ul class="workbench-action-list">${items.slice(0,20).map(item => `<li><div><strong>${C.esc(item.task ? item.task.title || t("workbench.viewTask") : item.area?.title || t("workbench.inspection"))}</strong>${C.resultPreview(item.summary,t)}</div>${item.task ? taskLink(ctx, item.task, t(kind === "decision" ? item.kind === "REVIEW_APPROVAL" ? "workbench.viewPlan" : "workbench.viewResult" : kind === "request" ? "workbench.viewRequest" : "workbench.viewTask"), kind === "decision" ? item.kind === "REVIEW_APPROVAL" ? "reviews" : "executions" : "overview") : item.runId ? `<button type="button" class="text-button" data-diagnostic-run="${C.esc(item.runId)}">${C.esc(t("workbench.viewRequest"))}</button>` : `<button type="button" class="text-button" data-topic-detail="${C.esc(item.area.id)}">${C.esc(t("workbench.viewArea"))}</button>`}</li>`).join("")}</ul>` : empty(ctx);
    const dataNotice = state.projectDataErrors ? Object.entries(state.projectDataErrors).map(([key,error]) => `<p class="detail-read-state" role="status">${C.esc(t("workbench.partial"))} · ${C.esc(key)}: ${C.esc(error)}</p>`).join("") : "";
    return `${dataNotice}${state.projectWorkspace?.tasks_truncated ? `<p class="detail-read-state">${C.esc(t("workbench.truncated",{total:state.projectWorkspace.tasks_total}))} <button type="button" class="text-button" data-project-tab="tasks">${C.esc(t("action.viewAll"))}</button></p>` : ""}<div class="overview-sections">${section(ctx,"workbench.needsAttention",section(ctx,"workbench.businessDecisions",rows(facts.decisions,"decision"))+section(ctx,"workbench.runtimeRequests",rows(facts.requests,"request")),'data-workbench-priority="attention"')}${section(ctx,"workbench.blockers",rows(facts.blockers,"blocker"),'data-workbench-priority="blockers"')}${section(ctx,"workbench.activeTasks",renderTaskRows(ctx,facts.active.slice(0,10)),'data-workbench-priority="active"')}${section(ctx,"workbench.delivered",facts.delivered.length ? `<div class="table-wrap"><table><thead><tr>${["tasks.columnTask","workbench.acceptance","workbench.gitCommit","workbench.integration"].map(key=>`<th scope="col">${C.esc(t(key))}</th>`).join("")}</tr></thead><tbody>${facts.delivered.map(task=>`<tr><td>${taskLink(ctx,task)}</td><td>${C.esc(t("workbench.accepted"))}</td><td>${C.esc(task.commitSummary || task.commitActivity || task.commitStatus || t("workbench.unknown"))}${task.commitSha&&!task.commitSummary?` <code>${C.esc(task.commitSha.slice(0,12))}</code>`:""}</td><td>${C.esc(task.devSummary || task.devIntegrationStatus || t("workbench.unknown"))}${task.mergedCommit&&!task.devSummary?` <code>${C.esc(task.mergedCommit.slice(0,12))}</code>`:""}</td></tr>`).join("")}</tbody></table></div>` : "",'data-workbench-priority="delivered"')}</div>`;
  }
  function renderFeatureAreas(ctx) {
    const {state,C,t}=ctx, all=list(state.projectWorkspace?.featureAreas), filter=state.featureAreaStatusFilter || "active";
    const areas=all.filter(area=>filter==="all" || C.featureAreaProductStatus(area)===filter.toUpperCase());
    return `<label class="inline-filter">${C.esc(t("topics.statusFilter"))}<select id="feature-area-status-filter">${["active","closed","all"].map(value=>`<option value="${value}"${filter===value?" selected":""}>${C.esc(t({active:"topics.filterActive",closed:"topics.filterClosed",all:"topics.filterAll"}[value]))}</option>`).join("")}</select></label>${areas.length?`<div class="table-wrap"><table><thead><tr>${["tasks.topic","workbench.lifecycle","workbench.currentTask","workbench.phase","workbench.activity","workbench.needsAttention"].map(key=>`<th scope="col">${C.esc(t(key))}</th>`).join("")}</tr></thead><tbody>${areas.map(area=>{const p=area.presentation||{},status=featureAreaStatus(area,state.projectWorkspace?.tasks);return `<tr><td><button type="button" class="text-button" data-topic-detail="${C.esc(area.id)}">${C.esc(area.title)}</button></td><td>${C.featureAreaStatusBadge(area,t)}</td><td>${status.current?taskLink(ctx,status.current):"—"}</td><td>${C.esc(status.current ? C.taskPhaseLabel(status.current,t) : "—")}</td><td>${C.esc(status.activity||"—")}</td><td>${C.resultPreview(p.pendingDecision?.summary || list(p.blockers).join(" · "),t) || "—"}</td></tr>`;}).join("")}</tbody></table></div>`:empty(ctx,"topics.empty.title")}`;
  }
  function renderFeatureAreaDetail(ctx, area) {
    const {state,C,t,legacy={}}=ctx,filter=state.areaTaskFilters?.[area.id] || "active",all=list(state.projectWorkspace?.tasks).filter(task=>task.feature_area_id===area.id),page=state.areaTaskPages?.[area.id],tasks=page ? list(page.items) : all.filter(task=>filter==="all" || (filter==="completed" ? terminal(task) : !terminal(task)));
    const pager = page && Number.isFinite(page.offset) && Number.isFinite(page.total) ? `<nav class="workbench-pagination" aria-label="${C.esc(t("workbench.pagination"))}"><button type="button" class="button" data-area-id="${C.esc(area.id)}" data-area-task-page="${Math.floor(page.offset/(page.limit||25))-1}"${page.offset===0?" disabled":""}>${C.esc(t("workbench.previous"))}</button><span>${C.esc(t("workbench.page",{page:Math.floor(page.offset/(page.limit||25))+1,pages:Math.max(1,Math.ceil(page.total/(page.limit||25))),total:page.total}))}</span><button type="button" class="button" data-area-id="${C.esc(area.id)}" data-area-task-page="${Math.floor(page.offset/(page.limit||25))+1}"${page.offset+(page.limit||25)>=page.total?" disabled":""}>${C.esc(t("workbench.next"))}</button></nav>` : "";
    return `<header class="feature-heading">${C.featureAreaStatusBadge(area,t)}${legacy.featureAreaMenu?.(area)||""}<p>${C.esc(area.summary||"")}</p></header>${legacy.areaTaskPanel?.(area)||""}${section(ctx,"tasks.title",`<label class="inline-filter">${C.esc(t("tasks.filter"))}<select data-area-task-filter data-area-id="${C.esc(area.id)}">${["active","all","completed"].map(value=>`<option value="${value}"${filter===value?" selected":""}>${C.esc(t({active:"tasks.filterActive",all:"tasks.filterAll",completed:"tasks.filterCompleted"}[value]))}</option>`).join("")}</select></label>${page?.error?`<p role="alert">${C.esc(t("workbench.readFailed"))}: ${C.esc(page.error)} <button type="button" data-area-id="${C.esc(area.id)}" data-area-task-page="0">${C.esc(t("workbench.retry"))}</button></p>`:""}${page?.loading?`<p role="status">${C.esc(t("common.loading"))}</p>`:""}<div${page?.loading?' inert aria-busy="true"':""}>${tasks.length?renderTaskRows(ctx,tasks,{showArea:false}):empty(ctx,filter==="active"?"workbench.noActiveTasks":"tasks.empty.title")}${pager}</div>`)}<details class="workbench-section" data-section-key="area-context"><summary>${C.esc(t("workbench.areaContext"))}</summary>${legacy.areaTaskPanel?.(area,true)||""}${C.keyValueDetails([[t("workbench.workspace"),`<code>${C.esc(area.current_workspace_id || area.presentation?.currentWorkspaceId || t("workbench.notGenerated"))}</code>`],[t("workbench.conversation"),`<code>${C.esc(area.agent_conversation_id || area.presentation?.agentOperation?.conversationId || t("workbench.notGenerated"))}</code>`]])}</details>${section(ctx,"workbench.delivery",`${legacy.areaCommitPanel?.(area)||empty(ctx)}${area.presentation?.canCommit?`<button class="button" type="button" data-menu-commit="${C.esc(area.id)}" data-request-id="${C.esc(area.presentation.commit?.requestId||"")}">${C.esc(area.presentation.commitView?.actionLabel || t("workbench.integration"))}</button>`:""}`)}<details class="workbench-section" data-section-key="area-documents"><summary>${C.esc(t("topicDetail.documents"))}</summary>${legacy.documentRows?.(list(state.projectWorkspace?.documents).filter(doc=>doc.feature_area_id===area.id))||empty(ctx)}</details>`;
  }
  function renderProjectContext(ctx) {
    const {state,C,t}=ctx, repos=list(state.developmentContext?.repositories);
    return `<div class="workbench-repositories">${repos.length?repos.map(repo=>`<details><summary><strong>${C.esc(repo.repositoryName)}</strong> · ${C.esc(t("branches.development"))}: <code>${C.esc(repo.developmentRef||t("workbench.unknown"))}</code></summary>${C.keyValueDetails([[t("workbench.currentHead"),`<code>${C.esc(repo.currentHeadSha||t("workbench.unknown"))}</code>`],[t("workbench.selectedRevision"),`<code>${C.esc(repo.selectedHeadSha||t("workbench.unknown"))}</code>`],[t("workbench.checkout"),`<code>${C.esc(repo.repositoryCheckoutRef||t("workbench.unknown"))}</code>`]])}</details>`).join(""):state.developmentError?`<p role="status">${C.esc(t("workbench.readFailed"))}: ${C.esc(state.developmentError)}</p>`:list(state.repositories).map(repo=>`<p><strong>${C.esc(repo.name)}</strong> · ${C.esc(t("branches.development"))}: ${C.esc(t("workbench.unknown"))}</p>`).join("")}</div>`;
  }
  function subnav(ctx,tabs,selected,attribute,prefix) {return `<nav class="subnav workbench-secondary-nav" aria-label="${ctx.C.esc(ctx.t(`project.${prefix}`))}">${tabs.map(tab=>`<button type="button" ${attribute}="${tab}" class="${selected===tab?"active":""}" aria-current="${selected===tab?"page":"false"}">${ctx.C.esc(ctx.t(`workbench.${tab}`))}</button>`).join("")}</nav>`;}
  function renderResources(ctx) {
    const tab=resourceTabs.includes(ctx.state.resourceTab)?ctx.state.resourceTab:"documents";
    return subnav(ctx,resourceTabs,tab,"data-resource-tab","resources")+(tab==="repositories"?renderProjectContext(ctx)+(ctx.legacy?.repositoryDetails?.()||""):ctx.legacy?.[tab]?.()||empty(ctx));
  }
  function renderDiagnostics(ctx) {
    const {state,C,t}=ctx,tab=diagnosticTabs.includes(state.diagnosticTab)?state.diagnosticTab:"runs";
    let body="";
    if(tab==="runs")body=list(state.runs).length?`<div class="table-wrap"><table><thead><tr>${["diagnostics.run","run.purpose","workbench.status","workbench.owner"].map(key=>`<th scope="col">${C.esc(t(key))}</th>`).join("")}</tr></thead><tbody>${state.runs.slice(0,100).map(run=>`<tr><td><button class="text-button mono" type="button" data-diagnostic-run="${C.esc(run.id)}">${C.esc(run.id)}</button></td><td><code>${C.esc(run.purpose)}</code></td><td>${C.esc(run.status)}</td><td>${run.task_id?`<code>${C.esc(run.task_id)}</code>`:C.esc(t("workbench.inspection"))}</td></tr>`).join("")}</tbody></table></div>`:empty(ctx,"workbench.emptyRuns");
    else body=ctx.legacy?.[tab==="runtime"?"diagnostics":tab]?.()||empty(ctx);
    return subnav(ctx,diagnosticTabs,tab,"data-diagnostic-tab","diagnostics")+`<p class="muted">${C.esc(t("workbench.diagnosticsReadonly"))}</p>`+body;
  }
  return {projectTabs,resourceTabs,diagnosticTabs,normalizeNavigation,normalizeTaskFilters,taskPage,taskView,featureAreaStatus,overviewFacts,renderTaskRows,renderTasks,renderOverview,renderFeatureAreas,renderFeatureAreaDetail,renderProjectContext,renderResources,renderDiagnostics};
});
