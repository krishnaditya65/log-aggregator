"use client";

import { useMemo, useState } from "react";
import type { Span } from "@/lib/api";
import { colorForKey } from "@/lib/colors";
import { formatDuration } from "@/lib/format";

type Node = Span & { depth: number; children: Node[] };

function buildTree(spans: Span[]): { roots: Node[]; minStart: number } {
  const byId = new Map<string, Node>();
  for (const s of spans) byId.set(s.SpanID, { ...s, depth: 0, children: [] });
  const roots: Node[] = [];
  for (const s of spans) {
    const node = byId.get(s.SpanID)!;
    const parent = s.ParentSpanID ? byId.get(s.ParentSpanID) : undefined;
    if (parent) parent.children.push(node);
    else roots.push(node);
  }
  const assignDepth = (node: Node, depth: number) => {
    node.depth = depth;
    node.children.sort((a, b) => a.StartTime - b.StartTime);
    node.children.forEach((c) => assignDepth(c, depth + 1));
  };
  roots.sort((a, b) => a.StartTime - b.StartTime);
  roots.forEach((r) => assignDepth(r, 0));
  const minStart = Math.min(...spans.map((s) => s.StartTime));
  return { roots, minStart };
}

function flatten(roots: Node[]): Node[] {
  const out: Node[] = [];
  const walk = (n: Node) => {
    out.push(n);
    n.children.forEach(walk);
  };
  roots.forEach(walk);
  return out;
}

// Trace waterfall: one horizontal bar per span, indented by call depth,
// positioned/scaled against total trace duration. Color follows service
// identity (same service = same hue everywhere in the app).
export function Waterfall({ spans }: { spans: Span[] }) {
  const [hoverId, setHoverId] = useState<string | null>(null);

  const { flat, totalNs, minStart } = useMemo(() => {
    if (spans.length === 0) return { flat: [] as Node[], totalNs: 1, minStart: 0 };
    const { roots, minStart } = buildTree(spans);
    const flat = flatten(roots);
    const maxEnd = Math.max(...spans.map((s) => s.EndTime));
    return { flat, totalNs: Math.max(1, maxEnd - minStart), minStart };
  }, [spans]);

  if (spans.length === 0) return <div className="empty-state">No spans.</div>;

  const rowH = 26;

  return (
    <div>
      <div className="legend" style={{ marginTop: 0, marginBottom: 10 }}>
        {[...new Set(spans.map((s) => s.Service))].map((svc) => (
          <div className="legend-item" key={svc}>
            <span className="legend-swatch" style={{ background: colorForKey(svc) }} />
            {svc}
          </div>
        ))}
      </div>
      <div style={{ display: "flex", flexDirection: "column", gap: 2 }}>
        {flat.map((n) => {
          const offsetPct = ((n.StartTime - minStart) / totalNs) * 100;
          const durationNs = Math.max(0, n.EndTime - n.StartTime);
          const widthPct = Math.max(0.4, (durationNs / totalNs) * 100);
          const isError = (n.Status ?? "").toLowerCase().includes("error");
          return (
            <div
              key={n.SpanID}
              style={{ display: "flex", alignItems: "center", gap: 8, height: rowH, position: "relative" }}
              onMouseEnter={() => setHoverId(n.SpanID)}
              onMouseLeave={() => setHoverId((id) => (id === n.SpanID ? null : id))}
            >
              <div
                style={{
                  width: 200,
                  paddingLeft: n.depth * 14,
                  fontSize: 11.5,
                  color: "var(--text-secondary)",
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                  flex: "none",
                }}
                title={n.Name}
              >
                {n.Name}
              </div>
              <div style={{ flex: 1, position: "relative", height: 16, background: "var(--surface-2)", borderRadius: 4 }}>
                <div
                  style={{
                    position: "absolute",
                    left: `${offsetPct}%`,
                    width: `${widthPct}%`,
                    height: "100%",
                    borderRadius: 4,
                    background: isError ? "var(--status-critical)" : colorForKey(n.Service),
                    outline: hoverId === n.SpanID ? "2px solid var(--surface-1)" : "none",
                    boxShadow: hoverId === n.SpanID ? "0 0 0 1px var(--text-primary)" : "none",
                  }}
                />
              </div>
              <div style={{ width: 70, fontSize: 11, textAlign: "right", color: "var(--text-muted)", flex: "none", fontVariantNumeric: "tabular-nums" }}>
                {formatDuration(durationNs)}
              </div>
              {hoverId === n.SpanID && (
                <div
                  className="viz-tooltip"
                  style={{ left: 200, top: rowH, position: "absolute", zIndex: 30 }}
                >
                  <div className="t-row">
                    <span className="t-key">service</span>
                    <span className="t-val">{n.Service}</span>
                  </div>
                  <div className="t-row">
                    <span className="t-key">duration</span>
                    <span className="t-val">{formatDuration(durationNs)}</span>
                  </div>
                  {n.Status && (
                    <div className="t-row">
                      <span className="t-key">status</span>
                      <span className="t-val">{n.Status}</span>
                    </div>
                  )}
                </div>
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
