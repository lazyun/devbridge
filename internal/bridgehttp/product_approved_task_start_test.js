"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
function fixture(){
  const route={project:"project",rootTask:"task",view:"TASK",activeTab:"overview"},sent=[];
  const context={C:{esc:v=>String(v??"")},t:k=>k,state:{selectedProjectID:"project"},taskActionRequests:new Map(),taskActionNavigation:0,detailRoute:{get:()=>route},detailReadBusy:false,detailReadError:"",mutationSubmitted:false,saveTaskState:()=>{},updateDetailReadState:()=>{},toast:()=>{},qs:v=>new URLSearchParams(v),$:()=>({querySelector:()=>null}),workRefresh:{stop:()=>{},enter:async()=>({ok:true})},api:async(path,options)=>{sent.push({path,body:options.body});return {run:{id:"original-run"}};}};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf("  function taskActionIdentity("),source.indexOf("  function taskFormDraftAuthority(")),context);
  // Test the production submit path with an intentionally empty fresh action
  // list: a successful lost-response start has already removed its old button.
  vm.runInContext(source.slice(source.indexOf("  const reviewAdmissionCodes="),source.indexOf("  function openTaskDetail(")),context);
  return {context,route,sent};
}
test("approved start displays explicit model/backend selection without replacement instruction fields",()=>{
  const {context}=fixture(),target={taskId:"task",subjectId:"task",featureAreaId:"area",taskDefinitionRevisionId:"revision",primaryMemberId:"primary",approvedWorksetHash:"a".repeat(64)};
  const action={action:"START_EXECUTION",available:true,target,requiredInputs:["nextRunRuntimeConfig","backendSelection"]},view={id:"task",actionsStatus:"AVAILABLE",actions:[action]};
  const presentation={details:{featureAreaId:"area"},taskView:view,taskOptions:{runtimeOptions:[{modelId:"model",enabled:true,supportedReasoningEfforts:["high"]}],backendInventoryComplete:true,backendCandidates:[{configId:"backend",configFingerprint:"b".repeat(64)}]}};
  context.openedTask={operatorActions:[{...action,domainAvailable:true,channelAvailable:true}]};
  const html=context.renderTaskActionControl(presentation,view,action);
  for(const field of ["data-approved-task-start","data-runtime-form","name=\"model\"","name=\"reasoningEffort\"","name=\"backendConfigId\"","data-backend-fingerprint"])assert.ok(html.includes(field),field);
  assert.doesNotMatch(html,/name="implementationInstruction"|name="verificationInstruction"/);
  const frozen={...action,requiredInputs:[]};
  context.openedTask.operatorActions=[{...frozen,domainAvailable:true,channelAvailable:true}];
  const frozenHTML=context.renderTaskActionControl(presentation,view,frozen);
  assert.match(frozenHTML,/data-task-action="START_EXECUTION"/);
  assert.doesNotMatch(frozenHTML,/name="model"|name="backendConfigId"/);
});
test("lost start response replays the saved selection after the current start capability disappeared",async()=>{
  const {context,sent}=fixture(),task={id:"task",presentation:{taskView:{id:"task",actionsStatus:"AVAILABLE",actions:[]}}};
  context.openedTask=task;
  const body=JSON.stringify({action:"START_EXECUTION",requestId:"original-request",target:{taskId:"task",taskDefinitionRevisionId:"old-revision"},nextRunRuntimeConfig:{model:"original-model",reasoningEffort:"high"},backendSelection:{configId:"original-backend",configFingerprint:"original-fingerprint"}});
  const record={project:"project",taskId:"task",action:"START_EXECUTION",key:"saved",requestId:"original-request",state:"UNKNOWN",body};
  context.taskActionRequests.set(record.key,record);
  await context.submitTaskAction(task,{dataset:{taskReplay:record.requestId},isConnected:true,disabled:false},record);
  assert.equal(sent.length,1);assert.equal(sent[0].body,body);assert.equal(record.state,"CONFIRMED");
  assert.equal(context.taskActionRequests.has(record.key),false);
  // A stale or injected replay button cannot submit a record absent from the
  // exact saved request map, even with the same caller-visible request ID.
  record.state="UNKNOWN";
  await context.submitTaskAction(task,{dataset:{taskReplay:record.requestId},isConnected:true,disabled:false},record);
  assert.equal(sent.length,1);
});
