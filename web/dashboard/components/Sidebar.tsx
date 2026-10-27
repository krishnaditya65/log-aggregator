"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ComponentType, SVGProps } from "react";
import {
  IconOverview,
  IconLogs,
  IconMetrics,
  IconSearch,
  IconAlerts,
  IconAiops,
  IconTraces,
  IconProfiling,
  IconArchive,
} from "./icons";
import { ThemeToggle } from "./ThemeToggle";

const NAV: { href: string; label: string; icon: ComponentType<SVGProps<SVGSVGElement>>; group: string }[] = [
  { href: "/", label: "Overview", icon: IconOverview, group: "" },
  { href: "/logs", label: "Logs", icon: IconLogs, group: "Explore" },
  { href: "/metrics", label: "Metrics", icon: IconMetrics, group: "Explore" },
  { href: "/search", label: "Semantic search", icon: IconSearch, group: "Explore" },
  { href: "/traces", label: "Traces", icon: IconTraces, group: "Explore" },
  { href: "/alerts", label: "Alerts", icon: IconAlerts, group: "Operate" },
  { href: "/aiops", label: "Root cause (AIOps)", icon: IconAiops, group: "Operate" },
  { href: "/profiling", label: "Profiling", icon: IconProfiling, group: "Operate" },
  { href: "/archive", label: "Cold storage", icon: IconArchive, group: "Operate" },
];

export function Sidebar() {
  const pathname = usePathname();
  let lastGroup = "";

  return (
    <aside className="sidebar">
      <div className="brand">
        <div className="brand-mark" />
        <div>
          <div className="brand-name">Custom Log Aggregator</div>
          <div className="brand-sub">observability console</div>
        </div>
      </div>

      {NAV.map((item) => {
        const showGroup = item.group && item.group !== lastGroup;
        lastGroup = item.group;
        const active = item.href === "/" ? pathname === "/" : pathname?.startsWith(item.href);
        const Icon = item.icon;
        return (
          <div key={item.href}>
            {showGroup && <div className="nav-group-label">{item.group}</div>}
            <Link href={item.href} className={`nav-link${active ? " active" : ""}`}>
              <Icon width={16} height={16} />
              {item.label}
            </Link>
          </div>
        );
      })}

      <div className="sidebar-foot">
        <ThemeToggle />
        <div style={{ marginTop: 10 }}>v1 + v2 API surface</div>
      </div>
    </aside>
  );
}
