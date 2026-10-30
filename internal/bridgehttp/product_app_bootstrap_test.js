"use strict";
const {test}=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const {createDocument}=require('./testdata/product_dom_harness.js');
const page=fs.readFileSync(`${__dirname}/ui/index.html`,'utf8');
const settle=()=>new Promise(r=>setTimeout(r,5));
async function until(check,label){for(let i=0;i<300;i++){if(check())return;await settle();}assert.fail(label);}
async function boot(t,{hash='',intercept,stored={}}={}){
 const {createFixtureServer}=await import('./testdata/task_owned_visual_host.mjs');const fixture=createFixtureServer({workbench:true});
 const base='http://fixture.invalid',doc=createDocument(page),location=new URL(base+'/ui/'+hash),storage=new Map(Object.entries(stored)),requests=[],timers=new Map(),windowEvents={};let timer=0;
 const context={document:doc,URL,URLSearchParams,AbortController,Date,Intl,console,structuredClone,navigator:{languages:['en'],clipboard:{writeText:async()=>{}}},location,history:{replaceState:(_,__,url)=>{const next=new URL(url,base);location.hash=next.hash;location.pathname=next.pathname;location.search=next.search;}},localStorage:{getItem:k=>storage.get(k)??null,setItem:(k,v)=>storage.set(k,String(v)),removeItem:k=>storage.delete(k)},crypto:{randomUUID:()=>crypto.randomUUID()},setTimeout:(fn,ms)=>{timers.set(++timer,{fn,ms});return timer;},clearTimeout:id=>timers.delete(id),addEventListener:(name,fn)=>{(windowEvents[name]??=[]).push(fn);},fetch:async(path,options={})=>{requests.push({path,options});const mocked=await intercept?.(String(path),options,fixture);if(mocked)return mocked;let status=200,body='';const response={setHeader(){},writeHead:code=>status=code,end:value=>body=String(value||'')};await fixture.server.listeners('request')[0]({url:String(path),method:options.method||'GET'},response);return new Response(body,{status});}};
 context.removeEventListener=(name,fn)=>{windowEvents[name]=(windowEvents[name]||[]).filter(listener=>listener!==fn);};context.window=context;vm.createContext(context);
 for(const [,file] of page.matchAll(/<script src="([^"]+)"/g))vm.runInContext(fs.readFileSync(`${__dirname}/ui/${file}`,'utf8'),context,{filename:file});
 t.after(()=>{for(const fn of windowEvents.pagehide||[])fn();});
 return {doc,context,requests,fixture,timers,storage,async hash(value){location.hash=value;for(const fn of windowEvents.hashchange||[])fn();await settle();},clickAttr(attr,value){const target=doc.createElement('button');target.setAttribute(attr,value??'');doc.querySelector('#detail').append(target);target.click();}};
}
test('Run shortcuts open exact persisted Prompt and Result; Token details remain accessible',async t=>{
 const app=await boot(t,{hash:'#project=project&task=task-2',intercept:async(path,options,fixture)=>{
  if(path.startsWith('/v1/runs/run-verification-only?'))return new Response(JSON.stringify({...fixture.runDetails.get('run-verification-only'),run:{...fixture.runDetails.get('run-verification-only').run,token_usage:{status:'AVAILABLE',counts:{input_tokens:0,cached_input_tokens:0,output_tokens:23,total_tokens:23}}}}));
 }});
 await until(()=>app.doc.querySelector('[data-task-id="task-2"]')&&!app.doc.querySelector('#detail-body').inert,'Task did not load');
 app.doc.querySelector('[data-task-tab="runs"]').click();
 await until(()=>app.doc.querySelector('[data-run-key="run:run-verification-only"]')&&!app.doc.querySelector('#detail-body').inert,'Task Runs did not load');
 assert.equal(app.doc.querySelector('.current-work'),null);assert.equal(app.doc.querySelector('#detail-read-status').hidden,true);
 for(const tab of ['instructions','evidence']){
  const execution=app.doc.querySelector('[data-run-key="run:run-verification-only"]');
  execution.querySelector(`[data-run-open-tab="${tab}"]`).click();
  await until(()=>app.doc.querySelector(`#run-tab-${tab}`)&&!app.doc.querySelector('#detail-body').inert,'Run shortcut did not load');
  if(tab==='instructions')await until(()=>app.doc.querySelector('#detail-body').textContent.includes('Persisted fixture prompt'),'Prompt was not read');
  if(tab==='evidence')assert.match(app.doc.querySelector('#detail-body').textContent,/Verification Report/);
  if(tab==='evidence'){
   app.doc.querySelector('[data-run-tab="usage"]').click();
   const body=app.doc.querySelector('#run-tab-usage');assert.match(body.textContent,/23/);
   assert.ok(body.querySelectorAll('dd').some(n=>n.textContent==='0'));
  }
  app.doc.querySelector('[data-run-back]').click();
  await until(()=>app.doc.querySelector('[data-run-key="run:run-verification-only"]')&&!app.doc.querySelector('#detail-body').inert,'Back did not restore Task');
  assert.ok(app.doc.querySelector('[data-run-key="run:run-verification-only"]'));
 }
 assert.ok(app.requests.some(r=>r.path.includes('/v1/runs/run-verification-only/prompt?')&&r.path.includes('task_id=task-2')));
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('Run list refresh retains its server page and loads updated rows',async t=>{
 let status='RUNNING';
 const app=await boot(t,{hash:'#project=project&task=task-2',intercept:async path=>{
  if(path.startsWith('/ui/api/tasks/task-2/runs?')){
   const offset=Number(new URL(path,'http://fixture.invalid').searchParams.get('offset'));
   return new Response(JSON.stringify({items:[{id:`paged-${offset}`,runPurpose:'IMPLEMENTATION',transportStatus:status}],total:45,offset,limit:20}));
  }
 }});
 await until(()=>app.doc.querySelector('[data-task-id="task-2"]')&&!app.doc.querySelector('#detail-body').inert,'Task did not load');
 app.doc.querySelector('[data-task-tab="runs"]').click();
 await until(()=>app.doc.querySelector('[data-run-key="run:paged-0"]')&&!app.doc.querySelector('#detail-body').inert,'initial page missing');
 app.doc.querySelector('[data-task-runs-page="20"]').click();
 await until(()=>app.doc.querySelector('[data-run-key="run:paged-20"]')&&!app.doc.querySelector('#detail-body').inert,'next page missing');
 status='SUCCEEDED';app.doc.querySelector('[data-task-refresh]').click();
 await until(()=>app.doc.querySelector('[data-run-key="run:paged-20"]')?.textContent.includes('Completed')&&!app.doc.querySelector('#detail-body').inert,'refresh did not update current page');
 assert.equal(app.doc.querySelector('[data-run-key="run:paged-0"]'),null);
 assert.ok(app.doc.querySelector('.task-tabs'),'Task view navigation remains available after a paginated Run refresh');
 assert.equal(app.doc.querySelector('[data-execution-history]'),null);
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('read status is hidden when healthy, neutral when loading and explanatory on failure',async t=>{
 let fail=false,release;const gate=new Promise(resolve=>release=resolve);
 const app=await boot(t,{hash:'#project=project&task=task-2',intercept:async path=>{
  if(fail&&path.startsWith('/ui/api/tasks/task-2?')){await gate;return new Response(JSON.stringify({message:'fixture read unavailable'}),{status:503});}
 }});
 await until(()=>app.doc.querySelector('[data-task-id="task-2"]')&&!app.doc.querySelector('#detail-body').inert,'Task did not load');
 assert.equal(app.doc.querySelector('#detail-read-status').hidden,true);
 fail=true;app.doc.querySelector('[data-task-refresh]').click();
 assert.equal(app.doc.querySelector('#detail-read-status').hidden,false);assert.equal(app.doc.querySelector('#detail-read-status').classList.contains('is-loading'),true);
 release();await until(()=>app.doc.querySelector('#detail-read-status').getAttribute('role')==='alert','failure was not shown');
 assert.match(app.doc.querySelector('#detail-read-status').textContent,/fixture read unavailable/);
 assert.equal(app.doc.querySelector('#detail-read-status').hidden,false);
 assert.equal(app.doc.querySelector('#detail-read-status').classList.contains('is-loading'),false);
 assert.ok(app.doc.querySelector('[data-task-id="task-2"]'));
});
test('result modal opens from Project and Task previews, preserves exact text through repaint and closes without leaving the Task',async t=>{
 const app=await boot(t,{hash:'#project=project&task=task-2'});
 await until(()=>app.doc.querySelector('[data-task-id="task-2"]')&&!app.doc.querySelector('#detail-body').inert,'Task did not load');
 const full='结论 😀 '.repeat(50)+'\r\n<script>unsafe()</script> & "quotes" \\tail';
 for(const parent of ['#page','#detail-body']){
   const host=app.doc.createElement('div');app.doc.querySelector(parent).append(host);
   host.innerHTML=app.context.DevBridgeComponents.resultPreview(full,key=>key,{title:'完整测试结论'});
   const button=host.querySelector('[data-view-full-result]');button.focus();button.click();
   assert.equal(app.doc.querySelector('#result-modal').classList.contains('hidden'),false);
   assert.equal(app.doc.querySelector('#result-modal-body').textContent,full);
   assert.equal(app.doc.querySelector('#result-modal-body').querySelector('script'),null);
   assert.equal(app.doc.activeElement,app.doc.querySelector('#result-modal-close'));
   app.doc.dispatchEvent({type:'keydown',key:'Tab'});assert.equal(app.doc.activeElement,app.doc.querySelector('#result-modal-body'));
   app.doc.dispatchEvent({type:'keydown',key:'Tab'});assert.equal(app.doc.activeElement,app.doc.querySelector('#result-modal-close'));
   app.doc.dispatchEvent({type:'keydown',key:'Escape'});
   assert.equal(app.doc.activeElement,button);assert.ok(app.doc.querySelector('#result-modal').classList.contains('hidden'));
   button.click();host.innerHTML='refreshed summary';assert.equal(app.doc.querySelector('#result-modal-body').textContent,full);
   app.doc.querySelector('#result-modal-backdrop').click();assert.ok(app.doc.querySelector('#result-modal').classList.contains('hidden'));
   assert.equal(app.doc.querySelector('#result-modal-body').textContent,'');assert.equal(app.doc.querySelector('#detail-layer').classList.contains('hidden'),false);
 }
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('Review preview fetches full text and a late read cannot replace a newer result modal',async t=>{
 let release,held=false,hold=false;const gate=new Promise(resolve=>release=resolve),full='full review\n'+('审查结果 '.repeat(100));
 const app=await boot(t,{hash:'#project=project&task=task-2',intercept:async path=>{
   if(path.startsWith('/ui/api/reviews/review-exact/result?')){if(hold){held=true;await gate;}return new Response(JSON.stringify({result:full}));}
 }});
 await until(()=>app.doc.querySelector('[data-task-id="task-2"]')&&!app.doc.querySelector('#detail-body').inert,'Task did not load');
 app.clickAttr('data-view-full-review-result','review-exact');
 await until(()=>!app.doc.querySelector('#result-modal').classList.contains('hidden'),'Review modal did not open');
 assert.equal(app.doc.querySelector('#result-modal-body').textContent,full);
 assert.ok(app.requests.some(r=>r.path==='/ui/api/reviews/review-exact/result?project_id=project'));
 app.doc.querySelector('#result-modal-close').click();hold=true;app.clickAttr('data-view-full-review-result','review-exact');await until(()=>held,'second Review read missing');
 const host=app.doc.createElement('div');app.doc.querySelector('#page').append(host);host.innerHTML=app.context.DevBridgeComponents.resultPreview('newer conclusion',key=>key);host.querySelector('[data-view-full-result]').click();
 release();await settle();await settle();assert.equal(app.doc.querySelector('#result-modal-body').textContent,'newer conclusion');
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('full production modules bootstrap against isolated fixture HTTP and open Task → Run → Back through mock event dispatch',async t=>{
 const app=await boot(t,{hash:'#project=project&task=task-2&tab=executions'});
 await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('data-task-id="task-2"'),'Task deep link did not finish');
 app.doc.querySelector('[data-task-tab="runs"]').click();
 await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('data-run-key="run:run-verification-only"'),'Task Runs did not render');
 assert.equal(app.doc.querySelector('#detail-body').inert,false);assert.match(app.doc.querySelector('#page').innerHTML,/Semantic|Task|任务/);
 const execution=app.doc.querySelector('[data-run-key="run:run-verification-only"]');app.doc.querySelector('#detail').scrollTop=419;
 const run=app.doc.querySelector('[data-task-run="run-verification-only"]');assert.ok(run,'production Run button');run.click();
 await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('run-detail'),'Run did not render');
 assert.ok(app.requests.some(r=>r.path.includes('/v1/runs/run-verification-only?')&&r.path.includes('task_id=task-2')&&r.path.includes('project_id=project')));
 app.doc.querySelector('[data-run-tab="instructions"]').click();await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('Persisted fixture prompt'),'persisted prompt did not render');assert.match(app.doc.querySelector('#detail-body').innerHTML,/&lt;script&gt;/);
 app.doc.querySelector('[data-run-back]').click();await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('data-task-id="task-2"')&&!app.doc.querySelector('#detail-body').inert,'Back did not finish');assert.ok(app.doc.querySelector('.task-tabs'));
 assert.ok(app.doc.querySelector('[data-run-key="run:run-verification-only"]'));assert.equal(app.doc.querySelector('#detail').scrollTop,419);assert.equal(app.doc.querySelector('textarea[name="nextWorkSpec"]'),null);
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('full app keeps successful workspace on partial project failure and fences a late Task after a newer Task',async t=>{
 let release;const gate=new Promise(r=>release=r);let held=false;
 const app=await boot(t,{intercept:async(path,options,fixture)=>{if(path.startsWith('/v1/repositories?'))return new Response(JSON.stringify({message:'fixture repositories unavailable'}),{status:503});if(path.startsWith('/ui/api/tasks/task-1?')&&!held){held=true;await gate;return new Response(JSON.stringify(fixture.tasks[0]));}}});
 await until(()=>app.doc.querySelector('#page').innerHTML.includes('fixture repositories unavailable'),'partial project notice missing');assert.match(app.doc.querySelector('#page').innerHTML,/workbench-priority="attention"/);
 app.clickAttr('data-task-detail','task-1');await until(()=>held,'first Task read not issued');app.clickAttr('data-task-detail','task-2');await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('data-task-id="task-2"'),'second Task did not render');
 release();await settle();await settle();assert.match(app.doc.querySelector('#detail-body').innerHTML,/data-task-id="task-2"/);assert.doesNotMatch(app.doc.querySelector('#detail-body').innerHTML,/data-task-id="task-1"/);
 app.doc.querySelector('#detail-close').click();assert.ok(app.doc.querySelector('#detail-layer').classList.contains('hidden'));assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('full app Project switch aborts a held Run and a late response cannot reopen its drawer or replace the new workspace',async t=>{
 let release,held=false;const gate=new Promise(resolve=>release=resolve);
 const app=await boot(t,{hash:'#project=project&task=task-2',intercept:async(path,options,fixture)=>{
   if(path.startsWith('/v1/projects?'))return new Response(JSON.stringify({items:[{id:'project',name:'Dev Bridge',project_context_status:'READY'},{id:'other',name:'Project Two',project_context_status:'READY'}]}));
   if(path.startsWith('/ui/api/project-workspace?')&&path.includes('project_id=other'))return new Response(JSON.stringify({project_id:'other',project_context:{state:'READY'},featureAreas:[],tasks:[],documents:[],workspaces:[],executions:[]}));
   if(path.startsWith('/v1/runs/run-verification-only?')){held=true;await gate;return new Response(JSON.stringify(fixture.runDetails.get('run-verification-only')));}
 }});
 await until(()=>app.doc.querySelector('#detail-body').innerHTML.includes('data-task-id="task-2"')&&!app.doc.querySelector('#detail-body').inert,'initial Task did not load');
 app.doc.querySelector('[data-task-tab="runs"]').click();
 await until(()=>app.doc.querySelector('[data-task-run="run-verification-only"]'),'Task Runs did not load');
 app.doc.querySelector('[data-task-run="run-verification-only"]').click();await until(()=>held,'Run was not requested');
 app.clickAttr('data-select-project','other');await until(()=>app.doc.querySelector('#page').innerHTML.includes('Project Two'),'new Project did not load');
 const request=app.requests.find(r=>r.path.startsWith('/v1/runs/run-verification-only?'));assert.equal(request.options.signal.aborted,true);
 release();await settle();await settle();assert.ok(app.doc.querySelector('#detail-layer').classList.contains('hidden'));assert.match(app.doc.querySelector('#page').innerHTML,/Project Two/);assert.equal(app.storage.get('devbridge.console.selectedProject'),'other');assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});

test('saved foreign filters are cleared before the first task page request and never remain hidden',async t=>{
 const app=await boot(t,{stored:{'devbridge.console.selectedProject':'project','devbridge.console.projectTab':'tasks','devbridge.console.workItemFilters':JSON.stringify({area:'removed-area',branch:'refs/heads/removed'})}});
 await until(()=>app.doc.querySelector('[data-task-filters-clear]')&&!app.doc.querySelector('[aria-busy="true"]'),'Task list did not load');
 const reads=app.requests.filter(r=>r.path.startsWith('/ui/api/work-tabs/tasks?'));assert.ok(reads.length);
 for(const {path} of reads){const q=new URL(path,'http://fixture.invalid').searchParams;assert.equal(q.get('feature_area_id'),null);assert.equal(q.get('branch'),null);}
 assert.ok(app.doc.querySelector('[data-task-detail]'));assert.match(app.doc.querySelector('#page').textContent,/Saved filters/);
 assert.deepEqual(JSON.parse(app.storage.get('devbridge.console.workItemFilters')),{area:'',branch:''});
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
test('completed task overview prioritizes real delivery and verification, localizes phase and omits disabled terminal actions',async t=>{
 const app=await boot(t,{hash:'#project=project&task=task-2',stored:{'devbridge.console.language':'zh-CN'},intercept:async(path,options,fixture)=>{
  if(path.startsWith('/ui/api/tasks/task-2?')){
   const value=structuredClone(fixture.tasks.find(task=>task.id==='task-2'));const v=value.presentation.taskView;
   Object.assign(v,{status:'SUCCEEDED',terminal:true,phase:'DELIVERING',phaseLabel:'Done',implementationMode:'DIRECT_FILE',commitStatus:'COMMITTED',commitSummary:'代码：已提交 · abc1234',devIntegrationStatus:'MERGED',devSummary:'Dev：已合入 · abc1234',verification:{summary:'五项验收标准通过 <script>inert</script>'},actionsStatus:'AVAILABLE',actions:[{action:'CANCEL_EXECUTION',available:false,reason:'This Task is terminal.'}]});
   delete value.presentation.pendingDecision;value.presentation.nextAction={code:'WORKFLOW_COMPLETE',message:'The Task is terminal.'};return new Response(JSON.stringify(value));
  }
 }});
 await until(()=>app.doc.querySelector('.task-outcome')&&!app.doc.querySelector('#detail-body').inert,'Task conclusion did not load');
 const header=app.doc.querySelector('.task-fixed-header').textContent,summary=app.doc.querySelector('.task-outcome').textContent;
 assert.match(header,/已完成/);assert.doesNotMatch(header,/Done|COMPLETE|SUCCEEDED|交付中/);
 for(const text of ['GPT 文件接口','已提交','已合入','五项验收标准通过'])assert.ok(summary.includes(text),text);
 assert.equal(app.doc.querySelector('.task-outcome script'),null);
 assert.equal(app.doc.querySelector('[data-section-key="task:execution-diagnostics"]').open,false);
 assert.equal(app.doc.querySelector('.task-operation-area'),null);assert.doesNotMatch(app.doc.querySelector('#detail-body').textContent,/The Task is terminal|终止执行/);
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});

test('session list localizes run purpose and outcome and never invents a missing Codex Thread',async t=>{
 const app=await boot(t,{stored:{'devbridge.console.selectedProject':'project','devbridge.console.projectTab':'sessions','devbridge.console.language':'zh-CN'},intercept:async path=>{
  if(path.startsWith('/v1/runs?')){const q=new URL(path,'http://fixture.invalid').searchParams;return new Response(JSON.stringify({items:[{id:'ai-review-logical',project_id:'project',task_id:'task-2',task_title:'审查任务',purpose:'SCOPE_REVIEW',status:'SUCCEEDED'}],has_more:false,total:1,limit:Number(q.get('limit'))||20,offset:Number(q.get('offset'))||0}));}
 }});
 await until(()=>app.doc.querySelector('[data-session-run="ai-review-logical"]'),'Session list did not load');
 assert.equal(app.doc.querySelector('#page .project-header'),null,'session page must not repeat the appbar project summary');
 assert.equal(app.doc.querySelector('#page .workbench-repositories'),null,'session page must not repeat appbar branch descriptions');
 assert.ok(app.doc.querySelector('#project-selector-slot [data-project-trigger]'),'appbar project selector remains available');
 assert.ok(app.doc.querySelector('#development-selector-slot'),'appbar branch context remains available');
 const row=app.doc.querySelector('[data-session-run="ai-review-logical"]').textContent;
 assert.match(row,/审查 · 已完成/);assert.doesNotMatch(row,/外部会话/);assert.doesNotMatch(row,/Codex Thread|SCOPE_REVIEW|SUCCEEDED|ai-review-logical/);
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});

test('session first selection, refresh and Instructions → Conversation render real content instead of an empty shell',async t=>{
 let message='首次已采集消息';
 const app=await boot(t,{stored:{'devbridge.console.selectedProject':'project','devbridge.console.projectTab':'sessions','devbridge.console.language':'zh-CN'},intercept:async path=>{
  if(path.startsWith('/v1/runs?')){const q=new URL(path,'http://fixture.invalid').searchParams;return new Response(JSON.stringify({items:[{id:'real-session',project_id:'project',task_id:'task-2',task_title:'验证任务',purpose:'VERIFICATION',status:'SUCCEEDED'}],has_more:false,total:1,limit:Number(q.get('limit'))||20,offset:Number(q.get('offset'))||0}));}
  if(path.startsWith('/ui/api/runs/real-session/session/items?'))return new Response(JSON.stringify({runId:'real-session',items:[{ordinal:1,item:{id:'message-1',type:'agentMessage',text:message,status:'completed'}}],hasMore:false}));
  if(path.startsWith('/ui/api/runs/real-session/session/instructions?'))return new Response(JSON.stringify({items:[{source:'RECORDED_DISPATCH_INPUT',dispatched:true,available:true,text:'准确冻结指令'}]}));
  if(path.startsWith('/ui/api/runs/real-session/session?'))return new Response(JSON.stringify({detail:{project_id:'project',run:{id:'real-session',task_id:'task-2',task_title:'验证任务',purpose:'VERIFICATION',status:'SUCCEEDED'}},session:{runId:'real-session',generation:'one',cursor:1,coverage:{state:'AVAILABLE'}}}));
 }});
 await until(()=>app.doc.querySelector('[data-session-run="real-session"]'),'run entry missing');app.doc.querySelector('[data-session-run="real-session"]').click();
 await until(()=>app.doc.querySelector('[data-session-content]').dataset.mode==='progress','first run selection did not show progress');assert.doesNotMatch(app.doc.querySelector('[data-session-content]').textContent,/首次已采集消息/);app.doc.querySelector('[data-session-tab="conversation"]').click();await until(()=>app.doc.querySelector('[data-session-item="message-1"]')?.textContent.includes(message),'conversation did not render exact captured content');
 message='更新后的准确消息';app.doc.querySelector('[data-session-refresh]').click();await until(()=>app.doc.querySelector('[data-session-item="message-1"]')?.textContent.includes(message),'same-run refresh did not reconcile content');
 app.doc.querySelector('[data-session-tab="instructions"]').click();await until(()=>app.doc.querySelector('[data-session-content]').textContent.includes('准确冻结指令'),'instructions missing');
 app.doc.querySelector('[data-session-tab="conversation"]').click();await until(()=>app.doc.querySelector('[data-session-item="message-1"]')?.textContent.includes(message),'return to Conversation left a different tab body');
 assert.equal(app.doc.querySelector('[data-session-content]').dataset.mode,'conversation');assert.doesNotMatch(app.doc.querySelector('[data-session-content]').textContent,/准确冻结指令/);
 assert.equal(app.requests.filter(r=>r.options.method&&r.options.method!=='GET').length,0);
});
