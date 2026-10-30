"use strict";

(function (root) {
  const Status = root.DevBridgeStatus || (typeof module !== "undefined" && module.exports ? require("./product-status.js") : null);
  const esc = (value) => String(value ?? "").replace(/[&<>'"]/g, (character) => ({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[character]));

  const taskPhaseLabel = (task,t) => Status.taskPhaseLabel(task,t);
  const runPurposeLabel = (purpose,t) => Status.runPurposeLabel(purpose,t);
  const progressLabel = (status,t,phase) => Status.progressLabel(status,t,phase);
  const statusText = (domain,raw,t) => Status.statusMaps[domain]?.[raw] ? Status.presentStatus(domain,raw,t).label : raw || t("workbench.unavailable");
  function statusBadge(domain, raw, t) {
    const status = Status.presentStatus(domain, raw, t);
    return `<span class="status-badge ${status.category}" title="${esc(status.explanation)}"><span class="status-dot" aria-hidden="true"></span>${esc(status.label)}</span>`;
  }

  function taskNextStep(task) {
    if(task?.verificationDecisions?.hold||task?.verificationDecisions?.completion)return null;
    const n=task?.nextStep;
    if(task?.status!=='ACTIVE'||task.terminal||n?.taskId!==task.id||!n.runId||!['IMPLEMENTATION','VERIFICATION','SCOPE_REVIEW'].includes(n.purpose))return null;
    if(n.code==='RESOLVE_RUNTIME_APPROVAL'){const a=task.activity?.runtime;return a?.runId===n.runId&&a.taskId===task.id&&a.taskDefinitionRevisionId===n.taskDefinitionRevisionId&&a.connectionState==='CONNECTED'&&a.requests?.some(q=>q.kind==='RUNTIME_APPROVAL'&&q.state==='PENDING'&&q.runId===a.runId&&q.threadId===a.threadId&&q.turnId===a.turnId)?n:null;}
    if(n.code==='ANALYZE_RUN_OUTPUT')return ['IMPLEMENTATION','VERIFICATION'].includes(n.purpose)?n:null;
    if(!task.currentExecution&&!task.currentReview&&task.runRecovery){const k=task.runRecovery,owner=n.purpose==='SCOPE_REVIEW'?task.currentReviewId===n.reviewId:task.currentExecutionId===n.executionId;if(!owner||task.current_definition_revision_id!==n.taskDefinitionRevisionId||k.runId!==n.runId||k.purpose!==n.purpose)return null;return ({RECOVER_RUN_RESULT:k.required&&k.resultState==='UNKNOWN',PROCESS_RUN_RESULT:!k.required&&['RECEIVED','NOT_STARTED'].includes(k.resultState),CHECK_RUN_RESULT:!k.required&&k.resultState==='UNAVAILABLE'})[n.code]?n:null;}
    if(n.purpose==='SCOPE_REVIEW'){const review=task.currentReview,k=task.runRecovery;if(review?.id!==n.reviewId||review.taskId!==task.id||review.run?.id!==n.runId||k?.runId!==n.runId||review.run.nativeResultState!==k.resultState)return null;return ({RECOVER_RUN_RESULT:k.required&&k.resultState==='UNKNOWN',PROCESS_RUN_RESULT:!k.required&&['RECEIVED','NOT_STARTED'].includes(k.resultState),CHECK_RUN_RESULT:!k.required&&k.resultState==='UNAVAILABLE'})[n.code]?n:null;}
    const e=task.currentExecution,r=n.purpose==='IMPLEMENTATION'?task.implementationRun:task.verificationRun,k=task.runRecovery;
    if(!e||e.id!==n.executionId||e.taskDefinitionRevisionId!==n.taskDefinitionRevisionId||task.details?.taskDefinitionRevisionId!==n.taskDefinitionRevisionId||r?.id!==n.runId||k?.runId!==n.runId||k.purpose!==n.purpose||r.nativeResultState!==k.resultState)return null;
    return ({RECOVER_RUN_RESULT:k.required&&k.resultState==='UNKNOWN',PROCESS_RUN_RESULT:!k.required&&['RECEIVED','NOT_STARTED'].includes(k.resultState),CHECK_RUN_RESULT:!k.required&&k.resultState==='UNAVAILABLE'})[n.code]?n:null;
  }
  function taskStatusBadge(task,t) {
    if(task?.verificationDecisions?.hold)return `<span class="status-badge warning">已暂存</span>`;
    if(task?.verificationDecisions?.completion){const state=task.verificationDecisions.completion.state;return `<span class="status-badge ${state==="COMPLETED"?"success":"warning"}">${esc(state==="COMPLETED"?"已完成并合入":state==="BLOCKED"?"合入受阻":"正在合入")}</span>`;}
    const next=taskNextStep(task);if(next)return `<span class="status-badge warning" title="${esc(next.summary)}">${esc(next.label)}</span>`;
    if(task?.status==='ACTIVE'&&task.runRecovery)return `<span class="status-badge warning" title="${esc(task.runRecovery.summary)}">${esc(task.runRecovery.label)}</span>`;
    if(task?.status==="ACTIVE"&&(task.phase==="RECOVERY_REQUIRED"||task.recovery?.required))return `<span class="status-badge warning" title="${esc(task.currentActivity?.summary||task.activity?.summary||t("taskRecovery.blocked"))}"><span class="status-dot" aria-hidden="true"></span>${esc(t("taskRecovery.blocked"))}</span>`;
    return statusBadge("task",task?.status,t);
  }

  const featureAreaProductStatus = (area) => ["ACTIVE", "CLOSED"].includes(area?.product?.domainStatus) ? area.product.domainStatus : "";

  function featureAreaStatusBadge(area, t) {
    return statusBadge("featureAreaProduct", featureAreaProductStatus(area), t);
  }

  // agentOperationStateBadge projects Core's durable Agent operation state.
  // READY needs no badge; BUSY and RECOVERY_REQUIRED are distinct nonterminal
  // product states, never folded into the FeatureArea lifecycle badge.
  function agentOperationStateBadge(state, t) {
    const normalized = String(state || "").toUpperCase();
    if (normalized === "BUSY") return `<span class="status-badge active" title="${esc(t("agentOperation.busy.explanation"))}"><span class="status-dot" aria-hidden="true"></span>${esc(t("agentOperation.busy.label"))}</span>`;
    if (normalized === "RECOVERY_REQUIRED") return `<span class="status-badge warning" title="${esc(t("agentOperation.recoveryRequired.explanation"))}"><span class="status-dot" aria-hidden="true"></span>${esc(t("agentOperation.recoveryRequired.label"))}</span>`;
    return "";
  }

  function emptyState(title, body) {
    return `<div class="empty-state"><span class="empty-mark" aria-hidden="true">◇</span><h2>${esc(title)}</h2><p>${esc(body)}</p></div>`;
  }

  // Keep an exact, escaped snapshot for the read-only modal. JSON encoding
  // preserves newlines and quotes across HTML attribute parsing.
  function resultPreview(value, t, {title = t("result.full"), reviewID = ""} = {}) {
    if (value == null || value === "") return "";
    const text = typeof value === "string" ? value : JSON.stringify(value, null, 2);
    const compact = text.replace(/\s+/g, " ").trim();
    const excerpt = Array.from(compact).slice(0, 80).join("");
    const action = reviewID ? `data-view-full-review-result="${esc(reviewID)}"` : `data-view-full-result data-result-content="${esc(JSON.stringify(text))}"`;
    return `<div class="result-preview"><pre class="result-preview-text">${esc(excerpt)}${excerpt.length < compact.length ? "…" : ""}</pre><button type="button" class="text-button" ${action} data-result-title="${esc(title)}" aria-label="${esc(t("result.viewAll") + " · " + title)}">${esc(t("result.viewAll"))}</button></div>`;
  }

  function needsAnalysisRefresh(detail) {
    const state=detail?.analysis?.state||detail?.run?.analysis_state;
    return ["SUCCEEDED","FAILED","INFRASTRUCTURE_FAILED","CANCELED"].includes(detail?.run?.status)&&(["REQUIRED","OPTIONAL"].includes(state)||!!detail?.analysis_read_error);
  }
  // Render persisted GPT authorship and native Codex output as separate sources.
  // Every string stays escaped; report content never becomes executable HTML.
  function runOutput(detail, t) {
    const r=detail.run||{},analysis=detail.analysis,state=analysis?.state||r.analysis_state||"";
    const native=detail.native_output??detail.evidence?.agent_claim??detail.review_result??"";
    const labels={summary:"分析结论",reviewDisposition:"审查建议",findings:"发现的问题",limitations:"限制与未验证项",recommendedNextSteps:"建议后续步骤",checks:"检查结果",acceptanceCriteria:"验收标准",workSpec:"实施方案",verificationSpec:"验证方案",goal:"目标",constraints:"约束",codexInstruction:"执行要求",executionScope:"执行范围",mode:"方式",scope:"范围",command:"命令",kind:"类型",required:"必需",status:"结果",evidence:"依据",reason:"原因",title:"标题",severity:"级别",path:"文件",index:"序号"};
    const field=(value,depth=0)=>{
      if(value===null||value===undefined)return '<span>—</span>';
      if(typeof value!=="object")return `<pre class="run-output-text">${esc(String(value))}</pre>`;
      if(depth>8)return `<pre class="run-output-text">${esc(JSON.stringify(value,null,2))}</pre>`;
      if(Array.isArray(value))return value.length?`<ol>${value.map(item=>`<li>${field(item,depth+1)}</li>`).join("")}</ol>`:'<p>无</p>';
      return `<dl class="analysis-fields">${Object.entries(value).map(([key,item])=>`<div><dt>${esc(labels[key]||key)}</dt><dd>${field(item,depth+1)}</dd></div>`).join("")}</dl>`;
    };
    let body="";
    if(detail.analysis_read_error)body+=`<section class="run-gpt-analysis"><h3>GPT 分析</h3><p role="alert">${esc(detail.analysis_read_error)}</p></section>`;
    else if(state==="RECORDED"){
      const report=analysis?.report;
      body+=`<section class="run-gpt-analysis"><h3>GPT 分析</h3><p class="muted">已入库${analysis?.receipt?.submittedAt?` · ${esc(analysis.receipt.submittedAt)}`:""} · 审批与验收以任务当前状态为准</p>`;
      if(report&&typeof report==="object"){
        if(report.summary)body+=field(report.summary);
        for(const key of ["reviewDisposition","findings","limitations","recommendedNextSteps","checks","acceptanceCriteria","workSpec","verificationSpec"]){
          if(report[key]!==undefined)body+=`<details class="analysis-section"${["checks","acceptanceCriteria","findings"].includes(key)?" open":""}><summary>${esc(labels[key])}</summary>${field(report[key])}</details>`;
        }
        body+=`<details class="analysis-section"><summary>完整分析报告（原始 JSON）</summary><pre class="run-output-text">${esc(JSON.stringify(report,null,2))}</pre></details>`;
      }else body+='<p role="alert">分析已记录，但正文尚未读取；请刷新重试。</p>';
      body+='</section>';
    }else if(state==="REQUIRED"||state==="OPTIONAL")body+=`<section class="run-gpt-analysis"><h3>GPT 分析</h3><p role="status">${state==="REQUIRED"?"Codex 已执行完成，等待 GPT 提交分析。":"尚未提交 GPT 分析。"} 对话中未提交到 Core 的分析不会出现在这里。</p></section>`;
    if(native)body+=`<details class="run-native-output"${state!=="RECORDED"?" open":""}><summary>Codex 原始输出</summary><pre class="run-output-text">${esc(native)}</pre>${detail.native_output_truncated||detail.evidence?.agent_claim_truncated?'<p role="status">输出超过页面读取上限，当前内容已截断；完整原文请查看该运行会话。</p>':""}</details>`;
    if(!body)return '<section class="run-output"><h3>执行结果</h3><p>尚无已记录的执行输出或 GPT 分析。</p></section>';
    return `<section class="run-output" aria-label="执行输出与 GPT 分析">${body}</section>`;
  }

  function copyButton(value, label, t) {
    if (!value) return "";
    return `<button type="button" class="copy-button" data-copy="${esc(value)}" aria-label="${esc(t("action.copy", {label}))}">${esc(t("action.copy", {label}))}</button>`;
  }

  function localizedTimestamp(value, locale, fallback) {
    if (!value) return esc(fallback);
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return esc(fallback);
    return `<time datetime="${esc(value)}">${esc(date.toLocaleString(locale, {dateStyle: "medium", timeStyle: "short"}))}</time>`;
  }

  function number(value, locale) {
    const parsed = Number(value);
    if (!Number.isFinite(parsed) || parsed < 0) return null;
    return new Intl.NumberFormat(locale || "en").format(parsed);
  }

  function compactNumber(value) {
    const parsed = Number(value);
    if (!Number.isFinite(parsed) || parsed < 0) return null;
    if (parsed >= 1000000000) return trimFraction(parsed / 1000000000) + "B";
    if (parsed >= 1000000) return trimFraction(parsed / 1000000) + "M";
    if (parsed >= 1000) return trimFraction(parsed / 1000) + "k";
    return String(Math.round(parsed));
  }

  function trimFraction(value) {
    const fixed = value.toFixed(1);
    return fixed.endsWith(".0") ? fixed.slice(0, -2) : fixed;
  }

  function durationLabel(milliseconds) {
    const parsed = Number(milliseconds);
    if (!Number.isFinite(parsed) || parsed < 0) return null;
    const totalSeconds = Math.round(parsed / 1000);
    if (totalSeconds < 60) return `${totalSeconds}s`;
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    const pad = (value) => String(value).padStart(2, "0");
    return hours ? `${hours}:${pad(minutes)}:${pad(seconds)}` : `${pad(minutes)}:${pad(seconds)}`;
  }

  function projectMatches(project, needle) {
    if (!needle) return true;
    return String(project.name || "").toLocaleLowerCase().includes(needle);
  }

  function moduleMatches(module, needle) {
    if (!needle) return true;
    const corpus = [
      module.name, module.description,
      ...(module.facts || []).flatMap((fact) => [fact.repository_name, fact.responsibility, ...(fact.production_roots || []), ...(fact.entrypoints || [])])
    ].filter(Boolean).join("\n").toLocaleLowerCase();
    return corpus.includes(needle);
  }

  function sectionHeader(title, subtitle, action = "") {
    return `<div class="section-heading"><div><h2>${esc(title)}</h2>${subtitle ? `<p>${esc(subtitle)}</p>` : ""}</div>${action}</div>`;
  }

  // projectTabToolbar follows an already-labelled Project tab. It retains
  // useful guidance and controls without mechanically repeating the active
  // tab name as another heading.
  function projectTabToolbar(subtitle, action = "") {
    return `<div class="project-tab-toolbar">${subtitle ? `<p>${esc(subtitle)}</p>` : ""}${action}</div>`;
  }

  function keyValueDetails(entries) {
    return `<dl class="key-values">${entries.map(([key, value]) => `<div><dt>${esc(key)}</dt><dd>${value || "—"}</dd></div>`).join("")}</dl>`;
  }

  function detailDisclosure(summary, body, open = false) {
    return `<details class="disclosure"${open ? " open" : ""}><summary>${esc(summary)}</summary><div class="disclosure-body">${body}</div></details>`;
  }

  function topicCard(topic, t, locale) {
    return `<article class="product-card topic-card">
      <div class="card-heading"><div><span class="eyebrow">${esc(t("topics.current"))}</span><h3>${esc(topic.title)}</h3></div>${featureAreaStatusBadge(topic, t)}</div>
      <p>${esc(topic.summary || t("common.unknown"))}</p>
      <div class="card-footer"><span class="muted">${esc(t("common.updated"))} · ${localizedTimestamp(topic.updated_at, locale, t("common.unknown"))}</span><button class="text-button" type="button" data-log-kind="topic" data-log-id="${esc(topic.id)}" data-log-label="${esc(topic.title)}">${esc(t("action.viewLogs"))}</button></div>
    </article>`;
  }

  function pathList(values, emptyText) {
    if (!values || !values.length) return `<span class="muted">${esc(emptyText)}</span>`;
    return `<ul class="path-list">${values.map((value) => `<li><code title="${esc(value)}">${esc(value)}</code></li>`).join("")}</ul>`;
  }

  function moduleCard(module, t, expanded = false) {
    const facts = module.facts || [];
    const rootCount = facts.reduce((sum, fact) => sum + (fact.production_roots || []).length, 0);
    const relationshipCount = facts.reduce((sum, fact) => sum + (fact.related_areas || []).length, 0);
    const repositoryCount = new Set(facts.map((fact) => fact.repository_name)).size;
    const responsibility = (facts[0] && facts[0].responsibility) || module.description || "";
    const summary = [module.name, module.description, ...facts.flatMap((fact) => [fact.repository_name, fact.responsibility, ...(fact.production_roots || []), ...(fact.entrypoints || []), ...(fact.related_areas || [])])].filter(Boolean).join("\n").replace(/[&<>'"]/g, (character) => ({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[character]));
    const meta = [t("projects.repositories", {count: repositoryCount}), `${rootCount} ${t("modules.roots")}`, `${relationshipCount} ${t("modules.related")}`];
    return `<div class="module-card" data-module-search="${esc(summary.toLocaleLowerCase())}" data-module-name="${esc(module.name)}">
      <button type="button" class="module-summary" data-module-toggle aria-expanded="${expanded}"><span class="module-chevron${expanded ? " expanded" : ""}" aria-hidden="true">${expanded ? "▾" : "▸"}</span><span class="module-name">${esc(module.name)}</span><span class="module-responsibility">${esc(responsibility)}</span><span class="module-meta">${esc(meta.join(" · "))}</span></button>
      <div class="module-details"${expanded ? "" : " hidden"}>${(facts || []).map((fact) => `<div class="module-facts">
        ${keyValueDetails([[t("modules.repository"), `<strong>${esc(fact.repository_name)}</strong>`], [t("modules.responsibility"), esc(fact.responsibility)]])}
        <div class="fact-columns"><div><h4>${esc(t("modules.roots"))}</h4>${pathList(fact.production_roots, t("common.none"))}</div><div><h4>${esc(t("modules.entrypoints"))}</h4>${pathList(fact.entrypoints, t("modules.noEntrypoints"))}</div><div><h4>${esc(t("modules.related"))}</h4>${pathList(fact.related_areas, t("modules.noRelated"))}</div></div>
      </div>`).join("")}</div>
    </div>`;
  }

  function reviewListUsage(review, t, locale) {
    const usage = review && review.usage;
    if (!usage) return `<span class="usage-muted">—</span>`;
    const status = usageStatus(usage, t);
    const counts = usageCounts(usage);
    const total = Number(counts && counts.total_tokens);
    const tokens = counts && counts.total_tokens != null && Number.isFinite(total) && total >= 0 ? compactNumber(total) : "—";
    const duration = durationLabel(usage.duration_ms);
    const durationText = duration == null ? "" : `${t("usage.duration")} ${duration}`;
    return `<span class="review-usage" title="${esc([...buildUsageFacts(usage, t, locale).map(([label, value]) => `${label}: ${value}`), durationText].filter(Boolean).join("\n"))}">${esc(status)} · ${esc(tokens)}${durationText ? `<span class="muted"> · ${esc(durationText)}</span>` : ""}</span>`;
  }

  // workKindBadge renders the optional organization-only Task work kind as a
  // compact classification badge. Unclassified Tasks render no badge.
  function workKindBadge(kind, t) {
    if (!kind) return "";
    const normalized = String(kind).toUpperCase();
    const known = new Set(["FEATURE", "BUG_FIX", "REFACTOR", "MAINTENANCE", "TEST_CHANGE", "DOCUMENTATION", "OTHER"]);
    if (!known.has(normalized)) return "";
    const key = `workKind.${normalized}`;
    const label = t(key);
    const shown = label === `[${key}]` ? normalized : label;
    return `<span class="work-kind-badge ${esc(normalized.toLowerCase().replace(/[^a-z0-9]/g, "_"))}" title="${esc(normalized)}">${esc(shown)}</span>`;
  }

  // Default model IDs are localized; bounded custom and historical IDs remain
  // visible verbatim even when they are no longer in the current inventory.
  function modelLabel(model, t) {
    if (!model) return "";
    const key = `workKind.model.${model}`;
    const label = t(key);
    return label === `[${key}]` ? String(model) : label;
  }

  function reasoningLabel(effort, t) {
    if (!effort) return "";
    const key = `workKind.reasoning.${effort}`;
    const label = t(key);
    return label === `[${key}]` ? "" : label;
  }

  function buildUsageFacts(usage, t, locale) {
    const facts = [];
    if (usage == null) return facts;
    facts.push([t("usage.status"), usageStatus(usage, t)]);
    const counts = usageCounts(usage);
    for (const [label, value] of [[t("usage.input"), counts && counts.input_tokens], [t("usage.cachedInput"), counts && counts.cached_input_tokens], [t("usage.nonCachedInput"), nonCachedInputTokens(counts)], [t("usage.output"), counts && counts.output_tokens], [t("usage.reasoningOutput"), counts && counts.reasoning_output_tokens], [t("usage.total"), counts && counts.total_tokens]]) {
      facts.push([label, value == null || value < 0 ? "—" : number(value, locale)]);
    }
    return facts;
  }

  function usageCounts(usage) {
    return usage && String(usage.status || "").toUpperCase() === "AVAILABLE" ? usage.usage || null : null;
  }

  // usageStatus is separate from the optional count fields. In particular, an
  // observed AVAILABLE total of zero is not the same fact as unavailable or
  // still-pending accounting, and absent optional breakdown fields stay unknown.
  function usageStatus(usage, t) {
    const status = String(usage && usage.status || "").toUpperCase();
    const key = `usage.status.${status}`;
    const label = t(key);
    return label === `[${key}]` ? (status || t("common.unknown")) : label;
  }

  // nonCachedInputTokens is a UI-derived fact: input minus cached input,
  // clamped at zero. It is never persisted and renders "—" when either
  // component is unknown.
  function nonCachedInputTokens(usage) {
    if (!usage || usage.input_tokens == null || usage.cached_input_tokens == null) return null;
    return Math.max(Number(usage.input_tokens) - Number(usage.cached_input_tokens), 0);
  }

  function eventSummary(entry, t) {
    const key = `event.${entry.event}`;
    const translated = t(key);
    return translated === `[${key}]` ? t("event.default", {component: entry.component || "Bridge"}) : translated;
  }

  // reviewListCell renders the latest-review cell for Feature Area rows. A
  // blocked or failed Review shows its durable blocker reason verbatim
  // (bounded to one line with the full text in a modal); the reason text is
  // never translated or rewritten.
  function reviewListCell(review, t) {
    if (!review) return `<span class="muted">${esc(t("common.none"))}</span>`;
    const badge = statusBadge("product", review.state, t);
    const reason = String(review.blocker_reason || "").trim();
    if (!["BLOCKED", "FAILED"].includes(String(review.state || "").toUpperCase()) || !reason) return badge;
    return `${badge}${resultPreview(reason, t, {title:t("workItems.blocker")})}`;
  }

  // These are read-only Core facts; cleanup state is never computed here.
  function worktreeInventory(inventory, t, locale) {
    if(!inventory)return "";
    const items=inventory.items||[];
    const text=key=>esc(t(`inventory.${key}`));
    const state=value=>text(`state.${value||"UNKNOWN"}`);
    const bool=(value,known)=>known ? text(value?"yes":"no") : state("UNKNOWN");
    const fact=(label,value)=>`<div><dt>${text(label)}</dt><dd>${value}</dd></div>`;
    return `<section class="worktree-inventory"><h3>Worktrees <span class="muted">${items.length}</span></h3><p class="muted">${text("refreshed")} ${localizedTimestamp(inventory.refreshedAt,locale,"—")}</p>${items.length ? items.map(item=>`<details class="worktree-item" data-cleanup-state="${esc(item.cleanupState)}"><summary><div class="worktree-heading"><strong>${text(`kind.${item.kind}`)}</strong><span>${state(item.cleanupState)}</span></div><code class="worktree-path">${esc(item.path)}</code><div class="worktree-summary"><span>${state(item.workingTreeState)}</span><span>${state(item.activityState)}</span><span>${state(item.integrationState)}</span><code>${esc(item.branchRef|| (item.detached?"detached HEAD":"—"))} · ${esc((item.head||"").slice(0,12)||"—")}</code></div></summary><dl class="worktree-facts">${fact("owner",esc([item.ownerType,item.ownerId].filter(Boolean).join(" · ")||"—"))}${fact("task",esc(item.taskId||"—"))}${fact("review",esc(item.reviewId||"—"))}${fact("repository",esc(item.repositoryId))}${fact("path",`<code>${esc(item.path)}</code>`)}${fact("branch",`<code>${esc(item.branchRef||"—")}</code>`)}${fact("head",`<code>${esc(item.head||"—")}</code>`)}${fact("gitRegistered",bool(item.gitRegistered,item.gitRegistrationKnown))}${fact("directoryExists",bool(item.directoryExists,item.directoryExistenceKnown))}${fact("workingTree",state(item.workingTreeState))}${fact("activity",state(item.activityState))}${fact("operation",esc(item.activeOperation ? [item.activeOperation.kind,item.activeOperation.id,item.activeOperation.runId].filter(Boolean).join(" · ") : "—"))}${fact("integration",state(item.integrationState))}${fact("integrationTarget",`<code>${esc(item.integrationTargetHead||"—")}</code>`)}${fact("cleanup",state(item.cleanupState))}${fact("lastActivity",localizedTimestamp(item.lastActivityAt,locale,"—"))}</dl>${(item.readErrors||[]).length ? `<p class="worktree-read-error">${text("partial")} · ${esc(item.readErrors.join(" · "))}</p>`:""}</details>`).join(""):`<p>${text("empty")}</p>`}</section>`;
  }

  const exported = {taskNextStep, taskPhaseLabel, runPurposeLabel, progressLabel, statusText, taskStatusBadge, resultPreview, runOutput, needsAnalysisRefresh, worktreeInventory, esc, statusBadge, featureAreaProductStatus, featureAreaStatusBadge, agentOperationStateBadge, emptyState, copyButton, localizedTimestamp, number, compactNumber, durationLabel, projectMatches, moduleMatches, sectionHeader, projectTabToolbar, keyValueDetails, detailDisclosure, topicCard, moduleCard, reviewListUsage, reviewListCell, buildUsageFacts, usageCounts, usageStatus, nonCachedInputTokens, eventSummary, workKindBadge, modelLabel, reasoningLabel};
  root.DevBridgeComponents = exported;
  if (typeof module !== "undefined" && module.exports) module.exports = exported;
})(typeof globalThis !== "undefined" ? globalThis : this);
