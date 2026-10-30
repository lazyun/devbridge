"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
function fixture({confirmed=true,channel=true,available=true,held=false}={}){
 const target={taskId:"task",featureAreaId:"area",subjectId:"task",expectedReviewRequest:{phase:"RECOVERY_REQUIRED",taskDefinitionRevisionId:"revision",currentExecutionId:"execution",currentReviewId:"review",pendingDecisionId:"decision"}};
 const acceptance={taskId:"task",featureAreaId:"area",subjectId:"execution",executionId:"execution",taskDefinitionRevisionId:"revision",decisionId:"TASK_ACCEPTANCE:execution"};
 const actions=[{action:"CONTINUE_TASK",available:true,target:acceptance},{action:"COMPLETE_TASK",available:false,reason:"verification missing",target:acceptance},{action:"DEFER_TASK",available:true,target:acceptance},{action:"REQUEST_TASK_REVIEW",available,reason:available?"":"RUNTIME_AUTHORITY_BUSY",target}];
 const p={details:{featureAreaId:"area"},taskView:{id:"task",phase:"RECOVERY_REQUIRED",status:"ACTIVE",actionsStatus:"AVAILABLE",activity:{},actions,verificationDecisions:{continuationConfirmed:confirmed,...(held?{hold:{note:"paused"}}:{})}}};
 const context={C:require("./ui/product-components.js"),TW:{taskOutcome:()=>"",taskResults:()=>"",tabs:()=>""},t:k=>k,i18n:{locale:"zh-CN"},state:{selectedProjectID:"project"},openedTask:{operatorActions:actions.map(c=>({action:c.action,target:c.target,domainAvailable:c.available,channelAvailable:c.action==="REQUEST_TASK_REVIEW"?channel:true}))},taskRunsPage:null,detailRoute:{get:()=>null},directFileActivityBadge:()=>"",renderUnconfirmedTaskRequests:()=>"",taskExecutionStateFacts:()=>"",taskImplementationFacts:()=>""};
 vm.createContext(context);
 vm.runInContext(source.slice(source.indexOf("  function taskActionIdentity("),source.indexOf("  function taskRequestContext(")),context);
 vm.runInContext(source.slice(source.indexOf("  const taskActionLabels="),source.indexOf("  function runtimeChoices(")),context);
 vm.runInContext(source.slice(source.indexOf("  function renderTaskActionControl("),source.indexOf("  function taskFormDraftAuthority(")),context);
 return context.renderTaskPresentation(p);
}
test("confirmed WebUI continuation exposes exact supplemental form without repeating Continue or hiding it in other actions",()=>{
 const html=fixture();
 assert.match(html,/已确认延续原任务，无需重复确认/);
 assert.match(html,/name="reviewPurpose" required/);
 assert.equal((html.match(/data-task-review-request/g)||[]).length,1);
 assert.doesNotMatch(html,/data-task-action="CONTINUE_TASK"|请在原 AI 对话准备修订方案/);
 const section=html.match(/<section class="task-continuation-next"[\s\S]*?<\/section>/)[0];
 assert.match(section,/data-task-action="REQUEST_TASK_REVIEW"/);
 assert.match(section,/&quot;currentExecutionId&quot;:&quot;execution&quot;/);
});
test("continuation form cannot bypass missing operator identity, stale eligibility or a deferred Task",()=>{
 for(const options of [{channel:false},{available:false},{held:true}]){
  const html=fixture(options),section=html.match(/<section class="task-continuation-next"[\s\S]*?<\/section>/)?.[0]||"";
  assert.doesNotMatch(section,/data-task-review-request/);
 }
 const before=fixture({confirmed:false});assert.match(before,/data-task-action="CONTINUE_TASK"/);assert.doesNotMatch(before,/task-continuation-next/);
});
