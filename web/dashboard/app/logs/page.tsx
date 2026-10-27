"use client";

import { useEffect, useMemo, useState } from "react";
import { v1, v2, type LogEntry, type PipeqlResult } from "@/lib/api";
import { formatTime } from "@/lib/format";
import { PageHead, Card, CardHead, QueryBar, EmptyState, ErrorBanner, SeverityBadge, DataTable, Skeleton } from "@/components/ui";
import { BarChart } from "@/components/BarChart";

const PIPEQL_EXAMPLES = [
  'search "error" | stats count() by (service)',
  'search level="error" | sort desc | limit 20',
  'search service="checkout" "timeout" | filter level="error" | stats count() by (host)',
  'search * | stats p95(duration_ms) by (service)',
];

export default function LogsPage() {
  const [mode, setMode] = useState<"quick" | "pipeline">("pipeline");

  return (
    <>
      <PageHead
        title="Logs"
        desc="Quick keyword search over recent logs (v1), or a pipelined pipeql query — search | filter | stats | sort | limit — over the buffered storage engine (v2)."
        action={
          <div className="theme-toggle">
            <button className={mode === "pipeline" ? "active" : ""} onClick={() => setMode("pipeline")} style={{ width: "auto", padding: "0 12px", fontSize: 12, fontWeight: 600 }}>
              Pipeline (v2)
            </button>
            <button className={mode === "quick" ? "active" : ""} onClick={() => setMode("quick")} style={{ width: "auto", padding: "0 12px", fontSize: 12, fontWeight: 600 }}>
              Quick search (v1)
            </button>
          </div>
        }
      />
      {mode === "pipeline" ? <PipelinePanel /> : <QuickPanel />}
    </>
  );
}

function QuickPanel() {
  const [query, setQuery] = useState("error");
  const [tag, setTag] = useState("");
  const [results, setResults] = useState<LogEntry[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  function run() {
    setLoading(true);
    setError("");
    v1
      .searchLogs(query, 100, tag || undefined)
      .then(setResults)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(run, []);

  return (
    <Card>
      <CardHead title="Search" eyebrow="GET /v1/logs?q=&tag=&limit=" />
      <div className="query-row">
        <input className="input" value={query} onChange={(e) => setQuery(e.target.value)} placeholder="keyword" onKeyDown={(e) => e.key === "Enter" && run()} />
        <input
          className="input"
          value={tag}
          onChange={(e) => setTag(e.target.value)}
          placeholder="tag filter, e.g. env=prod"
          style={{ maxWidth: 220 }}
          onKeyDown={(e) => e.key === "Enter" && run()}
        />
        <button className="btn btn-primary" onClick={run} disabled={loading}>
          {loading ? "Searching…" : "Search"}
        </button>
      </div>
      <ErrorBanner message={error} />
      <div style={{ marginTop: 16 }}>
        {loading ? (
          <Skeleton height={200} />
        ) : results && results.length > 0 ? (
          <div style={{ display: "grid", gap: 8 }}>
            {results.map((log, idx) => (
              <LogRow key={log.id ?? `${log.time}-${idx}`} time={log.time} service={log.service} level={log.level} message={log.message} host={log.host} />
            ))}
          </div>
        ) : (
          <EmptyState title="No matching logs" hint="Try a broader keyword, or POST to /v1/ingest/logs first." />
        )}
      </div>
    </Card>
  );
}

function PipelinePanel() {
  const [pipeline, setPipeline] = useState(PIPEQL_EXAMPLES[0]);
  const [result, setResult] = useState<PipeqlResult | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  function run() {
    setLoading(true);
    setError("");
    v2
      .searchLogs(pipeline)
      .then(setResult)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(run, []);

  const statsChartData = useMemo(() => {
    const fields = result?.StatsFields;
    const rows = result?.StatsRows;
    if (!result?.IsStats || !fields || !rows || fields.length < 2) return null;
    const valueField = fields[fields.length - 1];
    const groupField = fields.find((f) => f !== valueField) ?? fields[0];
    return rows.map((row) => ({
      key: String(row[groupField]),
      label: String(row[groupField]),
      value: Number(row[valueField]) || 0,
    }));
  }, [result]);

  return (
    <>
      <Card>
        <CardHead title="Pipeline query" eyebrow="pipeql · GET /v2/logs/search" />
        <QueryBar value={pipeline} onChange={setPipeline} onRun={run} examples={PIPEQL_EXAMPLES} running={loading} placeholder='search level="error" | stats count() by (service)' />
        <ErrorBanner message={error} />
      </Card>

      {loading ? (
        <Card className="section">
          <Skeleton height={220} />
        </Card>
      ) : result?.IsStats ? (
        <>
          {statsChartData && statsChartData.length > 0 && (
            <Card className="section">
              <CardHead title="Grouped result" eyebrow={result.StatsFields?.join(", ")} />
              <BarChart data={statsChartData} />
            </Card>
          )}
          <Card className="section">
            <CardHead title="Stats table" eyebrow={`${result.StatsRows?.length ?? 0} rows`} />
            {result.StatsRows && result.StatsRows.length > 0 && result.StatsFields ? (
              <DataTable
                columns={result.StatsFields.map((f) => ({ key: f, label: f, mono: true }))}
                rows={result.StatsRows}
                rowKey={(_row, i) => String(i)}
              />
            ) : (
              <EmptyState title="No rows" />
            )}
          </Card>
        </>
      ) : result ? (
        <Card className="section">
          <CardHead title="Records" eyebrow={`${result.Records.length} matched`} />
          {result.Records.length > 0 ? (
            <div style={{ display: "grid", gap: 8 }}>
              {result.Records.map((r, i) => (
                <LogRow key={i} time={r.Time} service={r.Service} level={r.Level} message={r.Message} host={r.Host} />
              ))}
            </div>
          ) : (
            <EmptyState title="No matching records" />
          )}
        </Card>
      ) : null}
    </>
  );
}

function LogRow({
  time,
  service,
  level,
  message,
  host,
}: {
  time: string | number;
  service: string;
  level?: string;
  message: string;
  host?: string;
}) {
  return (
    <div className="card card-pad" style={{ padding: 12 }}>
      <div style={{ display: "flex", gap: 10, alignItems: "center", marginBottom: 4, fontSize: 11.5 }}>
        <SeverityBadge level={level} />
        <span className="text-muted mono">{formatTime(time)}</span>
        <span className="text-secondary" style={{ fontWeight: 600 }}>
          {service}
        </span>
        {host && <span className="text-muted">@ {host}</span>}
      </div>
      <pre style={{ whiteSpace: "pre-wrap", margin: 0, fontSize: 12.5, color: "var(--text-primary)" }}>{message}</pre>
    </div>
  );
}
