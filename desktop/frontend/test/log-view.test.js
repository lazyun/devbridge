import { strict as assert } from "node:assert";
import test from "node:test";
import {
  LOG_CATEGORIES,
  filterLogs,
  logCategory,
  logsViewText,
} from "../src/log-view.js";

const entry = (source, text = "message") => ({ time: "T", source, stream: "stdout", text });

test("categories are the fixed six-value set", () => {
  assert.deepEqual(LOG_CATEGORIES, ["all", "core", "mcp", "tunnel", "desktop", "codex"]);
});

test("known sources map to their category, including prefixed variants", () => {
  assert.equal(logCategory("core"), "core");
  assert.equal(logCategory("mcp"), "mcp");
  assert.equal(logCategory("tunnel"), "tunnel");
  assert.equal(logCategory("desktop"), "desktop");
  assert.equal(logCategory("codex"), "codex");
  assert.equal(logCategory("core/sub"), "core");
  assert.equal(logCategory("Tunnel"), "tunnel");
});

test("unknown sources have no invented category", () => {
  assert.equal(logCategory("nginx"), null);
  assert.equal(logCategory(""), null);
  assert.equal(logCategory(undefined), null);
});

test("filtering keeps All complete and is source-metadata driven", () => {
  const entries = [entry("core", "a"), entry("mcp", "b"), entry("tunnel", "c"), entry("helper", "d")];
  assert.equal(filterLogs(entries, "all").length, 4);
  assert.deepEqual(filterLogs(entries, "core").map((e) => e.text), ["a"]);
  assert.deepEqual(filterLogs(entries, "mcp").map((e) => e.text), ["b"]);
  assert.deepEqual(filterLogs(entries, "tunnel").map((e) => e.text), ["c"]);
  assert.deepEqual(filterLogs(entries, "desktop"), []);
  assert.deepEqual(filterLogs(entries, "codex"), []);
  // Unknown sources appear under All but join no known category.
  const unknown = [entry("helper", "d")];
  assert.equal(filterLogs(unknown, "all").length, 1);
  for (const category of ["core", "mcp", "tunnel", "desktop", "codex"]) {
    assert.deepEqual(filterLogs(unknown, category), []);
  }
  assert.deepEqual(filterLogs(entries, "helper"), []);
});

test("filtering never mutates the underlying log list", () => {
  const entries = [entry("core"), entry("mcp"), entry("tunnel")];
  const snapshot = JSON.stringify(entries);
  filterLogs(entries, "core");
  filterLogs(entries, "all");
  assert.equal(JSON.stringify(entries), snapshot);
});

test("view text renders the filtered bounded list or the empty label", () => {
  const entries = [entry("core", "one"), entry("mcp", "two")];
  assert.equal(
    logsViewText(entries, "all", "none"),
    "T [core/stdout] one\nT [mcp/stdout] two",
  );
  assert.equal(logsViewText(entries, "core", "none"), "T [core/stdout] one");
  assert.equal(logsViewText(entries, "tunnel", "none"), "none");
});
