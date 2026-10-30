"use strict";

// This file is both the Card's exhaustive test entrypoint and the source of
// the lightweight host harness used by the focused Card suites below.  Node's
// test runner executes required files, so registering these tests unconditionally
// caused every focused suite to re-run the entire base suite in the same process.
// Only register the base suite when this file is the entrypoint.
const test = require.main === module ? require("node:test") : () => {};
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const crypto = require("node:crypto");

const html = fs.readFileSync(`${__dirname}/dev_bridge_ui.html`, "utf8");
// The document has a small diagnostic bootstrap followed by the Card's main
// program. Tests exercise the latter in isolation; greedily joining both
// script bodies would turn the intervening HTML into JavaScript.
const scriptBodies = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(match => match[1]);
const script = scriptBodies.at(-1);
if (!script) throw new Error("Dev Bridge Card main script is missing");

class Element {
  constructor(tag) { this.tagName=tag; this.childNodes=[]; this.attributes={}; this._text=""; this.value=""; this.selectedIndex=0; this.disabled=false; }
  set textContent(value) { this._text=String(value); for(const child of this.childNodes)child.parentNode=null;this.childNodes=[]; }
  get textContent() { return this._text+this.childNodes.map(v=>v.textContent).join(" "); }
  append(...children) { for(const child of children)child.parentNode=this;this.childNodes.push(...children); }
  addEventListener(name,fn) { this["on"+name]=fn; }
  setAttribute(name, value) { this.attributes[name]=String(value); }
  remove(){if(this.parentNode){this.parentNode.childNodes=this.parentNode.childNodes.filter(v=>v!==this);this.parentNode=null;}}
  getAttribute(name){return this.attributes[name];}
  contains(value){return this===value||this.childNodes.some(child=>child.contains(value));}
  querySelector(tag){return this.querySelectorAll(tag)[0];}
  focus(){this.focused=true;}
  get options() { return this.childNodes; }
  querySelectorAll(tag) { return this.childNodes.flatMap(child=>[...(child.tagName===tag?[child]:[]),...child.querySelectorAll(tag)]); }
  getBoundingClientRect() { return {width: 640, height: 320}; }
}

function boot(options={}) {
  const root=new Element("main"),posts=[],events=[],trace=[],visibleMessages=[],listeners={},saved=[],rpcTimers=new Map(),timers=new Map();let timerID=0,clock=0;
  const claimLedger=options.claimLedger||new Set(),attemptMarkers=new Set();
  const parent={postMessage(value){posts.push(structuredClone(value));const event=value.method==='tools/call'?value.params.name:value.method;events.push(event);trace.push({event,receipts:JSON.parse(context.window.__testEvaluate?.('JSON.stringify(typeof wakeReceipts==="undefined"?[]:[...wakeReceipts.values()].map(({contextStatus,claimAcquired,messageAttempted,path})=>({contextStatus,claimAcquired,messageAttempted,path})))')||'[]')});if(value.id!=null&&value.method)rpcTimers.set(value.id,timerID);
    if(value.method==='tools/call'&&value.params.name==='dev_bridge_review_wakeup_claim'&&!options.manualClaims)queueMicrotask(()=>{const key=JSON.stringify(value.params.arguments),found=claimLedger.has(key);claimLedger.add(key);send({jsonrpc:'2.0',id:value.id,result:{structuredContent:{status:found?'ALREADY_ATTEMPTED':'CLAIMED'}}});});
  }};
  const context={
    window:{openai:{widgetState:options.widgetState||{},setWidgetState(value){if(options.storageError)throw new Error('storage unavailable');saved.push(structuredClone(value));for(const [key,r]of Object.entries(value.privateContent?.reviewWakeups||{}))if(r.messageAttempted&&!attemptMarkers.has(key)){attemptMarkers.add(key);events.push('MESSAGE_ATTEMPT_MARKED');}this.widgetState=structuredClone(value);}}},
    document:{hidden:false,addEventListener(name,fn){listeners[name]=fn;},removeEventListener(name){delete listeners[name];},getElementById(){return root;},createElement(tag){return new Element(tag);}},
    parent,TextEncoder,Uint8Array,Date,
    crypto:{subtle:{async digest(algorithm,bytes){if(options.digest)return options.digest(algorithm,bytes);const digest=crypto.createHash("sha256").update(Buffer.from(bytes)).digest();return digest.buffer.slice(digest.byteOffset,digest.byteOffset+digest.byteLength);}}},
    addEventListener(name,fn){listeners[name]=fn;},removeEventListener(name,fn){if(listeners[name]===fn)delete listeners[name];},
    setTimeout(fn,ms){const id=++timerID;timers.set(id,{fn,ms,due:clock+ms});return id;},clearTimeout(id){timers.delete(id);},console
  };
  if(options.openai)Object.assign(context.window.openai,options.openai);
  if(options.noWidgetState)delete context.window.openai.setWidgetState;
  // Test-only closure access verifies dispatcher rejection, including hidden targets.
  for (const body of scriptBodies.slice(0, -1)) vm.runInNewContext(body, context);
  vm.runInNewContext(script.replace('\n})();', '\nwindow.__testEvaluate=source=>eval(source);\n})();'),context);
  const send=(data,source=parent)=>listeners.message&&listeners.message({data,source});
  const initializePost=posts.find(v=>v.method==="ui/initialize");
  return {
    root,posts,events,trace,saved,context,send,initializePost,visibleMessages,
    evaluate(source){return context.window.__testEvaluate(source);},
    async advance(ms){clock+=ms;for(const [id,t] of [...timers])if(t.due<=clock&&(t.ms===10000||t.ms===15000)){timers.delete(id);t.fn();}await settle();},
    recallTimers(){return [...timers.values()].filter(t=>t.ms===10000);},
    hostReply(post,result={}){if(post.method==="ui/message"&&!result.isError)visibleMessages.push(structuredClone(post.params));send({jsonrpc:"2.0",id:post.id,result});},
    timeout(post){const id=rpcTimers.get(post.id),timer=timers.get(id);if(!timer)throw new Error("missing timeout");timers.delete(id);timer.fn();},
    expire(ms){for(const [id,t]of [...timers])if(t.ms===ms){timers.delete(id);t.fn();}},
    event(name,event){listeners[name]?.(event);},
    polls(){return [...timers].filter(([,v])=>v.ms===5000);},
    poll(){const [id,v]=this.polls()[0];timers.delete(id);v.fn();},
    visibility(hidden){context.document.hidden=hidden;listeners.visibilitychange();},
    init(){if(!options.manualIdentity){const state=options.widgetState||{};let binding=options.toolInput||(state.aiSessionId?{aiSessionId:state.aiSessionId,projectId:state.projectId||'project',featureAreaId:state.featureAreaId||'',taskId:state.taskId||'',presentationRequestId:'fixture-entry',entryIdentity:'fixture-entry',display:'EXPANDED'}:{presentationMode:'PROJECT_SELECTION',presentationRequestId:'fixture-selection',entryIdentity:'fixture-selection',display:'EXPANDED'});if(binding.presentationMode!=='PROJECT_SELECTION')binding={presentationMode:'WORKSPACE',display:'EXPANDED',presentationRequestId:'fixture-entry',entryIdentity:'fixture-entry',...binding};send({jsonrpc:'2.0',method:'ui/notifications/tool-input',params:{arguments:binding}});send({jsonrpc:'2.0',method:'ui/notifications/tool-result',params:{structuredContent:binding}});}send({jsonrpc:"2.0",id:initializePost.id,result:{hostCapabilities:options.hostCapabilities||{},hostContext:options.hostContext||{}}});},
    input(value,meta={}){send({jsonrpc:"2.0",method:"ui/notifications/tool-input",params:{arguments:value,_meta:meta}});},
    result(value,meta={}){send({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{structuredContent:value,_meta:meta}});},
    teardown(id=77){send({jsonrpc:"2.0",id,method:"ui/resource-teardown",params:{}});},
    reply(post,value={},extra={}){send({jsonrpc:"2.0",id:post.id,result:{structuredContent:value,...extra}});},
    reject(post){send({jsonrpc:"2.0",id:post.id,error:{message:"lost"}});},
    calls(){return posts.filter(v=>v.method==="tools/call");},
    dispose(){if(listeners.beforeunload)listeners.beforeunload();}
  };
}

const tick=()=>new Promise(resolve=>setImmediate(resolve));
async function settle(){await tick();await tick();}
async function until(read){for(let index=0;index<20;index++){const value=read();if(value)return value;await tick();}assert.fail("condition not reached");}
function button(h,label){const found=h.root.querySelectorAll("button").find(v=>(label==="↻"?v.className==="refresh-button":v.textContent===label));assert.ok(found,`missing button: ${label}`);return found;}
function findButton(h,label){return h.root.querySelectorAll("button").find(v=>(label==="↻"?v.className==="refresh-button":v.textContent===label));}
function selects(h){return h.root.querySelectorAll("select");}
function inputs(h){return h.root.querySelectorAll("input");}
function entry(extra={}) { return {presentationMode:"WORKSPACE",aiSessionId:"session",projectId:"project",featureAreaId:"area-a",taskId:"task-a",presentationRequestId:"fixture-entry",entryIdentity:"fixture-entry",display:"EXPANDED",...extra}; }
function projection(extra={}) { return {
  featureArea:{id:"area-a",name:"Area A"},lifecycleStatus:"ACTIVE",workflowStatus:"REVIEW_APPROVAL_PENDING",currentPhase:"APPROVAL",
  product:{featureAreaId:"area-a",featureAreaName:"Area A",domainStatus:"ACTIVE",internalStatus:"ACTIVE",currentTask:{id:"task-a",status:"ACTIVE",phase:"REVIEW_APPROVAL_PENDING"},taskPhase:"REVIEW_APPROVAL_PENDING",availableActions:[],technicalDetails:{internalStatus:"ACTIVE"}},
  progress:[{phase:"REVIEW",status:"COMPLETE"},{phase:"APPROVED",status:"PENDING"}],
  pendingDecision:{kind:"REVIEW_APPROVAL",id:"REVIEW_APPROVAL:review-a",subjectId:"task-a",summary:"Review Approval required."},
  blockers:[],capabilities:[{action:"APPROVE",available:true,target:{featureAreaId:"area-a",decisionId:"REVIEW_APPROVAL:review-a",subjectId:"task-a"}},{action:"REFRESH",available:true}],details:{featureAreaId:"area-a",taskId:"task-a"},...extra
}; }
function workspace(extra={}) { return {
  schemaVersion:"1",developmentContext:{project:{id:"project",name:"Apollo"},ready:true,repositories:[{repository:{id:"repo",name:"API"},developmentCycleId:"cycle",developmentRef:"refs/heads/dev",branch:"dev",revision:"abc",selectionRequired:false}]},
  featureAreas:[{id:"area-a",name:"Area A",status:"ACTIVE",product:{domainStatus:"ACTIVE"}},{id:"area-b",name:"Area B",status:"DISCOVERING",product:{domainStatus:"ACTIVE"}}],selectedFeatureAreaId:"area-a",
  tasks:[{id:"task-a",featureAreaId:"area-a",name:"Task A",status:"ACTIVE",phase:"REVIEW_APPROVAL_PENDING"},{id:"task-b",featureAreaId:"area-a",name:"Task B",status:"SUCCEEDED"}],selectedTaskId:"task-a",presentation:projection(),...extra
}; }
function operationFixture(selected){const w=selected.work||{};return{kind:w.type==='DEVELOPMENT'?'EXECUTION':w.type||'EXECUTION',operationId:w.ownerId||'operation-'+selected.id,ownerTaskId:w.type==='INSPECTION'?'':selected.id,runId:w.runId||'',status:w.status||selected.status,updatedAt:selected.updatedAt,readOnly:!!w.readOnly,backendDisposition:w.backendDisposition};}
function inventory(){return {projectId:"project",availableActions:["workspace_get","development_branch_select"],repositories:[{repositoryId:"repo",branches:[{fullRef:"refs/heads/dev",shortName:"dev",headSha:"abc"},{fullRef:"refs/heads/next",shortName:"next",headSha:"def"}]}]};}
function finishRefresh(h,offset,value=workspace(),branches=inventory()){h.reply(h.calls()[offset],value);if(h.calls()[offset+1]?.params.name==='workspace_get')h.reply(h.calls()[offset+1],{result:{branches}});}

// Explicit same-Card read-only details, using exact operation responses from a fixture.
async function showTaskDetails(h){
 const before=h.calls().length,work=button(h,'查看详情').onclick();await settle();
 for(const call of h.calls().slice(before))if(call.params.name==='operation_get')h.reply(call,{operation:{...call.params.arguments.target,status:'SUCCEEDED'},partitions:[],atomic:false});
 await work;return h;
}

test("unbound entry only reads the presented Session binding and never creates a Project context",async()=>{
  const h=boot();h.init();await tick();
  assert.deepEqual(h.calls().map(v=>v.params.name),[]);
  assert.match(h.root.textContent,/请在对话中明确选择 Project/);
  assert.equal(h.calls().some(v=>["project_list","project_open","session_initialize","session_bind_project"].includes(v.params.name)),false);
  h.dispose();
});

test("a complete workspace_show meta projection renders on first mount without duplicate reads",async()=>{
 const initial=workspace({refreshedAt:new Date().toISOString()});
 const h=boot({manualIdentity:true});
 // Tool result may arrive before initialize ACK.  A valid same-call meta snapshot is
 // authoritative enough to render once the host is ready.
 h.input({aiSessionId:"session",projectId:"project",featureAreaId:"area-a",taskId:"task-a",presentationRequestId:"show-1",entryIdentity:"entry-1",display:"EXPANDED"});
 h.result({aiSessionId:"session",projectId:"project",featureAreaId:"area-a",taskId:"task-a",presentationRequestId:"show-1",entryIdentity:"entry-1",display:"EXPANDED"},{workspace:initial});
 h.init();await settle();
 assert.equal(h.calls().length,0,"fresh workspace_show projection must not cause an initial card_get/workspace_get");
 assert.match(h.root.textContent,/Project · Apollo/);assert.match(h.root.textContent,/审查已完成，等待审批/);assert.doesNotMatch(h.root.textContent,/确认/);
 h.dispose();
});

test("details_show Task snapshot without an operation target renders without an initial reread",async()=>{
 const h=boot({manualIdentity:true}),snapshot=workspace({refreshedAt:new Date().toISOString()});
 const binding=entry({presentationMode:"DETAILS",readOnly:true,presentationRequestId:"details-task",entryIdentity:"details-task"});
 h.input(binding);h.result(binding,{workspace:snapshot});h.init();await settle();assert.equal(h.calls().length,0);assert.match(h.root.textContent,/Project · Apollo/);h.dispose();
});

test("details_show reuses same-read operation details when Core supplements a minimal target",async()=>{
 const h=boot({manualIdentity:true}),snapshot=workspace({refreshedAt:new Date().toISOString()});
 const target={kind:"REVIEW",operationId:"review-a"},binding=entry({presentationMode:"DETAILS",readOnly:true,target,presentationRequestId:"details-review",entryIdentity:"details-review"});
 const operationDetails={target:{kind:"REVIEW",operationId:"review-a",ownerTaskId:"task-a",runId:"run-a"},status:"SUCCEEDED",summary:"Exact historical review"};
 h.input(binding);h.result(binding,{workspace:snapshot,operationDetails});h.init();await settle();assert.equal(h.calls().length,0);assert.match(h.root.textContent,/Historical \/ read-only/);assert.doesNotMatch(h.root.textContent,/Approve|确认/);h.dispose();
});

test("missing or invalid show projection explicitly falls back to an exact interactive read",async()=>{
 const h=boot({manualIdentity:true});
 const binding=entry({presentationRequestId:"show-1",entryIdentity:"show-1"});h.input(binding);
 h.result(binding,{workspace:{developmentContext:{project:{id:"project"}},selectedFeatureAreaId:"other",selectedTaskId:"task-a"}});
 h.init();await tick();
 assert.deepEqual(h.calls()[0].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a",selectionMode:"EXACT",projectId:"project"}});
 finishRefresh(h,0);await settle();h.dispose();
});

test("initialization timeout recovers when a late valid show binding arrives",async()=>{
 const h=boot({manualIdentity:true});h.init();await settle();h.expire(8000);await settle();assert.match(h.root.textContent,/等待工具身份超时/);
 const binding=entry({presentationRequestId:"late-show",entryIdentity:"late-show",display:"COLLAPSED"});h.input(binding);
 h.result(binding,{workspace:workspace({refreshedAt:new Date().toISOString()})});
 await settle();assert.match(h.root.textContent,/Dev Bridge/);assert.equal(h.calls().length,0);h.dispose();
});

test("a collapsed exact running Task keeps one bounded monitor and updates in place",async()=>{
 const running=workspace({selectedWork:{kind:"REVIEW",operationId:"review-a",ownerTaskId:"task-a",runId:"run-a",status:"RUNNING"},presentation:projection({currentState:"IMPLEMENTATION_RUNNING",workflowStatus:"IMPLEMENTATION_RUNNING"})});
 const h=boot({toolInput:{aiSessionId:"session",projectId:"project",featureAreaId:"area-a",taskId:"task-a",presentationRequestId:"running",display:"COLLAPSED"}});h.init();await settle();finishRefresh(h,0,running);await settle();await h.advance(15000);
 assert.equal(h.calls().at(-1).params.name,"operation_get");h.reply(h.calls().at(-1),{status:"SUCCEEDED"});await settle();
 const completed=workspace({presentation:projection({currentState:"COMPLETE",workflowStatus:"COMPLETE",pendingDecision:null,capabilities:[]})});finishRefresh(h,3,completed);await settle();assert.equal(h.polls().length,0);assert.match(h.root.textContent,/只读/);h.dispose();
});

test("FeatureArea and Task selectors navigate by exact IDs without workflow mutation",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a",compact:true}});h.init();await tick();finishRefresh(h,0);await tick();
  assert.match(h.root.textContent,/Task A/);
  const pathSelects=selects(h).filter(v=>v.childNodes.some(option=>option.value==="area-a"||option.value==="task-a"));
  const initialTaskSelect=pathSelects.find(v=>v.childNodes.some(option=>option.value==="task-b"));initialTaskSelect.value="task-b";initialTaskSelect.onchange();await tick();
  assert.deepEqual(h.calls()[2].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-b",selectionMode:"EXACT",projectId:"project"}});finishRefresh(h,2,workspace({selectedTaskId:"task-b",presentation:projection({details:{featureAreaId:"area-a",taskId:"task-b"}})}));await tick();
  assert.match(h.root.textContent,/Task B/);
  const refreshedPathSelects=selects(h).filter(v=>v.childNodes.some(option=>option.value==="area-a"||option.value==="task-b"));
  const areaSelect=refreshedPathSelects.find(v=>v.childNodes.some(option=>option.value==="area-b"));areaSelect.value="area-b";areaSelect.onchange();await tick();
  assert.deepEqual(h.calls()[4].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-b",selectionMode:"EXACT",projectId:"project",taskId:""}});finishRefresh(h,4,workspace({selectedFeatureAreaId:"area-b",selectedTaskId:"",tasks:[],presentation:{...projection(),featureArea:{id:"area-b",name:"Area B"},details:{featureAreaId:"area-b"}}}));await tick();
  const taskSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="task-a"));assert.equal(taskSelect,undefined);
  const saved=h.saved.at(-1);assert.equal(saved.aiSessionId,"session");assert.equal(saved.featureAreaId,"area-b");assert.equal(saved.taskId,"");assert.equal(saved.projectId,"project");assert.equal(saved.cardEntry.presentationRequestId,"fixture-entry");assert.equal(saved.viewVersion,"read-only-v1");
});

test("bound Project is read-only and cannot open a replacement context",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  assert.match(h.root.textContent,/Project · Apollo/);assert.equal(h.root.querySelectorAll("button").some(v=>/Apollo|Switch Project|Change Project/.test(v.textContent)),false);assert.equal(h.calls().some(v=>v.params.name==="project_list"||v.params.name==="project_open"),false);
});

test("same-navigation Project mismatch preserves the last exact read-only projection",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  const refreshing=button(h,"↻").onclick(),wrong=workspace();wrong.developmentContext.project={id:"zeus",name:"Zeus"};finishRefresh(h,2,wrong);await refreshing;
  assert.match(h.root.textContent,/Apollo/);assert.doesNotMatch(h.root.textContent,/Zeus/);assert.match(h.root.textContent,/读取失败|Refresh failed/);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("unbound Card never creates a Session or exposes a Project selector",async()=>{
  const h=boot();h.init();await tick();
  assert.match(h.root.textContent,/请在对话中明确选择 Project/);assert.equal(selects(h).length,0);assert.equal(h.calls().length,0);assert.equal(h.posts.some(post=>post.method==='tools/call'),false);h.dispose();
});

function byClass(h,name){return [...h.root.querySelectorAll("div"),...h.root.querySelectorAll("span"),...h.root.querySelectorAll("section"),...h.root.querySelectorAll("header"),...h.root.querySelectorAll("details")].filter(v=>(v.className||"").split(" ").includes(name));}
function expandBranch(h){const b=h.root.querySelectorAll("button").find(v=>v.attributes["aria-label"]==="Development Branch");assert.ok(b);b.onclick();return b;}
test("Card is compact by default; Branch facts are disclosure-only",async()=>{
 const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();
 const value=workspace({presentation:projection({pendingDecision:null,capabilities:[]})});value.developmentContext.repositories[0].revision="4bcaba907f8a7868";value.developmentContext.repositories[0].refChanged=true;
 const branches=inventory();branches.repositories[0].repositoryCheckoutRef="refs/heads/main";branches.repositories[0].repositoryCheckoutMatchesDevelopmentRef=false;
 finishRefresh(h,0,value,branches);await tick();
 for(const label of ["DEV BRIDGE","Project · Apollo","Branch","dev","4bcaba9"])assert.ok(h.root.textContent.includes(label),label);
 assert.doesNotMatch(h.root.textContent,/No action required|Repository checkout|Selected development ref moved/);assert.equal(byClass(h,"decision").length,0);assert.equal(findButton(h,"Switch"),undefined);
 const before=h.calls().length;expandBranch(h);assert.match(h.root.textContent,/Repository checkout differs.*main/);assert.match(h.root.textContent,/Selected development ref moved/);assert.equal(findButton(h,"Switch"),undefined);assert.equal(h.calls().length,before);
 expandBranch(h);assert.equal(findButton(h,"Switch"),undefined);h.dispose();
});
test("active Direct File capability is visible in the current FeatureArea row",async()=>{
 const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();
 finishRefresh(h,0,workspace({presentation:projection({directFile:{enabled:true,workflowId:"workflow-a"}})}));await tick();
 const indicator=byClass(h,"db-direct-file-indicator")[0];assert.ok(indicator);assert.equal(indicator.textContent,"DIRECT_FILE 已启用");assert.match(indicator.title,/workflow-a/);assert.equal(indicator.attributes["aria-label"],"Direct File capability active");
 h.dispose();
});
test("FeatureArea Card consumes Core product status instead of raw lifecycle",async()=>{
 const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();
 const value=workspace();value.featureAreas[0].status="DISCOVERING";value.featureAreas[0].product={domainStatus:"ACTIVE",internalStatus:"DISCOVERING"};value.presentation.lifecycleStatus="DISCOVERING";value.presentation.product={...value.presentation.product,domainStatus:"ACTIVE",internalStatus:"DISCOVERING"};
 finishRefresh(h,0,value);await tick();
 assert.match(h.root.textContent,/领域 Area A 活跃/);assert.doesNotMatch(h.root.textContent,/理解中|DISCOVERING/);h.dispose();
});
test("Branch disclosure remains read-only when Core advertises creation capability",async()=>{
 const branches=inventory();branches.availableActions.push("development_branch_create");const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace(),branches);await tick();expandBranch(h);
 assert.equal(findButton(h,"Create"),undefined);assert.equal(inputs(h).length,0);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("manual historical Task focus survives Refresh",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-b"}});h.init();await tick();finishRefresh(h,0,workspace({selectedTaskId:"task-b",presentation:projection({pendingDecision:null,capabilities:[{action:"REFRESH",available:true}],details:{featureAreaId:"area-a",taskId:"task-b"}})}));await tick();
  const refreshing=button(h,"↻").onclick();assert.deepEqual(h.calls()[2].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-b",selectionMode:"EXACT",projectId:"project"}});finishRefresh(h,2,workspace({selectedTaskId:"task-b",presentation:projection({pendingDecision:null,capabilities:[{action:"REFRESH",available:true}],details:{featureAreaId:"area-a",taskId:"task-b"}})}));await refreshing;
  assert.match(h.root.textContent,/Task B/);assert.equal(h.saved.at(-1).taskId,"task-b");
});

test("Task navigation clears the old projection before its exact replacement returns",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  const taskSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="task-b"));taskSelect.value="task-b";taskSelect.onchange();await tick();
  assert.deepEqual(h.calls()[2].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-b",selectionMode:"EXACT",projectId:"project"}});
  assert.doesNotMatch(h.root.textContent,/终止执行|确认|Approve once/);
  h.reject(h.calls()[2]);h.reply(h.calls()[3],inventory());await settle();
  assert.match(h.root.textContent,/读取失败|Refresh failed/);assert.match(h.root.textContent,/Apollo/);assert.equal(h.saved.at(-1).taskId,"task-b");assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("a new workspace whose projection identity does not match its selected Task is rejected",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  const taskSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="task-b"));taskSelect.value="task-b";taskSelect.onchange();
  finishRefresh(h,2,workspace({selectedTaskId:"task-b",presentation:projection({details:{featureAreaId:"area-a",taskId:"task-a"}})}));await settle();
  assert.match(h.root.textContent,/读取失败|Refresh failed/);assert.match(h.root.textContent,/Apollo/);assert.equal(h.saved.at(-1).taskId,"task-b");assert.equal(findButton(h,"确认"),undefined);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("accepted workspace replacement keeps a single read-only Card and fresh navigation",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  const refreshing=button(h,"↻").onclick();finishRefresh(h,2);await refreshing;
  assert.equal(byClass(h,"card").length,1);assert.doesNotMatch(h.root.textContent,/确认|Approve once|终止执行/);const taskSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="task-b"));taskSelect.value="task-b";taskSelect.onchange();await tick();assert.equal(h.calls()[4].params.name,"dev_bridge_card_get");assert.equal(h.calls()[4].params.arguments.taskId,"task-b");h.dispose();
});

test("Review gate is dialogue-only even when Core advertises approval options",async()=>{
  for(const action of ["APPROVE","REJECT"]){
    const current=projection({capabilities:[{action,available:true,target:{featureAreaId:"area-a",decisionId:"REVIEW_APPROVAL:review-a",subjectId:"task-a"}}]});
    const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace({presentation:current}));await tick();
    assert.equal(findButton(h,"确认"),undefined);assert.equal(findButton(h,"拒绝"),undefined);assert.match(h.root.textContent,/请在对话中明确决定/);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
  }
});

test("Execution cancellation and recovery capabilities are displayed without mutation controls",async()=>{
  const cases=[projection({pendingDecision:null,capabilities:[{action:"CANCEL_EXECUTION",available:true,target:{featureAreaId:"area-a",executionId:"exec-a",runId:"run-a"}}]}),projection({pendingDecision:null,capabilities:[{action:"RECOVER",available:true,target:{featureAreaId:"area-a",operationKind:"EXECUTION",operationId:"exec-a",runId:"run-a"}}]})];
  for(const current of cases){
    const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace({presentation:current}));await tick();
    assert.equal(findButton(h,"终止执行"),undefined);assert.equal(findButton(h,"恢复运行"),undefined);assert.match(h.root.textContent,/请在对话中明确/);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
  }
});

test("FeatureArea navigation replaces the exact read target without exposing decision controls",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  const areaSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="area-b"));areaSelect.value="area-b";areaSelect.onchange();await tick();
  assert.deepEqual(h.calls()[2].params,{name:"dev_bridge_card_get",arguments:{aiSessionId:"session",featureAreaId:"area-b",taskId:"",selectionMode:"EXACT",projectId:"project"}});for(const label of ["确认","终止执行","恢复运行","Cancel action"])assert.equal(findButton(h,label),undefined);h.dispose();
});

test("Task review gate projects its exact Task and directs decisions to the conversation",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  assert.equal(findButton(h,"确认"),undefined);assert.match(h.root.textContent,/审查已完成，等待审批/);assert.match(h.root.textContent,/请在对话中明确/);const refresh=button(h,"↻").onclick();finishRefresh(h,2);await refresh;assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("mounted runtime approval capability remains a read-only exact projection",async()=>{
  const current=projection({pendingDecision:{kind:"RUNTIME_APPROVAL",id:"approval-decision",subjectId:"approval-a"},capabilities:[{action:"APPROVE_ONCE",available:true,target:{featureAreaId:"area-a",subjectId:"approval-a"}}]});
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace({presentation:current}));await tick();assert.equal(findButton(h,"Approve once"),undefined);assert.equal(h.calls().some(call=>call.params.name==="runtime_request_respond"),false);assert.match(h.root.textContent,/请在对话中明确/);h.dispose();
});

test("non-selected Project decision is excluded from current Card interaction",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();
  const pending={kind:"REVIEW_APPROVAL",owner:"TASK",taskId:"task-foreign",featureAreaId:"area-b",semanticState:"REVIEW_APPROVAL_PENDING",decision:{kind:"REVIEW_APPROVAL",id:"REVIEW_APPROVAL:review-b",subjectId:"task-foreign",summary:"Approve Area B"},options:[{action:"APPROVE",available:true,target:{featureAreaId:"area-b",taskId:"task-foreign",decisionId:"REVIEW_APPROVAL:review-b",subjectId:"task-foreign"}}],requiredInput:["requestId"]};
  finishRefresh(h,0,workspace({pendingDecisions:[pending]}));await tick();
  assert.doesNotMatch(h.root.textContent,/Pending Decisions|Approve Area B/);assert.equal(h.calls().length,2);h.dispose();
});

test("Task Card excludes a non-selected Codex runtime request",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();
  const request={projectId:"project",featureAreaId:"area-b",taskId:"task-foreign",runId:"run-b",threadId:"thread-b",turnId:"turn-b",connectionId:"connection-b",itemId:"item-b",requestId:"request-b",waitingState:"WAITING_FOR_PERMISSION",state:"PENDING",availableResponses:[{decision:"accept"}]};
  finishRefresh(h,0,workspace({pendingDecisions:[{kind:"RUNTIME_REQUEST",owner:"RUNTIME",taskId:"task-foreign",featureAreaId:"area-b",semanticState:"PENDING",runtimeRequest:request,options:[]}]}));await tick();
  assert.equal(findButton(h,'允许本次'),undefined);assert.doesNotMatch(h.root.textContent,/Pending Decisions/);assert.equal(h.calls().length,2);h.dispose();
});

test("Development Branch disclosure retains FeatureArea and Task navigation without switching",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();
  expandBranch(h);assert.equal(findButton(h,"Switch"),undefined);const taskSelect=selects(h).find(v=>v.childNodes.some(option=>option.value==="task-b"));taskSelect.value="task-b";taskSelect.onchange();await tick();assert.equal(h.calls()[2].params.arguments.featureAreaId,"area-a");assert.equal(h.calls()[2].params.arguments.taskId,"task-b");assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("workspace replacement keeps Branch disclosure read-only",async()=>{
  const branches=inventory();branches.availableActions.push("development_branch_create");const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace(),branches);await tick();expandBranch(h);
  const refreshing=button(h,"↻").onclick();finishRefresh(h,2,workspace(),branches);await refreshing;expandBranch(h);assert.equal(findButton(h,"Switch"),undefined);assert.equal(findButton(h,"Create"),undefined);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("foreign notifications cannot replace the mounted entry identity",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();const before=h.calls().length;
  const foreign=entry({aiSessionId:"foreign",projectId:"foreign-project",featureAreaId:"area-b",taskId:"task-b"});h.result(foreign);await tick();assert.equal(h.calls().length,before);assert.match(h.root.textContent,/展示入口身份冲突|上下文冲突/);h.dispose();
});

test("failed Refresh preserves the last authoritative workspace and remains retryable",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0);await tick();const before=h.root.textContent;
  const refreshing=button(h,"↻").onclick();h.reject(h.calls()[2]);h.reply(h.calls()[3],inventory());await refreshing;assert.match(h.root.textContent,/读取失败|Refresh failed/);assert.match(h.root.textContent,/Project · Apollo/);assert.match(before,/Task A/);assert.equal(button(h,"↻").disabled,false);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("active Execution cancellation is represented as read-only guidance",async()=>{
  const active=projection({pendingDecision:null,capabilities:[{action:"CANCEL_EXECUTION",available:true,target:{featureAreaId:"area-a",executionId:"exec",runId:"run"}},{action:"REFRESH",available:true}]});
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace({presentation:active}));await tick();
  assert.equal(findButton(h,"终止执行"),undefined);assert.match(h.root.textContent,/请在对话中明确/);const refreshing=button(h,"↻").onclick();finishRefresh(h,2,workspace({presentation:active}));await refreshing;assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});

test("teardown rejects pending reads and ignores their late results",async()=>{
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a"}});h.init();await tick();const pendingRead=h.calls()[0];h.teardown(91);await tick();assert.ok(h.posts.some(v=>v.id===91&&v.result));h.reply(pendingRead,workspace());await tick();assert.doesNotMatch(h.root.textContent,/Task A/);
});

test("the unified Card uses exact read RPCs for navigation and no mutation tools",()=>{
  assert.match(html,/dev_bridge_card_get/);assert.doesNotMatch(html,/ui_workflow_state_get|feature_area_details_get/);assert.match(html,/Progress/);
  assert.match(html,/\.workspace-grid\{display:grid;grid-template-columns:minmax\(0,1fr\)/);assert.doesNotMatch(html,/switchProject|toggleProjectPicker/);
});

test("Project inspection and orphan Review remain visible in the same Card",async()=>{
 for (const [type,scope,area,status,disposition] of [["INSPECTION","PROJECT","","RUNNING","ACTIVE"],["REVIEW","FEATURE_AREA","area-a","CANCELED","UNKNOWN_ORPHANED"]]) {
  const h=boot({widgetState:{aiSessionId:"session",featureAreaId:area,taskId:type==='INSPECTION'?'':'work-visible'}});h.init();await tick();
  const selected={id:"work-visible",featureAreaId:area,name:`Visible ${type}`,status,work:{type,scope,phase:type,status,ownerType:type,ownerId:"owner",runId:"exact-run",agent:"Codex",backendDisposition:disposition,tokenAvailability:"UNAVAILABLE",readOnly:status==="CANCELED"}};
  finishRefresh(h,0,workspace({presentationId:'workspace:session:project',selectedFeatureAreaId:area,selectedTaskId:type==='INSPECTION'?'':selected.id,tasks:[selected],selectedWork:operationFixture(selected),presentation:null}));await tick();
  for(const value of [type,status,disposition])assert.ok(h.root.textContent.includes(value),value);
  assert.doesNotMatch(h.root.textContent,/Select a FeatureArea|Loading selected workspace/);
  assert.equal(h.root.childNodes.length,1);
  h.dispose();
 }
});


test("retirement keeps historical navigation while remaining read-only",async()=>{
  for(const status of ["RETIRING","RETIRED"]) {
    const value=workspace({featureAreas:[{id:"area-a",name:"Area A",status,product:{domainStatus:"CLOSED"}}],presentation:projection({pendingDecision:null,capabilities:[]})});
    const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,value);await tick();
    assert.ok(selects(h).some(v=>v.options.some(o=>o.value==="task-b")));for(const label of ["Confirm","Revise","Start review","Execute","终止执行","确认关闭"])assert.equal(findButton(h,label),undefined);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
  }
});

test("Review Approval remains an exact read-only decision projection",async()=>{
 const decision={kind:"REVIEW_APPROVAL",id:"REVIEW_APPROVAL:review-a",subjectId:"task-a",summary:"等待实施审批",allowedActions:["APPROVE","REVISE","REJECT"]};
 const current=projection({pendingDecision:decision,currentPhase:"APPROVAL",workflowStatus:"REVIEW_APPROVAL_PENDING",nextAction:{code:"RESOLVE_REVIEW_APPROVAL",message:"等待实施审批"},capabilities:[{action:"APPROVE",available:true,target:{featureAreaId:"area-a",decisionId:decision.id,subjectId:decision.subjectId}},{action:"REFRESH",available:true}]});
 const h=boot({widgetState:{aiSessionId:"session",featureAreaId:"area-a",taskId:"task-a"}});h.init();await tick();finishRefresh(h,0,workspace({presentation:current}));await tick();
 assert.equal(findButton(h,"确认"),undefined);assert.equal(findButton(h,"拒绝"),undefined);assert.match(h.root.textContent,/等待审批|请在对话中明确/);assert.equal(h.calls().every(call=>["dev_bridge_card_get","workspace_get"].includes(call.params.name)),true);h.dispose();
});


const visualFixture=require('./testdata/card_visual_fixture.cjs');
async function mountVisual(state,long=false){const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:['REVIEW_REQUIRED','FEATURE_AREA_IDLE'].includes(state)?'':'task-a'}});h.init();await tick();finishRefresh(h,0,visualFixture.fixture(state,long),structuredClone(visualFixture.branches));await tick();return h;}
async function mountCurrentVisual(state,long=false){return mountCanonicalCard(visualFixture.fixture(state,long));}
test('one badge per focus row and full accessible names',async()=>{
 for(const state of ['REVIEW_RUNNING','IMPLEMENTATION_RUNNING','VERIFICATION_RUNNING','RETIRING','RETIRED','RECOVERY_REQUIRED']){
  const h=await mountCurrentVisual(state,true);assert.match(h.root.textContent,/Dynamic coding-agent model support/);assert.equal(h.root.querySelectorAll('button').some(node=>/确认|批准|拒绝|恢复运行/.test(node.textContent)),false);assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)));h.dispose();
 }
});
test('Review approval has one primary action; runtime permission and recovery remain actionable',async()=>{
 for(const state of ['REVIEW_APPROVAL_REQUIRED','RUNTIME_APPROVAL_REQUIRED','RECOVERY_REQUIRED','COMPLETE']){const h=await mountCurrentVisual(state);assert.equal(h.root.querySelectorAll('button').some(node=>/确认|批准|拒绝|恢复运行|Approve|Decline/.test(node.textContent)),false);assert.match(h.root.textContent,/请在对话中|只读/);h.dispose();}
});
test('Branch disclosure survives Refresh and navigation without persistent mutation',async()=>{
 const h=await mountVisual('REVIEW_APPROVAL_REQUIRED');expandBranch(h);const toggle=()=>h.root.querySelectorAll('button').find(v=>v.attributes['aria-label']==='Development Branch');assert.equal(toggle().attributes['aria-expanded'],'true');assert.ok(toggle().attributes['aria-controls']);
 let work=button(h,'↻').onclick();finishRefresh(h,2,visualFixture.fixture(),structuredClone(visualFixture.branches));await work;assert.equal(toggle().attributes['aria-expanded'],'true');
 const taskSelector=selects(h).find(v=>v.attributes['aria-label']==='Change task');assert.equal(taskSelector.value,'task-a');assert.ok(taskSelector.options.some(v=>v.value===''&&!v.disabled&&v.textContent==='Feature overview'));taskSelector.value='';taskSelector.onchange();await tick();const value=visualFixture.fixture();value.selectedTaskId='';value.presentation.details.taskId='';value.selectedWork=null;finishRefresh(h,4,value,structuredClone(visualFixture.branches));await tick();assert.equal(toggle().attributes['aria-expanded'],'false');assert.ok(h.calls().every(v=>['dev_bridge_card_get','workspace_get'].includes(v.params.name)));assert.equal(Object.keys(h.saved.at(-1)).some(v=>/branch/i.test(v)),false);h.dispose();
});
test('Branch disclosure never offers creation or checkout controls',async()=>{
 const h=await mountVisual('COMPLETE');expandBranch(h);assert.equal(findButton(h,'Create'),undefined);assert.equal(findButton(h,'Switch'),undefined);assert.equal(inputs(h).length,0);assert.equal(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)),true);h.dispose();
});

const markers=h=>byClass(h,'step-marker').map(v=>v.textContent);
test('contradictory identity generation sequence and stages retain the last consistent snapshot',async()=>{
 const cases=[v=>v.presentation.generation='other',v=>v.presentation.workflowGuidance.sourceSequence=9,v=>{v.presentation.sourceSequence=9;v.presentation.workflowGuidance.sourceSequence=9;},v=>v.presentation.details.projectId='other',v=>v.presentation.taskStages.taskId='other',v=>v.presentation.workflowGuidance.taskId='other',v=>v.presentation.agentRuntime.runId='other',v=>v.presentation.taskView.id='foreign',v=>v.presentation.taskStages.stages.pop()];
 for(const change of cases){const h=await mountVisual('IMPLEMENTATION_RUNNING');const before=markers(h);const v=visualFixture.fixture('VERIFICATION_RUNNING');change(v);const work=button(h,'↻').onclick();finishRefresh(h,2,v,visualFixture.branches);await work;assert.match(h.root.textContent,/Projection inconsistency/);assert.deepEqual(markers(h),before);h.dispose();}
 const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();const v=visualFixture.fixture('IMPLEMENTATION_RUNNING');v.presentation.taskView.id='foreign';finishRefresh(h,0,v,visualFixture.branches);await tick();assert.match(h.root.textContent,/Projection inconsistency/);assert.equal(byClass(h,'step').length,0);h.dispose();
});
test('late Task stage response cannot overwrite a newly selected Task',async()=>{
 const h=await mountVisual('IMPLEMENTATION_RUNNING'),old=button(h,'↻').onclick();
 const select=selects(h).find(v=>v.attributes['aria-label']==='Change task');select.value='task-b';select.onchange();await tick();const next=visualFixture.fixture('COMPLETE');next.selectedTaskId='task-b';next.tasks[0].id=next.selectedWork.ownerTaskId='task-b';next.presentation.details.taskId=next.presentation.workflowGuidance.taskId=next.presentation.taskStages.taskId='task-b';next.presentation.taskStages.title='Historical Task B';next.presentation.taskView.id='task-b';next.presentation.taskView.title='Historical Task B';
 finishRefresh(h,4,next,visualFixture.branches);await tick();finishRefresh(h,2,visualFixture.fixture('RECOVERY_REQUIRED'),visualFixture.branches);await old;
	 assert.match(h.root.textContent,/Historical Task B/);assert.deepEqual(markers(h),['✓','✓','✓','✓','✓']);assert.ok(!findButton(h,'恢复运行'));h.dispose();
});
test('clamped mixed-language titles preserve accessible full text and selectors remain lightweight',async()=>{
 const h=await mountVisual('IMPLEMENTATION_RUNNING',true);
 for(const row of byClass(h,'current-line')){const title=row.childNodes[0];assert.equal(title.attributes['aria-label'],title.title);assert.ok(title.title.length>30);}
 assert.match(html,/-webkit-line-clamp:2/);assert.match(html,/select:hover:not\(:disabled\)/);assert.match(html,/select:focus-visible/);assert.match(html,/@media\(max-width:480px\)/);h.dispose();
});

test('unavailable telemetry activity time does not show a fabricated age',async()=>{
 const value=visualFixture.fixture('IMPLEMENTATION_RUNNING');value.presentation.agentRuntime.lastActivityAt='0001-01-01T00:00:00Z';const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');const before=h.calls().length,work=button(h,'↻').onclick();await settle();await replyCanonicalRead(h,before,value);await work;assert.doesNotMatch(h.root.textContent,/0001|分钟前|秒前/);h.dispose();
});


// Reuse the same mounted DOM/transport harness with snapshots read from actual
// candidate Core/GPT processes (the optional Local MCP acceptance gate).
module.exports={boot,settle,finishRefresh};

test('same Task replacement Decision, Execution or runtime request does not inherit the old target draft',async()=>{
 const h=await mountCurrentVisual('REVIEW_APPROVAL_REQUIRED'),before=h.calls().length,refresh=button(h,'↻').onclick();await settle();const next=visualFixture.fixture('COMPLETE');await replyCanonicalRead(h,before,next);await refresh;assert.equal(h.root.querySelectorAll('input').length,0);assert.equal(h.root.querySelectorAll('textarea').length,0);h.dispose();
});

test('background read of the exact same target preserves typed draft without Loading',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');assert.equal(h.root.attributes['aria-busy'],'false');assert.equal(h.root.querySelectorAll('textarea').length,0);assert.ok(h.polls().length<=1);h.dispose();
});





test('template pills use shared tones without changing authoritative labels',async()=>{
 const tones={ACTIVE:'active',PREPARING:'active',IMPLEMENTING:'active',VERIFYING:'active',COMPLETE:'success',SUCCEEDED:'success',WAITING:'warning',RETIRING:'warning',POSSIBLY_STALLED:'warning',RECOVERY_REQUIRED:'warning',TASK_DELIVERING:'warning',TASK_RECOVERY_REQUIRED:'warning',GIT_FINALIZATION_PENDING:'warning',REVIEW_MATERIALIZING:'active',FEATURE_AREA_BUSY:'active',FAILED:'danger',CANCELED:'danger',REJECTED:'danger',READY:'neutral',REVIEW:'neutral',DEVELOPMENT:'neutral',RETIRED:'neutral'};
 for(const [status,tone] of Object.entries(tones)){
  const fn=script.match(/function statusPill\(status\)\{[\s\S]*?\n  \}/)[0];
  const pill=vm.runInNewContext('('+fn+')(status)',{status,text:String,statusLabel:v=>v,lifecycleLabel:v=>v,el:(tag,className,value)=>({className,textContent:value})});
  assert.ok(pill.className.split(' ').includes('status-'+tone));assert.equal(pill.title,status);
 }
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');assert.doesNotMatch(h.root.textContent,/undefined|NaN/);h.dispose();
});

test('durable delivery and recovery phases expose Card phase labels and stage vocabulary',()=>{
 const phases=vm.runInNewContext('('+script.match(/const PHASE_LABELS=(\{[^}]*\});/)[1]+')');
 assert.equal(phases.DELIVERING,'交付中');assert.equal(phases.RECOVERY_REQUIRED,'需要恢复');
 const short=vm.runInNewContext('('+script.match(/const PHASE_SHORT_LABELS=(\{[^}]*\});/)[1]+')');
 assert.equal(short.DELIVERING,'交付');assert.equal(short.RECOVERY_REQUIRED,'恢复');
 for(const phase of Object.keys(phases))if(['REVIEW_REQUIRED','REVIEWING','REVIEW_APPROVAL_PENDING','IMPLEMENTATION_READY','IMPLEMENTING','VERIFICATION_READY','VERIFYING','ACCEPTANCE_PENDING'].includes(phase))assert.ok(short[phase],`short label kept for ${phase}`);
});

test('terminal Task status takes precedence over the retained delivery phase',()=>{
 const fn=script.match(/function taskPhaseLabel\(v\)\{[\s\S]*?\n  \}/)[0];
 const PHASE_LABELS={DELIVERING:'交付中',VERIFYING:'验证阶段'};
 const label=v=>vm.runInNewContext('('+fn+')(v)',{v,PHASE_LABELS});
 assert.equal(label({status:'SUCCEEDED',phase:'DELIVERING'}),'已完成');
 assert.equal(label({status:'ACTIVE',phase:'DELIVERING'}),'交付中');
 assert.equal(label({status:'ACTIVE',phase:'VERIFYING'}),'验证阶段');
 assert.equal(label({status:'CANCELED',phase:'DELIVERING'}),'已取消');
 assert.equal(label({status:'FAILED',phase:'DELIVERING'}),'已失败');
});

test('completed Task uses its final status in expanded and collapsed views despite retained delivery phase',async()=>{
 const h=await mountVisual('COMPLETE');
 h.evaluate("workspace.presentation.taskView.status='SUCCEEDED';workspace.presentation.taskView.phase='DELIVERING';render()");
 assert.match(byClass(h,'db-task-heading')[0].textContent,/已完成/);
 assert.match(byClass(h,'db-task-meta')[0].textContent,/已完成/);
 assert.doesNotMatch(byClass(h,'db-task-meta')[0].textContent,/交付/);
 h.evaluate('expanded=false;render()');assert.match(h.root.textContent,/已完成/);assert.doesNotMatch(h.root.textContent,/交付中/);h.dispose();
});


test('manual refresh exposes presentation busy state and clears it on completion',async()=>{
 const h=await mountVisual('IMPLEMENTATION_RUNNING'),refreshButton=button(h,'↻');
 const pending=refreshButton.onclick();assert.equal(refreshButton.attributes['aria-busy'],'true');
 finishRefresh(h,2,visualFixture.fixture('IMPLEMENTATION_RUNNING'),visualFixture.branches);await pending;
 assert.equal(refreshButton.attributes['aria-busy'],'false');h.dispose();
});

test('Task-first Card has one semantic rail, aggregate usage, primary Result and isolated diagnostics',async()=>{
 const h=await mountVisual('COMPLETE');assert.deepEqual(byClass(h,'step-name').map(n=>n.textContent),['审查','审批','实施','验证','完成']);
 assert.equal(byClass(h,'task-result').length,0,'result stays behind explicit details');await showTaskDetails(h);assert.equal(byClass(h,'task-result').length,1);assert.match(byClass(h,'task-result')[0].textContent,/SUCCEEDED/);
 assert.match(h.root.textContent,/31:52/);assert.match(h.root.textContent,/23:50/);assert.match(h.root.textContent,/8:02/);assert.match(h.root.textContent,/Token 用量无法完整可靠归因/);
 assert.match(h.root.textContent,/Goal Progress/);assert.match(h.root.textContent,/Native Plan/);assert.match(h.root.textContent,/Verification Run · SUCCEEDED/);assert.match(h.root.textContent,/Task acceptance · ACCEPTED/);assert.match(h.root.textContent,/Agent reported · BLOCKED/);
 assert.equal(h.polls().length,0);assert.ok(!findButton(h,'终止执行'));assert.ok(!findButton(h,'恢复运行'));assert.equal(findButton(h,'开始执行'),undefined);assert.equal(findButton(h,'继续验证'),undefined);assert.doesNotMatch(html,/verification_start|Preparation/);h.dispose();
});
test('opened Task polls only exact task_presentation_get then stops at terminal; overview never polls',async()=>{
 const h=await mountVisual('IMPLEMENTATION_RUNNING');assert.equal(h.polls().length,1);h.poll();const call=h.calls().at(-1);assert.equal(call.params.name,'dev_bridge_card_get');assert.equal(call.params.arguments.taskId,'task-a');
 h.reply(call,visualFixture.fixture('COMPLETE'));await settle();assert.equal(h.polls().length,0);assert.equal(byClass(h,'task-result').length,0);await showTaskDetails(h);assert.match(byClass(h,'task-result')[0].textContent,/SUCCEEDED/);h.dispose();
 const overview=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a'}});overview.init();await settle();const v=visualFixture.fixture('IMPLEMENTATION_RUNNING');v.selectedTaskId='';finishRefresh(overview,0,v);await settle();assert.equal(overview.polls().length,0);overview.dispose();
});
test('waiting Task does not poll; navigation remains fenced',async()=>{
 const h=await mountCurrentVisual('WAITING_FOR_PERMISSION');assert.equal(h.polls().length,1);h.poll();const read=h.calls().at(-1);assert.deepEqual(read.params,{name:'dev_bridge_card_get',arguments:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a',selectionMode:'EXACT',projectId:'project'}});h.reply(read,visualFixture.fixture('WAITING_FOR_PERMISSION'));await settle();assert.equal(h.root.querySelectorAll('button').some(node=>/允许|拒绝|Approve|Decline/.test(node.textContent)),false);assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)));h.dispose();
});
test('no native plan does not hide Codex activity or structured Goal Progress',async()=>{
 const value=visualFixture.fixture('IMPLEMENTATION_RUNNING');delete value.presentation.taskView.activity.nativePlan;const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');const before=h.calls().length,refresh=button(h,'↻').onclick();await settle();await replyCanonicalRead(h,before,value);await refresh;assert.match(h.root.textContent,/正在执行命令|EXECUTION/);assert.doesNotMatch(h.root.textContent,/%/);assert.equal(h.root.querySelectorAll('button').some(node=>/终止|恢复|开始执行/.test(node.textContent)),false);h.dispose();
});
test('Card supports light and dark grouped surfaces with no duplicate stage rail',()=>{
 assert.match(html,/:root\[data-theme="dark"\]/);assert.match(html,/@media\(prefers-color-scheme:dark\)/);assert.match(html,/task-outcomes/);assert.match(html,/task-advanced/);assert.doesNotMatch(html,/appendStepper\(stages/);
});


test('idle FeatureArea keeps latest result separate, exposes canonical next Task/reset/retire and never polls',async()=>{
 const h=await mountCurrentVisual('FEATURE_AREA_IDLE');assert.match(h.root.textContent,/当前无运行中的 Task|最近 Task/);assert.equal(h.polls().length,0);assert.equal(h.root.querySelectorAll('button').some(node=>/Create Task|Reset|关闭功能领域/.test(node.textContent)),false);assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)));h.dispose();
});
test('Card Retire is absent from the read-only Card',async()=>{
 const h=await mountCanonicalCard(visualFixture.fixture('FEATURE_AREA_IDLE'));assert.equal(findButton(h,'确认关闭'),undefined);assert.equal(h.calls().some(v=>v.params.name==='feature_area_lifecycle'),false);h.dispose();
});
test('Card suppresses Retire when the authoritative lifecycle is closed even if a stale capability says available',async()=>{
 for(const lifecycleStatus of ['RETIRING','RETIRED','DECOMPOSED']){
  const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a'}});h.init();await tick();const value=visualFixture.fixture('FEATURE_AREA_IDLE');value.presentation.lifecycleStatus=lifecycleStatus;value.presentation.capabilities=[{action:'RETIRE',available:true}];finishRefresh(h,0,value,structuredClone(visualFixture.branches));await tick();
  assert.equal(h.root.querySelectorAll('summary').some(v=>v.textContent==='关闭功能领域'),false,lifecycleStatus);assert.equal(h.calls().some(v=>v.params.name==='feature_area_lifecycle'),false);h.dispose();
 }
});
test('Card Retire confirmation never delegates to window.confirm',()=>assert.doesNotMatch(html,/window\.confirm/));
test('native request answers are not embedded Card controls',async()=>{
 const value=visualFixture.fixture('WAITING_FOR_PERMISSION');
 const request={state:'PENDING',method:'item/commandExecution/requestApproval',waitingState:'WAITING_FOR_PERMISSION',projectId:'project',featureAreaId:'area-a',taskId:'task-a',runId:'run-implementation',threadId:'thread-a',turnId:'turn-a',connectionId:'epoch-a',itemId:'item-a',requestId:'request-a',payload:{reason:'Run the authorized tests'},availableResponses:[{decision:'accept'}]};value.presentation.taskView.activity.runtime={requests:[request]};
 const h=await mountCanonicalCard(value);
 assert.equal(findButton(h,'允许本次'),undefined);assert.equal(h.calls().some(v=>v.params.name==='runtime_request_respond'),false);h.dispose();
});

test('historical verification mutation shows transport failure and unrecorded acceptance',async()=>{
 const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();
 const data=visualFixture.fixture('COMPLETE'),v=data.presentation.taskView;
 v.status=v.overallState='FAILED';v.result={status:'FAILED',summary:'Historical failure'};
 v.implementationRun={id:'impl',transportStatus:'SUCCEEDED'};
 v.verificationRun={id:'verify',transportStatus:'INFRASTRUCTURE_FAILED',failureCode:'VERIFICATION_MUTATED_WORKTREE'};
 v.verificationReport={availability:'UNAVAILABLE',summary:'No valid report'};
 v.currentExecutionAcceptance={status:'LEGACY_UNRECORDED',allowedActions:[]};
 finishRefresh(h,0,data,visualFixture.branches);await tick();await showTaskDetails(h);
 for(const fact of ['Implementation Run · SUCCEEDED','Verification Run · INFRASTRUCTURE_FAILED','VERIFICATION_MUTATED_WORKTREE','UNAVAILABLE','LEGACY_UNRECORDED'])assert.ok(h.root.textContent.includes(fact),fact);
 h.dispose();
});

test('auto-commit status is a read-only projection without a Card command',async()=>{
 const h=await mountCanonicalCard(visualFixture.fixture('COMPLETE'));assert.equal(h.calls().some(v=>/commit|feature_area_lifecycle/.test(v.params.name)),false);assert.equal(h.root.querySelectorAll('button').some(v=>/提交/.test(v.textContent)),false);h.dispose();
});
test('FeatureArea submission state stays selected and read-only',async()=>{
 const v=visualFixture.fixture('FEATURE_AREA_IDLE'),h=await mountCanonicalCard(v);assert.equal(h.calls().every(c=>['dev_bridge_card_get','workspace_get'].includes(c.params.name)),true);assert.equal(h.calls()[0].params.arguments.featureAreaId,'area-a');h.dispose();
});

test('current workspace keeps exact entry selection when server remembered selection changes',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');const before=h.calls().length,refresh=button(h,'↻').onclick();await settle();const stale=visualFixture.fixture('IMPLEMENTATION_RUNNING');stale.selectedFeatureAreaId='area-b';stale.selectedTaskId='';stale.presentation.featureArea.id='area-b';stale.presentation.details.featureAreaId='area-b';await replyCanonicalRead(h,before,stale);await refresh;assert.match(h.root.textContent,/Refresh failed|Projection inconsistency/);assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)));h.dispose();
});

test('current workspace refresh failure preserves content and last successful timestamp',async()=>{
 const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();
 finishRefresh(h,0,workspace({presentationId:'workspace:session:project',refreshedAt:'2026-09-11T01:02:00Z'}));await tick();
 const stamp=h.root.querySelectorAll('p').find(v=>v.className==='refresh-status').textContent;
 const refreshing=h.context.window.devBridgeCard.refresh();h.reject(h.calls()[2]);h.reply(h.calls()[3],inventory());await refreshing;
 assert.match(h.root.textContent,/刷新失败/);assert.match(h.root.textContent,/Task A/);
 assert.equal(h.root.querySelectorAll('p').find(v=>v.className==='refresh-status').textContent,stamp);
 assert.equal(h.root.childNodes.length,1);h.dispose();
});


test('initial Project layout separates intro, label and picker without workspace placeholders',async()=>{
 const h=boot({manualIdentity:true});h.init();h.input({presentationMode:'PROJECT_SELECTION',presentationRequestId:'selection',entryIdentity:'selection',display:'EXPANDED'});h.result({presentationMode:'PROJECT_SELECTION',presentationRequestId:'selection',entryIdentity:'selection',display:'EXPANDED'});await settle();assert.match(h.root.textContent,/请在对话中明确选择 Project/);assert.equal(h.calls().length,0);assert.equal(h.root.querySelectorAll('select').length,0);h.dispose();
});

test('modern dashboard preserves status values and refresh retry uses the existing read path',async()=>{
 const h=await mountCurrentVisual('REVIEW_APPROVAL_REQUIRED');assert.match(h.root.textContent,/审查已完成，等待审批/);const before=h.calls().length,refresh=button(h,'↻').onclick();await settle();await replyCanonicalRead(h,before,visualFixture.fixture('REVIEW_APPROVAL_REQUIRED'));await refresh;assert.doesNotMatch(h.root.textContent,/undefined|NaN|刷新失败/);h.dispose();
});

test('branch copy is presentation only and reports clipboard failure accessibly',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');assert.match(h.root.textContent,/Branch|dev_20260908/);assert.equal(h.root.querySelectorAll('button').some(node=>/Copy|复制/.test(node.textContent)),false);assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get'].includes(call.params.name)));h.dispose();
});

test("Review recovery is presented without a Development Task or recovery command",async()=>{
 const h=await mountCanonicalCard(visualFixture.fixture('REVIEW_APPROVAL_REQUIRED'));
 const selected={id:'review-work',featureAreaId:'area-a',name:'Review recovery',status:'FAILED',work:{type:'REVIEW',phase:'REVIEW',status:'FAILED',runStatus:'INFRASTRUCTURE_FAILED'}};
 const value=workspace({presentationId:'workspace:session:project',selectedTaskId:'',selectedWork:operationFixture(selected),tasks:[selected],featureAreas:[{id:'area-a',name:'Area A',status:'DISCOVERING',availability:'AVAILABLE',reviewStatus:'INFRASTRUCTURE_FAILED'}],presentation:projection({details:{featureAreaId:'area-a',taskId:''},currentState:'RECOVERY_REQUIRED',currentPhase:'REVIEW',workflowStatus:'RECOVERY_REQUIRED',nextAction:{code:'RECOVER_AGENT_OPERATION',message:'需要恢复 Review'},pendingDecision:null,capabilities:[],progress:[{phase:'REVIEW',status:'FAILED'}]})});
 assert.equal(h.calls().some(v=>/recover|execution_start/.test(v.params.name)),false);assert.equal(findButton(h,'恢复'),undefined);h.dispose();
});

test("selected detail failure accepts fresh inventory and clears stale actions; authoritative failure stays global",async()=>{
 const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();
 const value=workspace({presentationId:'workspace:session:project'});finishRefresh(h,0,value);await tick();
 const selected={id:'task-a',featureAreaId:'area-a',name:'Fresh development summary',status:'RUNNING',work:{type:'DEVELOPMENT',phase:'IMPLEMENTATION',status:'RUNNING'}};
 const fresh={...value,presentation:null,selectedWork:operationFixture(selected),tasks:[selected],selectedWorkReadError:'当前工作详情暂时不可用',developmentContext:{...value.developmentContext,repositories:[{...value.developmentContext.repositories[0],revision:'fresh-sha'}]}};
 const refreshing=button(h,'↻').onclick();finishRefresh(h,2,fresh);await refreshing;
 assert.match(h.root.textContent,/当前工作详情暂时不可用/);assert.match(h.root.textContent,/Fresh development summary/);assert.match(h.root.textContent,/fresh-s/);assert.doesNotMatch(h.root.textContent,/Refresh failed|刷新异常/);assert.equal(findButton(h,'Confirm'),undefined);
 const failed=button(h,'↻').onclick();finishRefresh(h,4,{...fresh,readError:'inventory unavailable'});await failed;
 assert.match(h.root.textContent,/Refresh failed; showing the last successful projection/);assert.match(h.root.textContent,/Fresh development summary/);h.dispose();
});

test("Review and Inspection summary-only navigation uses an empty selectedTaskId",async()=>{
 for(const type of ['REVIEW','INSPECTION']) {
  const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:''}});h.init();await tick();
  const selected={id:'work',featureAreaId:'area-a',name:`Current ${type}`,status:'RUNNING',work:{type,phase:type,status:'RUNNING'}};
  finishRefresh(h,0,workspace({presentationId:'workspace:session:project',selectedTaskId:'',selectedWork:operationFixture(selected),tasks:[selected],presentation:null}));await tick();
  assert.match(h.root.textContent,new RegExp(type));assert.doesNotMatch(h.root.textContent,/Refresh failed|Loading selected workspace|unavailable/);assert.equal(h.saved.at(-1).taskId,'');h.dispose();
 }
});

test('compact header, independent Feature row and primary Task panel preserve accessible icon selectors',async()=>{
 const h=await mountVisual('IMPLEMENTATION_RUNNING',true);
 const header=byClass(h,'card-header')[0];assert.match(header.textContent,/Project · devbridge/);assert.match(header.textContent,/Branch ·/);assert.match(header.textContent,/da0b6a3/);
 for(const legacy of ['branch-section','workspace-grid','db-status-strip','db-activity'])assert.equal(byClass(h,legacy).length,0);
 assert.equal(byClass(h,'db-feature-row').length,1);assert.equal(byClass(h,'db-task-primary').length,1);
 for(const kind of ['feature','task']){const picker=byClass(h,'db-icon-picker').find(n=>n.querySelectorAll('select').some(s=>s.attributes['aria-label']==='Change '+kind));assert.ok(picker);assert.equal(picker.childNodes[0].attributes['aria-hidden'],'true');}
 assert.deepEqual(byClass(h,'step-name').map(n=>n.textContent),['审查','审批','实施','验证','完成']);h.dispose();
});

test('six primary labels map canonical states without reinterpreting raw Run or Task status',async()=>{
 for(const state of ['REVIEW_RUNNING','IMPLEMENTATION_RUNNING','VERIFICATION_RUNNING','COMPLETE','FAILED']){const h=await mountCurrentVisual(state);assert.match(h.root.textContent,/审查|EXECUTION|SUCCEEDED|FAILED/);assert.equal(h.root.querySelectorAll('button').some(node=>/确认|批准|拒绝/.test(node.textContent)),false);h.dispose();}
});

test('Task Review uses shared rail and retains semantic Task selection',async()=>{
 for(const state of ['REVIEW_RUNNING','REVIEW_APPROVAL_REQUIRED']){const h=await mountCurrentVisual(state);assert.match(h.root.textContent,/审查/);assert.deepEqual(h.calls().map(call=>call.params.name),['dev_bridge_card_get','workspace_get']);h.dispose();}
});

test('official native plan counts are displayed separately and never converted to percentage',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');assert.doesNotMatch(h.root.textContent,/%/);assert.equal(h.root.querySelectorAll('progress').length,0);assert.match(h.root.textContent,/任务阶段|EXECUTION/);h.dispose();
});

test('branch inventory enrichment failure accepts fresh workspace without a stale error or stale branch actions',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING'),before=h.calls().length,update=button(h,'↻').onclick();await settle();const value=visualFixture.fixture('VERIFICATION_RUNNING');const card=h.calls().slice(before).find(call=>call.params.name==='dev_bridge_card_get');h.reply(card,value);await settle();const inventoryRead=h.calls().slice(before).find(call=>call.params.name==='workspace_get');h.reject(inventoryRead);await update;assert.match(h.root.textContent,/验证|EXECUTION/);assert.equal(findButton(h,'Switch'),undefined);h.dispose();
});

test('Feature commit action stays in its menu and Task commit integration stays in metadata and Details',async()=>{
 const h=await mountCurrentVisual('IMPLEMENTATION_RUNNING');assert.equal(h.root.querySelectorAll('button').some(node=>/提交到 dev|Commit/.test(node.textContent)),false);assert.match(h.root.textContent,/提交 \/ Dev|Dev/);h.dispose();
});

test('missing semantic facts stay neutral and Review runtime summary can expose official plan counts',async()=>{
 const value=visualFixture.fixture('REVIEW_RUNNING');value.presentation.taskView.progress=[{phase:'REVIEW',status:'RUNNING'}];delete value.presentation.taskView.activity.nativePlan;const h=await mountCanonicalCard(value);assert.doesNotMatch(h.root.textContent,/undefined|NaN|%/);assert.match(h.root.textContent,/审查/);h.dispose();
});

const recallCaps={devBridgePresentation:{visibility:true,recall:true}};
async function mountReadOnlyVisibilityCard(){
 const value=visualFixture.fixture('IMPLEMENTATION_RUNNING');value.presentationId='workspace:session:project';
 const h=await mountCanonicalCard(value,{hostCapabilities:recallCaps});
 h.viewport=visibility=>h.send({jsonrpc:'2.0',method:'ui/notifications/dev-bridge-presentation-visibility',params:{presentationId:value.presentationId,visibility}});
 h.legacyRecalls=()=>h.posts.filter(post=>post.method==='ui/dev-bridge/recall-presentation');
 return {h,value};
}
function assertLegacyRecallAbsent(h){
 assert.equal(h.legacyRecalls().length,0);assert.equal(h.posts.some(post=>post.method==='ui/message'),false);
 assert.equal(h.recallTimers().length,0);assert.equal(inputs(h).some(input=>input.attributes.role==='switch'),false);
 assert.ok(h.calls().every(call=>['dev_bridge_card_get','workspace_get','operation_get'].includes(call.params.name)));
}
async function replyCanonicalRead(h,offset,value){
 const card=h.calls().slice(offset).find(call=>call.params.name==='dev_bridge_card_get');assert.ok(card,'missing Card read');h.reply(card,value);await settle();
 const inventoryRead=h.calls().slice(offset).find(call=>call.params.name==='workspace_get');if(inventoryRead)h.reply(inventoryRead,{result:{branches:visualFixture.branches}});await settle();
}
test('read-only-v1 retires the auto-recall preference switch on canonical mount',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();assertLegacyRecallAbsent(h);assert.equal(h.saved.some(value=>value.privateContent?.cardPreferences),false);h.dispose();
});
test('read-only-v1 visibility transitions never schedule a legacy ten-second recall',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('VISIBLE');h.viewport('OFFSCREEN');await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 has no legacy recall timer to cancel on visible, hidden, completion, or replacement',async()=>{
 for(const trigger of ['VISIBLE','HIDDEN','OFFSCREEN']){const {h}=await mountReadOnlyVisibilityCard();h.viewport(trigger);await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();}
});
test('read-only-v1 cannot enable or rearm the retired auto-recall capability while offscreen',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('OFFSCREEN');await h.advance(50000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 ignores foreign visibility telemetry without a legacy recall side effect',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.send({jsonrpc:'2.0',method:'ui/notifications/dev-bridge-presentation-visibility',params:{presentationId:'foreign',visibility:'OFFSCREEN'}});await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 task navigation preserves the absence of auto-recall controls',async()=>{
 const {h,value}=await mountReadOnlyVisibilityCard(),select=selects(h).find(node=>node.attributes['aria-label']==='Change task'),before=h.calls().length;select.value='task-b';select.onchange();await settle();const next=structuredClone(value);next.selectedTaskId='task-b';next.tasks[0].id='task-b';next.selectedWork.ownerTaskId='task-b';next.presentation.taskView.id='task-b';next.presentation.details.taskId='task-b';await replyCanonicalRead(h,before,next);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 refresh failure cannot create a legacy recall request',async()=>{
 const {h}=await mountReadOnlyVisibilityCard(),before=h.calls().length,refresh=button(h,'↻').onclick();await settle();for(const call of h.calls().slice(before))h.reject(call);await refresh;assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 repeated hidden and offscreen telemetry remains side-effect free',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('HIDDEN');for(let index=0;index<3;index++){h.viewport('OFFSCREEN');await h.advance(30000);}assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 hidden-to-visible telemetry does not create a replacement recall epoch',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('VISIBLE');h.viewport('OFFSCREEN');await h.advance(9000);h.viewport('HIDDEN');h.viewport('VISIBLE');h.viewport('OFFSCREEN');await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 repeated visibility rearming remains free of a recall timer',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();for(const visibility of ['HIDDEN','VISIBLE','HIDDEN','OFFSCREEN'])h.viewport(visibility);await h.advance(30000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 duplicate offscreen events have no legacy deadline to preserve',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('VISIBLE');h.viewport('OFFSCREEN');await h.advance(5000);h.viewport('OFFSCREEN');h.viewport('OFFSCREEN');await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 hidden telemetry has no queued recall callback to dispatch',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('VISIBLE');h.viewport('OFFSCREEN');h.viewport('HIDDEN');await h.advance(30000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 hidden telemetry cannot leave a recall read in flight',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('VISIBLE');h.viewport('OFFSCREEN');await h.advance(10000);h.viewport('HIDDEN');assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 old presentation visibility cannot trigger legacy recall for the current Card',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.send({jsonrpc:'2.0',method:'ui/notifications/dev-bridge-presentation-visibility',params:{presentationId:'old-presentation',visibility:'HIDDEN'}});await h.advance(10000);assertLegacyRecallAbsent(h);h.dispose();
});
test('read-only-v1 refresh and visibility changes cannot restore the retired recall preference',async()=>{
 const {h}=await mountReadOnlyVisibilityCard();h.viewport('HIDDEN');const before=h.calls().length,refresh=button(h,'↻').onclick();await settle();await replyCanonicalRead(h,before,visualFixture.fixture('IMPLEMENTATION_RUNNING'));await refresh;assertLegacyRecallAbsent(h);h.dispose();
});


// The read-only-v1 Card accepts authority only from the host's canonical
// tool binding. This helper is intentionally not a compatibility shim for
// widgetState: every caller supplies the same tool-input/tool-result pair
// that production receives, then responds only to the real read requests.
async function mountCanonicalCard(value=visualFixture.fixture('REVIEW_APPROVAL_REQUIRED'),extra={}){
 const h=boot({manualIdentity:true,...extra});
 const identity={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:value.selectedFeatureAreaId||'area-a',taskId:value.selectedTaskId||'',presentationRequestId:'base-readonly',entryIdentity:'base-readonly',display:'EXPANDED'};
 h.init();h.input(identity);h.result(identity);await settle();
 const reads=h.calls();assert.equal(reads[0]?.params.name,'dev_bridge_card_get');h.reply(reads[0],value);
 if(reads[1]){assert.equal(reads[1].params.name,'workspace_get');h.reply(reads[1],{result:{branches:visualFixture.branches}});}
 await settle();return h;
}

test('Card presents the canonical Review state without reviving legacy review controls',async()=>{
 const h=await mountCanonicalCard();
 assert.match(h.root.textContent,/审查已完成，等待审批/);
 assert.match(h.root.textContent,/请在对话中明确决定/);
 assert.equal(h.root.querySelectorAll('button').some(v=>/审查|批准|拒绝|确认/.test(v.textContent)),false);
 assert.equal(h.calls().every(v=>['dev_bridge_card_get','workspace_get'].includes(v.params.name)),true);
 h.dispose();
});

function acceptanceWorkspace(){
 const value=workspace(),p=value.presentation;
 p.pendingDecision={kind:'TASK_ACCEPTANCE',id:'TASK_ACCEPTANCE:execution-a',subjectId:'execution-a',summary:'Acceptance'};
 p.taskView={...visualFixture.fixture().presentation.taskView,id:'task-a',title:'Task A',phase:'ACCEPTANCE_PENDING',currentExecution:{id:'execution-a',taskDefinitionRevisionId:'revision-a'},actions:['ACCEPT','REJECT','REQUEST_CHANGES','REQUEST_REVERIFICATION'].map(action=>({action,available:true,target:{taskId:'task-a',featureAreaId:'area-a',executionId:'execution-a',subjectId:'execution-a',taskDefinitionRevisionId:'revision-a',decisionId:p.pendingDecision.id}}))};
 return value;
}
for(const [option,label] of [['ACCEPT','验收'],['REJECT','拒绝'],['REQUEST_CHANGES','确认要求修改'],['REQUEST_REVERIFICATION','重新验证']])test(`Card Acceptance ${option} is read-only and never creates a replacement command`,async()=>{
 const value=acceptanceWorkspace(),h=await mountCanonicalCard(value);
 assert.equal(h.root.querySelectorAll('button').some(v=>v.textContent===label),false);
 assert.equal(h.calls().some(v=>['decision_respond','execution_start','task_create'].includes(v.params.name)),false);
 assert.match(h.root.textContent,/请在对话中明确决定|只读浏览/);
 h.dispose();
});
test('Card Acceptance fails closed for absent capabilities and mismatched observed identities',async()=>{
 for(const mutate of [p=>p.taskView.actions=[],p=>p.taskView.actions.forEach(c=>c.available=false),p=>p.pendingDecision.kind='REVIEW_APPROVAL',p=>p.taskView.currentExecution.id='old',p=>p.taskView.currentExecution.taskDefinitionRevisionId='old',p=>p.taskView.actions.forEach(c=>c.target.taskId='wrong'),p=>p.taskView.actions.forEach(c=>c.target.decisionId='TASK_ACCEPTANCE:old')]){
  const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();const value=acceptanceWorkspace();mutate(value.presentation);finishRefresh(h,0,value);await tick();assert.equal(h.root.querySelectorAll('button').some(v=>['验收','重新验证','确认要求修改'].includes(v.textContent)),false);assert.equal(h.calls().length,2);h.dispose();
 }
});

function pendingRuntimeDecision(kind='REVIEW_APPROVAL',action='APPROVE'){
 return {kind,owner:'TASK',taskId:'task-a',featureAreaId:'area-a',semanticState:'PENDING',decision:{kind,id:'gate-b',subjectId:kind==='TASK_ACCEPTANCE'?'execution-b':'task-a'},options:[{action,available:true,target:{taskId:'task-a',featureAreaId:'area-a',decisionId:'gate-b',executionId:'execution-b',taskDefinitionRevisionId:'revision-b'}}],defaultModel:'model-a',runtimeOptions:{modelConfigs:[{modelId:'model-a',enabled:true,supportedReasoningEfforts:['medium','high','ultra'],defaultReasoningEffort:'medium'},{modelId:'model-b',enabled:true,supportedReasoningEfforts:['low','high'],defaultReasoningEffort:'high'}]}};
}
function runtimeSelect(h,label){return selects(h).find(v=>v.attributes['aria-label']===label);}
function runtimeForm(h){return h.root.querySelectorAll('form').find(v=>v.querySelectorAll('select').some(v=>v.attributes['aria-label']==='Coding Agent model'));}

test('runtime configuration is not a Card-side approval form',async()=>{
 const h=await mountCanonicalCard();
 assert.equal(runtimeSelect(h,'Coding Agent model'),undefined);assert.equal(runtimeSelect(h,'Reasoning effort'),undefined);
 assert.equal(inputs(h).some(v=>v.attributes['aria-label']==='Codex Fast Mode'),false);
 assert.equal(h.calls().some(v=>v.params.name==='decision_respond'),false);h.dispose();
});

test('acceptance successors are directed to chat and never expose authored-spec editors',async()=>{
 const h=await mountCanonicalCard();assert.equal(h.root.querySelectorAll('textarea').length,0);assert.match(h.root.textContent,/请在对话中明确决定/);h.dispose();
});

test('acceptance and management capabilities never become Card commands',async()=>{const h=await mountCanonicalCard();assert.equal(findButton(h,'确认关闭'),undefined);assert.equal(h.calls().some(v=>/decision_respond|retire/.test(v.params.name)),false);h.dispose();});

test('runtime questions are surfaced through chat, not an embedded answer form',async()=>{const h=await mountCanonicalCard();assert.equal(findButton(h,'提交回答'),undefined);assert.equal(h.root.querySelectorAll('input').some(v=>v.attributes['aria-label']==='Which path?'),false);assert.equal(h.calls().some(v=>v.params.name==='runtime_request_respond'),false);h.dispose();});

test('projection replacement cannot leave a detached decision handler',async()=>{const h=await mountCanonicalCard(),before=h.calls().length;h.evaluate('refreshCard()');await settle();assert.equal(h.calls().slice(before).every(v=>['dev_bridge_card_get','workspace_get'].includes(v.params.name)),true);h.dispose();});

test('runtime approval is shown as read-only context and never invokes a permission resolver',async()=>{
 const h=boot({widgetState:{aiSessionId:'session',featureAreaId:'area-a',taskId:'task-a'}});h.init();await tick();
 const permissions={network:{enabled:true}},item={kind:'RUNTIME_APPROVAL',owner:'TASK',taskId:'task-a',featureAreaId:'area-a',semanticState:'PENDING',runtimeApprovalRequest:{reason:'Read fixture endpoint',api_key:'[REDACTED]'},decision:{kind:'RUNTIME_APPROVAL',id:'RUNTIME_APPROVAL:approval-b',subjectId:'approval-b',permissions,cancelEffect:'DECLINE_ACTION'},options:[{action:'GRANT_REQUESTED_PERMISSIONS',available:true,target:{subjectId:'approval-b'}},{action:'CANCEL',available:true,target:{subjectId:'approval-b'}}]};
 const value=visualFixture.fixture('REVIEW_APPROVAL_REQUIRED'),readonly=await mountCanonicalCard(value);assert.doesNotMatch(readonly.root.textContent,/Grant permissions|Decline action/);assert.equal(readonly.calls().some(v=>v.params.name==='runtime_request_respond'),false);readonly.dispose();
});


test("historical entry remains read-only and never exposes a replacement approval",async()=>{
 const h=await mountCanonicalCard();assert.equal(findButton(h,"确认"),undefined);assert.equal(h.calls().some(v=>v.params.name==='decision_respond'),false);h.dispose();
});

test("a resolved runtime origin refreshes without offering a replacement request",async()=>{
 const h=await mountCanonicalCard(),origin={taskId:"task-a",featureAreaId:"area-a",runId:"run-a",subjectId:"request-old"};
 const binding={aiSessionId:"session",projectId:"project",featureAreaId:"area-a",taskId:"task-a",actionTarget:origin,presentationRequestId:"runtime-origin",display:"EXPANDED"};
 const pending={kind:"RUNTIME_APPROVAL",owner:"TASK",taskId:"task-a",featureAreaId:"area-a",semanticState:"PENDING",decision:{id:"RUNTIME_APPROVAL:new",kind:"RUNTIME_APPROVAL",summary:"replacement-request-must-not-be-shown"},options:[{action:"APPROVE_ONCE",available:true,target:{...origin,subjectId:"request-new"}}]};
 assert.equal(findButton(h,"确认"),undefined);assert.equal(h.calls().some(v=>v.params.name==='runtime_request_respond'),false);h.dispose();
});


test('Review minimal policy suppresses technical copy in aggregate and projection fallback without changing authority',async()=>{
 const secret='ROOT_CAUSE_SECRET_MARKER /Users/private/source.go workSpec acceptanceCriteria';
 for(const aggregate of [false,true]){
  const value=workspace(),p=value.presentation,d=p.pendingDecision;d.summary=secret;
  p.nextAction={message:secret};p.workflowGuidance={message:secret};
  p.capabilities=['APPROVE','REVISE','REJECT'].map(action=>({action,available:true,target:{featureAreaId:'area-a',taskId:'task-a',decisionId:d.id,subjectId:d.subjectId}}));
  const item={...pendingRuntimeDecision(),decision:d,options:p.capabilities};
  if(aggregate)value.pendingDecisions=[item];
  const before=JSON.stringify(value),h=await mountCanonicalCard(value);
  assert.doesNotMatch(h.root.textContent,/ROOT_CAUSE_SECRET_MARKER|\/Users\/|workSpec|acceptanceCriteria|批准实施/);
  assert.match(h.root.textContent,/请在对话中明确决定/);assert.equal(findButton(h,'确认'),undefined);assert.equal(findButton(h,'拒绝'),undefined);assert.equal(findButton(h,'要求调整'),undefined);
  assert.deepEqual(Array.from(h.evaluate("workspace.presentation.capabilities.map(c=>c.action)")),['APPROVE','REVISE','REJECT']);
  assert.equal(h.evaluate('workspace.presentation.pendingDecision.summary'),secret);assert.equal(JSON.stringify(value),before);
  if(aggregate){assert.equal(runtimeSelect(h,'Coding Agent model'),undefined);assert.equal(runtimeSelect(h,'Reasoning effort'),undefined);assert.equal(inputs(h).find(n=>n.attributes['aria-label']==='Codex Fast Mode'),undefined);}
  h.dispose();
 }
});

test('non-Review permission and user-input requests do not become embedded controls',async()=>{
 const permission={kind:'RUNTIME_APPROVAL',featureAreaId:'area-a',taskId:'task-a',decision:{kind:'RUNTIME_APPROVAL',id:'RUNTIME_APPROVAL:permission',subjectId:'permission',summary:'Permission reason: access repository'},runtimeApprovalRequest:{reason:'Command requires write access',permissions:{filesystem:'write'}},options:[{action:'APPROVE_ONCE',available:true,target:{subjectId:'permission',runId:'run-a'}}]};
 const question={kind:'CODEX_REQUEST',featureAreaId:'area-a',taskId:'task-a',runtimeRequest:{projectId:'project',featureAreaId:'area-a',taskId:'task-a',runId:'run-a',connectionId:'connection',itemId:'question',state:'PENDING',method:'item/tool/requestUserInput',payload:{reason:'Need user input',questions:[{id:'scope',question:'Which repository should be changed?'}]},availableResponses:[]},options:[]};
 const h=await mountCanonicalCard();
 assert.equal(findButton(h,'Approve once'),undefined);assert.equal(findButton(h,'提交回答'),undefined);assert.equal(inputs(h).find(n=>n.attributes['aria-label']==='Which repository should be changed?'),undefined);assert.equal(h.calls().some(v=>v.params.name==='runtime_request_respond'),false);h.dispose();
});
