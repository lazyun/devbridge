"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
function harness(answer){
 // Run the shipped controller. Expose its private state only in this VM and
 // suppress unrelated DOM painting so async ownership can be exercised directly.
 const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,"utf8").replace(
  "return {mount, stop, refresh, refreshList, select, selectExactRun: select};",
  `return {load:nativeHistory,panel:nativeHistoryPanel,setup(){host={isConnected:true};project='project';runID='run';snapshot={detail:{run:{thread_id:'thread',turn_id:'turn'}}};paint=()=>{};},switchRun(){generation++;runID='other';nativePage=null;},page:()=>nativePage,error:()=>nativeError};`);
 const esc=v=>String(v??"").replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll(">","&gt;").replaceAll('"',"&quot;");
 const context={window:{DevBridgeComponents:{esc},DevBridgeStatus:{},DevBridgeLogFormat:{}},AbortController,URLSearchParams,console};vm.createContext(context);vm.runInContext(source,context);
 const calls=[];const controller=context.window.DevBridgeSessionWorkbench.create({api:async(url,options)=>{calls.push({url,options});return answer(url,options);},t:v=>v});controller.setup();return {controller,calls};
}
const page=(extra={})=>({runId:"run",threadId:"thread",turnId:"turn",availability:"AVAILABLE",items:[{id:"one",type:"agentMessage",status:"completed",text:"<img src=x onerror=bad>"}],nextCursor:"next-page",...extra});
test("native history is opt-in, preserves pagination, deduplicates records, and escapes text",async()=>{
 let number=0;const {controller,calls}=harness(()=>page(++number===1?{}:{items:[{id:"one",type:"agentMessage",text:"updated"},{id:"two",type:"plan",text:"finished"}],nextCursor:null}));
 assert.equal(calls.length,0);assert.match(controller.panel(),/读取 Codex 历史/);
 await controller.load();assert.equal(calls.length,1);assert.doesNotMatch(controller.panel(),/<img /);assert.match(controller.panel(),/&lt;img/);
 await controller.load();assert.match(calls[1].url,/cursor=next-page/);assert.equal(controller.page().items.length,2);await controller.load();assert.equal(calls.length,2,"exhausted page must not be fetched again");
 assert.ok(calls.every(c=>!c.options.method||c.options.method==="GET"));
});
test("history response cannot cross Run, Thread or Turn identities",async()=>{
 for(const replacement of [{runId:"other"},{threadId:"other"},{turnId:"other"}]){const {controller}=harness(()=>page(replacement));await controller.load();assert.equal(controller.page(),null);assert.match(controller.error(),/身份不匹配/);}
});
test("switching runs discards a delayed native history response",async()=>{
 let resolve;const {controller}=harness(()=>new Promise(r=>resolve=r));const pending=controller.load();controller.switchRun();resolve(page());await pending;assert.equal(controller.page(),null);
});
test("backend unavailable preserves a readable notice and permits explicit retry",async()=>{
 const {controller,calls}=harness(()=>page({availability:"UNAVAILABLE",notice:"原后端离线，仍可查看已采集记录。"}));await controller.load();assert.equal(controller.page(),null);assert.match(controller.panel(),/原后端离线/);await controller.load();assert.equal(calls.length,2);
});
