'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
async function panel(state='REVIEW_APPROVAL_REQUIRED'){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'inline',availableDisplayModes:['inline','pip','fullscreen']}});
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:state==='FEATURE_AREA_IDLE'?'':'task-a',presentationRequestId:'one-panel',entryIdentity:'one-panel',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:{...fixture(state),refreshedAt:new Date().toISOString()},singlePanel:true});h.init();await settle();return h;
}
const operation={aiSessionId:'session',intentId:'project-proposal',version:1,contentHash:'a'.repeat(64),purpose:'FEATURE_AREA',action:'CREATE_AREA',state:'PENDING'};
test('one panel requests PiP once and keeps creation proposals outside the current Task next steps',async()=>{
 const h=await panel();const display=h.posts.find(p=>p.method==='ui/request-display-mode');assert.ok(display);assert.equal(display.params.mode,'pip');
 const read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');assert.deepEqual(read.params.arguments,{aiSessionId:'session',taskId:'task-a'});
 h.hostReply(display,{mode:'pip'});h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[operation],truncated:false});await settle();
 assert.equal(h.root.querySelectorAll('details').some(row=>row.getAttribute('data-confirmation-intent')==='project-proposal'),false);
 h.root.querySelectorAll('button').find(button=>button.getAttribute('data-operation-tab')==='history').onclick();
 const proposal=h.root.querySelectorAll('details').find(row=>row.getAttribute('data-confirmation-intent')==='project-proposal');assert.ok(proposal);assert.equal(proposal.open,false);assert.match(proposal.textContent,/其他提案/);
 assert.equal(h.root.querySelectorAll('h1').length,1);assert.equal(h.root.querySelectorAll('button').some(button=>/返回对话/.test(button.textContent)),false);
 h.evaluate('render()');assert.equal(h.posts.filter(p=>p.method==='ui/request-display-mode').length,1);assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);assert.equal(h.calls().some(p=>/commit|prepare|show/.test(p.params.name)),false);h.dispose();
});
test('a foreign Session feed cannot expose confirmation controls in the persistent panel',async()=>{
 const h=await panel(),read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'foreign',projectId:'project',items:[operation]});await settle();
 assert.equal(h.root.querySelectorAll('details').some(row=>row.getAttribute('data-confirmation-intent')),false);assert.equal(h.calls().some(p=>p.params.name==='confirmation_commit'),false);h.dispose();
});
test('finishing the current review never promotes an old new-Task proposal into a required next step',async()=>{
 const h=await panel('REVIEW_RUNNING'),read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get'),display=h.posts.find(p=>p.method==='ui/request-display-mode');h.hostReply(display,{mode:'pip'});
 const old={...operation,purpose:'TASK',action:'CREATE_TASK',intentId:'old-new-task'};
 const review={...operation,purpose:'REVIEW',action:'START_REVIEW',intentId:'current-review',taskId:'task-a',contentHash:'b'.repeat(64)};
 const foreign={...review,intentId:'other-task-review',taskId:'task-b',contentHash:'c'.repeat(64)};
 h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[old,foreign,review]});await settle();
 const rows=()=>h.root.querySelectorAll('details').filter(row=>row.getAttribute('data-confirmation-intent'));
 assert.deepEqual(rows().map(row=>row.getAttribute('data-confirmation-intent')),['current-review']);assert.equal(rows()[0].open,true);
 h.evaluate("panelFeed.items.find(item=>item.intentId==='current-review').state='COMPLETED';render()");
 assert.equal(rows().length,0);assert.equal(h.root.querySelectorAll('button').some(button=>button.getAttribute('data-show-pending-confirmation')),false);
 h.root.querySelectorAll('button').find(button=>button.getAttribute('data-operation-tab')==='history').onclick();
 assert.equal(rows().length,3);assert.ok(rows().every(row=>!row.open));assert.match(rows().find(row=>row.getAttribute('data-confirmation-intent')==='old-new-task').textContent,/其他提案 · 待确认/);
 assert.equal(h.evaluate("panelFeed.items.find(item=>item.intentId==='old-new-task').state"),'PENDING');assert.equal(h.calls().some(p=>/commit|prepare|show/.test(p.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
test('creation remains discoverable in the same panel when no current Task is selected',async()=>{
 const h=await panel('FEATURE_AREA_IDLE'),read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');
 h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[{...operation,purpose:'TASK',action:'CREATE_TASK'}]});await settle();
 const display=h.posts.find(p=>p.method==='ui/request-display-mode');h.hostReply(display,{mode:'pip'});await settle();
 const row=h.root.querySelectorAll('details').find(row=>row.getAttribute('data-confirmation-intent')===operation.intentId);assert.ok(row);assert.equal(row.open,true);assert.doesNotMatch(row.textContent,/其他提案/);assert.equal(h.calls().some(p=>/commit|prepare|show/.test(p.params.name)),false);h.dispose();
});

test('fixed panel names GPT and Codex explicitly and never guesses an unknown review executor',async()=>{
 const h=await panel('REVIEW_RUNNING');h.hostReply(h.posts.find(p=>p.method==='ui/request-display-mode'),{mode:'pip'});await settle();
 for(const [kind,label] of [['UPPER_LLM','GPT 审查'],['CODING_AGENT','Codex 审查'],['PI_AGENT','Pi Agent 审查'],['','执行方未记录的审查']]){
  h.evaluate(`workspace.presentation.taskView.currentReview={id:'review',executorKind:${JSON.stringify(kind)},ordinal:2,reviewDepth:'DEEP',activity:{summary:'等待原对话'}};workspace.presentation.taskView.phase='REVIEWING';render()`);
  assert.match(h.root.querySelectorAll('p').find(node=>node.textContent.startsWith('审查方式：')).textContent,new RegExp(label));
 }
 assert.equal(h.calls().some(p=>/commit|prepare/.test(p.params.name)),false);h.dispose();
});


test('current GPT review has a click-only exact-target review-and-submit request',async()=>{
 const h=await panel('REVIEW_RUNNING');h.hostReply(h.posts.find(p=>p.method==='ui/request-display-mode'),{mode:'pip'});await settle();
 h.evaluate("workspace.presentation.taskView.phase='REVIEWING';workspace.presentation.taskView.actionsStatus='AVAILABLE';workspace.presentation.taskView.details={taskDefinitionRevisionId:'definition-2'};workspace.presentation.taskView.currentReview={id:'review_current',taskId:'task-a',executorKind:'UPPER_LLM',stage:'RUNNING',run:{id:'run_current',transportStatus:'RUNNING'}};render()");
 const ctx=h.evaluate('currentReviewContinuationContext()');assert.ok(ctx);assert.match(ctx.prompt,/review_result_submit/);assert.match(ctx.prompt,/"reviewId":"review_current"/);assert.match(ctx.prompt,/"runId":"run_current"/);assert.match(ctx.prompt,/等待用户确认/);
 const requestButton=h.root.querySelectorAll('button').find(b=>b.textContent==='让 GPT 继续审查并提交结果');assert.ok(requestButton);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);assert.equal(h.calls().some(p=>/prepare|commit|submit|recover/.test(p.params.name)),false);
 const sent=requestButton.onclick();await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/review_result_submit/);h.hostReply(message,{});await sent;
 await requestButton.onclick();assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.equal(h.calls().some(p=>/prepare|commit|submit|recover/.test(p.params.name)),false);
 for(const mutation of ["workspace.presentation.taskView.currentReview.executorKind='CODING_AGENT'","workspace.presentation.taskView.currentReview.executorKind='PI_AGENT'","workspace.presentation.taskView.currentReview.executorKind='UPPER_LLM';workspace.presentation.taskView.currentReview.taskId='foreign'"]){h.evaluate(mutation);assert.equal(h.evaluate('currentReviewContinuationContext()'),null);}
 h.dispose();
});

test('history remounts read-only cards and disposes a writable controller when current moves into history',async()=>{
 const h=await panel('REVIEW_RUNNING');h.hostReply(h.posts.find(p=>p.method==='ui/request-display-mode'),{mode:'pip'});
 const mounts=[];h.context.window.DevBridgeConfirmationStack={mount(host,ref,hooks){const record={host,ref,hooks,disposed:false};mounts.push(record);return {dispose(){record.disposed=true;}};}};
 const read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');
 h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[{...operation,purpose:'REVIEW',action:'START_REVIEW',taskId:'task-a'}]});await settle();
 h.evaluate('for(const r of formalCards.values())r.host.isConnected=true;mountFormalCards()');
 assert.equal(mounts.length,1);assert.equal(mounts[0].hooks.readOnly,false);assert.equal(mounts[0].hooks.canInteract(),true);
 h.evaluate("panelFeed.items[0].state='COMPLETED';render()");assert.equal(mounts[0].disposed,true);assert.equal(mounts[0].hooks.canInteract(),false);
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='history').onclick();
 h.evaluate('formalOpen=[...formalCards.keys()][0];for(const r of formalCards.values())r.host.isConnected=true;mountFormalCards()');
 assert.equal(mounts.length,2);assert.equal(mounts[1].hooks.readOnly,true);assert.equal(mounts[1].hooks.canInteract(),false);
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='next').onclick();assert.equal(mounts[1].disposed,true);
 assert.equal(h.calls().some(p=>/commit|prepare/.test(p.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

async function actionablePanel(state='REVIEW_RUNNING'){
 const h=await panel(state);h.context.crypto.randomUUID=()=> 'unique-user-click';
 const display=h.posts.find(p=>p.method==='ui/request-display-mode');if(display)h.hostReply(display,{mode:'pip'});else h.send({jsonrpc:'2.0',method:'ui/notifications/host-context-changed',params:{displayMode:'pip'}});
 h.evaluate("if(workspace.presentation.taskView){workspace.presentation.taskView.actionsStatus='AVAILABLE';workspace.presentation.taskView.status='ACTIVE';workspace.presentation.taskView.terminal=false;workspace.presentation.taskView.readOnly=false;}render()");await settle();return h;
}
const findButton=(h,label)=>h.root.querySelectorAll('button').find(b=>b.textContent===label);
test('one workbench covers setup, review, execution, verification, merge and terminal results without extra Cards',async()=>{
 for(const state of ['FEATURE_AREA_IDLE','REVIEW_RUNNING','REVIEW_APPROVAL_REQUIRED','DEVELOPMENT_TASK_READY','IMPLEMENTATION_RUNNING','VERIFICATION_RUNNING','RECOVERY_REQUIRED','COMPLETE']){
  const h=await actionablePanel(state);
  assert.ok(h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='results'),state);
  assert.ok(findButton(h,'＋ 发起操作'),state);
  h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='results').onclick();assert.match(h.root.textContent,/结果与报告/);
  h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='history').onclick();assert.equal(h.root.querySelectorAll('button').some(b=>b.textContent==='＋ 发起操作'),false);
  assert.equal(h.posts.some(p=>p.method==='ui/message'),false);assert.equal(h.calls().some(p=>/commit|prepare|submit|start|decide/.test(p.params.name)),false);h.dispose();
 }
});
test('new creation is matched by its own prepare request; older proposals remain read-only history',async()=>{
 const h=await actionablePanel();
 const ctx=h.evaluate("panelRequestContext('TASK','独立的新任务需求')");assert.ok(ctx);assert.match(ctx.prompt,/固定 requestId/);assert.match(ctx.prompt,/不消费其他提案/);
 h.evaluate(`panelProposalRequest={scope:panelScope(),requestId:${JSON.stringify(ctx.prepareRequestId)}}`);
 const read=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');
 h.reply(read,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[{...operation,purpose:'TASK',action:'CREATE_TASK',intentId:'new',requestId:ctx.prepareRequestId},{...operation,purpose:'TASK',action:'CREATE_TASK',intentId:'old',requestId:'another-request'}]});await settle();
 assert.deepEqual(h.root.querySelectorAll('details').filter(r=>r.getAttribute('data-confirmation-intent')).map(r=>r.getAttribute('data-confirmation-intent')),['new']);
 assert.equal(h.evaluate('taskID'),'task-a');assert.equal(h.calls().some(p=>/commit|submit/.test(p.params.name)),false);h.dispose();
});
test('capability requests revalidate exact revision and owner and never run without a click',async()=>{
 const h=await actionablePanel();
 h.evaluate("workspace.presentation.taskView.actions=[{action:'RECOVER',available:true,target:{taskId:'task-a',taskDefinitionRevisionId:'revision-a',operationKind:'REVIEW',operationId:'review-a',runId:'run-a'}}];render()");
 const ctx=h.evaluate("panelRequestContext('RECOVER','',workspace.presentation.taskView.actions[0])");assert.ok(ctx);assert.match(ctx.prompt,/canonical/);
 h.evaluate("workspace.presentation.taskView.actions[0].target.taskId='foreign'");assert.equal(h.evaluate("panelRequestContext('RECOVER','',workspace.presentation.taskView.actions[0])"),null);
 assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
function nativeQuestion(h,unknown=false){
 const runtimeRequest={kind:'CODEX_REQUEST',runtimeRequestId:'question-1',projectId:'project',featureAreaId:'area-a',taskId:'task-a',runId:'run-a',operationKind:'EXECUTION',operationId:'execution-a',executionId:'execution-a',taskDefinitionRevisionId:'revision-a',runtimeEpoch:'epoch-1',threadId:'thread-1',turnId:'turn-1',connectionId:'connection-1',state:'PENDING',method:'item/tool/requestUserInput',payload:{questions:[{id:'q1',question:'保留哪种兼容方式？',options:[{id:'keep',label:'保持兼容'},{id:'replace',label:'替换实现'}]}]},projection:{lifecycleState:unknown?'UNKNOWN':'WAITING_FOR_USER',responseDisposition:unknown?'UNKNOWN':'NOT_SENT',availableActions:unknown?['CONTINUE_WAITING','CANCEL_OPERATION']:['RESPOND','CANCEL_OPERATION']}};
 h.evaluate(`workspace.presentation.taskView.currentExecution={id:'execution-a',implementationRunId:'run-a'};workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';workspace.pendingDecisions=${JSON.stringify([{projectId:'project',featureAreaId:'area-a',taskId:'task-a',kind:'CODEX_REQUEST',runtimeRequest}])};render()`);
}
test('Codex original question requires exact final user confirmation and submits directly without messaging GPT',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');nativeQuestion(h);
 assert.equal(h.calls().some(p=>/submit|prepare_hash/.test(p.params.name)),false);
 const field=h.root.querySelectorAll('select').find(n=>n.getAttribute('aria-label')==='保留哪种兼容方式？');assert.ok(field);assert.equal(field.value,'');field.value='keep';field.onchange();findButton(h,'核对回答').onclick();
 assert.match(h.root.textContent,/确认并提交回答/);assert.equal(h.calls().some(p=>/submit/.test(p.params.name)),false);
 const sending=findButton(h,'确认并提交回答').onclick();await settle();
 const hash=h.calls().find(p=>p.params.name==='runtime_request_user_decision_prepare_hash');assert.ok(hash);assert.deepEqual(hash.params.arguments.decision.answers,[{questionId:'q1',selectedOptionId:'keep'}]);
 h.reply(hash,{recorded:false,decision:{...hash.params.arguments.decision,payloadSHA256:'b'.repeat(64)}});await settle();
 const submit=h.calls().find(p=>p.params.name==='runtime_request_user_decision_submit');assert.ok(submit);assert.equal(submit.params.arguments.decision.runtimeRequestId,'question-1');
 h.reply(submit,{decision:{...submit.params.arguments.decision,id:'durable-decision'}});await sending;
 assert.equal(h.posts.some(p=>p.method==='ui/message'),false);await h.evaluate("panelSubmitRuntime(workspace.pendingDecisions[0],'RESPOND','[]',panelScope()+':question:question-1:epoch-1')");assert.equal(h.calls().filter(p=>p.params.name==='runtime_request_user_decision_submit').length,1);h.dispose();
});
test('unknown answer disposition has no answer or resend button and never submits another answer',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');nativeQuestion(h,true);
 assert.equal(findButton(h,'核对回答'),undefined);assert.equal(h.root.querySelectorAll('select').some(n=>n.getAttribute('aria-label')==='保留哪种兼容方式？'),false);assert.match(h.root.textContent,/不会自动重发/);
 assert.equal(h.evaluate("panelRuntimeContext(workspace.pendingDecisions[0],'RESPOND','old answer')"),null);assert.equal(h.calls().some(p=>/submit/.test(p.params.name)),false);h.dispose();
});
test('results render exact report text in the panel and reject a foreign operation result',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');h.evaluate("workspace.presentation.taskView.currentExecution={id:'execution-a',implementationRunId:'run-a',taskDefinitionRevisionId:'revision-a'};workspace.presentation.taskView.implementationRun={id:'run-a',transportStatus:'SUCCEEDED',analysisState:'RECORDED'};render()");
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='results').onclick();
 const work=findButton(h,'读取完整报告与分析').onclick();await settle();
 for(const read of h.calls().filter(p=>p.params.name==='operation_get'))h.reply(read,{operation:{...read.params.arguments.target},runResults:[{summary:'准确运行结果可直接阅读'}]});
 const analysis=h.calls().find(p=>p.params.name==='run_analysis_get');assert.ok(analysis);h.reply(analysis,{run:{id:'run-a',taskId:'task-a'},report:{summary:'GPT 正式分析可直接阅读'}});await work;assert.match(h.root.textContent,/GPT 正式分析可直接阅读/);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

test('completed tasks allow explicit new work without reopening the old Task',async()=>{
 const h=await actionablePanel('COMPLETE');h.evaluate("workspace.presentation.taskView.status='SUCCEEDED';workspace.presentation.taskView.terminal=true;workspace.presentation.taskView.readOnly=true;render()");
 assert.equal(h.evaluate('panelInteractive()'),false);assert.ok(h.evaluate("panelRequestContext('TASK','新的独立目标')"));assert.equal(h.evaluate('taskID'),'task-a');assert.equal(findButton(h,'＋ 发起操作').disabled,false);h.dispose();
});
test('a host without PiP still gets the same single workbench inline',async()=>{
 const h=await actionablePanel();h.send({jsonrpc:'2.0',method:'ui/notifications/host-context-changed',params:{displayMode:'inline',availableDisplayModes:['inline']}});await settle();
 assert.ok(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-operation-tab')==='results'));assert.ok(findButton(h,'＋ 发起操作'));assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
test('a mismatched or altered native prepared answer cannot be submitted',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');nativeQuestion(h);
 const item=h.evaluate('workspace.pendingDecisions[0]'),key=h.evaluate("panelScope()+':question:question-1:epoch-1'");
 const send=h.evaluate(`panelSubmitRuntime(workspace.pendingDecisions[0],'RESPOND','[{"questionId":"q1","selectedOptionId":"keep"}]',${JSON.stringify(key)})`);await settle();
 const read=h.calls().find(p=>p.params.name==='runtime_request_user_decision_prepare_hash');assert.ok(read);
 h.reply(read,{recorded:false,decision:{...read.params.arguments.decision,answers:[{questionId:'q1',selectedOptionId:'replace'}],payloadSHA256:'b'.repeat(64)}});await send;
 assert.equal(h.calls().some(p=>p.params.name==='runtime_request_user_decision_submit'),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
test('runtime approval displays exact original request then preflights again before one direct decision',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');
 h.evaluate("workspace.presentation.taskView.currentExecution={id:'execution-a',implementationRunId:'run-a'};workspace.pendingDecisions=[{projectId:'project',featureAreaId:'area-a',taskId:'task-a',decision:{kind:'RUNTIME_APPROVAL',id:'approval:one',subjectId:'approval-one',allowedActions:['APPROVE_ONCE','DECLINE'],summary:'请求运行 go test'},options:[{action:'APPROVE_ONCE',target:{decisionId:'approval:one',subjectId:'approval-one',featureAreaId:'area-a',taskId:'task-a',runId:'run-a'}}]}];render()");
 assert.equal(findButton(h,'允许本次'),undefined);const viewing=findButton(h,'查看权限原文').onclick();await settle();const read=h.calls().find(p=>p.params.name==='ui_approval_get');assert.ok(read);const original={id:'approval-one',runId:'run-a',status:'PENDING',allowedDecisions:['APPROVE_ONCE','DECLINE'],request:{command:'go test ./...'},requestTruncated:false};h.reply(read,original);await viewing;assert.match(h.root.textContent,/go test/);
 const sending=findButton(h,'允许本次').onclick();await settle();const preflight=h.calls().filter(p=>p.params.name==='ui_approval_get').at(-1);assert.notEqual(preflight.id,read.id);h.reply(preflight,original);await settle();const decision=h.calls().find(p=>p.params.name==='ui_approval_decide');assert.ok(decision);assert.deepEqual(decision.params.arguments,{aiSessionId:'session',approvalId:'approval-one',decision:'APPROVE_ONCE'});h.reply(decision,{approval:{id:'approval-one',status:'APPROVED'}});await sending;
 assert.equal(h.calls().filter(p=>p.params.name==='ui_approval_decide').length,1);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
test('foreign report data is refused and cannot replace the selected Task results',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');h.evaluate("workspace.presentation.taskView.currentExecution={id:'execution-a',implementationRunId:'run-a',taskDefinitionRevisionId:'revision-a'};render()");
 const work=h.evaluate('panelReadResults(workspace.presentation)');await settle();const reads=h.calls().filter(p=>p.params.name==='operation_get');assert.ok(reads.length);
 for(const read of reads)h.reply(read,{operation:{kind:read.params.arguments.target.kind,operationId:'foreign-operation'},runResults:[{summary:'外部任务的结果不能显示'}]});await work;
 assert.equal(h.evaluate('panelResultData.length'),0);assert.match(h.evaluate('panelResultError'),/读取失败/);h.dispose();
});

test('GPT file implementation is requested only for the exact active approved file grant',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');h.evaluate("workspace.presentation.taskView.phase='IMPLEMENTING';workspace.presentation.taskView.implementationMode='DIRECT_FILE';workspace.presentation.taskView.directFile={taskId:'task-a',projectId:'project',featureAreaId:'area-a',aiSessionId:'session',taskDefinitionRevisionId:'revision-a',workflowId:'workflow-files',generation:2,state:'ACTIVE',enabled:true};render()");
 assert.ok(findButton(h,'让 GPT 继续文件实施'));assert.match(h.evaluate('currentFileImplementationContext().prompt'),/file_implementation_result_submit/);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 h.evaluate("workspace.presentation.taskView.directFile.taskDefinitionRevisionId='foreign'");assert.equal(h.evaluate('currentFileImplementationContext()'),null);h.dispose();
});
test('one reauthorization click checks the exact Core version and hands the same review to GPT only after successful readback',async()=>{
 for(const drift of [false,true]){
  const h=await actionablePanel('REVIEW_RUNNING');requireUpperReauthorization(h);h.evaluate("workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';render()");
  assert.equal(h.calls().some(p=>p.params.name==='agent_operation_recover'),false);
  const renewing=findButton(h,'重新授权并继续 GPT 审查').onclick();await settle();const read=h.calls().filter(p=>p.params.name==='dev_bridge_card_get').at(-1);
  const snapshot=JSON.parse(h.evaluate('JSON.stringify(workspace)'));if(drift)snapshot.presentation.taskView.actions[0].target.expectedUpperVersion.generation=2;h.reply(read,snapshot);await settle();
  const call=h.calls().find(p=>p.params.name==='agent_operation_recover');
  if(drift){assert.equal(call,undefined);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);}else{
   assert.ok(call);assert.deepEqual(call.params.arguments.expectedUpperVersion,{workflowEpoch:'epoch-1',generation:1});h.reply(call,{state:'REAUTHORIZED',operationId:'review-a',runId:'review-run'});await settle();
   const fresh=h.calls().filter(p=>p.params.name==='dev_bridge_card_get').at(-1);assert.notEqual(fresh,read);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
   delete snapshot.presentation.recovery;snapshot.presentation.taskView.currentReview.stage='PREPARING';snapshot.presentation.taskView.actions=[];h.reply(fresh,snapshot);await settle();
   const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/"reviewId":"review-a"/);assert.match(message.params.content[0].text,/"runId":"review-run"/);assert.match(message.params.content[0].text,/review_result_submit/);h.hostReply(message,{});
  }
  await renewing;assert.equal(h.calls().filter(p=>p.params.name==='agent_operation_recover').length,drift?0:1);h.dispose();
 }
});

test('runtime partition questions and actual UNKNOWN request states keep exact panel recovery controls',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');nativeQuestion(h,true);
 h.evaluate("workspace.pendingDecisions[0].runtimeRequest.state='UNKNOWN';workspace.presentation.taskView.activity.runtime={requests:[workspace.pendingDecisions[0].runtimeRequest]};workspace.pendingDecisions=[];render()");
 assert.ok(findButton(h,'继续等待'));assert.ok(findButton(h,'取消本次操作'));assert.equal(findButton(h,'核对回答'),undefined);assert.equal(h.evaluate('panelRuntimeItems().length'),1);assert.equal(h.calls().some(p=>/submit|recovery/.test(p.params.name)),false);h.dispose();
});

test('an already-recorded unknown answer does not block its separate explicit wait or cancel controls',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING');nativeQuestion(h,true);
 const key=h.evaluate("panelScope()+':question:question-1:epoch-1'");h.evaluate(`panelRuntimeReceipts.set(${JSON.stringify(key+':RESPOND')},'回答结果未知')`);
 const waiting=h.evaluate(`panelSubmitRuntime(workspace.pendingDecisions[0],'CONTINUE_WAITING','',${JSON.stringify(key)})`);await settle();
 const recovery=h.calls().find(p=>p.params.name==='runtime_request_recovery');assert.ok(recovery);assert.equal(recovery.params.arguments.recovery.action,'CONTINUE_WAITING');h.reply(recovery,{runtime:{requests:[]}});await waiting;
 assert.equal(h.calls().some(p=>p.params.name==='runtime_request_user_decision_submit'),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

test('completed Codex review without a formal report still exposes required GPT analysis and submission',async()=>{
 const h=await actionablePanel('REVIEW_RUNNING');h.evaluate("workspace.presentation.taskView.currentReview={id:'review-a',taskId:'task-a',executorKind:'CODING_AGENT',stage:'ANALYSIS_REQUIRED',resultAvailable:false,run:{id:'review-run',transportStatus:'SUCCEEDED',analysisState:'REQUIRED'}};render()");
 assert.ok(findButton(h,'让 GPT 分析 Codex 审查并提交结果'));const ctx=h.evaluate('panelRequiredAnalysisContext()');assert.match(ctx.prompt,/run_analysis_submit/);assert.match(ctx.prompt,/真实目标别名/);assert.equal(ctx.runId,'review-run');assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 h.evaluate("workspace.presentation.taskView.currentReview.run.analysisState='RECORDED'");assert.equal(h.evaluate('panelRequiredAnalysisContext()'),null);h.dispose();
});


const refreshBinding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'reload-panel',entryIdentity:'reload-panel',display:'EXPANDED'};
async function restoredPanel({saved={},mode='pip',result={},meta={},hint=true}={}){
 const h=boot({manualIdentity:true,widgetState:saved,hostContext:{displayMode:mode,availableDisplayModes:['inline','pip','fullscreen']}});
 h.input(refreshBinding);h.result({result:{...refreshBinding,...result},context:{aiSessionId:'session',projectId:'project'},...(hint?{presentationHint:{tool:'panel_show'}}:{})},meta);h.init();await settle();
 const replied=new Set();for(let turn=0;turn<5;turn++){
  for(const call of h.calls().filter(c=>!replied.has(c))){replied.add(call);
   if(call.params.name==='dev_bridge_card_get')h.reply(call,{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()});
   else if(call.params.name==='workspace_get')h.reply(call,{result:{branches:{projectId:'project',repositories:[]}}});
   else if(call.params.name==='dev_bridge_panel_get')h.reply(call,{scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[]});
  }await settle();
 }return h;
}
test('host reload without tool _meta retains the workbench and current actions through structured tool identity',async()=>{
 for(const singlePanel of [undefined,true]){
  const h=await restoredPanel({result:singlePanel?{singlePanel}: {}});
  h.evaluate("workspace.presentation.taskView.actionsStatus='AVAILABLE';render()");
  assert.equal(h.evaluate('singlePanel'),true);assert.ok(findButton(h,'＋ 发起操作'));
  assert.ok(h.root.querySelectorAll('button').find(b=>b.id==='db-operation-tab-results'));
  assert.doesNotMatch(h.root.textContent,/后续操作0|暂无后续操作/);
  assert.equal(h.calls().some(c=>/prepare|commit|recover|submit|start/.test(c.params.name)),false);h.dispose();
 }
});
test('same-entry display restoration preserves tabs and never lets historical panels auto-open over the selected viewer',async()=>{
 const first=await panel('REVIEW_RUNNING');first.hostReply(first.posts.find(p=>p.method==='ui/request-display-mode'),{mode:'pip'});await settle();
 first.evaluate("switchOperationTab('history')");
 const saved=structuredClone(first.saved.at(-1));first.dispose();
 saved.cardEntry={presentationMode:'WORKSPACE',presentationRequestId:'reload-panel',entryIdentity:'reload-panel',aiSessionId:'session',selectionHandle:''};
 for(const mode of ['inline','pip']){
  const h=await restoredPanel({saved,mode});
  assert.equal(h.evaluate('singlePanel'),true);assert.equal(h.evaluate('operationTab'),'history');
  assert.equal(h.posts.some(p=>p.method==='ui/request-display-mode'),false);
  if(mode==='pip')assert.ok(h.root.querySelectorAll('button').find(b=>b.id==='db-operation-tab-results'));
  else assert.match(h.root.textContent,/打开 Dev Bridge 工作面板/);
  assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
 }
});
test('foreign saved display preferences cannot suppress the newly selected panel or supply a Task identity',async()=>{
 const saved={cardEntry:{...refreshBinding,entryIdentity:'foreign'},panelPresentation:{singlePanel:true,autoRequested:true,operationTab:'history'},aiSessionId:'foreign',taskId:'foreign'};
 const h=await restoredPanel({saved,mode:'inline'});
 assert.equal(h.evaluate('operationTab'),'next');assert.equal(h.evaluate('taskID'),'task-a');
 assert.equal(h.posts.filter(p=>p.method==='ui/request-display-mode').length,1);h.dispose();
});
test('saved preferences alone cannot create a presentation entry and input cannot select the interactive panel type',async()=>{
 const h=boot({manualIdentity:true,widgetState:{cardEntry:refreshBinding,panelPresentation:{singlePanel:true},aiSessionId:'session',taskId:'task-a'}});h.init();await settle();
 assert.equal(h.evaluate('bindingReady'),false);assert.equal(h.calls().length,0);
 h.input({...refreshBinding,singlePanel:true});h.result(refreshBinding);await settle();
 assert.equal(h.evaluate('singlePanel'),false);h.dispose();
});

test('pre-v24 same-entry snapshots migrate without auto-opening old conversation results',async()=>{
 const saved={cardEntry:{presentationMode:'WORKSPACE',presentationRequestId:'reload-panel',entryIdentity:'reload-panel',aiSessionId:'session',selectionHandle:''},viewVersion:'read-only-v1',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',expanded:true};
 const h=await restoredPanel({saved,mode:'inline'});
 assert.equal(h.evaluate('singlePanel'),true);assert.equal(h.posts.some(p=>p.method==='ui/request-display-mode'),false);
 assert.match(h.root.textContent,/打开 Dev Bridge 工作面板/);assert.equal(h.evaluate('bindingReady'),true);h.dispose();
});

test('same-entry v24 presentation preference survives a host which omits both hint and private metadata',async()=>{
 const saved={cardEntry:{presentationMode:'WORKSPACE',presentationRequestId:'reload-panel',entryIdentity:'reload-panel',aiSessionId:'session',selectionHandle:''},viewVersion:'task-panel-v24',panelPresentation:{singlePanel:true,autoRequested:true,operationTab:'next'},aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a'};
 const h=await restoredPanel({saved,hint:false});
 assert.equal(h.evaluate('singlePanel'),true);assert.ok(findButton(h,'＋ 发起操作'));
 assert.equal(h.calls().some(c=>/prepare|commit|recover|submit|start/.test(c.params.name)),false);h.dispose();
});

function requireUpperReauthorization(h){
 h.evaluate("workspace.presentation.taskView.phase='REVIEWING';workspace.presentation.taskView.currentReview={id:'review-a',taskId:'task-a',executorKind:'UPPER_LLM',stage:'AUTHORIZATION_REQUIRED',run:{id:'review-run',transportStatus:'RUNNING'}};workspace.presentation.recovery={reason:'GPT_REVIEW_AUTHORIZATION_REQUIRED',summary:'服务重启后，原审查文件授权已失效。'};workspace.presentation.taskView.actions=[{action:'RECOVER',available:true,target:{featureAreaId:'area-a',taskId:'task-a',operationKind:'REVIEW',operationId:'review-a',runId:'review-run',expectedUpperVersion:{workflowEpoch:'epoch-1',generation:1}}},{action:'CANCEL_EXECUTION',available:true,target:{taskId:'task-a',featureAreaId:'area-a'}}];workspace.presentation.capabilities=[{action:'RETIRE',available:true,target:{featureAreaId:'area-a'}}];render()");
}
test('reauthorization is the only next-step button and reason; unrelated prepared forms and stale conversation handlers cannot bypass it',async()=>{
 const h=await actionablePanel();h.evaluate("workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';workspace.presentation.taskView.currentReview={id:'review-a',taskId:'task-a',executorKind:'UPPER_LLM',stage:'RUNNING',run:{id:'review-run',transportStatus:'RUNNING'}};render()");
 const stale=findButton(h,'让 GPT 继续审查并提交结果');assert.ok(stale);
 h.evaluate(`panelFeed={scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[${JSON.stringify({...operation,purpose:'REVIEW',action:'APPROVE',taskId:'task-a'})}]};render()`);
 requireUpperReauthorization(h);const list=h.evaluate('operationScroller');assert.deepEqual(list.querySelectorAll('button').map(b=>b.textContent),['重新授权并继续 GPT 审查']);assert.match(list.textContent,/原审查文件授权已失效/);assert.equal(list.querySelectorAll('textarea').length,0);assert.equal(list.querySelectorAll('details').length,0);assert.equal(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-show-pending-confirmation')),false);
 await stale.onclick();assert.equal(h.evaluate("panelRequestContext('TASK','新任务')"),null);assert.equal(h.evaluate("panelRequestContext('CANCEL_EXECUTION','',workspace.presentation.taskView.actions[1])"),null);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);assert.equal(h.calls().some(p=>/recover|prepare|commit/.test(p.params.name)),false);
 assert.equal(h.evaluate('operationScroller').querySelectorAll('textarea').length,0);
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='results').onclick();assert.match(h.root.textContent,/结果与报告/);h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='next').onclick();assert.deepEqual(h.evaluate('operationScroller').querySelectorAll('button').map(b=>b.textContent),['重新授权并继续 GPT 审查']);h.dispose();
});
test('missing or unreadable reauthorization eligibility keeps the sole button disabled and never exposes alternate actions',async()=>{
 const h=await actionablePanel();requireUpperReauthorization(h);
 for(const change of ["workspace.presentation.taskView.actions[0].available=false","workspace.presentation.taskView.actionsStatus='UNAVAILABLE'"]){h.evaluate(change+';render()');const buttons=h.evaluate('operationScroller').querySelectorAll('button');assert.equal(buttons.length,1);assert.equal(buttons[0].textContent,'重新授权并继续 GPT 审查');assert.equal(buttons[0].disabled,true);assert.match(h.evaluate('operationScroller').textContent,/资格暂不可用/);}
 assert.equal(h.calls().some(p=>/recover|prepare|commit/.test(p.params.name)),false);h.dispose();
});
test('fresh restored authorization removes the exclusive gate and returns the regular task actions',async()=>{
 const h=await actionablePanel();requireUpperReauthorization(h);assert.equal(h.evaluate('panelUpperRecoveryRequired()'),true);
 h.evaluate("workspace.presentation.taskView.currentReview.stage='RUNNING';delete workspace.presentation.recovery;workspace.presentation.taskView.actions=workspace.presentation.taskView.actions.filter(cap=>cap.action!=='RECOVER');render()");
 assert.equal(h.evaluate('panelUpperRecoveryRequired()'),false);assert.ok(findButton(h,'＋ 发起操作'));assert.ok(findButton(h,'让 GPT 继续审查并提交结果'));assert.equal(findButton(h,'重新授权并继续 GPT 审查'),undefined);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});
function nativePermission(h,revision='revision-a'){
 const q={kind:'RUNTIME_APPROVAL',runtimeRequestId:'native-permission',requestPayloadSHA256:'a'.repeat(64),runtimeEpoch:'epoch',operationKind:'EXECUTION',operationId:'execution-a',taskDefinitionRevisionId:revision,projectId:'project',featureAreaId:'area-a',taskId:'task-a',runId:'run-a',threadId:'thread',turnId:'turn',connectionId:'connection',itemId:'item',requestId:0,state:'PENDING',method:'item/commandExecution/requestApproval',payload:{command:'gofmt -w source.go; go test ./core',cwd:'/workspace',reason:'原始权限请求'},availableResponses:[{decision:'accept'},{decision:{acceptWithExecpolicyAmendment:{execpolicy_amendment:['gofmt','-w']}}}],projection:{lifecycleState:'PENDING',availableActions:[]}};
 h.evaluate(`workspace.presentation.taskView.currentExecution={id:'execution-a',implementationRunId:'run-a'};workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';workspace.pendingDecisions=${JSON.stringify([{kind:'RUNTIME_APPROVAL',projectId:'project',featureAreaId:'area-a',taskId:'task-a',options:null,runtimeRequest:q}])};render()`);return q;
}
test('native command approval with no legacy Approval row requires explicit option and confirmation then exact fresh lookup',async()=>{
 const h=await actionablePanel('IMPLEMENTATION_RUNNING'),q=nativePermission(h);assert.match(h.root.textContent,/gofmt -w source.go/);assert.ok(findButton(h,'允许本次'));assert.ok(findButton(h,'允许并保存命令规则'));assert.equal(h.calls().some(c=>c.params.name==='runtime_request_respond'),false);
 findButton(h,'允许本次').onclick();assert.ok(findButton(h,'确认允许本次'));assert.equal(h.calls().some(c=>c.params.name==='runtime_request_respond'),false);
 const sending=findButton(h,'确认允许本次').onclick();await settle();const read=h.calls().filter(c=>c.params.name==='operation_get').at(-1);assert.ok(read);h.reply(read,{runtimeRequests:[q]});await settle();
 const submit=h.calls().find(c=>c.params.name==='runtime_request_respond');assert.ok(submit);assert.equal(submit.params.arguments.kind,'RUNTIME_APPROVAL');assert.equal(submit.params.arguments.resolution.requestId,0);assert.deepEqual(submit.params.arguments.resolution.response,{decision:'accept'});assert.equal(submit.params.arguments.resolution.payloadSHA256,q.requestPayloadSHA256);assert.equal(submit.params.arguments.approvalId,undefined);
 h.reply(submit,{kind:'RUNTIME_APPROVAL',guarantee:'EXACT_RUNTIME_REQUEST_STATE',runtime:{runId:q.runId}});await sending;assert.equal(h.calls().some(c=>['ui_approval_decide','runtime_request_user_decision_submit'].includes(c.params.name)),false);assert.equal(h.posts.some(c=>c.method==='ui/message'),false);h.dispose();
});
test('native permission revision mismatch has no approval controls and changed request never dispatches',async()=>{
 let h=await actionablePanel('IMPLEMENTATION_RUNNING');nativePermission(h,'foreign-revision');assert.equal(findButton(h,'允许本次'),undefined);h.dispose();
 h=await actionablePanel('IMPLEMENTATION_RUNNING');const q=nativePermission(h);findButton(h,'允许本次').onclick();const sending=findButton(h,'确认允许本次').onclick();await settle();h.reply(h.calls().filter(c=>c.params.name==='operation_get').at(-1),{runtimeRequests:[{...q,requestPayloadSHA256:'b'.repeat(64)}]});await sending;assert.equal(h.calls().some(c=>c.params.name==='runtime_request_respond'),false);h.dispose();
});
