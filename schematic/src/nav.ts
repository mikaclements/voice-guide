// How the levels link: which views a view opens, and the chain down to any view.
import type { Schematic, View } from "./types.ts";

// drillsFrom lists the views a view opens: its cards' drill targets and its class view.
export function drillsFrom(view: View | undefined): string[] {
  if (!view) return [];
  const out: string[] = [];
  for (const n of view.nodes ?? []) if (!n.context && n.drill) out.push(n.drill);
  if (view.classes) out.push(view.classes);
  return out;
}

// pathTo is the chain of views from the top level down to id, so a link to any view keeps its
// breadcrumb.
export function pathTo(s: Schematic, id: string): string[] {
  const top = s.views[0].id;
  const prev = new Map<string, string>();
  const seen = new Set([top]);
  const queue = [top];
  for (let v = queue.shift(); v !== undefined; v = queue.shift()) {
    if (v === id) break;
    for (const next of drillsFrom(s.views.find((x) => x.id === v))) {
      if (seen.has(next)) continue;
      seen.add(next);
      prev.set(next, v);
      queue.push(next);
    }
  }
  if (!seen.has(id)) return [top];
  const path = [id];
  for (let c = prev.get(id); c !== undefined; c = prev.get(c)) path.unshift(c);
  return path;
}
