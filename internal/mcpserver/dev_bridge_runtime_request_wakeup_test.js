"use strict";
const test=require("node:test");
const assert=require("node:assert/strict");
const fs=require("node:fs");
const source=fs.readFileSync(`${__dirname}/dev_bridge_ui.html`,"utf8");

test("runtime-request wakeup claims an exact persisted request before notification and never answers it",()=>{
  const body=source.slice(source.indexOf("function maybeWakeRuntimeRequest"),source.indexOf("const readbackNote",source.indexOf("function maybeWakeRuntimeRequest")));
  assert.match(body,/candidate\.kind!=='CODEX_REQUEST'/);
  assert.match(body,/candidate\.projection\?\.lifecycleState!=='WAITING_FOR_USER'/);
  assert.match(body,/runtimeRequestId/);
  assert.match(body,/runtimeEpoch/);
  assert.match(body,/RUNTIME_WAKEUP_CLAIM/);
  assert.match(body,/result\.status!=='CLAIMED'/);
  assert.match(body,/request\('ui\/message',\{text:result\.prompt\}\)/);
  assert.doesNotMatch(body,/runtime_request_user_decision_submit|SUBMIT_AGENT_USER_DECISION|answers:/);
});

test("failed ChatGPT follow-up preserves Core UNKNOWN and cannot wake or resend it",async()=>{
 const vm=require("node:vm");
 const start=source.indexOf("  function maybeWakeRuntimeRequest("),end=source.indexOf("  // Bounded, component-local diagnostics",start);
 let messages=0,claims=0,finish;
 const context={singlePanel:false,generation:1,sessionID:"session",disposed:false,runtimeWakeupAttempts:new Set(),RUNTIME_WAKEUP_CLAIM:"claim",toolResult:v=>v,
 call:()=>{claims++;return Promise.resolve({status:"CLAIMED",runtimeRequestId:"request",prompt:"Original question"});},
 request:()=>{messages++;return new Promise((_,reject)=>{finish=()=>reject(new Error("host follow-up failed"));});}};
 vm.createContext(context);vm.runInContext(source.slice(start,end),context);
 const q={kind:"CODEX_REQUEST",runId:"run",runtimeRequestId:"request",runtimeEpoch:"epoch",projection:{lifecycleState:"WAITING_FOR_USER"}};
 const value={activity:{runtime:{requests:[q]}}};
 context.maybeWakeRuntimeRequest(value);
 await new Promise(resolve=>setImmediate(resolve));assert.equal(messages,1);
 q.projection={lifecycleState:"RESPONDING",responseDisposition:"UNKNOWN",resendAllowed:false,availableActions:["CONTINUE_WAITING","CANCEL_OPERATION","STOP_AND_REVISE"]};q.userDecision={id:"immutable",rawText:"Exact answer"};
 const before=JSON.stringify(q);finish();await new Promise(resolve=>setImmediate(resolve));
 context.maybeWakeRuntimeRequest(value);await new Promise(resolve=>setImmediate(resolve));
 assert.equal(JSON.stringify(q),before);assert.equal(claims,1);assert.equal(messages,1);
});

test("Card renders recovery and old UNKNOWN as independent Core facts without write controls",()=>{
 const vm=require("node:vm");
 const start=source.indexOf("  function renderReadOnlyFacts("),end=source.indexOf("  function taskDuration(",start);
 const element=(tag,cls,text="")=>({tag,cls,text,children:[],append(...nodes){this.children.push(...nodes);}});
 const all=n=>[n,...n.children.flatMap(all)];
 for(const state of ["REQUIRED","RUNNING","ASSESSED","RESOLVED","FAILED","UNKNOWN"]){
  const recovery={state,displayTitle:`Core ${state}`,displayMessage:"旧回答是否被 Codex 消费仍无法确认；当前工程状态已通过新的 Recovery Turn 独立核验。",reportSummary:"<script>inert text</script>",availableActions:["READ_RECOVERY_STATUS"]};
  const p={taskView:{phase:"IMPLEMENTATION_READY",actionsStatus:"AVAILABLE",activity:{recoveryStatus:recovery}}},before=JSON.stringify(p);
  const context={el:element,PHASE_LABELS:{},taskNextStep:()=>null,executionDisplay:()=>({label:"工程事实",terminal:false}),currentPendingInteractions:()=>[],projectionPendingItem:()=>null,recoveryState:()=>null};
  vm.createContext(context);vm.runInContext(source.match(/function taskPhaseLabel\(v\)\{[\s\S]*?\n  \}/)[0],context);vm.runInContext(source.slice(start,end),context);
  const first=element("main"),second=element("main");context.renderReadOnlyFacts(first,p);context.renderReadOnlyFacts(second,p);
  const texts=all(first).map(n=>n.text).join("\n");
  assert.ok(texts.includes(`Core ${state}`));assert.match(texts,/无法确认/);assert.match(texts,/独立核验/);assert.match(texts,/<script>inert text<\/script>/);
  assert.doesNotMatch(texts,/回答已确认收到/);assert.equal(all(first).some(n=>["button","input","script"].includes(n.tag)),false);
  assert.equal(JSON.stringify(first),JSON.stringify(second));assert.equal(JSON.stringify(p),before);
 }
});


test("persistent panel keeps runtime questions inside itself without a duplicate chat wakeup",()=>{
 const vm=require("node:vm"),start=source.indexOf("  function maybeWakeRuntimeRequest("),end=source.indexOf("  // Bounded, component-local diagnostics",start);let calls=0;
 const context={singlePanel:true,call:()=>{calls++;},request:()=>{calls++;}};vm.createContext(context);vm.runInContext(source.slice(start,end),context);
 context.maybeWakeRuntimeRequest({activity:{runtime:{requests:[{kind:"CODEX_REQUEST",projection:{lifecycleState:"WAITING_FOR_USER"}}]}}});assert.equal(calls,0);
});
