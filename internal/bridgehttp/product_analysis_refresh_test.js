"use strict";
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const C=require('./ui/product-components.js');
function harness(answer){
 const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,'utf8').replace('return {mount, stop, refresh, refreshList, select, selectExactRun: select};',`return {read:readSnapshot,view:()=>snapshot,setup(){host={isConnected:true};project='project';runID='run';paint=()=>{};paintNotice=()=>{};},switchRun(){generation++;runID='other';loading=false;}};`);
 const timers=new Map();let number=0;const context={window:{DevBridgeComponents:C,DevBridgeStatus:{},DevBridgeLogFormat:{}},document:{hidden:false},AbortController,URLSearchParams,console,setTimeout(fn,ms){const id=++number;timers.set(id,{fn,ms});return id;},clearTimeout(id){timers.delete(id);}};
 vm.createContext(context);vm.runInContext(source,context);
 const calls=[],controller=context.window.DevBridgeSessionWorkbench.create({api:async(url,options)=>{calls.push({url,options});return answer(url,options);},t:v=>v});controller.setup();
 return {controller,timers,calls,context};
}
function snapshot(state){return {detail:{project_id:'project',run:{id:'run',status:'SUCCEEDED',analysis_state:state},analysis:{state,...(state==='RECORDED'?{report:{summary:'GPT 已提交分析'}}:{})}},session:{runId:'run',generation:'g',cursor:1}};}
test('terminal Run keeps read-only analysis refresh and stops when GPT report arrives',async()=>{
 let state='REQUIRED';const h=harness(url=>url.includes('/items?')?{runId:'run',items:[],hasMore:false}:snapshot(state));
 await h.controller.read();assert.equal(h.controller.view().detail.analysis.state,'REQUIRED');assert.equal([...h.timers.values()].filter(x=>x.ms===5000).length,1);
 state='RECORDED';await h.controller.read();assert.equal(h.controller.view().detail.analysis.report.summary,'GPT 已提交分析');assert.equal(h.timers.size,0);
 assert.ok(h.calls.every(x=>!x.options.method||x.options.method==='GET'));
});
test('hidden terminal view pauses analysis polling and delayed reads cannot enter another Run',async()=>{
 const h=harness(url=>url.includes('/items?')?{runId:'run',items:[],hasMore:false}:snapshot('OPTIONAL'));h.context.document.hidden=true;
 await h.controller.read();assert.equal(h.timers.size,0);
 let resolve;const delayed=harness(()=>new Promise(r=>resolve=r));const read=delayed.controller.read();delayed.controller.switchRun();resolve(snapshot('RECORDED'));await read;
 assert.equal(delayed.controller.view(),null);assert.equal(delayed.timers.size,0);
});
