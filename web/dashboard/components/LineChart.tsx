"use client";

import { useMemo, useRef, useState } from "react";
import { colorForKey } from "@/lib/colors";
import { formatClock } from "@/lib/format";

export type LineSeries = {
  key: string;
  label: string;
  points: { t: number; v: number }[]; // t = unix seconds
};

const W = 900;
const H = 240;
const PAD_L = 44;
const PAD_R = 12;
const PAD_T = 14;
const PAD_B = 28;

function niceTicks(min: number, max: number, count = 4): number[] {
  if (min === max) {
    min -= 1;
    max += 1;
  }
  const span = max - min;
  const step = Math.pow(10, Math.floor(Math.log10(span / count)));
  const err = (span / count) / step;
  const mult = err >= 7.5 ? 10 : err >= 3 ? 5 : err >= 1.5 ? 2 : 1;
  const niceStep = step * mult;
  const start = Math.ceil(min / niceStep) * niceStep;
  const ticks: number[] = [];
  for (let v = start; v <= max + 1e-9; v += niceStep) ticks.push(Math.round(v * 1000) / 1000);
  return ticks.length > 0 ? ticks : [min, max];
}

// Single-axis multi-series line chart with hairline gridlines, a crosshair +
// tooltip on hover, and a legend (only rendered for >=2 series, per
// marks-and-anatomy - one series already names itself via the card title).
export function LineChart({
  series,
  valueFormatter = (v: number) => v.toFixed(2),
  emptyLabel = "No data points in range.",
}: {
  series: LineSeries[];
  valueFormatter?: (v: number) => string;
  emptyLabel?: string;
}) {
  const ref = useRef<SVGSVGElement>(null);
  const [hoverX, setHoverX] = useState<number | null>(null);

  const nonEmpty = series.filter((s) => s.points.length > 0);

  const { minT, maxT, minV, maxV } = useMemo(() => {
    let minT = Infinity;
    let maxT = -Infinity;
    let minV = Infinity;
    let maxV = -Infinity;
    for (const s of nonEmpty) {
      for (const p of s.points) {
        if (p.t < minT) minT = p.t;
        if (p.t > maxT) maxT = p.t;
        if (p.v < minV) minV = p.v;
        if (p.v > maxV) maxV = p.v;
      }
    }
    if (!Number.isFinite(minV)) {
      minV = 0;
      maxV = 1;
    }
    if (minV > 0) minV = 0; // anchor area/line charts to a real zero baseline when values are non-negative
    if (minV === maxV) maxV = minV + 1;
    return { minT, maxT, minV, maxV };
  }, [nonEmpty]);

  const x = (t: number) => PAD_L + ((t - minT) / (maxT - minT || 1)) * (W - PAD_L - PAD_R);
  const y = (v: number) => H - PAD_B - ((v - minV) / (maxV - minV || 1)) * (H - PAD_T - PAD_B);

  const yTicks = useMemo(() => niceTicks(minV, maxV, 4), [minV, maxV]);

  if (nonEmpty.length === 0) {
    return <div className="empty-state">{emptyLabel}</div>;
  }

  const showLegend = nonEmpty.length >= 2;

  function handleMove(e: React.MouseEvent<SVGSVGElement>) {
    const rect = ref.current?.getBoundingClientRect();
    if (!rect) return;
    const relX = ((e.clientX - rect.left) / rect.width) * W;
    setHoverX(Math.max(PAD_L, Math.min(W - PAD_R, relX)));
  }

  const hoverT = hoverX != null ? minT + ((hoverX - PAD_L) / (W - PAD_L - PAD_R)) * (maxT - minT) : null;

  const hoverRows =
    hoverT != null
      ? nonEmpty.map((s) => {
          let nearest = s.points[0];
          let best = Infinity;
          for (const p of s.points) {
            const d = Math.abs(p.t - hoverT);
            if (d < best) {
              best = d;
              nearest = p;
            }
          }
          return { key: s.key, label: s.label, point: nearest };
        })
      : [];

  return (
    <div style={{ position: "relative" }}>
      <svg
        ref={ref}
        viewBox={`0 0 ${W} ${H}`}
        width="100%"
        height={H}
        onMouseMove={handleMove}
        onMouseLeave={() => setHoverX(null)}
        style={{ display: "block", overflow: "visible" }}
      >
        {yTicks.map((t) => (
          <g key={t}>
            <line x1={PAD_L} x2={W - PAD_R} y1={y(t)} y2={y(t)} stroke="var(--surface-3)" strokeWidth={1} />
            <text x={PAD_L - 8} y={y(t)} dy={4} textAnchor="end" fontSize={10} fill="var(--text-muted)">
              {compactAxis(t)}
            </text>
          </g>
        ))}
        <line x1={PAD_L} x2={W - PAD_R} y1={H - PAD_B} y2={H - PAD_B} stroke="var(--axis)" strokeWidth={1} />

        {[minT, (minT + maxT) / 2, maxT].map((t, i) => (
          <text
            key={i}
            x={x(t)}
            y={H - PAD_B + 16}
            textAnchor={i === 0 ? "start" : i === 2 ? "end" : "middle"}
            fontSize={10}
            fill="var(--text-muted)"
          >
            {formatClock(t)}
          </text>
        ))}

        {nonEmpty.map((s) => {
          const color = colorForKey(s.key);
          const sorted = [...s.points].sort((a, b) => a.t - b.t);
          const linePath = sorted.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.t).toFixed(1)},${y(p.v).toFixed(1)}`).join(" ");
          const areaPath = `${linePath} L${x(sorted[sorted.length - 1].t).toFixed(1)},${y(minV).toFixed(1)} L${x(
            sorted[0].t,
          ).toFixed(1)},${y(minV).toFixed(1)} Z`;
          return (
            <g key={s.key}>
              <path d={areaPath} fill={color} opacity={0.1} stroke="none" />
              <path d={linePath} fill="none" stroke={color} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />
            </g>
          );
        })}

        {hoverX != null && (
          <line x1={hoverX} x2={hoverX} y1={PAD_T} y2={H - PAD_B} stroke="var(--text-muted)" strokeWidth={1} strokeDasharray="3 3" />
        )}
        {hoverX != null &&
          hoverRows.map((r) => (
            <circle
              key={r.key}
              cx={x(r.point.t)}
              cy={y(r.point.v)}
              r={4}
              fill={colorForKey(r.key)}
              stroke="var(--surface-1)"
              strokeWidth={2}
            />
          ))}
      </svg>

      {hoverX != null && hoverT != null && (
        <div
          className="viz-tooltip"
          style={{
            left: `min(${(hoverX / W) * 100}%, calc(100% - 180px))`,
            top: 4,
          }}
        >
          <div className="t-time">{formatClock(hoverT)}</div>
          {hoverRows.map((r) => (
            <div className="t-row" key={r.key}>
              <span className="t-key">
                <span className="legend-swatch" style={{ background: colorForKey(r.key) }} />
                {r.label}
              </span>
              <span className="t-val">{valueFormatter(r.point.v)}</span>
            </div>
          ))}
        </div>
      )}

      {showLegend && (
        <div className="legend">
          {nonEmpty.map((s) => (
            <div className="legend-item" key={s.key}>
              <span className="legend-swatch" style={{ background: colorForKey(s.key) }} />
              {s.label}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function compactAxis(v: number): string {
  const abs = Math.abs(v);
  if (abs >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (abs >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return Number.isInteger(v) ? String(v) : v.toFixed(1);
}
