'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture}=require('./testdata/card_visual_fixture.cjs');
const intent={aiSessionId:'session',intentId:'audit-confirmation',version:1,contentHash:'a'.repeat(64),purpose:'REVIEW',action:'START_REVIEW',state:'PENDING',taskId:'task-a'};
const feed=items=>({scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items});
async function panel(items=[intent]){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']}});
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'audit',entryIdentity:'audit',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()},singlePanel:true});h.init();await settle();
 h.reply(h.calls().find(p=>p.params.name==='dev_bridge_panel_get'),feed(items));await settle();return h;
}
function row(h){return h.root.querySelectorAll('details').filter(n=>n.getAttribute('data-confirmation-intent'));}
function installMount(h){const mounted=[];h.context.window.DevBridgeConfirmationStack={mount(host,ref,hooks){const m={host,ref,hooks,disposed:false};mounted.push(m);return {dispose(){m.disposed=true;}};}};h.evaluate('for(const record of formalCards.values())record.host.isConnected=true;mountFormalCards()');return mounted;}
test('feed failure preserves the mounted confirmation and disables it until an exact successful read',async()=>{
 const h=await panel(),mounted=installMount(h);assert.equal(row(h).length,1);const before=h.calls().length;h.expire(5000);await settle();
 for(const call of h.calls().slice(before)){
  if(call.params.name==='dev_bridge_panel_get')h.send({jsonrpc:'2.0',id:call.id,error:{code:-32000,message:'temporary host read failure'}});
  else if(call.params.name==='dev_bridge_card_get')h.reply(call,JSON.parse(h.evaluate('JSON.stringify(workspace)')));
 }
 await settle();assert.ok(h.evaluate('panelFeed'));assert.equal(row(h).length,1);assert.equal(mounted[0].disposed,false);assert.equal(mounted[0].hooks.canInteract(),false);const read=h.evaluate('refreshPanelFeed()');await settle();h.reply(h.calls().filter(c=>c.params.name==='dev_bridge_panel_get').at(-1),feed([intent]));await read;assert.equal(mounted[0].hooks.canInteract(),true);assert.equal(mounted[0].disposed,false);assert.doesNotMatch(h.root.textContent,/操作列表读取失败/);h.dispose();
});
test('partial and unknown confirmations stay actionable for exact readback instead of read-only history',async()=>{
 for(const state of ['PARTIAL','UNKNOWN']){const h=await panel([{...intent,state}]);assert.equal(h.evaluate('currentFormalFeed().items.filter(isNextOperation).length'),1);h.evaluate("formalOpen=formalKey(panelFeed.items[0]);render()");const mounted=installMount(h);assert.equal(row(h).length,1);assert.equal(mounted[0].hooks.readOnly,false);assert.equal(mounted[0].hooks.canInteract(),true);h.dispose();}
});
test('a requested creation proposal survives restoring host display state; other proposals need explicit selection',async()=>{
 const item={...intent,taskId:'',purpose:'TASK',action:'CREATE_TASK_AND_REVIEW',requestId:'create-from-current-panel'};const h=await panel([item]);
 h.evaluate("panelProposalRequest={scope:panelScope(),requestId:'create-from-current-panel'};save();panelProposalRequest=null;restorePanelPresentation(widgetState());render()");
 assert.equal(h.evaluate('currentFormalFeed().items.filter(isNextOperation).length'),1);h.evaluate("panelProposalRequest=null;render()");assert.equal(row(h).length,0);
 const choose=h.root.querySelectorAll('button').find(b=>b.textContent.startsWith('查看：'));assert.ok(choose);choose.onclick();assert.equal(row(h).length,1);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);assert.equal(h.calls().some(c=>/commit|prepare/.test(c.params.name)),false);h.dispose();
});
function setupReview(h){h.evaluate("workspace.presentation.taskView.actionsStatus='AVAILABLE';workspace.presentation.taskView.details.taskDefinitionRevisionId='revision-a';workspace.presentation.taskView.phase='REVIEWING';workspace.presentation.taskView.currentReview={id:'review-successor',taskId:'task-a',executorKind:'UPPER_LLM',stage:'PREPARING',run:{id:'run-successor',transportStatus:'RUNNING'}};render()");}
function event(h,kind){const reference={aiSessionId:'session',intentId:'audit-revise',version:1,purpose:'REVIEW',contentHash:'b'.repeat(64)};h.context.auditRecord={reference,scope:h.evaluate('formalScope'),readOnly:false};h.context.auditEvent={reference,result:{structuredContent:{intent:{...reference,projectId:'project',action:'REVISE',content:{review:{featureAreaId:'area-a'}},state:'COMPLETED',submission:{recorded:true,outcome:'CONSUMED'},commit:{requestId:'user-revise-click'},steps:[{kind,state:'COMPLETED',featureAreaId:'area-a',taskId:'task-a',reviewId:'review-successor',runId:'run-successor'}]}}}};}
async function replyReads(h,from,fail=false){for(const c of h.calls().slice(from)){if(c.params.name==='dev_bridge_card_get'){if(fail)h.send({jsonrpc:'2.0',id:c.id,error:{code:-32000,message:'temporary read failure'}});else h.reply(c,JSON.parse(h.evaluate('JSON.stringify(workspace)')));}else if(c.params.name==='workspace_get')h.reply(c,{branches:{repositories:[]}});else if(c.params.name==='dev_bridge_panel_get')h.reply(c,feed([]));}await settle();}
test('a completed REVISE receipt continues its exact successor GPT review',async()=>{
 const h=await panel([]);setupReview(h);event(h,'RESOLVE_REVIEW_DECISION');const from=h.calls().length,pending=h.evaluate('panelAfterConfirmation(auditRecord,auditEvent)');await settle();await replyReads(h,from);await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);assert.match(message.params.content[0].text,/run-successor/);h.hostReply(message,{});await pending;assert.ok(h.evaluate('currentReviewContinuationContext()'));h.dispose();
});
test('a confirmed review handoff survives a failed readback and continues once after the next successful read',async()=>{
 const h=await panel([]);setupReview(h);event(h,'START_REVIEW');let from=h.calls().length;const pending=h.evaluate('panelAfterConfirmation(auditRecord,auditEvent)');await settle();await replyReads(h,from,true);await pending;assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);
 from=h.calls().length;const refresh=h.evaluate('refresh({mode:REFRESH_MODE.INTERACTIVE})');await settle();await replyReads(h,from);await refresh;const drain=h.evaluate('panelDrainHandoffs()');await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);h.hostReply(message,{});await drain;await h.evaluate('panelDrainHandoffs()');assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);h.dispose();
});

test('queued host-restored handoff must revalidate its exact consumed receipt and never resends an uncertain send',async()=>{
 const h=await panel([]);setupReview(h);event(h,'START_REVIEW');let from=h.calls().length;const first=h.evaluate('panelAfterConfirmation(auditRecord,auditEvent)');await settle();await replyReads(h,from,true);await first;
 const job=JSON.parse(h.evaluate('JSON.stringify([...panelPendingHandoffs.values()][0])'));assert.equal(job.state,'QUEUED');
 h.context.restoredJob=job;h.evaluate('panelPendingHandoffs.clear();restoredJob.validated=false;panelPendingHandoffs.set(restoredJob.key,restoredJob)');
 from=h.calls().length;const read=h.evaluate('refresh({mode:REFRESH_MODE.INTERACTIVE})');await settle();await replyReads(h,from);await read;
 const drain=h.evaluate('panelDrainHandoffs()');await settle();const receiptRead=h.calls().filter(c=>c.params.name==='confirmation_get').at(-1);assert.ok(receiptRead);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.reply(receiptRead,h.context.auditEvent.result.structuredContent);await settle();const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);h.timeout(message);await drain;
 h.evaluate('for(const job of panelPendingHandoffs.values())job.validated=false');await h.evaluate('panelDrainHandoffs()');assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);h.dispose();
});

test('stale current Review cannot consume a queued authorized handoff',async()=>{
 const h=await panel([]);setupReview(h);event(h,'START_REVIEW');const from=h.calls().length,pending=h.evaluate('panelAfterConfirmation(auditRecord,auditEvent)');await settle();await replyReads(h,from,true);await pending;
 h.evaluate("workspace.presentation.taskView.currentReview.id='replacement-review';authorityFresh=true");await h.evaluate('panelDrainHandoffs()');assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

test('host restore never presents an interrupted send as still sending or retries recovery from display state',async()=>{
 const h=await panel([]);setupReview(h);h.context.savedPresentation={singlePanel:true,handoffs:[{key:'interrupted-send',state:'SENDING',reference:{aiSessionId:'session'}},{key:'recovery-without-receipt',state:'QUEUED'}]};
 // Recovery jobs have no formal receipt and are intentionally not auto-restored.
 h.evaluate("restorePanelPresentation({aiSessionId:'session',panelPresentation:savedPresentation})");
 assert.equal(h.evaluate("panelPendingHandoffs.get('interrupted-send').state"),'UNKNOWN');assert.equal(h.evaluate("panelPendingHandoffs.has('recovery-without-receipt')"),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

test('new implementation targets get independent prepare IDs and exact retries survive host restore',async()=>{
 const h=await panel([]);
 h.evaluate("workspace.presentation.taskView.phase='IMPLEMENTATION_READY';workspace.presentation.taskView.verificationDecisions=null;workspace.presentation.taskView.actionsStatus='AVAILABLE';workspace.presentation.taskView.details.taskDefinitionRevisionId='same-revision';workspace.presentation.taskView.actions=[{action:'RETRY_IMPLEMENTATION',available:true,target:{taskId:'task-a',featureAreaId:'area-a',taskDefinitionRevisionId:'same-revision',executionId:'execution-1',decisionId:'acceptance-1'}}];render()");
 assert.ok(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-panel-capability')==='RETRY_IMPLEMENTATION'));
 const read=()=>h.evaluate("panelRequestContext('RETRY_IMPLEMENTATION','按原方案继续实施',workspace.presentation.taskView.actions[0])");
 const first=read();assert.ok(first);
 h.evaluate('save();panelPrepareRequests.clear();restorePanelPresentation(widgetState())');
 assert.equal(read().prepareRequestId,first.prepareRequestId);
 h.evaluate("workspace.presentation.taskView.actions[0].target.executionId='execution-2';workspace.presentation.taskView.actions[0].target.decisionId='acceptance-2'");
 const second=read();assert.notEqual(second.key,first.key);assert.notEqual(second.prepareRequestId,first.prepareRequestId);
 assert.equal(read().prepareRequestId,second.prepareRequestId);
 h.dispose();
});
