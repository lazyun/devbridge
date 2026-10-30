'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js'),{fixture}=require('./testdata/card_visual_fixture.cjs');
const feed=items=>({scope:'SESSION',availability:'AVAILABLE',aiSessionId:'session',projectId:'project',items});
const intent={aiSessionId:'session',intentId:'review-confirmation',version:1,contentHash:'a'.repeat(64),purpose:'REVIEW',action:'START_REVIEW',state:'PENDING',taskId:'task-a'};
const details=(h,label)=>h.root.querySelectorAll('details').find(node=>node.childNodes[0]?.textContent===label);
const tab=(h,name)=>h.root.querySelectorAll('button').find(node=>node.getAttribute('data-operation-tab')===name).onclick();
async function panel(items=[]){
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']}});
 const create=h.context.document.createElement;h.context.document.createElement=tag=>{const node=create(tag);node.focus=()=>{h.context.document.activeElement=node;node.focused=true;};node.setSelectionRange=(start,end,direction)=>{Object.assign(node,{selectionStart:start,selectionEnd:end,selectionDirection:direction});};return node;};
 const binding={presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'refresh-panel',entryIdentity:'refresh-panel',display:'EXPANDED'};
 h.input(binding);h.result(binding,{workspace:{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()},singlePanel:true});h.init();await settle();
 h.reply(h.calls().find(p=>p.params.name==='dev_bridge_panel_get'),feed(items));await settle();return h;
}
async function poll(h,items){
 const before=h.calls().length;h.expire(5000);await settle();
 const calls=h.calls().slice(before);assert.ok(calls.some(c=>c.params.name==='dev_bridge_panel_get'));
 for(const call of calls){
  if(call.params.name==='dev_bridge_panel_get')h.reply(call,feed(items));
  else if(call.params.name==='dev_bridge_card_get'){const w=JSON.parse(h.evaluate('JSON.stringify(workspace)'));w.refreshedAt=new Date().toISOString();h.reply(call,w);}
  else assert.fail('Unexpected background call '+call.params.name);
 }
 await settle();assert.equal(h.calls().some(c=>/commit|prepare|submit|recover|start/.test(c.params.name)),false);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);
}
test('timed workspace and changed feed refresh preserve navigation, history expansion and actual scroll position',async()=>{
 const old={...intent,state:'COMPLETED'};const h=await panel([old]);tab(h,'history');
 details(h,'切换领域与任务').open=true;
 let row=h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')===old.intentId);row.open=true;row.ontoggle();
 h.evaluate('operationScroller.scrollTop=173'); // Before the browser dispatches its scroll event.
 await poll(h,[old,{...old,intentId:'older',contentHash:'b'.repeat(64)}]);
 assert.equal(h.evaluate('operationTab'),'history');assert.equal(details(h,'切换领域与任务').open,true);
 row=h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')===old.intentId);assert.equal(row.open,true);assert.equal(h.evaluate('operationScroller.scrollTop'),173);
 tab(h,'next');assert.equal(details(h,'切换领域与任务').open,true);tab(h,'history');assert.equal(h.evaluate('operationScroller.scrollTop'),173);assert.equal(h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')===old.intentId).open,true);h.dispose();
});
test('explicitly closed confirmation stays closed on automatic refresh, while a genuinely new confirmation is discoverable',async()=>{
 const h=await panel([intent]);let row=h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')===intent.intentId);assert.equal(row.open,true);row.open=false;row.ontoggle();
 await poll(h,[intent]);row=h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')===intent.intentId);assert.equal(row.open,false);
 await poll(h,[intent,{...intent,intentId:'new-confirmation',contentHash:'c'.repeat(64)}]);assert.equal(h.root.querySelectorAll('details').find(n=>n.getAttribute('data-confirmation-intent')==='new-confirmation').open,true);h.dispose();
});
test('results retain independently opened nested report fields through polls and tab switches',async()=>{
 const h=await panel();tab(h,'results');h.evaluate("panelResultData=[{label:'审查报告',value:{report:{findings:{summary:'节点确认结论'},risks:{summary:'边界风险'}}}}];render()");
 details(h,'发现').open=true;details(h,'风险').open=false;details(h,'任务要求与技术详情').open=true;
 await poll(h,[{...intent,state:'COMPLETED'}]);assert.equal(h.evaluate('operationTab'),'results');assert.equal(details(h,'发现').open,true);assert.equal(details(h,'风险').open,false);assert.equal(details(h,'任务要求与技术详情').open,true);
 tab(h,'history');tab(h,'results');assert.equal(details(h,'发现').open,true);assert.equal(details(h,'风险').open,false);h.dispose();
});
test('feed repaint preserves composer draft, keyboard focus and selection without restoring arbitrary input values',async()=>{
 const h=await panel();h.root.querySelectorAll('button').find(b=>b.textContent==='＋ 发起操作').onclick();
 const field=h.root.querySelectorAll('textarea').find(n=>n.getAttribute('aria-label')==='需求或修改意见');field.value='保留正在编辑的需求';field.oninput();field.focus();field.setSelectionRange(2,6,'backward');
 await poll(h,[{...intent,state:'COMPLETED'}]);const fresh=h.root.querySelectorAll('textarea').find(n=>n.getAttribute('aria-label')==='需求或修改意见');assert.ok(fresh);assert.equal(fresh.value,'保留正在编辑的需求');assert.equal(h.context.document.activeElement,fresh);assert.equal(fresh.selectionStart,2);assert.equal(fresh.selectionEnd,6);assert.equal(fresh.selectionDirection,'backward');
 // A new Run invalidates report data, but is not a reason to close the user's composer.
 h.evaluate("workspace.presentation.taskView.currentReview={id:'review-new'};render()");assert.ok(h.root.querySelectorAll('textarea').find(n=>n.getAttribute('aria-label')==='需求或修改意见'));h.dispose();
});
test('fresh terminal confirmation invalidates its mounted controller even while preserving surrounding view state',async()=>{
 const h=await panel([intent]),mounts=[];h.context.window.DevBridgeConfirmationStack={mount(host,ref,hooks){const record={host,ref,hooks,disposed:false};mounts.push(record);return {dispose(){record.disposed=true;}};}};
 h.evaluate('for(const record of formalCards.values())record.host.isConnected=true;mountFormalCards()');assert.equal(mounts[0].hooks.canInteract(),true);details(h,'切换领域与任务').open=true;
 await poll(h,[{...intent,state:'COMPLETED'}]);assert.equal(mounts[0].disposed,true);assert.equal(mounts[0].hooks.canInteract(),false);assert.equal(details(h,'切换领域与任务').open,true);assert.equal(h.root.querySelectorAll('details').some(n=>n.getAttribute('data-confirmation-intent')===intent.intentId),false);h.dispose();
});
test('definition change clears previous view and confirmation state rather than migrating stale UI authority',async()=>{
 const h=await panel([intent]);details(h,'切换领域与任务').open=true;tab(h,'history');h.evaluate("workspace.presentation.taskView.details.taskDefinitionRevisionId='definition-new';render()");
 assert.equal(h.evaluate('operationTab'),'next');assert.equal(details(h,'切换领域与任务').open,undefined);assert.equal(h.evaluate('panelViewState.size'),0);h.dispose();
});
test('native dropdown and report text selection defer painting without retaining an invalid confirmation',async()=>{
 const h=await panel([intent]),mounts=[];h.context.window.DevBridgeConfirmationStack={mount(host,ref,hooks){const record={host,hooks,disposed:false};mounts.push(record);return {dispose(){record.disposed=true;}};}};
 h.evaluate('for(const record of formalCards.values())record.host.isConnected=true;mountFormalCards()');
 details(h,'切换领域与任务').open=true;const select=h.root.querySelectorAll('select')[0];select.focus();
 await poll(h,[{...intent,state:'COMPLETED'}]);assert.equal(h.root.querySelectorAll('select')[0],select);assert.equal(mounts[0].host.inert,true);assert.equal(mounts[0].hooks.canInteract(),false);
 h.context.document.activeElement=null;h.event('focusout',{});h.expire(0);await settle();assert.equal(mounts[0].disposed,true);assert.equal(details(h,'切换领域与任务').open,true);
 tab(h,'results');const selected=details(h,'任务要求与技术详情');selected.open=true;h.context.window.getSelection=()=>({isCollapsed:false,anchorNode:selected});
 await poll(h,[{...intent,state:'COMPLETED'},{...intent,intentId:'another',state:'COMPLETED'}]);assert.equal(details(h,'任务要求与技术详情'),selected);
 h.context.window.getSelection=()=>({isCollapsed:true});h.event('selectionchange',{});h.expire(0);await settle();assert.notEqual(details(h,'任务要求与技术详情'),selected);assert.equal(details(h,'任务要求与技术详情').open,true);h.dispose();
});
