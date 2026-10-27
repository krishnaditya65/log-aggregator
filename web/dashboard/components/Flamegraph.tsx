"use client";

import { useState } from "react";
import type { FlameNode } from "@/lib/api";
import { colorForKey } from "@/lib/colors";
import { formatDuration } from "@/lib/format";

// Icicle-style flamegraph: each row is a call-stack depth, width
// proportional to TotalNS relative to the root. Click a frame to zoom
// (make it the new 100%-width root) - standard flamegraph interaction.
export function Flamegraph({ root }: { root: FlameNode }) {
  const [zoomed, setZoomed] = useState<FlameNode>(root);
  const [hover, setHover] = useState<{ node: FlameNode; x: number; y: number } | null>(null);

  const rows: { node: FlameNode; depth: number; offset: number; width: number }[] = [];
  const walk = (node: FlameNode, depth: number, offset: number, scale: number) => {
    const width = (node.TotalNS / (zoomed.TotalNS || 1)) * scale;
    rows.push({ node, depth, offset, width });
    let childOffset = offset;
    for (const child of node.Children ?? []) {
      const childWidth = (child.TotalNS / (zoomed.TotalNS || 1)) * scale;
      walk(child, depth + 1, childOffset, scale);
      childOffset += childWidth;
    }
  };
  walk(zoomed, 0, 0, 100);

  const rowH = 24;
  const maxDepth = Math.max(...rows.map((r) => r.depth), 0);

  return (
    <div>
      {zoomed !== root && (
        <button className="btn" style={{ marginBottom: 10 }} onClick={() => setZoomed(root)}>
          ← Reset zoom
        </button>
      )}
      <div style={{ position: "relative", height: (maxDepth + 1) * (rowH + 2), fontSize: 11 }}>
        {rows.map((r, i) => {
          if (r.width < 0.15) return null;
          return (
            <div
              key={`${r.depth}-${i}-${r.node.Name}`}
              onClick={() => setZoomed(r.node)}
              onMouseEnter={(e) => setHover({ node: r.node, x: e.clientX, y: e.clientY })}
              onMouseMove={(e) => setHover({ node: r.node, x: e.clientX, y: e.clientY })}
              onMouseLeave={() => setHover(null)}
              title={r.node.Name}
              style={{
                position: "absolute",
                left: `${r.offset}%`,
                top: r.depth * (rowH + 2),
                width: `${r.width}%`,
                height: rowH,
                background: colorForKey(r.node.Name),
                borderRadius: 3,
                cursor: "pointer",
                overflow: "hidden",
                display: "flex",
                alignItems: "center",
                paddingLeft: 6,
                color: "#fff",
                fontWeight: 600,
                whiteSpace: "nowrap",
                boxShadow: "inset 0 0 0 1px var(--surface-1)",
              }}
            >
              {r.width > 4 ? r.node.Name : ""}
            </div>
          );
        })}
      </div>
      {hover && (
        <div
          className="viz-tooltip"
          style={{ position: "fixed", left: hover.x + 14, top: hover.y + 14 }}
        >
          <div className="t-time">{hover.node.Name}</div>
          <div className="t-row">
            <span className="t-key">self</span>
            <span className="t-val">{formatDuration(hover.node.SelfNS)}</span>
          </div>
          <div className="t-row">
            <span className="t-key">total</span>
            <span className="t-val">{formatDuration(hover.node.TotalNS)}</span>
          </div>
        </div>
      )}
    </div>
  );
}
