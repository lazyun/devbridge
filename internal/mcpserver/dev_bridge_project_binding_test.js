'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const binding={presentationMode:'PROJECT_SELECTION',aiSessionId:'new-session',presentationRequestId:'first-panel',entryIdentity:'first-panel',display:'EXPANDED'};
const result=(projects=[{id:'project-a',name:'Alpha'},{id:'project-b',name:'Beta'}])=>({result:{aiSessionId:'new-session',state:'PROJECT_SELECTION_REQUIRED',boundProjectId:null,project:null,availableProjects:projects,availableProjectsStatus:'AVAILABLE',availableProjectsComplete:true},context:{aiSessionId:'new-session',projectId:null},workflow:{},presentationHint:{}});
const feed=(items=[])=>({aiSessionId:'new-session',projectId:'',scope:'SESSION',availability:'AVAILABLE',items});
const ref={aiSessionId:'new-session',intentId:'select-intent',purpose:'PROJECT_SETUP',action:'SELECT_PROJECT',contentHash:'a'.repeat(64),version:1,state:'PENDING',requestId:'project-click'};
const button=h=>h.root.querySelectorAll('button').find(b=>b.getAttribute('data-project-selection'));
async function unbound(projects){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']}});
 h.input(binding);h.result(binding,{singlePanel:true});h.init();await settle();
 const sessionRead=h.calls().find(p=>p.params.name==='dev_bridge_card_get');assert.ok(sessionRead);h.reply(sessionRead,result(projects));
 const panelRead=h.calls().find(p=>p.params.name==='dev_bridge_panel_get');assert.ok(panelRead);h.reply(panelRead,feed());await settle();return h;
}

test('first unbound panel exposes a direct project form without GPT messages or implicit selection',async()=>{
 const h=await unbound();
 assert.ok(button(h));assert.equal(button(h).disabled,false);assert.doesNotMatch(h.root.textContent,/Alpha|Beta/);
 assert.equal(h.calls().some(p=>/prepare|commit|project_open/.test(p.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
 // Tab changes and any local repaint must remain on the unbound screen.
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='history').onclick();assert.doesNotMatch(h.root.textContent,/Card is unavailable/);
 h.root.querySelectorAll('button').find(b=>b.getAttribute('data-operation-tab')==='next').onclick();assert.ok(button(h));
 const clicked=button(h).onclick();await settle();
 const prepare=h.calls().find(p=>p.params.name==='project_setup_prepare');assert.ok(prepare);assert.equal(prepare.params.arguments.aiSessionId,'new-session');assert.equal(prepare.params.arguments.action,'SELECT_PROJECT');assert.equal(prepare.params.arguments.purpose,'PROJECT_SETUP');assert.equal('projectId' in prepare.params.arguments,false);assert.equal('selection' in prepare.params.arguments,false);
 await button(h).onclick();assert.equal(h.calls().filter(p=>p.params.name==='project_setup_prepare').length,1);
 h.reply(prepare,{businessOutcome:'PREPARED',intent:ref});await settle();
 const poll=h.calls().filter(p=>p.params.name==='dev_bridge_panel_get').at(-1);h.reply(poll,feed([ref]));await clicked;await settle();
 const row=h.root.querySelectorAll('details').find(r=>r.getAttribute('data-confirmation-intent')==='select-intent');assert.ok(row);assert.equal(row.open,true);assert.equal(button(h),undefined);
 assert.equal(h.calls().some(p=>p.params.name==='confirmation_commit'||p.params.name==='project_open'),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);h.dispose();
});

test('uncertain project prepare keeps one request id and refresh never prepares',async()=>{
 const h=await unbound([{id:'only',name:'Only project'}]);const first=button(h).onclick();await settle();let prepare=h.calls().find(p=>p.params.name==='project_setup_prepare');const request=prepare.params.arguments.requestId;h.reject(prepare);await first;
 assert.match(h.root.textContent,/同一请求/);assert.equal(h.evaluate('boundProjectID'),'');
 const again=button(h).onclick();await settle();prepare=h.calls().filter(p=>p.params.name==='project_setup_prepare').at(-1);assert.equal(prepare.params.arguments.requestId,request);h.reject(prepare);await again;
 assert.equal(h.calls().some(p=>p.params.name==='confirmation_commit'||p.params.name==='session_initialize'||p.params.name==='project_open'),false);h.dispose();
});

test('pending project form is reused; empty candidates cannot open a useless form',async()=>{
 const h=await unbound();h.evaluate(`panelFeed=${JSON.stringify(feed([ref]))};render()`);assert.equal(button(h),undefined);assert.equal(h.calls().some(p=>/prepare/.test(p.params.name)),false);h.dispose();
 const empty=await unbound([]);assert.ok(button(empty));assert.equal(button(empty).disabled,true);empty.dispose();
});
