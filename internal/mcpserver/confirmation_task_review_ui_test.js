"use strict";
// Choice/rendering and exact-request tests over the ENTIRE shipped Card script.
// This Host deliberately supplies synthetic TASK facts (not production receipts).
// The real SQLite/canonical command tests are in confirmation_task_review_test.go.
const test=require("node:test"),assert=require("node:assert/strict");
const {createCardInstance,renderedText,fixture}=require("./confirmation_card_instance_mount_test.js");
const clone=value=>JSON.parse(JSON.stringify(value));

function pendingTask(prepared=true){
 const action=prepared?"CREATE_TASK_AND_REVIEW":"CREATE_TASK";
 return {intent:{aiSessionId:"task-review-session",intentId:"task-review-intent",version:1,purpose:"TASK",projectId:"project",action,contentHash:"a".repeat(64),state:"PENDING",canResume:false,steps:[],
  createdAt:fixture.replacement.entry.intent.createdAt,expiresAt:fixture.replacement.entry.intent.expiresAt,
  content:{aiSessionId:"task-review-session",requestId:"prepare",purpose:"TASK",action,
   task:{featureAreaId:"area",repositoryId:"repo",title:"可选择审查的任务",problemStatement:"Inspect the repository",constraints:[],startReview:prepared,reviewPlan:prepared?{mode:"REPOSITORY"}:{},...(prepared?{reviewDepth:"DEEP"}:{})},
   variants:prepared?[{id:"prepared",label:"Codex review",summary:"Inspect read-only",executorKind:"CODING_AGENT",reviewInstruction:"Inspect read-only and report findings."},{id:"ai",label:"AI review",summary:"Read files and submit findings",executorKind:"UPPER_LLM",reviewInstruction:"Read files and submit a review report."}]:[{id:"prepared",label:"Create task",summary:"Create task"}]}}};
}
function taskOptions(intent,startReview,noRuntime){
 const blocked=startReview&&noRuntime;
 return {options:{aiSessionId:intent.aiSessionId,intentId:intent.intentId,version:1,purpose:"TASK",projectId:"",repositoryId:"",startReview,
  projects:[],repositories:[],branches:[],variants:intent.content.variants.map(v=>({id:v.id,label:v.label,available:!(blocked&&v.executorKind==="CODING_AGENT"),reason:blocked&&v.executorKind==="CODING_AGENT"?"CODEX_RUNTIME_INVENTORY_UNAVAILABLE":""})),
  runtimeInventorySource:noRuntime?"UNAVAILABLE":"CORE_CODEX_RUNTIME_AUTHORITY",runtimeInventoryRevision:noRuntime?0:1,
  ...(noRuntime?{runtimeInventoryReadError:"CODEX_RUNTIME_INVENTORY_UNAVAILABLE"}:{}),
  agentConfigFingerprints:noRuntime?{}:{pi:"a".repeat(64)},backendContinuity:{state:"THREADLESS"},
  runtimeOptions:noRuntime?[]:[{modelId:"test-model",enabled:true,supportedReasoningEfforts:["high"],codexFastModeSupported:true}],
  backendInventoryComplete:true,backendCandidates:noRuntime?[]:[{configId:"backend",configFingerprint:"b".repeat(64),configName:"Test backend",source:"SESSION_INVENTORY"}],
  eligibility:{available:intent.content.variants.some(v=>!(blocked&&v.executorKind==="CODING_AGENT")),reprepareAllowed:false,attemptState:"NOT_ATTEMPTED",receiptState:"NOT_OBSERVED"}}};
}
function startTask(scenario={}){
 const envelope=pendingTask(scenario.prepared!==false),frozen=envelope.intent;
 if(scenario.noRepository)frozen.content.task.repositoryId="";
 const host={commits:[],reads:[],options:[],messages:[],receipt:null};let instance;
 const answer=async message=>{
  if(message.method==="ui/initialize")return {result:{protocolVersion:"2026-01-26",serverInfo:{name:"task-choice-test",version:"1"}}};
  if(message.method==="ui/message"){host.messages.push(clone(message.params));if(scenario.messageFailure)return {error:{code:-32601,message:"Host message unavailable"}};return {result:{}};}
  if(message.method!=="tools/call")return {result:{}};
  const {name,arguments:request}=message.params;
  for(const key of ["aiSessionId","intentId","version","purpose","contentHash"])assert.equal(request[key],frozen[key],`${name} ${key}`);
  if(name==="confirmation_options_get"){
   host.options.push(clone(request));assert.equal(typeof request.startReview,"boolean");
   const answer=taskOptions(frozen,scenario.wrongOptions?!request.startReview:request.startReview,scenario.noRuntime);
   if(scenario.continuity)answer.options.taskContinuity=clone(typeof scenario.continuity==="function"?scenario.continuity(host.options.length):scenario.continuity);
   return {result:{structuredContent:answer}};
  }
  if(name==="confirmation_get"){
   host.reads.push(clone(request));assert.equal(Object.hasOwn(request,"startReview"),false,"a receipt read cannot change the Review choice");
   const content=clone(host.receipt||envelope);
   return {result:{structuredContent:content,isError:!!content.failure}};
  }
  assert.equal(name,"confirmation_commit");host.commits.push(clone(request));
  if(scenario.transportFailure)return {error:{code:-32603,message:"lost response"}};
  const receipt=clone(envelope),value=receipt.intent,payloadSHA256=await instance.peek(`requestHash(${JSON.stringify(request)})`);
  if(scenario.rejectFirst&&host.commits.length===1){
   const failure={code:"BACKEND_SELECTION_NOT_APPLICABLE",reasonCode:"BACKEND_SELECTION_NOT_APPLICABLE",source:"CORE",stage:"CONFIRMATION_PREFLIGHT",message:"Edit the selected configuration.",nextAction:"EDIT_SELECTION"};
   value.submission={request:clone(request),payloadSHA256,outcome:"REJECTED_NOT_CONSUMED",recorded:true,failure,nextAction:"EDIT_SELECTION",receiptRequestId:"",businessOutcome:"REJECTED_NOT_CONSUMED",consumptionDisposition:"NOT_CONSUMED",consumed:false,redispatchSameIntent:false,reprepareAllowed:false,retryDisposition:"EDIT_SELECTION_AND_RESUBMIT"};
   receipt.failure=failure;
  }else{
   value.state="COMPLETED";value.commit=clone(request);
   value.steps=[{kind:"CREATE_TASK",requestId:request.requestId+"/task",state:"COMPLETED",taskId:"created-task"}];
   if(request.selection.startReview)value.steps.push({kind:"START_REVIEW",requestId:request.requestId+"/review",state:"COMPLETED",taskId:"created-task",reviewId:"review",runId:"run"});
   value.submission={request:clone(request),payloadSHA256,outcome:"CONSUMED",recorded:true,nextAction:"READ_ORIGINAL_RECEIPT",receiptRequestId:request.requestId,businessOutcome:"COMMITTED",consumptionDisposition:"CONSUMED",consumed:true,redispatchSameIntent:false,reprepareAllowed:false,retryDisposition:"NONE"};
  }
  host.receipt=clone(receipt);return {result:{structuredContent:receipt,isError:!!receipt.failure}};
 };
 instance=createCardInstance("task-review-choice",answer);
 return {instance,host,envelope};
}
async function bind(card){
 await card.instance.settle();
 const value=card.envelope.intent;
 await card.instance.pushToolInput({aiSessionId:value.aiSessionId,intentId:value.intentId,version:1,purpose:"TASK",action:value.action,requestId:"show"});
 await card.instance.pushToolResult(card.envelope);
 assert.equal(card.instance.peek("renderFailed"),false);
 return card;
}
function findNode(card,predicate){
 const stack=[card.instance.peek("root")];
 while(stack.length){const node=stack.pop();if(predicate(node))return node;stack.push(...(node.childNodes||[]));}
 return null;
}
const reviewSelect=card=>findNode(card,node=>node.tagName==="SELECT"&&node.dataset?.reviewMode==="true");
const button=(card,label)=>findNode(card,node=>node.tagName==="BUTTON"&&renderedText(node).join(" ").trim()===label);
async function drive(card,invoke){
 let pumping=true;
 const pump=(async()=>{while(pumping){await card.instance.settle();await new Promise(resolve=>setImmediate(resolve));}})();
 try{await invoke();}finally{pumping=false;await pump;}
 assert.equal(card.instance.peek("renderFailed"),false);
}
async function selectMode(card,value){const node=reviewSelect(card);assert.ok(node);assert.equal(node.disabled,false);node.value=value;await drive(card,()=>node.onchange());}
async function submit(card,label){const node=button(card,label);assert.ok(node,`missing ${label}`);assert.equal(node.disabled,false,card.instance.peek("JSON.stringify({choiceError:choiceError(),flight,unknown,stale,error,editableRetry,lastFailure,options,draft})"));await drive(card,()=>node.onclick());}
function chooseRuntime(card){card.instance.peek(`draft.model='test-model';draft.reasoningEffort='high';draft.codexFastMode=true;draft.backendConfigId='backend';draft.backendConfigFingerprint='${"b".repeat(64)}';safeRender()`);}


test("Task card requires the user's choice among three review methods",async()=>{
 const card=await bind(startTask({noRuntime:true})),before=JSON.stringify(card.envelope.intent.content);
 const control=reviewSelect(card);assert.equal(control.value,"");
 assert.equal(control.getAttribute("aria-describedby"),"task-review-help");
 assert.deepEqual(control.childNodes.map(n=>n.value),["","UPPER_LLM","CODING_AGENT","NONE"]);
 assert.match(card.instance.peek("choiceError()"),/用户选择审查方式/);
 await selectMode(card,"NONE");
 assert.equal(card.host.options.at(-1).startReview,false);
 assert.equal(card.instance.peek("choiceError()"),"");
 assert.equal(card.instance.peek("runtimeNeeded()"),false);
 assert.ok(renderedText(card.instance.peek("root")).some(text=>text.includes("实施方案确认")));
 await submit(card,"创建任务，进入实施确认");
 assert.deepEqual(card.host.commits[0].selection,{startReview:false,reviewMode:"NONE"});
 assert.equal(card.host.commits[0].action,"CREATE_TASK_AND_REVIEW");
 assert.equal(JSON.stringify(card.instance.peek("intent.content")),before);
 assert.equal(card.instance.peek("confirmedOutcome"),"CONSUMED");
 assert.equal(card.instance.peek("intent.steps.length"),1);
 assert.equal(reviewSelect(card),null);
});

test("Codex review uses the selected prepared instruction and explicit runtime",async()=>{
 const card=await bind(startTask());await selectMode(card,"CODING_AGENT");chooseRuntime(card);
 await selectMode(card,"NONE");
 assert.deepEqual(clone(card.instance.peek("selection()")),{startReview:false,reviewMode:"NONE"});
 await selectMode(card,"CODING_AGENT");
 await submit(card,"创建任务并开始审查");
 assert.equal(card.host.commits[0].variantId,"prepared");
 assert.deepEqual(card.host.commits[0].selection,{startReview:true,reviewMode:"CODING_AGENT",runtimeConfig:{model:"test-model",reasoningEffort:"high",codexFastMode:true},backend:{configId:"backend",configFingerprint:"b".repeat(64)}});
 assert.equal(card.instance.peek("intent.steps.length"),2);
});

test("AI file review needs no Codex backend and explains who continues the work",async()=>{
 const card=await bind(startTask({noRuntime:true}));
 await selectMode(card,"UPPER_LLM");
 assert.equal(card.instance.peek("choiceError()"),"");
 assert.ok(renderedText(card.instance.peek("root")).some(text=>text.includes("发起对话中的 AI")));
 await submit(card,"创建任务并开始审查");
 assert.equal(card.host.commits[0].variantId,"ai");
 assert.deepEqual(card.host.commits[0].selection,{startReview:true,reviewMode:"UPPER_LLM"});
});

test("Unprepared review methods are visibly unavailable and cannot be manufactured",async()=>{
 const card=await bind(startTask({prepared:false,noRuntime:true}));
 const control=reviewSelect(card);
 assert.equal(control.childNodes.find(n=>n.value==="UPPER_LLM").disabled,true);
 assert.equal(control.childNodes.find(n=>n.value==="CODING_AGENT").disabled,true);
 await card.instance.peek("changeReviewMode('CODING_AGENT')");
 assert.equal(card.instance.peek("draft.reviewMode"),"");
 await selectMode(card,"NONE");await submit(card,"创建任务，进入实施确认");
 assert.equal(card.host.commits[0].action,"CREATE_TASK");
});

test("Review options must prove the exact current startup choice",async()=>{
 const card=await bind(startTask({wrongOptions:true}));
 assert.equal(card.instance.peek("options"),null);
 await selectMode(card,"NONE");
 assert.equal(card.instance.peek("options"),null);
 assert.notEqual(card.instance.peek("choiceError()"),"");
 assert.equal(card.host.commits.length,0);
});

test("Review mode cannot change while in flight, stale, unknown, fatal or expired",async()=>{
 for(const flag of ["flight=true","stale=true","unknown=true","fatal=true","intent.expiresAt=new Date(Date.now()-1).toISOString()"]){
  const card=await bind(startTask()),control=reviewSelect(card),reads=card.host.options.length;
  card.instance.peek(`${flag};safeRender()`);
  control.value="NONE";await control.onchange();
  assert.equal(card.instance.peek("draft.reviewMode"),"",flag);
  assert.equal(card.host.options.length,reads,flag);
 }
});

test("Response loss retains the exact review mode and only reads its original receipt",async()=>{
 const card=await bind(startTask({transportFailure:true}));
 await selectMode(card,"NONE");await submit(card,"创建任务，进入实施确认");
 assert.equal(card.instance.peek("unknown"),true);
 const original=card.host.commits[0],options=card.host.options.length;
 assert.deepEqual(clone(card.instance.peek("pendingCommit")),original);
 await card.instance.peek("changeReviewMode('UPPER_LLM')");
 assert.equal(card.instance.peek("draft.reviewMode"),"NONE");
 await drive(card,()=>card.instance.peek("readbackOriginal()"));
 assert.equal(card.host.options.length,options);
 assert.equal(card.host.commits.length,1);
 assert.equal(card.host.reads.at(-1).requestId,original.requestId);
 assert.equal(card.instance.peek(`sameCommit(pendingCommit,${JSON.stringify({...original,selection:{startReview:false}})})`),false);
});

test("Legacy held requests replay without adding a new reviewMode field",async()=>{
 for(const selected of [undefined,false]){
  const card=startTask(),value=card.envelope.intent;
  const request={aiSessionId:value.aiSessionId,intentId:value.intentId,version:1,purpose:"TASK",action:value.action,contentHash:value.contentHash,requestId:"held",variantId:"prepared",selection:selected===undefined?{}:{startReview:selected}};
  const draft=clone(card.instance.peek("draft"));delete draft.startReview;delete draft.reviewMode;draft.model="saved-model";
  const snapshot={componentVersion:"PURPOSE_CONFIRMATION_V4",identity:{aiSessionId:value.aiSessionId,intentId:value.intentId,version:1,purpose:"TASK",projectId:value.projectId,contentHash:value.contentHash},pendingRequest:request,pendingRequestId:request.requestId,payloadSHA256:await card.instance.peek(`requestHash(${JSON.stringify(request)})`),outcomeUnknown:true,draft};
  card.instance.peek(`window.openai.widgetState=${JSON.stringify(snapshot)}`);
  await bind(card);
  assert.equal(card.instance.peek("unknown"),true);
  assert.equal(card.instance.peek("draft.model"),"saved-model");
  assert.equal(card.instance.peek("taskReviewSelected()"),selected===undefined);
  assert.deepEqual(clone(card.instance.peek("originalSubmission(pendingCommit)")),request);
 }
});

test("Only a proven not-consumed receipt permits editing the review mode",async()=>{
 const card=await bind(startTask({rejectFirst:true}));
 await selectMode(card,"NONE");await submit(card,"创建任务，进入实施确认");
 assert.equal(card.instance.peek("editableRetry"),true);
 const refused=card.host.commits[0];
 await selectMode(card,"UPPER_LLM");
 await drive(card,()=>card.instance.peek("refresh()"));
 assert.equal(card.instance.peek("draft.reviewMode"),"UPPER_LLM");
 await submit(card,"创建任务并开始审查");
 assert.equal(card.host.commits.length,2);
 assert.notEqual(card.host.commits[1].requestId,refused.requestId);
 assert.equal(card.host.commits[1].selection.reviewMode,"UPPER_LLM");
 assert.equal(card.instance.peek("confirmedOutcome"),"CONSUMED");
});

test("A task may skip review before selecting its repository for the implementation card",async()=>{
 const card=await bind(startTask({prepared:false,noRuntime:true,noRepository:true}));
 await selectMode(card,"NONE");assert.equal(card.instance.peek("choiceError()"),"");
 await submit(card,"创建任务，进入实施确认");
 assert.deepEqual(card.host.commits[0].selection,{startReview:false,reviewMode:"NONE"});
});

function continuityFact(overrides={}){return {availability:"AVAILABLE",projectId:"project",featureAreaId:"area",total:1,tasks:[{taskId:"original",title:"原任务",status:"ACTIVE",phase:"RECOVERY_REQUIRED",stage:"待找回验证结果",summary:"尚未收到这次验证的最终结果。",taskDefinitionRevisionId:"definition-2"}],...overrides};}
const textOf=card=>renderedText(card.instance.peek("root")).join("\n");
function chooseCreation(card,value){const node=findNode(card,node=>node.tagName==="SELECT"&&node.dataset?.taskCreationChoice==="true");assert.ok(node);assert.equal(node.disabled,false);node.value=value;node.onchange();}

test("unfinished Task requires an explicit new-task choice, preserves original and frozen creation identity",async()=>{
 const card=await bind(startTask({prepared:false,continuity:continuityFact()}));
 assert.match(textOf(card),/未完成.*待找回验证结果/);
 assert.match(textOf(card),/创建独立新任务不会自动完成或关闭原任务/);
 assert.equal(button(card,"创建任务"),null);
 await card.instance.peek("commit()");assert.equal(card.host.commits.length,0);
 chooseCreation(card,"NEW");assert.match(textOf(card),/原任务仍未完成，不会自动关闭/);
 await selectMode(card,"NONE");
 const before=JSON.stringify(card.envelope.intent.content);
 await submit(card,"创建任务，进入实施确认");
 assert.equal(card.host.commits.length,1);assert.equal(card.host.messages.length,0);
 assert.deepEqual(Object.keys(card.host.commits[0].selection).sort(),["reviewMode","startReview"]);
 assert.equal(JSON.stringify(card.envelope.intent.content),before);
});

test("continue original Task sends one exact read-only conversation request and never consumes creation",async()=>{
 const card=await bind(startTask({prepared:false,continuity:continuityFact()}));
 chooseCreation(card,"TASK:original");assert.match(textOf(card),/续接任务/);
 assert.equal(card.instance.peek("objectName()"),"原任务");
 await submit(card,"继续原任务");
 assert.equal(card.host.commits.length,0);assert.equal(card.host.messages.length,1);
 const prompt=card.host.messages[0].content[0].text;
 assert.match(prompt,/"taskId":"original"/);assert.match(prompt,/"taskDefinitionRevisionId":"definition-2"/);
 assert.match(prompt,/先只读/);assert.match(prompt,/不授权关闭任务、提交分析、批准、恢复、重试或启动执行/);
 assert.equal(card.instance.peek("intent.state"),"PENDING");assert.equal(card.instance.peek("pendingCommit"),null);
 assert.equal(button(card,"继续原任务").disabled,true);
 await drive(card,()=>card.instance.peek("continueExistingTask()"));assert.equal(card.host.messages.length,1);
});

test("cancel creation choice is local and does not cancel original Task or creation intent",async()=>{
 const card=await bind(startTask({continuity:continuityFact()}));chooseCreation(card,"CANCEL");
 assert.match(textOf(card),/未创建或关闭任何任务/);assert.equal(button(card,"创建任务"),null);
 await card.instance.peek("commit()");assert.equal(card.host.commits.length,0);assert.equal(card.host.messages.length,0);
 assert.equal(card.instance.peek("intent.state"),"PENDING");
});

test("preflight Task drift requires fresh acknowledgement before creating a new Task",async()=>{
 let changed=false;const fact=continuityFact();
 const card=await bind(startTask({prepared:false,continuity:()=>{const value=clone(fact);if(changed)value.tasks[0].phase="VERIFYING";return value;}}));
 chooseCreation(card,"NEW");await selectMode(card,"NONE");changed=true;
 await submit(card,"创建任务，进入实施确认");
 assert.equal(card.host.commits.length,0);assert.match(textOf(card),/原任务状态已变化/);
 chooseCreation(card,"NEW");await submit(card,"创建任务，进入实施确认");assert.equal(card.host.commits.length,1);
});

test("continuation read revalidates Task membership before messaging and rejects cross-area inventory",async()=>{
 let gone=false;const card=await bind(startTask({prepared:false,continuity:()=>continuityFact(gone?{tasks:[],total:0}:{})}));
 chooseCreation(card,"TASK:original");gone=true;await submit(card,"继续原任务");
 assert.equal(card.host.messages.length,0);assert.equal(card.host.commits.length,0);
 const foreign=await bind(startTask({continuity:continuityFact({featureAreaId:"another-area"})}));
 assert.match(foreign.instance.peek("error"),/TASK_CONTINUITY_INVALID|OPTIONS_READ_FAILED/);
 assert.equal(foreign.host.commits.length,0);
});

test("unknown inventory is shown explicitly; empty inventory does not add a continuation choice",async()=>{
 const unknown=await bind(startTask({prepared:false,continuity:continuityFact({availability:"UNAVAILABLE",total:0,tasks:[],readFailureCode:"TASK_INVENTORY_UNAVAILABLE"})}));
 assert.match(textOf(unknown),/未能核对已有任务/);assert.equal(button(unknown,"创建任务"),null);
 chooseCreation(unknown,"NEW");await selectMode(unknown,"NONE");await submit(unknown,"创建任务，进入实施确认");assert.equal(unknown.host.commits.length,1);
 const empty=await bind(startTask({prepared:false,continuity:continuityFact({total:0,tasks:[]})}));
 assert.equal(empty.instance.peek("taskCreationNeedsChoice()"),false);await selectMode(empty,"NONE");assert.ok(button(empty,"创建任务，进入实施确认"));
});


test("unsupported conversation messaging offers the exact prompt without automatically resending",async()=>{
 const card=await bind(startTask({prepared:false,continuity:continuityFact(),messageFailure:true}));
 chooseCreation(card,"TASK:original");await submit(card,"继续原任务");
 assert.equal(card.host.messages.length,1);assert.equal(card.host.commits.length,0);
 assert.match(textOf(card),/未能确认消息是否发送/);
 const field=findNode(card,node=>node.tagName==="TEXTAREA"&&node.getAttribute("aria-label")==="原任务续接指令");assert.ok(field);
 assert.equal(field.value,card.host.messages[0].content[0].text);assert.equal(field.readOnly,true);
 assert.equal(button(card,"继续原任务").disabled,true);
});


test("continuing a reviewing Task requests actual GPT file review and formal submission",async()=>{
 const fact=continuityFact();fact.tasks[0].phase='REVIEWING';
 const card=await bind(startTask({prepared:false,continuity:fact}));chooseCreation(card,'TASK:original');
 assert.ok(button(card,'继续当前审查'));assert.match(textOf(card),/正式提交审查结果/);
 await submit(card,'继续当前审查');const prompt=card.host.messages[0].content[0].text;
 assert.match(prompt,/"taskId":"original"/);assert.match(prompt,/UPPER_LLM/);assert.match(prompt,/review_access_get/);assert.match(prompt,/review_result_submit/);assert.match(prompt,/回读确认入库/);assert.match(prompt,/等待用户确认/);assert.match(prompt,/执行者为 Codex\/Pi/);assert.doesNotMatch(prompt,/先只读核对当前状态和可用的续接路径|不授权关闭任务、提交分析/);
 assert.equal(card.host.commits.length,0);assert.equal(card.instance.peek('intent.state'),'PENDING');assert.equal(button(card,'继续当前审查').disabled,true);
 await drive(card,()=>card.instance.peek('continueExistingTask()'));assert.equal(card.host.messages.length,1);
});
test("review continuation rechecks phase and definition before sending any review instruction",async()=>{
 for(const change of ['phase','revision']){
  let changed=false;const fact=continuityFact();fact.tasks[0].phase='REVIEWING';
  const card=await bind(startTask({prepared:false,continuity:()=>{const value=clone(fact);if(changed){if(change==='phase')value.tasks[0].phase='VERIFYING';else value.tasks[0].taskDefinitionRevisionId='definition-3';}return value;}}));
  chooseCreation(card,'TASK:original');changed=true;await submit(card,'继续当前审查');
  assert.equal(card.host.messages.length,0);assert.equal(card.host.commits.length,0);assert.match(textOf(card),/原任务状态已变化/);
 }
});

test("history shows pending proposal read-only and rejects confirmation and conversation continuation",async()=>{
 const card=startTask({prepared:false,continuity:continuityFact()});
 card.instance.peek("window.devBridgeHistoryReadOnly=true");await bind(card);
 assert.match(textOf(card),/历史操作仅供查看/);
 assert.equal(reviewSelect(card),null);assert.equal(button(card,"继续原任务"),null);assert.equal(button(card,"创建任务"),null);
 await card.instance.peek("commit()");await card.instance.peek("continueExistingTask()");await card.instance.peek("resumeConfirmation()");
 for(const name of ['confirmation_commit','ui/message'])assert.equal(await card.instance.peek(`rpc(${JSON.stringify(name)},{},true).then(()=>false,e=>true)`),true);
 assert.equal(card.host.commits.length,0);assert.equal(card.host.messages.length,0);assert.equal(card.instance.peek('intent.state'),'PENDING');
 const reads=card.host.reads.length;await drive(card,()=>card.instance.peek('refresh()'));assert.ok(card.host.reads.length>reads);
});
test("retained current-card click handlers cannot submit after switching to history",async()=>{
 const card=await bind(startTask({prepared:false,continuity:continuityFact()}));chooseCreation(card,'NEW');await selectMode(card,'NONE');
 const confirm=button(card,'创建任务，进入实施确认');assert.ok(confirm);
 card.instance.peek("window.devBridgeHistoryReadOnly=true;safeRender()");await confirm.onclick();await card.instance.settle();
 assert.equal(card.host.commits.length,0);assert.equal(card.host.messages.length,0);assert.match(textOf(card),/历史操作仅供查看/);
});

test("cooperative review visibly distinguishes trust from native isolation and freezes the explicit choice",async()=>{
 const card=await bind(startTask());await selectMode(card,"CODING_AGENT");chooseRuntime(card);
 card.instance.peek("draft.codexFastMode=false;draft.executionPolicy='COOPERATIVE_TRUSTED_V1';safeRender()");
 assert.notEqual(card.instance.peek("choiceError()"),"");
 const text=renderedText(card.instance.peek("root")).join(" ");
 assert.match(text,/不提供 TASK_SANDBOX_V1/);assert.match(text,/不等于模型已读取或遵守/);assert.match(text,/确认及派发前阻止运行/);
 card.instance.peek("draft.agentId='pi';draft.agentConfigFingerprint='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';safeRender()");
 assert.equal(card.instance.peek("choiceError()"),"");
 await submit(card,"创建任务并开始审查");
 assert.equal(card.host.commits.length,1);
 assert.equal(card.host.commits[0].selection.runtimeConfig.agentId,"pi");
 assert.equal(card.host.commits[0].selection.runtimeConfig.executionPolicy,"COOPERATIVE_TRUSTED_V1");
 assert.equal(card.instance.peek("confirmedOutcome"),"CONSUMED");
});
