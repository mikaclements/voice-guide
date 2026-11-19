// Lays out one level with ELK's layered algorithm: groups as compound nodes, orthogonal edges, and
// each edge label given the exact size of its pill so ELK keeps it clear of nodes and other labels.
import ELK from "elkjs/lib/elk.bundled.js";
import type { ElkExtendedEdge, ElkNode, LayoutOptions } from "elkjs/lib/elk-api";

export interface Point {
  x: number;
  y: number;
}

export interface SpecNode {
  id: string;
  parent?: string;
  width: number;
  height: number;
  group?: boolean;
  // Space a group keeps around its children; the top holds its header.
  padding?: { top: number; left: number; bottom: number; right: number };
  // The narrowest a group may be, so its header is never cut.
  minWidth?: number;
}

export interface SpecEdge {
  id: string;
  from: string;
  to: string;
  label?: { width: number; height: number };
  // Laid out from target to source, so a port sits before the adapters that implement it; drawn
  // the right way round.
  reverse?: boolean;
}

export interface Placed {
  id: string;
  parent?: string;
  x: number; // relative to the parent, as React Flow wants children
  y: number;
  absX: number;
  absY: number;
  width: number;
  height: number;
}

export interface Routed {
  id: string;
  points: Point[];
  label?: { x: number; y: number; width: number; height: number };
}

export interface Layout {
  nodes: Map<string, Placed>;
  edges: Map<string, Routed>;
  width: number;
  height: number;
}

const elk = new ELK();

export function baseOptions(density: "roomy" | "dense" = "roomy"): LayoutOptions {
  const dense = density === "dense";
  return {
    "elk.algorithm": "layered",
    "elk.direction": "RIGHT",
    "elk.hierarchyHandling": "INCLUDE_CHILDREN",
    "elk.edgeRouting": "ORTHOGONAL",
    "elk.layered.spacing.nodeNodeBetweenLayers": "48",
    "elk.spacing.nodeNode": dense ? "28" : "34",
    "elk.spacing.edgeNode": dense ? "18" : "22",
    "elk.spacing.edgeEdge": "12",
    "elk.layered.spacing.edgeNodeBetweenLayers": dense ? "16" : "18",
    "elk.layered.spacing.edgeEdgeBetweenLayers": "12",
    "elk.spacing.edgeLabel": "6",
    "elk.spacing.labelNode": "8",
    "elk.edgeLabels.inline": "true",
    "elk.layered.edgeLabels.sideSelection": "SMART_DOWN",
    "elk.layered.nodePlacement.strategy": "NETWORK_SIMPLEX",
    "elk.layered.crossingMinimization.strategy": "LAYER_SWEEP",
    "elk.layered.considerModelOrder.strategy": "NODES_AND_EDGES",
    "elk.layered.thoroughness": "40",
    "elk.layered.mergeEdges": "false",
    "elk.padding": "[top=24,left=24,bottom=24,right=24]",
  };
}

export async function layout(
  nodes: SpecNode[],
  edges: SpecEdge[],
  options: LayoutOptions,
): Promise<Layout> {
  const byId = new Map<string, ElkNode>();
  const root: ElkNode = { id: "__root", layoutOptions: options, children: [], edges: [] };
  for (const n of nodes) {
    const e: ElkNode = { id: n.id, width: n.width, height: n.height };
    if (n.group) {
      e.children = [];
      const p = n.padding ?? { top: 48, left: 22, bottom: 22, right: 22 };
      e.layoutOptions = {
        "elk.padding": `[top=${p.top},left=${p.left},bottom=${p.bottom},right=${p.right}]`,
      };
      if (n.minWidth) {
        e.layoutOptions["elk.nodeSize.constraints"] = "MINIMUM_SIZE";
        e.layoutOptions["elk.nodeSize.minimum"] = `(${Math.ceil(n.minWidth)}, 0)`;
      }
      delete e.width;
      delete e.height;
    }
    byId.set(n.id, e);
  }
  for (const n of nodes) {
    const e = byId.get(n.id);
    const parent = (n.parent ? byId.get(n.parent) : undefined) ?? root;
    if (e) parent.children = [...(parent.children ?? []), e];
  }
  root.edges = edges.map((e): ElkExtendedEdge => {
    const [s, t] = e.reverse ? [e.to, e.from] : [e.from, e.to];
    const out: ElkExtendedEdge = { id: e.id, sources: [s], targets: [t] };
    if (e.label) {
      out.labels = [
        {
          id: `${e.id}__label`,
          text: " ",
          width: e.label.width,
          height: e.label.height,
          layoutOptions: { "elk.edgeLabels.placement": "CENTER" },
        },
      ];
    }
    return out;
  });

  const done = await elk.layout(root);

  const placed = new Map<string, Placed>();
  const walk = (n: ElkNode, parent: string | undefined, ox: number, oy: number) => {
    for (const c of n.children ?? []) {
      const x = c.x ?? 0;
      const y = c.y ?? 0;
      placed.set(c.id, {
        id: c.id,
        parent,
        x,
        y,
        absX: ox + x,
        absY: oy + y,
        width: c.width ?? 0,
        height: c.height ?? 0,
      });
      walk(c, c.id, ox + x, oy + y);
    }
  };
  walk(done, undefined, 0, 0);

  const routed = new Map<string, Routed>();
  const reversed = new Set(edges.filter((e) => e.reverse).map((e) => e.id));
  for (const e of (done.edges ?? []) as (ElkExtendedEdge & { container?: string })[]) {
    const c = e.container && e.container !== "__root" ? placed.get(e.container) : undefined;
    const ox = c?.absX ?? 0;
    const oy = c?.absY ?? 0;
    const pts: Point[] = [];
    for (const s of e.sections ?? []) {
      pts.push({ x: s.startPoint.x + ox, y: s.startPoint.y + oy });
      for (const b of s.bendPoints ?? []) pts.push({ x: b.x + ox, y: b.y + oy });
      pts.push({ x: s.endPoint.x + ox, y: s.endPoint.y + oy });
    }
    if (reversed.has(e.id)) pts.reverse();
    const l = e.labels?.[0];
    routed.set(e.id, {
      id: e.id,
      points: pts,
      label:
        l && l.x !== undefined && l.y !== undefined
          ? { x: l.x + ox, y: l.y + oy, width: l.width ?? 0, height: l.height ?? 0 }
          : undefined,
    });
  }
  return { nodes: placed, edges: routed, width: done.width ?? 0, height: done.height ?? 0 };
}
