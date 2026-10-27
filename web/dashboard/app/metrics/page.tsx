"use client";

import { useEffect, useMemo, useState } from "react";
import { v2, type MetricSeries } from "@/lib/api";
import { PageHead, Card, CardHead, QueryBar, ErrorBanner, EmptyState } from "@/components/ui";
import { LineChart, type LineSeries } from "@/components/LineChart";
import { colorForKey } from "@/lib/colors";

const EXAMPLES = [
  'latency_ms{service="checkout"}',
  'sum(rate(http_errors_total[5m])) by (service)',
  "percentile(95, latency_ms[5m])",
  'avg_over_time(cpu_pct{service="api"}[5m])',
];

function seriesLabel(labels: Record<string, string>): string {
  const entries = Object.entries(labels);
  if (entries.length === 0) return "series";
  return entries.map(([k, v]) => `${k}="${v}"`).join(", ");
}

export default function MetricsPage() {
  const [query, setQuery] = useState(EXAMPLES[0]);
  const [series, setSeries] = useState<MetricSeries[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  function run() {
    setLoading(true);
    setError("");
    v2
      .queryMetrics(query)
      .then((s) => setSeries(s ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(run, []);

  const chartSeries: LineSeries[] = useMemo(
    () =>
      series.map((s, i) => ({
        key: seriesLabel(s.Labels) || `series-${i}`,
        label: seriesLabel(s.Labels) || `series ${i + 1}`,
        points: s.Points.map((p) => ({ t: p.T, v: p.V })),
      })),
    [series],
  );

  const latestValues = useMemo(
    () =>
      chartSeries.map((s) => ({
        key: s.key,
        label: s.label,
        value: s.points.length > 0 ? s.points[s.points.length - 1].v : null,
      })),
    [chartSeries],
  );

  return (
    <>
      <PageHead title="Metrics" desc="A PromQL-like query language (metricsql) over the embedded time-series engine — selectors, aggregations, rate/percentile/quantile, and arithmetic." />

      <Card>
        <CardHead title="Query" eyebrow="metricsql · GET /v2/metrics/query" />
        <QueryBar value={query} onChange={setQuery} onRun={run} examples={EXAMPLES} running={loading} placeholder='latency_ms{service="checkout"}' />
        <ErrorBanner message={error} />
      </Card>

      {latestValues.length > 0 && (
        <div className={`grid ${latestValues.length >= 4 ? "grid-4" : latestValues.length === 3 ? "grid-3" : "grid-2"} section`}>
          {latestValues.slice(0, 4).map((s) => (
            <div key={s.key} className="card card-pad stat-tile">
              <div className="stat-label" style={{ display: "flex", alignItems: "center", gap: 6, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                <span className="legend-swatch" style={{ background: colorForKey(s.key) }} />
                <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{s.label}</span>
              </div>
              <div className="stat-value">{s.value != null ? s.value.toFixed(2) : "—"}</div>
              <div className="stat-foot">latest point</div>
            </div>
          ))}
        </div>
      )}

      <Card className="section">
        <CardHead title="Series" eyebrow={`${series.length} series returned`} />
        {loading ? (
          <div className="skeleton" style={{ height: 240 }} />
        ) : chartSeries.length > 0 ? (
          <LineChart series={chartSeries} valueFormatter={(v) => v.toFixed(2)} />
        ) : (
          <EmptyState title="No series returned" hint="Try one of the example queries above, or ingest metrics first via /v2/metrics." />
        )}
      </Card>

      <Card className="section">
        <CardHead title="Grammar reference" eyebrow="internal/querylang/metricsql" />
        <pre className="mono" style={{ margin: 0, fontSize: 12, color: "var(--text-secondary)", whiteSpace: "pre-wrap" }}>
{`metric{label="val", ...}[5m]                          range selector
sum|avg|min|max|count|quantile(op)(expr) by (labels)  aggregation
rate(expr[5m]) | irate(expr[5m])                       per-second increase (counter-reset-aware)
avg_over_time|max_over_time|min_over_time|sum_over_time(expr[5m])
percentile(95, expr[5m])                               interpolated, 0-100 scale, time-window
quantile(0.95, expr)                                   cross-series aggregation, 0-1 scale
expr + expr | expr - expr | expr * expr | expr / expr
expr > N | expr < N | expr >= N | expr <= N            vector comparison filters (alerting)`}
        </pre>
      </Card>
    </>
  );
}
