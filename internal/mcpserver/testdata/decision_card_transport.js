"use strict";
// The Go integration test forwards these unmodified app tool requests through
// fresh Streamable HTTP MCP sessions into the real application and SQLite store.
const assert = require("node:assert/strict");
const readline = require("node:readline");
const {createHash} = require("node:crypto");
const {mount, click} = require("./decision_card_host.js");
const messages = new Map();
let nextID = 1, start;
const fixtureReady = new Promise(resolve => { start = resolve; });
readline.createInterface({input: process.stdin}).on("line", line => {
  const value = JSON.parse(line);
  if (value.fixture) start(value.fixture);
  else { const waiting = messages.get(value.id); messages.delete(value.id); value.error ? waiting.reject(new Error(value.error)) : waiting.resolve(value.result); }
});
function call(name, args) {
  const id = nextID++;
  return new Promise((resolve, reject) => { messages.set(id, {resolve, reject}); process.stdout.write(JSON.stringify({id, method: "tools/call", params: {name, arguments: args}}) + "\n"); });
}
(async () => {
  const fixture = await fixtureReady;
  if (fixture.scenario === "rotated-remount") {
    const b = (await call("ui_decision_get", {aiSessionId: fixture.aiSessionId, decisionId: fixture.decisionB})).structuredContent;
    const baseID = "card-v2-" + createHash("sha256").update(JSON.stringify(["devbridge-decision-card-v2", fixture.decisionB, "CONFIRM", ""])).digest("hex") + "-confirm";
    assert.equal(b.requestIds.confirm, baseID);
    const staleCard = await mount(fixture.html); staleCard.input({aiSessionId: fixture.aiSessionId}); staleCard.render(b);
    // A valid independent caller claims B's base ID after B's first hydration.
    const a = (await call("ui_task_decide", {aiSessionId: fixture.aiSessionId, decisionId: fixture.decisionA, option: "CONFIRM", requestId: baseID})).structuredContent;
    assert.equal(a.resolution.accepted, true);
    const first = await click(staleCard, "Confirm task");
    assert.equal(first.request.params.arguments.requestId, baseID);
    const conflict = (await call(first.request.params.name, first.request.params.arguments)).structuredContent;
    assert.equal(conflict.resolution.conflict, true); assert.equal(conflict.resolution.accepted, false);
    const rotatedID = conflict.requestIds.confirm;
    assert.notEqual(rotatedID, baseID);
    staleCard.reply(first.request, conflict); await first.completion;
    assert.doesNotMatch(staleCard.root.textContent, /Resolved ·/);
    const held = await click(staleCard, "Try again");
    assert.equal(held.request.params.arguments.requestId, rotatedID);
    // Delay the host's outgoing mutation before server commit; the timeout
    // and fresh pending hydration are synchronized without sleeping.
    staleCard.timeout(); await held.completion;
    const freshPending = (await call("ui_decision_get", {aiSessionId: fixture.freshAISessionId, decisionId: fixture.decisionB})).structuredContent;
    assert.equal(freshPending.decisionState, "PENDING");
    assert.equal(freshPending.requestIds.confirm, rotatedID);
    const remounted = await mount(fixture.html); remounted.input({aiSessionId: fixture.freshAISessionId}); remounted.render(freshPending);
    const retry = await click(remounted, "Confirm task");
    assert.equal(retry.request.params.arguments.requestId, held.request.params.arguments.requestId);
    const committed = (await call(retry.request.params.name, retry.request.params.arguments)).structuredContent;
    assert.equal(committed.resolution.accepted, true);
    remounted.reply(retry.request, committed); await retry.completion;
    assert.match(remounted.root.textContent, /Resolved · CONFIRM/);
    const delayedOriginal = (await call(held.request.params.name, held.request.params.arguments)).structuredContent;
    assert.equal(delayedOriginal.resolution.replay, true);
    assert.deepEqual(delayedOriginal.resolution.resolutionReceipt, committed.resolution.resolutionReceipt);
    const hydration = (await call("ui_decision_get", {aiSessionId: fixture.freshAISessionId, decisionId: fixture.decisionB})).structuredContent;
    const finalMount = await mount(fixture.html); finalMount.render(hydration);
    assert.match(finalMount.root.textContent, /Resolved · CONFIRM/);
    process.stdout.write(JSON.stringify({done: true}) + "\n"); process.exit(0);
  }
  const argsA = {aiSessionId: fixture.aiSessionId, decisionId: fixture.decisionA, option: "CONFIRM", requestId: "card-e1b4c7e8-confirm"};
  const a = (await call("ui_task_decide", argsA)).structuredContent;
  assert.equal(a.resolution.accepted, true);
  assert.equal(a.resolution.resolutionReceipt.decisionId, fixture.decisionA);
  const bPending = (await call("ui_decision_get", {aiSessionId: fixture.aiSessionId, decisionId: fixture.decisionB})).structuredContent;
  assert.equal(bPending.decisionState, "PENDING");
  const conflict = (await call("ui_task_decide", {...argsA, decisionId: fixture.decisionB})).structuredContent;
  assert.equal(conflict.resolution.accepted, false);
  assert.equal(conflict.resolution.conflict, true);
  assert.equal(conflict.resolution.resolutionReceipt.decisionId, fixture.decisionA);
  const card = await mount(fixture.html); card.input({aiSessionId: fixture.aiSessionId}); card.render(conflict);
  assert.doesNotMatch(card.root.textContent, /Resolved ·/);
  const first = await click(card, "Confirm task");
  const firstArgs = first.request.params.arguments;
  assert.match(firstArgs.requestId, /^card-v2-[a-f0-9]{64}-confirm$/);
  assert.equal(firstArgs.decisionId, fixture.decisionB);
  const committed = (await call(first.request.params.name, firstArgs)).structuredContent;
  assert.equal(committed.resolution.accepted, true);
  assert.equal(committed.resolution.resolutionReceipt.decisionId, fixture.decisionB);
  // Drop the committed response at the host boundary, then fire the app's
  // deterministic timeout seam. No timer sleeps create this failure window.
  card.timeout(); await first.completion;
  assert.match(card.root.textContent, /OUTCOME_UNKNOWN/);
  const second = await click(card, "Retry");
  assert.deepEqual(second.request.params.arguments, firstArgs);
  const replay = (await call(second.request.params.name, second.request.params.arguments)).structuredContent;
  assert.equal(replay.resolution.replay, true);
  assert.deepEqual(replay.resolution.resolutionReceipt, committed.resolution.resolutionReceipt);
  card.reply(second.request, replay); await second.completion;
  assert.match(card.root.textContent, /Resolved · CONFIRM/);
  const freshCard = await mount(fixture.html);
  const hydrated = (await call("ui_decision_get", {aiSessionId: fixture.freshAISessionId, decisionId: fixture.decisionB})).structuredContent;
  freshCard.render(hydrated);
  assert.match(freshCard.root.textContent, /Resolved · CONFIRM/);
  assert.equal(freshCard.root.querySelectorAll("button").length, 0);
  process.stdout.write(JSON.stringify({done: true}) + "\n");
  process.exit(0);
})().catch(error => { process.stderr.write(error.stack + "\n"); process.exit(1); });
