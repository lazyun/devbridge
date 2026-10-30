const esc = value => String(value ?? "").replace(/[&<>"']/g,c=>({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const copy = value => JSON.parse(JSON.stringify(value));
const supported = new Set(["CONTINUE_TASK","DEFER_TASK","COMPLETE_TASK","CANCEL_EXECUTION"]);
const terminal = task => ["SUCCEEDED","FAILED","CANCELED"].includes(task?.status) || task?.presentation?.taskView?.terminal === true;
const words = {
 zh:{title:"工作台",subtitle:"当前任务与下一步",project:"项目",refresh:"刷新",all:"全部任务",active:"进行中",history:"已结束",empty:"这个项目还没有任务",select:"选择一个任务，查看进展与下一步",loading:"正在读取…",retry:"重试读取",attention:"需要处理",progress:"任务进度",overview:"概览",runs:"运行记录",requirements:"任务目标",activity:"当前活动",verification:"验证结果",code:"代码交付",updated:"更新于",page:"当前页",previous:"上一页",next:"下一页",noRuns:"暂无运行记录",details:"技术详情",unavailable:"尚无记录",offline:"Core 尚未就绪，请从总览启动服务。",stale:"读取失败，保留上次结果。成功刷新前暂停操作。",more:"更多操作",advanced:"在浏览器处理其他工作流",confirm:"确认操作",cancel:"取消",submit:"确认",sending:"正在提交…",note:"备注（可选）",continued:"已确认。当前任务已更新。",unknown:"请求结果尚未确认。请核对当前状态；重试将使用原请求，不会生成新的操作。",replay:"重试原请求",close:"关闭",result:"运行结果",output:"实际输出",checks:"检查结果",readFailed:"读取失败",match:"任务已变化，请重新查看当前状态。",noAction:"当前没有需要处理的操作",repository:"个仓库",total:"个任务",pending:"待处理",search:"搜索任务",readonly:"这项操作需要完整的专用工作流。",persist:"无法保存请求身份，已停止提交。",reload:"刷新当前任务",continue:"延续任务",defer:"暂存任务",complete:"完成并合入",stop:"停止当前执行",continueHelp:"保留当前任务与历史，准备下一轮工作；不会立即启动 Codex。",deferHelp:"暂存当前任务，保留全部历史。",completeHelp:"按 Core 已验证的计划提交并合入以下开发分支。",stopHelp:"停止当前执行，保留已有改动和历史。",target:"合入目标",emptyProjects:"请先在“项目”中登记仓库。",evidence:"必需证据尚未完整",storageRead:"无法读取之前的请求记录，任务操作已暂停。"},
 en:{title:"Workbench",subtitle:"Current work and next steps",project:"Project",refresh:"Refresh",all:"All tasks",active:"Active",history:"Finished",empty:"No tasks in this project",select:"Select a task to see its progress and next step",loading:"Reading…",retry:"Retry",attention:"Needs attention",progress:"Progress",overview:"Overview",runs:"Run history",requirements:"Task goal",activity:"Current activity",verification:"Verification",code:"Delivery",updated:"Updated",page:"This page",previous:"Previous",next:"Next",noRuns:"No recorded runs",details:"Technical details",unavailable:"Not recorded",offline:"Core is not ready. Start the services from Overview.",stale:"Read failed. Previous results are retained; actions pause until a successful refresh.",more:"More actions",advanced:"Other workflows in browser",confirm:"Confirm action",cancel:"Cancel",submit:"Confirm",sending:"Submitting…",note:"Note (optional)",continued:"Confirmed. The task has been updated.",unknown:"The result is unconfirmed. Check the current state; retry uses the exact original request.",replay:"Retry original request",close:"Close",result:"Run result",output:"Actual output",checks:"Checks",readFailed:"Read failed",match:"This task changed. Read its current state again.",noAction:"No action is currently needed",repository:"repositories",total:"tasks",pending:"pending",search:"Search tasks",readonly:"This action requires its dedicated workflow.",persist:"Cannot persist the request identity. Submission stopped.",reload:"Refresh task",continue:"Continue task",defer:"Defer task",complete:"Complete and merge",stop:"Stop execution",continueHelp:"Keep this task and its history and prepare the next work. This does not immediately start Codex.",deferHelp:"Defer the task and keep its history.",completeHelp:"Commit and merge using the plan verified by Core, with the development targets below.",stopHelp:"Stop the current execution and retain changes and history.",target:"Merge targets",emptyProjects:"Register a repository from Projects first.",evidence:"Required evidence is incomplete",storageRead:"Previous request records are unreadable. Task actions are paused."}
};
const phaseNames={zh:{REVIEW_REQUIRED:"待审查",REVIEWING:"审查中",REVIEW_APPROVAL_PENDING:"待批准",IMPLEMENTING:"实施中",VERIFYING:"验证中",ACCEPTANCE_PENDING:"待验收",RECOVERY_REQUIRED:"需要恢复",DELIVERING:"交付中",DONE:"已完成",CANCELED:"已取消",REVIEW:"审查",APPROVED:"批准",IMPLEMENTATION:"实施",VERIFICATION:"验证",SCOPE_REVIEW:"审查",SUCCEEDED:"已完成",FAILED:"失败",INFRASTRUCTURE_FAILED:"运行异常",PREPARING:"准备中",RUNNING:"运行中",PENDING:"待进行",COMPLETE:"已完成"},en:{REVIEW_REQUIRED:"Needs review",REVIEWING:"Reviewing",REVIEW_APPROVAL_PENDING:"Needs approval",IMPLEMENTING:"Implementing",VERIFYING:"Verifying",ACCEPTANCE_PENDING:"Needs acceptance",RECOVERY_REQUIRED:"Needs recovery",DELIVERING:"Delivering",DONE:"Done",CANCELED:"Canceled",REVIEW:"Review",APPROVED:"Approval",IMPLEMENTATION:"Implementation",VERIFICATION:"Verification",SCOPE_REVIEW:"Review",SUCCEEDED:"Succeeded",FAILED:"Failed",INFRASTRUCTURE_FAILED:"Runtime failure",PREPARING:"Preparing",RUNNING:"Running",PENDING:"Pending",COMPLETE:"Complete"}};
const language = locale => locale?.startsWith("zh") ? "zh" : "en";
const label = (code,locale) => phaseNames[language(locale)][code] || code || "—";
export function phaseLabel(task,locale="zh-CN") {
 const v=task?.presentation?.taskView;
 if(v?.verificationDecisions?.hold)return language(locale)==="zh"?"已暂存":"Deferred";
 if(terminal(task))return label(task.status==="SUCCEEDED"?"DONE":task.status,locale);
 return v?.nextStep?.label || label(v?.businessPhase || task.businessPhase || task.phase,locale);
}
export function taskCapabilities(task) {
 return (task?.operatorActions || []).filter(cap=>supported.has(cap.action) && cap.target?.taskId===task.id);
}
const actionWord=(action,w)=>({CONTINUE_TASK:w.continue,DEFER_TASK:w.defer,COMPLETE_TASK:w.complete,CANCEL_EXECUTION:w.stop})[action]||action;
const actionHelp=(action,w)=>({CONTINUE_TASK:w.continueHelp,DEFER_TASK:w.deferHelp,COMPLETE_TASK:w.completeHelp,CANCEL_EXECUTION:w.stopHelp})[action]||"";
const time=(value,locale)=>value ? new Date(value).toLocaleString(locale,{month:"short",day:"numeric",hour:"2-digit",minute:"2-digit"}) : "—";
const view = task => task?.presentation?.taskView || {};
export function taskNotice(task) {
 const v=view(task);
 return v.actionNode?.blocking ? v.actionNode.reason : v.nextStep?.summary || v.verification?.summary || task?.phaseDescription || "";
}
export function nativeTaskHTML(state,locale="zh-CN") {
 const w=words[language(locale)],task=state.detail;
 if(!task)return `<div class="nc-empty"><span class="nc-empty-icon">◇</span><h2>${esc(state.detailLoading?w.loading:w.select)}</h2></div>`;
 const v=view(task),notice=taskNotice(task),caps=taskCapabilities(task),locked=state.stale||state.detailError||state.sending||state.storageError||state.offline;
 const can=cap=>cap.domainAvailable===true&&cap.channelAvailable===true&&!locked;
 const primary=caps.find(c=>c.action==="CONTINUE_TASK"&&c.domainAvailable)||caps.find(c=>c.action==="COMPLETE_TASK"&&c.domainAvailable)||caps.find(c=>c.domainAvailable);
 const pending=state.pending?.projectId===state.projectId&&state.pending?.taskId===task.id;
 const button=(cap,main=false)=>`<button class="${main?"nc-primary":"nc-secondary"}" data-nc-command="${esc(cap.action)}" ${can(cap)&&!pending?"":"disabled"} title="${esc(cap.reason||actionHelp(cap.action,w))}">${esc(actionWord(cap.action,w))}</button>`;
 const progress=(v.progress||[]).map(p=>`<li class="${p.status==="COMPLETE"?"done":p.status==="RUNNING"?"current":""}"><i></i><span>${esc(p.phase==="DONE"?(language(locale)==="zh"?"完成":"Finish"):label(p.phase,locale))}</span></li>`).join("");
 const dedicated=(task.operatorActions||[]).some(c=>c.domainAvailable&&c.channelAvailable&&!supported.has(c.action));
 const actions=`<div class="nc-command-row">${primary?button(primary,true):dedicated?`<span class="nc-muted">${esc(w.readonly)}</span><button class="nc-secondary" data-nc-browser>${esc(w.advanced)} ↗</button>`:`<span class="nc-muted">${esc(w.noAction)}</span>`}${caps.filter(c=>c!==primary&&c.action!=="CANCEL_EXECUTION").map(c=>button(c)).join("")}</div>${caps.find(c=>c.action==="COMPLETE_TASK"&&!c.domainAvailable)?.reason?`<p class="nc-gate">${esc(caps.find(c=>c.action==="COMPLETE_TASK").reason)}</p>`:""}${primary?`<p class="nc-muted nc-action-help">${esc(actionHelp(primary.action,w))}</p>`:""}`;
 const facts=[[w.activity,task.currentActivity?.summary||v.activity?.summary],[w.code,task.commitSummary||v.commitSummary],[w.verification,notice===v.verification?.summary?"":v.verification?.summary]].filter(([,value])=>value).map(([key,value])=>`<div><dt>${esc(key)}</dt><dd>${esc(value)}</dd></div>`).join("");
 const history=state.runs?.items||[];
 const content=state.tab==="runs"?`<div class="nc-run-list">${history.length?history.map(r=>`<button data-nc-run="${esc(r.id)}"><span><strong>${esc(label(r.runPurpose||r.purpose,locale))}</strong><small>${esc(time(r.startedAt||r.createdAt,locale))}</small></span><span class="nc-run-status">${esc(label(r.transportStatus||r.status,locale))} <b>›</b></span></button>`).join(""):`<p class="nc-muted">${esc(state.runsLoading?w.loading:state.runsError||w.noRuns)}</p>`}${state.runs?.total>history.length?`<div class="nc-pagination"><button data-nc-runs-page="${Math.max(0,(state.runs.offset||0)-20)}" ${(state.runs.offset||0)===0?"disabled":""}>${esc(language(locale)==="zh"?"更早记录":"Earlier")}</button><span>${history.length} / ${state.runs.total}</span><button data-nc-runs-page="${Math.min(Math.max(0,state.runs.total-20),(state.runs.offset||0)+20)}" ${(state.runs.offset||0)+20>=state.runs.total?"disabled":""}>${esc(language(locale)==="zh"?"较新记录":"Newer")}</button></div>`:""}</div>`:`${notice?`<div class="nc-notice"><span class="nc-notice-dot"></span><p>${esc(notice)}</p></div>`:""}${actions}<dl class="nc-facts">${facts}</dl><details class="nc-disclosure"><summary>${esc(w.requirements)}</summary><p>${esc(v.goal||task.work?.goal||task.title)}</p>${(v.originalRequirement?.constraints||[]).length?`<ul>${v.originalRequirement.constraints.map(s=>`<li>${esc(s)}</li>`).join("")}</ul>`:""}</details>${(v.reviewHistory||[]).length?`<details class="nc-disclosure"><summary>${esc(language(locale)==="zh"?"审查记录":"Reviews")} <span>${v.reviewHistory.length}</span></summary>${v.reviewHistory.map(r=>`<div class="nc-review"><strong>${esc(r.executorKind==="UPPER_LLM"?"GPT":r.executorKind||"Codex")}</strong><span>${esc(label(r.status,locale))}</span><p>${esc(r.activity?.summary||r.failureSummary||"")}</p>${r.run?.id?`<button data-nc-run="${esc(r.run.id)}">${esc(w.result)} →</button>`:""}</div>`).join("")}</details>`:""}<details class="nc-disclosure"><summary>${esc(w.more)}</summary>${caps.filter(c=>c.action==="CANCEL_EXECUTION").map(c=>button(c)).join("")}<button data-nc-browser>${esc(w.advanced)} ↗</button><details><summary>${esc(w.details)}</summary><code>${esc(task.id)}</code><p>${esc(task.current_definition_revision_id||"")}</p></details></details>`;
 return `<header class="nc-detail-heading"><p class="nc-eyebrow">${esc(task.feature_area_title||task.presentation?.featureArea?.name||w.requirements)}</p><div class="nc-task-title"><h2>${esc(task.title)}</h2><span class="nc-badge">${esc(phaseLabel(task,locale))}</span></div><p class="nc-goal">${esc(v.goal||task.work?.goal||"")}</p></header><ol class="nc-progress">${progress}</ol>${state.detailError?`<div class="nc-error" role="alert">${esc(w.stale)} ${esc(state.detailError)}</div>`:""}${pending?`<div class="nc-request-warning" role="alert"><p>${esc(w.unknown)}</p><button data-nc-replay ${state.sending||state.pending.runtime!==state.runtime?"disabled":""}>${esc(state.sending?w.sending:w.replay)}</button></div>`:""}<nav class="nc-tabs"><button data-nc-tab="overview" class="${state.tab==="overview"?"selected":""}">${esc(w.overview)}</button><button data-nc-tab="runs" class="${state.tab==="runs"?"selected":""}">${esc(w.runs)}</button></nav>${content}<footer class="nc-detail-footer">${esc(w.updated)} ${esc(time(task.updated_at,locale))}</footer>`;
}

export function nativeListHTML(state,locale="zh-CN") {
 const w=words[language(locale)];
 const items=state.tasks.filter(task=>state.filter==="all" || (state.filter==="active"?!terminal(task):terminal(task)));
 return `<div class="nc-list-caption">${esc(w.page)} · ${state.tasks.length} / ${state.total}</div>${items.length?items.map(task=>`<button class="nc-task-row ${state.taskId===task.id?"selected":""}" data-nc-task="${esc(task.id)}"><span class="nc-task-row-title">${esc(task.title)}</span><span class="nc-task-row-meta"><span class="${!terminal(task)?"nc-state-active":""}">${esc(phaseLabel(task,locale))}</span><time>${esc(time(task.updated_at,locale))}</time></span></button>`).join(""):`<p class="nc-list-empty">${esc(state.loading?w.loading:w.empty)}</p>`}<div class="nc-pagination"><button data-nc-page="${Math.max(0,state.offset-30)}" ${state.offset===0?"disabled":""}>‹ ${esc(w.previous)}</button><span>${Math.floor(state.offset/30)+1}</span><button data-nc-page="${state.offset+30}" ${state.offset+30>=state.total?"disabled":""}>${esc(w.next)} ›</button></div>`;
}

export function createNativeConsole({backend,storage,onChange=()=>{},uuid=()=>crypto.randomUUID()}) {
 const state={projects:[],projectId:"",tasks:[],total:0,offset:0,filter:"all",search:"",taskId:"",detail:null,tab:"overview",runs:null,run:null,error:"",detailError:"",stale:false,loading:false,detailLoading:false,runsLoading:false,runsError:"",offline:true,runtime:"",sending:false,notice:"",actionError:"",confirm:null,pending:null,storageError:false};
 const requestKey="devbridge.desktop.nativeConsole.pending.v1",viewKey="devbridge.desktop.nativeConsole.view.v1";
 let root=null,locale="zh-CN",generation=0,detailSeq=0,readSeq=0,lastPoll=0,inFlight=false,queuedRefresh=false,signatures=new Map();
 let pendingRecords=[];
 try {const saved=JSON.parse(storage?.getItem(viewKey)||"null");if(saved&&typeof saved.projectId==="string"){state.projectId=saved.projectId;state.taskId=typeof saved.taskId==="string"?saved.taskId:"";}}
 catch { /* A broken viewing preference carries no authority. */ }
 try {const value=JSON.parse(storage?.getItem(requestKey)||"[]");if(!Array.isArray(value)||value.length>100)throw Error();for(const record of value){if(!record.body||!record.taskId||!record.projectId||!record.runtime||JSON.parse(record.body).requestId!==record.requestId)throw Error();}pendingRecords=value;}
 catch {state.storageError=true;}
 function selectPending(){state.pending=pendingRecords.find(r=>r.projectId===state.projectId&&r.taskId===state.taskId)||null;}
 selectPending();
 const w=()=>words[language(locale)];
 const read=async(kind,entity="",offset=state.offset,search=state.search)=>JSON.parse(await backend().ConsoleRead(kind,state.projectId,entity,offset,search));
 const same=(g,project,id)=>g===generation&&project===state.projectId&&(!id||id===state.taskId);
 function saveView(){try{storage?.setItem(viewKey,JSON.stringify({projectId:state.projectId,taskId:state.taskId}));}catch{}}
 function persist(record){if(!storage)throw Error(w().persist);const next=[...pendingRecords.filter(r=>r.projectId!==record.projectId||r.taskId!==record.taskId),record];const raw=JSON.stringify(next);storage.setItem(requestKey,raw);if(storage.getItem(requestKey)!==raw)throw Error(w().persist);pendingRecords=next;selectPending();}
 function clearPending(){const next=pendingRecords.filter(r=>r.projectId!==state.projectId||r.taskId!==state.taskId);storage.setItem(requestKey,JSON.stringify(next));pendingRecords=next;selectPending();}
 function changed(){draw();onChange(state);}
 async function projects(){const g=generation,runtime=state.runtime;try{const value=JSON.parse(await backend().ConsoleRead("projects","","",0,""));if(g!==generation||runtime!==state.runtime)return false;if(!Array.isArray(value.items)||value.items.some(p=>typeof p.id!=="string"||!p.id))throw Error(w().readFailed);state.projects=value.items;if(!state.projects.some(p=>p.id===state.projectId)){state.projectId=state.projects[0]?.id||"";state.taskId="";state.detail=null;selectPending();}saveView();changed();return true;}catch(e){if(g===generation&&runtime===state.runtime){state.error=String(e);state.stale=true;changed();}return false;}}
 async function detail(){
  const id=state.taskId,project=state.projectId,g=generation,seq=++detailSeq;if(!id)return;state.detailLoading=true;changed();
  try{const value=await read("task",id);if(!same(g,project,id)||seq!==detailSeq)return;if(value.id!==id)throw Error(w().match);state.detail=value;state.detailError="";}
  catch(e){if(same(g,project,id)&&seq===detailSeq)state.detailError=String(e);}
  finally{if(same(g,project,id)&&seq===detailSeq){state.detailLoading=false;changed();}}
 }
 async function refresh(force=false){
  if(inFlight){if(force)queuedRefresh=true;return;}
  if(state.offline||state.sending||state.confirm||state.run||(!force&&Date.now()-lastPoll<6000))return;
  inFlight=true;lastPoll=Date.now();const g=generation,project=state.projectId,seq=++readSeq;state.loading=true;changed();
  try{
   if(!state.projects.length&&!await projects())return;
   if(!same(g,project)&&project!="")return;
   if(!state.projectId)return;
   const captured=state.projectId,value=await read("tasks");if(g!==generation||captured!==state.projectId||seq!==readSeq)return;
   if(!Array.isArray(value.items)||!Number.isInteger(value.total))throw Error(w().readFailed);
   state.tasks=value.items;state.total=value.total;state.error="";state.stale=false;
   if(!state.taskId||!state.tasks.some(t=>t.id===state.taskId)){state.taskId=state.tasks.find(t=>!terminal(t))?.id||state.tasks[0]?.id||"";state.detail=null;state.runs=null;selectPending();saveView();}
   await detail();
  }catch(e){if(g===generation){state.error=String(e);state.stale=true;}}
  finally{inFlight=false;if(g===generation){state.loading=false;changed();}if(queuedRefresh){queuedRefresh=false;void refresh(true);}}
 }
 async function selectProject(id){if(state.sending||id===state.projectId)return;++generation;++detailSeq;state.projectId=id;state.taskId="";state.tasks=[];state.detail=null;state.runs=null;state.error="";state.detailError="";state.confirm=null;state.offset=0;state.tab="overview";state.loading=false;selectPending();saveView();changed();await refresh(true);}
 async function selectTask(id){if(state.sending)return;++detailSeq;state.taskId=id;state.detail=null;state.detailError="";state.runs=null;state.tab="overview";selectPending();saveView();changed();await detail();}
 async function runs(offset=-1){const g=generation,id=state.taskId,p=state.projectId;state.runsLoading=true;changed();try{let value=await read("runs",id,Math.max(0,offset),"");if(!same(g,p,id))return;if(offset<0&&value.total>20)value=await read("runs",id,value.total-20,"");if(same(g,p,id)){if(!Array.isArray(value.items))throw Error(w().readFailed);state.runs={...value,items:[...value.items].reverse()};state.runsError="";}}catch(e){if(same(g,p,id))state.runsError=String(e);}finally{if(same(g,p,id)){state.runsLoading=false;changed();}}}
 async function run(id){const g=generation,task=state.taskId,p=state.projectId;state.run={loading:true};changed();try{const value=await read("run",id);if(same(g,p,task)){if(value.project_id!==p||(value.run?.task_id&&value.run.task_id!==task))throw Error(w().match);state.run={data:value};}}catch(e){if(same(g,p,task))state.run={error:String(e)};}changed();}
 async function prepare(action){
  if(state.offline||state.stale||state.storageError||state.pending?.taskId===state.taskId&&state.pending?.projectId===state.projectId||state.sending)return;
  state.actionError="";const g=generation,id=state.taskId,p=state.projectId;await detail();if(!same(g,p,id)||state.detailError)return;
  const cap=taskCapabilities(state.detail).find(c=>c.action===action&&c.domainAvailable&&c.channelAvailable);if(!cap)return;
  try{const plan=action==="COMPLETE_TASK"?await read("completion",id):null;if(!same(g,p,id))return;state.confirm={action,target:copy(cap.target),plan,projectId:p,taskId:id,title:state.detail.title,runtime:state.runtime};changed();}catch(e){state.error=String(e);changed();}
 }
 async function send(replay=false,note=""){
  if(state.sending||state.offline)return;
  let record=state.pending;
  if(!replay){
   const c=state.confirm;if(!c||c.projectId!==state.projectId||c.taskId!==state.taskId||c.runtime!==state.runtime)return;
   const body={action:c.action,target:c.target,requestId:uuid(),...(note?{note}:{}),...(c.plan?{completionPlan:c.plan}:{})};record={projectId:c.projectId,taskId:c.taskId,requestId:body.requestId,body:JSON.stringify(body),runtime:c.runtime};
  }
  if(!record||record.runtime!==state.runtime||record.taskId!==state.taskId||record.projectId!==state.projectId)return;
  try{persist(record);}catch(e){state.error=String(e);changed();return;}
  state.sending=true;state.confirm=null;state.notice="";state.actionError="";changed();
  try{
   const response=JSON.parse(await backend().ConsoleTaskAction(record.projectId,record.taskId,record.body));
   if(response.status>=200&&response.status<300){clearPending();state.notice=w().continued;state.error="";}
   else if(response.status>=400&&response.status<500&&![408,429].includes(response.status)){clearPending();state.actionError=response.data?.message||w().match;}
   else {state.actionError=response.data?.message||w().unknown;}
  }catch(e){state.actionError=String(e);}
  finally{state.sending=false;state.stale=true;changed();await refresh(true);}
 }
 function patch(selector,html){const element=root?.querySelector(selector);if(!element||signatures.get(selector)===html)return;signatures.set(selector,html);element.innerHTML=html;}
 function draw(){
  if(!root?.isConnected)return;
  const x=w(),selected=state.projects.find(p=>p.id===state.projectId);
  patch('[data-nc-project]',`<select aria-label="${esc(x.project)}" data-nc-project-select ${state.sending?"disabled":""}>${state.projects.map(p=>`<option value="${esc(p.id)}" ${p.id===state.projectId?"selected":""}>${esc(p.name)}</option>`).join("")}</select>`);
  patch('[data-nc-description]',`${esc(selected?.name||x.project)} <span>／ ${selected?.repository_count??0} ${esc(x.repository)} · ${state.total} ${esc(x.total)}</span>`);
  patch('[data-nc-status]',`${state.offline?`<div class="nc-error">${esc(x.offline)}</div>`:""}${state.error||state.storageError?`<div class="nc-error" role="alert">${esc(state.storageError?x.storageRead:state.error)}${state.stale?`<small>${esc(x.stale)}</small>`:""}</div>`:""}${state.actionError?`<div class="nc-error" role="alert">${esc(state.actionError)}</div>`:""}${state.notice?`<div class="nc-success" role="status">${esc(state.notice)}</div>`:""}`);
  patch('[data-nc-list]',nativeListHTML(state,locale));patch('[data-nc-detail]',nativeTaskHTML(state,locale));
  root.querySelectorAll('[data-nc-filter]').forEach(b=>b.classList.toggle('selected',b.dataset.ncFilter===state.filter));
  const c=state.confirm;
  patch('[data-nc-modal]',c?`<div class="nc-modal-shade"><section class="nc-modal" role="dialog" aria-modal="true" aria-labelledby="nc-confirm-title"><p class="nc-eyebrow">${esc(c.title||state.detail?.title)}</p><h2 id="nc-confirm-title">${esc(c.replay?x.replay:actionWord(c.action,x))}</h2><p>${esc(c.replay?x.unknown:actionHelp(c.action,x))}</p>${c.plan?`<div class="nc-targets"><strong>${esc(x.target)}</strong>${(c.plan.members||[]).map(m=>`<p>${esc(m.repositoryName||m.repositoryId)} → ${esc(m.developmentRef)}<small>${esc(m.expectedDevHead)}</small></p>`).join("")}</div>`:""}${!c.replay&&c.action!=="COMPLETE_TASK"?`<label>${esc(x.note)}<textarea data-nc-note rows="3" maxlength="10000"></textarea></label>`:""}<footer><button data-nc-dismiss>${esc(x.cancel)}</button><button class="nc-primary" data-nc-confirm ${state.sending?"disabled":""}>${esc(state.sending?x.sending:x.submit)}</button></footer></section></div>`:state.run?runHTML(state.run,x,locale):"");
 }
 function mount(element,nextLocale){if(!element)return;if(root===element){locale=nextLocale;return;}root=element;locale=nextLocale;signatures=new Map();const x=w();root.innerHTML=`<header class="nc-header"><div><p class="nc-eyebrow">${esc(x.subtitle)}</p><h1>${esc(x.title)}</h1></div><div class="nc-header-tools"><div data-nc-project></div><button class="nc-refresh" data-nc-refresh aria-label="${esc(x.refresh)}">↻</button></div></header><p class="nc-project-line" data-nc-description></p><div data-nc-status></div><section class="nc-workspace"><aside class="nc-list-pane"><div class="nc-filter-bar">${[["all",x.all],["active",x.active],["history",x.history]].map(([id,title])=>`<button data-nc-filter="${id}">${esc(title)}</button>`).join("")}</div><form class="nc-search" data-nc-search-form><input data-nc-search aria-label="${esc(x.search)}" placeholder="${esc(x.search)}" value="${esc(state.search)}"><button type="submit" aria-label="${esc(x.search)}">⌕</button></form><div data-nc-list></div></aside><article class="nc-inspector" data-nc-detail></article></section><div data-nc-modal></div>`;
  root.addEventListener('change',e=>{if(e.target.matches('[data-nc-project-select]'))void selectProject(e.target.value);});
  root.addEventListener('submit',e=>{if(e.target.matches('[data-nc-search-form]')){e.preventDefault();state.search=root.querySelector('[data-nc-search]').value;state.offset=0;++generation;void refresh(true);}});
  root.addEventListener('click',e=>{const b=e.target.closest('button');if(!b||b.disabled)return;const d=b.dataset;
   if('ncRefresh'in d){state.error="";void projects().then(()=>refresh(true));}else if(d.ncTask)void selectTask(d.ncTask);else if(d.ncFilter){state.filter=d.ncFilter;const visible=state.tasks.filter(t=>state.filter==='all'||(state.filter==='active'?!terminal(t):terminal(t)));if(!visible.some(t=>t.id===state.taskId)){if(visible.length)void selectTask(visible[0].id);else {state.taskId='';state.detail=null;selectPending();saveView();}}changed();}else if('ncPage'in d){state.offset=Number(d.ncPage);++generation;void refresh(true);}else if(d.ncTab){state.tab=d.ncTab;changed();if(d.ncTab==='runs')void runs();}else if(d.ncCommand)void prepare(d.ncCommand);else if('ncRunsPage'in d)void runs(Number(d.ncRunsPage));else if(d.ncRun)void run(d.ncRun);else if('ncBrowser'in d)void Promise.resolve(backend().OpenConsoleTask(state.projectId,state.taskId)).catch(e=>{state.actionError=String(e);changed();});else if('ncDismiss'in d){state.confirm=null;state.run=null;changed();}else if('ncReplay'in d){state.confirm={replay:true};changed();}else if('ncConfirm'in d){const replay=state.confirm?.replay,note=root.querySelector('[data-nc-note]')?.value||"";if(replay)state.confirm=null;void send(!!replay,note);}
  });draw();void refresh(true);
 }
 function status(core,nextLocale=locale){locale=nextLocale;const was=state.offline;state.offline=!['RUNNING','EXTERNAL'].includes(core?.state)||!['MANAGED','EXTERNAL'].includes(core?.ownership);const identity=JSON.stringify([core?.endpoint,core?.dataRoot]);if(!state.offline&&state.runtime!==identity){++generation;++detailSeq;state.runtime=identity;state.stale=true;state.confirm=null;state.projects=[];state.tasks=[];state.detail=null;state.runs=null;state.run=null;state.notice="";state.loading=false;changed();}
  if(was!==state.offline)changed();if(root?.isConnected)void refresh(was&&!state.offline);
 }
 return {state,mount,status,refresh,selectProject,selectTask,prepare,send,runs,run};
}

function runHTML(record,w,locale){
 const d=record.data,summary=d?.run,output=d?.native_output||d?.review_result||"",report=d?.verification_report;
 return `<div class="nc-modal-shade"><section class="nc-modal nc-result-modal" role="dialog" aria-modal="true" aria-labelledby="nc-run-title"><header><div><p class="nc-eyebrow">${esc(w.result)}</p><h2 id="nc-run-title">${esc(summary?label(summary.purpose,locale):w.loading)}</h2></div><button data-nc-dismiss aria-label="${esc(w.close)}">×</button></header>${record.error?`<p class="nc-error">${esc(record.error)}</p>`:""}${d?`<p class="nc-muted">${esc(label(summary?.status,locale))} · ${esc(time(summary?.finished_at||summary?.started_at,locale))}</p>${d.failure?`<p class="nc-error">${esc(d.failure.message||d.failure.summary||d.failure.code)}</p>`:""}${report?`<h3>${esc(w.checks)}</h3><pre>${esc(JSON.stringify(report,null,2))}</pre>`:""}<h3>${esc(w.output)}</h3><pre>${esc(output||d.review_evidence_error||d.analysis_read_error||w.unavailable)}</pre>${d.native_output_truncated||d.review_result_truncated?`<p class="nc-muted">${esc(language(locale)==="zh"?"输出为 Core 提供的有界内容，未显示全部文本。":"Output is bounded by Core and is not the complete text.")}</p>`:""}`:""}</section></div>`;
}
