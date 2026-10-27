"use client";

import { useEffect, useState } from "react";
import { IconSun, IconMoon } from "./icons";

type Mode = "dark" | "light";

export function ThemeToggle() {
  const [mode, setMode] = useState<Mode>("dark");

  useEffect(() => {
    const stored = window.localStorage.getItem("cla-theme") as Mode | null;
    if (stored === "dark" || stored === "light") apply(stored);
  }, []);

  function apply(next: Mode) {
    setMode(next);
    document.documentElement.setAttribute("data-theme", next);
    window.localStorage.setItem("cla-theme", next);
  }

  return (
    <div className="theme-toggle" role="group" aria-label="Theme">
      <button className={mode === "dark" ? "active" : ""} onClick={() => apply("dark")} title="Dark theme" aria-label="Dark theme">
        <IconMoon width={13} height={13} />
      </button>
      <button className={mode === "light" ? "active" : ""} onClick={() => apply("light")} title="Light theme" aria-label="Light theme">
        <IconSun width={13} height={13} />
      </button>
    </div>
  );
}
