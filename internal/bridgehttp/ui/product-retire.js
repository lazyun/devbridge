"use strict";

(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) module.exports = api;
  else root.DevBridgeRetire = api;
})(typeof window !== "undefined" ? window : globalThis, function () {
  // A confirmation owns UI state; a command owns its durable identity. Closing
  // the dialog never cancels a submitted command or changes its retry identity.
  function create({requestId, mutate, refresh, changed = () => {}}) {
    const commands = new Map();
    let topic = null, error = "", generation = 0;
    const keyFor = value => JSON.stringify([value.projectId, value.id]);
    function state() {
      return {topic: topic ? {...topic} : null, busy: !!(topic && commands.get(keyFor(topic))?.flight), error};
    }
    function open(value) {
      if (!value?.projectId || !value?.id) return;
      topic = {projectId: value.projectId, id: value.id, title: value.title || ""};
      error = "";
      generation++;
      changed();
    }
    function cancel() {
      topic = null;
      error = "";
      generation++;
      changed();
    }
    function confirm() {
      if (!topic) return Promise.resolve();
      const selected = {...topic}, key = keyFor(selected), version = generation;
      let command = commands.get(key);
      if (command?.flight) return command.flight;
      if (!command) {
        command = {requestId: requestId(), mutated: false, result: undefined, flight: null};
        commands.set(key, command);
      }
      error = "";
      const current = () => topic && keyFor(topic) === key && generation === version;
      // Defer callbacks until flight is registered, including synchronous
      // callback failures and reentrant confirm attempts from changed().
      command.flight = Promise.resolve().then(async () => {
        try {
          if (!command.mutated) {
            command.result = await mutate({...selected, requestId: command.requestId});
            command.mutated = true;
          }
          await refresh(selected, command.result);
          if (current()) { topic = null; error = ""; generation++; }
        } catch (failure) {
          if (current()) error = failure?.message || String(failure);
        } finally {
          command.flight = null;
          changed();
        }
      });
      changed();
      return command.flight;
    }
    return {open, cancel, confirm, state};
  }
  return {create};
});
