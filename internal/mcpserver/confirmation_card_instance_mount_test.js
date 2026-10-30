"use strict";
// Card half of the shared Card-instance lifecycle fixture. The envelopes below are
// the exact production structuredContent/_meta of confirmation_show and
// confirmation_options_get, captured from a real replacement flow (real SQLite
// store, real MCP wire, production middleware) by
// internal/controlapi/confirmation_card_instance_lifecycle_test.go. The Go mount
// gate loads the SAME file in confirmation_card_instance_fixture_test.go.
//
// Nothing here cherry-picks Card helpers: the ENTIRE embedded main script is
// evaluated (only its outer IIFE wrapper is removed, which the first test asserts),
// the real Host message protocol drives it, and the only replaced surface is the
// browser/Host boundary (DOM nodes, postMessage, timers). A Card-side regression in
// decode, acceptEntry, validateIntent, applyIntent, refresh or readOptions therefore
// shows up here, not merely in a hand-called function.
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),crypto=require("node:crypto"),vm=require("node:vm");
const {webcrypto}=require("node:crypto");
const html=fs.readFileSync(process.env.DEVBRIDGE_CONFIRMATION_UI_HTML||`${__dirname}/confirmation_ui.html`,"utf8");
const script=html.match(/<script id="confirmation-main">([\s\S]*?)<\/script>/)[1];
const fixture=JSON.parse(fs.readFileSync(`${__dirname}/testdata/confirmation_card_instance_lifecycle_v5.json`,"utf8"));

// The script is one strict-mode IIFE; the harness runs its body so the Card's own
// top-level state (entry, intent, listeners) is observable without editing the script.
function cardBody(){
 const open=script.indexOf("(()=>{");
 const close=script.lastIndexOf("})();");
 assert.ok(open>0&&close>open,"Card main script must stay a single IIFE");
 assert.equal(script.slice(0,open).trim(),"'use strict';","Card main script must start in strict mode");
 assert.equal(script.slice(close+5).trim(),"","nothing may follow the Card IIFE");
 return "'use strict';\n"+script.slice(open+6,close);
}

// The Card gates its primary action on `primary.childNodes.length`, so the shim keeps
// one real child list behind children/childNodes/first* instead of a static field;
// otherwise a rendered button would vanish from the inspected tree.
function domNode(tag){
 const target={nodeType:1,tagName:String(tag).toUpperCase(),textContent:"",value:"",disabled:false,listeners:{},attributes:{}};
 const kids=[];
 const noop=()=>{};
 const detach=node=>{
  if(!node||typeof node!=="object")return;
  const index=kids.indexOf(node);
  if(index>=0)kids.splice(index,1);
  if(node.parentNode===target)delete node.parentNode;
 };
 const attach=(node,index)=>{
  if(!node||typeof node!=="object"||!node.nodeType)return;
  detach(node);
  if(index===undefined||index<0||index>=kids.length)kids.push(node);else kids.splice(index,0,node);
  Object.defineProperty(node,"parentNode",{value:target,configurable:!0,writable:!0});
 };
 const elementKids=()=>kids.filter(node=>node.nodeType===1);
 const api={
  append:(...a)=>{for(const item of a)attach(item);},
  prepend:(...a)=>{for(const item of a.slice().reverse())attach(item,0);},
  replaceChildren:(...a)=>{for(const item of kids.slice())detach(item);for(const item of a)attach(item);},
  remove:()=>{const parent=target.parentNode;parent?.removeChild?.(target);},
  removeChild:node=>{detach(node);return node;},
  insertBefore:(node,reference)=>{attach(node,reference?kids.indexOf(reference):undefined);return node;},
  appendChild:node=>{attach(node);return node;},
  observe:noop,unobserve:noop,disconnect:noop,
  focus:noop,blur:noop,click:noop,scroll:noop,scrollTo:noop,scrollIntoView:noop,
  setAttribute:(k,v)=>{target.attributes[k]=String(v);},
  getAttribute:k=>k in target.attributes?target.attributes[k]:null,
  removeAttribute:k=>{delete target.attributes[k];},
  addEventListener:(type,fn)=>{if(!target.listeners[type])target.listeners[type]=[];target.listeners[type].push(fn);},
  removeEventListener:(type,fn)=>{target.listeners[type]=(target.listeners[type]||[]).filter(f=>f!==fn);},
  querySelector:()=>null,querySelectorAll:()=>[],getElementsByTagName:()=>[],closest:()=>null,matches:()=>!1,contains:()=>!1,
  getBoundingClientRect:()=>({top:0,left:0,right:720,bottom:400,width:720,height:400,x:0,y:0}),
  classList:{add:noop,remove:noop,toggle:noop,contains:()=>!1},
  dataset:{},style:{},offsetTop:0,offsetLeft:0,offsetWidth:0,offsetHeight:0,
  scrollTop:0,scrollLeft:0,scrollHeight:0,scrollWidth:0,clientTop:0,clientLeft:0,clientHeight:0,clientWidth:0,
  get children(){return elementKids();},
  get childNodes(){return kids.slice();},
  get childElementCount(){return elementKids().length;},
  get firstChild(){return kids.length?kids[0]:null;},
  get lastChild(){return kids.length?kids[kids.length-1]:null;},
  get firstElementChild(){const rows=elementKids();return rows.length?rows[0]:null;},
  get lastElementChild(){const rows=elementKids();return rows.length?rows[rows.length-1]:null;},
  get parentElement(){return target.parentNode||null;},
  nextSibling:null,previousSibling:null,
  open:!1,hidden:!1,isConnected:!0,
 };
 return new Proxy(target,{
  get(t,prop){
   if(prop in api)return api[prop];
   if(prop in t)return t[prop];
   return undefined;
  },
  set(t,prop,value){t[prop]=value;return !0;},
 });
}

// createCardInstance starts one independent widget instance, exactly as a
// per-instance-aware Host would, and records every Host call the Card makes.
// A caller may pass its own `answer(request)` to script the Host; the default
// responder serves the shared lifecycle fixture.
function createCardInstance(label,overrideAnswer){
 const hostCalls=[],toolCalls=[],widgetStates=[],hashTasks=new Set();
 const elements=new Map();
 const byId=id=>{if(!elements.has(id))elements.set(id,domNode("div"));return elements.get(id);};
 const document={
  getElementById:byId,
  createElement:tag=>domNode(tag),
  createElementNS:(ns,tag)=>domNode(tag),
  createTextNode:text=>({nodeType:3,textContent:String(text)}),
  createRange:()=>({selectNodeContents:()=>{},cloneRange:()=>({setEnd:()=>{},setStart:()=>{}}),getBoundingClientRect:()=>({top:0,left:0,right:0,bottom:0,width:0,height:0}),toString:()=>""}),
  body:domNode("body"),documentElement:domNode("html"),activeElement:null,currentScript:null,
  querySelector:()=>null,querySelectorAll:()=>[],getSelection:()=>({removeAllRanges:()=>{},addRange:()=>{},rangeCount:0}),
 };
 const sandbox={
  document,
  console,
  structuredClone:value=>JSON.parse(JSON.stringify(value)),
  // Timer-free host boundary: the Card's 45s RPC watchdog must not keep the offline
  // suite alive, and every request is answered below, so nothing may depend on it.
  setTimeout:()=>({}),clearTimeout:()=>{},setInterval:()=>({}),clearInterval:()=>{},
  requestAnimationFrame:fn=>{queueMicrotask(()=>{try{fn(0);}catch(_){}} );return 1;},
  cancelAnimationFrame:()=>{},
  getComputedStyle:()=>({getPropertyValue:()=>"",height:"0px",width:"0px"}),
  matchMedia:()=>({matches:!1,addEventListener:()=>{},removeEventListener:()=>{},addListener:()=>{},removeListener:()=>{}}),
  navigator:{clipboard:{writeText:()=>Promise.resolve()}},
  location:{href:"https://host.invalid/mcp",origin:"https://host.invalid"},
  // A real SHA-256 so a scripted Host can reproduce the Card's canonical payload
  // hash instead of the test inventing one.
  crypto:{randomUUID:()=>"test-"+Math.random().toString(16).slice(2),subtle:{digest(...args){const job=webcrypto.subtle.digest(...args);hashTasks.add(job);job.finally(()=>hashTasks.delete(job));return job;}},digest:webcrypto.digest},
  TextEncoder:globalThis.TextEncoder,TextDecoder:globalThis.TextDecoder,
  ResizeObserver:undefined,Event:undefined,CustomEvent:undefined,fetch:undefined,
  openai:{
   widgetState:undefined,
   setWidgetState:snapshot=>{widgetStates.push(JSON.parse(JSON.stringify(snapshot)));},
  },
 };
 const context=vm.createContext(sandbox);
 // These immutable wire envelopes were captured at creation time. Evaluate
 // their pending state at that time, independent of the calendar running CI;
 // expiry-specific tests may advance this instance's clock explicitly.
 vm.runInContext(`Date.now=()=>${Date.parse(fixture.replacement.entry.intent.createdAt)+1000};`,context);
 const listeners=[];
 sandbox.addEventListener=(type,fn)=>{if(type==="message")listeners.push(fn);};
 sandbox.removeEventListener=(type,fn)=>{if(type!=="message")return;const index=listeners.indexOf(fn);if(index>=0)listeners.splice(index,1);};
 // In a browser window IS the global object, so window.addEventListener and the bare
 // call the Card uses must be the same registry.
 vm.runInContext("globalThis.window=globalThis;",context);
 const parent={postMessage:message=>{hostCalls.push(JSON.parse(JSON.stringify(message??"null")));respond(message);}};
 sandbox.parent=parent;
 const replies=[];
 function respond(message){
  const request=message;
  if(!request||typeof request!=="object"||!Number.isInteger(request.id))return;
  // A scripted Host may answer asynchronously (it has to hash the Card's own request
  // to produce a receipt), so the reply is awaited rather than assumed sync.
  replies.push(async()=>{deliver({jsonrpc:"2.0",id:request.id,...await answer(request)});});
 }
 function answer(request){
  if(overrideAnswer)return overrideAnswer(request);
  if(request.method==="ui/initialize")return{result:{protocolVersion:"2026-01-26",serverInfo:{name:"test-host",version:"1"}}};
  if(request.method!=="tools/call")return{result:{}};
  const name=request.params?.name,arguments_=request.params?.arguments||{};
  toolCalls.push({name,arguments:JSON.parse(JSON.stringify(arguments_)),aiSessionId:arguments_.aiSessionId,intentId:arguments_.intentId,version:arguments_.version,purpose:arguments_.purpose,requestId:arguments_.requestId});
  for(const card of [fixture.superseded,fixture.replacement]){
   const intent=card.entry.intent;
   if(intent.aiSessionId!==arguments_.aiSessionId||intent.intentId!==arguments_.intentId||intent.version!==arguments_.version)continue;
   if(name!=="confirmation_get"&&name!=="confirmation_options_get")break;
   const envelope=name==="confirmation_get"?{intent:card.entry.intent,display:card.entry.display}:card.options.entry;
   return{result:{structuredContent:JSON.parse(JSON.stringify(envelope))}};
  }
  assert.fail(`${label}: the Card requested something the fixture cannot serve: ${name} ${JSON.stringify(arguments_)}`);
 }
 function deliver(message){for(const fn of listeners.slice())fn({source:parent,data:message,origin:"https://host.invalid"});}
 vm.runInContext(cardBody(),context,{filename:"confirmation_ui.html#main"});
 async function settle(){
  for(let round=0;round<12;round+=1){
   const before=hostCalls.length;
   while(replies.length)await replies.shift()();
   await Promise.all([...hashTasks]);
   await new Promise(resolve=>setImmediate(resolve));
   if(!replies.length&&!hashTasks.size&&hostCalls.length===before&&round>0)break;
  }
 }
 const pushToolInput=async args=>{deliver({jsonrpc:"2.0",method:"ui/notifications/tool-input",params:{arguments:args}});await settle();};
 const pushToolResult=async envelope=>{deliver({jsonrpc:"2.0",method:"ui/notifications/tool-result",params:{structuredContent:JSON.parse(JSON.stringify(envelope))}});await settle();};
 const peek=expression=>vm.runInContext(expression,context);
 return {peek,settle,pushToolInput,pushToolResult,hostCalls,toolCalls,widgetStates};
}

function identityOf(card){const intent=card.entry.intent;return {aiSessionId:intent.aiSessionId,intentId:intent.intentId,version:intent.version,purpose:intent.purpose};}
function toolInputOf(card){return {...identityOf(card),action:card.entry.intent.action,requestId:"tool-input-"+card.intentId};}

// renderedText walks the Card's own DOM tree. The harness' nodes are plain objects
// created outside the vm, so the Card's real render output is inspectable without a
// browser: this reads the machine facts behind the visible Card (which state label
// and which confirmation action this instance offers), not a screenshot.
function renderedText(node,found=[]){
 if(!node||typeof node!=="object")return found;
 if(node.nodeType===3&&node.textContent)found.push(String(node.textContent));
 else if(typeof node.textContent==="string"&&node.textContent)found.push(node.textContent);
 for(const child of node.childNodes||[])renderedText(child,found);
 return found;
}

function renderedButtons(node,found=[]){
 if(!node||typeof node!=="object")return found;
 if(node.nodeType===1&&node.tagName==="BUTTON")found.push({label:renderedText(node).join(" ").trim(),disabled:node.disabled===!0});
 for(const child of node.childNodes||[])renderedButtons(child,found);
 return found;
}

// Copy the V5 Card derives for this exact intent shape (REVIEW / REVISE). These are
// asserted constants, not values recomputed through the Card's own helpers, so the
// G3/G4 checks cannot pass by agreeing with a broken renderer.
const PENDING_LABEL="待确认";
const SUPERSEDED_LABEL="已被新版本替代";
const RE_REVIEW_ACTION_LABEL="确认要求修订";
// V5 renders the intent's own action title as the Card eyebrow, so the superseded and
// replacement Cards must be distinguishable by their visible wording, not only by state.
const RE_REVIEW_ACTION_TITLE="重新审查";

function actionLabels(card){return renderedButtons(card.peek("root")).map(button=>button.label);}

test("the shared Card instance fixture is captured from this exact embedded Card script",()=>{
 assert.equal(crypto.createHash("sha256").update(script,"utf8").digest("hex"),fixture.superseded.meta.mainScriptSHA256);
 assert.equal(fixture.replacement.meta.mainScriptSHA256,fixture.superseded.meta.mainScriptSHA256);
 assert.equal(fixture.superseded.meta.componentVersion,"PURPOSE_CONFIRMATION_V14");
 assert.equal(fixture._meta.staticResourceUri,"ui://devbridge/confirmation/v14");
 assert.equal(fixture.superseded.meta.resourceUri,fixture.replacement.meta.resourceUri);
 assert.notEqual(fixture.superseded.presentationInstanceId,fixture.replacement.presentationInstanceId);
});

test("a mounted Card keeps its own intent when the Host reuses the instance for a new result (§9)",async()=>{
 const instance=createCardInstance("reuse-instance");
 await instance.settle();
 assert.equal(instance.peek("initialized"),!0,"the Card must finish the Host handshake");
 assert.equal(instance.peek("entry"),null,"no intent is bound before the Host pushes an entry");

 await instance.pushToolInput(toolInputOf(fixture.superseded));
 await instance.pushToolResult(fixture.superseded.entry);
 assert.equal(instance.peek("entryDisposition"),"");
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId);
 assert.equal(instance.peek("intent.state"),"SUPERSEDED");
 assert.equal(instance.peek("fatal"),!1);
 assert.equal(instance.peek("renderFailed"),!1,"the production superseded Card renders without a local failure");
 // G3: the old Card reads as replaced history and offers no confirmation action.
 const oldText=renderedText(instance.peek("root"));
 assert.ok(oldText.some(text=>text.includes(SUPERSEDED_LABEL)),`G3 superseded Card state: ${JSON.stringify(oldText)}`);
 assert.ok(!oldText.some(text=>text.includes(PENDING_LABEL)),`G3 the superseded Card must not read as pending: ${JSON.stringify(oldText)}`);
 assert.ok(!actionLabels(instance).includes(RE_REVIEW_ACTION_LABEL),`G3 the superseded Card must not offer ${RE_REVIEW_ACTION_LABEL}: ${JSON.stringify(actionLabels(instance))}`);
 // The frozen action title stays in history (it describes what the old intent was for);
 // what must disappear is the state banner's pending reading and any confirm control.
 assert.ok(actionLabels(instance).includes("复制继续引用"),"G3 the superseded Card only offers the read-only continuation reference");
 const requestsBefore=instance.toolCalls.length;

 // The live defect: the Host pushes the replacement result into the mounted Card.
 // The Card must refuse it and stay its own historical Card.
 await instance.pushToolResult(fixture.replacement.entry);
 assert.equal(instance.peek("entryDisposition"),"ENTRY_INSTANCE_IDENTITY_MISMATCH");
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId,"the old Card keeps its own bound intent");
 assert.equal(instance.peek("entry.contentHash"),fixture.superseded.contentHash);
 assert.equal(instance.peek("intent.state"),"SUPERSEDED","the old Card never displays the replacement as pending");
 assert.equal(instance.peek("fatal"),!1,"a foreign entry is a typed disposition, not a broken Card");
 assert.equal(instance.peek("renderFailed"),!1);
 assert.equal(instance.toolCalls.length,requestsBefore,"a refused entry may not trigger new business reads");
 assert.ok(!actionLabels(instance).includes(RE_REVIEW_ACTION_LABEL),`the refused Card must not gain ${RE_REVIEW_ACTION_LABEL}`);
 for(const call of instance.toolCalls)assert.equal(call.intentId,fixture.superseded.intentId,`the old instance requested ${call.intentId}`);

 // Its own refresh still works, still names its own intent, and the refusal stays on
 // the record instead of being silently forgotten.
 await instance.pushToolResult(fixture.superseded.entry);
 assert.equal(instance.peek("entryDisposition"),"ENTRY_INSTANCE_IDENTITY_MISMATCH","the typed refusal remains the instance's diagnostic fact");
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId);
 assert.equal(instance.peek("intent.state"),"SUPERSEDED");
});

test("a foreign tool-input followed by its result also fails closed without rebinding (§9)",async()=>{
 const instance=createCardInstance("foreign-input");
 await instance.settle();
 await instance.pushToolInput(toolInputOf(fixture.superseded));
 await instance.pushToolResult(fixture.superseded.entry);
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId);

 // A Host that replaces the whole submission context is refused at the identity
 // boundary; either way the mounted Card never becomes the new intent's Card.
 await instance.pushToolInput(toolInputOf(fixture.replacement));
 assert.equal(instance.peek("fatal"),!0,"a changed input identity is a typed failure, not a silent swap");
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId);
 assert.equal(instance.peek("intent.state"),"SUPERSEDED");
 await instance.pushToolResult(fixture.replacement.entry);
 assert.equal(instance.peek("entry.intentId"),fixture.superseded.intentId,"the replacement never binds into the old instance");
 assert.equal(instance.peek("intent.state"),"SUPERSEDED");
 for(const call of instance.toolCalls)assert.equal(call.intentId,fixture.superseded.intentId);
});

test("an independent Card instance binds the replacement intent as its own PENDING Card (§4/§11)",async()=>{
 const instance=createCardInstance("replacement-instance");
 await instance.settle();
 await instance.pushToolInput(toolInputOf(fixture.replacement));
 await instance.pushToolResult(fixture.replacement.entry);
 assert.equal(instance.peek("entryDisposition"),"");
 assert.equal(instance.peek("entry.intentId"),fixture.replacement.intentId);
 assert.equal(instance.peek("entry.contentHash"),fixture.replacement.contentHash);
 assert.equal(instance.peek("intent.state"),"PENDING");
 assert.equal(instance.peek("stale"),!1,"a fresh replacement instance is not the expired old Card");
 assert.equal(instance.peek("unknown"),!1);
 assert.equal(instance.peek("fatal"),!1);
 assert.equal(instance.peek("renderFailed"),!1);
 assert.equal(instance.peek("intent.submission ?? null"),null,"the replacement Card carries no commit state");
 assert.deepEqual(instance.peek("intent.steps"),[]);
 assert.equal(instance.peek("options?.eligibility?.available"),!0,"the new Card can still confirm");
 assert.equal(instance.peek("readerContextValid"),!0,"the replacement Card keeps a valid reader context");
 assert.equal(instance.peek("display?.intentId"),fixture.replacement.intentId,"the bound display names this instance's own intent");
 assert.equal(instance.peek("display?.taskId"),fixture.superseded.entry.display.taskId,"the same-Task re-review never moves to another Task");
 assert.equal(instance.peek("intent.content.sourceIntentId"),fixture.superseded.intentId,"lineage stays data, never identity");
 assert.notEqual(instance.peek("entry.intentId"),instance.peek("intent.content.sourceIntentId"),"binding uses the new identity, not the replaced one");
 // G4: the replacement instance renders as its own pending Card and offers the
 // same-Task re-review confirmation that the old Card lost.
 const newText=renderedText(instance.peek("root"));
 assert.ok(newText.some(text=>text.includes(PENDING_LABEL)),`G4 pending Card state: ${JSON.stringify(newText)}`);
 assert.ok(actionLabels(instance).includes(RE_REVIEW_ACTION_LABEL),`G4 the replacement Card must offer ${RE_REVIEW_ACTION_LABEL}: ${JSON.stringify(actionLabels(instance))}`);
 assert.ok(!newText.some(text=>text.includes(SUPERSEDED_LABEL)),"G4 the replacement Card never presents itself as replaced history");
 assert.ok(newText.some(text=>text.trim()===RE_REVIEW_ACTION_TITLE),`G4 the replacement Card titles its own action ${RE_REVIEW_ACTION_TITLE}: ${JSON.stringify(newText)}`);
 assert.equal(instance.peek("intent.content.variants")?.[0]?.label,"同一任务重新审查","the replacement Card's plan copy comes from its own frozen intent");
 // The instance reads only its own identity.
 assert.ok(instance.toolCalls.length>0,"a pending Card must read its own options");
 for(const call of instance.toolCalls){
  assert.equal(call.intentId,fixture.replacement.intentId,`the new instance requested ${call.intentId}`);
  assert.equal(call.aiSessionId,fixture.replacement.entry.intent.aiSessionId);
 }
 assert.ok(instance.toolCalls.some(call=>call.name==="confirmation_options_get"));
 // G6/G13: nothing has been submitted on this intent, so its own reads must never
 // borrow the show's presentation handle as a receipt pointer.
 for(const call of instance.toolCalls)assert.ok(!call.requestId,`a never-submitted Card requested a receipt for ${call.requestId}`);

 // Two live instances are two independent Cards over one static resource.
 const historical=createCardInstance("superseded-instance");
 await historical.settle();
 await historical.pushToolInput(toolInputOf(fixture.superseded));
 await historical.pushToolResult(fixture.superseded.entry);
 assert.equal(historical.peek("intent.state"),"SUPERSEDED");
 assert.equal(instance.peek("intent.state"),"PENDING");
 assert.notEqual(historical.peek("entry.contentHash"),instance.peek("entry.contentHash"));
 assert.notEqual(historical.peek("entry.intentId"),instance.peek("entry.intentId"));
 const historicalText=renderedText(historical.peek("root"));
 assert.ok(historicalText.some(text=>text.includes(SUPERSEDED_LABEL)),"the paired historical Card still reads as replaced history");
 assert.ok(!actionLabels(historical).includes(RE_REVIEW_ACTION_LABEL),`the paired historical Card must not offer ${RE_REVIEW_ACTION_LABEL}`);
 assert.ok(actionLabels(instance).includes(RE_REVIEW_ACTION_LABEL),"the two live instances offer their own actions");
 assert.equal(renderedButtons(instance.peek("root")).find(button=>button.label===RE_REVIEW_ACTION_LABEL).disabled,!1,"the replacement Card's own confirmation is live");
});

test("a replacement Card instance inherits no pending-commit or unknown state from the old intent (§7)",async()=>{
 const instance=createCardInstance("isolated-state");
 await instance.settle();
 await instance.pushToolInput(toolInputOf(fixture.replacement));
 await instance.pushToolResult(fixture.replacement.entry);
 assert.equal(instance.peek("pendingCommit"),null);
 assert.equal(instance.peek("pendingHash"),"");
 assert.equal(instance.peek("confirmedOutcome"),"");
 assert.equal(instance.peek("unknown"),!1);
 assert.equal(instance.peek("flight"),!1,"the read settled instead of hanging on a stale request");
 const snapshot=instance.widgetStates.at(-1);
 assert.ok(snapshot,"the bound Card persists its own widget snapshot");
 assert.equal(snapshot.identity.intentId,fixture.replacement.intentId);
 assert.equal(snapshot.identity.contentHash,fixture.replacement.contentHash);
 assert.notEqual(snapshot.identity.intentId,fixture.superseded.intentId);
 assert.equal(snapshot.pendingRequestId,"","a freshly bound replacement has nothing pending");
});

// Exported so a sibling suite can drive the same real embedded Card over the same
// real Host message protocol with a scripted responder (mirrors the established
// dev_bridge_ui_test.js harness convention, including its re-registered tests).
module.exports={createCardInstance,cardBody,domNode,renderedText,renderedButtons,fixture,
 PENDING_LABEL,SUPERSEDED_LABEL,RE_REVIEW_ACTION_LABEL,RE_REVIEW_ACTION_TITLE,toolInputOf,actionLabels};
