"use client";

import Link from "next/link";
import { useMemo } from "react";
import { v1, v2 } from "@/lib/api";
import { useApi } from "@/lib/useApi";
import { compactNumber, formatTime } from "@/lib/format";
import { PageHead, Card, CardHead, StatTile, Skeleton, EmptyState, SeverityBadge, ErrorBanner } from "@/components/ui";
import { BarChart } from "@/components/BarChart";
import { LineChart } from "@/components/LineChart";

export default function OverviewPage() {
  const stats = useApi(() => v1.stats(), []);
  const v2stats = useApi(() => v2.stats(), []);
  const logs = useApi(() => v1.searchLogs("", 100), []);
  const latency = useApi(() => v1.queryMetrics("request_latency_ms"), []);

  const v2Unavailable = v2stats.error != null;

  const levelCounts = useMemo(() => {
    if (!logs.data) return [];
    const counts = new Map<string, number>();
    for (const l of logs.data) {
      const level = (l.level || "info").toLowerCase();
      counts.set(level, (counts.get(level) ?? 0) + 1);
    }
    return [...counts.entries()].map(([key, value]) => ({ key, label: key, value })).sort((a, b) => b.value - a.value);
  }, [logs.data]);

  const serviceCounts = useMemo(() => {
    if (!logs.data) return [];
    const counts = new Map<string, number>();
    for (const l of logs.data) counts.set(l.service, (counts.get(l.service) ?? 0) + 1);
    return [...counts.entries()].map(([key, value]) => ({ key, label: key, value }));
  }, [logs.data]);

  const latencySeries = useMemo(() => {
    if (!latency.data || latency.data.length === 0) return [];
    const points = latency.data
      .map((m) => ({ t: Math.floor(new Date(m.time).getTime() / 1000), v: m.value }))
      .sort((a, b) => a.t - b.t);
    return [{ key: "request_latency_ms", label: "request_latency_ms", points }];
  }, [latency.data]);

  return (
    <>
      <PageHead
        title="Overview"
        desc="A live snapshot of ingestion volume, storage engines, and recent activity across the stack."
      />

      <div className="grid grid-4">
        <StatTile label="Logs stored" value={stats.data ? compactNumber(stats.data.log_count) : "—"} loading={stats.loading} foot="internal/storage (v1)" />
        <StatTile label="Metric points stored" value={stats.data ? compactNumber(stats.data.metric_count) : "—"} loading={stats.loading} foot="internal/storage (v1)" />
        <StatTile
          label="TSDB decoded blocks"
          value={v2stats.data ? compactNumber(v2stats.data.tsdb_decoded_blocks) : "—"}
          loading={v2stats.loading}
          foot={v2Unavailable ? "v2 stack not enabled" : "internal/tsdb"}
        />
        <StatTile
          label="Vector index size"
          value={v2stats.data ? compactNumber(v2stats.data.vector_index_size) : "—"}
          loading={v2stats.loading}
          foot={v2Unavailable ? "v2 stack not enabled" : "internal/vectorsearch (HNSW)"}
        />
      </div>

      {v2Unavailable && (
        <div className="error-banner" style={{ marginTop: 14 }}>
          The v2 API surface ({"/v2/*"}) isn&apos;t responding — the server may be running without <span className="mono">Platform</span> configured
          (basic mode). Advanced pages (Metrics/Traces/Alerts v2/AIOps/Profiling/Archive) need it. See docs/ARCHITECTURE.md.
        </div>
      )}

      <div className="grid grid-2 section">
        <Card>
          <CardHead title="Request latency" eyebrow="metric · request_latency_ms" action={<Link href="/metrics" className="btn">Open Metrics →</Link>} />
          {latency.loading ? (
            <Skeleton height={200} />
          ) : latencySeries.length > 0 ? (
            <LineChart series={latencySeries} valueFormatter={(v) => `${v.toFixed(1)}ms`} />
          ) : (
            <EmptyState title="No request_latency_ms points yet" hint="POST to /v1/ingest/metrics or /v2/metrics to populate this." />
          )}
        </Card>

        <Card>
          <CardHead title="Recent log levels" eyebrow={`last ${logs.data?.length ?? 0} logs`} action={<Link href="/logs" className="btn">Open Logs →</Link>} />
          {logs.loading ? (
            <Skeleton height={200} />
          ) : levelCounts.length > 0 ? (
            <BarChart data={levelCounts} valueFormatter={(v) => String(v)} />
          ) : (
            <EmptyState title="No logs yet" hint="POST to /v1/ingest/logs or /v2/logs to populate this." />
          )}
        </Card>
      </div>

      <div className="grid grid-2 section">
        <Card>
          <CardHead title="Logs by service" eyebrow="recent sample" />
          {logs.loading ? (
            <Skeleton height={160} />
          ) : serviceCounts.length > 0 ? (
            <BarChart data={serviceCounts} valueFormatter={(v) => String(v)} />
          ) : (
            <EmptyState title="No services observed yet" />
          )}
        </Card>

        <Card>
          <CardHead title="Storage & pipeline counters" eyebrow="v2 stack" />
          {v2Unavailable ? (
            <EmptyState title="v2 stack not enabled" hint="Set Platform on the server config to see tracing/vectorsearch/logsdb counters." />
          ) : v2stats.loading ? (
            <Skeleton height={160} />
          ) : (
            <div className="grid grid-2" style={{ gap: 10 }}>
              <MiniStat label="Pending traces" value={v2stats.data ? compactNumber(v2stats.data.tracing_pending_traces) : "—"} />
              <MiniStat label="Vector index size" value={v2stats.data ? compactNumber(v2stats.data.vector_index_size) : "—"} />
              <MiniStat label="TSDB decoded blocks" value={v2stats.data ? compactNumber(v2stats.data.tsdb_decoded_blocks) : "—"} />
              <MiniStat label="LogsDB segments" value={formatSegmentStat(v2stats.data?.logsdb_segment_stats)} />
            </div>
          )}
        </Card>
      </div>

      <Card className="section">
        <CardHead title="Recent logs" eyebrow="v1 · GET /v1/logs" action={<Link href="/logs" className="btn">Explore in Logs →</Link>} />
        {logs.error ? (
          <ErrorBanner message={logs.error} />
        ) : logs.loading ? (
          <Skeleton height={220} />
        ) : logs.data && logs.data.length > 0 ? (
          <div style={{ display: "grid", gap: 8, maxHeight: 360, overflowY: "auto" }}>
            {logs.data.slice(0, 15).map((log, idx) => (
              <div key={log.id ?? `${log.time}-${idx}`} style={{ display: "flex", gap: 10, alignItems: "baseline", fontSize: 12.5 }}>
                <SeverityBadge level={log.level} />
                <span className="text-muted mono" style={{ flex: "none", width: 150 }}>
                  {formatTime(log.time)}
                </span>
                <span className="text-secondary" style={{ flex: "none", width: 110, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  {log.service}
                </span>
                <span style={{ overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{log.message}</span>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState title="No logs yet" hint="POST to /v1/ingest/logs to see results here." />
        )}
      </Card>
    </>
  );
}

function MiniStat({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ background: "var(--surface-2)", borderRadius: 10, padding: "12px 14px" }}>
      <div className="stat-label">{label}</div>
      <div style={{ fontSize: 18, fontWeight: 700, marginTop: 4 }}>{value}</div>
    </div>
  );
}

function formatSegmentStat(v: unknown): string {
  if (v == null) return "—";
  if (typeof v === "object") {
    const entries = Object.entries(v as Record<string, unknown>);
    if (entries.length === 0) return "0";
    return entries.map(([k, val]) => `${k}: ${String(val)}`).join(", ");
  }
  return String(v);
}
