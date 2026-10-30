"use strict";
const test = require("node:test");
const assert = require("node:assert/strict");
const {create} = require("./ui/product-retire.js");

const area = {projectId: "project", id: "area", title: "Console"};
function deferred() { let resolve; const promise = new Promise(done => { resolve = done; }); return {promise, resolve}; }
function fixture(options = {}) {
  const calls = {ids: 0, writes: [], reads: [], changes: 0};
  const controller = create({
    requestId: () => `retire-${++calls.ids}`,
    mutate: async command => { calls.writes.push(command); return {id: command.id, status: "RETIRING"}; },
    refresh: async (topic, result) => { calls.reads.push({topic, result}); },
    changed: () => { calls.changes++; }, ...options
  });
  return {controller, calls};
}

test("opening and canceling retirement never create a command identity or mutate", async () => {
  const {controller, calls} = fixture();
  controller.open(area);
  assert.deepEqual(controller.state(), {topic: area, busy: false, error: ""});
  controller.state().topic.id = "tampered";
  assert.equal(controller.state().topic.id, area.id);
  controller.cancel();
  await controller.confirm();
  assert.deepEqual(controller.state(), {topic: null, busy: false, error: ""});
  assert.equal(calls.ids, 0);
  assert.equal(calls.writes.length, 0);
  assert.equal(calls.reads.length, 0);
});

test("confirm submits exactly once and keeps dialog busy until projection refresh completes", async () => {
  const write = deferred(), read = deferred(), events = [];
  const {controller, calls} = fixture({
    mutate: async command => { events.push(command); await write.promise; return {id: area.id, status: "RETIRING"}; },
    refresh: async (topic, result) => { events.push({topic, result}); await read.promise; }
  });
  controller.open(area);
  const first = controller.confirm(), second = controller.confirm();
  assert.equal(first, second);
  assert.equal(calls.ids, 1);
  assert.equal(controller.state().busy, true);
  await Promise.resolve();
  assert.deepEqual(events, [{...area, requestId: "retire-1"}]);
  write.resolve();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(controller.state().busy, true);
  assert.deepEqual(events[1], {topic: area, result: {id: area.id, status: "RETIRING"}});
  read.resolve();
  await first;
  assert.deepEqual(controller.state(), {topic: null, busy: false, error: ""});
});

test("transport failure retries the same identity after cancel and reopen", async () => {
  const writes = [];
  const {controller, calls} = fixture({mutate: async value => {
    writes.push(value);
    if (writes.length === 1) throw new Error("Transport unavailable");
    return {id: value.id, status: "RETIRED"};
  }});
  controller.open(area);
  await controller.confirm();
  assert.equal(controller.state().error, "Transport unavailable");
  assert.equal(controller.state().busy, false);
  controller.cancel();
  controller.open(area);
  await controller.confirm();
  assert.equal(calls.ids, 1);
  assert.equal(writes.length, 2);
  assert.equal(writes[0].requestId, writes[1].requestId);
  assert.equal(controller.state().topic, null);
});

test("successful mutation followed by refresh failure retries only the refresh", async () => {
  let reads = 0;
  const {controller, calls} = fixture({refresh: async () => { if (++reads === 1) throw new Error("Readback failed"); }});
  controller.open(area);
  await controller.confirm();
  assert.equal(controller.state().error, "Readback failed");
  await controller.confirm();
  assert.equal(calls.ids, 1);
  assert.equal(calls.writes.length, 1);
  assert.equal(reads, 2);
  assert.equal(controller.state().topic, null);
});

test("late completion cannot close another Feature confirmation and project identity fences commands", async () => {
  const pending = deferred();
  const writes = [];
  const {controller, calls} = fixture({mutate: async value => {
    writes.push(value);
    if (value.projectId === area.projectId) await pending.promise;
    return {id: value.id, status: "RETIRED"};
  }});
  controller.open(area);
  const first = controller.confirm();
  controller.cancel();
  const other = {...area, projectId: "other"};
  controller.open(other);
  pending.resolve();
  await first;
  assert.deepEqual(controller.state().topic, other);
  await controller.confirm();
  assert.equal(calls.ids, 2);
  assert.notEqual(writes[0].requestId, writes[1].requestId);
});

test("reopening the same Feature while a request is pending cannot duplicate the write", async () => {
  const pending = deferred();
  let writes = 0;
  const {controller, calls} = fixture({mutate: async () => { writes++; await pending.promise; return {id: area.id, status: "RETIRED"}; }});
  controller.open(area);
  const first = controller.confirm();
  controller.cancel();
  controller.open(area);
  assert.equal(controller.state().busy, true);
  assert.equal(controller.confirm(), first);
  pending.resolve();
  await first;
  assert.equal(writes, 1);
  assert.equal(calls.ids, 1);
  assert.equal(controller.state().busy, false);
});
