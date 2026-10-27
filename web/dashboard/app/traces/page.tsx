"use client";

import { useEffect, useMemo, useState } from "react";
import { v1, v2, type Span } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState, DataTable } from "@/components/ui";
import { Waterfall } from "@/components/Waterfall";
import { formatDuration } from "@/lib/format";

export default function TracesPage() {
  const [traceId, setTraceId] = useState("");
  const [spans, setSpans] = useState<Span[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [recentIds, setRecentIds] = useState<string[]>([]);

  // Discover candidate trace IDs from recent logs (LogEntry.trace_id) since
  // there is no "list traces" endpoint - only GET /v2/traces/{id}.
  useEffect(() => {
    v1
      .searchLogs("", 200)
      .then((logs) => {
        const ids = [...new Set(logs.map((l) => l.trace_id).filter((id): id is string => !!id))];
        setRecentIds(ids.slice(0, 12));
      })
      .catch(() => setRecentIds([]));
  }, []);

  function lookup(id: string) {
    if (!id) return;
    setLoading(true);
    setError("");
    setSpans(null);
    v2
      .getTrace(id)
      .then(setSpans)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  const rootSpan = useMemo(() => spans?.find((s) => !s.ParentSpanID), [spans]);
  const totalDuration = useMemo(() => {
    if (!spans || spans.length === 0) return 0;
    const start = Math.min(...spans.map((s) => s.StartTime));
    const end = Math.max(...spans.map((s) => s.EndTime));
    return end - start;
  }, [spans]);
  const services = useMemo(() => (spans ? [...new Set(spans.map((s) => s.Service))] : []), [spans]);
  const errorCount = useMemo(() => (spans ? spans.filter((s) => (s.Status ?? "").toLowerCase().includes("error")).length : 0), [spans]);

  return (
    <>
      <PageHead title="Traces" desc="Tail-sampled distributed traces — error/slow traces always kept, healthy traces deterministically sampled by trace-ID hash." />

      <Card>
        <CardHead title="Trace lookup" eyebrow="GET /v2/traces/{id}" />
        <div className="query-row">
          <input
            className="input input-mono"
            value={traceId}
            onChange={(e) => setTraceId(e.target.value)}
            placeholder="trace ID"
            onKeyDown={(e) => e.key === "Enter" && lookup(traceId)}
          />
          <button className="btn btn-primary" onClick={() => lookup(traceId)} disabled={loading || !traceId}>
            {loading ? "Loading…" : "Fetch trace"}
          </button>
        </div>
        {recentIds.length > 0 && (
          <div className="chip-row">
            {recentIds.map((id) => (
              <button
                key={id}
                className="chip"
                onClick={() => {
                  setTraceId(id);
                  lookup(id);
                }}
                title="From a recent log's trace_id"
              >
                {id.slice(0, 12)}…
              </button>
            ))}
          </div>
        )}
        <ErrorBanner message={error} />
      </Card>

      {spans && spans.length > 0 && (
        <div className="grid grid-4 section">
          <div className="card card-pad stat-tile">
            <div className="stat-label">Root span</div>
            <div className="stat-value" style={{ fontSize: 16 }}>
              {rootSpan?.Name ?? "—"}
            </div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Total duration</div>
            <div className="stat-value">{formatDuration(totalDuration)}</div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Services involved</div>
            <div className="stat-value">{services.length}</div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Error spans</div>
            <div className="stat-value" style={{ color: errorCount > 0 ? "var(--status-critical)" : undefined }}>
              {errorCount}
            </div>
          </div>
        </div>
      )}

      <Card className="section">
        <CardHead title="Waterfall" eyebrow={spans ? `${spans.length} spans` : undefined} />
        {loading ? (
          <div className="skeleton" style={{ height: 220 }} />
        ) : spans && spans.length > 0 ? (
          <Waterfall spans={spans} />
        ) : (
          <EmptyState title="No trace loaded" hint="Enter a trace ID above, or pick one of the recent trace IDs found in your logs." />
        )}
      </Card>

      {spans && spans.length > 0 && (
        <Card className="section">
          <CardHead title="Spans" eyebrow="raw list" />
          <DataTable
            columns={[
              { key: "Name", label: "Name" },
              { key: "Service", label: "Service" },
              { key: "SpanID", label: "Span ID", mono: true },
              { key: "ParentSpanID", label: "Parent", mono: true },
              { key: "duration", label: "Duration", render: (row) => formatDuration(row.EndTime - row.StartTime) },
              { key: "Status", label: "Status" },
            ]}
            rows={spans}
            rowKey={(s) => s.SpanID}
          />
        </Card>
      )}
    </>
  );
}
