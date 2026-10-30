'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture}=require('./testdata/card_visual_fixture.cjs');
async function mount(mismatch=false){
 const h=boot({manualIdentity:true,hostCapabilities:{message:{text:{}}}}),workspace={...fixture('VERIFICATION_RUNNING'),refreshedAt:new Date().toISOString()},v=workspace.presentation.taskView;
 v.status='ACTIVE';v.terminal=false;v.readOnly=false;v.actionsStatus='AVAILABLE';v.currentExecution={id:'execution-one'};v.details={...v.details,taskDefinitionRevisionId:'revision-one'};
 v.verificationDecisions={taskId:'task-a',executionId:mismatch?'foreign':'execution-one',taskDefinitionRevisionId:'revision-one',choices:[['CONTINUE_TASK','延续任务',true],['COMPLETE_TASK','完成并合入',false],['DEFER_TASK','暂存任务',true]].map(([action,label,available])=>({action,label,available,reason:available?'':'验证尚未通过'}))};
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'decisions-entry',entryIdentity:'decisions-entry',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace});h.init();await settle();return h;
}
const choice=(h,action)=>h.root.querySelectorAll('button').find(b=>b.getAttribute('data-verification-choice')===action);
test('verification choices are click-only, exact-target, and never commit from the read-only component',async()=>{
 const h=await mount();assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);
 assert.equal(choice(h,'CONTINUE_TASK').disabled,false);assert.equal(choice(h,'COMPLETE_TASK').disabled,true);assert.equal(choice(h,'DEFER_TASK').disabled,false);
 const click=choice(h,'CONTINUE_TASK').onclick(),post=h.posts.find(p=>p.method==='ui/message');assert.ok(post);
 assert.match(post.params.content[0].text,/execution-one/);assert.match(post.params.content[0].text,/revision-one/);assert.match(post.params.content[0].text,/execution_prepare\(action=CONTINUE_TASK\)/);assert.match(post.params.content[0].text,/不调用 confirmation_commit/);
 h.hostReply(post,{});await click;assert.equal(h.calls().some(p=>/commit|start|submit/.test(p.params.name)),false);h.dispose();
});
test('foreign execution identity disables all verification choices',async()=>{const h=await mount(true);for(const action of ['CONTINUE_TASK','COMPLETE_TASK','DEFER_TASK'])assert.equal(choice(h,action).disabled,true);assert.equal(h.posts.filter(p=>p.method==='ui/message').length,0);h.dispose();});

test('recorded continuation unlocks a new preparation request without repeating the old confirmation',async()=>{
 const h=await mount();const first=choice(h,'CONTINUE_TASK').onclick(),post=h.posts.find(p=>p.method==='ui/message');h.hostReply(post,{});await first;assert.equal(choice(h,'CONTINUE_TASK').disabled,true);
 h.evaluate('workspace.presentation.taskView.verificationDecisions.continuationConfirmed=true;render()');assert.equal(choice(h,'CONTINUE_TASK').disabled,false);assert.equal(choice(h,'CONTINUE_TASK').textContent,'准备后续方案');assert.match(h.root.textContent,/已确认延续原任务/);
 const next=choice(h,'CONTINUE_TASK').onclick(),messages=h.posts.filter(p=>p.method==='ui/message');assert.equal(messages.length,2);assert.match(messages[1].params.content[0].text,/无需再次准备 CONTINUE_TASK/);assert.match(messages[1].params.content[0].text,/REQUEST_TASK_REVIEW/);h.hostReply(messages[1],{});await next;h.dispose();
});
