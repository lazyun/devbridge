"use strict";

// Pure state machine for the Console Logs view: pause/follow semantics and
// new-entry accounting live here so node:test can exercise them directly.
(function (root) {
  const initialState = () => ({paused: false, follow: true, pendingCount: 0, lastTimestamp: ""});

  // nextView folds one fetched batch (newest-first from the server) into the
  // current view state. While following, the batch replaces the visible
  // window; while paused, new entries are only counted and replayed on
  // resume. lastTimestamp tracks the newest seen entry for jump accounting.
  function nextView(view, fetched) {
    const newest = fetched.length ? String(fetched[0].timestamp || "") : view.lastTimestamp;
    if (view.paused) {
      const fresh = fetched.filter((entry) => String(entry.timestamp || "") > view.lastTimestamp);
      return {...view, pendingCount: view.pendingCount + fresh.length, lastTimestamp: newest > view.lastTimestamp ? newest : view.lastTimestamp};
    }
    return {...view, pendingCount: 0, lastTimestamp: newest};
  }

  function togglePause(view) {
    if (view.paused) {
      // Resuming replays the pending window on the next fetch; the count
      // clears when the replaced batch renders.
      return {...view, paused: false, follow: true, pendingCount: 0};
    }
    return {...view, paused: true, follow: false};
  }

  function jumpToLatest(view) {
    return {...view, paused: false, follow: true, pendingCount: 0};
  }

  const exported = {initialState, nextView, togglePause, jumpToLatest};
  root.DevBridgeLogView = exported;
  if (typeof module !== "undefined" && module.exports) module.exports = exported;
})(typeof globalThis !== "undefined" ? globalThis : this);
