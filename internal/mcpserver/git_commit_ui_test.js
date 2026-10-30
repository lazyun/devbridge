'use strict';

const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');

const html=fs.readFileSync(`${__dirname}/dev_bridge_ui.html`,'utf8');

// Git submission is no longer a Card-side workflow. The Card is a read-only
// presentation, while exact commit and recovery commands remain in the
// authoritative conversation/Core flow. These tests fence that contract.
test('FeatureArea commit cannot be submitted from the Card',()=>{
 assert.doesNotMatch(html,/function\s+renderCommit\b/);
 assert.doesNotMatch(html,/['\"]feature_area_submit['\"]/);
});
test('Task commit recovery cannot be submitted from the Card',()=>{
 assert.doesNotMatch(html,/['\"]task_commit_retry['\"]/);
 assert.doesNotMatch(html,/['\"]重试提交['\"]/);
});
test('Card contains no active commit mutation controls',()=>{
 assert.doesNotMatch(html,/正在提交…/);
 assert.doesNotMatch(html,/提交到\s*dev/);
});
test('Card keeps commit facts presentation-only',()=>{
 assert.match(html,/db-feature-commit/);
 assert.match(html,/commitSummary/);
});
