import type { ReactNode } from "react";
import { badgeClass, severityToStatus } from "@/lib/colors";
import { IconEmpty } from "./icons";

export function Card({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={`card card-pad${className ? ` ${className}` : ""}`}>{children}</div>;
}

export function CardHead({ title, eyebrow, action }: { title: string; eyebrow?: string; action?: ReactNode }) {
  return (
    <div className="card-head">
      <div>
        {eyebrow && <div className="eyebrow">{eyebrow}</div>}
        <h3 className="card-title">{title}</h3>
      </div>
      {action}
    </div>
  );
}

export function PageHead({ title, desc, action }: { title: string; desc?: string; action?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1 className="page-title">{title}</h1>
        {desc && <p className="page-desc">{desc}</p>}
      </div>
      {action}
    </div>
  );
}

export function StatTile({
  label,
  value,
  foot,
  loading,
}: {
  label: string;
  value: string;
  foot?: string;
  loading?: boolean;
}) {
  return (
    <div className="card card-pad stat-tile">
      <div className="stat-label">{label}</div>
      {loading ? (
        <div className="skeleton" style={{ height: 26, width: "60%" }} />
      ) : (
        <div className="stat-value">{value}</div>
      )}
      {foot && <div className="stat-foot">{foot}</div>}
    </div>
  );
}

export function SeverityBadge({ level }: { level?: string }) {
  const status = severityToStatus(level);
  const text = level ? level.toUpperCase() : "UNKNOWN";
  return (
    <span className={badgeClass(level)}>
      <span className="badge-dot" />
      {text}
    </span>
  );
}

export function Badge({ tone, children }: { tone: "good" | "warning" | "serious" | "critical" | "neutral"; children: ReactNode }) {
  return (
    <span className={`badge badge-${tone}`}>
      <span className="badge-dot" />
      {children}
    </span>
  );
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="empty-state">
      <IconEmpty />
      <div style={{ color: "var(--text-secondary)", fontWeight: 600 }}>{title}</div>
      {hint && <div>{hint}</div>}
    </div>
  );
}

export function ErrorBanner({ message }: { message: string }) {
  if (!message) return null;
  return <div className="error-banner">{message}</div>;
}

export function Skeleton({ height = 16, width = "100%" }: { height?: number; width?: string | number }) {
  return <div className="skeleton" style={{ height, width }} />;
}

export function QueryBar({
  value,
  onChange,
  onRun,
  placeholder,
  examples,
  running,
}: {
  value: string;
  onChange: (v: string) => void;
  onRun: () => void;
  placeholder?: string;
  examples?: string[];
  running?: boolean;
}) {
  return (
    <div>
      <div className="query-row">
        <input
          className="input input-mono"
          value={value}
          placeholder={placeholder}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") onRun();
          }}
        />
        <button className="btn btn-primary" onClick={onRun} disabled={running}>
          {running ? "Running…" : "Run"}
        </button>
      </div>
      {examples && examples.length > 0 && (
        <div className="chip-row">
          {examples.map((ex) => (
            <button key={ex} className="chip" onClick={() => onChange(ex)} title="Use this example">
              {ex}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function DataTable<T extends Record<string, unknown>>({
  columns,
  rows,
  rowKey,
}: {
  columns: { key: string; label: string; mono?: boolean; render?: (row: T) => ReactNode }[];
  rows: T[];
  rowKey: (row: T, i: number) => string;
}) {
  return (
    <div className="table-wrap">
      <table className="data">
        <thead>
          <tr>
            {columns.map((c) => (
              <th key={c.key}>{c.label}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, i) => (
            <tr key={rowKey(row, i)}>
              {columns.map((c) => (
                <td key={c.key} className={c.mono ? "mono" : undefined}>
                  {c.render ? c.render(row) : String(row[c.key] ?? "—")}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
