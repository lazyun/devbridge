"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const html=fs.readFileSync(`${__dirname}/confirmation_ui.html`,"utf8");
const start=html.indexOf("function commandPermissionNotice("),end=html.indexOf("function renderOverview(",start);
function notice(purpose,action,variant){const context={intent:{purpose,action,state:"PENDING"},selectedVariant:()=>variant};vm.createContext(context);vm.runInContext(html.slice(start,end),context);return context.commandPermissionNotice();}
test("formal confirmation makes a proposed network grant explicit",()=>{
 assert.match(notice("EXECUTION","REQUEST_CHANGES",{workSpec:{commandPermissions:{networkAccess:true}}}),/允许实施和验证联网/);
 const inherited=notice("EXECUTION","CONFIRM_AND_START_IMPLEMENTATION",{workSpec:{}});
 assert.match(inherited,/未单独授予联网权限/);
 assert.match(inherited,/如项目已授权联网/);
 assert.doesNotMatch(inherited,/网络权限：关闭/);
 assert.match(inherited,/可信验证直接使用工作树并核对源码前后快照/);
 assert.match(notice("EXECUTION","START_APPROVED",{}),/此前已确认的方案/);
});
test("project network grants never turn a coding review into an online run",()=>{
 const review=notice("REVIEW","START_REVIEW",{executorKind:"CODING_AGENT",workSpec:{commandPermissions:{networkAccess:true}}});
 assert.match(review,/只读审查使用独立副本/);
 assert.match(review,/只读审查关闭网络/);
 assert.doesNotMatch(review,/可信验证|可继承/);
});
test("file review and completion are not described as a command grant",()=>{
 assert.equal(notice("REVIEW","START_REVIEW",{executorKind:"UPPER_LLM"}),"");
 assert.equal(notice("EXECUTION","COMPLETE_TASK",{workSpec:{commandPermissions:{networkAccess:true}}}),"");
});
