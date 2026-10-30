"use strict";

// A local view of Core-owned Runs. Viewing, reconnecting and closing only read
// records and manage this client's HTTP subscription; they never start a Run.
(function (root) {
  const esc = root.DevBridgeComponents.esc;
  const list = value => Array.isArray(value) ? value : [];
  const terminal = value => ["SUCCEEDED", "FAILED", "INFRASTRUCTURE_FAILED", "CANCELED"].includes(value);
  const text = value => String(value ?? "").replace(/\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|$))/g, "").replace(/[\x00-\x08\x0b-\x1f\x7f]/g, "");
  const pre = value => `<pre>${esc(text(value))}</pre>`;
  const facts = rows => `<dl class="session-facts">${rows.map(([key, value]) => `<dt>${esc(key)}</dt><dd>${esc(value ?? "UNAVAILABLE")}</dd>`).join("")}</dl>`;
  // Deliberately small Markdown subset: escaped text, headings, lists and fenced
  // code. Links/images/raw HTML stay inert text; no remote content is fetched.
  function markdown(value) {
    let code = false;
    return text(value).split("\n").map(line => {
      if (/^```/.test(line)) {code = !code; return code ? "<pre><code>" : "</code></pre>";}
      if (code) return esc(line) + "\n";
      if (/^#{1,6}\s/.test(line)) return `<p class="session-message-heading">${esc(line.replace(/^#{1,6}\s/, ""))}</p>`;
      return `<p>${esc(line).replace(/`([^`]+)`/g, "<code>$1</code>").replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>") || "<br>"}</p>`;
    }).join("") + (code ? "</code></pre>" : "");
  }
  const query = values => new URLSearchParams(Object.entries(values).filter(([, value]) => value !== "" && value !== undefined && value !== null)).toString();
  const itemState = value => ({inProgress:"进行中",completed:"已完成",failed:"失败",declined:"已拒绝",interrupted:"已中断",pending:"待开始"})[value] || "状态未记录";
  const itemLabel = value => ({agentMessage:"进展消息",commandExecution:"执行命令",fileChange:"修改文件",plan:"制定计划",mcpToolCall:"调用工具",dynamicToolCall:"调用工具",collabToolCall:"协作任务",webSearch:"检索资料",contextCompaction:"整理上下文",enteredReviewMode:"进入审查",exitedReviewMode:"结束审查",reasoning:"正在处理"})[value] || "处理任务";
  function metadata(item) {if (item?.metadata && typeof item.metadata === "object") return item.metadata; try {return JSON.parse(item?.metadata || "{}");} catch (_) {return {};}}
  function duration(value) {if (!Number.isFinite(value) || value < 0) return "未记录"; const seconds=Math.floor(value/1000);return seconds<60?`${seconds} 秒`:seconds<3600?`${Math.floor(seconds/60)} 分 ${seconds%60} 秒`:`${Math.floor(seconds/3600)} 小时 ${Math.floor(seconds%3600/60)} 分`;}
  // Native activity describes work observed so far, never workflow completion.
  // Plans are mutable and optional; do not turn their counts into a percentage.
  function nativeApprovalLabel(response) {
    const d=response?.decision;
    if(typeof d==='string')return ({accept:'允许本次',acceptForSession:'允许本会话',decline:'拒绝本次',cancel:'取消当前操作'})[d]||'';
    if(d?.acceptWithExecpolicyAmendment)return '允许并保存命令规则';
    if(d?.applyNetworkPolicyAmendment)return '应用所示网络规则';
    if(response?.permissions&&['turn','session'].includes(response.scope))return Object.keys(response.permissions).length?(response.scope==='turn'?'本回合授予所示权限':'本会话授予所示权限'):'拒绝请求的权限';
    return '';
  }
  function nativeApprovalResolution(request,runtime,index) {
    if(request?.kind!=='RUNTIME_APPROVAL'||request.state!=='PENDING'||runtime?.availability!=='AVAILABLE'||runtime.connectionState!=='CONNECTED'||!request.runtimeRequestId||!request.runtimeEpoch||!request.itemId||request.requestId===undefined||!/^[a-f0-9]{64}$/.test(request.requestPayloadSHA256||'')||!nativeApprovalLabel(request.availableResponses?.[index]))return null;
    const body={};
    for(const name of ['operationKind','operationId','taskDefinitionRevisionId','reviewId','executionId','agentConversationId','recoveryStatusId','runtimeEpoch','appServerInstanceId','appServerInstanceEpoch','projectId','featureAreaId','taskId','runId','threadId','turnId','connectionId'])if(request[name]!==undefined){if(runtime[name]!==request[name])return null;body[name]=request[name];}
    return {...body,runtimeRequestId:request.runtimeRequestId,requestId:request.requestId,itemId:request.itemId,expectedState:'PENDING',payloadSHA256:request.requestPayloadSHA256,response:request.availableResponses[index]};
  }
  function progressSummary(value, now=Date.now()) {
    const d=value?.detail||{},r=d.run||{},rt=value?.session?.runtime||d.runtime||{},ended=terminal(r.status),upper=r.executor_kind==="UPPER_LLM",reauthorization=upper&&!ended&&r.review_authorization_required===true;
    const records=new Map();[...list(rt.items),...list(value?.items?.items).map(row=>row.item||row)].forEach(item=>{if(item?.id){const prior=records.get(item.id);if(!prior||(item.revision||0)>=(prior.revision||0))records.set(item.id,item);}});
    const items=[...records.values()];
    const active=!upper&&!ended&&!["completed","failed","interrupted"].includes(rt.turnStatus)&&rt.availability==="AVAILABLE"&&rt.connectionState==="CONNECTED";
    const flags=active?list(rt.nativeThreadStatus?.activeFlags):[];
    const waiting=active?list(rt.requests).filter(request=>request.projection?.lifecycleState==="WAITING_FOR_USER"||request.projection?.responseDisposition==="UNKNOWN"||request.kind==="RUNTIME_APPROVAL"&&request.state==="PENDING"&&request.threadId===rt.threadId&&request.turnId===rt.turnId):[];
    const current=active?[...items].reverse().find(item=>item.status==="inProgress"&&!item.completedAt)||(!rt.currentItem?.completedAt&&rt.currentItem?.status==="inProgress"?rt.currentItem:null):null;
    const steps=list(rt.latestPlan?.steps),completed=steps.filter(step=>step.status==="completed").length,step=active?steps.find(step=>step.status==="inProgress"):null;
    const message=[...items].reverse().find(item=>item.type==="agentMessage"&&(item.text||metadata(item).text));
    const last=Date.parse(upper?d.review_evidence?.last_observed_at:rt.sequence>0?rt.lastActivityAt:"");
    const started=Date.parse(r.started_at||""),finished=Date.parse(r.finished_at||"");
    const elapsed=Number.isFinite(r.duration_ms)?r.duration_ms:ended?(Number.isFinite(d.elapsed_ms)?d.elapsed_ms:Number.isFinite(started)&&Number.isFinite(finished)?finished-started:null):Number.isFinite(started)?now-started:Number.isFinite(d.elapsed_ms)?d.elapsed_ms:null;
    let headline=ended?({SUCCEEDED:"本次运行已结束",CANCELED:"本次运行已取消",FAILED:"本次运行失败",INFRASTRUCTURE_FAILED:"运行环境中断"})[r.status]:reauthorization?"GPT 审查待重新授权":upper?(d.review_evidence_status==="AVAILABLE"&&!(d.review_evidence?.successful_operations>0)?"等待 GPT 读取文件":"等待 GPT 提交审查报告"):r.status==="WAITING_APPROVAL"?"等待运行授权":!active?"等待活动记录":flags.includes("waitingOnUserInput")?"等待你的回答":flags.includes("waitingOnApproval")?"等待运行授权":current?itemLabel(current.type):step?"按计划执行中":"等待下一项活动";
    if(!ended&&waiting.length)headline=waiting.some(q=>q.kind==="RUNTIME_APPROVAL")?"等待命令或文件权限批准":"需要你处理运行请求";
    if(!upper&&!ended&&["completed","failed","interrupted"].includes(rt.turnStatus))headline="等待 Core 记录运行结果";
    return {ended,upper,reauthorization,active,headline,current,steps,completed,step,message,waiting:waiting.length,elapsed,lastActivity:Number.isFinite(last)&&last>0?last:null,quiet:active&&!waiting.length&&!flags.includes("waitingOnApproval")&&Number.isFinite(last)&&now-last>180000,items};
  }
  function decisionEscapeJSON(value) {return JSON.stringify(value).replace(/</g,"\\u003c").replace(/>/g,"\\u003e").replace(/&/g,"\\u0026").replace(/\u2028/g,"\\u2028").replace(/\u2029/g,"\\u2029");}
  // This is the fixed Go UserDecisionInput field order. Hashing this exact
  // payload lets Core reject substituted answers without giving the browser
  // any authority to supply submittedBy.
  async function decisionPayloadSHA256(input) {
    const answers=(Array.isArray(input.answers)?input.answers:[]).map(answer=>({questionId:answer?.questionId||"",selectedOptionId:answer?.selectedOptionId||undefined,rawText:answer?.rawText||undefined}));
    const ordered={operationKind:input.operationKind||undefined,operationId:input.operationId||undefined,taskDefinitionRevisionId:input.taskDefinitionRevisionId||undefined,reviewId:input.reviewId||undefined,executionId:input.executionId||undefined,agentConversationId:input.agentConversationId||undefined,runtimeEpoch:input.runtimeEpoch||undefined,appServerInstanceId:input.appServerInstanceId||undefined,appServerInstanceEpoch:input.appServerInstanceEpoch||undefined,projectId:input.projectId||"",featureAreaId:input.featureAreaId||"",taskId:input.taskId||"",runId:input.runId||"",threadId:input.threadId||"",turnId:input.turnId||"",connectionId:input.connectionId||"",runtimeRequestId:input.runtimeRequestId||"",submissionRequestId:input.submissionRequestId||"",expectedState:input.expectedState||"",payloadSHA256:"",action:input.action||"",selectedOptionId:input.selectedOptionId||undefined,rawText:input.rawText||undefined,answers:answers.length?answers:undefined,sourceSurface:input.sourceSurface||"",sourceUserEventId:input.sourceUserEventId||undefined};
    const bytes=new TextEncoder().encode(decisionEscapeJSON(ordered));
    if(!globalThis.crypto?.subtle)throw new Error("浏览器无法计算决策完整性摘要；请刷新或使用受支持客户端。");
    const digest=await globalThis.crypto.subtle.digest("SHA-256",bytes);
    return [...new Uint8Array(digest)].map(v=>v.toString(16).padStart(2,"0")).join("");
  }
  function create({api, t, layout = "console", resourceVersion = "", filterHost = null}) {
    if (typeof api !== "function" || typeof t !== "function") throw new TypeError("SessionWorkbench requires api and t functions.");
    let host = null, project = "", runID = "", area = "", scope = "all", offset = 0, tab = "progress", availableAreas = [], clockTimer = null;
    let page = null, snapshot = null, prompt = null, older = [], notice = "", filterNotice = "", listError = "", detailError = "", online = false, loading = false;
    let generation = 0, listVersion = 0, readVersion = 0, mountVersion = 0, readController = null, listController = null, streamController = null, promptController = null, historyController = null, reconnectTimer = null, watchdogTimer = null, listTimer = null;
    let listState = "idle", snapshotState = "idle", streamState = "idle", listRefreshPending = false, listRefreshInFlight = false, listBackoff = 5000, lastSnapshotAt = "", lastStreamAt = "", lastListAt = "", listErrorCode = "", detailErrorCode = "", visibilityBound = false, lifecycleBound = false, areaSignature = "", listOpen = false, technicalOpen = false;
    let streamLive = false, writeBusy = false, newContent = false, before = null, historyMore = false, historyLoading = false;
    let historyWindows = [], historyVersion = 0, restoreScroll = false;
    let nativePage = null, nativeError = "", nativeLoading = false, nativeController = null;
    const steerDrafts = new Map(), steerUncertain = new Map();
    const blockedWrites = new Set(), signatures = new Map(), scrolls = new Map();
    const current = n => host?.isConnected && n === generation;
    const detail = () => snapshot?.detail || {};
    const runtime = () => snapshot?.session?.runtime || detail().runtime || {};
    const executorLabel=run=>({UPPER_LLM:"GPT",CODING_AGENT:"Codex",PI_AGENT:"Pi Agent"})[run.executor_kind]||"";
    const purposeLabel=run=>run.purpose==="SCOPE_REVIEW"?`${executorLabel(run)||"执行方未记录"} 审查`:`${root.DevBridgeStatus.runPurposeLabel(run.purpose,t)}${executorLabel(run)?" · "+executorLabel(run):""}`;
    const statusLabel=run=>run.review_authorization_required===true?"待重新授权":run.executor_kind==="UPPER_LLM"&&run.status==="RUNNING"?"等待 GPT 提交报告":root.DevBridgeStatus.presentStatus("run",run.status,t).label;
    const writable = () => online && streamLive && !writeBusy && !terminal(detail().run?.status);
    const runtimeCurrent = () => runtime().availability === "AVAILABLE" && runtime().sequence > 0 && runtime().connectionState === "CONNECTED" && !!runtime().connectionId;
    function savePosition() {const box = host?.querySelector("[data-session-scroll]"); if (box && runID) {scrolls.set(project + runID + tab, box.scrollTop); persist();}}
    function stop() {
      savePosition(); generation++; listVersion++; readVersion++; mountVersion++; readController?.abort(); listController?.abort(); streamController?.abort(); promptController?.abort(); historyController?.abort(); nativeController?.abort(); nativePage = null; nativeError = ""; nativeLoading = false; loading = false; historyLoading = false; writeBusy = false;
      clearTimeout(clockTimer); clearTimeout(reconnectTimer); clearTimeout(watchdogTimer); clearTimeout(listTimer); reconnectTimer = null; listTimer = null; listRefreshPending = false; listRefreshInFlight = false; streamLive = false; online = false; streamState = "closed"; listOpen = false; technicalOpen = false; host?.removeEventListener("click", click); host?.removeEventListener("change", change); host?.removeEventListener("input", steeringInput); host?.removeEventListener("keydown", keydown); filterHost?.removeEventListener("change", change); if (filterHost) filterHost.replaceChildren(); if (visibilityBound) {document.removeEventListener("visibilitychange", visibilityChange); visibilityBound = false;} if (lifecycleBound) {root.removeEventListener("pagehide", pagehide); root.removeEventListener("offline", offline); lifecycleBound = false;} host = null;
    }
    function persist() {
      // Exact view identity only; no messages, credentials or request payloads.
      try {const parsed = JSON.parse(sessionStorage.getItem("devbridge.run-views") || "{}"); const saved = parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {}; saved[project] = {runID, area, scope, offset, tab, historyWindows, scrollTop: scrolls.get(project + runID + tab) || 0}; sessionStorage.setItem("devbridge.run-views", JSON.stringify(saved));} catch (_) { /* storage may be unavailable in an embedded WebView */ }
    }
    function mount(element, projectID, areas) {
      // Product renders can repeat with the same DOM node.  Update only the
      // local context in that case; do not tear down a live exact Run view.
      if (host === element && project === projectID) {updateContext(areas); return;}
      stop(); host = element; mountVersion++;
      if (project !== projectID) {
        project = projectID; runID = ""; historyWindows = []; historyVersion++; snapshot = null; page = null; prompt = null; older = []; offset = 0; scope = "all"; area = ""; filterNotice = "";
        try {const parsed = JSON.parse(sessionStorage.getItem("devbridge.run-views") || "{}"); const allSaved = parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {}; const legacy = JSON.parse(sessionStorage.getItem("devbridge.run-view") || "{}"); const saved = allSaved[project] || (legacy?.project === project ? legacy : {}); runID = typeof saved.runID === "string" ? saved.runID : ""; area = typeof saved.area === "string" ? saved.area : ""; scope = ["current", "history", "all"].includes(saved.scope) ? saved.scope : "all"; offset = Math.max(0, Math.min(1000000, Number(saved.offset) || 0)); tab = ["progress", "conversation", "results", "instructions", "diff"].includes(saved.tab) ? saved.tab : "progress"; historyWindows = list(saved.historyWindows).filter(Number.isSafeInteger).filter(value => value > 0).slice(0,20); scrolls.set(project + runID + tab, Math.max(0, Number(saved.scrollTop) || 0)); restoreScroll = true; if (!allSaved[project] && legacy?.project === project) persist();} catch (_) { /* no saved view */ }
      }
      availableAreas = list(areas); areaSignature = JSON.stringify(availableAreas.map(value => [value.id, value.title || value.id]));
      if (area && !availableAreas.some(value => value.id === area)) {area = ""; filterNotice = t("session.areaUnavailable"); persist();}
      restoreScroll = true;
      const filters = `<label>${esc(t("session.scope"))}<select data-session-scope><option value="current">${esc(t("session.scopeCurrent"))}</option><option value="history">${esc(t("session.scopeHistory"))}</option><option value="all">${esc(t("session.scopeAll"))}</option></select></label><label>${esc(t("session.area"))}<select data-session-area><option value="">${esc(t("session.areaAll"))}</option>${list(areas).map(value => `<option value="${esc(value.id)}">${esc(value.title || value.id)}</option>`).join("")}</select></label>`;
      host.innerHTML = `<section class="session-workbench" data-session-list-open="false" data-session-technical-open="false"><aside class="session-sidebar"><button type="button" class="session-list-close" data-session-list-close>${esc(t("session.closeList"))}</button><h2>${esc(t("session.title"))}</h2>${filterHost ? "" : filters}<div data-session-list></div></aside><section class="session-main"><header data-session-header></header><div data-session-notice role="status"></div><nav data-session-tabs></nav><div data-session-scroll tabindex="0"><div data-session-content></div></div><button class="button session-new" data-session-new hidden>有新内容 ↓</button></section><aside class="session-technical"><div data-session-technical></div></aside></section>`;
      if (filterHost) {filterHost.innerHTML = filters; filterHost.addEventListener("change", change);}
      (filterHost || host).querySelector("[data-session-scope]").value = scope;
      (filterHost || host).querySelector("[data-session-area]").value = area;
      host.addEventListener("click", click);
      host.addEventListener("change", change);
      host.addEventListener("input", steeringInput);
      host.addEventListener("keydown", keydown);
      document.addEventListener("visibilitychange", visibilityChange); visibilityBound = true;
      root.addEventListener("pagehide", pagehide); root.addEventListener("offline", offline); lifecycleBound = true;
      signatures.clear(); paint(); tickClock(); readList(); scheduleListPoll(); if (runID) {readSnapshot(true); if(tab === "instructions" && !prompt) readPrompt();}
    }
    function tickClock() {clearTimeout(clockTimer); if(!host)return; clockTimer=setTimeout(()=>{if(host&&!document.hidden&&runID&&tab==="progress"&&!terminal(detail().run?.status))paint();tickClock();},15000);}
    function updateContext(areas) {
      const nextAreas = list(areas), nextSignature = JSON.stringify(nextAreas.map(value => [value.id, value.title || value.id]));
      if (nextSignature === areaSignature) return;
      availableAreas = nextAreas; areaSignature = nextSignature;
      const select = (filterHost || host)?.querySelector("[data-session-area]");
      const invalidated = area && !availableAreas.some(value => value.id === area);
      if (invalidated) {area = ""; offset = 0; filterNotice = t("session.areaUnavailable"); persist();}
      if (select) {const prior = area; select.innerHTML = `<option value="">${esc(t("session.areaAll"))}</option>${availableAreas.map(value => `<option value="${esc(value.id)}">${esc(value.title || value.id)}</option>`).join("")}`; select.value = prior;}
      if (host && project) {paint(); if (invalidated) readList();}
    }
    function scheduleListPoll(delay = listBackoff) {
      clearTimeout(listTimer); listTimer = null;
      if (!host || document.hidden) return;
      listTimer = setTimeout(() => {listTimer = null; readList();}, delay);
    }
    function visibilityChange() {
      if (!host) return;
      if (document.hidden) {clearTimeout(listTimer); listTimer = null; if(terminal(detail().run?.status))clearTimeout(reconnectTimer); return;}
      readList();
      if(runID&&root.DevBridgeComponents.needsAnalysisRefresh(detail()))readSnapshot(false);
    }
    async function refreshList({includeSelectedSnapshot = true} = {}) {
      const listRead = readList();
      const snapshotRead = includeSelectedSnapshot && runID ? readSnapshot(false) : null;
      await Promise.all([listRead, snapshotRead]);
    }
    function refresh() {return refreshList();}
    async function readList() {
      if (!host || !project) return;
      if (listRefreshInFlight) {listRefreshPending = true; return;}
      listRefreshInFlight = true; listState = page ? "refreshing" : "loading"; paintList();
      const controller = new AbortController(); listController = controller;
      const version = ++listVersion, mounted = mountVersion, expectedProject = project, expectedArea = area, expectedScope = scope, expectedOffset = offset;
      try {const value = await api(`/v1/runs?${query({project_id: expectedProject, feature_area_id: expectedArea, scope: expectedScope, limit: 20, offset: expectedOffset})}`, {signal: controller.signal}); if (mountVersion !== mounted || version !== listVersion || project !== expectedProject || area !== expectedArea || scope !== expectedScope || offset !== expectedOffset) return; if (!value || !Array.isArray(value.items) || typeof value.has_more !== "boolean" || !Number.isSafeInteger(value.limit) || value.limit < 1 || value.limit > 20 || !Number.isSafeInteger(value.offset) || value.offset !== expectedOffset || value.items.some(run => !run || typeof run !== "object" || typeof run.id !== "string" || !run.id || run.project_id !== expectedProject)) throw new Error("运行列表响应不可用"); page = value; listState = "ready"; listError = ""; lastListAt = new Date().toISOString(); listErrorCode = ""; listBackoff = 5000;}
      catch (error) {if (mountVersion === mounted && version === listVersion && error.name !== "AbortError") {listState = "failed"; listError = error.message; listErrorCode = error.code || ""; listBackoff = 30000;}}
      finally {if (mountVersion === mounted && version === listVersion) {listRefreshInFlight = false; paintList(); if (listRefreshPending) {listRefreshPending = false; readList();} else scheduleListPoll();}}
    }
    function paintList() {
      const element = host?.querySelector("[data-session-list]"); if (!element) return;
      const items = list(page?.items), stale = listState === "failed" && page;
      const empty = listState === "idle" ? t("session.listIdle") : listState === "loading" && !page ? t("session.listLoading") : listState === "failed" && !page ? t("session.readFailed", {reason: listError || t("session.listUnavailable")}) : page && !items.length ? t("session.listEmpty") : "";
      const selectedRun = detail().run || {};
      const currentStatuses = new Set(["CREATED", "PREPARING", "RUNNING", "WAITING_APPROVAL", "COLLECTING"]);
      const historyStatuses = new Set(["SUCCEEDED", "FAILED", "INFRASTRUCTURE_FAILED", "CANCELED"]);
      const scopeMismatch = snapshot && ((scope === "current" && !currentStatuses.has(selectedRun.status)) || (scope === "history" && !historyStatuses.has(selectedRun.status)));
      const areaMismatch = snapshot && area && selectedRun.feature_area_id !== area;
      const outsidePage = runID && page && !items.some(run => run.id === runID);
      const filtered = scopeMismatch || areaMismatch ? `<p class="session-filtered-selection">${esc(t("session.selectionFiltered"))}</p>` : outsidePage ? `<p class="session-filtered-selection">${esc(t("session.selectionOutsidePage"))}</p>` : "";
      const rows = items.length ? `<ul>${items.map(run => `<li><button type="button" data-session-run="${esc(run.id)}" title="${esc(run.id)}" class="${run.id === runID ? "active" : ""}"><strong>${esc(run.task_title || run.task_goal || run.id)}</strong><span>${esc(purposeLabel(run))} · ${esc(statusLabel(run))}</span>${run.started_at||run.created_at?`<small>${root.DevBridgeComponents.localizedTimestamp(run.started_at||run.created_at,document.documentElement.lang||undefined,t("workbench.unknown"))}</small>`:""}${(run.pending_approval_count??0)+(run.pending_request_count??0)>0?`<small>${esc(t("session.pendingCount",{count:(run.pending_approval_count??0)+(run.pending_request_count??0)}))}</small>`:""}</button></li>`).join("")}</ul>` : `<p>${esc(empty)}</p>`;
      const actions = page && !items.length ? `<p class="session-list-actions"><button type="button" data-session-scope-all ${scope === "all" ? "hidden" : ""}>${esc(t("session.viewAll"))}</button><button type="button" data-session-scope-history ${scope === "history" ? "hidden" : ""}>${esc(t("session.viewHistory"))}</button><button type="button" data-session-clear-area ${!area ? "hidden" : ""}>${esc(t("session.clearArea"))}</button><button type="button" data-session-first-page ${!offset ? "hidden" : ""}>${esc(t("session.firstPage"))}</button></p>` : "";
      patch("[data-session-list]", `${filterNotice ? `<p class="session-filtered-selection">${esc(filterNotice)}</p>` : ""}${stale ? `<p class="session-stale">${esc(t("session.listStale", {reason: listError || t("session.listUnavailable")}))}</p>` : ""}${filtered}${rows}${actions}<nav class="session-pagination"><button data-session-page="${Math.max(0, offset - 20)}" ${!offset ? "disabled" : ""}>${esc(t("session.previous"))}</button><span>${offset + 1}</span><button data-session-page="${offset + 20}" ${!page?.has_more || listState === "loading" ? "disabled" : ""}>${esc(t("session.next"))}</button></nav>`);
    }
    function patch(selector, value) {const element = host?.querySelector(selector); if (element && signatures.get(selector) !== value) {element.innerHTML = value; signatures.set(selector, value);}}
    function paintNotice() {
      let message, kind = "muted";
      if (!runID) message = "";
      else if (snapshotState === "loading") message = t("session.snapshotLoading");
      else if (snapshotState === "failed" && !snapshot) {message = t("session.readFailed", {reason: detailError || t("session.snapshotUnavailable")}); kind = "session-disconnected";}
      else if (terminal(detail().run?.status)) message = "";
      else if (streamState === "connected") message = "";
      else if (streamState === "connecting") message = "正在连接实时更新…";
      else if (snapshot) {message = t("session.streamDisconnected", {time: lastSnapshotAt || t("session.timeUnavailable")}); kind = "session-disconnected";}
      else message = t("session.unselected");
      const extra = runID ? [...new Set([notice, detailError].filter(Boolean))].join(" · ") : "";
      patch("[data-session-notice]", message||extra ? `<p class="${kind}">${esc(message)}${extra ? `${message?" · ":""}${esc(extra)}` : ""}</p>` : "");
      const steerButton=host?.querySelector("[data-session-steer]");
      if(steerButton) steerButton.disabled=!snapshot?.steering?.capability?.allowed||!writable()||!runtimeCurrent()||steerUncertain.has(steerKey());
      host?.querySelectorAll("[data-session-write]").forEach(button => {button.disabled = !writable() || !button.hasAttribute("data-session-cancel") && !button.hasAttribute("data-session-recovery") && !runtimeCurrent() || blockedWrites.has(button.dataset.sessionWrite);});
    }
    function paint() {
      if (!host) return; paintList(); paintNotice();
      const d = detail(), r = d.run || {}, rt = runtime();
      patch("[data-session-header]", runID ? `<div><div class="session-run-caption">${esc(purposeLabel(r))} · ${esc(statusLabel(r))}</div><h2>${esc(r.task_title || r.task_goal || "运行记录")}</h2></div><div class="session-header-actions"><button type="button" data-session-list-open>${esc(t("session.returnList"))}</button><button type="button" data-session-refresh>${esc(t("session.refresh"))}</button><button type="button" data-session-technical-open>诊断</button><button type="button" data-session-close aria-label="关闭运行视图">×</button></div>` : `<div><h2>选择一项运行</h2></div><button type="button" data-session-list-open>${esc(t("session.returnList"))}</button>`);
      const pending=(r.pending_request_count||0)+(r.pending_approval_count||0);
      patch("[data-session-tabs]", runID ? [ ["progress", "进度"+(pending?` · ${pending}`:"")], ["conversation", "会话记录"], ["results", "结果与分析"], ["instructions", "指令"], ["diff", "文件变化"] ].map(([value, title]) => `<button type="button" data-session-tab="${value}" aria-pressed="${tab === value}">${title}</button>`).join("") : "");
      const box = host.querySelector("[data-session-scroll]"), atBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 80, prior = box.scrollTop;
      const value = !runID ? `<div class="session-empty"><span aria-hidden="true">▷</span><h3>选择运行，查看实际进展</h3><p>左侧按审查、实施和验证列出每次运行。</p></div>` : tab === "progress" ? progress() : tab === "results" ? root.DevBridgeComponents.runOutput(d,t) : tab === "instructions" ? instructions() : tab === "diff" ? diff() : conversation();
      const changed = signatures.get("[data-session-content]") !== value;
      if (runID && ["conversation","progress"].includes(tab)) paintConversation(value,tab);
      else {patch("[data-session-content]", value); host.querySelector("[data-session-content]").dataset.mode = tab;}
      if (changed) {if (atBottom && tab === "conversation") box.scrollTop = box.scrollHeight; else {box.scrollTop = prior; newContent = tab === "conversation";}}
      host.querySelector("[data-session-new]").hidden = !newContent;
      const f = d.frozen_config || {}, agent = rt.agent || {}, usage = r.token_usage || {};
      patch("[data-session-technical]", `<button type="button" data-session-technical-close>${esc(t("session.closeDetails"))}</button>` + facts([["Task", r.task_id || "未关联"], ["Task phase", snapshot?.task_phase || "UNAVAILABLE"], ["Review", r.review_id || "未关联"], ["Execution", r.execution_proposal_id || "未关联"], ["AgentConversation", snapshot?.agent_conversation_id || "UNAVAILABLE"], ["Thread", r.thread_id || "尚无记录"], ["Turn", r.turn_id || "尚无记录"], ["Connection", rt.connectionId || "UNAVAILABLE"], ["连接状态", rt.connectionState || "UNKNOWN"], ["最后 Codex 活动", rt.sequence > 0 && !String(rt.lastActivityAt || "").startsWith("0001") ? rt.lastActivityAt || "UNAVAILABLE" : "UNAVAILABLE"], ["Workspace", d.workspace?.worktree_path || r.workspace_id || "尚未准备"], ["Model", f.model || "UNAVAILABLE"], ["Reasoning", f.reasoning_effort || "UNAVAILABLE"], ["用量", usage.status === "AVAILABLE" ? usage.counts?.total_tokens ?? "UNAVAILABLE" : usage.status || "UNAVAILABLE"]]) + `<details class="session-view-diagnostics"><summary>${esc(t("session.diagnostics"))}</summary>${facts([["Layout", layout], ["Web resource", resourceVersion || "UNAVAILABLE"], ["Desktop executable", layout === "embedded" ? "UNAVAILABLE (not passed to document)" : "NOT_APPLICABLE"], ["Core declaration", "UNAVAILABLE (/readyz exposes readiness only)"], ["Project selected", project ? "yes" : "no"], ["Run selected", runID ? "yes" : "no"], ["Filter", `${scope}/${area || "all"}`], ["Page", offset], ["List", `${listState} ${lastListAt}`], ["List error code", listErrorCode || "UNAVAILABLE"], ["Snapshot", `${snapshotState} ${lastSnapshotAt}`], ["Stream", `${streamState} ${snapshot?.session?.generation || ""} ${lastStreamAt}`], ["Detail error code", detailErrorCode || "UNAVAILABLE"]])}</details>` + (d.failure ? `<p role="alert">${esc(d.failure.code || "")} ${esc(d.failure.message || "")}</p>` : "") + `<details class="session-view-diagnostics"><summary>采集与工作流记录</summary>${coverage()}${nativeStatus(rt)}${list(d.events).map(event=>`<p class="session-core-event">${esc(event.type)} · ${esc(event.to||event.reason_code||"")} · ${esc(event.created_at)}</p>`).join("")}${[rt.lastError,rt.lastWarning,...list(rt.diagnostics)].filter(Boolean).map(event=>`<p class="session-diagnostic">${esc(event.method)} · ${esc(event.summary)}</p>`).join("")}</details>` + cancellation());
      paintNotice();
    }
    function progress() {
      const d=detail(),r=d.run||{},rt=runtime(),view=progressSummary(snapshot),usage=r.token_usage||{},model=d.frozen_config?.model;
      const last=view.lastActivity===null?"尚无记录":`${duration(Math.max(0,Date.now()-view.lastActivity))}前`;
      const connection=view.upper?"GPT 对话":view.ended?"历史记录":!streamLive?"更新连接中断":view.active?"实时活动":"已连接 Core";
      const next=view.reauthorization?"服务重启后原文件授权已失效。请在任务详情点击「重新授权 GPT 审查」，再回到原 GPT 对话继续；原审查记录保留。":view.upper&&!view.ended?"由原 GPT 对话读取文件并提交报告。此处展示已记录的审查证据；请回到原对话继续审查。":view.ended?(r.analysis_state==="REQUIRED"?"等待 GPT 分析运行结果。":r.analysis_state==="RECORDED"?"GPT 分析已入库；下一步请查看任务。":"查看本次结果与分析，决定后续操作。") : !view.active?"尚未收到当前回合的实时活动；这里保留最后读取的记录。":view.current?text(metadata(view.current).command||view.step?.step||itemLabel(view.current.type)):view.step?.step||"等待下一项活动。";
      const metrics=[[view.upper?"审查历时":"运行耗时",duration(view.elapsed)]];
      if(!view.upper||view.lastActivity!==null)metrics.push([view.upper?"最后读取":"最后活动",last]);
      if(model)metrics.push(["模型",model]);
      if(usage.status==="AVAILABLE"&&Number.isFinite(usage.counts?.total_tokens))metrics.push(["Token 用量",Number(usage.counts.total_tokens).toLocaleString()]);
      if(view.upper&&d.review_evidence_status==="AVAILABLE")metrics.push(["已记录文件操作",String(d.review_evidence?.successful_operations??0)]);
      const message=view.message&&!view.ended?text(view.message.text||metadata(view.message).text).slice(0,600):"";
      const plan=view.steps.length?`<section class="session-progress-plan"><header><h3>本次计划${view.active?"":" · 最后记录"}</h3><span>${view.completed} / ${view.steps.length} 步</span></header><ol>${view.steps.map(step=>`<li data-step-state="${esc(step.status)}"><span aria-hidden="true">${step.status==="completed"?"✓":step.status==="inProgress"?"●":"○"}</span><div>${esc(step.step)}<small>${esc(itemState(step.status))}</small></div></li>`).join("")}</ol><p class="muted">步骤会随执行调整，完成步骤不等于任务验收。</p></section>`:view.upper?"":`<p class="session-progress-hint">本次尚未发布步骤计划；当前活动仍会实时显示。</p>`;
      return `<div data-session-progress-region><section class="session-progress-overview" data-runtime-live="${view.active&&streamLive}"><div class="session-progress-eyebrow"><span class="session-live-dot" aria-hidden="true"></span>${esc(connection)}</div><h3>${esc(!streamLive&&view.active?"最后记录："+view.headline:view.headline)}</h3><p class="session-progress-current">${esc(next)}</p>${view.quiet?'<p class="session-quiet" role="status">较长时间没有新活动；不代表运行失败。可重新读取或查看会话记录。</p>':""}<dl class="session-progress-metrics">${metrics.map(([label,value])=>`<div><dt>${esc(label)}</dt><dd>${esc(value)}</dd></div>`).join("")}</dl>${view.ended?'<button class="button" type="button" data-session-tab="results">查看结果与分析 →</button>':""}</section></div><div data-session-request-region>${requests(true)}</div><div data-session-plan-region>${plan}</div><div data-session-message-region>${message?`<section class="session-progress-message"><header><h3>最近进展</h3><button type="button" data-session-tab="conversation">查看会话记录 →</button></header>${markdown(message)}${text(view.message.text||metadata(view.message).text).length>600?'<p class="muted">更多内容见会话记录。</p>':""}</section>`:""}</div><div data-session-items></div><div data-session-steering-region>${steering()}</div>`;
    }
    function coverage() {
      const value = snapshot?.session?.coverage;
      return `<p class="session-coverage">记录覆盖：${esc(typeof value === "string" ? value : value?.state || "UNAVAILABLE")} ${esc(list(value?.reasons).join(" · "))} · 历史未采集内容不会由完成摘要补齐。</p>`;
    }
    function item(value) {
      const meta=metadata(value),kind=value.type||"unsupported",state=itemState(value.status),title=itemLabel(kind);
      if(kind==="reasoning")return "";
      let body="";
      if(kind==="agentMessage"||kind==="plan")body=markdown(value.text||meta.text||"");
      else if(kind==="commandExecution")body=pre(meta.command||"命令正文未采集")+(meta.exitCode!==undefined?`<p>退出码 ${esc(meta.exitCode)}</p>`:"")+(value.outputTail?pre(value.outputTail):"");
      else if(kind==="fileChange")body=list(meta.changes).map(change=>`<p><code>${esc(change.path)}</code></p>${change.diff?pre(change.diff):""}`).join("");
      else if(value.text||meta.text)body=markdown(value.text||meta.text);
      else return "";
      const cut=value.truncated?'<p class="muted">此项显示内容已截断。</p>':"";
      const caption=kind==="commandExecution"?text(meta.command||title).slice(0,180):kind==="fileChange"?`${title} · ${list(meta.changes).length} 个文件`:title;
      return `<article data-session-item="${esc(value.id)}" class="session-item session-item-${["agentMessage","commandExecution","fileChange","plan"].includes(kind)?kind:"unsupported"}">${["commandExecution","fileChange"].includes(kind)?`<details><summary><span>${esc(caption)}</span><small>${esc(state)}</small></summary>${body}${cut}</details>`:`<header><strong>${esc(title)}</strong><span>${esc(state)}</span></header>${body}${cut}`}</article>`;
    }
    function conversation() {
      if (!runID) return "";
      const merged=new Map();
      [...older,...list(snapshot?.items?.items)].forEach(record=>{const value=record.item||record,prior=merged.get(value.id);if(!prior||(record.revision||0)>=(prior.revision||0))merged.set(value.id,record);});
      const records=[...merged.values()].sort((a,b)=>(a.ordinal||0)-(b.ordinal||0));
      const rendered=records.map(record=>item(record.item||record)).filter(Boolean);
      return `<div data-session-history-region class="session-history-toolbar">${historyMore?`<button type="button" data-session-history ${historyLoading?"disabled":""}>${historyLoading?"读取中…":"读取更早记录"}</button>`:""}<span>${rendered.length} 项可读记录</span></div><div data-session-items>${rendered.length?records.map((record,index)=>`${index&&record.ordinal>records[index-1].ordinal+1?`<p data-session-item="gap:${record.ordinal}" class="session-coverage">此处还有未加载的记录。<button type="button" data-session-gap="${record.ordinal}">读取缺少的页面</button></p>`:""}${item(record.item||record)}`).join(""):'<div class="session-empty"><h3>尚无已采集的会话内容</h3><p>可返回进度查看运行状态；GPT 审查正文请在原对话中查看。</p></div>'}</div><div data-session-request-region><details class="session-record-source"><summary>运行请求记录</summary>${requests()}</details></div><div data-session-native-region><details class="session-record-source"><summary>读取 Codex 保存的历史</summary>${nativeHistoryPanel()}</details></div>`;
    }
    function paintConversation(html,mode="conversation") {
      const content = host.querySelector("[data-session-content]");
      if (content.dataset.mode !== mode || !content.querySelector("[data-session-items]")) {content.innerHTML = html; content.dataset.mode = mode; signatures.set("[data-session-content]", html); return;}
      if (signatures.get("[data-session-content]") === html) return;
      const template = document.createElement("template"); template.innerHTML = html;
      for (const selector of ["[data-session-progress-region]", "[data-session-message-region]", "[data-session-results-region]", "[data-session-native-region]", "[data-session-history-region]", "[data-session-plan-region]", "[data-session-request-region]", "[data-session-steering-region]", "[data-session-events-region]"]) {
        const next = template.content.querySelector(selector), previous = content.querySelector(selector);
        if (next && previous && next.innerHTML !== previous.innerHTML) {
          if (selector === "[data-session-steering-region]" && previous.contains(document.activeElement)) {
            const oldButton = previous.querySelector("[data-session-steer]"), newButton = next.querySelector("[data-session-steer]");
            if (oldButton && newButton) oldButton.disabled = newButton.disabled;
          } else {const open=[...previous.querySelectorAll("details")].map(row=>row.open);previous.innerHTML = next.innerHTML;previous.querySelectorAll("details").forEach((row,index)=>{row.open=!!open[index];});}
        }
      }
      const container = content.querySelector("[data-session-items]"), nextItems = template.content.querySelector("[data-session-items]");
      if (container && nextItems) {
        const existing = new Map([...container.children].map(node => [node.dataset.sessionItem, node]));
        let cursor = container.firstElementChild;
        for (const next of [...nextItems.children]) {
          const key = next.dataset.sessionItem; let node = key ? existing.get(key) : null;
          if (node && node.outerHTML !== next.outerHTML) {
            const disclosure = [...node.querySelectorAll("details")].map(value => value.open);
            node.innerHTML = next.innerHTML; node.className = next.className;
            node.querySelectorAll("details").forEach((value, index) => {value.open = !!disclosure[index];});
          }
          if (!node) node = next;
          existing.delete(key);
          if (node !== cursor) container.insertBefore(node, cursor);
          cursor = node.nextElementSibling;
        }
        for (const node of existing.values()) node.remove();
      }
      signatures.set("[data-session-content]", html);
    }
    function instructions() {
      return `<h3>准确 Run 指令</h3><p>作者提案、冻结指令与实际派发输入分别记录；不会补入其他 Run 或 Thread 的指令。</p>${list(prompt?.items).map((entry, index) => `<section><h4>${esc(({FROZEN_AUTHOR_INSTRUCTION: "冻结作者指令", RECORDED_DISPATCH_INPUT: "实际 Run 输入"})[entry.source] || entry.source)}</h4><p>${entry.dispatched ? "已有派发记录" : "尚无派发记录"}</p>${entry.available ? `<button type="button" data-session-copy="${index}">复制接口原文</button>${pre(entry.text || "")}` : "<p>UNAVAILABLE · 尚无可读取文本</p>"}</section>`).join("") || "<p>尚无已读取指令；未记录内容不会被推断为已派发。</p>"}`;
    }
    function diff() {
      const rt = runtime(), d = detail();
      return `<h3>Agent 报告</h3><p>App Server diff 是报告，不等于 Bridge 验证后的 Git 事实。</p>${rt.turnDiff ? pre(rt.turnDiff) : "<p>未采集 Turn diff。</p>"}<h3>Bridge Git 证据</h3>${facts([["开始快照", d.start_snapshot || "UNAVAILABLE"], ["结束快照", d.end_snapshot || "UNAVAILABLE"], ["证据可用性", d.evidence?.available ? "AVAILABLE" : "UNAVAILABLE"], ["Base commit", d.evidence?.base_commit || "UNAVAILABLE"], ["Head commit", d.evidence?.head_commit || "UNAVAILABLE"]])}<p>此页面只读取已有证据，不重新执行 Git 采集。</p>${d.evidence?.diff ? pre(d.evidence.diff) : ""}${d.evidence?.diff_truncated || d.evidence?.changed_files_truncated ? "<p>TRUNCATED · Git 证据超过展示上限。</p>" : ""}${list(d.evidence?.changed_files).map(path => `<p><code>${esc(typeof path === "string" ? path : path.path)}</code></p>`).join("")}`;
    }
    function nativeStatus(rt) {
      const value = rt.nativeThreadStatus;
      if (!value) return "";
      const flags = list(value.activeFlags), labels = [];
      if (flags.includes("waitingOnApproval")) labels.push("等待运行授权");
      if (flags.includes("waitingOnUserInput")) labels.push("等待用户输入");
      const label = labels.join("、") || ({active:"正在运行", idle:"当前回合空闲", notLoaded:"会话未加载", systemError:"运行异常"})[value.type] || "状态未知";
      const historical=rt.connectionState!=="CONNECTED"||["completed","failed","interrupted"].includes(rt.turnStatus);
      return `<p class="session-native-status">Codex${historical?" 最后记录":""}：${esc(label)}${rt.connectionState !== "CONNECTED" ? "（连接已断开）" : ""}<small> · 运行状态记录，不代表任务已验收</small></p>`;
    }
    function steerKey() {return JSON.stringify([project,runID,runtime().threadId,runtime().turnId]);}
    function steeringInput(event) {if (event.target.matches("[data-session-steering-text]")) steerDrafts.set(steerKey(),event.target.value);}
    function steering() {
      const view=snapshot?.steering, cap=view?.capability||{}, receipts=list(view?.receipts), key=steerKey(), pending=steerUncertain.get(key);
      if (pending && receipts.some(r=>r.submissionRequestId===pending && ["ACCEPTED","REJECTED"].includes(r.state))) steerUncertain.delete(key);
      if (!cap.allowed && !receipts.length && !steerUncertain.has(key)) return "";
      const history=receipts.map(r=>`<article class="session-steering-receipt"><strong>${esc(({ACCEPTED:"Codex 已接收，等待后续执行",REJECTED:"本次指令未被接收",DISPATCHING:"发送结果待核对，不可重发",UNKNOWN:"发送结果未知，不可重发"})[r.state]||"状态待核对")}</strong>${pre(r.text)}<small>${esc(r.submittedAt)}</small></article>`).join("");
      if(terminal(detail().run?.status))return `<section class="session-steering"><h3>追加说明记录</h3>${history}</section>`;
      const allowed=cap.allowed&&writable()&&runtimeCurrent()&&!steerUncertain.has(key);
      return `<section class="session-steering"><h3>执行中追加指令</h3><p>补充当前回合的执行说明。任务范围、模型、文件权限和验收要求仍按已批准方案执行；变更方案需停止后修订。</p><label>追加说明<textarea data-session-steering-text rows="3" maxlength="16384" autocomplete="off" ${terminal(detail().run?.status)?"disabled":""}>${esc(steerDrafts.get(key)||"")}</textarea></label><button type="button" data-session-steer ${allowed?"":"disabled"}>发送到当前回合</button>${!allowed?`<p>${esc(steerUncertain.has(key)?"发送结果待核对，请刷新读取回执；不会自动重发。":cap.reason||"连接恢复并核对当前回合后可发送")}</p>`:""}${history}</section>`;
    }
    async function submitSteering() {
      const cap=snapshot?.steering?.capability, key=steerKey(), target=runID, expectedProject=project, n=generation;
      if (!cap?.allowed || !cap.target || !writable() || !runtimeCurrent() || steerUncertain.has(key)) return;
      const field=host.querySelector("[data-session-steering-text]"), value=field?.value||"";
      if (!value.trim()) {notice="请填写追加说明。"; paintNotice(); return;}
      if (new TextEncoder().encode(value).length>16384) {notice="追加说明不能超过 16 KiB。"; paintNotice(); return;}
      if (!globalThis.crypto?.randomUUID) {notice="浏览器无法生成发送凭据，请刷新或使用受支持客户端。"; paintNotice(); return;}
      const requestID=globalThis.crypto.randomUUID();
      steerDrafts.set(key,value); steerUncertain.set(key,requestID); writeBusy=true; paint();
      try {
        const receipt=await api(`/ui/api/runs/${encodeURIComponent(target)}/steering`,{method:"POST",body:JSON.stringify({...cap.target,submissionRequestId:requestID,text:value})});
        if (receipt.runId!==target || receipt.submissionRequestId!==requestID || receipt.threadId!==cap.target.threadId || receipt.turnId!==cap.target.turnId) throw new Error("发送回执与当前回合不一致");
        if (["ACCEPTED","REJECTED"].includes(receipt.state)) steerUncertain.delete(key);
        if (receipt.state==="ACCEPTED" && steerDrafts.get(key)===value) {
          steerDrafts.delete(key);
          if(current(n)&&runID===target&&project===expectedProject) {const field=host.querySelector("[data-session-steering-text]");if(field?.value===value)field.value="";}
        }
        if (current(n)&&runID===target&&project===expectedProject) notice=receipt.state==="ACCEPTED"?"Codex 已接收追加说明；这不代表任务已完成。":receipt.state==="REJECTED"?"Codex 未接收这次说明，请核对回合状态。":"发送结果未知，请读取回执，不要重复提交。";
      } catch(error) {
        const rejected=[400,401,403,404,409,422].includes(error.status);
        if(rejected)steerUncertain.delete(key);
        if(current(n)&&runID===target&&project===expectedProject) notice=rejected?`追加说明未发送：${error.message}`:`发送结果待核对：${error.message}。请刷新读取回执。`;
      }
      finally {if(current(n)&&runID===target&&project===expectedProject) {writeBusy=false; await readSnapshot(); paint();}}
    }
    function requestKey(request) {return JSON.stringify([request.runId, request.runtimeRequestId, request.runtimeEpoch]);}
    function decisionFields(request) {
      const payload=request.payload||{};
      if(request.method==="item/tool/requestUserInput")return list(payload.questions).map((question,index)=>{const options=list(question.options);const name=question.question||question.id||`问题 ${index+1}`;return `<label>${esc(name)}${options.length&&!question.isOther?`<select data-decision-question="${index}" required><option value="" disabled selected>请选择</option>${options.map(option=>`<option value="${esc(option.id||option.label)}">${esc(option.label||option.id)}${option.description?` · ${esc(option.description)}`:""}</option>`).join("")}</select>`:`<input data-decision-question="${index}" type="${question.isSecret?"password":"text"}" required maxlength="16384" autocomplete="off">`}</label>`;}).join("");
      if(request.method==="mcpServer/elicitation/request")return `<label>原始请求动作<select data-decision-elicitation required><option value="" disabled selected>请选择</option><option value="accept">accept</option><option value="decline">decline</option><option value="cancel">cancel</option></select></label><label>原始 JSON 内容（仅 accept 且请求需要内容时填写）<textarea data-decision-elicitation-content maxlength="16384" autocomplete="off"></textarea></label>`;
      return "";
    }
    function decisionButtonLabel(action) {return ({CONTINUE_WAITING:"继续等待",RESPOND:"提交回答",STOP_AND_REVISE:"停止并修订",CANCEL_OPERATION:"取消操作"})[action]||action;}
    function requests(onlyPending=false) {
      const recovery=snapshot?.recoveryStatus;
      const status=recovery&&recovery.state!=="NOT_REQUIRED"&&(!onlyPending||recovery.state!=="RESOLVED")?`<section class="session-recovery-status"><strong>${esc(recovery.displayTitle)}</strong><p>${esc(recovery.displayMessage||"")}</p><p>${esc(recovery.reportSummary||"")}</p></section>`:"";
      return status+list(runtime().requests).map((request, index) => {
        if(onlyPending&&!(request.kind==="RUNTIME_APPROVAL"&&request.state==="PENDING"||["WAITING_FOR_USER","UNKNOWN","USER_DECISION_RECORDED","RESPONDING"].includes(request.projection?.lifecycleState)||request.projection?.responseDisposition==="UNKNOWN"))return "";
        const projection=request.projection||{}, key=requestKey(request), payload=request.payload||{}, waiting=request.kind==="CODEX_REQUEST"&&projection.lifecycleState==="WAITING_FOR_USER"&&list(projection.availableActions).length&&request.runId===runID&&request.projectId===project&&request.connectionId===runtime().connectionId&&request.threadId===runtime().threadId&&request.turnId===runtime().turnId;
        const unknown=projection.responseDisposition==="UNKNOWN", actions=waiting||unknown?list(projection.availableActions):[];
        const nativeButtons=request.kind==="RUNTIME_APPROVAL"?list(request.availableResponses).map((response,choice)=>nativeApprovalResolution(request,runtime(),choice)?`<button type="button" data-session-write="${esc(key)}" data-session-native-approval="${index}" data-native-choice="${choice}">${esc(nativeApprovalLabel(response))}</button>`:"").join(""):"";
        const buttons=nativeButtons+actions.map(action=>`<button type="button" data-session-write="${esc(key)}" ${unknown?"data-session-recovery":"data-session-decision"}="${index}" data-decision-action="${esc(action)}">${esc(decisionButtonLabel(action))}</button>`).join("");
        const stateCopy=projection.displayTitle||projection.title||(request.kind==="RUNTIME_APPROVAL"&&request.state==="PENDING"?"等待命令或文件权限批准":"运行时请求状态待确认");
        return `<section class="session-request" data-session-request="${index}"><strong>${esc(request.kind||"RUNTIME_REQUEST")} · ${esc(request.method)}</strong><p>${esc(stateCopy)} · ${esc(projection.responseDisposition||"UNAVAILABLE")}</p><p>${esc(projection.displayMessage||projection.summary||"")}</p>${projection.userDecisionRecorded?`<pre>${esc(projection.userDecisionSummary||"")}</pre>`:""}<p>${esc(payload.reason||payload.message||payload.command||"")}</p><details><summary>原始请求与选项</summary>${pre(JSON.stringify({payload,availableResponses:request.availableResponses,responseSchema:request.responseSchema},null,2))}</details><small>runtimeRequestId ${esc(request.runtimeRequestId||"历史请求")}</small>${waiting?decisionFields(request):""}<div>${buttons||"<p>只读 · 只有原始、当前 WAITING_FOR_USER 请求可提交；页面不会代答或重试。</p>"}</div></section>`;
      }).join("") + list(detail().approvals).map((approval, index) => onlyPending&&approval.status!=="PENDING"?"":`<section class="session-request"><strong>RUNTIME_APPROVAL · ${esc(approval.kind)}</strong><p>${esc(approval.status)} · ${esc(approval.decision?.type || "尚无决定")} · ${esc(approval.delivered_at || "尚无投递记录")}</p><p>${esc(approval.id)}</p><details><summary>请求内容与范围（脱敏展示）</summary>${pre(approval.request_payload || "未提供")}</details>${approval.status === "PENDING" ? list(approval.allowed_decisions).filter(decision => ["APPROVE_ONCE", "APPROVE_FOR_SESSION", "DECLINE", "CANCEL", "GRANT_REQUESTED_PERMISSIONS"].includes(decision)).map(decision => `<button type="button" data-session-write="approval:${esc(approval.id)}" data-session-approval="${index}" data-decision="${esc(decision)}">${esc(({APPROVE_ONCE:"允许本次",APPROVE_FOR_SESSION:"允许本会话",DECLINE:"拒绝此项",CANCEL:(detail().run?.execution_proposal_id || detail().run?.operation_kind || detail().run?.operation_id || detail().run?.review_id) ? "拒绝此项（由原 Core 转换）" : "取消整个运行（原审批 CANCEL）",GRANT_REQUESTED_PERMISSIONS:"授予请求中的权限"})[decision])}</button>`).join("") : ""}</section>`).join("");
    }
    function cancellation() {
      const capability = snapshot?.cancellation_capability;
      if (snapshot?.cancellation?.state === "CANCEL_REQUESTED") return "<p>取消处理中 · 等待原 Core 终态。</p>";
      if (!detail().run?.agent_execution_id || !capability?.available || capability.target?.runId !== runID) return "<p>取消操作未获原 Core 当前资格；恢复请沿正式操作指引。</p>";
      return `<details><summary>运行操作</summary><button type="button" class="danger" data-session-cancel data-session-write="cancel:${esc(runID)}">取消整个运行</button><p>受理回执不是终态；等待 Core 收口。</p></details>`;
    }
    async function readSnapshot(reconnect = false) {
      if (!runID || loading) return;
      clearTimeout(reconnectTimer); reconnectTimer=null;
      loading = true; snapshotState = snapshot ? "refreshing" : "loading"; const n = generation, version = ++readVersion, target = runID, expectedProject = project;
      readController?.abort(); const controller = new AbortController(); readController = controller;
      paintNotice();
      try {
        const value = await api(`/ui/api/runs/${encodeURIComponent(target)}/session?${query({project_id: project})}`, {signal: controller.signal});
        if (!current(n) || version !== readVersion || target !== runID || expectedProject !== project) return;
        if (value.detail?.project_id !== expectedProject || value.detail?.run?.id !== target || value.session?.runId !== target) throw new Error("准确 Run 会话响应不可用");
        if (snapshot?.session?.generation === value.session?.generation && Number(value.session?.cursor) < Number(snapshot.session?.cursor)) return;
        const items = await api(`/ui/api/runs/${encodeURIComponent(target)}/session/items?${query({project_id: project, limit: 50})}`, {signal: controller.signal});
        if (!current(n) || version !== readVersion || target !== runID || expectedProject !== project) return;
        if (items.runId !== target || !Array.isArray(items.items) || typeof items.hasMore !== "boolean") throw new Error("会话历史响应不可用");
        if (snapshot?.session?.generation && snapshot.session.generation !== value.session?.generation) {older = []; historyWindows = []; historyVersion++; notice = "GAP · Core 流已更换，原游标不能连续补读。";}
        const hv = historyVersion, windows = historyWindows.slice();
        const previous = windows.length ? await Promise.all(windows.map(boundary => api(`/ui/api/runs/${encodeURIComponent(target)}/session/items?${query({project_id: project, before: boundary, limit: 50})}`, {signal: controller.signal}))) : [];
        if (!current(n) || version !== readVersion || target !== runID || expectedProject !== project) return;
        if (previous.some(value => value.runId !== target || !Array.isArray(value.items) || typeof value.hasMore !== "boolean")) throw new Error("会话历史响应不可用");
        if (hv === historyVersion && previous.length) {older = previous.flatMap(page => list(page.items)); const first = previous.reduce((a, b) => a.nextBefore < b.nextBefore ? a : b); before = first.nextBefore; historyMore = !!first.hasMore;}
        snapshot = {...value, items}; online = true; detailError = ""; detailErrorCode = ""; snapshotState = "ready"; lastSnapshotAt = new Date().toISOString();
        const shouldConnect = reconnect && !terminal(detail().run?.status);
        if (terminal(detail().run?.status)) {streamController?.abort(); streamLive = false; streamState = "closed";}
        else if (shouldConnect) streamState = "connecting";
        if (!older.length) {before = items.nextBefore ?? null; historyMore = !!items.hasMore;}
        paint(); if (restoreScroll) {host.querySelector("[data-session-scroll]").scrollTop = scrolls.get(project + runID + tab) || 0; restoreScroll = false;} if (shouldConnect) startStream(n);
      } catch (error) {if (current(n) && version === readVersion && target === runID && expectedProject === project && error.name !== "AbortError") {detailError = error.message; detailErrorCode = error.code || ""; const unreadable = error.status === 403 || error.status === 404; if (unreadable) {streamController?.abort(); clearTimeout(reconnectTimer); snapshot = null; prompt = null; older = []; before = null; historyMore = false; online = false; snapshotState = "failed"; streamLive = false; streamState = "closed";} else {online = !!snapshot; snapshotState = snapshot ? "stale" : "failed"; streamLive = false; streamState = snapshot && terminal(detail().run?.status) ? "closed" : "failed"; if (streamState === "failed") scheduleReconnect(n);} paint();}}
      finally {if (current(n) && version === readVersion && target === runID) {loading = false; if(!document.hidden&&online&&root.DevBridgeComponents.needsAnalysisRefresh(detail()))scheduleReconnect(n,5000);}}
    }
    function scheduleReconnect(n, delay = 3000) {
      clearTimeout(reconnectTimer);
      if (current(n)) reconnectTimer = setTimeout(() => {if (current(n)) {streamController?.abort(); readSnapshot(true);}}, delay);
    }
    async function startStream(n) {
      streamController?.abort(); clearTimeout(watchdogTimer); const controller = new AbortController(), target = runID, expectedProject = project, streamGeneration = snapshot?.session?.generation; streamController = controller; streamLive = false; streamState = "connecting"; paintNotice();
      const armWatchdog = () => {clearTimeout(watchdogTimer); watchdogTimer = setTimeout(() => {if (current(n) && streamController === controller && target === runID && expectedProject === project) {streamLive = false; streamState = "failed"; detailError = "更新流无响应，已禁用操作并重新读取"; controller.abort(); paintNotice(); scheduleReconnect(n);}}, 30000);};
      armWatchdog();
      try {
        const response = await fetch(`/ui/api/runs/${encodeURIComponent(target)}/session/events?${query({project_id: expectedProject, generation: streamGeneration, cursor: snapshot.session.cursor})}`, {signal: controller.signal, headers: {Accept: "text/event-stream", ...(document.querySelector('meta[name="devbridge-desktop-embed"]')?.content ? {"X-Devbridge-Desktop-Embed": document.querySelector('meta[name="devbridge-desktop-embed"]').content} : {})}, credentials: "same-origin", cache: "no-store"});
        if (!response.ok || !response.body) {const error = new Error(`订阅不可用 ${response.status}`); error.code = `HTTP_${response.status}`; throw error;}
        if (!current(n) || streamController !== controller || controller.signal.aborted || target !== runID || expectedProject !== project || streamGeneration !== snapshot?.session?.generation) return;
        streamLive = true; streamState = "connected"; lastStreamAt = new Date().toISOString(); paint();
        armWatchdog();
        const reader = response.body.getReader(), decoder = new TextDecoder(); let buffer = "";
        try {while (current(n) && streamController === controller && !controller.signal.aborted) {
          const chunk = await reader.read(); if (chunk.done) break; armWatchdog();
          buffer += decoder.decode(chunk.value, {stream: true}).replace(/\r\n/g, "\n");
          if (buffer.length > 32 * 1024 * 1024) throw new Error("GAP · 订阅帧超出本地上限，需要重读");
          let at; while ((at = buffer.indexOf("\n\n")) >= 0) {
            const frame = buffer.slice(0, at); buffer = buffer.slice(at + 2);
            const event = frame.split("\n").find(line => line.startsWith("event:"))?.slice(6).trim();
            const raw = frame.split("\n").filter(line => line.startsWith("data:")).map(line => line.slice(5).trimStart()).join("\n");
            if (!raw) continue;
            if (["resync", "gap", "reset", "unavailable", "reconnect"].includes(event)) throw new Error("GAP · 游标失效，重读快照");
            const update = JSON.parse(raw);
            if (target !== runID || expectedProject !== project || streamGeneration !== snapshot?.session?.generation) return;
            if (update.generation && update.generation !== streamGeneration) throw new Error("GAP · 流 generation 已变化");
            if (update.resetRequired) throw new Error(`GAP · ${update.reason || "需要重读快照"}`);
            lastStreamAt = new Date().toISOString(); if (event !== "heartbeat") await readSnapshot(false);
          }
        }} finally {if (streamController === controller) clearTimeout(watchdogTimer); await reader.cancel().catch(() => {}); reader.releaseLock();}
        if (current(n) && streamController === controller) throw new Error("更新流已关闭");
      } catch (error) {if (current(n) && streamController === controller && target === runID && expectedProject === project && error.name !== "AbortError") {streamLive = false; streamState = "failed"; detailError = error.message; detailErrorCode = error.code || ""; paintNotice(); scheduleReconnect(n);}}
    }
    async function select(target) {
      savePosition(); generation++; readController?.abort(); streamController?.abort(); promptController?.abort(); historyController?.abort(); nativeController?.abort(); clearTimeout(reconnectTimer); clearTimeout(watchdogTimer);
      nativePage = null; nativeError = ""; nativeLoading = false;
      readVersion++; runID = target; tab="progress"; historyWindows = []; historyVersion++; restoreScroll = true; snapshot = null; prompt = null; older = []; before = null; historyMore = false; historyLoading = false; writeBusy = false; loading = false; online = false; streamLive = false; snapshotState = target ? "loading" : "idle"; streamState = "idle"; lastSnapshotAt = ""; lastStreamAt = ""; detailError = ""; detailErrorCode = ""; newContent = false; notice = ""; persist(); signatures.clear(); paint();
      if (runID) {await readSnapshot(true); if (tab === "instructions") readPrompt();}
    }
    async function readPrompt() {
      const n = generation, target = runID; promptController?.abort(); promptController = new AbortController();
      try {const value = await api(`/ui/api/runs/${encodeURIComponent(target)}/session/instructions?${query({project_id: project})}`, {signal: promptController.signal}); if (!value || !Array.isArray(value.items)) throw new Error("运行指令响应不可用"); if (current(n) && target === runID) {prompt = value; paint();}}
      catch (error) {if (current(n) && error.name !== "AbortError") {notice = error.message; paintNotice();}}
    }
    function nativeHistoryPanel() {
      const target = detail().run || {};
      if (!target.thread_id || !target.turn_id) return "";
      return `<section class="session-native-history"><h3>Codex 持久化历史</h3><p>读取此 Run 对应的 Thread / Turn；不会恢复或继续执行。</p><button type="button" data-session-native ${nativeLoading || nativePage && !nativePage.nextCursor ? "disabled" : ""}>${nativeLoading ? "读取中…" : nativePage ? "读取下一页 Codex 历史" : "读取 Codex 历史"}</button>${nativeError ? `<p role="status">${esc(nativeError)}</p>` : ""}${list(nativePage?.items).map(value => `<article class="session-item"><header><strong>${esc(value.type)}</strong><span>${esc(value.status || "")}</span></header>${pre(value.text)}${value.truncated ? "<p>TRUNCATED · 此项超过显示上限。</p>" : ""}</article>`).join("")}</section>`;
    }
    async function nativeHistory() {
      if (nativeLoading || nativePage && !nativePage.nextCursor || !runID) return;
      const n = generation, target = runID, expectedProject = project, expectedThread = detail().run?.thread_id, expectedTurn = detail().run?.turn_id;
      if (!expectedThread || !expectedTurn) return;
      nativeLoading = true; nativeError = ""; nativeController?.abort(); const controller = new AbortController(); nativeController = controller; paint();
      try {
        const value = await api(`/ui/api/runs/${encodeURIComponent(target)}/session/native-history?${query({project_id: expectedProject, cursor: nativePage?.nextCursor, limit: 20})}`, {signal: controller.signal});
        if (!current(n) || target !== runID || expectedProject !== project || expectedThread !== detail().run?.thread_id || expectedTurn !== detail().run?.turn_id) return;
        if (value.runId !== target || value.threadId !== expectedThread || value.turnId !== expectedTurn) throw new Error("Codex 历史身份不匹配");
        if (value.availability !== "AVAILABLE") throw new Error(value.notice || "Codex 历史不可读取；仍可查看 Bridge 已采集记录。");
        if (!Array.isArray(value.items) || value.nextCursor !== null && typeof value.nextCursor !== "string") throw new Error("Codex 历史分页不可用");
        const rows = new Map(list(nativePage?.items).map(item => [item.id,item]));
        value.items.forEach(item => rows.set(item.id,item));
        nativePage = {...value, items: [...rows.values()].slice(-500)};
        if (rows.size >= 500) {nativePage.nextCursor = null; nativeError = "当前页面最多保留 500 项 Codex 历史。";}
      } catch (error) {if (current(n) && error.name !== "AbortError") nativeError = error.message;}
      finally {if (current(n)) {nativeLoading = false; paint();}}
    }
    async function history(boundaryOverride) {
      if ((!historyMore && !boundaryOverride) || historyLoading || historyWindows.length >= 20 || older.length >= 1000) return;
      const boundary = boundaryOverride || before; historyVersion++;
      historyLoading = true; historyController?.abort(); historyController = new AbortController(); const n = generation, target = runID, expectedProject = project, box = host.querySelector("[data-session-scroll]"), height = box.scrollHeight, top = box.scrollTop; paint();
      try {const value = await api(`/ui/api/runs/${encodeURIComponent(target)}/session/items?${query({project_id: expectedProject, before: boundary, limit: 50})}`, {signal: historyController.signal}); if (!current(n) || target !== runID || expectedProject !== project) return; if (value.runId !== target || !Array.isArray(value.items) || typeof value.hasMore !== "boolean") throw new Error("会话历史响应不可用"); older = [...value.items, ...older].slice(-1000); if (boundary && !historyWindows.includes(boundary)) historyWindows.push(boundary); if (!boundaryOverride || !before || value.nextBefore < before) {before = value.nextBefore; historyMore = value.hasMore;} persist(); if (older.length >= 1000) notice = "TRUNCATED · 当前页面最多保留 1000 项；重新打开可继续有界读取。";}
      catch (error) {if (current(n) && error.name !== "AbortError") notice = error.message;}
      finally {if (current(n)) {historyLoading = false; paint(); box.scrollTop = top + box.scrollHeight - height;}}
    }
    async function submit(button) {
      if (!writable() || !button.hasAttribute("data-session-cancel") && !button.hasAttribute("data-session-recovery") && !runtimeCurrent() || blockedWrites.has(button.dataset.sessionWrite)) return;
      const n = generation, key = button.dataset.sessionWrite; let url, body;
      if (button.hasAttribute("data-session-native-approval")) {
        const request=list(runtime().requests)[Number(button.dataset.sessionNativeApproval)];
        body=nativeApprovalResolution(request,runtime(),Number(button.dataset.nativeChoice));
        if(!body||body.runId!==runID||body.projectId!==project||!confirm(nativeApprovalLabel(body.response)+"？\n"+JSON.stringify({command:request.payload?.command,cwd:request.payload?.cwd,response:body.response},null,2)))return;
        url=`/ui/api/runs/${encodeURIComponent(runID)}/agent-requests/resolve`;
      } else if (button.hasAttribute("data-session-recovery")) {
        const request=list(runtime().requests)[Number(button.dataset.sessionRecovery)],action=button.dataset.decisionAction;
        if(!request||request.projection?.responseDisposition!=="UNKNOWN"||!list(request.projection.availableActions).includes(action))return;
        if(action==="CONTINUE_WAITING"){await readSnapshot();return;}
        body={};for(const name of ["operationKind","operationId","taskDefinitionRevisionId","reviewId","executionId","agentConversationId","runtimeEpoch","appServerInstanceId","appServerInstanceEpoch","projectId","featureAreaId","taskId","runId","threadId","turnId","connectionId","runtimeRequestId"])if(request[name]!==undefined)body[name]=request[name];
        body.action=action;body.commandRequestId=crypto.randomUUID();
        url=`/ui/api/runs/${encodeURIComponent(runID)}/agent-requests/recovery`;
      } else if (button.hasAttribute("data-session-decision")) {
        const request=list(runtime().requests)[Number(button.dataset.sessionDecision)],action=button.dataset.decisionAction;
        if(!request||request.kind!=="CODEX_REQUEST"||request.projection?.lifecycleState!=="WAITING_FOR_USER"||!list(request.projection.availableActions).includes(action)||request.runId!==runID||request.projectId!==project||request.connectionId!==runtime().connectionId||request.threadId!==runtime().threadId||request.turnId!==runtime().turnId)return;
        const section=button.closest("[data-session-request]"),payload=request.payload||{};
        const decision={operationKind:request.operationKind,operationId:request.operationId,taskDefinitionRevisionId:request.taskDefinitionRevisionId,reviewId:request.reviewId,executionId:request.executionId,agentConversationId:request.agentConversationId,runtimeEpoch:request.runtimeEpoch,appServerInstanceId:request.appServerInstanceId,appServerInstanceEpoch:request.appServerInstanceEpoch,projectId:request.projectId,featureAreaId:request.featureAreaId,taskId:request.taskId,runId:request.runId,threadId:request.threadId,turnId:request.turnId,connectionId:request.connectionId,runtimeRequestId:request.runtimeRequestId,submissionRequestId:crypto.randomUUID(),expectedState:request.state,action,answers:[],sourceSurface:"PRODUCT_SESSION_WORKBENCH"};
        if(action==="RESPOND"&&request.method==="item/tool/requestUserInput")for(const field of section.querySelectorAll("[data-decision-question]")){if(!field.reportValidity())return;const question=list(payload.questions)[Number(field.dataset.decisionQuestion)]||{};const options=list(question.options);const selected=options.find(option=>(option.id||option.label)===field.value);decision.answers.push({questionId:question.id,selectedOptionId:selected?(selected.id||selected.label):"",rawText:selected?"":field.value});}
        if(action==="RESPOND"&&request.method==="mcpServer/elicitation/request"){const field=section.querySelector("[data-decision-elicitation]"),content=section.querySelector("[data-decision-elicitation-content]");if(!field?.reportValidity())return;decision.selectedOptionId=field.value;decision.rawText=content?.value||"";}
        decision.payloadSHA256=await decisionPayloadSHA256(decision);
        body=decision;
        url=`/ui/api/runs/${encodeURIComponent(runID)}/agent-user-decisions`;
      } else if (button.hasAttribute("data-session-approval")) {
        const approval = list(detail().approvals)[Number(button.dataset.sessionApproval)], decision = button.dataset.decision;
        if (!approval || !list(approval.allowed_decisions).includes(decision)) return;
        const run = detail().run || {}, workflowOwned = !!(run.execution_proposal_id || run.operation_kind || run.operation_id || run.review_id);
        if (decision === "CANCEL" && !workflowOwned && !confirm("取消整个运行（原审批 CANCEL）？这会沿原 Core 审批合同取消此准确 Run，不只是拒绝当前请求。已有记录将保留，最终状态以 Core 回读为准。")) return;
        url = `/ui/api/runs/${encodeURIComponent(runID)}/approvals/${encodeURIComponent(approval.id)}/decide?${query({project_id: project})}`;
        body = {decision: {type: decision}};
      } else if (button.hasAttribute("data-session-cancel")) {
        const target = snapshot?.cancellation_capability?.target;
        if (!target || target.runId !== runID || !detail().run?.agent_execution_id || !confirm("取消此准确运行？Core 将请求停止并保留历史；受理不是终态。")) return;
        url = `/ui/api/executions/${encodeURIComponent(detail().run.agent_execution_id)}/cancel?${query({project_id: project})}`;
        body = {runId: target.runId, requestId: crypto.randomUUID(), ...(target.expectedUpperVersion ? {expectedUpperVersion: target.expectedUpperVersion} : {})};
      } else return;
      blockedWrites.add(key); writeBusy = true; paintNotice();
      try {await api(url, {method: "POST", body: JSON.stringify(body)}); if (current(n)) notice = "操作已受理；投递、后端结果与终态以原 Core 回读为准。";}
      catch (error) {if (current(n)) notice = `${error.message} · 结果可能未知，先回读；此页面不会自动重发。`;}
      finally {if (current(n)) {writeBusy = false; await readSnapshot(); paintNotice();}}
    }
    function click(event) {
      const button = event.target.closest("button"); if (!button || button.disabled) return;
      if (button.hasAttribute("data-session-run")) {listOpen = false; host.querySelector(".session-workbench")?.setAttribute("data-session-list-open", "false"); select(button.dataset.sessionRun); return;}
      if (button.hasAttribute("data-session-page")) {offset = Number(button.dataset.sessionPage); persist(); readList(); return;}
      if (button.hasAttribute("data-session-tab")) {savePosition(); tab = button.dataset.sessionTab; persist(); paint(); host.querySelector("[data-session-scroll]").scrollTop = scrolls.get(project + runID + tab) || 0; if (tab === "instructions" && !prompt) readPrompt(); return;}
      if (button.hasAttribute("data-session-refresh")) {refreshList(); return;}
      if (button.hasAttribute("data-session-close")) {select(""); return;}
      if (button.hasAttribute("data-session-list-open")) {listOpen = true; host.querySelector(".session-workbench")?.setAttribute("data-session-list-open", "true"); (host.querySelector("[data-session-scope]") || host.querySelector("[data-session-list-close]"))?.focus(); return;}
      if (button.hasAttribute("data-session-list-close")) {listOpen = false; host.querySelector(".session-workbench")?.setAttribute("data-session-list-open", "false"); host.querySelector("[data-session-list-open]")?.focus(); return;}
      if (button.hasAttribute("data-session-technical-open")) {technicalOpen = true; host.querySelector(".session-workbench")?.setAttribute("data-session-technical-open", "true"); host.querySelector("[data-session-technical-close]")?.focus(); return;}
      if (button.hasAttribute("data-session-technical-close")) {technicalOpen = false; host.querySelector(".session-workbench")?.setAttribute("data-session-technical-open", "false"); host.querySelector("[data-session-technical-open]")?.focus(); return;}
      if (button.hasAttribute("data-session-scope-all")) {scope = "all"; offset = 0; persist(); readList(); return;}
      if (button.hasAttribute("data-session-scope-history")) {scope = "history"; offset = 0; persist(); readList(); return;}
      if (button.hasAttribute("data-session-clear-area")) {area = ""; filterNotice = ""; offset = 0; (filterHost || host).querySelector("[data-session-area]").value = ""; persist(); readList(); return;}
      if (button.hasAttribute("data-session-first-page")) {offset = 0; persist(); readList(); return;}
      if (button.hasAttribute("data-session-steer")) {submitSteering(); return;}
      if (button.hasAttribute("data-session-native")) {nativeHistory(); return;}
      if (button.hasAttribute("data-session-history")) {history(); return;}
      if (button.hasAttribute("data-session-gap")) {history(Number(button.dataset.sessionGap)); return;}
      if (button.hasAttribute("data-session-new")) {const box = host.querySelector("[data-session-scroll]"); box.scrollTop = box.scrollHeight; newContent = false; button.hidden = true; return;}
      if (button.hasAttribute("data-session-copy") && prompt?.items?.[Number(button.dataset.sessionCopy)]?.available) {navigator.clipboard.writeText(prompt.items[Number(button.dataset.sessionCopy)].text || "").catch(() => {notice = "复制失败"; paintNotice();}); return;}
      if (button.hasAttribute("data-session-write")) submit(button);
    }
    function keydown(event) {if (event.key !== "Escape") return; if (technicalOpen) {technicalOpen = false; host?.querySelector(".session-workbench")?.setAttribute("data-session-technical-open", "false"); host?.querySelector("[data-session-technical-open]")?.focus();} else if (listOpen) {listOpen = false; host?.querySelector(".session-workbench")?.setAttribute("data-session-list-open", "false"); host?.querySelector("[data-session-list-open]")?.focus();}}
    function change(event) {
      if (event.target.matches("[data-session-scope]")) scope = event.target.value;
      else if (event.target.matches("[data-session-area]")) area = event.target.value;
      else return;
      filterNotice = ""; offset = 0; persist(); readList();
    }
    function pagehide() {stop();}
    function offline() {if (!runID) {streamState = "idle"; detailError = ""; detailErrorCode = ""; paintNotice(); return;} online = false; streamLive = false; streamState = terminal(detail().run?.status) ? "closed" : "failed"; detailError = streamState === "failed" ? t("session.offline") : ""; detailErrorCode = streamState === "failed" ? "BROWSER_OFFLINE" : ""; streamController?.abort(); clearTimeout(watchdogTimer); paintNotice(); if (streamState === "failed") scheduleReconnect(generation);}
    return {mount, stop, refresh, refreshList, select, selectExactRun: select};
  }
  root.DevBridgeSessionWorkbench = {create, decisionPayloadSHA256, progressSummary, nativeApprovalLabel, nativeApprovalResolution};
})(window);
