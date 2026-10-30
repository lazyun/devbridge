"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const W = require("./ui/product-workbench.js");
const I = require("./ui/product-i18n.js");
global.DevBridgeStatus = require("./ui/product-status.js");
const C = require("./ui/product-components.js");
function context(state = {}, language = "en") {
  const i = I.createI18n({navigator:{languages:[language]}});
  return {state:{projectWorkspace:{tasks:[],featureAreas:[],documents:[]},...state},C,t:i.t,locale:i.locale};
}
function task(id, status="ACTIVE",phase="REVIEWING",more={}) {return {id,title:`Task ${id}`,status,phase,feature_area_id:"area",updated_at:"2026-09-12T01:00:00Z",...more};}

test("area task table omits redundant area column while project task table retains it",()=>{
  const {createDocument}=require('./testdata/product_dom_harness.js');
  const area={id:'area',title:'Area identity',status:'ACTIVE'};
  const ctx=context({projectWorkspace:{tasks:[task('one')],featureAreas:[area],documents:[]}});
  const detail=createDocument(W.renderFeatureAreaDetail(ctx,area)),project=createDocument(W.renderTasks(ctx));
  const headers=doc=>doc.querySelectorAll('th').map(n=>n.textContent);
  assert.equal(headers(detail).length,5);assert.equal(headers(project).length,6);
  assert.equal(headers(detail).includes(ctx.t('tasks.topic')),false);
  assert.equal(headers(project).includes(ctx.t('tasks.topic')),true);
  assert.equal(detail.querySelectorAll('tbody tr td').length,5);
});

test("old saved navigation has one explicit destination and no workflow tabs",()=>{
  assert.deepEqual(W.projectTabs,["overview","featureAreas","tasks","sessions","resources","diagnostics"]);
  assert.equal(W.normalizeNavigation("projects","documents").projectTab,"resources");
  assert.equal(W.normalizeNavigation("projects","modules").resourceTab,"modules");
  assert.equal(W.normalizeNavigation("projects","workspaces").diagnosticTab,"workspaces");
  assert.equal(W.normalizeNavigation("logs","tasks").diagnosticTab,"logs");
  assert.equal(W.normalizeNavigation("projects","executions").projectTab,"diagnostics");
  assert.equal(W.normalizeNavigation("unknown","obsolete").projectTab,"overview");
});

test("semantic task filters retain titles, exact status/phase and bounded stable page",()=>{
  const items=Array.from({length:150},(_,i)=>task(String(i),i%2?"ACTIVE":"SUCCEEDED",i%2?"REVIEWING":"ACCEPTANCE_PENDING"));
  items.push({id:"review",title:"Review instruction",kind:"REVIEW"});
  assert.equal(W.taskPage(items,{status:"ACTIVE",phase:"REVIEWING"},0).items.length,25);
  assert.equal(W.taskPage(items,{status:"ACTIVE",phase:"REVIEWING"},99).page,2);
  assert.equal(W.taskPage(items,{search:"Task 149",area:"area"}).items[0].id,"149");
  assert.equal(W.taskPage(items,{},0,1000).items.length,100);
  assert.deepEqual(W.taskPage(items,{status:"ACTIVE"},1).items.map(x=>x.id),W.taskPage(items,{status:"ACTIVE"},1).items.map(x=>x.id));
});

test("remote task pages are not sliced or filtered a second time and unavailable filters remain selectable",()=>{
  const ctx=context({taskPageResult:{items:[task("remote")],offset:100,limit:25,total:101},taskFilters:{search:"remote",status:"FAILED"}});
  const html=W.renderTasks(ctx);
  assert.match(html,/data-task-detail="remote"/);
  assert.match(html,/Page 5 of 5/);
  assert.match(html,/<option value="FAILED" selected>/);
  assert.match(html,/value="VERIFYING"/);
  assert.equal((html.match(/data-task-detail=/g)||[]).length,1);
});

test("overview separates decisions, requests, blockers, acceptance and Git facts without mutation controls",()=>{
  const tasks=[task("plan","ACTIVE","REVIEW_APPROVAL_PENDING"),task("accept","ACTIVE","ACCEPTANCE_PENDING"),task("failed-review","ACTIVE","REVIEWING",{currentActivity:{status:"INFRASTRUCTURE_FAILED",summary:"interrupted"}}),task("delivered","SUCCEEDED","ACCEPTANCE_PENDING",{commitActivity:"FAILED",commitSummary:"Git commit failed",devIntegrationStatus:"NOT_INTEGRATED"})];
  const workspace={tasks,featureAreas:[]};
  const approvals=[{id:"request",status:"PENDING",run_id:"inspection",kind:"COMMAND_EXECUTION",task_id:""}];
  const before=JSON.stringify(workspace),facts=W.overviewFacts(workspace,approvals);
  assert.equal(facts.decisions.length,2);
  assert.equal(facts.blockers.length,2);
  assert.equal(facts.requests[0].task,null);
  assert.equal(facts.active.find(x=>x.id==="failed-review").status,"ACTIVE");
  const html=W.renderOverview(context({projectWorkspace:workspace,approvals}));
  assert.match(html,/data-task-open-tab="reviews"/);
  assert.match(html,/data-task-open-tab="executions"/);
  assert.match(html,/data-diagnostic-run="inspection"/);
  assert.match(html,/Task accepted/);
  assert.match(html,/Git commit failed/);
  assert.doesNotMatch(html,/data-(?:resolve|approve|cancel|recovery|execution-cancel|work-action)=/);
  assert.equal(JSON.stringify(workspace),before);
});

test("runtime request owner fence rejects a snapshot for another run",()=>{
  const workspace={tasks:[task("a")],featureAreas:[]};
  const runtime={runId:"run",requests:[{state:"PENDING",requestId:1,runId:"run",taskId:"a",method:"input"}]};
  assert.equal(W.overviewFacts(workspace,[],[{id:"other",task_id:"a",agent_runtime:runtime}]).requests.length,0);
  assert.equal(W.overviewFacts(workspace,[],[{id:"run",task_id:"a",agent_runtime:runtime}]).requests[0].task.id,"a");
});

test("area task pages retain historical Tasks and exact canonical current pointer",()=>{
  const area={id:"area",title:"Area",status:"ACTIVE",product:{domainStatus:"ACTIVE",currentTask:{id:"remote",status:"ACTIVE",phase:"REVIEWING"},taskPhase:"REVIEWING",availableActions:[],technicalDetails:{internalStatus:"ACTIVE"}}};
  const ctx=context({projectWorkspace:{tasks:[],featureAreas:[area]},areaTaskPages:{area:{items:[task("history","SUCCEEDED")],offset:25,limit:25,total:26}},areaTaskFilters:{area:"all"}});
  assert.match(W.renderFeatureAreas(ctx),/data-task-detail="remote"/);
  const html=W.renderFeatureAreaDetail(ctx,area);
  assert.match(html,/data-task-detail="history"/);
  assert.match(html,/data-area-task-page="0"/);
  assert.match(html,/<details[^>]*data-section-key="area-context"/);
  assert.equal(C.featureAreaProductStatus({...area,status:"REVIEWING"}),"ACTIVE");
});

test("FeatureArea lifecycle, current Task phase, and run activity keep separate owners",()=>{
  const idle={id:"area",title:"Chat",status:"DISCOVERING",agent_operation_message:"Agent context is ready for work.",product:{domainStatus:"ACTIVE",internalStatus:"DISCOVERING",availableActions:["CREATE_TASK"],technicalDetails:{internalStatus:"DISCOVERING"}},presentation:{agentOperation:{status:"READY",message:"Agent context is ready for work."}}};
  assert.deepEqual(W.featureAreaStatus(idle,[]),{current:null,phase:"",activity:""});
  const idleHTML=W.renderFeatureAreas(context({projectWorkspace:{tasks:[],featureAreas:[idle]}}));
  assert.match(idleHTML,/>Chat</);assert.match(idleHTML,/Active/);assert.doesNotMatch(idleHTML,/Discovering|Agent context is ready for work/);

  for (const closed of [{...idle,status:"RETIRED",product:{...idle.product,domainStatus:"CLOSED",internalStatus:"RETIRED"}},{...idle,status:"DECOMPOSED",product:{...idle.product,domainStatus:"CLOSED",internalStatus:"DECOMPOSED"}},{...idle,status:"ACTIVE",legacy_read_only:true,product:{...idle.product,domainStatus:"CLOSED"}}]) {
    const closedHTML=W.renderFeatureAreas(context({projectWorkspace:{tasks:[],featureAreas:[closed]},featureAreaStatusFilter:"all"}));
    assert.match(closedHTML,/Closed/);assert.doesNotMatch(closedHTML,/Discovering|Decomposed|Retired/);
  }

  const current=task("current","ACTIVE","VERIFYING",{phaseLabel:"Verification"});
  const running={...idle,status:"ACTIVE",product:{...idle.product,currentTask:{id:"current",status:"ACTIVE",phase:"VERIFYING"},taskPhase:"VERIFYING",operationActivity:{state:"BUSY",message:"Verification is running.",kind:"EXECUTION",operationId:"execution",runId:"run"}},presentation:{currentTask:{id:"wrong-client-source",status:"ACTIVE",phase:"REVIEWING"},currentOperation:{kind:"EXECUTION",id:"execution",runId:"run",status:"RUNNING"},agentOperation:{status:"BUSY",message:"Wrong client source"}}};
  assert.deepEqual(W.featureAreaStatus(running,[current]),{current,phase:"Verification",activity:"Verification is running."});
  const runningHTML=W.renderFeatureAreas(context({projectWorkspace:{tasks:[current],featureAreas:[running]}}));
  assert.match(runningHTML,/Task current/);assert.match(runningHTML,/Verification/);assert.match(runningHTML,/Verification is running/);
});

test("repository facts retain current ref, selected revision and checkout separately",()=>{
  const html=W.renderProjectContext(context({developmentContext:{repositories:[{repositoryName:"one",developmentRef:"refs/heads/dev",currentHeadSha:"current",selectedHeadSha:"selected",repositoryCheckoutRef:"refs/heads/other"},{repositoryName:"two",developmentRef:"refs/heads/second",currentHeadSha:"two-head"}]}}));
  for(const fact of ["current","selected","refs/heads/other","refs/heads/second"]) assert.ok(html.includes(fact));
});

test("escaped titles, read failures, empty pages and bilingual keys remain explicit",()=>{
  const ctx=context({taskPageResult:{items:[task("a","ACTIVE","REVIEWING",{title:'<script>alert(1)</script>'})],offset:0,limit:25,total:1},taskListError:"permission denied"});
  const html=W.renderTasks(ctx);
  assert.match(html,/&lt;script&gt;/);
  assert.doesNotMatch(html,/<script>/);
  assert.match(html,/role="alert"/);
  assert.match(html,/data-task-list-retry/);
  assert.match(W.renderTasks(context()),/No tasks match/);
  for(const locale of ["en","zh-CN"]) {
    const translated=W.renderOverview(context({},locale))+W.renderTasks(context({},locale))+W.renderDiagnostics(context({},locale));
    assert.doesNotMatch(translated,/\[workbench\./);
  }
  assert.equal(I.messages["zh-CN"]["workbench.term.execution"],"执行批次");
  assert.equal(I.messages["zh-CN"]["workbench.term.run"],"运行记录");
});

test('task phase and filter labels follow locale while task status retains authority',()=>{
 const zh=context({projectWorkspace:{tasks:[task('done','SUCCEEDED','DELIVERING',{phaseLabel:'Done'}),task('active')],featureAreas:[]}},'zh-CN');
 const html=W.renderTasks(zh);assert.match(html,/>已完成</);assert.match(html,/>进行中</);assert.match(html,/>等待审批</);assert.doesNotMatch(html,/>Done<|>SUCCEEDED<|>REVIEW_APPROVAL_PENDING</);
 const filters={area:'missing',branch:'missing',status:'FAILED',search:'keep'};
 assert.deepEqual(W.normalizeTaskFilters(zh.state.projectWorkspace,filters),{area:'',branch:'',status:'FAILED',search:'keep'});assert.equal(filters.area,'missing');
 assert.equal(W.normalizeTaskFilters({...zh.state.projectWorkspace,tasks_truncated:true},{branch:'unlisted-history'}).branch,'unlisted-history');
 assert.equal(C.taskPhaseLabel({status:'ACTIVE',phase:'DELIVERING'},zh.t),'交付中');
 assert.equal(C.taskPhaseLabel({status:'CANCELED',phase:'DELIVERING'},zh.t),'已取消');
});

test('recovery keeps original business stage, independent warning and phase filter',()=>{
 const interrupted=task('recover','ACTIVE','RECOVERY_REQUIRED',{businessPhase:'VERIFYING',recovery:{required:true},currentActivity:{status:'RECOVERY_REQUIRED',summary:'已中断，等待恢复。'}});
 const ctx=context({projectWorkspace:{tasks:[interrupted],featureAreas:[],documents:[]}},'zh-CN');
 assert.equal(C.taskPhaseLabel(interrupted,ctx.t),'验证');
 const {createDocument}=require('./testdata/product_dom_harness.js'),doc=createDocument(W.renderTasks(ctx));
 assert.match(doc.root.textContent,/验证/);assert.match(doc.root.textContent,/等待恢复/);assert.match(doc.root.textContent,/已中断，等待恢复/);
 assert.doesNotMatch(doc.querySelector('tbody').textContent,/验证中/);
 assert.equal(W.taskPage([interrupted],{phase:'VERIFYING'}).items[0].id,'recover');
 assert.equal(W.taskPage([interrupted],{phase:'RECOVERY_REQUIRED'}).items[0].id,'recover');
 assert.equal(C.taskPhaseLabel({...interrupted,businessPhase:''},ctx.t),'需要恢复');
 assert.equal(C.taskPhaseLabel({...interrupted,phase:'ACCEPTANCE_PENDING',businessPhase:'IMPLEMENTING'},ctx.t),'等待验收');
 assert.equal(C.taskPhaseLabel({...interrupted,status:'SUCCEEDED'},ctx.t),'已完成');
 assert.equal(C.progressLabel('BLOCKED',ctx.t),'受阻');
});

test('one projected next step is primary in task list and task outcome, not a competing recovery state',()=>{
 const nextStep={code:'ANALYZE_RUN_OUTPUT',label:'待 GPT 分析实施结果',summary:'先分析本次实施输出，再决定后续处理。',taskId:'task-a',runId:'impl-a',purpose:'IMPLEMENTATION'};
 const value=task('task-a','ACTIVE','RECOVERY_REQUIRED',{businessPhase:'VERIFYING',nextStep,currentActivity:{summary:'已中断，等待恢复。'}}),ctx=context({projectWorkspace:{tasks:[value],featureAreas:[]}},'zh-CN');
 const {createDocument}=require('./testdata/product_dom_harness.js'),doc=createDocument(W.renderTasks(ctx)),body=doc.querySelector('tbody').textContent;
 assert.match(body,/待 GPT 分析实施结果/);assert.match(body,/先分析本次实施输出/);assert.doesNotMatch(body,/等待恢复/);
 const TW=require('./ui/product-task-workbench.js'),outcome=TW.taskOutcome(value,ctx);assert.match(outcome,/待 GPT 分析实施结果/);assert.doesNotMatch(outcome,/等待恢复/);
 assert.equal(C.taskNextStep({...value,status:'CANCELED'}),null);assert.equal(C.taskNextStep({...value,nextStep:{...nextStep,taskId:'other'}}),null);
 assert.equal(value.phase,'RECOVERY_REQUIRED');assert.equal(value.currentActivity.summary,'已中断，等待恢复。');
});

test('recovery is scoped to a missing native result, while received results show workflow processing',()=>{
 for(const [resultState,required,code,label] of [['UNKNOWN',true,'RECOVER_RUN_RESULT','待找回验证结果'],['RECEIVED',false,'PROCESS_RUN_RESULT','验证结果已收到，待处理'],['NOT_STARTED',false,'PROCESS_RUN_RESULT','验证尚未开始，待处理']]){
  const runRecovery={runId:'verify',purpose:'VERIFICATION',resultState,required,label,summary:'这次验证运行的准确结果状态'},nextStep={code,label,summary:runRecovery.summary,taskId:'task-a',runId:'verify',executionId:'execution',taskDefinitionRevisionId:'revision',purpose:'VERIFICATION'};
  const value=task('task-a','ACTIVE','RECOVERY_REQUIRED',{businessPhase:'VERIFYING',runRecovery,nextStep,currentExecution:{id:'execution',taskDefinitionRevisionId:'revision'},details:{taskDefinitionRevisionId:'revision'},verificationRun:{id:'verify',nativeResultState:resultState},currentActivity:{summary:runRecovery.summary}}),ctx=context({projectWorkspace:{tasks:[value],featureAreas:[]}},'zh-CN');
  assert.equal(C.taskNextStep(value),nextStep);
  const {createDocument}=require('./testdata/product_dom_harness.js'),doc=createDocument(W.renderTasks(ctx));assert.match(doc.querySelector('tbody').textContent,new RegExp(label));assert.doesNotMatch(doc.querySelector('tbody').textContent,/等待恢复/);
  const TW=require('./ui/product-task-workbench.js');assert.match(TW.taskOutcome(value,ctx),new RegExp(label));
  assert.equal(C.taskNextStep({...value,verificationRun:{id:'old',nativeResultState:resultState}}),null);
  assert.equal(C.taskNextStep({...value,runRecovery:{...runRecovery,resultState:'OTHER'}}),null);
  const compact=task('task-a','ACTIVE','RECOVERY_REQUIRED',{businessPhase:'VERIFYING',runRecovery,nextStep,currentExecutionId:'execution',current_definition_revision_id:'revision',currentActivity:{summary:runRecovery.summary}}),compactCtx=context({projectWorkspace:{tasks:[compact],featureAreas:[]}},'zh-CN');
  assert.equal(C.taskNextStep(compact),nextStep);assert.match(createDocument(W.renderTasks(compactCtx)).querySelector('tbody').textContent,new RegExp(label));
  assert.equal(C.taskNextStep({...compact,currentExecutionId:'old-execution'}),null);
 }
});
