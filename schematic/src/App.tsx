// The page: a top bar with the breadcrumb, the canvas for the level you're on, and the side panel.
// Double-click (or a card's footer) drills down; the breadcrumb, Esc and Backspace go up. A
// walk-through lights one step of a story at a time, captioned in a bar over the canvas.
import {
  Background,
  BackgroundVariant,
  type Edge,
  type FitViewOptions,
  type Node,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from "@xyflow/react";
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { FlowBar } from "./components/FlowBar.tsx";
import { type NodeData, nodeTypes } from "./components/nodes.tsx";
import { Panel, type Selection } from "./components/Panel.tsx";
import { edgeTypes } from "./components/RoutedEdge.tsx";
import { type Focus, FocusContext } from "./focus.ts";
import { foldedBehind, hiddenNodes, hostGroup } from "./fold.ts";
import { type Drawn, drawClasses, drawView, type Folding } from "./graph.ts";
import { Icon, Logo } from "./icons.tsx";
import { KINDS, kindColor, LEGEND_ORDER } from "./kinds.ts";
import { pathTo } from "./nav.ts";
import type { ClassData, Kind, Schematic, View } from "./types.ts";
import { stepMillis, type Walk, walkAfter, walkBefore } from "./walk.ts";

type Theme = "light" | "dark";

function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => {
    const asked = new URLSearchParams(location.search).get("theme");
    if (asked === "light" || asked === "dark") return asked;
    return localStorage.getItem("schematic-theme") === "dark" ? "dark" : "light";
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("schematic-theme", theme);
  }, [theme]);
  return [theme, () => setTheme((t) => (t === "light" ? "dark" : "light"))];
}

// drawKeyOf names what a drawing shows: the view and, for a class view, the filter, and for a
// folded level, what is folded.
function drawKeyOf(view: View, unexported: boolean, folding?: Folding): string {
  const filter = view.type === "classes" && unexported ? "all" : "exported";
  const folded = folding
    ? `${[...folding.hidden].sort().join(",")}/${[...folding.folds]
        .map(([id, f]) => `${id}:${f.open ? 1 : 0}`)
        .join(",")}`
    : "";
  return `${view.id}|${filter}|${folded}`;
}

// Room for the toolbar above and the legend and zoom buttons below; more above for a level's
// "start here" caption, and more below for a walk-through's bar, which is taller than the legend.
function fitOptions(start: boolean, walking: boolean): FitViewOptions {
  return {
    padding: {
      top: start ? "128px" : "68px",
      bottom: walking ? "150px" : "72px",
      left: "28px",
      right: "28px",
    },
    maxZoom: 1.15,
    minZoom: 0.12,
  };
}

// What the address asks for beyond the level: every adapter or label shown, or a walk-through's
// step open. The visual check uses these to screenshot each state.
const asked = new URLSearchParams(location.search);
const askedAll = asked.get("adapters") === "all";
const askedLabels = asked.get("labels") === "all";

export function App() {
  return (
    <ReactFlowProvider>
      <Shell />
    </ReactFlowProvider>
  );
}

function Shell() {
  const [schematic, setSchematic] = useState<Schematic | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [stack, setStack] = useState<string[]>([]);
  const [classes, setClasses] = useState<Record<string, ClassData>>({});
  const [drawn, setDrawn] = useState<{ key: string; d: Drawn } | null>(null);
  const [selection, setSelection] = useState<Selection>(null);
  const [hoverNode, setHoverNode] = useState<string | null>(null);
  const [hoverEdge, setHoverEdge] = useState<string | null>(null);
  const [unexported, setUnexported] = useState(false);
  // The package a drill into a class view should fit on, set by the card that opened it.
  const [focusPkg, setFocusPkg] = useState<string | null>(null);
  // Host cards whose folded adapters show, on this level; or every adapter, on every level.
  const [opened, setOpened] = useState<Set<string>>(new Set());
  const [showAll, setShowAll] = useState(askedAll);
  // The walk-through step open, and whether it plays on by itself.
  const [walk, setWalk] = useState<Walk | null>(null);
  const [playing, setPlaying] = useState(false);
  // A step the address asked for, opened once its level is shown.
  const askedWalk = useRef<{ view: string; walk: Walk } | null>(null);
  const [theme, toggleTheme] = useTheme();
  const rf = useReactFlow();
  const canvasRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    fetch("schematic.json")
      .then((r) => {
        if (!r.ok) throw new Error(`schematic.json: ${r.status}`);
        return r.json() as Promise<Schematic>;
      })
      .then((s) => {
        setSchematic(s);
        const id = decodeURIComponent(location.hash.slice(1)) || s.views[0].id;
        const flow = asked.get("flow");
        if (flow)
          askedWalk.current = {
            view: id,
            walk: { flow, step: Number(asked.get("step") ?? 1) - 1 },
          };
        setStack(pathTo(s, id));
      })
      .catch((e: unknown) => setError(String(e)));
  }, []);

  const view = schematic?.views.find((v) => v.id === stack[stack.length - 1]);
  const isClasses = view?.type === "classes";
  const classData = view?.source ? classes[view.source] : undefined;

  useEffect(() => {
    const source = view?.source;
    if (!isClasses || !source || classes[source]) return;
    fetch(source)
      .then((r) => {
        if (!r.ok) throw new Error(`${source}: ${r.status}; scripts/schematic.sh generates it`);
        return r.json() as Promise<ClassData>;
      })
      .then((d) => setClasses((c) => ({ ...c, [source]: d })))
      .catch((e: unknown) => setError(String(e)));
  }, [view, isClasses, classes]);

  const flow = walk ? view?.flows?.find((f) => f.id === walk.flow) : undefined;
  const step = flow?.steps[walk?.step ?? 0];

  // What is folded: adapters behind closed hosts, unless every adapter shows; a walk-through's
  // cards always show, so its steps light cards that are there.
  const folding = useMemo((): Folding | undefined => {
    if (!view || view.type === "classes") return undefined;
    const behind = foldedBehind(view);
    if (!behind.size) return undefined;
    const keep = new Set(flow?.steps.flatMap((s) => s.nodes) ?? []);
    const hidden = showAll ? new Set<string>() : hiddenNodes(view, opened, keep);
    const folds = new Map<string, { count: number; open: boolean }>();
    if (!showAll && !flow)
      for (const [h, adapters] of behind)
        folds.set(h, { count: adapters.length, open: opened.has(h) });
    return { hidden, folds };
  }, [view, flow, showAll, opened]);

  const drawKey = view ? drawKeyOf(view, unexported, folding) : "";
  useEffect(() => {
    if (!view || !schematic) return;
    let drawing: Promise<Drawn>;
    if (view.type === "classes") {
      if (!classData) return;
      drawing = drawClasses(classData, view.root ?? "", { unexported });
    } else {
      drawing = drawView(view, schematic, folding);
    }
    let live = true;
    const key = drawKeyOf(view, unexported, folding);
    drawing
      .then((d) => live && setDrawn({ key, d }))
      .catch((e: unknown) => live && setError(String(e)));
    return () => {
      live = false;
    };
  }, [view, schematic, classData, unexported, folding]);

  const current = drawn && drawn.key === drawKey ? drawn.d : null;

  // The level you're on is in the address, so a link opens it and the browser's back button goes up.
  useEffect(() => {
    if (!view) return;
    if (decodeURIComponent(location.hash.slice(1)) === view.id) return;
    const url = `${location.pathname}${location.search}#${encodeURIComponent(view.id)}`;
    if (location.hash) history.pushState(null, "", url);
    else history.replaceState(null, "", url);
  }, [view]);

  useEffect(() => {
    if (!schematic) return;
    const follow = () => {
      const id = decodeURIComponent(location.hash.slice(1)) || schematic.views[0].id;
      setStack((s) => (s[s.length - 1] === id ? s : pathTo(schematic, id)));
    };
    window.addEventListener("hashchange", follow);
    window.addEventListener("popstate", follow);
    return () => {
      window.removeEventListener("hashchange", follow);
      window.removeEventListener("popstate", follow);
    };
  }, [schematic]);

  // A new level starts with nothing selected or hovered, its adapters folded and no walk-through
  // open, unless the address asked for one.
  const [shownView, setShownView] = useState(view?.id);
  if (shownView !== view?.id) {
    setShownView(view?.id);
    setSelection(null);
    setHoverNode(null);
    setHoverEdge(null);
    setOpened(new Set());
    setPlaying(false);
    const a = askedWalk.current;
    setWalk(a && a.view === view?.id ? a.walk : null);
    askedWalk.current = null;
  }

  const startShown = !!view?.start && !walk;
  const walking = walk !== null;
  const fit = useMemo(() => fitOptions(startShown, walking), [startShown, walking]);
  const fitKey = `${drawKey}|${view?.start && !walk ? "start" : ""}|${walk ? "walk" : ""}`;

  // A new level settles into view: it fades in while the viewport eases from slightly further out.
  const settle = useCallback(
    async (nodes: { id: string }[] | undefined, animate: boolean, fit: FitViewOptions) => {
      if (!animate) {
        // The same level drawn again (adapters shown or folded, a walk-through opened) eases over.
        await rf.fitView({ ...fit, nodes, duration: 380 });
        return;
      }
      await rf.fitView({ ...fit, nodes, duration: 0 });
      const target = rf.getViewport();
      const el = canvasRef.current;
      if (!animate || !el) return;
      const k = 0.93;
      const cx = el.clientWidth / 2;
      const cy = el.clientHeight / 2;
      await rf.setViewport({
        x: cx - (cx - target.x) * k,
        y: cy - (cy - target.y) * k,
        zoom: target.zoom * k,
      });
      el.querySelector(".react-flow__viewport")?.animate([{ opacity: 0 }, { opacity: 1 }], {
        duration: 320,
        easing: "ease-out",
      });
      await rf.setViewport(target, { duration: 480 });
    },
    [rf],
  );

  const lastFitted = useRef<string>("");
  useEffect(() => {
    if (!current || !view) return;
    const viewChanged = lastFitted.current.split("|")[0] !== view.id;
    if (lastFitted.current === fitKey) return;
    lastFitted.current = fitKey;
    const pkgId = focusPkg ? `pkg:${focusPkg}` : null;
    const target = pkgId && current.nodes.some((n) => n.id === pkgId) ? [{ id: pkgId }] : undefined;
    let frame = requestAnimationFrame(() => {
      frame = requestAnimationFrame(() => {
        void settle(target, viewChanged, fit);
        if (target && pkgId) setSelection({ type: "node", id: pkgId });
        setFocusPkg(null);
      });
    });
    return () => cancelAnimationFrame(frame);
  }, [current, view, fitKey, fit, focusPkg, settle]);

  // fold shows or folds the adapters behind a host card, and behind every host sharing one.
  const fold = useCallback(
    (id: string) => {
      if (!view) return;
      const group = hostGroup(view, id);
      setOpened((o) => {
        const next = new Set(o);
        const close = o.has(id);
        for (const h of group) close ? next.delete(h) : next.add(h);
        return next;
      });
    },
    [view],
  );

  // select picks a card or a connection; a card folded away shows first, and a host card clicked
  // shows the adapters folded behind it.
  const select = useCallback(
    (s: Selection) => {
      if (s?.type === "node" && view && folding) {
        if (folding.hidden.has(s.id)) {
          const behind = foldedBehind(view);
          const host = [...behind].find(([, adapters]) => adapters.includes(s.id))?.[0];
          if (host) setOpened((o) => new Set([...o, ...hostGroup(view, host)]));
        } else if (folding.folds.get(s.id)?.open === false) {
          setOpened((o) => new Set([...o, ...hostGroup(view, s.id)]));
        }
      }
      setSelection(s);
    },
    [view, folding],
  );

  const startWalk = useCallback((w: Walk | null, play = false) => {
    setWalk(w);
    setPlaying(play && w !== null);
  }, []);

  // A played walk-through moves on by itself after each caption has had time to be read.
  useEffect(() => {
    if (!playing || !walk || !view || !step) return;
    const timer = setTimeout(() => {
      const next = walkAfter(view, walk);
      if (next) setWalk(next);
      else setPlaying(false);
    }, stepMillis(step.caption));
    return () => clearTimeout(timer);
  }, [playing, walk, view, step]);

  const open = useCallback(
    (id: string) => {
      if (!view || !schematic) return;
      const n = view.nodes?.find((x) => x.id === id);
      if (!n?.drill || n.context) return;
      const target = schematic.views.find((v) => v.id === n.drill);
      if (!target) return;
      setFocusPkg(target.type === "classes" ? (n.focus ?? null) : null);
      setStack((s) => [...s, target.id]);
    },
    [view, schematic],
  );

  const openTypes = useCallback(() => {
    const types = view?.classes;
    if (!types) return;
    setFocusPkg(null);
    setStack((s) => [...s, types]);
  }, [view]);

  const goUp = useCallback(() => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s)), []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) return;
      if (walk && view && (e.key === "ArrowRight" || e.key === "ArrowLeft")) {
        const w = e.key === "ArrowRight" ? walkAfter(view, walk) : walkBefore(view, walk);
        if (w) setWalk(w);
        setPlaying(false);
      } else if (walk && e.key === " ") {
        e.preventDefault();
        setPlaying((p) => !p);
      } else if (e.key === "Escape") {
        if (selection) setSelection(null);
        else if (walk) startWalk(null);
        else goUp();
      } else if (e.key === "Backspace") {
        e.preventDefault();
        goUp();
      } else if (e.key === "Enter" && selection?.type === "node") {
        open(selection.id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [selection, goUp, open, walk, view, startWalk]);

  const graph = useMemo(() => {
    if (!current) return { nodes: [] as Node[], edges: [] as Edge[] };
    const nodes: Node[] = current.nodes.map((n) => {
      const data: NodeData = { model: n.model, kind: n.kind, onOpen: open, onFold: fold };
      return {
        id: n.id,
        type: n.model.variant === "group" ? "box" : n.model.variant === "class" ? "cls" : "card",
        position: { x: n.x, y: n.y },
        parentId: n.parent,
        data,
        width: n.width,
        height: n.height,
        style: { width: n.width, height: n.height },
        draggable: false,
        selectable: false,
        connectable: false,
        focusable: false,
      };
    });
    const edges: Edge[] = current.edges.map((e) => ({
      id: e.id,
      source: e.from,
      target: e.to,
      type: "routed",
      data: { edge: e },
      // Above every group box; ELK keeps routes clear of the cards themselves.
      zIndex: 1000,
      selectable: false,
      focusable: false,
    }));
    return { nodes, edges };
  }, [current, open, fold]);

  const focus: Focus = useMemo(() => {
    const base = {
      selectedNode: selection?.type === "node" ? selection.id : null,
      selectedEdge: selection?.type === "edge" ? selection.id : null,
      hoveredEdge: hoverEdge,
      allLabels: askedLabels,
      hoverEdge: setHoverEdge,
      selectEdge: (id: string) => setSelection({ type: "edge", id }),
    };
    const none = { ...base, nodes: null, edges: null };
    if (!current) return none;
    // A walk-through's step lights its own cards and connections, whatever the pointer is on.
    if (step) return { ...base, nodes: new Set(step.nodes), edges: new Set(step.edges) };
    const edgeId = hoverEdge ?? (hoverNode ? null : base.selectedEdge);
    if (edgeId) {
      const e = current.edges.find((x) => x.id === edgeId);
      if (e) return { ...base, nodes: new Set([e.from, e.to]), edges: new Set([e.id]) };
    }
    const nodeId = hoverNode ?? base.selectedNode;
    if (!nodeId) return none;
    const n = current.nodes.find((x) => x.id === nodeId);
    if (!n) return none;
    // A group lights what's inside it; a card or a type lights itself and its neighbours.
    const inside = new Set<string>([nodeId]);
    if (n.model.variant === "group") {
      let grew = true;
      while (grew) {
        grew = false;
        for (const c of current.nodes) {
          if (c.parent && inside.has(c.parent) && !inside.has(c.id)) {
            inside.add(c.id);
            grew = true;
          }
        }
      }
    }
    const nodes = new Set(inside);
    const edges = new Set<string>();
    for (const e of current.edges) {
      if (inside.has(e.from) || inside.has(e.to)) {
        edges.add(e.id);
        nodes.add(e.from);
        nodes.add(e.to);
      }
    }
    return { ...base, nodes, edges };
  }, [current, selection, hoverNode, hoverEdge, step]);

  const fitAll = useCallback(() => void rf.fitView({ ...fit, duration: 420 }), [rf, fit]);
  const hasFolds = !!view && view.type !== "classes" && foldedBehind(view).size > 0;

  if (error) return <div className="failed">{error}</div>;
  if (!schematic || !view) return <div className="loading">Loading the schematic…</div>;

  return (
    <div className="app">
      <header className="topbar">
        <div className="brand">
          <Logo />
          <span className="brand-name">voice-guide</span>
          <span className="brand-sub">schematic</span>
        </div>
        <nav className="crumbs" aria-label="Levels">
          {stack.map((id, i) => {
            const v = schematic.views.find((x) => x.id === id);
            const last = i === stack.length - 1;
            return (
              <Fragment key={id}>
                {i > 0 && (
                  <span className="crumb-sep">
                    <Icon name="chevron" size={12} />
                  </span>
                )}
                <button
                  type="button"
                  className={last ? "crumb current" : "crumb"}
                  disabled={last}
                  onClick={() => setStack(stack.slice(0, i + 1))}
                >
                  <span className="crumb-level">{v?.level}</span>
                  {v?.title ?? id}
                </button>
              </Fragment>
            );
          })}
        </nav>
        <div className="topbar-right">
          <span className="hint">
            <kbd>double-click</kbd> open <kbd>Esc</kbd> up
          </span>
          <button
            type="button"
            className="icon-button"
            onClick={toggleTheme}
            title={theme === "light" ? "Dark theme" : "Light theme"}
            aria-label={theme === "light" ? "Dark theme" : "Light theme"}
          >
            <Icon name={theme === "light" ? "moon" : "sun"} size={15} />
          </button>
        </div>
      </header>

      <main className="main">
        <div className="canvas" ref={canvasRef}>
          <FocusContext.Provider value={focus}>
            <ReactFlow
              nodes={graph.nodes}
              edges={graph.edges}
              nodeTypes={nodeTypes}
              edgeTypes={edgeTypes}
              nodesDraggable={false}
              nodesConnectable={false}
              elementsSelectable={false}
              zoomOnDoubleClick={false}
              minZoom={0.1}
              maxZoom={2.2}
              proOptions={{ hideAttribution: true }}
              onNodeMouseEnter={(_, n) => n.type !== "box" && setHoverNode(n.id)}
              onNodeMouseLeave={(_, n) => setHoverNode((h) => (h === n.id ? null : h))}
              onNodeClick={(_, n) => select({ type: "node", id: n.id })}
              onNodeDoubleClick={(_, n) => open(n.id)}
              onEdgeMouseEnter={(_, e) => setHoverEdge(e.id)}
              onEdgeMouseLeave={() => setHoverEdge(null)}
              onEdgeClick={(_, e) => setSelection({ type: "edge", id: e.id })}
              onPaneClick={() => setSelection(null)}
            >
              <Background variant={BackgroundVariant.Dots} gap={22} size={1.3} />
            </ReactFlow>
          </FocusContext.Provider>

          <div className="canvas-top">
            <div className="view-title">
              <span className="level-pill">Level {view.level}</span>
              {view.title}
            </div>
            {view.classes && (
              <button type="button" className="toolbar-button" onClick={openTypes}>
                <Icon name="code" size={14} />
                Types from the code
              </button>
            )}
            {hasFolds && (
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={showAll}
                  onChange={(e) => setShowAll(e.target.checked)}
                />
                <span className="switch" />
                All adapters
              </label>
            )}
            {isClasses && (
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={unexported}
                  onChange={(e) => setUnexported(e.target.checked)}
                />
                <span className="switch" />
                Unexported members
              </label>
            )}
          </div>

          {view.start && !walk && <p className="start-here">{view.start}</p>}

          {flow && step && walk ? (
            <FlowBar
              flow={flow}
              step={walk.step}
              playing={playing}
              hasNext={!!walkAfter(view, walk)}
              hasBack={!!walkBefore(view, walk)}
              onNext={() => {
                const w = walkAfter(view, walk);
                if (w) setWalk(w);
                setPlaying(false);
              }}
              onBack={() => {
                const w = walkBefore(view, walk);
                if (w) setWalk(w);
                setPlaying(false);
              }}
              onPlay={() => setPlaying((p) => !p)}
              onClose={() => startWalk(null)}
            />
          ) : (
            <Legend view={view} drawn={current} />
          )}

          <div className="zoom">
            <button
              type="button"
              title="Zoom in"
              aria-label="Zoom in"
              onClick={() => void rf.zoomIn({ duration: 200 })}
            >
              <Icon name="plus" size={14} />
            </button>
            <button
              type="button"
              title="Zoom out"
              aria-label="Zoom out"
              onClick={() => void rf.zoomOut({ duration: 200 })}
            >
              <Icon name="minus" size={14} />
            </button>
            <button type="button" title="Fit" aria-label="Fit" onClick={fitAll}>
              <Icon name="fit" size={14} />
            </button>
          </div>
        </div>

        <Panel
          schematic={schematic}
          view={view}
          nodes={current?.nodes ?? []}
          edges={current?.edges ?? []}
          classes={classData}
          selection={selection}
          onSelect={select}
          onOpen={open}
          walk={walk}
          playing={playing}
          onWalk={startWalk}
        />
      </main>
    </div>
  );
}

function LineSample({ kind }: { kind: "flow" | "implements" | "holds" | "embeds" }) {
  const dash = kind === "implements" ? "5 4" : kind === "embeds" ? "1.5 3.5" : undefined;
  return (
    <svg className="legend-line" width="34" height="10" viewBox="0 0 34 10" aria-hidden="true">
      <path
        d="M1 5h24"
        stroke="var(--edge-lit)"
        strokeWidth="1.4"
        strokeDasharray={dash}
        strokeLinecap="round"
        fill="none"
      />
      {kind === "holds" ? (
        <path
          d="M27 1.5 32 5l-5 3.5"
          stroke="var(--edge-lit)"
          strokeWidth="1.4"
          fill="none"
          strokeLinejoin="round"
        />
      ) : kind === "implements" ? (
        <path
          d="M33 5 25 0.8v8.4Z"
          stroke="var(--edge-lit)"
          strokeWidth="1.3"
          fill="var(--surface)"
          strokeLinejoin="round"
        />
      ) : (
        <path d="M33 5 26 1.5v7Z" fill="var(--edge-lit)" />
      )}
    </svg>
  );
}

// Legend shows the kinds and line styles the current level actually uses.
function Legend({ view, drawn }: { view: View; drawn: Drawn | null }) {
  if (!drawn) return null;
  if (view.type === "classes") {
    const rels = new Set(drawn.edges.map((e) => e.style));
    return (
      <div className="legend">
        <span className="legend-item">
          <span
            className="legend-swatch"
            style={{ "--accent": kindColor("port") } as React.CSSProperties}
          />
          Interface
        </span>
        <span className="legend-item">
          <span
            className="legend-swatch"
            style={{ "--accent": kindColor("adapter") } as React.CSSProperties}
          />
          Struct or type
        </span>
        <span className="legend-sep" />
        {(["implements", "holds", "embeds"] as const)
          .filter((k) => rels.has(k))
          .map((k) => (
            <span key={k} className="legend-item">
              <LineSample kind={k} />
              {k}
            </span>
          ))}
      </div>
    );
  }
  const kinds = new Set<Kind>(
    drawn.nodes.filter((n) => n.model.variant !== "group").map((n) => n.kind),
  );
  const styles = new Set(drawn.edges.map((e) => e.style));
  return (
    <div className="legend">
      {LEGEND_ORDER.filter((k) => kinds.has(k)).map((k) => (
        <span key={k} className="legend-item">
          <span
            className="legend-swatch"
            style={{ "--accent": kindColor(k) } as React.CSSProperties}
          />
          {KINDS[k].legend}
        </span>
      ))}
      {styles.size > 0 && <span className="legend-sep" />}
      {styles.has("flow") && (
        <span className="legend-item">
          <LineSample kind="flow" />
          calls, with what flows
        </span>
      )}
      {styles.has("implements") && (
        <span className="legend-item">
          <LineSample kind="implements" />
          implements
        </span>
      )}
    </div>
  );
}
