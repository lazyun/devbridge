'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs');
const {boot,settle,finishRefresh}=require('./dev_bridge_ui_test.js');
test('candidate authoritative Review and Implementation snapshots replace one mounted Card', {skip:!process.env.DEVBRIDGE_CARD_SNAPSHOTS},async()=>{
 const fixture=JSON.parse(fs.readFileSync(process.env.DEVBRIDGE_CARD_SNAPSHOTS,'utf8'));
 for(const pair of fixture.transitions){
  const h=boot({widgetState:{aiSessionId:fixture.session,featureAreaId:pair[0].selectedFeatureAreaId}});h.init();await settle();
  finishRefresh(h,0,pair[0]);await settle();assert.equal(h.polls().length,1);
  const mounted=h.context.window.devBridgeCard;h.poll();await settle();finishRefresh(h,2,pair[1]);await settle();
  assert.equal(h.context.window.devBridgeCard,mounted);assert.equal(h.polls().length,0);assert.doesNotMatch(h.root.textContent,/Projection inconsistency|Dev Bridge Card is unavailable/);
  assert.ok(h.calls().every(v=>['dev_bridge_card_get','workspace_get'].includes(v.params.name)));
  if(pair[1].presentation.currentState==='REVIEW_APPROVAL_REQUIRED')assert.match(h.root.textContent,/确认/);
  h.dispose();
 }
});

test('candidate Approval action continues into Implementation on the same mounted Card', {skip:!process.env.DEVBRIDGE_CARD_SNAPSHOTS},async()=>{
 const f=JSON.parse(fs.readFileSync(process.env.DEVBRIDGE_CARD_SNAPSHOTS,'utf8'));
 const h=boot({widgetState:{aiSessionId:f.session,featureAreaId:f.approval.selectedFeatureAreaId}});h.init();await settle();finishRefresh(h,0,f.approval);await settle();assert.equal(h.polls().length,0);
 const mounted=h.context.window.devBridgeCard,approve=h.root.querySelectorAll('button').find(v=>v.textContent==='确认');assert.ok(approve);
 const work=approve.onclick();for(let i=0;i<30&&!h.calls()[2];i++)await settle();
 assert.equal(h.calls()[2].params.name,'decision_respond');h.reply(h.calls()[2],{});await settle();h.reply(h.calls()[3],{});await settle();finishRefresh(h,4,f.running);await work;await settle();
 assert.equal(h.polls().length,1);assert.match(h.root.textContent,/Implementation/);assert.equal(h.context.window.devBridgeCard,mounted);
 h.poll();await settle();finishRefresh(h,6,f.terminal);await settle();assert.equal(h.polls().length,1);
 assert.ok(h.calls().every(v=>['decision_respond','workspace_get','dev_bridge_card_get','workspace_get'].includes(v.params.name)));h.dispose();
});

test('candidate exact durable Review completion sends one host follow-up while Approval remains pending', {skip:!process.env.DEVBRIDGE_CARD_SNAPSHOTS},async()=>{
 const f=JSON.parse(fs.readFileSync(process.env.DEVBRIDGE_CARD_SNAPSHOTS,'utf8')),v=f.approval,g=v.presentation.workflowGuidance;
 const h=boot({hostCapabilities:{message:{text:{}},updateModelContext:{text:{}}},widgetState:{aiSessionId:f.session,featureAreaId:v.selectedFeatureAreaId}});
 h.init();await settle();finishRefresh(h,0,f.transitions[0][0]);await settle();h.poll();finishRefresh(h,2,v);await settle();
 const context=h.posts.find(p=>p.method==='ui/update-model-context');assert.ok(context,JSON.stringify(v.presentation));assert.match(h.root.textContent,/确认/);assert.equal(h.polls().length,0);
 h.hostReply(context);await settle();finishRefresh(h,5,v);await settle();
 const message=h.posts.find(p=>p.method==='ui/message');assert.ok(message);h.hostReply(message);await settle();
 assert.equal(h.visibleMessages.length,1);for(const id of [f.session,g.projectId,g.featureAreaId,g.reviewId,g.decisionId,g.reviewApproval.resultSha256,g.reviewApproval.provenanceSha256])assert.ok(message.params.content[0].text.includes(id));
 const work=h.context.window.devBridgeCard.refresh();finishRefresh(h,7,v);await work;assert.equal(h.posts.filter(p=>p.method==='ui/message').length,1);assert.ok(h.calls().every(p=>['dev_bridge_card_get','workspace_get','dev_bridge_review_wakeup_claim'].includes(p.params.name)));h.dispose();
});
