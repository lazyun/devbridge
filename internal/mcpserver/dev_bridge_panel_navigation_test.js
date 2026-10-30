'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
const focus=(version,area='area-a',task='task-b')=>({aiSessionId:'session',projectId:'project',featureAreaId:area,taskId:task,epoch:'server-one',version});
const feed=navigation=>({scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items:[],...(navigation?{navigation}:{})});
async function panel(){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']}}),binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'navigation',entryIdentity:'navigation',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()},singlePanel:true});h.init();await settle();return h;
}
function workspace(area,task){const w=fixture('REVIEW_RUNNING');w.selectedFeatureAreaId=area;w.selectedTaskId=task;w.presentation.featureArea.id=area;w.presentation.details.featureAreaId=area;w.presentation.details.taskId=task;w.presentation.taskView.id=task;w.presentation.taskView.title='Selected '+task;w.selectedWork=null;if(!task)w.presentation.taskView=null;if(!area)w.presentation=null;return w;}
const lastCall=(h,name)=>h.calls().filter(c=>c.params.name===name).at(-1);
const harmless=h=>{assert.equal(h.calls().some(c=>/prepare|commit|submit|recover|start|show/.test(c.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);};
test('GPT focus changes the existing panel only after a fresh exact read, without another Card',async()=>{
 const h=await panel();h.reply(lastCall(h,'dev_bridge_panel_get'),feed(focus(1)));await settle();
 const read=lastCall(h,'dev_bridge_card_get');assert.deepEqual(read.params.arguments,{aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-b',selectionMode:'EXACT'});
 assert.equal(h.evaluate('taskID'),'task-a');h.reply(read,workspace('area-a','task-b'));await settle();
 assert.equal(h.evaluate('taskID'),'task-b');assert.match(h.root.textContent,/Selected task-b/);assert.equal(h.evaluate('entryOrigin.presentationRequestId'),'navigation');harmless(h);h.dispose();
});
test('manual Area overview and Task selection share panel_focus and retain empty Task explicitly',async()=>{
 const h=await panel();h.reply(lastCall(h,'dev_bridge_panel_get'),feed());await settle();
 let select=h.root.querySelectorAll('select')[1];select.value='';select.onchange();await settle();
 let call=lastCall(h,'panel_focus');assert.deepEqual(call.params.arguments,{aiSessionId:'session',projectId:'project',featureAreaId:'area-a'});h.reply(call,{navigation:focus(1,'area-a','')});await settle();
 h.reply(lastCall(h,'dev_bridge_card_get'),workspace('area-a',''));await settle();assert.equal(h.evaluate('taskID'),'');assert.equal(h.evaluate('featureAreaID'),'area-a');
 h.reply(lastCall(h,'dev_bridge_panel_get'),feed(focus(1,'area-a','')));await settle();
 select=h.root.querySelectorAll('select')[1];select.value='task-b';select.onchange();await settle();call=lastCall(h,'panel_focus');assert.equal(call.params.arguments.taskId,'task-b');h.reply(call,{navigation:focus(2)});await settle();h.reply(lastCall(h,'dev_bridge_card_get'),workspace('area-a','task-b'));await settle();assert.equal(h.evaluate('taskID'),'task-b');harmless(h);h.dispose();
});
test('invalid target or failed read retains the old Task and clears no workflow state',async()=>{
 for(const kind of ['foreign','failed','wrong-read']){
  const h=await panel();const nav=focus(1);if(kind==='foreign')nav.projectId='foreign';h.reply(lastCall(h,'dev_bridge_panel_get'),feed(nav));await settle();
  const read=lastCall(h,'dev_bridge_card_get');if(kind==='foreign')assert.equal(read,undefined);else if(kind==='failed')h.reject(read);else h.reply(read,workspace('area-a','wrong'));
  await settle();assert.equal(h.evaluate('taskID'),'task-a');assert.equal(h.evaluate('workspace.selectedTaskId'),'task-a');harmless(h);h.dispose();
 }
});
test('late lower-version focus never overwrites a newer selection and unchanged focus does not rerender',async()=>{
 const h=await panel();h.reply(lastCall(h,'dev_bridge_panel_get'),feed(focus(5)));await settle();h.reply(lastCall(h,'dev_bridge_card_get'),workspace('area-a','task-b'));await settle();
 const before=h.calls().length;h.expire(5000);await settle();for(const call of h.calls().slice(before)){if(call.params.name==='dev_bridge_panel_get')h.reply(call,feed(focus(4,'area-a','task-a')));else if(call.params.name==='dev_bridge_card_get')h.reply(call,workspace('area-a','task-b'));}await settle();assert.equal(h.evaluate('taskID'),'task-b');assert.equal(h.evaluate('panelNavigationState.version'),5);harmless(h);h.dispose();
});
test('no-click dispatcher cannot navigate and historical exact entries cannot be repurposed',async()=>{
 const h=await panel();await assert.rejects(h.evaluate("request('panel_focus',{aiSessionId:'session',projectId:'project',taskId:'task-b'})"));
 h.evaluate("entryBinding.readOnly=true");await h.evaluate("selectPanelTarget('area-a','task-b')");assert.equal(lastCall(h,'panel_focus'),undefined);h.dispose();
});
test('Project inventory Task selection derives its actual Area, while clearing Area remains Project overview',async()=>{
 const h=await panel();h.reply(lastCall(h,'dev_bridge_panel_get'),feed(focus(1,'','')));await settle();h.reply(lastCall(h,'dev_bridge_card_get'),workspace('',''));await settle();assert.equal(h.evaluate('featureAreaID'),'');
 const before=h.calls().length;const change=h.evaluate("selectPanelTarget('','task-b')");await settle();const call=lastCall(h,'panel_focus');assert.ok(h.calls().length>before);assert.deepEqual(call.params.arguments,{aiSessionId:'session',projectId:'project',taskId:'task-b'});h.reply(call,{navigation:focus(2)});await settle();h.reply(lastCall(h,'dev_bridge_card_get'),workspace('area-a','task-b'));await change;assert.equal(h.evaluate('featureAreaID'),'area-a');assert.equal(h.evaluate('taskID'),'task-b');harmless(h);h.dispose();
});
test('restored focus acknowledgement prevents an older focus from undoing a confirmed creation navigation',async()=>{
 const h=await panel();h.reply(lastCall(h,'dev_bridge_panel_get'),feed(focus(2)));await settle();h.reply(lastCall(h,'dev_bridge_card_get'),workspace('area-a','task-b'));await settle();
 const saved=h.saved.at(-1);assert.equal(saved.panelPresentation.navigation.version,2);
 h.evaluate("setIdentity(sessionID,'area-a','new-confirmed-task',true);panelNavigationState=null");
 h.evaluate('restorePanelPresentation('+JSON.stringify(saved)+')');await h.evaluate('applyPanelNavigation('+JSON.stringify(focus(2))+')');assert.equal(h.evaluate('taskID'),'new-confirmed-task');assert.equal(h.calls().filter(c=>c.params.name==='dev_bridge_card_get').length,1);harmless(h);h.dispose();
});
