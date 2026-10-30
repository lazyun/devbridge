'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'handoff-receipts',entryIdentity:'handoff-receipts',display:'EXPANDED'};
const feed=items=>({scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items});
async function panel(options={}){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']},...options});
 h.context.crypto.randomUUID=()=> 'stable-handoff-request';
 const workspace={...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()};
 Object.assign(workspace.presentation.taskView,{actionsStatus:'AVAILABLE',phase:'REVIEWING',details:{taskDefinitionRevisionId:'revision-a'},currentReview:{id:'review-a',taskId:'task-a',executorKind:'UPPER_LLM',stage:'PREPARING',run:{id:'run-a',transportStatus:'RUNNING'}}});
 h.input(binding);h.result(binding,{workspace,singlePanel:true});h.init();await settle();
 const replied=new Set();
 for(let round=0;round<4;round++){
  for(const c of h.calls())if(!replied.has(c.id)){
   replied.add(c.id);
   if(c.params.name==='dev_bridge_panel_get')h.reply(c,feed([]));
   if(c.params.name==='dev_bridge_card_get')h.reply(c,workspace);
   if(c.params.name==='workspace_get')h.reply(c,{branches:{repositories:[]}});
  }
  await settle();
 }
 return h;
}
const message=h=>h.posts.filter(p=>p.method==='ui/message');
const button=(h,label)=>h.root.querySelectorAll('button').find(b=>b.textContent===label);
const attempt=h=>JSON.parse(h.evaluate('JSON.stringify([...conversationAttempts.values()][0])'));
const context=h=>h.evaluate('currentReviewContinuationContext()');
const send=h=>h.evaluate('sendConversationContext(currentReviewContinuationContext())');
function noWrites(h){assert.equal(h.calls().some(c=>/commit|submit|start|recover|prepare/.test(c.params.name)),false);}

function verificationCheckpoint(h){
 h.evaluate("const v=workspace.presentation.taskView;v.currentExecution={id:'execution-a'};v.actionNode={generation:23};v.verificationDecisions={taskId:'task-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',choices:[{action:'CONTINUE_TASK',label:'延续任务',available:true}]};render()");
}
function savedLegacyContinuation(h){
 const current=h.evaluate("verificationDecisionContext('CONTINUE_TASK')");
 const saved=structuredClone(current);
 saved.prompt=saved.prompt.replace(/正式 prepare 把上述 taskActionGeneration（若提供）[^\n]+?(?=正式 prepare 固定 requestId：)/,'正式 prepare 携带上述 taskActionGeneration（若提供），代际变化则停止。');
 assert.notEqual(saved.prompt,current.prompt);
 return saved;
}
test('explicit retry upgrades only the known legacy continuation guidance after reload, preserving request and target',async()=>{
 const first=await panel();verificationCheckpoint(first);first.context.old=savedLegacyContinuation(first);
 const initial=first.evaluate('sendConversationContext(old)');first.hostReply(message(first)[0],{});await initial;
 const snapshot=structuredClone(first.context.window.openai.widgetState);first.dispose();
 const h=await panel({widgetState:snapshot});verificationCheckpoint(h);
 const before=attempt(h).context;assert.equal(message(h).length,0);
 const retry=button(h,'检查并重试延续请求');assert.ok(retry);assert.equal(retry.disabled,false);
 const pending=retry.onclick(),post=message(h)[0];assert.ok(post,'legacy template must not silently block the explicit retry');
 assert.match(post.params.content[0].text,/放在参数顶层/);
 assert.equal(attempt(h).context.prepareRequestId,before.prepareRequestId);
 assert.deepEqual(attempt(h).context.receiptTarget,before.receiptTarget);
 h.hostReply(post,{});await pending;
 const replay=button(h,'检查并重试延续请求');h.evaluate("panelFeed.items=[{aiSessionId:sessionID,projectId:boundProjectID,taskId:taskID,requestId:[...conversationAttempts.values()][0].context.prepareRequestId,intentId:'prepared',version:1,contentHash:'a'.repeat(64),purpose:'EXECUTION',action:'CONTINUE_TASK',state:'PENDING'}]");
 await replay.onclick();assert.equal(message(h).length,1);assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');noWrites(h);h.dispose();
});
test('legacy guidance compatibility cannot change task, generation, request, receipt target or business instructions',async()=>{
 for(const change of ['task','generation','request','receipt','instruction']){
  const h=await panel();verificationCheckpoint(h);h.context.old=savedLegacyContinuation(h);
  const initial=h.evaluate('sendConversationContext(old)');h.hostReply(message(h)[0],{});await initial;
  const retry=button(h,'检查并重试延续请求');
  if(change==='task')h.evaluate("workspace.presentation.taskView.verificationDecisions.taskId='other-task'");
  if(change==='generation')h.evaluate('workspace.presentation.taskView.actionNode.generation=24');
  if(change==='request')h.evaluate("[...conversationAttempts.values()][0].context.prepareRequestId='other-request'");
  if(change==='receipt')h.evaluate("[...conversationAttempts.values()][0].context.receiptTarget.taskId='other-task'");
  if(change==='instruction')h.evaluate("[...conversationAttempts.values()][0].context.prompt+='立即启动运行'");
  await retry.onclick();assert.equal(message(h).length,1,change);assert.match(attempt(h).notice,/目标、内容或操作资格已变化/);noWrites(h);h.dispose();
 }
});

test('message intent is saved before host dispatch and ACK still awaits the exact report',async()=>{
 const h=await panel(),pending=send(h),post=message(h)[0];assert.ok(post);
 const saved=h.saved.find(s=>s.panelPresentation?.conversationAttempts?.some(a=>a.state==='SENDING'));
 assert.ok(saved);assert.equal(saved.panelPresentation.conversationAttempts[0].businessState,'AWAITING_RECEIPT');
 h.hostReply(post,{});await pending;
 assert.equal(attempt(h).state,'SENT');assert.equal(attempt(h).businessState,'AWAITING_RECEIPT');
 assert.match(h.root.textContent,/宿主已接收消息.*等待准确业务回执/);
 h.evaluate("workspace.presentation.taskView.currentReview={...workspace.presentation.taskView.currentReview,id:'different-review',reportId:'unrelated'};reconcileConversationAttempts()");
 assert.equal(attempt(h).businessState,'STOPPED');
 h.evaluate("workspace.presentation.taskView.currentReview={...workspace.presentation.taskView.currentReview,id:'review-a',reportId:'report-a'};reconcileConversationAttempts();render()");
 assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');assert.equal(attempt(h).receipt.reportId,'report-a');
 assert.match(h.root.textContent,/正式报告已入库/);assert.equal(message(h).length,1);noWrites(h);h.dispose();
});

test('reload of an interrupted send preserves UNKNOWN and explicit retry uses identical payload',async()=>{
 const first=await panel(),pending=send(first),original=message(first)[0];
 const saved=structuredClone(first.context.window.openai.widgetState);
 first.dispose();await pending;
 const h=await panel({widgetState:saved});assert.equal(message(h).length,0);
 assert.equal(attempt(h).state,'UNKNOWN');
 assert.equal(button(h,'已请求继续审查').disabled,true);
 const retry=button(h,'核对后重新请求 GPT');assert.ok(retry);assert.equal(retry.disabled,false);
 const resent=retry.onclick();assert.equal(message(h).length,1);
 assert.deepEqual(message(h)[0].params,original.params);h.hostReply(message(h)[0],{});await resent;
 assert.equal(attempt(h).state,'SENT');noWrites(h);h.dispose();
});

test('known host rejection remains retryable and does not silently disappear on reload',async()=>{
 const h=await panel(),pending=send(h),post=message(h)[0];
 h.send({jsonrpc:'2.0',id:post.id,error:{code:-32601,message:'unsupported'}});
 await pending;assert.equal(attempt(h).state,'REJECTED');
 const saved=structuredClone(h.context.window.openai.widgetState);h.dispose();
 const restored=await panel({widgetState:saved});assert.equal(attempt(restored).state,'REJECTED');assert.equal(message(restored).length,0);
 const retried=button(restored,'核对后重新请求 GPT').onclick();restored.hostReply(message(restored)[0],{});await retried;
 assert.equal(message(restored).length,1);restored.dispose();
});

test('business receipt wins over a late ACK or late host error',async()=>{
 for(const reply of ['ack','error']){
  const h=await panel(),pending=send(h),post=message(h)[0];
  h.evaluate("workspace.presentation.taskView.currentReview.reportId='report-a';reconcileConversationAttempts()");
  if(reply==='ack')h.hostReply(post,{});else h.reject(post);
  await pending;assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');assert.match(attempt(h).notice,/正式报告已入库/);assert.doesNotMatch(attempt(h).notice,/等待准确业务回执/);h.dispose();
 }
});

test('prepare receipt correlates the exact original request and retry survives a new mount',async()=>{
 const h=await panel();
 h.evaluate("panelComposer='TASK';panelDrafts.set(panelScope()+':TASK','独立任务的原始需求')");
 h.context.prepareContext=h.evaluate("panelRequestContext('TASK','独立任务的原始需求')");
 const pending=h.evaluate('sendConversationContext(prepareContext)'),post=message(h)[0],original=structuredClone(post.params);
 h.timeout(post);await pending;
 const saved=structuredClone(h.context.window.openai.widgetState);h.dispose();
 const resumed=await panel({widgetState:saved});
 assert.equal(attempt(resumed).context.prepareRequestId,'panel_stable-handoff-request');assert.equal(message(resumed).length,0);
 // Directly expose the same context as the composer; no business write occurs.
 resumed.context.same=resumed.evaluate("panelRequestContext('TASK','独立任务的原始需求')");
 resumed.evaluate("appendConversationRetry(root,same,conversationAttempts.get(same.key))");
 const resent=button(resumed,'核对后重新请求 GPT').onclick();assert.deepEqual(message(resumed)[0].params,original);resumed.hostReply(message(resumed)[0],{});await resent;
 const item={aiSessionId:'session',projectId:'project',intentId:'prepared',version:1,contentHash:'a'.repeat(64),purpose:'TASK',action:'CREATE_TASK',state:'PENDING',requestId:'different'};
 resumed.context.receiptItem=item;
 resumed.evaluate("panelFeed.items=[receiptItem];reconcileConversationAttempts()");assert.equal(attempt(resumed).businessState,'AWAITING_RECEIPT');
 resumed.evaluate("receiptItem.requestId='panel_stable-handoff-request';panelFeed.items=[receiptItem];reconcileConversationAttempts();render()");
 assert.equal(attempt(resumed).businessState,'RECEIPT_OBSERVED');assert.equal(attempt(resumed).receipt.intentId,'prepared');noWrites(resumed);resumed.dispose();
});

test('failed or unsupported snapshot storage blocks dispatch and allows explicit recovery',async()=>{
 for(const kind of ['throw','async','unsupported']){
  const h=await panel(),original=h.context.window.openai.setWidgetState;
  if(kind==='throw')h.context.window.openai.setWidgetState=()=>{throw Error('storage');};
  if(kind==='async')h.context.window.openai.setWidgetState=()=>Promise.reject(Error('storage'));
  if(kind==='unsupported')delete h.context.window.openai.setWidgetState;
  await send(h);assert.equal(message(h).length,0);assert.equal(attempt(h).state,'NOT_SENT');assert.match(h.root.textContent,/尚未发送/);
  h.context.window.openai.setWidgetState=original;
  const retried=button(h,'核对后重新请求 GPT').onclick();assert.equal(message(h).length,1);h.hostReply(message(h)[0],{});await retried;noWrites(h);h.dispose();
 }
});

test('detached retry, forged restored prompt and stage-only changes cannot duplicate authorized work',async()=>{
 for(const mutation of ['target','prompt']){
  const h=await panel(),pending=send(h);h.hostReply(message(h)[0],{});await pending;
  const key=context(h).key;h.evaluate("workspace.presentation.taskView.currentReview.stage='RUNNING';render()");
  assert.equal(context(h).key,key);assert.equal(message(h).length,1);
  const retry=button(h,'核对后重新请求 GPT');
  if(mutation==='target')h.evaluate("workspace.presentation.taskView.currentReview.id='new-review'");
  else h.evaluate("[...conversationAttempts.values()][0].context.prompt='untrusted replacement'");
  await retry.onclick();assert.equal(message(h).length,1);
  if(mutation==='prompt')assert.match(h.root.textContent,/目标、内容或操作资格已变化/);
  h.dispose();
 }
});

test('current Run analysis completion is independent of message delivery',async()=>{
 const h=await panel();
 h.evaluate("workspace.presentation.taskView.currentReview.executorKind='CODING_AGENT';workspace.presentation.taskView.currentReview.run.transportStatus='SUCCEEDED';workspace.presentation.taskView.currentReview.run.analysisState='REQUIRED';render()");
 const pending=h.evaluate('sendConversationContext(panelRequiredAnalysisContext())');h.hostReply(message(h)[0],{});await pending;
 h.evaluate("workspace.presentation.taskView.currentReview.run={...workspace.presentation.taskView.currentReview.run,id:'old-run',analysisState:'RECORDED'};reconcileConversationAttempts()");
 assert.equal(attempt(h).businessState,'AWAITING_RECEIPT');
 h.evaluate("workspace.presentation.taskView.currentReview.run.id='run-a';reconcileConversationAttempts()");
 assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');noWrites(h);h.dispose();
});

test('file completion reads the original workflow result and rejects another result',async()=>{
 const h=await panel();
 h.evaluate("Object.assign(workspace.presentation.taskView,{phase:'IMPLEMENTING',implementationMode:'DIRECT_FILE',directFile:{aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',taskDefinitionRevisionId:'revision-a',workflowId:'workflow-a',state:'ACTIVE',enabled:true,generation:2}})");
 const pending=h.evaluate('sendConversationContext(currentFileImplementationContext())');h.hostReply(message(h)[0],{});await pending;
 h.evaluate('pollConversationFileReceipts()');let read=h.calls().find(c=>c.params.name==='file_implementation_result_get');assert.ok(read);
 h.reply(read,{id:'wrong',aiSessionId:'session',taskId:'other',taskDefinitionRevisionId:'revision-a',workflowId:'workflow-a'});await settle();assert.equal(attempt(h).businessState,'AWAITING_RECEIPT');
 h.evaluate("[...conversationAttempts.values()][0].nextReadAt=0;pollConversationFileReceipts()");read=h.calls().filter(c=>c.params.name==='file_implementation_result_get').at(-1);
 h.reply(read,{id:'result-a',aiSessionId:'session',taskId:'task-a',taskDefinitionRevisionId:'revision-a',workflowId:'workflow-a'});await settle();assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');assert.equal(attempt(h).receipt.resultId,'result-a');noWrites(h);h.dispose();
});

test('asynchronous snapshot acknowledgement precedes the host send',async()=>{
 const h=await panel(),original=h.context.window.openai.setWidgetState;let acknowledge;
 h.context.window.openai.setWidgetState=function(value){original.call(this,value);return new Promise(resolve=>{acknowledge=resolve;});};
 const pending=send(h);assert.equal(message(h).length,0);assert.equal(attempt(h).state,'SENDING');
 h.context.window.openai.setWidgetState=original;acknowledge();await settle();assert.equal(message(h).length,1);
 h.hostReply(message(h)[0],{});await pending;h.dispose();
});

test('restored completion claims require fresh business proof and foreign journals are ignored',async()=>{
 const h=await panel(),pending=send(h);h.hostReply(message(h)[0],{});await pending;
 const saved=structuredClone(h.context.window.openai.widgetState);
 saved.panelPresentation.conversationAttempts[0].businessState='RECEIPT_OBSERVED';
 saved.panelPresentation.conversationAttempts[0].receipt={reportId:'unproven'};
 h.dispose();
 const restored=await panel({widgetState:saved});assert.equal(attempt(restored).businessState,'AWAITING_RECEIPT');assert.doesNotMatch(restored.root.textContent,/正式报告已入库/);assert.equal(message(restored).length,0);restored.dispose();
 saved.panelPresentation.conversationAttempts[0].session='another-session';
 const foreign=await panel({widgetState:saved});assert.equal(foreign.evaluate('conversationAttempts.size'),0);assert.equal(message(foreign).length,0);foreign.dispose();
});

test('legacy sent handoff suppresses redispatch and follows its exact report receipt',async()=>{
 const h=await panel();
 h.evaluate("panelPendingHandoffs.set('legacy',{key:'legacy',state:'SENT',session:sessionID,project:boundProjectID,area:featureAreaID,task:taskID,revision:'revision-a',review:'review-a',run:'run-a'});reconcileConversationAttempts();render()");
 assert.equal(attempt(h).state,'SENT');await h.evaluate('panelDrainHandoffs()');assert.equal(message(h).length,0);
 h.evaluate("workspace.presentation.taskView.currentReview.reportId='report-a';reconcileConversationAttempts()");
 assert.equal(attempt(h).businessState,'RECEIPT_OBSERVED');assert.equal(h.evaluate("panelPendingHandoffs.get('legacy').businessState"),'RECEIPT_OBSERVED');noWrites(h);h.dispose();
});
