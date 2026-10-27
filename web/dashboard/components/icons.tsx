// Minimal inline icon set (stroke-based, 18px, currentColor) - no icon
// library dependency, matches the rest of this app's zero-dependency
// footprint (just next/react).
import type { SVGProps } from "react";

function Icon(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={16}
      height={16}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    />
  );
}

export const IconOverview = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="3" y="3" width="7" height="9" rx="1.5" />
    <rect x="14" y="3" width="7" height="5" rx="1.5" />
    <rect x="14" y="12" width="7" height="9" rx="1.5" />
    <rect x="3" y="16" width="7" height="5" rx="1.5" />
  </Icon>
);

export const IconLogs = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M4 4h16M4 9h16M4 14h10M4 19h7" />
  </Icon>
);

export const IconMetrics = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M4 19V9M11 19V4M18 19v-6" />
  </Icon>
);

export const IconSearch = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="M21 21l-4.3-4.3" />
  </Icon>
);

export const IconAlerts = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M12 3l9 16H3z" />
    <path d="M12 10v4M12 17.5v.01" />
  </Icon>
);

export const IconAiops = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="6" cy="18" r="2.4" />
    <circle cx="18" cy="6" r="2.4" />
    <circle cx="12" cy="12" r="2.4" />
    <path d="M8 16.3l2.2-2.6M13.8 10.3l2.2-2.6" />
  </Icon>
);

export const IconTraces = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="3" y="5" width="10" height="3.2" rx="1" />
    <rect x="7" y="10.4" width="14" height="3.2" rx="1" />
    <rect x="3" y="15.8" width="7" height="3.2" rx="1" />
  </Icon>
);

export const IconProfiling = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M4 20V9M9 20v-6M14 20V6M19 20v-9" />
  </Icon>
);

export const IconArchive = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <rect x="3" y="4" width="18" height="4.5" rx="1" />
    <path d="M5 8.5V19a1 1 0 001 1h12a1 1 0 001-1V8.5" />
    <path d="M10 13h4" />
  </Icon>
);

export const IconChevron = (p: SVGProps<SVGSVGElement>) => (
  <Icon strokeWidth={2.2} {...p}>
    <path d="M9 6l6 6-6 6" />
  </Icon>
);

export const IconRun = (p: SVGProps<SVGSVGElement>) => (
  <Icon fill="currentColor" stroke="none" {...p}>
    <path d="M6 4l14 8-14 8V4z" />
  </Icon>
);

export const IconSun = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <circle cx="12" cy="12" r="4.2" />
    <path d="M12 2.5v2.2M12 19.3v2.2M4.4 4.4l1.6 1.6M18 18l1.6 1.6M2.5 12h2.2M19.3 12h2.2M4.4 19.6L6 18M18 6l1.6-1.6" />
  </Icon>
);

export const IconMoon = (p: SVGProps<SVGSVGElement>) => (
  <Icon {...p}>
    <path d="M20 14.5A8.5 8.5 0 1110 3.2a6.8 6.8 0 0010 11.3z" />
  </Icon>
);

export const IconEmpty = (p: SVGProps<SVGSVGElement>) => (
  <Icon width={28} height={28} {...p}>
    <circle cx="11" cy="11" r="7" />
    <path d="M21 21l-4.3-4.3M8 11h6" />
  </Icon>
);
