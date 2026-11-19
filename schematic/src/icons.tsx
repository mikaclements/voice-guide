import type { ReactNode } from "react";
import type { Kind } from "./types.ts";

// Small line icons, one per kind, drawn on a 16-unit grid.
const paths: Record<string, ReactNode> = {
  client: (
    <>
      <rect x="2" y="3" width="12" height="10" rx="2" />
      <path d="M2 6h12" />
      <circle cx="4.2" cy="4.6" r="0.35" fill="currentColor" />
    </>
  ),
  service: (
    <>
      <rect x="2.5" y="2.5" width="11" height="4.5" rx="1.4" />
      <rect x="2.5" y="9" width="11" height="4.5" rx="1.4" />
      <path d="M5 4.75h.01M5 11.25h.01" strokeWidth="1.8" />
    </>
  ),
  module: (
    <>
      <path d="M8 1.8 13.5 4.9v6.2L8 14.2 2.5 11.1V4.9Z" />
      <path d="M2.5 4.9 8 8l5.5-3.1M8 8v6.2" />
    </>
  ),
  core: (
    <>
      <path d="M5 2.5h6l3 5.5-3 5.5H5L2 8Z" />
      <circle cx="8" cy="8" r="1.6" />
    </>
  ),
  shared: (
    <>
      <circle cx="4" cy="8" r="1.8" />
      <circle cx="12" cy="4" r="1.8" />
      <circle cx="12" cy="12" r="1.8" />
      <path d="M5.6 7.2 10.4 4.8M5.6 8.8l4.8 2.4" />
    </>
  ),
  port: (
    <>
      <circle cx="10.5" cy="8" r="3" />
      <path d="M1.8 8h5.7" />
    </>
  ),
  adapter: (
    <>
      <path d="M6 2v3M10 2v3" />
      <path d="M4 5h8v3a4 4 0 0 1-8 0Z" />
      <path d="M8 12v2.2" />
    </>
  ),
  store: (
    <>
      <ellipse cx="8" cy="4" rx="5" ry="2" />
      <path d="M3 4v8c0 1.1 2.2 2 5 2s5-.9 5-2V4" />
      <path d="M3 8c0 1.1 2.2 2 5 2s5-.9 5-2" />
    </>
  ),
  external: (
    <>
      <circle cx="8" cy="8" r="5.8" />
      <path d="M2.2 8h11.6M8 2.2c1.7 1.6 2.5 3.5 2.5 5.8S9.7 12.2 8 13.8C6.3 12.2 5.5 10.3 5.5 8S6.3 3.8 8 2.2Z" />
    </>
  ),
};

export function KindIcon({ kind, size = 16 }: { kind: Kind; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.35"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[kind] ?? paths.module}
    </svg>
  );
}

export function Icon({ name, size = 16 }: { name: string; size?: number }) {
  const glyphs: Record<string, ReactNode> = {
    chevron: <path d="m6 3.5 4.5 4.5L6 12.5" />,
    back: <path d="M10 3.5 5.5 8l4.5 4.5" />,
    close: <path d="m4 4 8 8M12 4l-8 8" />,
    sun: (
      <>
        <circle cx="8" cy="8" r="2.8" />
        <path d="M8 1.5v1.6M8 12.9v1.6M1.5 8h1.6M12.9 8h1.6M3.4 3.4l1.1 1.1M11.5 11.5l1.1 1.1M3.4 12.6l1.1-1.1M11.5 4.5l1.1-1.1" />
      </>
    ),
    moon: <path d="M13.2 9.6A5.6 5.6 0 0 1 6.4 2.8a5.6 5.6 0 1 0 6.8 6.8Z" />,
    plus: <path d="M8 3.5v9M3.5 8h9" />,
    minus: <path d="M3.5 8h9" />,
    fit: (
      <path d="M2.5 6V3.8c0-.7.6-1.3 1.3-1.3H6M10 2.5h2.2c.7 0 1.3.6 1.3 1.3V6M13.5 10v2.2c0 .7-.6 1.3-1.3 1.3H10M6 13.5H3.8c-.7 0-1.3-.6-1.3-1.3V10" />
    ),
    code: <path d="m5.5 4.5-3.5 3.5 3.5 3.5M10.5 4.5 14 8l-3.5 3.5" />,
    arrowRight: <path d="M2.5 8h11M9.5 4l4 4-4 4" />,
    play: <path d="M5 3.5v9l7-4.5Z" />,
    pause: <path d="M5.5 3.5v9M10.5 3.5v9" />,
    arrowLeft: <path d="M13.5 8h-11M6.5 4l-4 4 4 4" />,
    layers: (
      <>
        <path d="m8 2 6 3.2-6 3.2-6-3.2Z" />
        <path d="m2 8.4 6 3.2 6-3.2" />
      </>
    ),
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {glyphs[name]}
    </svg>
  );
}

export function Logo() {
  return (
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden="true">
      <rect width="32" height="32" rx="8" fill="var(--logo-bg)" />
      <path
        d="M9 18v-4M13 21V11M17 19v-6M21 22V10M25 17v-2"
        stroke="var(--logo-fg)"
        strokeWidth="2.2"
        strokeLinecap="round"
      />
    </svg>
  );
}
