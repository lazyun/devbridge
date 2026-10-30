"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),crypto=require("node:crypto");
const {createCardInstance,renderedText,renderedButtons}=require("./confirmation_card_instance_mount_test.js");
const fixture=JSON.parse(fs.readFileSync(`${__dirname}/testdata/confirmation_retired_continuation_v5.json`,"utf8"));
const clone=value=>JSON.parse(JSON.stringify(value));

function card(){
 const calls=[];
 const instance=createCardInstance("retired-no-effect",request=>{
  if(request.method==="ui/initialize")return {result:{protocolVersion:"2026-01-26",serverInfo:{name:"retired-receipt-host",version:"1"}}};
  if(request.method!=="tools/call")return {result:{}};
  const {name,arguments:input}=request.params;
  calls.push({name,input});
  assert.equal(name,"confirmation_get","the rejected Card can only read the original receipt");
  const original=fixture.read.intent;
  for(const key of ["aiSessionId","intentId","version","purpose","contentHash"])assert.equal(input[key],original[key]);
  if(input.requestId)assert.equal(input.requestId,original.commit.requestId);
  return {result:{structuredContent:clone(fixture.read)}};
 });
 return {instance,calls};
}

test("retired no-effect fixture is the actual current Card wire capture",()=>{
 const html=fs.readFileSync(process.env.DEVBRIDGE_CONFIRMATION_UI_HTML||`${__dirname}/confirmation_ui.html`,"utf8");
 const main=html.match(/<script id="confirmation-main">([\s\S]*?)<\/script>/)[1];
 assert.equal(crypto.createHash("sha256").update(main).digest("hex"),fixture._meta.mainScriptSHA256);
 assert.match(fixture._meta.singleSourceOfTruth,/Never hand-edit/);
 assert.equal(fixture.read.intent.submission.businessOutcome,"REJECTED_NO_EFFECT");
});

test("retired consumed no-effect receipt mounts as rejection and never reopens commit",async()=>{
 const {instance,calls}=card(),original=fixture.entry.intent;
 await instance.settle();
 await instance.pushToolInput({aiSessionId:original.aiSessionId,intentId:original.intentId,version:original.version,purpose:original.purpose,action:original.action});
 await instance.pushToolResult(fixture.entry);
 assert.equal(instance.peek("intent.state"),"REJECTED");
 assert.equal(instance.peek("renderFailed"),false);
 assert.equal(instance.peek("confirmedOutcome"),"CONSUMED");
 assert.equal(instance.peek("submissionState()"),"REJECTED");
 assert.equal(instance.peek("unknown"),false);
 assert.equal(instance.peek("editableRetry"),false);
 assert.equal(instance.peek("pendingCommit.requestId"),original.commit.requestId);
 assert.equal(instance.peek("pendingHash"),original.submission.payloadSHA256);
 assert.equal(instance.peek("intent.canResume"),false);
 assert.equal(instance.peek("submissionNext"),"READ_CURRENT_TARGET");
 assert.ok(renderedText(instance.peek("root")).some(text=>text.includes("拒绝")));
 assert.ok(!renderedButtons(instance.peek("root")).some(button=>button.label==="确认并开始"||button.label==="继续原步骤"));
 await instance.peek("commit()");
 await instance.settle();
 assert.ok(calls.every(call=>call.name==="confirmation_get"));
});

test("no-effect business disposition requires the complete non-resumable proof",async()=>{
 const {instance}=card();await instance.settle();
 const changes={
  resumable:v=>{v.canResume=true;}, unknown:v=>{v.state="UNKNOWN";},
  wrongAction:v=>{v.action="RETRY_IMPLEMENTATION";},
  missingProof:v=>{v.steps[0].effect="UNKNOWN";},
  ordinaryFailure:v=>{v.steps[0].failure.stage="BUSINESS_STEP";},
  wrongFailure:v=>{v.steps[0].failure.code="TASK_RUNTIME_SELECTION_BINDING_MISSING";},
  started:v=>{v.steps[1].state="COMPLETED";},
  output:v=>{v.steps[0].executionId="unproven-effect";},
  reprepare:v=>{v.submission.reprepareAllowed=true;},
  retry:v=>{v.submission.retryDisposition="EDIT_SELECTION_AND_RESUBMIT";},
  missingConsumption:v=>{v.submission.consumed=false;},
  otherReceipt:v=>{v.submission.receiptRequestId="other";},
 };
 for(const [name,alter]of Object.entries(changes)){
  const value=clone(fixture.read.intent);alter(value);
  assert.throws(()=>instance.peek(`validateSubmission(${JSON.stringify(value)})`),undefined,name);
 }
});

test("formal preparation-ready effect is readable without claiming a Run dispatch",async()=>{
 const {instance}=card();await instance.settle();
 const value=clone(fixture.read.intent);delete value.submission;
 value.state="COMPLETED";
 value.steps=[{kind:"APPROVE",requestId:"original-formal-step",state:"COMPLETED",effect:"PREPARATION_READY"}];
 assert.equal(instance.peek(`validateIntent(${JSON.stringify(value)}).steps[0].effect`),"PREPARATION_READY");
 assert.equal(instance.peek(`stepOutcomeLabel(${JSON.stringify(value.steps[0])})`),"实施准备已完成；实际运行状态请查看任务进度");
});
