// Fixed categorical order (validated — see dataviz skill palette.md). Never
// cycle/reorder per-chart: color must follow the entity, not its rank, so a
// filter that changes which series are visible must not repaint survivors.
export const SERIES_VARS = [
  "--series-1",
  "--series-2",
  "--series-3",
  "--series-4",
  "--series-5",
  "--series-6",
  "--series-7",
  "--series-8",
] as const;

// Assigns a stable slot per key (label/service/host/...) so the same entity
// always gets the same color across renders and across charts, rather than
// a color depending on array index / sort order.
const keyToSlot = new Map<string, number>();
let nextSlot = 0;

export function colorForKey(key: string): string {
  if (!keyToSlot.has(key)) {
    keyToSlot.set(key, nextSlot % SERIES_VARS.length);
    nextSlot += 1;
  }
  return `var(${SERIES_VARS[keyToSlot.get(key)!]})`;
}

export function resetColorAssignment() {
  keyToSlot.clear();
  nextSlot = 0;
}

export const STATUS_COLORS = {
  good: "var(--status-good)",
  warning: "var(--status-warning)",
  serious: "var(--status-serious)",
  critical: "var(--status-critical)",
} as const;

// Maps free-text log levels / alert severities onto the fixed status ramp.
// Unknown levels fall back to neutral rather than borrowing a categorical
// series hue (status colors are reserved, never reused for "series N").
export function severityToStatus(level: string | undefined): keyof typeof STATUS_COLORS | "neutral" {
  const l = (level ?? "").toLowerCase();
  if (["critical", "fatal", "emerg", "emergency"].includes(l)) return "critical";
  if (["error", "err", "severe"].includes(l)) return "serious";
  if (["warn", "warning"].includes(l)) return "warning";
  if (["info", "debug", "trace", "notice"].includes(l)) return "good";
  return "neutral";
}

export function badgeClass(level: string | undefined): string {
  const status = severityToStatus(level);
  return `badge badge-${status}`;
}
