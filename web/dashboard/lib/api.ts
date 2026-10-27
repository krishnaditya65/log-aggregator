// Typed client for internal/api's v1 + v2 HTTP surface. See
// docs/ARCHITECTURE.md "API reference" in the repo root for the canonical
// route list. Every wire type below mirrors a Go struct with no `json`
// tags, so field names are the exported Go names verbatim (capitalized) -
// except internal/domain types (LogEntry, MetricPoint, SpanEntry, AlertRule),
// which do carry snake_case tags.

export const API_URL = process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_URL}${path}`, {
    ...init,
    headers: { "content-type": "application/json", ...init?.headers },
  });
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText);
    throw new ApiError(res.status, text || `${res.status} ${res.statusText}`);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function qs(params: Record<string, string | number | undefined | null>): string {
  const search = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") search.set(k, String(v));
  }
  const s = search.toString();
  return s ? `?${s}` : "";
}

// ---------- v1 (domain.* wire types - snake_case tags) ----------

export type LogEntry = {
  id?: string;
  time: string;
  service: string;
  host?: string;
  level?: string;
  message: string;
  trace_id?: string;
  span_id?: string;
  tenant_id?: string;
  tags?: Record<string, string>;
  fields?: Record<string, unknown>;
  raw?: string;
};

export type MetricPoint = {
  id?: string;
  name: string;
  time: string;
  service: string;
  value: number;
  unit?: string;
  tenant_id?: string;
  tags?: Record<string, string>;
};

export type AlertRuleV1 = {
  id: string;
  name: string;
  metric: string;
  threshold: number;
  window: number; // nanoseconds (time.Duration)
  operator: string;
  webhook_url?: string;
  tags?: Record<string, string>;
  enabled: boolean;
};

export type StoreSnapshot = { log_count: number; metric_count: number };

export const v1 = {
  searchLogs: (q: string, limit = 50, tag?: string) =>
    request<LogEntry[]>(`/v1/logs${qs({ q, limit, tag })}`),
  queryMetrics: (name: string, start?: string, end?: string, tag?: string) =>
    request<MetricPoint[]>(`/v1/metrics${qs({ name, start, end, tag })}`),
  listAlerts: () => request<AlertRuleV1[]>("/v1/alerts"),
  upsertAlert: (rule: Partial<AlertRuleV1>) =>
    request<AlertRuleV1>("/v1/alerts", { method: "POST", body: JSON.stringify(rule) }),
  stats: () => request<StoreSnapshot>("/v1/stats"),
};

// ---------- v2 (Go-default field casing: capitalized, no json tags) ----------

export type PipeqlRecord = {
  Time: number; // unix seconds
  Host: string;
  Service: string;
  Level: string;
  Message: string;
  Fields?: Record<string, unknown>;
};

export type PipeqlResult = {
  IsStats: boolean;
  Records: PipeqlRecord[];
  StatsFields: string[] | null;
  StatsRows: Record<string, unknown>[] | null;
};

export type SemanticResult = { id: string; score: number; text: string };

export type MetricSeries = {
  Labels: Record<string, string>;
  Points: { T: number; V: number }[];
};

export type Span = {
  TraceID: string;
  SpanID: string;
  ParentSpanID?: string;
  Service: string;
  Name: string;
  StartTime: number; // unix nanos
  EndTime: number; // unix nanos
  Status?: string;
  Attributes?: Record<string, unknown>;
  Tags?: Record<string, string>;
};

export type RuleView = {
  id: string;
  name: string;
  expr: string;
  for: number; // nanoseconds
  severity: string;
  labels?: Record<string, string>;
};

export type RootCauseHypothesis = {
  Service: string;
  Time: number; // unix seconds
  Confidence: number;
  Evidence: string[];
};

export type ArchiveRow = Record<string, unknown>;

export type V2Stats = {
  tsdb_decoded_blocks: number;
  logsdb_segment_stats: unknown;
  vector_index_size: number;
  tracing_pending_traces: number;
};

export const v2 = {
  ingestLogs: (entries: LogEntry[]) => request<{ accepted: number }>("/v2/logs", { method: "POST", body: JSON.stringify(entries) }),
  ingestMetrics: (points: MetricPoint[]) =>
    request<{ accepted: number }>("/v2/metrics", { method: "POST", body: JSON.stringify(points) }),

  searchLogs: (pipeline: string, start?: string, end?: string) =>
    request<PipeqlResult>(`/v2/logs/search${qs({ pipeline, start, end })}`),
  semanticSearch: (q: string, k = 10) => request<SemanticResult[]>(`/v2/logs/semantic${qs({ q, k })}`),
  queryMetrics: (query: string, time?: number) => request<MetricSeries[]>(`/v2/metrics/query${qs({ query, time })}`),
  getTrace: (id: string) => request<Span[]>(`/v2/traces/${encodeURIComponent(id)}`),

  listAlertRules: () => request<RuleView[]>("/v2/alerts/rules"),
  createAlertRule: (spec: {
    id?: string;
    name: string;
    expr: string;
    for_seconds: number;
    severity: string;
    labels?: Record<string, string>;
    annotations?: Record<string, string>;
  }) => request<RuleView>("/v2/alerts/rules", { method: "POST", body: JSON.stringify(spec) }),
  createSilence: (silence: {
    ID?: string;
    Matchers: Record<string, string>;
    StartsAt: string;
    EndsAt: string;
    CreatedBy?: string;
    Comment?: string;
  }) => request<unknown>("/v2/alerts/silences", { method: "POST", body: JSON.stringify(silence) }),
  recordDeployment: (evt: { service: string; version: string; time?: string }) =>
    request<{ ok: boolean }>("/v2/deployments", { method: "POST", body: JSON.stringify(evt) }),

  rootCause: (window?: string) => request<RootCauseHypothesis[]>(`/v2/aiops/rootcause${qs({ window })}`),

  captureProfile: (duration?: string) =>
    request<{ service: string; host: string; time: number; bytes: number }>(
      `/v2/profiling/capture${qs({ duration })}`,
      { method: "POST" },
    ),
  flamegraph: (time: number) => request<FlameNode>(`/v2/profiling/flamegraph${qs({ time })}`),

  archiveIngest: (rows: ArchiveRow[]) =>
    request<{ archived: number; key: string }>("/v2/archive/ingest", { method: "POST", body: JSON.stringify({ rows }) }),
  archiveQuery: (prefix: string, predicates: string) =>
    request<ArchiveRow[]>(`/v2/archive/query${qs({ prefix, predicates })}`),

  stats: () => request<V2Stats>("/v2/stats"),
};

export type FlameNode = {
  Name: string;
  SelfNS: number;
  TotalNS: number;
  Children: FlameNode[] | null;
};
