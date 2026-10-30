"use strict";
const test=require("node:test");
const assert=require("node:assert/strict");
const fs=require("node:fs");
const C=require("./ui/product-components.js");
const I18n=require("./ui/product-i18n.js");
const base={kind:"FEATURE_AREA",path:"/tmp/<script>alert(1)</script>",ownerType:"FEATURE_AREA",ownerId:"area",repositoryId:"repo",taskId:"task",gitRegistrationKnown:true,directoryExistenceKnown:true,workingTreeState:"CLEAN",integrationState:"UNKNOWN",activityState:"IDLE"};

test("inventory distinguishes removed, leftover directory and stale Git metadata in both languages",()=>{
  for(const locale of ["en","zh-CN"]){
    const {t}=I18n.createI18n({navigator:{languages:[locale]}});
    const items=[{...base,cleanupState:"REMOVED",gitRegistered:false,directoryExists:false},{...base,cleanupState:"CLEANUP_PENDING",gitRegistered:false,directoryExists:true},{...base,cleanupState:"MISSING_DIRECTORY",gitRegistered:true,directoryExists:false}];
    const html=C.worktreeInventory({items,refreshedAt:"2026-09-12T00:00:00Z"},t,locale);
    for(const state of ["REMOVED","CLEANUP_PENDING","MISSING_DIRECTORY"])assert.ok(html.includes(t(`inventory.state.${state}`)));
    assert.ok(html.includes(t("inventory.gitRegistered")));
    assert.ok(html.includes(t("inventory.directoryExists")));
    assert.equal((html.match(/<details /g)||[]).length,3);
    assert.doesNotMatch(html,/<script>|<button|<form|onclick=|\[inventory\./);
    assert.match(html,/&lt;script&gt;/);
  }
});

test("formal, active, dirty, unique commits and unknown facts retain Core cleanup state",()=>{
  const {t}=I18n.createI18n({navigator:{languages:["en"]}});
  for(const state of ["PROTECTED_FORMAL","ACTIVE","PROTECTED_DIRTY","PROTECTED_UNMERGED","UNKNOWN","SAFE_TO_CLEAN"]){
    const html=C.worktreeInventory({items:[{...base,kind:state==="PROTECTED_FORMAL"?"FORMAL":"FEATURE_AREA",cleanupState:state,gitRegistrationKnown:false}]},t,"en");
    assert.ok(html.includes(t(`inventory.state.${state}`)));
    assert.doesNotMatch(html,/<button|\[inventory\./);
  }
});

test("workspace refresh is a fenced read of shared inventory and exposes no cleanup command",()=>{
  const app=fs.readFileSync(require.resolve("./ui/product-app.js"),"utf8");
  const read=app.slice(app.indexOf("async function loadWorktreeInventory()"),app.indexOf("\n  function ",app.indexOf("async function loadWorktreeInventory()")));
  assert.match(read,/\/ui\/api\/worktree-inventory/);
  assert.match(read,/state.selectedProjectID===projectID && version===worktreeReadVersion/);
  assert.doesNotMatch(read,/method\s*:|POST|DELETE|PUT/);
  for(const file of ["product-app.js","product-components.js"]){const source=fs.readFileSync(require.resolve(`./ui/${file}`),"utf8");assert.doesNotMatch(source,/git worktree (remove|prune)|rm -rf|child_process|execSync|data-worktree-(delete|cleanup)/);}
});
