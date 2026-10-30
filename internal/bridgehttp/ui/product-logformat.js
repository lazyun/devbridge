"use strict";

// Pure presentation helpers for the Console Logs view: per-component message
// formatting, protocol-noise classification, and structured filtering. These
// stay free of DOM access so node:test can exercise them directly.
(function (root) {
  const esc = (value) => String(value ?? "").replace(/[&<>'"]/g, (character) => ({"&":"&amp;","<":"&lt;",">":"&gt;","'":"&#39;",'"':"&quot;"}[character]));

  const durationText = (milliseconds) => {
    const parsed = Number(milliseconds);
    return Number.isFinite(parsed) && parsed >= 0 ? `${parsed} ms` : "";
  };

  const STATUS_TEXT = new Set(["completed", "succeeded", "failed"]);

  const dataString = (entry, key) => {
    const value = entry.data ? entry.data[key] : undefined;
    return value == null ? "" : String(value);
  };

  // formatLogLine renders the human summary for one structured log entry.
  // The canonical event stays authoritative; known data fields give it the
  // concrete method, path, tool, or status without inventing content.
  function formatLogLine(entry, t) {
    const data = entry.data || {};
    const component = String(entry.component || "");
    const event = String(entry.event || "");
    if (component === "http") {
      if (event === "local.request.failed") {
        const errorCode = String(entry.errorCode || data.errorCode || "");
        return `${dataString(entry, "method")} ${dataString(entry, "path")} — ${t("logs.line.failed")}${errorCode ? ` · ${errorCode}` : ""}`;
      }
      const status = dataString(entry, "status");
      const duration = durationText(data.durationMillis);
      return `${dataString(entry, "method")} ${dataString(entry, "path")}${status ? ` → ${status}` : ""}${duration ? ` · ${duration}` : ""}`.trim() || t("logs.line.default", {component});
    }
    if (component === "mcp" || component === "localmcp") {
      const tool = dataString(entry, "tool");
      const method = dataString(entry, "method");
      const label = STATUS_TEXT.has(event.slice(event.lastIndexOf(".") + 1)) ? t(`logs.line.${event.slice(event.lastIndexOf(".") + 1)}`) : "";
      const duration = durationText(data.durationMillis);
      if (tool) return `${tool}${label ? ` — ${label}` : ""}${duration ? ` · ${duration}` : ""}`;
      if (method) return `${method}${label ? ` — ${label}` : ""}${duration ? ` · ${duration}` : ""}`;
      return t("logs.line.default", {component});
    }
    if (event.includes("codex") || component.includes("codex")) {
      const thread = dataString(entry, "threadId");
      const turn = dataString(entry, "turnId");
      return `${dataString(entry, "event") || event}${thread ? ` · ${thread}` : ""}${turn ? ` · ${turn}` : ""}`.trim() || t("logs.line.default", {component});
    }
    if (component === "runtime" || component === "recovery") {
      const state = dataString(entry, "state");
      const reason = dataString(entry, "reason") || String(entry.message || "");
      return `${state || event}${reason ? ` — ${reason}` : ""}`.trim();
    }
    if (entry.message) return String(entry.message);
    return t("logs.line.default", {component});
  }

  // isProtocolNoise hides the handshake and receive-half chatter that drowns
  // out actionable events. ERROR and WARN levels always stay visible.
  function isProtocolNoise(entry) {
    const level = String(entry.level || "").toUpperCase();
    if (level === "ERROR" || level === "WARN") return false;
    const event = String(entry.event || "");
    const component = String(entry.component || "");
    const method = entry.data ? String(entry.data.method || "") : "";
    if (event === "localmcp.tool.failed" || event === "mcp.tool.failed") return false;
    if (method === "tools/list" || method === "ping" || method.startsWith("notifications/")) return true;
    if (event === "local.request.requested") return true;
    if (event === "local.request.completed") return true;
    if (event === "mcp.tool.requested") return true;
    if (event === "mcp.tool.succeeded" && dataString(entry, "tool") === "tools/list") return true;
    if (component === "mcp" && event.startsWith("mcp.tool.")) return false;
    if (component === "localmcp" && (event === "localmcp.tool.completed" || event === "localmcp.tool.failed")) {
      return method !== "" && method !== "tools/call";
    }
    return false;
  }

  // filterLogs applies the structured client-side filters. level/component/
  // event match exactly; text searches the canonical event, message, error
  // code, and serialized structured data. verbose re-admits protocol noise.
  function filterLogs(entries, {level, component, event, text, verbose}) {
    const needle = String(text || "").trim().toLocaleLowerCase();
    return entries.filter((entry) => {
      if (!verbose && isProtocolNoise(entry)) return false;
      if (level && String(entry.level || "") !== level) return false;
      if (component && String(entry.component || "") !== component) return false;
      if (event && String(entry.event || "") !== event) return false;
      if (!needle) return true;
      const corpus = [entry.event, entry.message, entry.errorCode, entry.data ? JSON.stringify(entry.data) : ""].filter(Boolean).join("\n").toLocaleLowerCase();
      return corpus.includes(needle);
    });
  }

  // logFacets derives the distinct values for the filter dropdowns from the
  // currently loaded entries, sorted deterministically.
  function logFacets(entries) {
    const collect = (key) => [...new Set(entries.map((entry) => String(entry[key] || "")).filter(Boolean))].sort();
    return {levels: collect("level"), components: collect("component"), events: collect("event")};
  }

  const exported = {formatLogLine, isProtocolNoise, filterLogs, logFacets};
  root.DevBridgeLogFormat = exported;
  if (typeof module !== "undefined" && module.exports) module.exports = exported;
})(typeof globalThis !== "undefined" ? globalThis : this);
