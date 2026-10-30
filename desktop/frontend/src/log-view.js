// Pure Desktop log-view state: category derivation, filtering, and view text.
// Categories come only from the authoritative log entry source metadata —
// never from parsing message text.

export const LOG_CATEGORIES = ["all", "core", "mcp", "tunnel", "desktop", "codex"];

// logCategory maps a log entry source to its known category, or null when the
// source has no known category (such entries appear only under All).
export function logCategory(source) {
  const value = String(source || "").toLowerCase();
  for (const category of LOG_CATEGORIES) {
    if (category === "all") continue;
    if (value === category || value.startsWith(category + "/")) return category;
  }
  return null;
}

export function filterLogs(entries, category) {
  const list = Array.isArray(entries) ? entries : [];
  if (!category || category === "all") return list.slice();
  return list.filter((entry) => logCategory(entry?.source) === category);
}

export function formatLogEntry(entry) {
  return `${entry?.time || ""} [${entry?.source || ""}/${entry?.stream || ""}] ${entry?.text || ""}`;
}

// logsViewText renders the bounded, filtered log viewport text. The
// underlying entries are never mutated: filtering returns a new list.
export function logsViewText(entries, category, emptyLabel) {
  const filtered = filterLogs(entries, category);
  return filtered.length ? filtered.map(formatLogEntry).join("\n") : emptyLabel;
}
