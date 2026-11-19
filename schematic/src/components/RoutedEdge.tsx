import { EdgeLabelRenderer, type EdgeProps } from "@xyflow/react";
import { memo } from "react";
import { useFocus } from "../focus.ts";
import type { DrawnEdge } from "../graph.ts";
import type { Point } from "../layout.ts";

export interface EdgeData extends Record<string, unknown> {
  edge: DrawnEdge;
}

const RADIUS = 9;

// roundedPath draws an orthogonal polyline with its corners rounded.
function roundedPath(pts: Point[]): string {
  if (pts.length === 0) return "";
  let d = `M ${pts[0].x} ${pts[0].y}`;
  for (let i = 1; i < pts.length - 1; i++) {
    const p = pts[i - 1];
    const c = pts[i];
    const n = pts[i + 1];
    const d1 = Math.hypot(c.x - p.x, c.y - p.y);
    const d2 = Math.hypot(n.x - c.x, n.y - c.y);
    const r = Math.min(RADIUS, d1 / 2, d2 / 2);
    if (r < 0.5) {
      d += ` L ${c.x} ${c.y}`;
      continue;
    }
    const ax = c.x - ((c.x - p.x) / d1) * r;
    const ay = c.y - ((c.y - p.y) / d1) * r;
    const bx = c.x + ((n.x - c.x) / d2) * r;
    const by = c.y + ((n.y - c.y) / d2) * r;
    d += ` L ${ax} ${ay} Q ${c.x} ${c.y} ${bx} ${by}`;
  }
  const last = pts[pts.length - 1];
  d += ` L ${last.x} ${last.y}`;
  return d;
}

// The arrowhead sits on the last segment; the line stops at its base so a hollow head stays clean.
function head(pts: Point[], len: number) {
  const n = pts.length;
  const tip = pts[n - 1];
  const prev = pts[n - 2] ?? { x: tip.x - 1, y: tip.y };
  const dx = tip.x - prev.x;
  const dy = tip.y - prev.y;
  const l = Math.hypot(dx, dy) || 1;
  const ux = dx / l;
  const uy = dy / l;
  const base = { x: tip.x - ux * len, y: tip.y - uy * len };
  return { tip, base, ux, uy };
}

export const RoutedEdge = memo(function RoutedEdge({ id, data }: EdgeProps) {
  const { edge } = data as EdgeData;
  const f = useFocus();
  const lit = f.edges?.has(id) ?? false;
  const dim = f.edges !== null && !f.edges.has(id);
  const selected = f.selectedEdge === id;
  // A label shows only for a connection lit by the pointer, the selection or a walk-through step,
  // so a level doesn't open as a wall of text. A type view's labels, a field's name each, stay.
  const labelled = edge.mono || lit || selected || f.hoveredEdge === id || f.allLabels;
  const pts = edge.points;
  if (pts.length < 2) return null;

  const style = edge.style;
  const len = style === "implements" ? 11 : style === "holds" ? 8 : 7;
  const h = head(pts, len);
  const shaft = [...pts.slice(0, -1), h.base];
  const d = roundedPath(shaft);
  const w = style === "implements" ? 6 : style === "holds" ? 4.5 : 4;
  const px = -h.uy * w;
  const py = h.ux * w;
  const tri = `M ${h.tip.x} ${h.tip.y} L ${h.base.x + px} ${h.base.y + py} L ${h.base.x - px} ${h.base.y - py} Z`;
  const chevron = `M ${h.base.x + px} ${h.base.y + py} L ${h.tip.x} ${h.tip.y} L ${h.base.x - px} ${h.base.y - py}`;

  const cls = [
    "edge",
    `edge-${style}`,
    lit ? "lit" : "",
    dim ? "dim" : "",
    selected ? "selected" : "",
  ].join(" ");
  const label = edge.label;
  return (
    <>
      <g className={cls}>
        <path className="edge-hit" d={roundedPath(pts)} />
        <path className="edge-line" d={d} />
        {style === "flow" && lit && <path className="edge-flow" d={d} />}
        {style === "holds" ? (
          <path className="edge-head open" d={chevron} />
        ) : (
          <path className={`edge-head ${style === "implements" ? "hollow" : "solid"}`} d={tri} />
        )}
      </g>
      {labelled && label && (edge.send.length > 0 || edge.chip) && (
        <EdgeLabelRenderer>
          <button
            type="button"
            className={[
              "pill",
              `pill-${style}`,
              lit ? "lit" : "",
              dim ? "dim" : "",
              selected ? "selected" : "",
              edge.mono ? "mono" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            style={{
              transform: `translate(${label.x}px, ${label.y}px)`,
              width: label.width,
              height: label.height,
            }}
            onMouseEnter={() => f.hoverEdge(id)}
            onMouseLeave={() => f.hoverEdge(null)}
            onClick={(e) => {
              e.stopPropagation();
              f.selectEdge(id);
            }}
          >
            {edge.mono ? (
              <span className="pill-via">{edge.send[0]}</span>
            ) : (
              <span className="pill-grid">
                {edge.chip && <span className="pill-chip">{edge.chip}</span>}
                <span className="pill-text">
                  {edge.send.map((l) => (
                    <span key={l} className="pill-send">
                      {l}
                    </span>
                  ))}
                  {edge.back.map((l, i) => (
                    <span key={l} className="pill-back">
                      <span className="pill-arrow">{i === 0 ? "←" : ""}</span>
                      {l}
                    </span>
                  ))}
                </span>
              </span>
            )}
          </button>
        </EdgeLabelRenderer>
      )}
    </>
  );
});

export const edgeTypes = { routed: RoutedEdge };
