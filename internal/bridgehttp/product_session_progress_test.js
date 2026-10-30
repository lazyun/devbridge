"use strict";
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,'utf8');
const esc=value=>String(value??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function harness(snapshot){
 const instrumented=source.replace('return {mount, stop, refresh, refreshList, select, selectExactRun: select};',`return {progress,conversation,setup(){snapshot=__snapshot;runID='run';project='project';online=true;streamLive=true;}};`);
 const context={window:{DevBridgeComponents:{esc},DevBridgeStatus:{}},__snapshot:snapshot,URLSearchParams,Date,console};vm.createContext(context);vm.runInContext(instrumented,context);
 const api=context.window.DevBridgeSessionWorkbench,controller=api.create({api:()=>{throw Error('render must never read or write');},t:x=>x});controller.setup();return {summary:api.progressSummary,controller};
}
const now=Date.parse('2026-10-23T04:00:00Z');
function snapshot(){return {detail:{run:{id:'run',status:'RUNNING',started_at:'2026-10-23T03:00:00Z'}},session:{runtime:{availability:'AVAILABLE',connectionState:'CONNECTED',sequence:10,turnStatus:'inProgress',lastActivityAt:'2026-10-23T03:59:50Z',items:[],requests:[]}},items:{items:[]}};}
test('native step counts are observations, never completion or a task percentage',()=>{
 const value=snapshot(),{summary}=harness(value);value.session.runtime.latestPlan={steps:[{step:'one',status:'completed'},{step:'two',status:'completed'}]};
 const model=summary(value,now);assert.equal(model.completed,2);assert.equal(model.ended,false);assert.equal(model.headline,'等待下一项活动');assert.equal(model.elapsed,3600000);assert.equal(model.quiet,false);
 const html=harness(value).controller.progress();assert.match(html,/2 \/ 2 步/);assert.doesNotMatch(html,/%|本次运行已结束/);
});
test('active units, pending user requests and stale connection flags stay distinct',()=>{
 const value=snapshot(),rt=value.session.runtime;rt.items=[{id:'active',type:'commandExecution',status:'inProgress',metadata:{command:'go test ./...'}},{id:'last',type:'agentMessage',status:'completed',text:'Update'}];rt.currentItem=rt.items[1];
 const {summary}=harness(value);assert.equal(summary(value,now).current.id,'active');assert.equal(summary(value,now).headline,'执行命令');
 rt.nativeThreadStatus={type:'active',activeFlags:['waitingOnApproval']};assert.equal(summary(value,now).headline,'等待运行授权');
 rt.requests=[{projection:{lifecycleState:'WAITING_FOR_USER'}}];assert.equal(summary(value,now).headline,'需要你处理运行请求');
 rt.connectionState='DISCONNECTED';const stale=summary(value,now);assert.equal(stale.current,null);assert.equal(stale.waiting,0);assert.equal(stale.headline,'等待活动记录');
});
test('Core terminal outcome overrides old active items, waiting flags and elapsed clocks',()=>{
 const value=snapshot(),rt=value.session.runtime;value.detail.run.status='SUCCEEDED';value.detail.run.duration_ms=1234;rt.nativeThreadStatus={type:'active',activeFlags:['waitingOnApproval']};rt.items=[{id:'old',type:'commandExecution',status:'inProgress'}];
 const model=harness(value).summary(value,now);assert.equal(model.ended,true);assert.equal(model.active,false);assert.equal(model.current,null);assert.equal(model.elapsed,1234);assert.equal(model.headline,'本次运行已结束');assert.equal(harness(value).summary(value,now+3600000).elapsed,1234);
});
test('GPT review is identified by explicit executor provenance, never a missing Thread',()=>{
 const value=snapshot();value.detail.run.executor_kind='UPPER_LLM';delete value.session.runtime;value.detail.review_evidence_status='AVAILABLE';value.detail.review_evidence={successful_operations:3,last_observed_at:"2026-10-23T03:59:00Z"};
 const {controller,summary}=harness(value);assert.equal(summary(value,now).upper,true);assert.match(controller.progress(),/等待 GPT 提交审查报告|在原 GPT 对话中/);assert.match(controller.progress(),/已记录文件操作/);assert.match(controller.progress(),/最后读取/);assert.equal(summary(value,now).lastActivity,Date.parse(value.detail.review_evidence.last_observed_at));assert.doesNotMatch(controller.progress(),/Codex 发布|未记录外部会话/);
 delete value.detail.run.executor_kind;assert.equal(summary(value,now).upper,false);
});
test('progress contains only a bounded escaped update; transcript does not duplicate results, plans or empty reasoning',()=>{
 const value=snapshot();value.detail.native_output='NATIVE_RESULT';value.detail.analysis={report:{summary:'ANALYSIS_RESULT'}};value.items.items=[{ordinal:1,item:{id:'empty',type:'reasoning',status:'completed'}},{ordinal:2,item:{id:'msg',type:'agentMessage',text:'<img src=x onerror=bad>'+'.'.repeat(900),status:'completed'}},{ordinal:3,item:{id:'cmd',type:'commandExecution',status:'completed',metadata:{command:'go test ./...',exitCode:0},outputTail:'EXACT_OUTPUT'}}];
 const {controller}=harness(value),progress=controller.progress(),conversation=controller.conversation();assert.doesNotMatch(progress,/<img|NATIVE_RESULT|ANALYSIS_RESULT|EXACT_OUTPUT/);assert.match(progress,/&lt;img/);assert.match(progress,/更多内容见会话记录/);
 assert.doesNotMatch(conversation,/NATIVE_RESULT|ANALYSIS_RESULT|暂不支持|APP_SERVER|data-session-item="empty"/);assert.match(conversation,/EXACT_OUTPUT/);assert.match(conversation,/<details><summary>/);assert.doesNotMatch(conversation,/<details open/);
});
test('zero and unavailable timestamps never produce a fabricated activity age',()=>{
 const value=snapshot();value.session.runtime.sequence=0;value.session.runtime.lastActivityAt='0001-01-01T00:00:00Z';assert.equal(harness(value).summary(value,now).lastActivity,null);
 value.session.runtime.sequence=1;value.session.runtime.lastActivityAt='not-a-date';assert.equal(harness(value).summary(value,now).lastActivity,null);
});

test('GPT review cannot inherit stale Codex live activity or waiting flags',()=>{
 const value=snapshot();value.detail.run.executor_kind='UPPER_LLM';value.detail.run.purpose='SCOPE_REVIEW';value.detail.review_evidence_status='AVAILABLE';value.detail.review_evidence={successful_operations:0};
 value.session.runtime.items=[{id:'old-codex',type:'commandExecution',status:'inProgress'}];value.session.runtime.requests=[{projection:{lifecycleState:'WAITING_FOR_USER'}}];
 const {summary,controller}=harness(value),model=summary(value,now);assert.equal(model.active,false);assert.equal(model.current,null);assert.equal(model.waiting,0);assert.equal(model.headline,'等待 GPT 读取文件');assert.doesNotMatch(controller.progress(),/实时活动|正在执行命令/);
 value.detail.run.status='SUCCEEDED';assert.doesNotMatch(harness(value).controller.progress(),/请回到原对话继续审查/);
});

test('GPT file authorization renewal takes priority over waiting-for-report copy',()=>{
 const value=snapshot();value.detail.run.executor_kind='UPPER_LLM';value.detail.run.review_authorization_required=true;
 const {summary,controller}=harness(value);assert.equal(summary(value,now).headline,'GPT 审查待重新授权');assert.equal(summary(value,now).active,false);assert.match(controller.progress(),/重新授权 GPT 审查/);assert.doesNotMatch(controller.progress(),/等待 GPT 提交审查报告/);
 value.detail.run.status='SUCCEEDED';assert.equal(harness(value).summary(value,now).reauthorization,false);
});
