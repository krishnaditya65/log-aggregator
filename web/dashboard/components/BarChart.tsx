"use client";

import { colorForKey } from "@/lib/colors";

export type BarDatum = { key: string; label: string; value: number };

// Horizontal bar chart: categorical magnitude comparison (top services by
// error count, hosts by volume, ...). Bars are capped at 24px thick with a
// 4px rounded data-end per the mark spec, value labeled at the tip rather
// than inside the bar (safer against long labels than an inline number).
export function BarChart({
  data,
  valueFormatter = (v: number) => String(v),
  maxBars = 10,
}: {
  data: BarDatum[];
  valueFormatter?: (v: number) => string;
  maxBars?: number;
}) {
  const sorted = [...data].sort((a, b) => b.value - a.value).slice(0, maxBars);
  const max = Math.max(1, ...sorted.map((d) => d.value));

  if (sorted.length === 0) {
    return <div className="empty-state">No data.</div>;
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      {sorted.map((d) => {
        const pct = Math.max(2, (d.value / max) * 100);
        return (
          <div key={d.key} style={{ display: "grid", gridTemplateColumns: "120px 1fr 56px", alignItems: "center", gap: 10 }}>
            <div
              style={{
                fontSize: 12,
                color: "var(--text-secondary)",
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
              title={d.label}
            >
              {d.label}
            </div>
            <div style={{ background: "var(--surface-2)", borderRadius: 4, height: 12 }}>
              <div
                style={{
                  width: `${pct}%`,
                  height: "100%",
                  background: colorForKey(d.key),
                  borderRadius: 4,
                  transition: "width 0.25s ease",
                }}
              />
            </div>
            <div style={{ fontSize: 12, fontWeight: 700, textAlign: "right", fontVariantNumeric: "tabular-nums" }}>
              {valueFormatter(d.value)}
            </div>
          </div>
        );
      })}
    </div>
  );
}
