"use client";

import { useEffect, useState } from "react";
import { v2, type RootCauseHypothesis } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState } from "@/components/ui";
import { formatTime } from "@/lib/format";

const WINDOWS = ["5m", "10m", "30m", "1h"];

export default function AiopsPage() {
  return (
    <>
      <PageHead
        title="Root cause analysis"
        desc="Rule/statistics-based correlation across anomalies, deployments, and log spikes — not a trained model (no labeled incident corpus exists to train one against); see docs/ARCHITECTURE.md's honest capability matrix."
      />
      <div className="grid grid-2">
        <RootCausePanel />
        <DeploymentPanel />
      </div>
    </>
  );
}

function RootCausePanel() {
  const [window_, setWindow] = useState("10m");
  const [hyps, setHyps] = useState<RootCauseHypothesis[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  function run() {
    setLoading(true);
    setError("");
    v2
      .rootCause(window_)
      .then((hs) => setHyps(hs ?? []))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }

  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(run, []);

  return (
    <Card>
      <CardHead title="Hypotheses" eyebrow="GET /v2/aiops/rootcause?window=" />
      <div className="query-row">
        <select className="select" value={window_} onChange={(e) => setWindow(e.target.value)} style={{ maxWidth: 140 }}>
          {WINDOWS.map((w) => (
            <option key={w} value={w}>
              window: {w}
            </option>
          ))}
        </select>
        <button className="btn btn-primary" onClick={run} disabled={loading}>
          {loading ? "Analyzing…" : "Analyze recent anomalies"}
        </button>
      </div>
      <ErrorBanner message={error} />

      <div style={{ marginTop: 14 }}>
        {loading ? (
          <div className="skeleton" style={{ height: 200 }} />
        ) : hyps.length > 0 ? (
          <div style={{ display: "grid", gap: 10 }}>
            {hyps.map((h, i) => (
              <div key={i} className="card card-pad" style={{ padding: 14 }}>
                <div style={{ display: "flex", justifyContent: "space-between", alignItems: "baseline", gap: 8 }}>
                  <strong style={{ fontSize: 14 }}>{h.Service}</strong>
                  <span className="text-muted mono" style={{ fontSize: 11 }}>
                    {formatTime(h.Time)}
                  </span>
                </div>
                <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 8 }}>
                  <div style={{ flex: 1, height: 8, background: "var(--seq-100)", borderRadius: 4 }}>
                    <div
                      style={{
                        width: `${Math.round(h.Confidence * 100)}%`,
                        height: "100%",
                        borderRadius: 4,
                        background: "var(--series-1)",
                      }}
                    />
                  </div>
                  <span className="mono" style={{ fontSize: 12, fontWeight: 700, width: 44, textAlign: "right" }}>
                    {Math.round(h.Confidence * 100)}%
                  </span>
                </div>
                {h.Evidence?.length > 0 && (
                  <ul style={{ margin: "10px 0 0", paddingLeft: 18, fontSize: 12.5, color: "var(--text-secondary)" }}>
                    {h.Evidence.map((e, j) => (
                      <li key={j} style={{ marginBottom: 2 }}>
                        {e}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            ))}
          </div>
        ) : (
          <EmptyState title="No hypotheses" hint="Needs anomalies + deployments/log spikes within the selected window." />
        )}
      </div>
    </Card>
  );
}

function DeploymentPanel() {
  const [service, setService] = useState("checkout");
  const [version, setVersion] = useState("v1.4.2");
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [saving, setSaving] = useState(false);

  function record() {
    setSaving(true);
    setError("");
    setSuccess("");
    v2
      .recordDeployment({ service, version })
      .then(() => setSuccess(`Recorded deployment of ${service}@${version}`))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setSaving(false));
  }

  return (
    <Card>
      <CardHead title="Record a deployment" eyebrow="POST /v2/deployments — feeds alert deploy-correlation + AIOps" />
      <p className="text-muted" style={{ fontSize: 12.5, marginTop: -4 }}>
        Recording a deployment lets root-cause analysis correlate anomalies against &quot;did this service just ship?&quot; with a time-decayed confidence
        score.
      </p>
      <div className="grid" style={{ gap: 8 }}>
        <div className="field">
          <label className="field-label">Service</label>
          <input className="input" value={service} onChange={(e) => setService(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label">Version</label>
          <input className="input" value={version} onChange={(e) => setVersion(e.target.value)} />
        </div>
        <button className="btn btn-primary" onClick={record} disabled={saving || !service || !version}>
          {saving ? "Recording…" : "Record deployment"}
        </button>
      </div>
      <ErrorBanner message={error} />
      {success && (
        <div className="badge badge-good" style={{ width: "fit-content", marginTop: 10 }}>
          <span className="badge-dot" />
          {success}
        </div>
      )}
    </Card>
  );
}
