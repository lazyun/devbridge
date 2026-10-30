"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const TW=require("./ui/product-task-workbench.js"),{create}=require("./ui/product-work-refresh.js");
const C={...require("./ui/product-components.js"),esc:v=>String(v??"").replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll('"',"&quot;"),localizedTimestamp:v=>v||"UNKNOWN",keyValueDetails:rows=>rows.map(([k,v])=>`${k}: ${v}`).join("\n")};
const options={C,t:key=>key,locale:"en",rootTask:"task"};
const deferred=()=>{let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};};
test("typed route preserves exact root, tab, stable expansion, actual scroll, filters and scoped draft through Run and Back",()=>{
 const r=TW.createRoute();r.task("project","task-a","tasks");r.tab("executions");
 r.save({expanded:["execution:old","revision:v2"],scrollTop:749,runFilter:"VERIFICATION"});
 const draft=r.draftKey("decision-1/revision-2");r.drafts.set(draft,{nextWorkSpec:"unsaved goal"});
 const before=r.get().generation;r.run("exact-run");assert.equal(r.get().target,"exact-run");r.tab("evidence");r.back();
 assert.equal(r.get().rootTask,"task-a");assert.equal(r.get().project,"project");assert.equal(r.get().activeTab,"executions");assert.deepEqual(r.get().expanded,["execution:old","revision:v2"]);assert.equal(r.get().scrollTop,749);assert.equal(r.get().runFilter,"VERIFICATION");assert.ok(r.get().generation>before);assert.equal(r.drafts.get(draft).nextWorkSpec,"unsaved goal");
 r.task("project","task-b");assert.notEqual(r.draftKey("decision-1/revision-2"),draft);assert.equal(r.get().activeTab,"overview");
 r.task("project","task-a");assert.equal(r.get().scrollTop,749);assert.equal(r.drafts.has(r.draftKey("decision-2/revision-2")),false);
});
test("same-route replacement, Task/Run/Project changes and close abort and fence every older read",()=>{
 const r=TW.createRoute();r.task("p","a");const a=r.read(),b=r.read();assert.equal(a.signal.aborted,true);assert.equal(a.current(),false);assert.equal(b.current(),true);
 r.run("run-a");assert.equal(b.current(),false);const run=r.read();r.back();assert.equal(run.current(),false);
 const back=r.read();r.task("other-project","a");assert.equal(back.current(),false);const other=r.read();r.close();assert.equal(other.current(),false);assert.equal(other.signal.aborted,true);
});
test("interactive refresh blocks only its owner while background merges and keeps the last good snapshot on failure",async()=>{
 const reads=[],paint=[],timers=new Map();let nextTimer=0;
 const refresh=create({read:target=>{const d=deferred();reads.push({target,...d});return d.promise;},selectTaskView:payload=>payload?.presentation?.taskView,apply:(_,data)=>paint.push(data),setTimer:fn=>{timers.set(++nextTimer,fn);return nextTimer;},clearTimer:id=>timers.delete(id)});
 const first=refresh.enter("p","a");assert.equal(refresh.busy(),true);refresh.refresh();assert.equal(reads.length,1);reads[0].resolve({presentation:{taskView:{terminal:false}}});await first;assert.equal(refresh.busy(),false);
 const background=refresh.refresh();assert.equal(refresh.busy(),false);const interactive=refresh.refresh("INTERACTIVE");assert.equal(reads[1].target.signal.aborted,true);assert.equal(refresh.busy(),true);
 reads[1].resolve({wrong:true});await background;assert.equal(refresh.busy(),true);assert.equal(paint.length,1);
 reads[2].reject(new Error("offline"));await interactive;assert.equal(refresh.busy(),false);assert.equal(refresh.error(),"offline");assert.equal(paint.length,1);refresh.stop();assert.equal(timers.size,0);
});
test("Run overview preserves AVAILABLE zero, pending/unavailable tokens, frozen identity and valid durations",()=>{
 for(const status of ["AVAILABLE","PENDING","UNAVAILABLE"]){
  const html=TW.renderRun({run:{id:"r",purpose:"VERIFICATION",status:"SUCCEEDED",task_id:"task",task_execution_id:"business-execution",agent_execution_id:"agent-identity",token_usage:{status,...(status==="AVAILABLE"?{counts:{input_tokens:0,output_tokens:0,total_tokens:0}}:{})}},frozen_config:{model:"frozen-model",reasoning_effort:"xhigh",source:"EXECUTION",task_definition_revision_id:"revision-old"},elapsed_ms:0,duration_complete:true},options);
  assert.match(html,new RegExp(status));assert.match(html,/business-execution/);assert.match(html,/frozen-model/);assert.match(html,/xhigh/);assert.match(html,/revision-old/);assert.match(html,/0:00/);
  if(status==="AVAILABLE")assert.match(html,/total_tokens[^\n]*0/);else assert.doesNotMatch(html,/total_tokens/);
 }
 for(const ms of [null,undefined,-1,NaN])assert.equal(TW.duration(ms,true,"UNKNOWN"),"UNKNOWN");assert.equal(TW.duration(61000,false,"UNKNOWN"),"1:01 …");
 const missing=TW.renderRun({run:{id:"r",purpose:"IMPLEMENTATION",token_usage:{status:"UNAVAILABLE"}}},options);assert.match(missing,/workbench.unknown/);assert.doesNotMatch(missing,/gpt-|Desktop|0:00/);
});
test("Run activities keep object payloads escaped and read-only; task-owned requests link back",()=>{
 const html=TW.renderRun({run:{id:"r",purpose:"IMPLEMENTATION"},runtime:{runId:"r",requests:[{state:"PENDING",payload:{reason:"<script>evil()</script>",requestedSchema:{type:"object"}}}]}},{...options,activeTab:"activity"});
 assert.match(html,/&lt;script>/);assert.match(html,/requestedSchema/);assert.match(html,/data-run-back/);assert.doesNotMatch(html,/<script>|<form|data-agent-answer|data-task-action/);
 const diagnostics=TW.renderRun({run:{id:"inspection",purpose:"INSPECTION",task_id:"historical-technical-id"}},{...options,rootTask:"",source:"diagnostics"});assert.match(diagnostics,/workbench.backDiagnostics/);assert.doesNotMatch(diagnostics,/data-task-detail/);
 const owned=TW.renderRun({run:{id:"r",purpose:"SCOPE_REVIEW",task_id:"semantic-task"}},{...options,rootTask:"",source:"diagnostics"});assert.match(owned,/data-task-detail="semantic-task"/);
});
test("Run evidence separates verification report from transport success and retains exact Run diagnostics",()=>{
 const verify=TW.renderRun({run:{id:"v",purpose:"VERIFICATION",status:"SUCCEEDED"},verification_report:{passed:false,checks:[{status:"FAILED"}]}},{...options,activeTab:"evidence"});assert.match(verify,/passed[^\n]*false/);assert.match(verify,/FAILED/);assert.doesNotMatch(verify,/ACCEPT|data-task-action/);
 const missing=TW.renderRun({run:{purpose:"VERIFICATION"}},{...options,activeTab:"evidence"});assert.match(missing,/workbench.notGenerated/);
 const diagnostic=TW.renderRun({run:{id:"historical",purpose:"IMPLEMENTATION",agent_execution_id:"agent-old",task_execution_id:"business-old",thread_id:"thread-old",turn_id:"turn-old"},runtime_epoch:3},{...options,activeTab:"diagnostics"});for(const fact of ["historical","agent-old","business-old","thread-old","turn-old"])assert.ok(diagnostic.includes(fact));assert.match(diagnostic,/runtimeEpoch[^\n]*3/);
});
test("Run tables keep server order and Run ID identity despite changing elapsed/status and compact list is bounded",()=>{
 const runs=Array.from({length:20},(_,i)=>({id:`run-${i}`,executionId:"legacy-agent-id",taskExecutionId:`execution-${i}`,runPurpose:"VERIFICATION",transportStatus:"RUNNING",startedAt:"same-time",elapsedMs:i*1000,durationComplete:false}));
 const html=TW.runList(runs,{...options,total:140,offset:20,limit:20});assert.equal((html.match(/data-run-key/g)||[]).length,20);assert.ok(html.indexOf('data-run-key="run:run-0"')<html.indexOf('data-run-key="run:run-19"'));assert.doesNotMatch(html,/legacy-agent-id/);assert.match(html,/data-task-runs-page="40"/);
 const compact=TW.runList(runs,{...options,total:140,compact:true});assert.equal((compact.match(/data-run-key/g)||[]).length,5);assert.match(compact,/data-task-tab="runs"/);
 runs[0].transportStatus="SUCCEEDED";runs[0].elapsedMs=99999;const changed=TW.runList(runs,options);assert.ok(changed.indexOf('data-run-key="run:run-0"')<changed.indexOf('data-run-key="run:run-1"'));
});
test("Task mutation remains closed when operator action facts are absent",async()=>{
 const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8"),route={project:"p",rootTask:"t",view:"TASK"};let mutations=0,reads=0,failMutation=false;const identities=[];
 const context={taskActionRequests:new Map(),state:{selectedProjectID:"p"},workDrawerTaskID:"t",detailReadBusy:false,detailReadError:"",mutationSubmitted:false,detailRoute:{get:()=>route},saveTaskState:()=>{},updateDetailReadState:()=>{},toast:()=>{},qs:v=>new URLSearchParams(v),window:{crypto:{randomUUID:()=>`request-${mutations}`}},workRefresh:{error:()=>"",stop:()=>{},enter:async()=>{reads++;return {ok:false,error:"read offline"};},refresh:async()=>{reads++;return {ok:true};}},api:async(_,options)=>{mutations++;identities.push(JSON.parse(options.body).requestId);if(failMutation)throw new Error("connection lost");return {accepted:true};}};
 vm.createContext(context);vm.runInContext(source.slice(source.indexOf('  function taskActionIdentity('),source.indexOf('  function openTaskDetail(')),context);
 const task={id:"t",presentation:{taskView:{actions:[{action:"APPROVE",available:true,target:{decisionId:"current-only"}}]}}},button={dataset:{taskAction:"APPROVE"},disabled:false};
 await context.invokeTaskAction(task,button);assert.equal(mutations,0);assert.equal(reads,0);assert.equal(context.mutationSubmitted,false);assert.equal(context.taskActionRequests.size,0);assert.deepEqual(identities,[]);
});
test("Review evidence distinguishes unavailable history, non-applicability and partial read failure",()=>{
 for(const status of ["NOT_APPLICABLE","AVAILABLE","READ_FAILED"]){
  const html=TW.renderRun({run:{purpose:"SCOPE_REVIEW"},review_evidence_status:status,review_result_status:"UNAVAILABLE",...(status==="READ_FAILED"?{review_evidence_error:"history read failed"}:{})},{...options,activeTab:"evidence"});
  assert.match(html,new RegExp(status));if(status==="READ_FAILED"){assert.match(html,/role="alert"/);assert.match(html,/history read failed/);}
 }
 const empty=TW.runList([],{...options,total:0});assert.match(empty,/0–0 \/ 0/);
});

test("Review history identifies the actual executor and only real Run threads offer a session",()=>{
 const history=TW.reviewHistory({currentReview:{id:"ai"},reviewHistory:[{id:"ai",ordinal:1,executorKind:"UPPER_LLM",activity:{executionMethod:"AI 读取文件并提交审查报告",summary:"等待发起对话中的 AI",notice:"已至少 5 分钟没有新活动"}}]},options);
 assert.match(history,/AI 读取文件并提交审查报告/);assert.match(history,/5 分钟/);
 const logical=TW.renderRun({run:{id:"logical",purpose:"SCOPE_REVIEW",status:"RUNNING"}},{...options,activeTab:"overview"});
 assert.doesNotMatch(logical,/data-open-run-session/);
 const coding=TW.renderRun({run:{id:"real",purpose:"SCOPE_REVIEW",thread_id:"codex-thread"}},{...options,activeTab:"overview"});
 assert.match(coding,/data-open-run-session="real"/);
});

test("saved verification disposition takes precedence over historical recovery in outcome",()=>{
 const v={id:"task",status:"ACTIVE",phase:"RECOVERY_REQUIRED",runRecovery:{label:"旧恢复状态"},verificationDecisions:{hold:{note:"待办"}}};
 assert.match(TW.taskOutcome(v,options),/已暂存/);assert.doesNotMatch(TW.taskOutcome(v,options),/旧恢复状态/);
 v.verificationDecisions={completion:{state:"BLOCKED",error:"target drift"}};assert.match(TW.taskOutcome(v,options),/合入受阻/);assert.doesNotMatch(TW.taskOutcome(v,options),/旧恢复状态/);
 v.verificationDecisions.completion.state="COMPLETED";assert.match(TW.taskOutcome(v,options),/已完成并合入/);
});
