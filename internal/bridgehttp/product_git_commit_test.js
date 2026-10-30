'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
global.DevBridgeStatus=require('./ui/product-status.js');
const W=require('./ui/product-workbench.js'),Components=require('./ui/product-components.js');
const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,'utf8');
function fixture(){
 const topic={id:'area',status:'ACTIVE',presentation:{commitStatus:'UNCOMMITTED',canCommit:true,commit:{developmentBranch:'dev_exact'},commitView:{summary:'提交：未提交',actionLabel:'提交到 dev_exact'}}};
 const calls=[],opened=[],toasts=[];let release;
 const context={C:Components,t:key=>key,locale:"en",state:{selectedProjectID:'project',projectWorkspace:{featureAreas:[topic],tasks:[{id:'one',feature_area_id:'area'},{id:'two',feature_area_id:'area'},{id:'foreign',feature_area_id:'other'}]}},workflowDrawerVersion:1,workflowDrawerTarget:'area',window:{crypto:{randomUUID:()=> 'same-request'},clearTimeout(){},setTimeout(){}},document:{hidden:false},qs:v=>new URLSearchParams(v).toString(),toast:v=>toasts.push(v),openTopicDetail:v=>opened.push(v),loadProjectData:()=>assert.fail('project-wide read'),api:async(path,options)=>{calls.push({path,options});if(options)return new Promise(r=>release=r);if(path.includes('presentation'))return topic.presentation;return {presentation:{taskView:{commitSha:'short',mergedCommit:'merged'}}}}};
 vm.createContext(context);vm.runInContext(source.slice(source.indexOf('  function featureAreaRetireControl('),source.indexOf('  function renderTopicRows(')),context);vm.runInContext(source.slice(source.indexOf('  const gitAreaRequests='),source.indexOf('  function usageFactsBlock(')),context);
 return {context,topic,calls,opened,toasts,delivery:()=>W.renderFeatureAreaDetail({...context,legacy:{areaCommitPanel:context.areaCommitPanel,featureAreaMenu:context.featureAreaMenu}},topic),release:()=>release({})};
}
test('Web FeatureArea submission is scoped, double-click fenced, refreshes every related Task only',async()=>{
 const f=fixture();assert.match(f.delivery(),/提交到 dev_exact/);assert.doesNotMatch(f.context.featureAreaMenu(f.topic),/data-menu-commit|提交到 dev_exact/);
 const button={disabled:false,dataset:{},textContent:'提交到 dev_exact'};const pending=f.context.areaAction('area','COMMIT_FEATURE_AREA',{},button);await f.context.areaAction('area','COMMIT_FEATURE_AREA',{},button);assert.equal(f.calls.length,1);assert.equal(button.textContent,'正在提交…');f.release();await pending;
 assert.equal(f.calls.filter(v=>v.options).length,1);assert.ok(f.calls.some(v=>v.path.includes('/tasks/one?')));assert.ok(f.calls.some(v=>v.path.includes('/tasks/two?')));assert.ok(!f.calls.some(v=>v.path.includes('foreign')));assert.deepEqual(f.opened,['area']);assert.equal(f.topic.status,'ACTIVE');
});
test('Web displays shared disabled reason, running state, conflict and recovery without exposing technical errors',()=>{
 const f=fixture();f.topic.presentation.canCommit=false;f.topic.presentation.commitView.reason='等待 Task 自动提交完成';let html=f.context.areaCommitPanel(f.topic);assert.match(html,/提交：未提交/);assert.match(html,/等待 Task 自动提交完成/);assert.doesNotMatch(html,/data-git-area-action|data-menu-commit/);assert.match(html,/data-view-full-result/);
 f.topic.presentation.commitView.activity='RUNNING';html=f.context.areaCommitPanel(f.topic);assert.match(html,/role="status">正在提交…/);
 f.topic.presentation.commitView={summary:'提交：未提交',reason:'无法提交到 dev：存在代码冲突',details:{lastError:'MERGE_CONFLICT'}};html=f.context.areaCommitPanel(f.topic);assert.match(html.split('<details>')[0],/无法提交到 dev：存在代码冲突/);assert.doesNotMatch(html.split('<details>')[0],/MERGE_CONFLICT/);
 f.topic.presentation.canCommit=true;f.topic.presentation.commit.requestId='durable';f.topic.presentation.commitView={summary:'提交：未提交',reason:'提交状态需要恢复',activity:'RECOVERY_REQUIRED',actionLabel:'恢复提交'};html=f.delivery();assert.match(html,/data-request-id="durable"/);assert.match(html,/>恢复提交</);
});
test('Task list keeps lifecycle and short Code/Dev facts without full hashes',()=>{
 const context={C:{esc:String,statusBadge:(_,v)=>v,localizedTimestamp:()=>"Updated"},i18n:{locale:"en"},t:v=>v};vm.createContext(context);vm.runInContext(source.slice(source.indexOf('  function workItems('),source.indexOf('  function renderTasks(')),context);
 const sha='a'.repeat(40),merge='b'.repeat(40);const html=context.renderWorkItemRows(context.workItems({tasks:[{id:'one',title:'One',status:'SUCCEEDED',phase:'ACCEPTANCE_PENDING',phaseLabel:'Done',commitSha:sha,mergedCommit:merge}]}));assert.match(html,/Status.*Code.*Dev/);assert.match(html,/aaaaaaa/);assert.match(html,/bbbbbbb/);assert.ok(!html.includes(sha)&&!html.includes(merge));
});
test('multi-repository recovery buttons preserve exact parent and predecessor identities',async()=>{
 const f=fixture(),c=f.topic.presentation.commit;
 Object.assign(c,{canRecoverMerge:true,requestId:'original-parent'});
 f.topic.presentation.capabilities=[{action:'RECOVER_FEATURE_AREA_MERGE',available:true}];
 f.topic.presentation.commitView.activity='RECOVERY_REQUIRED';
 let html=f.context.areaCommitPanel(f.topic);assert.match(html,/data-merge-request-id="original-parent"/);assert.match(html,/继续原交付/);assert.doesNotMatch(html,/role="status">正在提交/);
 const button={disabled:false,dataset:{mergeRequestId:'original-parent'},textContent:'继续原交付'};
 let pending=f.context.areaAction('area','RECOVER_FEATURE_AREA_MERGE',{},button);f.release();await pending;
 assert.equal(JSON.parse(f.calls[0].options.body).mergeRequestId,'original-parent');
 c.canRecoverMerge=false;c.mergeRetryOperation={mergeRequestId:'original-parent',repositoryId:'repo',operationId:'rejected-op'};
 f.topic.presentation.capabilities=[{action:'REPREPARE_FEATURE_AREA_MERGE',available:true}];
 html=f.context.areaCommitPanel(f.topic);assert.match(html,/data-predecessor-operation-id="rejected-op"/);assert.match(html,/重新授权合入/);
 const retry={disabled:false,dataset:{mergeRequestId:'original-parent',repositoryId:'repo',predecessorOperationId:'rejected-op'},textContent:'重新授权合入'};
 pending=f.context.areaAction('area','REPREPARE_FEATURE_AREA_MERGE',{},retry);f.release();await pending;
 const body=JSON.parse(f.calls.filter(v=>v.options).at(-1).options.body);
 assert.equal(body.repositoryId,'repo');assert.equal(body.predecessorOperationId,'rejected-op');assert.equal(body.mergeRequestId,'original-parent');
});
test('unknown multi-repository outcome offers no write button or running spinner',()=>{
 const f=fixture();f.topic.presentation.commit={mergeRecoveryState:'UNKNOWN'};f.topic.presentation.capabilities=[];
 f.topic.presentation.commitView={summary:'提交：部分仓库已合入',activity:'OUTCOME_UNKNOWN',reason:'结果尚未确认，请刷新查看回执'};
 const html=f.context.areaCommitPanel(f.topic);assert.doesNotMatch(html,/data-git-area-action|正在提交…/);assert.match(html,/结果尚未确认/);
});
test('Task list uses per-repository summaries without a fabricated scalar SHA',()=>{
 const context={C:{esc:String,statusBadge:(_,v)=>v,localizedTimestamp:()=>"Updated"},t:v=>v,i18n:{locale:'en'}};vm.createContext(context);vm.runInContext(source.slice(source.indexOf('  function workItems('),source.indexOf('  function renderTasks(')),context);
 const html=context.renderWorkItemRows(context.workItems({tasks:[{id:'t',title:'Task',status:'SUCCEEDED',commitSummary:'代码：已完成逐仓交付',devSummary:'Dev：已全部合入'}]}));
 assert.match(html,/已完成逐仓交付/);assert.match(html,/已全部合入/);assert.doesNotMatch(html,/未提交/);
});

test('explicit plan release preserves identity and never shares the retry request key',async()=>{
 const f=fixture(),c=f.topic.presentation.commit;
 c.mergeRetryOperation={mergeRequestId:'parent',repositoryId:'repo',operationId:'rejected',canRelease:true};
 f.topic.presentation.capabilities=[{action:'REPREPARE_FEATURE_AREA_MERGE',available:true}];
 assert.match(f.context.areaCommitPanel(f.topic),/解除旧计划/);
 vm.runInContext("gitAreaRequests.set('project:area:REPREPARE_FEATURE_AREA_MERGE:parent:rejected:false','failed-retry')",f.context);
 const button={disabled:false,dataset:{mergeRequestId:'parent',repositoryId:'repo',predecessorOperationId:'rejected',releasePlan:'true'},textContent:'解除旧计划'};
 const pending=f.context.areaAction('area','REPREPARE_FEATURE_AREA_MERGE',{},button);
 assert.equal(button.textContent,'正在解除…');
 const body=JSON.parse(f.calls[0].options.body);assert.equal(body.releasePlan,true);assert.notEqual(body.requestId,'failed-retry');assert.equal(body.mergeRequestId,'parent');assert.equal(body.predecessorOperationId,'rejected');
 f.release();await pending;
 c.mergeRetryOperation.canRelease=false;
 assert.doesNotMatch(f.context.areaCommitPanel(f.topic),/解除旧计划/);
});
