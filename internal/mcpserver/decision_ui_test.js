"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const html = fs.readFileSync(`${__dirname}/decision_ui.html`, "utf8");
const identityCode = html.slice(html.indexOf("function rememberIdentity("), html.indexOf("function render("));
function identities() {
  const state = {workflowIdentity: {}, input: {}, pollTimer: null, pollGeneration: 0, pollIdentity: "", clearTimeout() {}};
  vm.createContext(state);
  vm.runInContext(identityCode, state);
  return state;
}
test("switching from a Review card to a Task clears the Review selector", () => {
  const state = identities();
  state.rememberIdentity({aiSessionId: "session", reviewId: "review-one", currentRun: {id: "run-review"}, phase: "FEATURE_AREA_REVIEW"});
  assert.equal(state.workflowIdentity.reviewId, "review-one");
  state.rememberIdentity({phase: "IMPLEMENTATION_RUNNING", task: {id: "task-one"}, currentRun: {id: "run-impl"}});
  assert.equal(state.workflowIdentity.taskId, "task-one");
  assert.equal(state.workflowIdentity.reviewId, "");
  assert.equal(state.workflowIdentity.runId, "run-impl");
});
test("switching from an Execution card to a Review clears all Task lineage", () => {
  const state = identities();
  state.rememberIdentity({executionProposal: {id: "execution-one", taskId: "task-one", taskPlanId: "plan-one"}});
  state.rememberIdentity({reviewId: "review-two", phase: "FEATURE_AREA_REVIEW", currentRun: {id: "run-review-two"}});
  assert.equal(state.workflowIdentity.reviewId, "review-two");
  assert.equal(state.workflowIdentity.taskId, "");
  assert.equal(state.workflowIdentity.taskPlanId, "");
  assert.equal(state.workflowIdentity.executionProposalId, "");
});
test("a new product Review replaces previous Execution polling ownership", () => {
  const state = identities();
  state.rememberIdentity({executionProposal: {id: "execution-one", taskId: "task-one", taskPlanId: "plan-one"}, run: {id: "run-one", taskId: "task-one"}});
  state.rememberIdentity({review: {id: "review-new", featureArea: {id: "area-new"}}});
  assert.equal(state.workflowIdentity.reviewId, "review-new");
  assert.equal(state.workflowIdentity.taskId, "");
  assert.equal(state.workflowIdentity.runId, "");
});
test("new Task polling discards previous plan and Execution selectors", () => {
  const state = identities();
  state.rememberIdentity({phase: "RESULT", task: {id: "task-one"}, taskPlanId: "plan-one", executionProposalId: "execution-one", currentRun: {id: "run-one"}});
  state.rememberIdentity({phase: "IMPLEMENTATION_RUNNING", task: {id: "task-one"}, currentRun: {id: "run-impl-two"}});
  assert.equal(state.workflowIdentity.taskPlanId, "");
  assert.equal(state.workflowIdentity.executionProposalId, "");
  assert.equal(state.workflowIdentity.runId, "run-impl-two");
});

const {mount, click} = require("./testdata/decision_card_host.js");

test("active Implementation and Verification Cards direct cancellation to chat without a legacy action", async () => {
  for (const purpose of ["IMPLEMENTATION", "VERIFICATION"]) {
    const host = await mount(); host.input({aiSessionId: "session-active"});
    host.render({run: {id: "run-active", taskId: "task-active", purpose, status: "RUNNING"}});
    assert.match(host.root.textContent, /To cancel this Execution, ask in chat\./);
    assert.equal(host.button("Cancel Task"), undefined);
    assert.equal(host.root.querySelectorAll("button").length, 0);
    assert.equal(host.posts.filter(p => p.method === "tools/call").length, 0);
    host.render({run: {id: "run-active", taskId: "task-active", purpose, status: "CANCELED"}});
    assert.doesNotMatch(host.root.textContent, /To cancel this Execution/);
    assert.equal(host.button("Cancel Task"), undefined);
  }
  const review = await mount();
  review.render({run: {id: "run-review", purpose: "SCOPE_REVIEW", status: "RUNNING"}});
  assert.doesNotMatch(review.root.textContent, /To cancel this Execution|Cancel Task/);
});

test("internal plans and proposals never expose retired authorization actions", async () => {
  for (const data of [{taskPlan:{id:"plan",taskId:"task",status:"PENDING_CONFIRMATION",workSpec:{goal:"Intent"}}},{executionProposal:{id:"exec",taskId:"task",status:"PENDING_CONFIRMATION",authorizationActionable:true,workSpec:{goal:"Intent"}}},{currentState:{pendingDecision:{decisionKind:"TASK_CONFIRMATION",decisionId:"TASK_CONFIRMATION:plan"}}}]) {
    const host=await mount(); host.render(data);
    for (const label of ["Confirm task","Cancel","Execute"]) assert.equal(host.button(label),undefined);
    assert.equal(host.posts.filter(p=>p.method==="tools/call").length,0);
  }
  assert.doesNotMatch(html,/ui_task_decide|ui_execution_decide|ui_decision_get/);
});

test("runtime Approval response loss retries exact identity and rejects foreign readback", async () => {
  const host=await mount(); host.input({aiSessionId:"session"});
  const approval={id:"approval",runId:"run",status:"PENDING",request:{command:"go test"},allowedDecisions:["CANCEL"],cancelEffect:"DECLINE_ACTION"};
  host.render({approval}); const action=await click(host,"Decline action");
  host.timeout();await action.completion;
  assert.match(host.root.textContent,/Retry uses the same Decision identity/);
  const retry=await click(host,"Retry");assert.deepEqual(retry.request.params.arguments,action.request.params.arguments);
  host.reply(retry.request,{guarantee:"AUTHORITATIVE_ENTITY_STATE",entityDecision:{decisionId:"RUNTIME_APPROVAL:foreign",subjectState:"DELIVERED",recordedOption:"CANCEL"},approval:{...approval,status:"DELIVERED"}});await retry.completion;
  assert.match(host.root.textContent,/OUTCOME_UNKNOWN/);
});

test("Approval CANCEL describes current-action refusal separately from legacy Run cancellation", async () => {
  for (const [effect, label] of [["DECLINE_ACTION", "Decline action"], ["CANCEL_LEGACY_RUN", "Cancel legacy Run"]]) {
    const host = await mount(); host.input({aiSessionId: "session"});
    const approval = {id: "approval", runId: "run", status: "PENDING", request: {command: "go test"}, allowedDecisions: ["CANCEL"], cancelEffect: effect};
    host.render({approval});
    assert.ok(host.button(label));
    if (effect === "DECLINE_ACTION") assert.match(host.root.textContent, /does not cancel the whole operation/);
    else assert.doesNotMatch(host.root.textContent, /does not cancel the whole operation/);
    const action = await click(host, label);
    assert.equal(action.request.params.arguments.decision, "CANCEL");
    host.reply(action.request, {guarantee: "AUTHORITATIVE_ENTITY_STATE", entityDecision: {decisionId: "RUNTIME_APPROVAL:approval", subjectState: "DELIVERED", recordedOption: "CANCEL", alreadyResolved: false, conflict: false}, approval: {...approval, status: "DELIVERED"}});
    await action.completion;
    assert.equal(host.button(label), undefined);
  }
});

test("Run token metrics distinguish unavailable, pending and observed zero", async () => {
  for (const [tokenUsage, label] of [
    [{status: "UNAVAILABLE"}, "Token usage unavailable"],
    [{status: "PENDING"}, "Token usage pending"],
    [{status: "AVAILABLE", usage: {totalTokens: 0}}, "0 tokens"],
    [{status: "UNAVAILABLE", usage: {totalTokens: 0}}, "Token usage unavailable"],
  ]) {
    const host = await mount();
    host.render({run: {id: "run-usage", purpose: "IMPLEMENTATION", status: "SUCCEEDED", tokenUsage}});
    assert.ok(host.root.textContent.includes(label), host.root.textContent);
    if (tokenUsage.status !== "AVAILABLE") assert.doesNotMatch(host.root.textContent, /0 tokens/);
  }
});


test("raw pending proposal alone cannot render an Execution decision", async () => {
  const host = await mount();
  host.render({executionProposal:{id:"canceled",status:"PENDING_CONFIRMATION",outcome:"CANCELED",authorizationActionable:false}});
  assert.equal(host.button("Execute"),undefined);
  assert.equal(host.button("Cancel"),undefined);
});

test("production orphan Card transport payload preserves disposition in mounted DOM", {skip: !process.env.DEVBRIDGE_ORPHAN_CARD_FIXTURE}, async () => {
  const card=JSON.parse(fs.readFileSync(process.env.DEVBRIDGE_ORPHAN_CARD_FIXTURE,"utf8"));
  assert.equal(card.phase,"UNKNOWN_ORPHANED");
  assert.equal(card.agentExecution.status,"UNKNOWN_ORPHANED");
  assert.equal(card.agentExecution.backendDisposition,"UNKNOWN_ORPHANED");
  const host=await mount(); host.render(card);
  const status=host.root.querySelectorAll("span").find(v=>v.className==="status");
  assert.equal(status.textContent,"UNKNOWN_ORPHANED");
  assert.match(host.root.textContent,/workspace and context quarantined/);
  assert.match(host.root.textContent,/Physical backend termination is not proven/);
  for(const label of ["Execute","Cancel","Start","Retry"]) assert.equal(host.button(label),undefined);
  assert.doesNotMatch(host.root.textContent,/To cancel this Execution/);
});
