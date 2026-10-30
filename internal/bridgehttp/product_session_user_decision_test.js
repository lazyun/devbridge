"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const {webcrypto} = require("node:crypto");

function workbenchAPI() {
  const source = fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`, "utf8");
  const context = {window: {DevBridgeComponents:{esc:value=>String(value)}, DevBridgeStatus:{}, DevBridgeLogFormat:{}}, crypto: webcrypto, TextEncoder, Uint8Array, console};
  context.window.crypto = webcrypto;
  vm.createContext(context);
  vm.runInContext(source, context);
  return {source, api: context.window.DevBridgeSessionWorkbench};
}

function input() {
  return {operationKind:"EXECUTION",operationId:"execution-a",taskDefinitionRevisionId:"revision-a",agentConversationId:"conversation-a",runtimeEpoch:"epoch-a",projectId:"project-a",featureAreaId:"area-a",taskId:"task-a",runId:"run-a",threadId:"thread-a",turnId:"turn-a",connectionId:"connection-a",runtimeRequestId:"request-a",submissionRequestId:"submission-a",expectedState:"WAITING_FOR_USER",action:"RESPOND",answers:[{questionId:"q1",selectedOptionId:"one"}],sourceSurface:"PRODUCT_SESSION_WORKBENCH"};
}

test("session decision hash is deterministic, covers answers, and matches the browser-safe Go escaping shape", async () => {
  const {api} = workbenchAPI();
  const first = input(), same = input();
  const hash = await api.decisionPayloadSHA256(first);
  assert.match(hash, /^[0-9a-f]{64}$/);
  assert.equal(await api.decisionPayloadSHA256(same), hash);
  same.answers[0].selectedOptionId = "two";
  assert.notEqual(await api.decisionPayloadSHA256(same), hash);
  first.rawText = "<unsafe>&\u2028";
  assert.notEqual(await api.decisionPayloadSHA256(first), hash);
	const omitted = input();
	omitted.answers = [{questionId:"q1", selectedOptionId:"one", rawText:""}];
	assert.equal(await api.decisionPayloadSHA256(omitted), hash, "empty optional answer fields are omitted like Go json.Marshal");
	const bound = input();
	bound.appServerInstanceId = "instance-a";
	bound.appServerInstanceEpoch = 1789621196732112;
	const boundHash = await api.decisionPayloadSHA256(bound);
	assert.notEqual(boundHash, hash);
	bound.appServerInstanceEpoch++;
	assert.notEqual(await api.decisionPayloadSHA256(bound), boundHash, "instance epoch participates in the immutable answer hash");
});

test("workbench submits only canonical user decisions and leaves historical requests read-only", () => {
  const {source} = workbenchAPI();
  assert.match(source, /agent-user-decisions/);
  assert.doesNotMatch(source, /data-session-answer/);
  const semantic=source.split('else if (button.hasAttribute("data-session-decision"))')[1].split('else if (button.hasAttribute("data-session-approval"))')[0];
  assert.doesNotMatch(semantic, /agent-requests\/resolve/);
  assert.match(semantic, /agent-user-decisions/);
  assert.match(source, /projection\.lifecycleState==="WAITING_FOR_USER"/);
  assert.match(source, /projection\.responseDisposition==="UNKNOWN"/);
  assert.match(source, /runtimeRequestId/);
  assert.match(source, /原始请求与选项/);
  assert.match(source, /页面不会代答或重试/);
	assert.match(source, /data-decision-elicitation-content/);
	assert.match(source, /selectedOptionId:answer\?\.selectedOptionId\|\|undefined/);
});

test("workbench dynamically renders Core UNKNOWN without answer controls for console and embedded Desktop", () => {
  const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,"utf8");
  const start=source.indexOf("    function requestKey("),end=source.indexOf("    function cancellation()",start);
  const q={...input(),kind:"CODEX_REQUEST",method:"item/tool/requestUserInput",state:"WAITING_FOR_USER",availableActions:["RESPOND"],payload:{questions:[{id:"q",question:"Original exact question?",options:[{id:"yes",label:"yes"}]}]},projection:{lifecycleState:"RESPONDING",responseDisposition:"UNKNOWN",displayTitle:"回答状态无法确认",displayMessage:"你的回答已经保存。Codex 可能已经收到，但当前协议无法确认。系统不会自动重发。",userDecisionRecorded:true,userDecisionSummary:"Exact original answer",resendAllowed:false,availableActions:["CONTINUE_WAITING","CANCEL_OPERATION","STOP_AND_REVISE"]}};
  const before=JSON.stringify(q);
  for(const layout of ["console","embedded"]){
    const context={layout,snapshot:null,runID:q.runId,project:q.projectId,esc:v=>String(v??""),pre:v=>`<pre>${v}</pre>`,list:v=>Array.isArray(v)?v:[],runtime:()=>({requests:[q],connectionId:q.connectionId,threadId:q.threadId,turnId:q.turnId}),detail:()=>({})};
    vm.createContext(context);vm.runInContext(source.slice(start,end),context);
    const html=context.requests();
    assert.match(html,/回答状态无法确认/);assert.match(html,/Exact original answer/);assert.match(html,/Original exact question/);assert.match(html,/系统不会自动重发/);
    assert.equal((html.match(/data-session-recovery=/g)||[]).length,3);
    for(const action of ["CONTINUE_WAITING","CANCEL_OPERATION","STOP_AND_REVISE"])assert.ok(html.includes(`data-decision-action="${action}"`));
    assert.doesNotMatch(html,/data-session-decision=|data-decision-question=|<select|<input|<textarea|data-decision-action="RESPOND"/);
    assert.equal(context.requests(),html,"refresh is a pure rendering read");
  }
  assert.equal(JSON.stringify(q),before,"render did not infer or mutate disposition from stale legacy state");
});

test("recovery state rendering preserves Core facts and never dispatches or answers during refresh", () => {
  const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,"utf8");
  const start=source.indexOf("    function requestKey("),end=source.indexOf("    function cancellation()",start);
  for(const layout of ["console","embedded"]){
    for(const state of ["REQUIRED","RUNNING","ASSESSED","RESOLVED","FAILED","UNKNOWN"]){
      const projection={state,displayTitle:`Core ${state}`,displayMessage:"旧回答是否被 Codex 消费仍无法确认；当前工程状态已通过新的 Recovery Turn 独立核验。",reportSummary:"<script>unsafe()</script>",availableActions:["READ_RECOVERY_STATUS"]};
      const before=JSON.stringify(projection);
      const context={layout,snapshot:{recoveryStatus:projection},esc:v=>String(v??"").replace(/&/g,"&amp;").replace(/</g,"&lt;").replace(/>/g,"&gt;"),list:v=>Array.isArray(v)?v:[],runtime:()=>({requests:[]}),detail:()=>({})};
      vm.createContext(context);vm.runInContext(source.slice(start,end),context);
      const html=context.requests();
      assert.ok(html.includes(`Core ${state}`));
      assert.match(html,/无法确认/);assert.match(html,/独立核验/);assert.match(html,/&lt;script&gt;/);
      assert.doesNotMatch(html,/<script|回答已确认收到|<button|<input|<textarea/);
      assert.equal(context.requests(),html);
      assert.equal(JSON.stringify(projection),before);
    }
  }
});
