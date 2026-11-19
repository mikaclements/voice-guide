// Folding: at a module's level, each adapter is tucked behind the core card it plugs into (a port
// it implements, or the core type it calls), so the level opens on its rules and its ports. A
// card from outside the level that only an adapter reaches is tucked away with it. Opening a host
// card shows its adapters again.
import type { View } from "./types.ts";

// foldHosts maps each adapter that folds to the cards it folds behind: the ports it implements or,
// for an adapter that implements none, the core types it calls. An adapter with neither stays.
export function foldHosts(view: View): Map<string, string[]> {
  const byId = new Map((view.nodes ?? []).map((n) => [n.id, n]));
  const hosts = new Map<string, string[]>();
  for (const n of view.nodes ?? []) {
    if (n.kind !== "adapter" || n.context) continue;
    const out = (view.edges ?? []).filter((e) => e.from === n.id);
    let to = out.filter((e) => e.style === "implements").map((e) => e.to);
    if (!to.length)
      to = out
        .filter((e) => {
          const t = byId.get(e.to);
          return t && !t.context && (t.kind === "core" || t.kind === "port");
        })
        .map((e) => e.to);
    if (to.length) hosts.set(n.id, [...new Set(to)]);
  }
  return hosts;
}

// hostGroup is every host that shares an adapter with host, directly or through another, so
// opening one opens them all and a store implementing three ports never shows half open.
export function hostGroup(view: View, host: string): string[] {
  const hosts = foldHosts(view);
  const group = new Set([host]);
  let grew = true;
  while (grew) {
    grew = false;
    for (const hs of hosts.values()) {
      if (hs.some((h) => group.has(h)) && hs.some((h) => !group.has(h))) {
        for (const h of hs) group.add(h);
        grew = true;
      }
    }
  }
  return [...group];
}

// hiddenNodes lists the cards folded away: adapters none of whose hosts is open, and the outside
// cards whose every connection is to one of them. Cards in keep (a walk-through's) always show.
export function hiddenNodes(view: View, open: Set<string>, keep: Set<string>): Set<string> {
  const hidden = new Set<string>();
  for (const [adapter, hs] of foldHosts(view)) {
    if (!keep.has(adapter) && !hs.some((h) => open.has(h))) hidden.add(adapter);
  }
  for (const n of view.nodes ?? []) {
    if (!n.context || keep.has(n.id)) continue;
    const edges = (view.edges ?? []).filter((e) => e.from === n.id || e.to === n.id);
    const other = (e: { from: string; to: string }) => (e.from === n.id ? e.to : e.from);
    if (edges.length && edges.every((e) => hidden.has(other(e)))) hidden.add(n.id);
  }
  return hidden;
}

// foldedBehind lists, for each host card, the adapters folded behind it, shown or not.
export function foldedBehind(view: View): Map<string, string[]> {
  const behind = new Map<string, string[]>();
  for (const [adapter, hs] of foldHosts(view)) {
    for (const h of hs) behind.set(h, [...(behind.get(h) ?? []), adapter]);
  }
  return behind;
}
