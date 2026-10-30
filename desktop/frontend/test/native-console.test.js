import {strict as assert} from 'node:assert';
import test from 'node:test';
import {createNativeConsole,nativeTaskHTML,nativeListHTML,phaseLabel} from '../src/native-console.js';
const core={state:'RUNNING',ownership:'MANAGED',endpoint:'http://127.0.0.1:8081',dataRoot:'/data',build:'.79'};
const storage=()=>{const values=new Map();return {getItem:key=>values.get(key)||null,setItem:(key,value)=>values.set(key,value),removeItem:key=>values.delete(key)};};
const task=id=>({id,title:'原生任务 <script>',status:'ACTIVE',businessPhase:'ACCEPTANCE_PENDING',updated_at:'2026-10-26T00:00:00Z',presentation:{taskView:{goal:'完成代码改造',progress:[{phase:'REVIEW',status:'COMPLETE'}],verification:{summary:'验证证据尚不完整'}}},operatorActions:[{action:'CONTINUE_TASK',domainAvailable:true,channelAvailable:true,target:{taskId:id,executionId:'exec-'+id,taskActionGeneration:23}},{action:'COMPLETE_TASK',domainAvailable:false,channelAvailable:true,target:{taskId:id},reason:'必需验证未通过'}]});
function fixture(options={}){
 const sent=[],mem=options.storage||storage();let rejectReads=false,mutate=async()=>JSON.stringify({status:200,data:{confirmed:true}});
 const api={ConsoleRead:async(kind,project,id)=>{if(rejectReads)throw Error('offline');return JSON.stringify(kind==='projects'?{items:[{id:'p',name:'Project',repository_count:1},{id:'q',name:'Other',repository_count:2}]}:kind==='tasks'?{items:[task('a'),task('b')],total:2}:kind==='task'?task(id):kind==='completion'?{taskId:id,members:[]}:kind==='runs'?{items:[],total:0}:{});},ConsoleTaskAction:async(p,id,body)=>{sent.push({p,id,body});return mutate();}};
 const c=createNativeConsole({backend:()=>api,storage:mem,uuid:()=>`request-${sent.length+1}`});c.status(core);
 return {c,api,sent,mem,failReads:()=>rejectReads=true,setMutation:fn=>mutate=fn};
}
const turn=()=>new Promise(resolve=>setImmediate(resolve));

test('native inspector escapes source text and displays Core gates without embedded chrome',async()=>{
 const f=fixture();await f.c.refresh(true);const html=nativeTaskHTML(f.c.state);
 assert.match(html,/原生任务 &lt;script&gt;/);assert.doesNotMatch(html,/<iframe|<script>|execution_approved_/);assert.match(html,/验证证据尚不完整/);assert.match(html,/data-nc-command="CONTINUE_TASK"/);assert.match(html,/data-nc-command="COMPLETE_TASK" disabled/);assert.equal(phaseLabel(task('a')),'待验收');assert.match(nativeListHTML(f.c.state),/selected/);
});
test('failed reads preserve last task and list while disabling commands',async()=>{
 const f=fixture();await f.c.refresh(true);f.failReads();await f.c.refresh(true);
 assert.equal(f.c.state.tasks.length,2);assert.equal(f.c.state.detail.id,'a');assert.equal(f.c.state.stale,true);
 assert.match(nativeTaskHTML(f.c.state),/data-nc-command="CONTINUE_TASK" disabled/);await f.c.prepare('CONTINUE_TASK');assert.equal(f.c.state.confirm,null);
});
test('late reads cannot overwrite a newly selected project',async()=>{
 const f=fixture();await f.c.refresh(true);let resolve;const old=f.api.ConsoleRead;f.api.ConsoleRead=(kind,p,...rest)=>kind==='tasks'&&p==='p'?new Promise(r=>resolve=r):old(kind,p,...rest);
 const pending=f.c.refresh(true);await f.c.selectProject('q');resolve(JSON.stringify({items:[task('old')],total:1}));await pending;await turn();await turn();
 assert.equal(f.c.state.projectId,'q');assert.deepEqual(f.c.state.tasks.map(t=>t.id),['a','b']);
});
test('unknown submission persists exact identity across reload; explicit retry never generates a new body',async()=>{
 const f=fixture();await f.c.refresh(true);f.setMutation(async()=>{throw Error('response lost');});await f.c.prepare('CONTINUE_TASK');await f.c.send(false,'用户备注');assert.equal(f.sent.length,1);const body=f.sent[0].body;assert.equal(JSON.parse(body).target.taskActionGeneration,23);
 const reload=fixture({storage:f.mem});await reload.c.refresh(true);assert.equal(reload.sent.length,0);assert.equal(reload.c.state.pending.body,body);await reload.c.send(true);assert.equal(reload.sent.length,1);assert.equal(reload.sent[0].body,body);assert.equal(reload.c.state.pending,null);
});
test('pending requests for different tasks cannot overwrite each other',async()=>{
 const f=fixture();await f.c.refresh(true);f.setMutation(async()=>{throw Error('lost');});await f.c.prepare('CONTINUE_TASK');await f.c.send();const first=f.c.state.pending.body;
 await f.c.selectTask('b');await f.c.prepare('CONTINUE_TASK');await f.c.send();assert.equal(f.sent.length,2);
 await f.c.selectTask('a');assert.equal(f.c.state.pending.body,first);
});
test('double click submits only once and persistence failure prevents dispatch',async()=>{
 const f=fixture();await f.c.refresh(true);let resolve;f.setMutation(()=>new Promise(r=>resolve=r));await f.c.prepare('CONTINUE_TASK');const one=f.c.send();await f.c.send();assert.equal(f.sent.length,1);resolve(JSON.stringify({status:200,data:{}}));await one;
 const broken=fixture({storage:{getItem:()=>null,setItem(){throw Error('quota');}}});await broken.c.refresh(true);await broken.c.prepare('CONTINUE_TASK');await broken.c.send();assert.equal(broken.sent.length,0);
});
test('a Core rejection remains visible after refreshing the task; stale unavailable actions cannot be prepared',async()=>{
 const f=fixture();await f.c.refresh(true);await f.c.prepare('COMPLETE_TASK');assert.equal(f.c.state.confirm,null);
 f.setMutation(async()=>JSON.stringify({status:409,data:{message:'当前操作版本已变化'}}));await f.c.prepare('CONTINUE_TASK');await f.c.send();assert.equal(f.c.state.actionError,'当前操作版本已变化');assert.equal(f.c.state.pending,null);
});
test('request cannot replay against a different attached data root',async()=>{
 const f=fixture();await f.c.refresh(true);f.setMutation(async()=>{throw Error('lost');});await f.c.prepare('CONTINUE_TASK');await f.c.send();f.c.status({...core,dataRoot:'/another'});await f.c.send(true);assert.equal(f.sent.length,1);
});

test('native run history opens the latest bounded page and retains paging identity',async()=>{
 const f=fixture();await f.c.refresh(true);const original=f.api.ConsoleRead,calls=[];
 f.api.ConsoleRead=async(kind,p,id,offset,...rest)=>{if(kind!=='runs')return original(kind,p,id,offset,...rest);calls.push(offset);return JSON.stringify({items:[{id:'earlier-'+offset},{id:'later-'+offset}],total:43,offset,limit:20});};
 await f.c.runs();assert.deepEqual(calls,[0,23]);assert.equal(f.c.state.runs.items[0].id,'later-23');await f.c.runs(3);assert.equal(calls.at(-1),3);assert.equal(f.c.state.runs.offset,3);
});

test('project metadata from an old attached runtime cannot populate the new runtime',async()=>{
 const f=fixture();let resolve;f.api.ConsoleRead=()=>new Promise(r=>resolve=r);const old=f.c.refresh(true);f.c.status({...core,dataRoot:'/new-data'});resolve(JSON.stringify({items:[{id:'stale-project'}]}));await old;assert.deepEqual(f.c.state.projects,[]);assert.equal(f.c.state.detail,null);
});

test('a workflow requiring its dedicated approval form is not mislabeled as having no action',()=>{
 const owner=task('a');owner.operatorActions=[{action:'APPROVE_AND_START',domainAvailable:true,channelAvailable:true,target:{taskId:'a'}}];const html=nativeTaskHTML({detail:owner,tab:'overview'});assert.match(html,/专用工作流/);assert.match(html,/data-nc-browser/);assert.doesNotMatch(html,/当前没有需要处理的操作/);
});
