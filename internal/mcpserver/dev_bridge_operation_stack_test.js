'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
async function mount(change=()=>{},singlePanel=false){
 const workspace={...fixture('REVIEW_APPROVAL_REQUIRED'),refreshedAt:new Date().toISOString()};
 workspace.confirmationFeed={aiSessionId:'session',projectId:'project',taskId:'task-a',availability:'AVAILABLE',truncated:false,items:[{aiSessionId:'session',intentId:'next-confirmation',version:1,contentHash:'a'.repeat(64),purpose:'EXECUTION',action:'CONTINUE_TASK',state:'PENDING'},{aiSessionId:'session',intentId:'old-confirmation',version:1,contentHash:'b'.repeat(64),purpose:'EXECUTION',action:'APPROVE_AND_START',state:'COMPLETED'}]};change(workspace.confirmationFeed,workspace);
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip','fullscreen']}}),binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'stack-entry',entryIdentity:'stack-entry',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace,singlePanel});h.init();await settle();
 if(singlePanel){h.reply(h.calls().find(p=>p.params.name==='dev_bridge_panel_get'),{...workspace.confirmationFeed,scope:'SESSION'});await settle();}
 return h;
}
const rows=h=>h.root.querySelectorAll('details').filter(r=>r.getAttribute('data-confirmation-intent'));
const tab=(h,name)=>h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')===name);
test('progress stays above separate next and history tabs; switching never submits',async()=>{
 const h=await mount();assert.match(h.root.textContent,/任务进度/);assert.match(h.root.textContent,/后续操作/);assert.match(h.root.textContent,/历史操作/);assert.match(h.root.textContent,/延续任务/);assert.doesNotMatch(h.root.textContent,/确认已完成/);
 assert.equal(rows(h).length,1);assert.equal(rows(h)[0].open,true);assert.equal(tab(h,'next').getAttribute('aria-selected'),'true');
 tab(h,'history').onclick();assert.equal(rows(h).length,1);assert.equal(rows(h)[0].open,false);assert.match(rows(h)[0].textContent,/确认已完成/);
 tab(h,'next').onclick();assert.equal(rows(h)[0].open,true);
 assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);assert.equal(h.calls().some(p=>/commit|start|prepare/.test(p.params.name)),false);h.dispose();
});
test('a feed from another session, project or task cannot mount formal controls',async()=>{
 for(const field of ['aiSessionId','projectId','taskId']){const h=await mount(feed=>feed[field]='foreign');assert.equal(h.root.querySelectorAll('details').filter(r=>r.getAttribute('data-confirmation-intent')).length,0);assert.equal(h.calls().some(p=>p.params.name==='confirmation_commit'),false);h.dispose();}
});
test('a reused identity or malformed content hash makes the entire list unavailable',async()=>{
 for(const change of [feed=>feed.items.push({...feed.items[0]}),feed=>feed.items[0].contentHash='stale']){const h=await mount(change);assert.equal(h.root.querySelectorAll('details').filter(r=>r.getAttribute('data-confirmation-intent')).length,0);assert.match(h.root.textContent,/不可核对/);h.dispose();}
});

test('formal input and tool-result notifications do not replace the persistent progress entry',async()=>{
 const h=await mount();h.input({aiSessionId:'session',intentId:'next-confirmation',version:1,purpose:'EXECUTION',contentHash:'a'.repeat(64)});h.result({intent:{aiSessionId:'session',intentId:'foreign-confirmation'}});await settle();assert.match(h.root.textContent,/任务进度/);assert.match(h.root.textContent,/后续操作/);assert.equal(h.evaluate('taskID'),'task-a');assert.equal(h.posts.some(p=>p.method==='ui/request-display-mode'),false);h.dispose();
});

test('completed and stale operations live only in history and start collapsed',async()=>{const h=await mount(feed=>{feed.items[0].state='STALE';});assert.equal(rows(h).length,0);tab(h,'history').onclick();assert.equal(rows(h).length,2);assert.ok(rows(h).every(r=>!r.open));assert.match(h.root.textContent,/目标已变化/);h.dispose();});

test('a new pending confirmation opens before an older partial receipt without changing either state',async()=>{
 const h=await mount(feed=>{feed.items[1].state='PARTIAL';feed.items.reverse();});
 assert.equal(rows(h)[0].getAttribute('data-confirmation-intent'),'next-confirmation');assert.equal(rows(h)[0].open,true);assert.equal(rows(h).length,2);assert.match(rows(h)[1].textContent,/部分步骤待处理/);
 tab(h,'history').onclick();assert.equal(rows(h).length,0);
 assert.equal(h.calls().some(p=>/commit|start|prepare/.test(p.params.name)),false);h.dispose();
});

test('a new pending item stays discoverable while the user keeps an old receipt open; showing it never submits',async()=>{
 const h=await mount(feed=>{feed.items[0].state='PARTIAL';});
 const old=rows(h).find(r=>r.getAttribute('data-confirmation-intent')==='next-confirmation');old.open=true;old.ontoggle();
 h.evaluate(`workspace.confirmationFeed.items.unshift({aiSessionId:'session',intentId:'new-start',version:1,contentHash:'c'.repeat(64),purpose:'REVIEW',action:'START_REVIEW',state:'PENDING'});render()`);
 assert.equal(rows(h).find(r=>r.getAttribute('data-confirmation-intent')==='next-confirmation').open,true);
 const reveal=h.root.querySelectorAll('button').find(b=>b.getAttribute('data-show-pending-confirmation')==='new-start');assert.ok(reveal);
 reveal.onclick();assert.equal(rows(h)[0].getAttribute('data-confirmation-intent'),'new-start');assert.equal(rows(h)[0].open,true);assert.equal(rows(h).length,2);assert.equal(rows(h)[1].open,false);
 tab(h,'history').onclick();assert.ok(rows(h).every(r=>r.getAttribute('data-confirmation-intent')!=='next-confirmation'));
 assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);assert.equal(h.calls().some(p=>/commit|start|prepare/.test(p.params.name)),false);h.dispose();
});

test('a finished confirmation moves to history without replay and tab keys work',async()=>{
 const h=await mount();h.evaluate("workspace.confirmationFeed.items[0].state='COMPLETED';render()");assert.equal(rows(h).length,0);
 tab(h,'next').onkeydown({key:'ArrowRight',preventDefault(){}});assert.equal(tab(h,'history').getAttribute('aria-selected'),'true');assert.equal(rows(h).length,2);assert.ok(rows(h).every(r=>!r.open));
 assert.equal(h.calls().some(p=>/commit|start|prepare/.test(p.params.name)),false);h.dispose();
});

test('an exact local uncertain submission changes only the pending row display, retaining Core state',async()=>{
 const h=await mount();
 h.evaluate("{const item=workspace.confirmationFeed.items[0];formalStates.set(formalKey(item),{outcomeUnknown:true,pendingRequest:{aiSessionId:item.aiSessionId,intentId:item.intentId,version:item.version,purpose:item.purpose,contentHash:item.contentHash}});render()}");
 assert.match(rows(h)[0].textContent,/结果待核对/);assert.equal(h.evaluate('workspace.confirmationFeed.items[0].state'),'PENDING');
 h.evaluate("workspace.confirmationFeed.items[0].state='COMPLETED';render()");tab(h,'history').onclick();assert.match(rows(h).find(r=>r.getAttribute('data-confirmation-intent')==='next-confirmation').textContent,/确认已完成/);h.dispose();
});


test('Task current node keeps the original confirmation but hides independent action branches',async()=>{
 const h=await mount((feed,w)=>{w.presentation.taskView.actionNode={taskId:'task-a',id:'task-a:8',generation:8,state:'UNKNOWN',blocking:true,owned:true,intentId:'next-confirmation',reason:'请先核对原操作'};});
 assert.match(h.root.textContent,/请先核对原操作/);assert.equal(rows(h).length,1);
 assert.equal(h.root.querySelectorAll('button').filter(b=>b.getAttribute('data-verification-choice')).length,0);
 assert.equal(h.root.querySelectorAll('button').some(b=>b.textContent==='要求修改方案'||b.textContent==='批准并开始实施'),false);
 assert.equal(h.calls().some(p=>/commit|start|prepare|conflict_resolve/.test(p.params.name)),false);h.dispose();
});

test('legacy conflict exposes only explicit Core-qualified choices without automatically selecting one',async()=>{
 const h=await mount((feed,w)=>{w.presentation.taskView.actionNode={taskId:'task-a',id:'task-a:9',generation:9,state:'CONFLICT',blocking:true,reason:'需要核对历史链路',candidates:[{intentId:'next-confirmation',version:1,purpose:'EXECUTION',contentHash:'a'.repeat(64),action:'CONTINUE_TASK',selectable:true}]};});
 assert.match(h.root.textContent,/保留此链路并关闭其他未提交提案/);
 assert.equal(h.calls().some(p=>p.params.name==='task_action_conflict_resolve'),false);h.dispose();
});

async function conflictPanel(){
 return mount((feed,w)=>{w.presentation.taskView.actionsStatus='AVAILABLE';w.presentation.taskView.actionNode={taskId:'task-a',id:'task-a:9',generation:9,state:'CONFLICT',blocking:true,reason:'需要核对历史链路',candidates:[{intentId:'next-confirmation',version:1,purpose:'EXECUTION',contentHash:'a'.repeat(64),action:'CONTINUE_TASK',selectable:true,consumed:false}]};},true);
}
const conflictButton=(h,close=false)=>h.root.querySelectorAll('button').find(b=>close?b.textContent==='关闭此未提交提案':b.textContent.startsWith('保留此链路并关闭其他未提交提案'));
test('conflict buttons dispatch exact app-private writes only after explicit clicks and never execute business commands',async()=>{
 for(const close of [false,true]){
  const h=await conflictPanel();assert.equal(h.calls().some(p=>/conflict_resolve|confirmation_discard/.test(p.params.name)),false);
  const from=h.calls().length,click=conflictButton(h,close).onclick();await settle();
  const mutation=h.calls().slice(from).find(p=>p.params.name===(close?'confirmation_discard':'task_action_conflict_resolve'));assert.ok(mutation);
  assert.deepEqual(mutation.params.arguments,{aiSessionId:'session',...(close?{}:{taskId:'task-a',generation:9}),intentId:'next-confirmation',version:1,purpose:'EXECUTION',contentHash:'a'.repeat(64)});
  h.reply(mutation,{intent:{}},{isError:true});await click;
  assert.match(h.root.textContent,/Tool call failed/);
  assert.equal(h.calls().slice(from).some(p=>/commit|start|prepare/.test(p.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
 }
});
test('old conflict controls cannot write after generation, candidate, navigation or read authority changes',async()=>{
 for(const change of ["workspace.presentation.taskView.actionNode.generation++","workspace.presentation.taskView.actionNode.candidates[0].contentHash='b'.repeat(64)","workspace.presentation.taskView.actionNode.candidates[0].selectable=false","workspace.presentation.taskView.readOnly=true","panelFeedError='offline'","setIdentity(sessionID,featureAreaID,'other-task',true)"]){
  const h=await conflictPanel(),old=conflictButton(h),from=h.calls().length;h.evaluate(change);await old.onclick();
  assert.equal(h.calls().slice(from).some(p=>p.params.name==='task_action_conflict_resolve'),false,change);h.dispose();
 }
});
test('conflict recovery remains available when ordinary business capabilities cannot be read',async()=>{
 const h=await conflictPanel();h.evaluate("workspace.presentation.taskView.actionsStatus='UNAVAILABLE';render()");
 const click=conflictButton(h).onclick();await settle();const call=h.calls().find(p=>p.params.name==='task_action_conflict_resolve');assert.ok(call);
 h.reply(call,{}, {isError:true});await click;h.dispose();
});
test('private task-chain writes stay denied without the explicit click path',async()=>{
 const h=await conflictPanel();
 for(const name of ['task_action_conflict_resolve','confirmation_discard']){
  h.context.testTool=name;
  assert.match(await h.evaluate("request(testTool,{aiSessionId:sessionID}).catch(e=>e.message)"),/Read-only Card request denied/);
  assert.match(await h.evaluate("call(testTool,{aiSessionId:sessionID}).catch(e=>e.message)"),/read-only Card/);
 }
 assert.equal(h.calls().some(p=>/conflict_resolve|confirmation_discard/.test(p.params.name)),false);h.dispose();
});

test('a canceled admission exposes only its exact Core-authorized stop-and-revise branch',async()=>{
 const h=await mount((feed,w)=>{const v=w.presentation.taskView;v.actionsStatus='AVAILABLE';v.actionNode={taskId:'task-a',id:'task-a:12',generation:12,kind:'NO_RUN_RECOVERY',state:'PARTIAL',blocking:true,reason:'原执行已在派发前取消'};v.actions=[{action:'STOP_AND_REVISE',available:true,target:{taskId:'task-a',featureAreaId:'area-a',taskDefinitionRevisionId:'revision-a',taskActionGeneration:12}}];},true);
 assert.match(h.root.textContent,/原执行已在派发前取消/);
 assert.equal(h.root.querySelectorAll('button').filter(b=>b.getAttribute('data-panel-capability')==='STOP_AND_REVISE').length,1);
 assert.equal(h.root.querySelectorAll('button').some(b=>b.getAttribute('data-verification-choice')),false);
 assert.equal(h.calls().some(p=>/commit|start|prepare|recover/.test(p.params.name)),false);h.dispose();
});
