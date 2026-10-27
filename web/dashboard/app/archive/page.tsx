"use client";

import { useState } from "react";
import { v2, type ArchiveRow } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState, DataTable } from "@/components/ui";

const DEFAULT_ROWS = JSON.stringify(
  [
    { service: "checkout", status: 200, latency_ms: 42, host: "web-1" },
    { service: "checkout", status: 500, latency_ms: 890, host: "web-2" },
    { service: "billing", status: 200, latency_ms: 65, host: "web-1" },
  ],
  null,
  2,
);

const OPS = ["=", "!=", ">", ">=", "<", "<="];

type Predicate = { column: string; op: string; value: string };

export default function ArchivePage() {
  const [rowsJson, setRowsJson] = useState(DEFAULT_ROWS);
  const [ingestError, setIngestError] = useState("");
  const [ingesting, setIngesting] = useState(false);
  const [archivedKey, setArchivedKey] = useState("");

  const [prefix, setPrefix] = useState("");
  const [predicates, setPredicates] = useState<Predicate[]>([{ column: "status", op: ">=", value: "500" }]);
  const [results, setResults] = useState<ArchiveRow[] | null>(null);
  const [queryError, setQueryError] = useState("");
  const [querying, setQuerying] = useState(false);

  function ingest() {
    setIngestError("");
    let rows: ArchiveRow[];
    try {
      rows = JSON.parse(rowsJson);
      if (!Array.isArray(rows)) throw new Error("expected a JSON array of row objects");
    } catch (e) {
      setIngestError(e instanceof Error ? e.message : String(e));
      return;
    }
    setIngesting(true);
    v2
      .archiveIngest(rows)
      .then((res) => {
        setArchivedKey(res.key);
        setPrefix(res.key.split("-").slice(0, 1).join("-"));
      })
      .catch((e) => setIngestError(e instanceof Error ? e.message : String(e)))
      .finally(() => setIngesting(false));
  }

  function query() {
    setQueryError("");
    setQuerying(true);
    const predicateStr = predicates
      .filter((p) => p.column && p.value !== "")
      .map((p) => `${p.column}:${p.op}:${p.value}`)
      .join(",");
    v2
      .archiveQuery(prefix, predicateStr)
      .then((rows) => setResults(rows ?? []))
      .catch((e) => setQueryError(e instanceof Error ? e.message : String(e)))
      .finally(() => setQuerying(false));
  }

  function updatePredicate(i: number, patch: Partial<Predicate>) {
    setPredicates((ps) => ps.map((p, idx) => (idx === i ? { ...p, ...patch } : p)));
  }

  const columns = results && results.length > 0 ? Object.keys(results[0]) : [];

  return (
    <>
      <PageHead
        title="Cold storage"
        desc="Parquet-like columnar archive with row-group min/max pushdown — ingest ad-hoc rows into a segment, then query with predicate pushdown to see which row groups get skipped."
      />

      <div className="grid grid-2">
        <Card>
          <CardHead title="Ingest a segment" eyebrow="POST /v2/archive/ingest" />
          <p className="text-muted" style={{ fontSize: 12.5, marginTop: -4 }}>
            Paste a JSON array of row objects (all rows should share roughly the same columns for a meaningful demo).
          </p>
          <textarea
            className="input input-mono"
            style={{ minHeight: 160, resize: "vertical" }}
            value={rowsJson}
            onChange={(e) => setRowsJson(e.target.value)}
          />
          <button className="btn btn-primary" style={{ marginTop: 10 }} onClick={ingest} disabled={ingesting}>
            {ingesting ? "Archiving…" : "Archive rows"}
          </button>
          <ErrorBanner message={ingestError} />
          {archivedKey && (
            <div className="badge badge-good" style={{ width: "fit-content", marginTop: 10 }}>
              <span className="badge-dot" />
              archived as {archivedKey}
            </div>
          )}
        </Card>

        <Card>
          <CardHead title="Query with pushdown" eyebrow="GET /v2/archive/query?prefix=&predicates=" />
          <div className="field">
            <label className="field-label">Key prefix</label>
            <input className="input input-mono" value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="adhoc-" />
          </div>
          <div style={{ marginTop: 10, display: "grid", gap: 6 }}>
            <label className="field-label">Predicates (AND)</label>
            {predicates.map((p, i) => (
              <div key={i} style={{ display: "flex", gap: 6 }}>
                <input className="input mono" value={p.column} onChange={(e) => updatePredicate(i, { column: e.target.value })} placeholder="column" style={{ flex: 1 }} />
                <select className="select" value={p.op} onChange={(e) => updatePredicate(i, { op: e.target.value })} style={{ maxWidth: 66 }}>
                  {OPS.map((op) => (
                    <option key={op}>{op}</option>
                  ))}
                </select>
                <input className="input mono" value={p.value} onChange={(e) => updatePredicate(i, { value: e.target.value })} placeholder="value" style={{ flex: 1 }} />
                <button className="btn" onClick={() => setPredicates((ps) => ps.filter((_, idx) => idx !== i))} title="Remove">
                  ✕
                </button>
              </div>
            ))}
            <button className="btn" onClick={() => setPredicates((ps) => [...ps, { column: "", op: "=", value: "" }])} style={{ width: "fit-content" }}>
              + Add predicate
            </button>
          </div>
          <button className="btn btn-primary" style={{ marginTop: 12 }} onClick={query} disabled={querying}>
            {querying ? "Querying…" : "Run query"}
          </button>
          <ErrorBanner message={queryError} />
        </Card>
      </div>

      <Card className="section">
        <CardHead title="Results" eyebrow={results ? `${results.length} rows (after row-group skip)` : undefined} />
        {results && results.length > 0 ? (
          <DataTable columns={columns.map((c) => ({ key: c, label: c, mono: true }))} rows={results} rowKey={(_r, i) => String(i)} />
        ) : results ? (
          <EmptyState title="No rows matched" hint="Every predicate is AND-ed together — try loosening one, or double check the key prefix." />
        ) : (
          <EmptyState title="No query run yet" hint="Archive some rows, then run a predicate query above." />
        )}
      </Card>
    </>
  );
}
