'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');
function bind(v){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-refresh',entryIdentity:'readonly-refresh',display:'EXPANDED'};}
async function mount(){const v=fixture('REVIEW_APPROVAL_REQUIRED'),h=boot({manualIdentity:true});h.init();h.result(bind(v));await settle();h.reply(h.calls()[0],v);h.reply(h.calls()[1],{result:{branches}});await settle();return {h,v};}
test('manual refresh is a bounded read and contains no mutation call',async()=>{const {h,v}=await mount(),before=h.calls().length;h.evaluate('refreshCard()');await settle();const reads=h.calls().slice(before);assert.ok(reads.length>0);assert.equal(reads.every(c=>['dev_bridge_card_get','workspace_get'].includes(c.params.name)),true);h.reply(reads[0],v);if(reads[1])h.reply(reads[1],{result:{branches}});await settle();h.dispose();});
test('a late result after identity replacement is fenced',async()=>{const {h,v}=await mount(),before=h.calls().length;h.evaluate('refreshCard()');await settle();h.input({...bind(v),taskId:'other-task',presentationRequestId:'other',entryIdentity:'other'});h.reply(h.calls()[before],{...v,selectedTaskId:'foreign'});await settle();assert.doesNotMatch(h.root.textContent,/foreign/);h.dispose();});
