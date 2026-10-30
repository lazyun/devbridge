'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {createCardInstance,fixture,toolInputOf,renderedText,renderedButtons}=require('./confirmation_card_instance_mount_test.js');
test('task-panel confirmation keeps the exact action and folds explanation without repeating task identity',async()=>{
 const card=createCardInstance('compact-task-panel');card.peek('window.devBridgeTaskPanel=true');await card.settle();
 await card.pushToolInput(toolInputOf(fixture.replacement));await card.pushToolResult(fixture.replacement.entry);
 assert.equal(card.peek('renderFailed'),false);assert.equal(card.peek('entry.intentId'),fixture.replacement.intentId);
 // A display-only summary exercises the compact disclosure; identity and formal
 // action eligibility still come from the real captured wire fixture.
 card.peek("selectedVariant().displaySummary={summary:'在原任务上按反馈修订审查方案',keyConstraints:['保留原任务和审查历史']};safeRender()");
 const root=card.peek('root'),find=(node,tag)=>[...(node.tagName===tag?[node]:[]),...(node.childNodes||[]).flatMap(child=>find(child,tag))];
 assert.equal(find(root,'H1').length,0);const explanation=find(root,'DETAILS').find(node=>renderedText(node).includes('操作说明与范围'));assert.ok(explanation);assert.equal(explanation.open,false);
 assert.ok(renderedButtons(root).some(button=>button.label==='确认要求修订'&&!button.disabled));
 assert.equal(card.toolCalls.some(call=>call.name==='confirmation_commit'),false);
});
