"use strict";
// Run the complete embedded app with a minimal DOM and its real JSON-RPC host
// boundary. No render/action functions are replaced or exported by the app.
const fs = require("node:fs");
const vm = require("node:vm");
const {webcrypto, createHash} = require("node:crypto");
class Element {
  constructor(tag) { this.tagName = tag; this.className = ""; this.childNodes = []; this._text = ""; this.disabled = false; }
  set textContent(value) { this._text = String(value); this.childNodes = []; }
  get textContent() { return this._text + this.childNodes.map(c => c.textContent).join(" "); }
  append(...nodes) { for (const node of nodes) { node.parentNode = this; this.childNodes.push(node); } }
  replaceChildren(...nodes) { this.textContent = ""; this.append(...nodes); }
  remove() { this.parentNode.childNodes = this.parentNode.childNodes.filter(c => c !== this); }
  querySelectorAll(selector) { return this.childNodes.flatMap(child => [...(selector.startsWith(".") ? child.className.split(" ").includes(selector.slice(1)) : child.tagName === selector) ? [child] : [], ...child.querySelectorAll(selector)]); }
  querySelector(selector) { return this.querySelectorAll(selector)[0] || null; }
  addEventListener() {}
  getBoundingClientRect() { return {width: 420, height: 200}; }
}
async function mount(html = fs.readFileSync(`${__dirname}/../decision_ui.html`, "utf8")) {
  const root = new Element("main"), listeners = {}, timers = new Map(), posts = [], waiting = [];
  let timerID = 0;
  const context = {
    crypto: webcrypto, TextEncoder, Uint8Array,
    document: {getElementById: () => root, createElement: tag => new Element(tag), querySelectorAll: selector => root.querySelectorAll(selector), body: {}, documentElement: {style: {}}},
    parent: {postMessage(message) { const value = structuredClone(message); posts.push(value); if (value.method === "tools/call" && waiting.length) waiting.shift()(value); }},
    addEventListener: (name, handler) => { listeners[name] = handler; },
    setTimeout: (callback, delay) => { const id = ++timerID; timers.set(id, {callback, delay}); return id; },
    clearTimeout: id => timers.delete(id),
    requestAnimationFrame: () => ++timerID, cancelAnimationFrame() {},
    ResizeObserver: class { observe() {} disconnect() {} },
    getComputedStyle: () => ({paddingTop: "0", paddingBottom: "0"}),
  };
  vm.runInNewContext(html.match(/<script>([\s\S]*)<\/script>/)[1], context);
  const send = data => listeners.message({data});
  const reply = (request, value) => send({jsonrpc: "2.0", id: request.id, result: {structuredContent: value}});
  const initialize = posts.find(p => p.method === "ui/initialize");
  send({jsonrpc: "2.0", id: initialize.id, result: {hostCapabilities: {}}});
  await Promise.resolve();
  return {
    root, posts, reply,
    render(value) { send({jsonrpc: "2.0", method: "ui/notifications/tool-result", params: {structuredContent: value}}); },
    input(value) { send({jsonrpc: "2.0", method: "ui/notifications/tool-input", params: {arguments: value}}); },
    nextCall() { return new Promise(resolve => waiting.push(resolve)); },
    button(label) { return root.querySelectorAll("button").find(b => b.textContent === label); },
    timeout() { const timer = [...timers.values()].find(t => t.delay === 15000); if (!timer) throw new Error("No pending tool timeout"); timer.callback(); },
  };
}
function pending(decisionId, aiSessionId = "session-one") {
  const requestId = option => "card-v2-" + createHash("sha256").update(JSON.stringify(["devbridge-decision-card-v2", decisionId, option, ""])).digest("hex") + "-" + option.toLowerCase();
  return {requestIds: {confirm: requestId("CONFIRM"), cancel: requestId("CANCEL")}, decisionState: "PENDING", decisionId, taskPlan: {id: decisionId.split(":")[1], taskId: "task-" + decisionId, aiSessionId, title: "Decision " + decisionId, status: "PENDING_CONFIRMATION", workSpec: {goal: "Confirm the exact Task", executionScope: {kind: "PATHS", paths: ["tracked.txt"]}}, verificationSpec: {mode: "NONE", waiver: {reason: "Fixture"}}}};
}
function resolved(value, requestId, option = "CONFIRM") {
  return {...value, decisionState: "RESOLVED", taskPlan: {...value.taskPlan, status: option === "CONFIRM" ? "APPLIED" : "REJECTED"}, resolution: {resolved: true, accepted: true, conflict: false, replay: false, resolutionReceipt: {decisionId: value.decisionId, option, requestId, resultKind: option === "CONFIRM" ? "EXECUTION_PROPOSAL" : "TASK_PLAN", resultId: "result-" + value.decisionId, resolvedAt: "2026-09-08T00:00:00Z"}}};
}
async function click(host, label) {
  const next = host.nextCall(), button = host.button(label);
  if (!button || button.disabled) throw new Error("Action unavailable: " + label);
  const completion = button.onclick();
  return {request: await next, completion};
}
module.exports = {mount, pending, resolved, click};
