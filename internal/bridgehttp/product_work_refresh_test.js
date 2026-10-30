"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict");
const {create}=require("./ui/product-work-refresh.js");
function fixture(){
 const data=new Map(),timers=new Map(),calls=[],applied=[];let hidden=false,id=0,changed=0;
 const pending=[];
 const controller=create({storage:{getItem:k=>data.get(k)||null,setItem:(k,v)=>data.set(k,v)},hidden:()=>hidden,
 setTimer:(fn,ms)=>{timers.set(++id,{fn,ms});return id;},clearTimer:id=>timers.delete(id),
 read:target=>{calls.push(target);return new Promise((resolve,reject)=>pending.push({resolve,reject}));},selectTaskView:payload=>payload?.presentation?.taskView,apply:(target,payload)=>applied.push({target,payload}),changed:()=>changed++});
 return {controller,timers,calls,applied,pending,hide:v=>hidden=v,tick:()=>{const [id,t]=timers.entries().next().value;timers.delete(id);t.fn();},changed:()=>changed};
}
const settle=()=>new Promise(resolve=>setImmediate(resolve));

const fs=require('node:fs'),vm=require('node:vm'),app=fs.readFileSync(`${__dirname}/ui/product-app.js`,'utf8');
test("FeatureArea whitespace/status and Enter/Space open exact row; independent actions stop propagation",()=>{
 const opened=[],rowEvents={},actionEvents={};
 const action={addEventListener:(name,fn)=>actionEvents[name]=fn};
 const row={dataset:{topicRow:'exact-area'},addEventListener:(name,fn)=>rowEvents[name]=fn,querySelectorAll:()=>[action]};
 const context={$$:()=>[row],openTopicDetail:id=>opened.push(id)};vm.createContext(context);
 vm.runInContext(app.slice(app.indexOf("    $$('[data-topic-row]')"),app.indexOf("    $$('#page [data-menu-commit]')")),context);
 for(const region of ['whitespace','status'])rowEvents.click({target:{closest:()=>null}});
 for(const key of ['Enter',' ']){let prevented=false;rowEvents.keydown({target:row,key,preventDefault:()=>prevented=true});assert.ok(prevented);}
 rowEvents.keydown({target:action,key:'Enter',preventDefault:()=>assert.fail('nested action')});
 rowEvents.click({target:{closest:()=>action}});assert.equal(opened.length,4);assert.ok(opened.every(id=>id==='exact-area'));
 let stopped=false;actionEvents.click({stopPropagation:()=>stopped=true});assert.ok(stopped);
 assert.match(app,/data-topic-row=.*aria-label=/);assert.match(app,/topic-row" role="row" tabindex="0"/);
});
test("terminate uses observed capability and canonical exact target; accepted remains canceling; fresh even off",async()=>{
 let confirm=false,mutations=0,fresh=0,payload,url,release;
 const context={qs:v=>new URLSearchParams(v).toString(),taskActionIdentity:value=>JSON.stringify(value),C:{esc:String},state:{selectedProjectID:'project',projectTab:'executions'},workDrawerTaskID:'',workflowDrawerVersion:0,window:{confirm:()=>confirm,crypto:{randomUUID:()=> 'request-identity'}},toast:()=>{},workRefresh:{freshAfterAction:async()=>fresh++},api:(path,options)=>{mutations++;url=path;payload=JSON.parse(options.body);return new Promise(resolve=>release=resolve);}};
 vm.createContext(context);vm.runInContext(app.slice(app.indexOf('  const executionCancelRequests='),app.indexOf('  function openExecutionDetail(')),context);
 const button={disabled:false,dataset:{executionCancel:'execution',runId:'run'}};
 await context.cancelExecution(button);assert.equal(mutations,0);confirm=true;
 const execution={id:'execution',run_id:'run',state:'RUNNING',cancellation_capability:{available:true,target:{executionId:'execution',runId:'run'}}};
 assert.match(context.executionCancelControl(execution),/data-execution-cancel="execution"/);
 const p=context.cancelExecution(button);assert.equal(button.textContent,'正在终止…');assert.equal(button.disabled,true);assert.equal(execution.state,'RUNNING');
 assert.equal(url,'/ui/api/executions/execution/cancel?project_id=project');assert.deepEqual(JSON.parse(JSON.stringify(payload)),{runId:'run',requestId:'request-identity'});
 release({state:'CANCEL_REQUESTED'});await p;assert.equal(fresh,1);assert.equal(execution.state,'RUNNING');assert.equal(button.textContent,'正在终止…');
 assert.match(context.executionCancelControl({...execution,cancellation:{state:'CANCEL_REQUESTED'}}),/disabled>正在终止/);
 for(const state of ['CANCELED','UNKNOWN_ORPHANED'])assert.equal(context.executionCancelControl({...execution,state,cancellation_capability:{available:false}}),'');
 assert.equal(context.executionCancelControl({...execution,run_id:'other-run'}),'');
 assert.ok(!app.slice(app.indexOf('async function cancelExecution('),app.indexOf('function openExecutionDetail(')).includes('loadProjectData'));
});


test("authoritative Work status and UNKNOWN orphan receipt have explicit product labels",()=>{
 const status=require('./ui/product-status.js'),i18n=require('./ui/product-i18n.js').createI18n({navigator:{languages:['zh-CN']}});
 for(const raw of ['RUNNING','SUCCEEDED','CANCEL_REQUESTED','UNKNOWN_ORPHANED']){
  const shown=status.presentStatus('product',raw,i18n.t);assert.notEqual(shown.label,i18n.t('status.unknown.label'));assert.doesNotMatch(shown.label,/^\[/);
 }
 assert.match(status.presentStatus('product','UNKNOWN_ORPHANED',i18n.t).label,/未知.*隔离/);
});

test('Review current activity collapses locally, preserves refresh, resets navigation and never duplicates message',()=>{
 const body={hidden:true},attrs={'aria-controls':'agent-current-activity'},button={setAttribute:(k,v)=>attrs[k]=v,getAttribute:k=>attrs[k]};
 const context={t:k=>k,C:{...require("./ui/product-components.js"),esc:String,keyValueDetails:rows=>rows.map(v=>v.join(':')).join('|')},document:{getElementById:()=>body}};vm.createContext(context);
 vm.runInContext(app.slice(app.indexOf('  function relativeActivity('),app.indexOf('  async function answerAgentRequest(')),context);
 const runtime={availability:'AVAILABLE',currentItem:{type:'agentMessage',status:'completed',metadata:{text:'exact bounded message'}},lastActivityAt:new Date().toISOString(),waitingState:'WAITING_FOR_PERMISSION'};
 context.selectActivityContext('area/review');let html=context.renderAgentRuntime(runtime);
 assert.match(html,/aria-expanded="false"/);assert.match(html,/id="agent-current-activity" hidden/);assert.match(html,/<details><summary>活动诊断/);
 assert.equal(require('./testdata/product_dom_harness.js').createDocument(html).root.textContent.split('exact bounded message').length-1,1);assert.ok(html.indexOf('等待权限确认')<html.indexOf('exact bounded message'));
 context.toggleActivity(button);assert.equal(attrs['aria-expanded'],'true');assert.equal(body.hidden,false);
 context.selectActivityContext('area/review');assert.match(context.renderAgentRuntime(runtime),/aria-expanded="true"/);
 context.toggleActivity(button);assert.equal(body.hidden,true);context.toggleActivity(button);
 context.selectActivityContext('another/review');html=context.renderAgentRuntime(runtime,true);assert.match(html,/aria-expanded="false"/);assert.match(html,/最后活动 · Agent message/);
 // Native button semantics supply Enter/Space activation through the delegated click.
 assert.match(html,/<button type="button" data-agent-activity/);assert.match(app,/if\(activity\)\{toggleActivity\(activity\);return;\}/);
});
test('Review token shows exact counts while Task list has no Run-derived usage authority',()=>{
 const context={t:k=>k,C:{...require("./ui/product-components.js"),esc:String,keyValueDetails:rows=>rows.map(v=>v.join(':')).join('|')}};vm.createContext(context);
 vm.runInContext(app.slice(app.indexOf('  function relativeActivity('),app.indexOf('  async function answerAgentRequest(')),context);
 vm.runInContext(app.slice(app.indexOf('  function workItems('),app.indexOf('  function renderWorkItemRows(')),context);
 const usage={status:'AVAILABLE',usage:{input_tokens:584383,cached_input_tokens:474624,output_tokens:3111,reasoning_output_tokens:1166,total_tokens:587494},model_context_window:258400};
 const html=context.reviewTokenBlock(usage);for(const count of ['584,383','474,624','3,111','1,166','587,494','258,400'])assert.ok(html.includes(count));assert.doesNotMatch(html,/非缓存/);
 assert.match(context.reviewTokenBlock({status:'PENDING',usage:{total_tokens:0}}),/统计中/);assert.doesNotMatch(context.reviewTokenBlock({status:'UNAVAILABLE',usage:{total_tokens:0}}),/总计|:0/);assert.match(context.reviewTokenBlock(null),/不可用/);
 const items=context.workItems({tasks:[{id:'semantic-task',title:'Task undergoing review',phase:'REVIEWING',status:'ACTIVE',latest_run:{id:'exact',usage}}],executions:[{task_id:'semantic-task',usage:{status:'UNAVAILABLE'}}]});assert.equal(items[0].usage,undefined);assert.equal(items[0].id,'semantic-task');
});

test("pending DEVELOPMENT without a Run renders cancellation and sends no fabricated Run",async()=>{
 let sent,refreshed=0;
 const context={qs:v=>new URLSearchParams(v).toString(),taskActionIdentity:value=>JSON.stringify(value),C:{esc:String},state:{selectedProjectID:'project',projectTab:'executions'},workDrawerTaskID:'',workflowDrawerVersion:0,window:{confirm:()=>true,crypto:{randomUUID:()=> 'pending-request'}},toast:()=>{},workRefresh:{freshAfterAction:async()=>refreshed++},api:async(path,options)=>{sent={path,body:JSON.parse(options.body)};return {state:'CANCELED',backendInterruptState:'NO_BACKEND'};}};
 vm.createContext(context);vm.runInContext(app.slice(app.indexOf('  const executionCancelRequests='),app.indexOf('  function openExecutionDetail(')),context);
 const execution={id:'pending-execution',kind:'DEVELOPMENT',state:'PENDING',cancellation_capability:{available:true,target:{executionId:'pending-execution'}}};
 const html=context.executionCancelControl(execution);assert.match(html,/data-execution-cancel="pending-execution"/);assert.match(html,/data-run-id=""/);assert.doesNotMatch(html,/undefined/);
 await context.cancelExecution({disabled:false,dataset:{executionCancel:'pending-execution',runId:''}});
 assert.deepEqual(sent,{path:'/ui/api/executions/pending-execution/cancel?project_id=project',body:{runId:'',requestId:'pending-request'}});assert.equal(refreshed,1);
});

test('only opened nonterminal Task polls; terminal response is the final read',async()=>{
 const f=fixture();const first=f.controller.enter('p','task-a');assert.equal(f.calls.length,1);assert.equal(f.calls[0].taskId,'task-a');
 f.pending[0].resolve({presentation:{taskView:{terminal:false}}});await first;assert.equal(f.timers.size,1);
 f.tick();assert.equal(f.calls.length,2);f.controller.refresh();assert.equal(f.calls.length,2);
 f.pending[1].resolve({presentation:{taskView:{terminal:true}}});await settle();assert.equal(f.timers.size,0);assert.equal(f.applied.length,2);
 await f.controller.stop();assert.equal(f.timers.size,0);
 const start=f.controller.enter('p','terminal');f.pending[2].resolve({presentation:{taskView:{terminal:true}}});await start;assert.equal(f.timers.size,0);
});
test('Task switch and close abort and fence old responses; hidden pauses without list reads',async()=>{
 const f=fixture();const a=f.controller.enter('p','a'),b=f.controller.enter('p','b');assert.equal(f.calls[0].signal.aborted,true);
 f.pending[0].resolve({old:true});await a;assert.equal(f.applied.length,0);
 f.pending[1].resolve({presentation:{taskView:{terminal:false}}});await b;assert.equal(f.applied[0].target.taskId,'b');
 f.hide(true);f.controller.visibility();assert.equal(f.timers.size,0);f.hide(false);const resume=f.controller.visibility();assert.equal(f.calls[2].taskId,'b');
 f.controller.stop();assert.equal(f.calls[2].signal.aborted,true);f.pending[2].resolve({late:true});await resume;assert.equal(f.applied.length,1);
 assert.ok(!app.slice(app.indexOf('const workRefresh ='),app.indexOf('async function api')).includes('/work-tabs/'));
});
test('Task failure retries one scoped read and preserves last presentation',async()=>{
 const f=fixture();const first=f.controller.enter('p','a');f.pending[0].resolve({presentation:{taskView:{terminal:false}}});await first;
 f.tick();f.pending[1].reject(new Error('offline'));await settle();assert.equal(f.applied.length,1);assert.equal(f.controller.error(),'offline');assert.equal(f.timers.size,1);
 f.controller.stop();assert.equal(f.timers.size,0);
});
function taskRenderContext(){
 const context={C:{...require("./ui/product-components.js"),esc:value=>String(value??''),statusBadge:(_d,s)=>s,localizedTimestamp:value=>String(value||'—')},i18n:{locale:'en'},t:k=>k,renderAgentRequests:()=>'',TW:require("./ui/product-task-workbench.js"),detailRoute:{get:()=>({activeTab:"overview"})},taskRunsPage:null,openedTask:null,taskActionRequests:new Map(),state:{selectedProjectID:'project'},taskActionIdentity:value=>JSON.stringify(value),reviewApprovalIdentity:()=>null};
 vm.createContext(context);vm.runInContext(app.slice(app.indexOf('  function directFileActivityBadge('),app.indexOf('  function paintTaskDetail(')),context);return context;
}
test('Direct File activity badge is presentation-only and appears in the Task header',()=>{
 const context=taskRenderContext();
 assert.equal(context.directFileActivityBadge({}), '');
 const badge=context.directFileActivityBadge({directFile:{enabled:true,workflowId:'workflow-a'}});
 assert.match(badge,/data-direct-file-active="true"/);assert.match(badge,/DIRECT_FILE ACTIVE/);assert.match(badge,/workflow-a/);
 const p={directFile:{enabled:true,workflowId:'workflow-a'},taskView:{id:'task',title:'Task',goal:'Goal',phase:'IMPLEMENTING',phaseLabel:'Implementation',phaseDescription:'Working',status:'ACTIVE',progress:[],actions:[],usage:{}}};
 const header=context.renderTaskPresentation(p).split('</header>')[0];assert.match(header,/data-direct-file-active="true"/);assert.match(header,/DIRECT_FILE ACTIVE/);
});
test('Task overview shows progress and does not leak the Run inventory',()=>{
 const context=taskRenderContext();
 const p={taskView:{id:'task',title:'Semantic task',phase:'REVIEWING',status:'ACTIVE',progress:[{phase:'REVIEW',status:'CURRENT'}],actions:[],details:{runs:[{id:'verify-old',runPurpose:'VERIFICATION',transportStatus:'SUCCEEDED'}],runsTotal:1}}};
 const html=context.renderTaskPresentation(p);
 assert.match(html,/aria-label="Task progress"/);
 assert.doesNotMatch(html,/data-task-run-list|data-task-run="verify-old"|data-execution-history|data-review-history|current-work/);
 assert.match(html,/task-tabs|role="tabpanel"/);
 const css=fs.readFileSync(`${__dirname}/ui/product.css`,'utf8');assert.match(css,/\.task-operation-area \.detail-actions\s*\{[^}]*flex-direction: row/);
});

test('Task actions require the matching server operator facts and never invent an actionable control',()=>{
 const context=taskRenderContext();
 for(const [phase,actions] of [
 ['REVIEW_REQUIRED',['START_TASK_REVIEW']],['REVIEWING',[]],['REVIEW_APPROVAL_PENDING',['APPROVE','REVISE','REJECT']],
 ['IMPLEMENTATION_READY',['START_EXECUTION']],['IMPLEMENTING',[]],['VERIFICATION_READY',['START_VERIFICATION']],['VERIFYING',[]],
 ['ACCEPTANCE_PENDING',['ACCEPT','REQUEST_CHANGES','REQUEST_REVERIFICATION','REJECT']],['SUCCEEDED',[]],['CANCELED',[]],['FAILED',[]]]){
  const p={taskView:{id:'task',phase,allowedReviewDepths:['DEEP'],status:['SUCCEEDED','CANCELED','FAILED'].includes(phase)?phase:'ACTIVE',actions:actions.map(action=>({action,available:true})),progress:[],reviewHistory:[],executionHistory:[]}};
  const html=context.renderTaskPresentation(p);
  assert.deepEqual([...html.matchAll(/data-task-action="([^"]+)"/g)].map(m=>m[1]),[],phase);
  if(actions.length)assert.match(html,/workbench\.actionsUnconfirmed/);
  assert.doesNotMatch(html,/ILLEGAL_DISABLED/);
 }
});

test('re-review is one feedback-bound command and exposes no second manual Review start',()=>{
 const context=taskRenderContext(),target={taskId:'task',featureAreaId:'area',subjectId:'task',decisionId:'REVIEW_APPROVAL:review-1'};
 const action={action:'REVISE',available:true,target},view={id:'task',phase:'REVIEW_APPROVAL_PENDING',status:'ACTIVE',actionsStatus:'AVAILABLE',actions:[action],progress:[]};
 const presentation={taskView:view,details:{featureAreaId:'area'},taskOptions:{},workflowGuidance:{projectId:'project',featureAreaId:'area',taskId:'task',reviewApproval:{decisionId:target.decisionId,reviewId:'review-1',resultSha256:'a'.repeat(64),provenanceSha256:'b'.repeat(64)}}};
 context.openedTask={operatorActions:[{...action,domainAvailable:true,channelAvailable:true}]};
 const html=context.renderTaskActionControl(presentation,view,action);
 assert.match(html,/data-re-review/);assert.match(html,/name="reviewFeedback"/);assert.match(html,/maxlength="8192"/);
 assert.doesNotMatch(html,/START_TASK_REVIEW|backendConfigId|reasoningEffort|repositoryTargets|newReviewId|newRunId/);
 assert.match(context.taskActionConsequence(action),/同一 Task|自动准备和启动/);
});

test('invalid Review result exposes same-Task retry, repository navigation and abandon controls',()=>{
 const context=taskRenderContext(),target={taskId:'task',featureAreaId:'area',subjectId:'task',decisionId:'INVALID_REVIEW_RESULT_RESOLUTION:review-1'};
 const actions=['REQUEST_RE_REVIEW','OPEN_PROJECT_REPOSITORIES','ABANDON_TASK'].map(action=>({action,available:true,target}));
 const view={id:'task',phase:'REVIEWING',status:'ACTIVE',actionsStatus:'AVAILABLE',actions,progress:[]},presentation={taskView:view,details:{featureAreaId:'area'},taskOptions:{}};
 context.openedTask={operatorActions:actions.map(action=>({...action,domainAvailable:true,channelAvailable:true}))};
 const rendered=actions.map(action=>context.renderTaskActionControl(presentation,view,action));
 assert.match(rendered[0],/data-re-review/);assert.match(rendered[0],/name="reviewFeedback"/);
 assert.match(rendered[1],/data-open-project-repositories/);
 assert.match(rendered[2],/data-task-action="ABANDON_TASK"/);
 assert.doesNotMatch(rendered.join(''),/repositoryId|memberId|originCommit|START_TASK_REVIEW/);
});

test('accepted Task keeps scoped polling through auto-commit and stops on commit success or failure',async()=>{
 for(const outcome of ['COMMITTED','FAILED']){
 const f=fixture(),start=f.controller.enter('project','accepted-task');
 f.pending.shift().resolve({presentation:{taskView:{terminal:true,commitActivity:'RUNNING'}}});await start;
 assert.equal(f.timers.size,1);f.tick();
 f.pending.shift().resolve({presentation:{taskView:{terminal:true,commitActivity:outcome==='FAILED'?'FAILED':'',commitStatus:outcome}}});await settle();
 assert.equal(f.timers.size,0);assert.ok(f.calls.every(v=>v.taskId==='accepted-task'&&v.project==='project'));
 }
});


test('Review depth choices remain unavailable until the matching operator capability is read',()=>{
 const context=taskRenderContext();
 const view={id:'semantic-task',title:'Semantic title',phase:'REVIEW_REQUIRED',phaseLabel:'Review',phaseDescription:'需要深度审查',status:'ACTIVE',requiredReviewDepth:'FAST',allowedReviewDepths:['FAST','DEEP'],actions:[{action:'START_TASK_REVIEW',available:true}],reviewHistory:[{id:'fast',taskId:'semantic-task',ordinal:1,reviewDepth:'FAST',reviewDisposition:'FURTHER_REVIEW_REQUIRED',status:'SUCCEEDED',instruction:'Instruction is not title'},{id:'deep',taskId:'semantic-task',ordinal:2,reviewDepth:'DEEP',reviewDisposition:'APPROVAL_READY',status:'SUCCEEDED'}]};
 let html=context.renderTaskPresentation({taskView:view});
 assert.match(html,/workbench\.actionsUnconfirmed/);
 assert.doesNotMatch(html,/value="FAST">快速审查|value="DEEP">深度审查|data-review-history|data-review-depth/);

 assert.doesNotMatch(html,/FAILED|Failed|审查失败/);assert.match(html,/data-task-id="semantic-task"/);
 view.requiredReviewDepth='DEEP';view.allowedReviewDepths=['DEEP'];html=context.renderTaskPresentation({taskView:view});
 assert.doesNotMatch(html,/value="FAST"|快速审查|value="DEEP">深度审查/);
 view.phase='REVIEW_APPROVAL_PENDING';view.phaseDescription='等待审批';view.requiredReviewDepth=null;view.allowedReviewDepths=[];view.actions=[{action:'APPROVE',available:true,target:{subjectId:'semantic-task',decisionId:'latest'}}];html=context.renderTaskPresentation({taskView:view});
 assert.match(html,/REVIEW_APPROVAL_PENDING/);assert.equal((html.match(/data-task-action="APPROVE"/g)||[]).length,0);
 assert.doesNotMatch(html,/data-review-history|data-execution-history/);
 assert.ok(app.includes('reviewDepth:button.value'));
 assert.ok(app.includes('taskFormDraftAuthority'));
});
