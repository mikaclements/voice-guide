import type { Kind } from "./types.ts";

// One colour per kind of component; the CSS defines --k-<kind> for both themes.
export const KINDS: Record<Exclude<Kind, "group">, { label: string; legend: string }> = {
  client: { label: "Client", legend: "Client" },
  service: { label: "Service", legend: "Service" },
  module: { label: "Module", legend: "Module" },
  core: { label: "Core", legend: "Module core" },
  shared: { label: "Shared", legend: "Shared package" },
  port: { label: "Port", legend: "Port" },
  adapter: { label: "Adapter", legend: "Adapter" },
  store: { label: "Data store", legend: "Data store" },
  external: { label: "External", legend: "External" },
};

export const LEGEND_ORDER: Exclude<Kind, "group">[] = [
  "client",
  "service",
  "module",
  "core",
  "shared",
  "port",
  "adapter",
  "store",
  "external",
];

export function kindColor(kind: Kind | undefined): string {
  return `var(--k-${kind && kind !== "group" ? kind : "module"})`;
}
