"use client";

import { useEffect, useState } from "react";
import { v2, type SemanticResult } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState } from "@/components/ui";

const EXAMPLES = ["database connection timeout", "payment failed after retry", "out of memory killed the process", "slow response from downstream service"];

export default function SearchPage() {
  const [q, setQ] = useState(EXAMPLES[0]);
  const [k, setK] = useState(10);
  const [results, setResults] = useState<SemanticResult[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  function run() {
    setLoading(true);
    setError("");
    v2
      .semanticSearch(q, k)
      .then((rs) => setResults(rs ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(run, []);

  const maxScore = Math.max(0.0001, ...results.map((r) => r.score));

  return (
    <>
      <PageHead
        title="Semantic search"
        desc="Natural-language search over indexed log messages via a from-scratch HNSW index + local hashing/TF-IDF embedding — matches don't need to share exact keywords."
      />

      <Card>
        <CardHead title="Query" eyebrow="GET /v2/logs/semantic?q=&k=" />
        <div className="query-row">
          <input className="input" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Describe what you're looking for…" onKeyDown={(e) => e.key === "Enter" && run()} />
          <input
            className="input"
            type="number"
            min={1}
            max={50}
            value={k}
            onChange={(e) => setK(Number(e.target.value) || 10)}
            style={{ maxWidth: 90 }}
            title="Number of results (k)"
          />
          <button className="btn btn-primary" onClick={run} disabled={loading}>
            {loading ? "Searching…" : "Search"}
          </button>
        </div>
        <div className="chip-row">
          {EXAMPLES.map((ex) => (
            <button key={ex} className="chip" onClick={() => setQ(ex)}>
              {ex}
            </button>
          ))}
        </div>
        <ErrorBanner message={error} />
      </Card>

      <Card className="section">
        <CardHead title="Results" eyebrow={`${results.length} matches`} />
        {loading ? (
          <div className="skeleton" style={{ height: 200 }} />
        ) : results.length > 0 ? (
          <div style={{ display: "grid", gap: 10 }}>
            {results.map((r) => (
              <div key={r.id} className="card card-pad" style={{ padding: 14 }}>
                <div style={{ display: "flex", alignItems: "center", gap: 10, marginBottom: 8 }}>
                  <div style={{ flex: 1, height: 6, background: "var(--surface-2)", borderRadius: 3 }}>
                    <div
                      style={{
                        width: `${Math.max(4, (r.score / maxScore) * 100)}%`,
                        height: "100%",
                        borderRadius: 3,
                        background: "var(--series-1)",
                      }}
                    />
                  </div>
                  <span className="mono text-muted" style={{ fontSize: 11, flex: "none" }}>
                    score {r.score.toFixed(3)}
                  </span>
                </div>
                <div style={{ fontSize: 13 }}>{r.text}</div>
              </div>
            ))}
          </div>
        ) : (
          <EmptyState title="No matches" hint="Ingest some logs first, or try a broader description." />
        )}
      </Card>
    </>
  );
}
