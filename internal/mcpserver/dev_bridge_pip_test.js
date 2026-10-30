'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture}=require('./testdata/card_visual_fixture.cjs');

async function mount(options={},state='REVIEW_APPROVAL_REQUIRED'){
 const h=boot({manualIdentity:true,...options}),w={...fixture(state),refreshedAt:new Date().toISOString()};
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'pip-entry',entryIdentity:'pip-entry',display:options.display||'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:w});h.init();await settle();return h;
}
const control=(h,label)=>h.root.querySelectorAll('button').find(b=>b.textContent===label);
const request=h=>h.posts.filter(p=>p.method==='ui/request-display-mode').at(-1);
const modes={availableDisplayModes:['inline','pip','fullscreen']};
function assertNoWrites(h){assert.equal(h.calls().every(p=>['dev_bridge_card_get','workspace_get','operation_get','session_binding_get'].includes(p.params.name)),true);}

test('PiP is declared, but unsupported hosts retain the ordinary inline Card',async()=>{
 const h=await mount({hostContext:{availableDisplayModes:['inline']}});assert.deepEqual(Array.from(h.initializePost.params.appCapabilities.availableDisplayModes),['inline','pip','fullscreen']);
 assert.equal(control(h,'悬浮查看'),undefined);assert.match(h.root.textContent,/DEV BRIDGE/);assertNoWrites(h);h.dispose();
});
test('undeclared display capabilities are probed only by an explicit user click',async()=>{
 const h=await mount();assert.equal(request(h),undefined);const switching=control(h,'悬浮查看').onclick();assert.equal(request(h).params.mode,'pip');h.reject(request(h));await switching;
 assert.equal(h.root.getAttribute('data-display-mode')||'inline','inline');assert.match(h.root.textContent,/暂时无法切换/);assertNoWrites(h);h.dispose();
});
test('standard host switches the same exact Task and returns inline without a business call',async()=>{
 const h=await mount({hostContext:modes}),before=h.calls().length;
 const switching=control(h,'悬浮查看').onclick();assert.equal(request(h).params.mode,'pip');assert.equal(h.root.getAttribute('data-display-mode'),'inline');
 h.hostReply(request(h),{mode:'pip'});await switching;assert.match(h.root.textContent,/任务进度/);assert.match(h.root.textContent,/等待/);assert.ok(control(h,'返回对话'));assert.equal(h.calls().length,before);
 assert.equal(h.evaluate('taskID'),'task-a');assert.equal(h.evaluate('sessionID'),'session');assert.equal(h.polls().length,1,'waiting approval remains observable in PiP');
 const back=control(h,'返回对话').onclick();h.hostReply(request(h),{mode:'inline'});await back;assert.match(h.root.textContent,/DEV BRIDGE/);assert.equal(h.polls().length,0);assertNoWrites(h);h.dispose();
});
test('OpenAI compatibility bridge supports PiP and the mobile fullscreen grant',async()=>{
 const calls=[],h=await mount({openai:{requestDisplayMode:async({mode})=>{calls.push(mode);return {mode:mode==='pip'?'fullscreen':'inline'};}}});
 await control(h,'悬浮查看').onclick();assert.equal(h.root.getAttribute('data-display-mode'),'fullscreen');assert.ok(control(h,'返回对话'));
 await control(h,'返回对话').onclick();assert.deepEqual(calls,['pip','inline']);assertNoWrites(h);h.dispose();
});
test('denial, failure and timeout never pretend PiP was enabled',async()=>{
 for(const outcome of ['denied','error','timeout']){
  const h=await mount({hostContext:modes});const switching=control(h,'悬浮查看').onclick();
  if(outcome==='denied')h.hostReply(request(h),{mode:'inline'});else if(outcome==='error')h.reject(request(h));else h.timeout(request(h));
  await switching;assert.equal(h.root.getAttribute('data-display-mode')||'inline','inline');assert.match(h.root.textContent,/继续|当前卡片/);assert.ok(control(h,'悬浮查看'));assertNoWrites(h);h.dispose();
 }
});
test('host context and globals dismissal restore the original collapsed view',async()=>{
 const h=await mount({hostContext:{...modes,displayMode:'pip'},display:'COLLAPSED'});assert.match(h.root.textContent,/任务进度/);
 assert.equal(h.polls().length,1,'PiP from a collapsed entry still monitors pending approvals');
 h.send({jsonrpc:'2.0',method:'ui/notifications/host-context-changed',params:{displayMode:'inline'}});await settle();assert.match(h.root.textContent,/Dev Bridge · 只读/);
 h.event('openai:set_globals',{detail:{globals:{displayMode:'pip'}}});assert.match(h.root.textContent,/任务进度/);
 h.event('openai:set_globals',{detail:{globals:{displayMode:'inline'}}});assert.match(h.root.textContent,/Dev Bridge · 只读/);assertNoWrites(h);h.dispose();
});
test('a pending OpenAI display request is bounded and canceled on teardown',async()=>{
 const h=await mount({openai:{requestDisplayMode:()=>new Promise(()=>{})}}),switching=control(h,'悬浮查看').onclick();
 await settle();h.dispose();await switching;assert.equal(h.evaluate('disposed'),true);assert.equal(h.polls().length,0);
});
test('Task success, failure and cancellation keep the user-selected tab or PiP until explicit return',async()=>{
 for(const mode of ['pip','fullscreen'])for(const status of ['SUCCEEDED','FAILED','CANCELED']){
  const h=await mount({hostContext:{...modes,displayMode:mode}});assert.equal(request(h),undefined);
  h.evaluate("workspace.presentation.taskView.status='"+status+"';workspace.presentation.taskView.phase='DELIVERING';render()");await settle();
  assert.ok(h.root.textContent.includes(({SUCCEEDED:'已完成',FAILED:'已失败',CANCELED:'已取消'})[status]));assert.doesNotMatch(h.root.textContent,/交付中/);assert.equal(request(h),undefined);assert.equal(h.root.getAttribute('data-display-mode'),mode);
  assert.ok(control(h,'返回对话'));const back=control(h,'返回对话').onclick();assert.equal(request(h).params.mode,'inline');h.hostReply(request(h),{mode:'inline'});await back;assertNoWrites(h);h.dispose();
 }
});

test('hidden and disposed Cards stop PiP polling and ignore late display responses',async()=>{
 const h=await mount({hostContext:{...modes,displayMode:'pip'}});assert.equal(h.polls().length,1);h.visibility(true);assert.equal(h.polls().length,0);
 const switching=control(h,'返回对话').onclick(),last=request(h);h.dispose();await switching;h.hostReply(last,{mode:'inline'});assert.equal(h.polls().length,0);assertNoWrites(h);
});
