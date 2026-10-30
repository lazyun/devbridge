"use strict";
(function(root){
  const TASK_TABS=["overview","reviews","executions","runs"],RUN_TABS=["overview","activity","instructions","evidence","usage","diagnostics"];
  // Navigation state is separate from Core current pointers and fetched snapshots.
  function createRoute(){
    let route=null,generation=0,controller=null;
    const saved=new Map(),drafts=new Map();
    const key=r=>JSON.stringify([r.project,r.rootTask]);
    function navigate(value){controller?.abort();controller=null;route=value?{...value,generation:++generation}:null;if(!value)generation++;return route;}
    function task(project,rootTask,source="tasks"){return navigate({project,rootTask,view:"TASK",target:rootTask,source,activeTab:"overview",...(saved.get(key({project,rootTask}))||{})});}
    function save(snapshot){if(route?.view==="TASK"){Object.assign(route,snapshot);saved.set(key(route),{...snapshot,activeTab:route.activeTab});}}
    function diagnostic(project,id){return navigate({project,rootTask:"",view:"RUN",target:id,source:"diagnostics",activeTab:"overview"});}
    function run(id){if(!route)return null;return navigate({...route,view:"RUN",target:id,activeTab:"overview"});}
    function back(){if(!route?.rootTask)return null;return task(route.project,route.rootTask,route.source);}
    function read(){controller?.abort();controller=new AbortController();const captured=route,signal=controller.signal;return {route:captured,signal,current:()=>route===captured&&!signal.aborted};}
    function tab(value){if(route&&(route.view==="TASK"?TASK_TABS:RUN_TABS).includes(value))route.activeTab=value;}
    function draftKey(authority){return JSON.stringify([route?.project,route?.rootTask,authority]);}
    return {get:()=>route,task,run,diagnostic,back,save,read,tab,close:()=>navigate(null),drafts,draftKey};
  }
  function tabs(view,active,esc,t){const values=view==="TASK"?TASK_TABS:RUN_TABS;return `<div class="${view==="TASK"?"task-tabs":"run-tabs"}" role="tablist">${values.map(tab=>`<button type="button" role="tab" aria-selected="${active===tab}" aria-controls="${view.toLowerCase()}-tab-${tab}" id="${view.toLowerCase()}-tab-button-${tab}" data-${view==="TASK"?"task":"run"}-tab="${tab}">${esc(t(`workbench.${view==="TASK"?"task":"run"}.${tab}`))}</button>`).join("")}</div>`;}
  function duration(ms,complete,unknown){return ms===null||ms===undefined||!Number.isFinite(Number(ms))||Number(ms)<0?unknown:`${Math.floor(Number(ms)/60000)}:${String(Math.floor(Number(ms)/1000)%60).padStart(2,"0")}${complete===false?" …":""}`;}
  function runList(items,{C,t,locale,total=items.length,offset=0,limit=20,compact=false,filter="",truncated=false}){
    const esc=C.esc,visible=items.filter(r=>!filter||(r.runPurpose||r.purpose)===filter),rows=compact?visible.slice(0,5):visible;
    const purposes=["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION"];
    return `${!compact?`<label>${esc(t("workbench.runPurpose"))}<select data-run-filter><option value="">${esc(t("workbench.allRuns"))}</option>${purposes.map(p=>`<option value="${p}"${filter===p?" selected":""}>${p}</option>`).join("")}</select></label>`:""}${rows.length?`<div class="table-wrap"><table class="run-list-table"><thead><tr>${["runPurpose","owner","status","model","started","duration","actions"].map(k=>`<th>${esc(t("workbench."+k))}</th>`).join("")}</tr></thead><tbody>${rows.map(r=>`<tr data-run-key="run:${esc(r.id)}"><td><button type="button" class="text-button" data-task-run="${esc(r.id)}"><span>${esc(({SCOPE_REVIEW:t("workbench.task.reviews"),IMPLEMENTATION:t("phase.implementation"),VERIFICATION:t("phase.verification")})[r.runPurpose||r.purpose]||r.purpose)}</span><small><code>${esc(r.runPurpose||r.purpose)}</code></small></button></td><td><code>${esc(r.reviewId||r.taskExecutionId||r.operationId||"—")}</code></td><td>${C.statusBadge("run",r.transportStatus,t)}</td><td>${esc(r.model||t("workbench.unknown"))}<small>${esc(r.reasoningEffort||t("workbench.unknown"))}</small></td><td>${C.localizedTimestamp(r.startedAt,locale,t("workbench.unknown"))}</td><td>${esc(duration(r.elapsedMs,r.durationComplete,t("workbench.unknown")))}</td><td class="run-row-actions">${r.threadId?`<button type="button" class="text-button" data-open-run-session="${esc(r.id)}">查看会话</button>`:""}${[["instructions",t("workbench.viewPrompt")],["evidence",t("workbench.viewResult")]].map(([tab,label])=>`<button type="button" class="text-button" data-task-run="${esc(r.id)}" data-run-open-tab="${tab}">${esc(label)}</button>`).join("")}</td></tr>`).join("")}</tbody></table></div>`:`<p>${esc(t("workbench.emptyRuns"))}</p>`}${compact&&total>rows.length?`<button type="button" class="text-button" data-task-tab="runs">${esc(t("workbench.allRuns"))} (${esc(total)})</button>`:""}${truncated?`<p class="muted">${esc(t("workbench.boundedRuns"))}</p>`:""}${!compact?`<nav class="pagination" aria-label="Runs"><button type="button" data-task-runs-page="${Math.max(0,offset-limit)}"${offset===0?" disabled":""}>${esc(t("workbench.previous"))}</button><span>${esc(items.length?offset+1:0)}–${esc(offset+items.length)} / ${esc(total)}</span><button type="button" data-task-runs-page="${offset+limit}"${offset+items.length>=total?" disabled":""}>${esc(t("workbench.next"))}</button></nav>`:""}`;
  }
  function taskOutcome(v,{C,t}) {
    const esc=C.esc,unknown=t("workbench.unavailable"),method=({DIRECT_FILE:t("workbench.directFile"),CODING_AGENT:t("workbench.codingAgent")})[v.implementationMode]||unknown;
    const code=v.commitSummary||({COMMITTED:t("commit.resultCommitted"),UNCOMMITTED:t("commit.resultUncommitted")})[v.commitStatus]||unknown;
    const disposition=v.verificationDecisions,decisionLabel=disposition?.hold?"已暂存":disposition?.completion?({COMPLETED:"已完成并合入",BLOCKED:"合入受阻",DELIVERING:"正在提交并合入"})[disposition.completion.state]:"";
    const dev=disposition?.completion?.state==="COMPLETED"?"已合入":v.devSummary||({MERGED:t("commit.resultMerged"),UNMERGED:t("commit.resultUnmerged")})[v.devIntegrationStatus]||unknown;
    return `<section class="workbench-section task-outcome" aria-label="${esc(t("workbench.outcome"))}"><h3>${esc(t("workbench.outcome"))}</h3><div class="task-outcome-grid">${[[t("workbench.status"),decisionLabel||C.taskNextStep(v)?.label||v.runRecovery?.label||(v.status==="ACTIVE"&&(v.phase==="RECOVERY_REQUIRED"||v.recovery?.required)?t("taskRecovery.blocked"):C.statusText("task",v.status,t))],[t("workbench.implementationMethod"),method],[t("workbench.gitCommit"),code],[t("workbench.integration"),dev]].map(([label,value])=>`<div><span>${esc(label)}</span><strong>${esc(value)}</strong></div>`).join("")}</div>${v.verification?.summary?`<div class="task-verification-summary"><h4>${esc(t("workbench.verificationResult"))}</h4><p>${esc(v.verification.summary)}</p></div>`:""}</section>`;
  }
  // Historical facts only: these renderers never render a Decision or capabilities.
  function reviewHistory(view,{C,t}) {
    const esc=C.esc,items=view.reviewHistory,unknown=t("workbench.unavailable");
    if(!Array.isArray(items))return `<p role="alert">${esc(t("workbench.readFailed"))}</p>`;
    if(!items.length)return `<p>${esc(t("workbench.emptyReviews"))}</p>`;
    return items.map(r=>`<section class="workbench-section" data-section-key="review:${esc(r.id)}"><h3>#${esc(r.ordinal)} · ${esc(r.activity?.executionMethod||({UPPER_LLM:"GPT 审查",CODING_AGENT:"Codex 审查",PI_AGENT:"Pi Agent 审查"})[r.executorKind]||r.executorKind||unknown)} ${r.id===view.currentReview?.id?`<span class="status-badge">${esc(t("workbench.current"))}</span>`:""}</h3>${r.activity?.summary?`<p>${esc(r.activity.summary)}</p>`:""}${r.activity?.notice?`<p role="status">${esc(r.activity.notice)}</p>`:""}${C.keyValueDetails([
      [t("workbench.reviewDepth"),esc(["FAST","DEEP"].includes(r.reviewDepth)?t("reviewDepth."+r.reviewDepth):r.reviewDepth||unknown)],
      [t("workbench.status"),esc(C.statusText("run",r.status,t))],
      [t("workbench.resultAvailability"),esc(t(r.resultAvailable===true?"workbench.available":r.resultAvailable===false?"workbench.notGenerated":"workbench.unavailable"))],
      [t("workbench.linkedRun"),historyRun(r.run,{C,t})],
      ...(r.executorKind==="UPPER_LLM"?[[t("workbench.executorModel"),esc(r.executorMetadata?.model||unknown)],[t("workbench.provenance"),esc(r.executorMetadata?.provenance&&r.executorMetadata.provenance!=="UNAVAILABLE"?r.executorMetadata.provenance:t("workbench.metadataUnrecorded"))],[t("usage.title"),esc(unknown)]]:[])
    ])}${r.resultAvailable?`<button type="button" class="text-button" data-view-full-review-result="${esc(r.id)}">${esc(t("review.fullResult"))}</button>`:""}${r.failureSummary?C.resultPreview(r.failureSummary,t):""}</section>`).join("");
  }
  function taskResults(view,{C,t}) {
    const rows=[["审查",view.currentReview?.run?.id],["实施",view.currentExecution?.implementationRunId],["验证",view.currentExecution?.verificationRunId]].filter(([,id])=>id);
    if(!rows.length)return "";
    return `<section class="workbench-section task-results"><h3>执行输出与 GPT 分析</h3><p class="muted">选择阶段查看 Codex 原始输出与已提交的 GPT 分析报告。</p><div class="detail-actions">${rows.map(([label,id])=>`<button type="button" data-task-run="${C.esc(id)}" data-run-open-tab="evidence">查看${C.esc(label)}结果与分析</button>`).join("")}</div></section>`;
  }
  function historyRun(run,{C,t},id="") {
    const runID=run?.id||id;
    return runID?`<button type="button" class="text-button" data-task-run="${C.esc(runID)}" title="${C.esc(runID)}">${C.esc(t("workbench.runRecord"))}</button> · ${C.esc(C.statusText("run",run?.transportStatus,t))}`:C.esc(t("workbench.unavailable"));
  }
  function executionHistory(view,{C,t}) {
    const esc=C.esc,items=view.executionHistory,unknown=t("workbench.unavailable");
    const title=[view.title,view.goal].find(value=>typeof value==="string"&&value.trim())?.trim()||t("workbench.task.executions");
    if(!Array.isArray(items))return `<p role="alert">${esc(t("workbench.readFailed"))}</p>`;
    if(!items.length)return `<p>${esc(t("workbench.emptyExecutions"))}</p>`;
    return items.map(h=>{const e=h.execution;return `<section class="workbench-section" data-history-key="execution:${esc(e.id)}"><h3>#${esc(h.ordinal)} · ${esc(title)}</h3>${e.summary?`<p>${esc(e.summary)}</p>`:""}${C.keyValueDetails([
      ["Execution ID",`<code>${esc(e.id)}</code>`],
      [t("workbench.definitionRevision"),esc(e.taskDefinitionRevisionId||unknown)],
      [t("workbench.sourceAction"),esc(h.sourceAction||unknown)],
      ...(h.supersededByReviewRequestId?[["已被补审请求替代",esc(h.supersededByReviewRequestId)]]:[]),
      ...(h.predecessorExecutionId?[["前驱 Execution",esc(h.predecessorExecutionId)],["来源决定",esc(h.sourceDecisionId||unknown)]]:[]),
      ["运行配置",esc(JSON.stringify(e.runtimeConfig||{}))],
      [t("workbench.status"),esc(["DRAFT","PREPARING","AWAITING_CONFIRMATION","AWAITING_AUTHORIZATION","READY","CONSUMED","CANCELED","FAILED"].includes(e.state)?t("executionState."+e.state):e.state||unknown)],
      [t("workbench.executionOutcome"),esc(C.statusText("run",e.outcome,t))],
      ["Failure code",esc(e.failureCode||unknown)],
      [t("phase.implementation"),historyRun(h.implementationRun,{C,t},e.implementationRunId)],
      [t("phase.verification"),historyRun(h.verificationRun,{C,t},e.verificationRunId)]
    ])}<details data-section-key="execution:${esc(e.id)}"><summary>${esc(t("workbench.acceptanceFact"))}</summary>${h.acceptance?C.resultPreview(h.acceptance,t):`<p>${esc(unknown)}</p>`}</details></section>`;}).join("");
  }
  function renderRun(detail,{C,t,locale,activeTab="overview",prompt=null,promptError="",rootTask="",source="tasks"}){
    const esc=C.esc,r=detail.run||{},f=detail.frozen_config||{},unknown=t("workbench.unknown");
    const pre=value=>`<pre>${esc(JSON.stringify(value,null,2)?.slice(0,131072)||t("workbench.unavailable"))}</pre>`;
    const facts=rows=>C.keyValueDetails(rows.map(([k,v])=>[k,esc(v??unknown)]));
    const purpose=r.run_purpose||r.purpose||unknown;
    let content="";
    if(activeTab==="overview")content=facts([[t("workbench.runPurpose"),C.runPurposeLabel(purpose,t)],[t("workbench.owner"),r.review_id||r.task_execution_id||r.execution_proposal_id||r.operation_id],[t("workbench.status"),C.statusText("run",r.status,t)],[t("workbench.model"),f.model||unknown],[t("workbench.reasoning"),f.reasoning_effort||unknown],["Config source",f.source||unknown],["TaskDefinitionRevision",f.task_definition_revision_id||unknown],[t("workbench.started"),r.started_at||unknown],[t("workbench.duration"),duration(detail.elapsed_ms,detail.duration_complete,unknown)]])+C.resultPreview(detail.failure,t)+pre({usage:r.token_usage||{status:"UNAVAILABLE"}});
    // Preserve object payloads and never render request forms inside Run details.
    if(activeTab==="activity"){
      const runtime=detail.runtime,plan=runtime?.latestPlan,item=runtime?.currentItem,metadata=item?.metadata||{},requests=runtime?.requests||[];
      const activity=metadata.command||metadata.tool||metadata.query||metadata.text||(metadata.changes||[]).map(change=>change.path).join(", ");
      content=runtime?`<p>${esc(runtime.availability||"UNAVAILABLE")} · ${esc(runtime.waitingState||runtime.agentHealth||"")}</p>${plan?`<h3>Native plan</h3><ol>${(plan.steps||[]).map(step=>`<li>${esc(step.step)} · ${esc(step.status)}</li>`).join("")}</ol>`:""}${item?`<h3>${esc(item.type)} · ${esc(item.status)}</h3>${item.type==="agentMessage"?C.resultPreview(activity,t):`<pre>${esc(String(activity).slice(0,65536))}</pre>`}${item.outputTail?`<h4>Output</h4><pre>${esc(item.outputTail)}</pre>`:""}`:""}${runtime.turnDiff?`<details><summary>${esc(runtime.diffSource||"Diff")}</summary><pre>${esc(runtime.turnDiff)}</pre></details>`:""}`:`<p>${esc(t("workbench.unavailable"))}</p>`;
      content+=`<h3>Runtime requests · read only</h3>${requests.length?requests.map(request=>`<details><summary>${esc(request.waitingState||request.method||request.id)} · ${esc(request.state||request.status)}</summary>${pre(request.payload)}</details>`).join(""):`<p>${esc(t("workbench.notApplicable"))}</p>`}`+((detail.approvals||[]).some(request=>request.status==="PENDING")||requests.some(request=>(request.state||request.status)==="PENDING")?`<button type="button" data-run-back>${esc(t(rootTask?"workbench.backTask":"workbench.backDiagnostics"))}</button>`:"")+`<details><summary>Runtime history</summary>${pre({approvals:detail.approvals,approvals_truncated:detail.approvals_truncated,events:detail.events,events_truncated:detail.events_truncated})}</details>`;
    }
    if(activeTab==="instructions")content=promptError?`<p role="alert">${esc(promptError)}</p><button type="button" data-run-retry>${esc(t("workbench.retry"))}</button>`:prompt?prompt.available?facts([["SHA256",prompt.sha256],["Bytes",prompt.bytes]])+`<pre>${esc(prompt.prompt||"")}</pre>`:`<p>${esc(t("workbench.unavailable"))}</p>`:`<p role="status">Loading…</p>`;
    if(activeTab==="evidence")content=purpose==="VERIFICATION"?`<h3>Verification Report</h3>${detail.verification_report?C.resultPreview(detail.verification_report,t,{title:"Verification Report"}):`<p>${esc(t("workbench.notGenerated"))}</p>`}`:purpose==="SCOPE_REVIEW"?`<h3>Review evidence / provenance</h3><p>${esc(detail.review_evidence_status||"UNAVAILABLE")} · ${esc(detail.review_result_status||"UNAVAILABLE")}</p>${detail.review_evidence_error?`<p role="alert">${esc(t("workbench.readFailed"))} · ${esc(detail.review_evidence_error)}</p>`:""}${detail.review_evidence||detail.review_result?C.resultPreview(detail.review_result,t,{title:t("review.fullResult"),reviewID:r.review_id})+C.resultPreview(detail.review_evidence,t,{title:"Review evidence / provenance"}):`<p>${esc(t(detail.review_evidence_status==="NOT_APPLICABLE"?"workbench.notApplicable":"workbench.unavailable"))}</p>`}`:`<h3>Git / delivery evidence</h3>${detail.evidence?.available?C.resultPreview(detail.evidence,t,{title:"Git / delivery evidence"}):`<p>${esc(t("workbench.notGenerated"))}</p>`}`;
    if(activeTab==="overview"||activeTab==="evidence")content=C.runOutput(detail,t)+content;
    if(activeTab==="usage") {
      const usage=r.token_usage||{status:"UNAVAILABLE"};
      content=`<h3>${esc(t("usage.title"))}</h3>`+C.keyValueDetails(C.buildUsageFacts({status:usage.status,usage:usage.counts||usage.usage},t,locale).map(([label,value])=>[label,esc(value)]));
    }
    if(activeTab==="diagnostics")content=pre({runId:r.id,agentExecutionId:r.agent_execution_id,taskId:r.task_id,reviewId:r.review_id,taskExecutionId:r.task_execution_id||r.execution_proposal_id,operationKind:r.operation_kind,operationId:r.operation_id,workspace:detail.workspace,threadId:r.thread_id,turnId:r.turn_id,runtimeEpoch:detail.runtime_epoch,runtime:detail.runtime,events:detail.events});
    return `<article class="task-workbench run-detail"><header class="task-fixed-header"><button type="button" class="text-button" data-run-back>${esc(t(rootTask?"workbench.backTask":"workbench.backDiagnostics"))}</button><h3>${esc(C.runPurposeLabel(purpose,t))}</h3>${r.thread_id||detail.runtime?.threadId?`<button type="button" data-open-run-session="${esc(r.id)}">查看会话</button>`:""}<p>${esc(C.statusText("run",r.status,t))} · <code>${esc(r.review_id||r.task_execution_id||r.execution_proposal_id||r.operation_id||"—")}</code></p>${f.task_definition_revision_id?`<code>${esc(f.task_definition_revision_id)}</code>`:""}</header>${!rootTask&&r.task_id&&["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION"].includes(purpose)?`<button type="button" data-task-detail="${esc(r.task_id)}">${esc(t("workbench.backTask"))}</button>`:""}${tabs("RUN",activeTab,esc,t)}<section class="run-tab-content" role="tabpanel" id="run-tab-${activeTab}" aria-labelledby="run-tab-button-${activeTab}">${content}</section></article>`;
  }
  const api={TASK_TABS,RUN_TABS,createRoute,tabs,duration,runList,taskOutcome,reviewHistory,executionHistory,taskResults,renderRun};
  if(typeof module!=="undefined"&&module.exports)module.exports=api;else root.DevBridgeTaskWorkbench=api;
})(typeof window!=="undefined"?window:globalThis);
