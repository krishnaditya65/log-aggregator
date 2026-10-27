"use client";

import { useEffect, useState } from "react";
import { v1, v2, type AlertRuleV1, type RuleView } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState, Badge } from "@/components/ui";
import { formatDuration } from "@/lib/format";

const SEVERITIES = ["info", "warning", "critical"];

function severityTone(sev: string): "good" | "warning" | "critical" | "neutral" {
  const s = sev.toLowerCase();
  if (s === "critical") return "critical";
  if (s === "warning" || s === "warn") return "warning";
  if (s === "info") return "good";
  return "neutral";
}

export default function AlertsPage() {
  return (
    <>
      <PageHead
        title="Alerts"
        desc="Dynamic metricsql-backed alert rules, maintenance silences, and deploy-correlated routing (v2) — plus the original static threshold alerts (v1)."
      />
      <div className="grid grid-2">
        <AlertRulesPanel />
        <SilencePanel />
      </div>
      <div className="section">
        <LegacyAlertsPanel />
      </div>
    </>
  );
}

function AlertRulesPanel() {
  const [rules, setRules] = useState<RuleView[]>([]);
  const [name, setName] = useState("high error rate");
  const [expr, setExpr] = useState('errors_total{service="checkout"} > 10');
  const [severity, setSeverity] = useState("warning");
  const [forSeconds, setForSeconds] = useState(0);
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);

  function load() {
    v2
      .listAlertRules()
      .then((rs) => setRules(rs ?? []))
      .catch(() => setRules([]));
  }

  useEffect(load, []);

  function create() {
    setCreating(true);
    setError("");
    v2
      .createAlertRule({ name, expr, severity, for_seconds: forSeconds })
      .then(() => {
        load();
        setName("");
        setExpr("");
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setCreating(false));
  }

  return (
    <Card>
      <CardHead title="Alert rules" eyebrow="v2 · metricsql-backed, anomaly detection, Slack/PagerDuty routing" />
      <div className="grid" style={{ gridTemplateColumns: "1fr", gap: 8 }}>
        <div className="field">
          <label className="field-label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label">metricsql expression</label>
          <input className="input input-mono" value={expr} onChange={(e) => setExpr(e.target.value)} />
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <div className="field" style={{ flex: 1 }}>
            <label className="field-label">Severity</label>
            <select className="select" value={severity} onChange={(e) => setSeverity(e.target.value)}>
              {SEVERITIES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </div>
          <div className="field" style={{ flex: 1 }}>
            <label className="field-label">For (seconds)</label>
            <input className="input" type="number" min={0} value={forSeconds} onChange={(e) => setForSeconds(Number(e.target.value) || 0)} />
          </div>
        </div>
        <button className="btn btn-primary" onClick={create} disabled={creating || !name || !expr}>
          {creating ? "Creating…" : "Create rule"}
        </button>
      </div>
      <ErrorBanner message={error} />

      <hr className="hairline" />

      {rules.length > 0 ? (
        <div style={{ display: "grid", gap: 8 }}>
          {rules.map((r) => (
            <div key={r.id} className="card card-pad" style={{ padding: 12 }}>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", gap: 8 }}>
                <strong style={{ fontSize: 13 }}>{r.name}</strong>
                <Badge tone={severityTone(r.severity)}>{r.severity}</Badge>
              </div>
              <pre className="mono" style={{ whiteSpace: "pre-wrap", margin: "6px 0 0", fontSize: 12, color: "var(--text-secondary)" }}>
                {r.expr}
              </pre>
              {r.for > 0 && <div className="text-muted" style={{ fontSize: 11, marginTop: 4 }}>for {formatDuration(r.for)}</div>}
            </div>
          ))}
        </div>
      ) : (
        <EmptyState title="No rules yet" />
      )}
    </Card>
  );
}

function SilencePanel() {
  const [matcherKey, setMatcherKey] = useState("service");
  const [matcherVal, setMatcherVal] = useState("checkout");
  const [comment, setComment] = useState("planned maintenance");
  const [durationMin, setDurationMin] = useState(60);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [creating, setCreating] = useState(false);

  function create() {
    setCreating(true);
    setError("");
    setSuccess("");
    const now = new Date();
    const ends = new Date(now.getTime() + durationMin * 60_000);
    v2
      .createSilence({
        Matchers: matcherKey ? { [matcherKey]: matcherVal } : {},
        StartsAt: now.toISOString(),
        EndsAt: ends.toISOString(),
        CreatedBy: "dashboard",
        Comment: comment,
      })
      .then(() => setSuccess(`Silence active until ${ends.toLocaleTimeString()}`))
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setCreating(false));
  }

  return (
    <Card>
      <CardHead title="Silences" eyebrow="v2 · POST /v2/alerts/silences — suppress matching alerts for a maintenance window" />
      <div style={{ display: "grid", gap: 8 }}>
        <div style={{ display: "flex", gap: 8 }}>
          <div className="field" style={{ flex: 1 }}>
            <label className="field-label">Matcher label</label>
            <input className="input" value={matcherKey} onChange={(e) => setMatcherKey(e.target.value)} />
          </div>
          <div className="field" style={{ flex: 1 }}>
            <label className="field-label">Matcher value</label>
            <input className="input" value={matcherVal} onChange={(e) => setMatcherVal(e.target.value)} />
          </div>
        </div>
        <div className="field">
          <label className="field-label">Comment</label>
          <input className="input" value={comment} onChange={(e) => setComment(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label">Duration (minutes)</label>
          <input className="input" type="number" min={1} value={durationMin} onChange={(e) => setDurationMin(Number(e.target.value) || 60)} />
        </div>
        <button className="btn btn-primary" onClick={create} disabled={creating}>
          {creating ? "Creating…" : "Create silence"}
        </button>
        <ErrorBanner message={error} />
        {success && (
          <div className="badge badge-good" style={{ width: "fit-content" }}>
            <span className="badge-dot" />
            {success}
          </div>
        )}
      </div>
    </Card>
  );
}

function LegacyAlertsPanel() {
  const [rules, setRules] = useState<AlertRuleV1[]>([]);
  const [name, setName] = useState("");
  const [metric, setMetric] = useState("request_latency_ms");
  const [threshold, setThreshold] = useState(500);
  const [operator, setOperator] = useState(">");
  const [error, setError] = useState("");
  const [creating, setCreating] = useState(false);

  function load() {
    v1
      .listAlerts()
      .then((rs) => setRules(rs ?? []))
      .catch(() => setRules([]));
  }

  useEffect(load, []);

  function create() {
    setCreating(true);
    setError("");
    v1
      .upsertAlert({ name, metric, threshold, operator, window: 5 * 60 * 1_000_000_000, enabled: true })
      .then(() => {
        load();
        setName("");
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setCreating(false));
  }

  return (
    <Card>
      <CardHead title="Legacy alerts (v1)" eyebrow="Static threshold alerts, unchanged since the original stack" />
      <div className="grid grid-4" style={{ gap: 8, alignItems: "end" }}>
        <div className="field">
          <label className="field-label">Name</label>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="high latency" />
        </div>
        <div className="field">
          <label className="field-label">Metric</label>
          <input className="input" value={metric} onChange={(e) => setMetric(e.target.value)} />
        </div>
        <div className="field">
          <label className="field-label">Operator / threshold</label>
          <div style={{ display: "flex", gap: 6 }}>
            <select className="select" value={operator} onChange={(e) => setOperator(e.target.value)} style={{ maxWidth: 70 }}>
              <option>{">"}</option>
              <option>{"<"}</option>
              <option>{">="}</option>
              <option>{"<="}</option>
            </select>
            <input className="input" type="number" value={threshold} onChange={(e) => setThreshold(Number(e.target.value) || 0)} />
          </div>
        </div>
        <button className="btn btn-primary" onClick={create} disabled={creating || !name}>
          {creating ? "Saving…" : "Create"}
        </button>
      </div>
      <ErrorBanner message={error} />
      <hr className="hairline" />
      {rules.length > 0 ? (
        <div className="table-wrap">
          <table className="data">
            <thead>
              <tr>
                <th>Name</th>
                <th>Metric</th>
                <th>Condition</th>
                <th>Enabled</th>
              </tr>
            </thead>
            <tbody>
              {rules.map((r) => (
                <tr key={r.id}>
                  <td>{r.name}</td>
                  <td className="mono">{r.metric}</td>
                  <td className="mono">
                    {r.operator} {r.threshold}
                  </td>
                  <td>{r.enabled ? <Badge tone="good">on</Badge> : <Badge tone="neutral">off</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <EmptyState title="No legacy alert rules yet" />
      )}
    </Card>
  );
}
