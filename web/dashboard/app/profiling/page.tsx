"use client";

import { useState } from "react";
import { v2, type FlameNode } from "@/lib/api";
import { PageHead, Card, CardHead, ErrorBanner, EmptyState } from "@/components/ui";
import { Flamegraph } from "@/components/Flamegraph";
import { formatDuration, formatBytes, formatTime } from "@/lib/format";

const DURATIONS = ["2s", "5s", "10s", "30s"];

export default function ProfilingPage() {
  const [duration, setDuration] = useState("5s");
  const [capture, setCapture] = useState<{ service: string; host: string; time: number; bytes: number } | null>(null);
  const [flame, setFlame] = useState<FlameNode | null>(null);
  const [error, setError] = useState("");
  const [capturing, setCapturing] = useState(false);
  const [loadingGraph, setLoadingGraph] = useState(false);

  function runCapture() {
    setCapturing(true);
    setError("");
    setFlame(null);
    v2
      .captureProfile(duration)
      .then((c) => {
        setCapture(c);
        setLoadingGraph(true);
        return v2.flamegraph(c.time);
      })
      .then(setFlame)
      .catch((e) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => {
        setCapturing(false);
        setLoadingGraph(false);
      });
  }

  return (
    <>
      <PageHead
        title="Profiling"
        desc="Continuous CPU profiling via runtime/pprof, decoded into a flamegraph by a hand-rolled pprof-protobuf parser — no external profiling service dependency."
      />

      <Card>
        <CardHead title="Capture a CPU profile" eyebrow="POST /v2/profiling/capture · GET /v2/profiling/flamegraph" />
        <div className="query-row">
          <select className="select" value={duration} onChange={(e) => setDuration(e.target.value)} style={{ maxWidth: 140 }}>
            {DURATIONS.map((d) => (
              <option key={d} value={d}>
                {d} capture
              </option>
            ))}
          </select>
          <button className="btn btn-primary" onClick={runCapture} disabled={capturing}>
            {capturing ? "Capturing…" : "Capture & render flamegraph"}
          </button>
        </div>
        <ErrorBanner message={error} />
      </Card>

      {capture && (
        <div className="grid grid-4 section">
          <div className="card card-pad stat-tile">
            <div className="stat-label">Service</div>
            <div className="stat-value" style={{ fontSize: 16 }}>
              {capture.service || "—"}
            </div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Host</div>
            <div className="stat-value" style={{ fontSize: 16 }}>
              {capture.host || "—"}
            </div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Captured at</div>
            <div className="stat-value" style={{ fontSize: 16 }}>
              {formatTime(capture.time)}
            </div>
          </div>
          <div className="card card-pad stat-tile">
            <div className="stat-label">Profile size</div>
            <div className="stat-value">{formatBytes(capture.bytes)}</div>
          </div>
        </div>
      )}

      <Card className="section">
        <CardHead title="Flamegraph" eyebrow={flame ? `root total ${formatDuration(flame.TotalNS)}` : "click a frame to zoom"} />
        {loadingGraph ? (
          <div className="skeleton" style={{ height: 240 }} />
        ) : flame ? (
          <Flamegraph root={flame} />
        ) : (
          <EmptyState title="No profile captured yet" hint="Capture a CPU profile above to render its flamegraph." />
        )}
      </Card>
    </>
  );
}
