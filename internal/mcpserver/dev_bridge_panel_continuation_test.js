'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
const button=(h,label)=>h.root.querySelectorAll('button').find(b=>b.textContent===label);
async function panel(options={}){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']},...options});h.context.crypto.randomUUID=()=> 'stable-request';
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'continue-panel',entryIdentity:'continue-panel',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()},singlePanel:true});h.init();await settle();
 h.reply(h.calls().find(p=>p.params.name==='dev_bridge_panel_get'),{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[]});await settle();
 h.evaluate("workspace.presentation.taskView.actionsStatus='AVAILABLE';workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';workspace.presentation.taskView.phase='REVIEWING';workspace.presentation.taskView.currentReview={id:'review-a',taskId:'task-a',executorKind:'UPPER_LLM',stage:'PREPARING',run:{id:'run-a',transportStatus:'RUNNING'}};render()");return h;
}
function requireRecovery(h){h.evaluate("workspace.presentation.taskView.currentReview.stage='AUTHORIZATION_REQUIRED';workspace.presentation.recovery={reason:'GPT_REVIEW_AUTHORIZATION_REQUIRED',summary:'原审查授权已失效'};workspace.presentation.taskView.actions=[{action:'RECOVER',available:true,target:{featureAreaId:'area-a',taskId:'task-a',operationKind:'REVIEW',operationId:'review-a',runId:'run-a',expectedUpperVersion:{grantEpoch:'old',authorityEpoch:'current',grantGeneration:1}}}];render()");}
const snapshot=h=>JSON.parse(h.evaluate('JSON.stringify(workspace)'));

function codexInterrupted(h,purpose='IMPLEMENTATION'){
 const action=purpose==='VERIFICATION'?'REQUEST_REVERIFICATION':'RETRY_IMPLEMENTATION';
 h.context.interruptedPurpose=purpose;h.context.interruptedAction=action;
 h.evaluate("const v=workspace.presentation.taskView;v.phase='RECOVERY_REQUIRED';v.businessPhase=interruptedPurpose==='VERIFICATION'?'VERIFYING':'IMPLEMENTING';v.status='ACTIVE';v.terminal=false;v.readOnly=false;v.implementationMode='CODING_AGENT';v.currentReview=null;v.nextStep=null;v.currentExecution={id:'execution-a',taskDefinitionRevisionId:'revision-a',implementationRunId:'impl-a',verificationRunId:interruptedPurpose==='VERIFICATION'?'verify-a':''};const r={id:interruptedPurpose==='VERIFICATION'?'verify-a':'impl-a',transportStatus:'INFRASTRUCTURE_FAILED',nativeResultState:'RECEIVED',failureCode:'CODEX_TURN_INTERRUPTED'};if(interruptedPurpose==='VERIFICATION')v.verificationRun=r;else v.implementationRun=r;const cap={action:interruptedAction,available:true,target:{taskId:'task-a',featureAreaId:'area-a',taskDefinitionRevisionId:'revision-a',executionId:'execution-a',subjectId:'execution-a',decisionId:'TASK_ACCEPTANCE:execution-a'}};v.actions=[cap];v.codexContinuation={runId:r.id,purpose:interruptedPurpose,state:'AVAILABLE',label:'Codex 已中断，可以继续',summary:'沿原任务继续，保留已改文件',action:cap};render()");
}

test('interrupted Codex offers an exact tracked preparation request without starting work or replaying commands',async()=>{
 for(const purpose of ['IMPLEMENTATION','VERIFICATION']){
  const h=await panel();codexInterrupted(h,purpose);
  assert.match(h.root.textContent,/Codex 已中断，可以继续/);const continueButton=button(h,'继续 Codex · 准备确认');assert.ok(continueButton);
  const from=h.calls().length,click=continueButton.onclick();await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);
  const prompt=message.params.content[0].text;assert.match(prompt,purpose==='VERIFICATION'?/REQUEST_REVERIFICATION/:/RETRY_IMPLEMENTATION/);assert.match(prompt,/保留工作区中已完成的改动/);assert.match(prompt,/不自行调用 confirmation_commit/);assert.match(prompt,purpose==='VERIFICATION'?/verify-a/:/impl-a/);
  h.hostReply(message,{});await click;assert.equal(h.calls().slice(from).some(c=>/start|commit|recover|prepare/.test(c.params.name)),false);
  assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.equal(button(h,'继续 Codex · 准备确认').disabled,true);assert.match(h.root.textContent,/等待准确业务回执/);
  assert.ok(h.evaluate('JSON.stringify(widgetState())').includes('原 Run ID'));h.dispose();
 }
});

test('stale continuation metadata cannot offer a resume for unknown, canceled, busy or foreign work',async()=>{
 for(const change of ["v.implementationRun.nativeResultState='UNKNOWN'","v.implementationRun.transportStatus='CANCELED'","v.actions[0].available=false","v.currentExecution.implementationRunId='new-run'","v.actions[0].target.taskId='other-task'","v.readOnly=true","v.terminal=true","v.actionsStatus='UNAVAILABLE'","panelFeedError='offline'"]){
  const h=await panel();codexInterrupted(h);h.context.continuationChange=change;h.evaluate('(()=>{const v=workspace.presentation.taskView;eval(continuationChange);render()})()');assert.equal(button(h,'继续 Codex · 准备确认'),undefined,change);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
 }
});

test('refresh and prepared confirmation retain one continuation request and block duplicate preparation',async()=>{
 const h=await panel();codexInterrupted(h);const old=button(h,'继续 Codex · 准备确认');
 h.evaluate("panelFeed.items.push({aiSessionId:sessionID,intentId:'resume-confirmation',version:1,contentHash:'a'.repeat(64),purpose:'EXECUTION',action:'RETRY_IMPLEMENTATION',state:'PENDING',taskId:taskID});render()");
 assert.equal(button(h,'继续 Codex · 准备确认'),undefined);assert.match(h.root.textContent,/续作确认已准备/);await old.onclick();assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
function replyRefresh(h,from,value){for(const call of h.calls().slice(from)){if(call.params.name==='dev_bridge_card_get')h.reply(call,value);else if(call.params.name==='workspace_get')h.reply(call,{branches:{repositories:[]}});}}
function receipt(h,overrides={}){
 const reference={aiSessionId:'session',intentId:'start-review',version:1,purpose:'REVIEW',contentHash:'a'.repeat(64)};
 return {record:{reference,scope:h.evaluate('formalScope'),readOnly:false},event:{reference,result:{structuredContent:{intent:{...reference,projectId:'project',state:'COMPLETED',submission:{outcome:'CONSUMED',recorded:true},commit:{requestId:'user-click'},steps:[{kind:'START_REVIEW',state:'COMPLETED',featureAreaId:'area-a',taskId:'task-a',reviewId:'review-a',runId:'run-a'}],...overrides}}}}};
}
async function afterConfirmation(h,data){h.context.testRecord=data.record;h.context.testEvent=data.event;const from=h.calls().length,pending=h.evaluate('panelAfterConfirmation(testRecord,testEvent)');await settle();return {from,pending};}
test('host rejection is actionable; an explicit retry reuses the original request, while timeout stays unknown and is never resent',async()=>{
 const h=await panel();requireRecovery(h);const from=h.calls().length,first=button(h,'重新授权并继续 GPT 审查').onclick();await settle();replyRefresh(h,from,snapshot(h));await settle();
 const call=h.calls().find(c=>c.params.name==='agent_operation_recover');assert.ok(call);h.send({jsonrpc:'2.0',id:call.id,error:{code:-32601,message:'Tool not available to app'}});await first;
 assert.match(h.root.textContent,/宿主拒绝/);assert.equal(button(h,'重新授权并继续 GPT 审查').disabled,false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 const before=h.calls().length,retry=button(h,'重新授权并继续 GPT 审查').onclick();await settle();replyRefresh(h,before,snapshot(h));await settle();const retried=h.calls().filter(c=>c.params.name==='agent_operation_recover').at(-1);assert.deepEqual(retried.params.arguments,call.params.arguments);
 h.timeout(retried);await retry;assert.match(h.root.textContent,/结果暂不能确认/);assert.equal(button(h,'重新授权并继续 GPT 审查').disabled,true);await button(h,'重新授权并继续 GPT 审查').onclick();assert.equal(h.calls().filter(c=>c.params.name==='agent_operation_recover').length,2);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
test('successful exact confirmation hands the authorized GPT review to the same conversation once without another user message',async()=>{
 const h=await panel(),data=receipt(h),run=await afterConfirmation(h,data);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);replyRefresh(h,run.from,snapshot(h));await settle();
 const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/review_result_submit/);assert.match(message.params.content[0].text,/"runId":"run-a"/);h.hostReply(message,{});await run.pending;
 const repeated=await afterConfirmation(h,data);replyRefresh(h,repeated.from,snapshot(h));await repeated.pending;assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.equal(h.calls().some(c=>/commit|recover|prepare|submit/.test(c.params.name)),false);h.dispose();
});
test('partial, unknown, rejected, mismatched and historical confirmations never auto-continue GPT',async()=>{
 for(const change of ['partial','unknown','unrecorded','mismatch','error','history']){
  const h=await panel(),data=receipt(h);const intent=data.event.result.structuredContent.intent;
  if(change==='partial')intent.state='PARTIAL';if(change==='unknown')intent.submission.outcome='UNKNOWN';if(change==='unrecorded')intent.submission.recorded=false;if(change==='mismatch')intent.contentHash='b'.repeat(64);if(change==='error')data.event.result.isError=true;if(change==='history')data.record.readOnly=true;
  const run=await afterConfirmation(h,data);replyRefresh(h,run.from,snapshot(h));await run.pending;assert.equal(h.posts.some(p=>p.method==='ui/message'),false,change);h.dispose();
 }
});
test('confirmed creation follows only its recorded new task and exact Review/Run even when the new definition differs',async()=>{
 const h=await panel(),data=receipt(h);data.event.result.structuredContent.intent.steps=[{kind:'CREATE_TASK',state:'COMPLETED',taskId:'new-task',revisionId:'new-revision'},{kind:'START_REVIEW',state:'COMPLETED',featureAreaId:'area-a',taskId:'new-task',reviewId:'new-review',runId:'new-run'}];
 const next=JSON.parse(JSON.stringify(snapshot(h)).replaceAll('task-a','new-task').replaceAll('revision-a','new-revision').replaceAll('review-a','new-review').replaceAll('run-a','new-run'));
 const run=await afterConfirmation(h,data);assert.equal(h.evaluate('taskID'),'new-task');replyRefresh(h,run.from,next);await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/"taskId":"new-task"/);assert.match(message.params.content[0].text,/"reviewId":"new-review"/);h.hostReply(message,{});await run.pending;h.dispose();
});
test('navigation during readback or a different current Review prevents continuation of the old confirmed operation',async()=>{
 for(const change of ['navigation','review']){const h=await panel(),run=await afterConfirmation(h,receipt(h)),next=snapshot(h);if(change==='navigation')h.evaluate("setIdentity(sessionID,featureAreaID,'other-task',true)");else next.presentation.taskView.currentReview.id='other-review';replyRefresh(h,run.from,next);await run.pending;assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();}
});
test('a confirmed Direct File grant continues only its exact active workflow after fresh readback',async()=>{
 const h=await panel(),grant={taskId:'task-a',projectId:'project',featureAreaId:'area-a',aiSessionId:'session',taskDefinitionRevisionId:'revision-a',workflowId:'file-workflow',generation:2,state:'ACTIVE',enabled:true};h.context.testGrant=grant;
 h.evaluate("workspace.presentation.taskView.phase='IMPLEMENTING';workspace.presentation.taskView.implementationMode='DIRECT_FILE';workspace.presentation.taskView.directFile=testGrant;render()");
 const data=receipt(h,{currentDirectFile:grant,steps:[{kind:'APPROVE',state:'COMPLETED',taskId:'task-a'}]}),run=await afterConfirmation(h,data);replyRefresh(h,run.from,snapshot(h));await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/file_implementation_result_submit/);h.hostReply(message,{});await run.pending;h.dispose();
});

function verificationCheckpoint(h){
 h.evaluate("workspace.presentation.taskView.phase='ACCEPTANCE_PENDING';workspace.presentation.taskView.currentExecution={id:'execution-a'};workspace.presentation.taskView.verificationDecisions={taskId:'task-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',continuationConfirmed:true,choices:[{action:'CONTINUE_TASK',label:'延续任务',available:true}]};workspace.presentation.taskView.actions=[{action:'REQUEST_CHANGES',available:true,target:{taskId:'task-a',featureAreaId:'area-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',decisionId:'TASK_ACCEPTANCE:execution-a'}},{action:'REQUEST_REVERIFICATION',available:true,target:{taskId:'task-a',featureAreaId:'area-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',decisionId:'TASK_ACCEPTANCE:execution-a'}}];render()");
}
function continuationReceipt(h){
 const content={action:'CONTINUE_TASK',execution:{action:'CONTINUE_TASK',taskId:'task-a',featureAreaId:'area-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',decisionId:'TASK_ACCEPTANCE:execution-a'}};
 const data=receipt(h,{purpose:'EXECUTION',action:'CONTINUE_TASK',content,steps:[{kind:'RECORD_TASK_CHOICE',state:'COMPLETED',taskId:'task-a',executionId:'execution-a',revisionId:'revision-a'}]});
 data.record.reference.purpose='EXECUTION';return data;
}
test('a consumed Continue confirmation automatically requests the exact same Task successor plan once',async()=>{
 const h=await panel();verificationCheckpoint(h);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 assert.match(h.root.textContent,/已确认延续原任务/);assert.ok(button(h,'准备后续方案'));
 assert.ok(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-panel-capability')==='REQUEST_CHANGES'));
 assert.ok(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-panel-capability')==='REQUEST_REVERIFICATION'));
 const data=continuationReceipt(h),run=await afterConfirmation(h,data);replyRefresh(h,run.from,snapshot(h));await settle();
 const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);
 const text=message.params.content[0].text;assert.match(text,/无需再次准备 CONTINUE_TASK/);assert.match(text,/execution_prepare\(action=REQUEST_CHANGES\)/);assert.match(text,/"executionId":"execution-a"/);assert.match(text,/等待用户在面板确认/);
 h.hostReply(message,{});await run.pending;
 const repeated=await afterConfirmation(h,data);replyRefresh(h,repeated.from,snapshot(h));await repeated.pending;
 assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);
 assert.equal(h.calls().some(c=>/commit|prepare|start|submit/.test(c.params.name)),false);h.dispose();
});
test('continuation handoff rejects unconfirmed, historical, stale, held and mismatched receipts',async()=>{
 for(const mutation of ['pending','unknown','history','foreign-task','wrong-step','execution','revision','held','unconfirmed']){
  const h=await panel();verificationCheckpoint(h);const data=continuationReceipt(h),intent=data.event.result.structuredContent.intent,next=snapshot(h);
  if(mutation==='pending')intent.state='PENDING';if(mutation==='unknown')intent.submission.outcome='UNKNOWN';if(mutation==='history')data.record.readOnly=true;
  if(mutation==='foreign-task')intent.content.execution.taskId='foreign-task';if(mutation==='wrong-step')intent.steps[0].executionId='foreign-execution';
  if(mutation==='execution')next.presentation.taskView.currentExecution.id='replacement-execution';if(mutation==='revision')next.presentation.taskView.details.taskDefinitionRevisionId='replacement-revision';
  if(mutation==='held')next.presentation.taskView.verificationDecisions.hold={note:'paused'};if(mutation==='unconfirmed')next.presentation.taskView.verificationDecisions.continuationConfirmed=false;
  const run=await afterConfirmation(h,data);replyRefresh(h,run.from,next);await run.pending;assert.equal(h.posts.some(p=>p.method==='ui/message'),false,mutation);h.dispose();
 }
});
test('continuation host timeout is preserved and never resends on refresh or a repeated callback',async()=>{
 const h=await panel();verificationCheckpoint(h);const data=continuationReceipt(h),run=await afterConfirmation(h,data);replyRefresh(h,run.from,snapshot(h));await settle();
 const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);h.timeout(message);await run.pending;
 assert.equal(h.evaluate('[...panelPendingHandoffs.values()][0].state'),'UNKNOWN');
 await h.evaluate('panelDrainHandoffs()');const again=await afterConfirmation(h,data);replyRefresh(h,again.from,snapshot(h));await again.pending;
 assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.match(h.root.textContent,/未能确认|无法确认|未确认/);h.dispose();
});
test('a queued continuation survives read failure and validates its frozen receipt before host restore resumes',async()=>{
 const h=await panel();verificationCheckpoint(h);const data=continuationReceipt(h),first=await afterConfirmation(h,data);
 for(const call of h.calls().slice(first.from))if(call.params.name==='dev_bridge_card_get')h.send({jsonrpc:'2.0',id:call.id,error:{code:-32000,message:'read failed'}});else if(call.params.name==='workspace_get')h.reply(call,{branches:{repositories:[]}});
 await first.pending;assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 const saved=h.evaluate('JSON.stringify([...panelPendingHandoffs.values()][0])');h.context.savedJob=JSON.parse(saved);
 h.evaluate('panelPendingHandoffs.clear();savedJob.validated=false;panelPendingHandoffs.set(savedJob.key,savedJob);authorityFresh=true');
 const drain=h.evaluate('panelDrainHandoffs()');await settle();const read=h.calls().filter(c=>c.params.name==='confirmation_get').at(-1);assert.ok(read);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 h.reply(read,data.event.result.structuredContent);await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);h.hostReply(message,{});await drain;
 await h.evaluate('panelDrainHandoffs()');assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);h.dispose();
});

test('task heading describes only the exact current continuation and distinguishes awaiting a new plan',async()=>{
 const h=await panel();
 h.evaluate("(()=>{const v=workspace.presentation.taskView;v.id='task-a';v.terminal=false;v.goal='整体目标';v.details={taskDefinitionRevisionId:'revision-next'};v.currentExecution={id:'execution-next',taskDefinitionRevisionId:'revision-next',summary:'修复节点确认后的回访冲突'};v.executionHistory=[{current:false,ordinal:1,execution:{id:'execution-old',taskDefinitionRevisionId:'revision-old',summary:'不应展示的旧目标'}},{current:true,ordinal:2,sourceAction:'REQUEST_CHANGES',predecessorExecutionId:'execution-old',execution:v.currentExecution}];v.verificationDecisions=null;render()})()");
 assert.match(h.root.querySelectorAll('p').filter(x=>x.className==='task-continuation').map(x=>x.textContent).join(''),/本次延续 · 修订实施：修复节点确认后的回访冲突/);
 assert.doesNotMatch(h.root.querySelectorAll('p').filter(x=>x.className==='task-continuation').map(x=>x.textContent).join(''),/不应展示的旧目标|整体目标/);
 h.evaluate("workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-newer';render()");assert.equal(h.root.querySelectorAll('p').filter(x=>x.className==='task-continuation').length,0);
 h.evaluate("(()=>{const v=workspace.presentation.taskView;v.details.taskDefinitionRevisionId='revision-next';v.verificationDecisions={continuationConfirmed:true,taskId:v.id,executionId:v.currentExecution.id,taskDefinitionRevisionId:'revision-next',choices:[]};render()})()");
 assert.match(h.root.querySelectorAll('p').filter(x=>x.className==='task-continuation').map(x=>x.textContent).join(''),/已确认，待准备后续方案/);assert.doesNotMatch(h.root.querySelectorAll('p').filter(x=>x.className==='task-continuation').map(x=>x.textContent).join(''),/回访冲突/);
 h.dispose();
});


test('persistent workbench stays open while click messages reach the main conversation',async()=>{
 for(const mode of ['pip','fullscreen'])for(const route of ['active','standard','compatibility']){
  const sent=[],options={hostContext:{displayMode:mode,availableDisplayModes:['inline','pip','fullscreen']}};
  if(route==='active')options.hostCapabilities={experimental:{'openai/message':{}}};
  if(route==='standard')options.hostCapabilities={message:{text:{}}};
  if(route==='compatibility')options.openai={sendFollowUpMessage:async value=>sent.push(value)};
  const h=await panel(options);codexInterrupted(h);
  const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
  const message=h.posts.find(p=>p.method==='ui/message');
  if(route==='compatibility'){
   assert.equal(message,undefined);assert.equal(sent.length,1);assert.equal(sent[0].scrollToBottom,false);
  }else{
   assert.ok(message);if(route==='active')assert.deepEqual(message.params._meta,{'openai/message':{target:'active',send:true}});
   h.hostReply(message,{});
  }
  await click;await settle();
  const restore=h.posts.filter(p=>p.method==='ui/request-display-mode');
  assert.equal(restore.length,1);assert.equal(restore[0].params.mode,mode);
  h.hostReply(restore[0],{mode});await settle();
  assert.equal(h.evaluate('displayMode'),mode,route+' '+mode);
  assert.equal(h.posts.some(p=>p.method==='ui/request-display-mode'&&p.params.mode==='inline'),false);
  assert.equal(h.root.querySelectorAll('h1').length,1);
  assert.equal(button(h,'继续 Codex · 准备确认').disabled,true);
  assert.match(h.root.textContent,/等待准确业务回执/);
  assert.equal(h.calls().some(p=>/commit|start|submit|prepare/.test(p.params.name)),false);
  h.dispose();
 }
});

test('workbench can be explicitly reopened after a reply without resending its request',async()=>{
 const h=await panel();codexInterrupted(h);
 const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
 h.hostReply(h.posts.find(p=>p.method==='ui/message'),{});await click;await settle();
 const preserved=h.posts.find(p=>p.method==='ui/request-display-mode');assert.ok(preserved);h.hostReply(preserved,{mode:'pip'});await settle();
 const displayCount=h.posts.filter(p=>p.method==='ui/request-display-mode').length;
 // The host/user closes the viewer independently of message delivery.
 h.event('openai:set_globals',{detail:{globals:{displayMode:'inline'}}});await settle();
 assert.equal(h.evaluate('displayMode'),'inline');
 assert.equal(h.posts.filter(p=>p.method==='ui/request-display-mode').length,displayCount,'manual close must not auto-reopen');
 const reopen=button(h,'打开 Dev Bridge 工作面板');assert.ok(reopen);assert.equal(reopen.disabled,false);
 const open=reopen.onclick();await settle();const display=h.posts.filter(p=>p.method==='ui/request-display-mode').at(-1);assert.notEqual(display,preserved);assert.equal(display.params.mode,'pip');
 h.hostReply(display,{mode:'pip'});await open;await settle();
 assert.equal(h.evaluate('displayMode'),'pip');assert.equal(h.evaluate('displayPending'),false);
 assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);
 assert.equal(button(h,'继续 Codex · 准备确认').disabled,true);
 assert.equal(h.evaluate('[...conversationAttempts.values()][0].state'),'SENT');
 h.dispose();
});

test('failed persistent-panel messages neither close the viewer nor auto-resend',async()=>{
 for(const outcome of ['error','timeout']){
  const h=await panel();codexInterrupted(h);const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
  const message=h.posts.find(p=>p.method==='ui/message');if(outcome==='error')h.reject(message);else h.timeout(message);await click;
  assert.equal(h.evaluate('displayMode'),'pip');assert.equal(h.posts.some(p=>p.method==='ui/request-display-mode'),false);
  assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.match(h.root.textContent,/未能确认|无法确认|未确认/);
  h.dispose();
 }
});

test('host hiding the viewer during delivery is recovered once without duplicating the message',async()=>{
 for(const mode of ['pip','fullscreen'])for(const route of ['active','standard','compatibility']){
  let accepted;const options={hostContext:{displayMode:mode,availableDisplayModes:['inline','pip','fullscreen']}};
  if(route==='active')options.hostCapabilities={experimental:{'openai/message':{}}};
  if(route==='standard')options.hostCapabilities={message:{text:{}}};
  if(route==='compatibility')options.openai={sendFollowUpMessage:()=>new Promise(resolve=>accepted=resolve)};
  const h=await panel(options);codexInterrupted(h);
  const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
  h.event('openai:set_globals',{detail:{globals:{displayMode:'inline'}}});await settle();
  if(route==='compatibility')accepted();else h.hostReply(h.posts.find(p=>p.method==='ui/message'),{});
  await click;await settle();
  const display=h.posts.filter(p=>p.method==='ui/request-display-mode');assert.equal(display.length,1);assert.equal(display[0].params.mode,mode);
  h.hostReply(display[0],{mode});await settle();assert.equal(h.evaluate('displayMode'),mode);
  assert.equal(h.evaluate('[...conversationAttempts.values()][0].state'),'SENT');
  assert.equal(h.calls().some(p=>/commit|start|submit|prepare/.test(p.params.name)),false);
  h.evaluate('ensureSinglePanel();render()');await settle();assert.equal(h.posts.filter(p=>p.method==='ui/request-display-mode').length,1);
  assert.equal(h.posts.filter(p=>p.method==='ui/message').length,route==='compatibility'?0:1);h.dispose();
 }
});

test('display denial, error and timeout never turn an accepted message into unknown or resend it',async()=>{
 for(const outcome of ['denied','error','timeout']){
  const h=await panel();codexInterrupted(h);const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
  h.event('openai:set_globals',{detail:{globals:{displayMode:'inline'}}});
  h.hostReply(h.posts.find(p=>p.method==='ui/message'),{});await click;await settle();
  const display=h.posts.find(p=>p.method==='ui/request-display-mode');assert.ok(display);
  if(outcome==='denied')h.hostReply(display,{mode:'inline'});else if(outcome==='error')h.reject(display);else h.timeout(display);
  await settle();assert.equal(h.evaluate('displayPending'),false);assert.equal(h.evaluate('[...conversationAttempts.values()][0].state'),'SENT');
  const reopen=button(h,'打开 Dev Bridge 工作面板');assert.ok(reopen);assert.equal(reopen.disabled,false);
  const opening=reopen.onclick();const next=h.posts.filter(p=>p.method==='ui/request-display-mode').at(-1);h.hostReply(next,{mode:'pip'});await opening;
  assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);h.dispose();
 }
});

test('navigation or explicit display changes during delivery take priority over preserving the old viewer',async()=>{
 for(const change of ['task','display']){
  const h=await panel();codexInterrupted(h);const click=button(h,'继续 Codex · 准备确认').onclick();await settle();
  let switching;
  if(change==='task')h.evaluate("setIdentity('session','area-a','task-b',true)");
  else{switching=h.evaluate("switchDisplayMode('inline')");h.hostReply(h.posts.find(p=>p.method==='ui/request-display-mode'),{mode:'inline'});await switching;}
  h.hostReply(h.posts.find(p=>p.method==='ui/message'),{});await click;await settle();
  assert.equal(h.posts.filter(p=>p.method==='ui/request-display-mode').length,change==='display'?1:0);
  assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);h.dispose();
 }
});
