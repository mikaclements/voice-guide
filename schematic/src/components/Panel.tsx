import type { ReactNode } from "react";
import { foldedBehind } from "../fold.ts";
import type { ClassModel, DrawnEdge, DrawnNode, GroupModel } from "../graph.ts";
import { packageLabel, settingWhere } from "../graph.ts";
import { Icon, KindIcon } from "../icons.tsx";
import { KINDS, kindColor } from "../kinds.ts";
import type { ClassData, ClassType, Schematic, View } from "../types.ts";
import type { Walk } from "../walk.ts";

export type Selection = { type: "node"; id: string } | { type: "edge"; id: string } | null;

interface Props {
  schematic: Schematic;
  view: View;
  nodes: DrawnNode[];
  edges: DrawnEdge[];
  classes?: ClassData;
  selection: Selection;
  onSelect: (s: Selection) => void;
  onOpen: (nodeId: string) => void;
  walk: Walk | null;
  playing: boolean;
  onWalk: (w: Walk | null, play?: boolean) => void;
}

// Decisions lists, folded away, the design decisions behind a level, a card or a connection, each
// linking to where the design makes it.
function Decisions({ ids, schematic }: { ids: string[]; schematic: Schematic }) {
  const list = ids.map((id) => schematic.considerations[id]).filter(Boolean);
  if (!list.length) return null;
  return (
    <details className="decisions">
      <summary>
        Decisions <span className="count">{list.length}</span>
      </summary>
      <ul>
        {list.map((c) => (
          <li key={c.title}>
            <a
              href={`${schematic.design}#${c.ref?.split("#")[1] ?? ""}`}
              target="_blank"
              rel="noreferrer"
            >
              {c.title}
            </a>
            <p>{c.decision}</p>
          </li>
        ))}
      </ul>
    </details>
  );
}

// Walks lists a level's walk-throughs; the one open lists its steps, each opened by a click.
function Walks({
  view,
  walk,
  playing,
  onWalk,
}: {
  view: View;
  walk: Walk | null;
  playing: boolean;
  onWalk: (w: Walk | null, play?: boolean) => void;
}) {
  const flows = view.flows ?? [];
  if (!flows.length) return null;
  return (
    <section className="panel-section">
      <h3>
        Walk-throughs
        <button
          type="button"
          className="play-all"
          onClick={() =>
            playing ? onWalk(walk, false) : onWalk(walk ?? { flow: flows[0].id, step: 0 }, true)
          }
        >
          <Icon name={playing ? "pause" : "play"} size={11} />
          {playing ? "Pause" : walk ? "Play on" : "Play all"}
        </button>
      </h3>
      <div className="walks">
        {flows.map((f) => {
          const on = walk?.flow === f.id;
          return (
            <div key={f.id} className={on ? "walk on" : "walk"}>
              <button
                type="button"
                className="walk-head"
                onClick={() => onWalk(on ? null : { flow: f.id, step: 0 })}
              >
                <span className="walk-icon">
                  <Icon name={on ? "close" : "play"} size={11} />
                </span>
                {f.name}
                <span className="count">{f.steps.length}</span>
              </button>
              {on && (
                <ol className="walk-steps">
                  {f.steps.map((st, i) => (
                    <li key={st.caption} className={walk?.step === i ? "on" : ""}>
                      <button type="button" onClick={() => onWalk({ flow: f.id, step: i })}>
                        {st.caption}
                      </button>
                    </li>
                  ))}
                </ol>
              )}
            </div>
          );
        })}
      </div>
    </section>
  );
}

function nameOf(n: DrawnNode | undefined): string {
  if (!n) return "";
  const m = n.model;
  if (m.variant === "group") return m.title;
  if (m.variant === "class")
    return m.type.external ? `${m.type.package.split("/").pop()}.${m.type.name}` : m.type.name;
  return m.node.name;
}

function FlowItem({
  edge,
  peer,
  dir,
  onClick,
}: {
  edge: DrawnEdge;
  peer?: DrawnNode;
  dir: "in" | "out";
  onClick: () => void;
}) {
  return (
    <button type="button" className="flow" onClick={onClick}>
      <div className="flow-head">
        <span className="flow-dir">
          <Icon name={dir === "in" ? "arrowRight" : "arrowRight"} size={12} />
        </span>
        <span
          className="flow-peer"
          style={{ "--accent": kindColor(peer?.kind) } as React.CSSProperties}
        >
          <span className="dot" />
          {dir === "in" ? `from ${nameOf(peer)}` : `to ${nameOf(peer)}`}
        </span>
        {edge.chip && <span className="chip">{edge.chip}</span>}
      </div>
      {edge.edge?.send && <div className="flow-send">{edge.edge.send}</div>}
      {edge.edge?.back && (
        <div className="flow-back">
          <span>←</span> {edge.edge.back}
        </div>
      )}
    </button>
  );
}

function Header({
  eyebrow,
  title,
  sub,
  kind,
  children,
}: {
  eyebrow: ReactNode;
  title: string;
  sub?: ReactNode;
  kind?: string;
  children?: ReactNode;
}) {
  return (
    <header
      className="panel-header"
      style={
        { "--accent": kind ? kindColor(kind as never) : "var(--muted)" } as React.CSSProperties
      }
    >
      <div className="panel-eyebrow">{eyebrow}</div>
      <h2>{title}</h2>
      {sub && <div className="panel-sub">{sub}</div>}
      {children}
    </header>
  );
}

function Overview({
  view,
  schematic,
  classes,
  walk,
  playing,
  onWalk,
}: {
  view: View;
  schematic: Schematic;
  classes?: ClassData;
  walk: Walk | null;
  playing: boolean;
  onWalk: (w: Walk | null, play?: boolean) => void;
}) {
  return (
    <>
      <Header
        eyebrow={
          <>
            <span className="level-pill">Level {view.level}</span>
            {view.type === "classes" ? "Generated from the code" : "Hand-written from the design"}
          </>
        }
        title={view.title}
      >
        <p className="lead">{view.lead}</p>
        {classes && (
          <div className="generated">
            <Icon name="code" size={13} />
            <span>
              {classes.types.filter((t) => !t.external).length} types in {classes.packages.length}{" "}
              packages · {classes.relations.length} relationships · generated{" "}
              {new Date(classes.generated).toLocaleString()}
            </span>
          </div>
        )}
      </Header>
      <Walks view={view} walk={walk} playing={playing} onWalk={onWalk} />
      <Decisions ids={view.considerations} schematic={schematic} />
    </>
  );
}

function ClassDetail({
  t,
  classes,
  nodes,
  onSelect,
}: {
  t: ClassType;
  classes?: ClassData;
  nodes: DrawnNode[];
  onSelect: (s: Selection) => void;
}) {
  const rels = classes?.relations ?? [];
  const byId = new Map((classes?.types ?? []).map((x) => [x.id, x]));
  const shown = new Set(nodes.map((n) => n.id));
  const link = (id: string) => {
    const x = byId.get(id);
    const label = x
      ? x.package === t.package
        ? x.name
        : `${x.package.split("/").pop()}.${x.name}`
      : id;
    return shown.has(id) ? (
      <button type="button" className="link" onClick={() => onSelect({ type: "node", id })}>
        {label}
      </button>
    ) : (
      <span className="link off">{label}</span>
    );
  };
  // Each relationship as the related type's ID and how it's shown.
  const groups: [string, [string, ReactNode][]][] = [
    [
      "Implements",
      rels.filter((r) => r.from === t.id && r.kind === "implements").map((r) => [r.to, link(r.to)]),
    ],
    [
      "Implemented by",
      rels
        .filter((r) => r.to === t.id && r.kind === "implements")
        .map((r) => [r.from, link(r.from)]),
    ],
    [
      "Holds",
      rels
        .filter((r) => r.from === t.id && r.kind !== "implements")
        .map((r) => [
          r.to,
          <>
            {link(r.to)} <span className="via">via {(r.via ?? []).join(", ") || r.kind}</span>
          </>,
        ]),
    ],
    [
      "Held by",
      rels
        .filter((r) => r.to === t.id && r.kind !== "implements")
        .map((r) => [r.from, link(r.from)]),
    ],
  ];
  return (
    <>
      {t.doc && <p className="doc">{t.doc}</p>}
      {t.file && (
        <div className="source">
          <Icon name="code" size={13} /> {t.file}:{t.line}
        </div>
      )}
      {groups.some(([, l]) => l.length) && (
        <section className="panel-section">
          <h3>Relationships</h3>
          <div className="rels">
            {groups
              .filter(([, l]) => l.length)
              .map(([k, l]) => (
                <div key={k} className="rel-row">
                  <span className="rel-kind">{k}</span>
                  <span className="rel-list">
                    {l.map(([id, x]) => (
                      <span key={id}>{x}</span>
                    ))}
                  </span>
                </div>
              ))}
          </div>
        </section>
      )}
      {t.constructors && t.constructors.length > 0 && (
        <section className="panel-section">
          <h3>Constructors</h3>
          <div className="members">
            {t.constructors.map((c) => (
              <div key={c.name} className="member">
                <code>
                  <b>{c.name}</b>
                  {c.signature}
                </code>
              </div>
            ))}
          </div>
        </section>
      )}
      {t.fields.length > 0 && (
        <section className="panel-section">
          <h3>
            {t.kind === "interface" ? "Embeds" : "Fields"}
            <span className="count">{t.fields.length}</span>
          </h3>
          <div className="members">
            {t.fields.map((f) => (
              <div key={f.name} className={f.exported ? "member" : "member muted"}>
                <code>
                  {!f.embedded && <b>{f.name} </b>}
                  {f.type}
                </code>
                {f.doc && <p>{f.doc}</p>}
              </div>
            ))}
          </div>
        </section>
      )}
      {t.methods.length > 0 && (
        <section className="panel-section">
          <h3>
            Methods<span className="count">{t.methods.length}</span>
          </h3>
          <div className="members">
            {[...t.methods]
              .sort((a, b) => Number(b.exported) - Number(a.exported))
              .map((m) => (
                <div key={m.name} className={m.exported ? "member" : "member muted"}>
                  <code>
                    <b>{m.name}</b>
                    {m.signature}
                  </code>
                  {m.doc && <p>{m.doc}</p>}
                </div>
              ))}
          </div>
        </section>
      )}
    </>
  );
}

export function Panel({
  schematic,
  view,
  nodes,
  edges,
  classes,
  selection,
  onSelect,
  onOpen,
  walk,
  playing,
  onWalk,
}: Props) {
  const byId = new Map(nodes.map((n) => [n.id, n]));
  let body: ReactNode;

  if (selection?.type === "edge") {
    const e = edges.find((x) => x.id === selection.id);
    const from = e && byId.get(e.from);
    const to = e && byId.get(e.to);
    if (e) {
      const rel = e.relation;
      body = (
        <>
          <Header
            eyebrow={
              <>
                {e.chip && <span className="chip">{e.chip}</span>}
                {rel ? rel.kind : e.style === "implements" ? "implements" : "Connection"}
              </>
            }
            title={`${nameOf(from)} → ${nameOf(to)}`}
          >
            {e.edge?.send && (
              <div className="flow-big">
                <span className="flow-big-dir">sends</span>
                <span>{e.edge.send}</span>
              </div>
            )}
            {e.edge?.back && (
              <div className="flow-big">
                <span className="flow-big-dir">gets back</span>
                <span>{e.edge.back}</span>
              </div>
            )}
            {e.edge?.detail && <p className="lead">{e.edge.detail}</p>}
            {e.style === "implements" && !rel && (
              <p className="lead">
                {nameOf(from)} implements the {nameOf(to)} port: the core depends on the interface,
                and the entry point chooses this implementation by setting.
              </p>
            )}
            {rel && (
              <p className="lead">
                {rel.kind === "implements"
                  ? `${nameOf(from)} satisfies ${nameOf(to)}${rel.pointer ? " through its pointer type" : ""}, found by go/types.`
                  : rel.kind === "embeds"
                    ? `${nameOf(from)} embeds ${nameOf(to)}, so its fields and methods are promoted.`
                    : `${nameOf(from)} holds ${rel.many ? "several values of " : ""}${nameOf(to)} in ${(rel.via ?? []).join(", ")}.`}
              </p>
            )}
          </Header>
          <div className="panel-ends">
            {(
              [
                ["from", from],
                ["to", to],
              ] as const
            ).map(
              ([role, n]) =>
                n && (
                  <button
                    key={role}
                    type="button"
                    className="end"
                    onClick={() => onSelect({ type: "node", id: n.id })}
                  >
                    <span
                      className="end-icon"
                      style={{ "--accent": kindColor(n.kind) } as React.CSSProperties}
                    >
                      <KindIcon kind={n.kind === "group" ? "module" : n.kind} size={13} />
                    </span>
                    <span className="end-role">{role}</span>
                    <span className="end-name">{nameOf(n)}</span>
                    <Icon name="chevron" size={13} />
                  </button>
                ),
            )}
          </div>
          <Decisions ids={e.edge?.considerations ?? []} schematic={schematic} />
        </>
      );
    }
  } else if (selection?.type === "node") {
    const n = byId.get(selection.id);
    if (n && (n.model.variant === "card" || n.model.variant === "ghost")) {
      const sn = n.model.node;
      const incoming = edges.filter((e) => e.to === n.id && e.style !== "implements");
      const outgoing = edges.filter((e) => e.from === n.id && e.style !== "implements");
      // From the level's data, not the drawing, so adapters folded away are listed too.
      const named = new Map((view.nodes ?? []).map((x) => [x.id, x.name]));
      const implementsList = (view.edges ?? [])
        .filter((e) => e.style === "implements" && e.from === n.id)
        .map((e) => e.to);
      const adapters = foldedBehind(view).get(n.id) ?? [];
      const kind = sn.kind === "group" ? "module" : sn.kind;
      body = (
        <>
          <Header
            kind={kind}
            eyebrow={
              <>
                <span className="eyebrow-icon">
                  <KindIcon kind={kind} size={13} />
                </span>
                {[KINDS[kind].label, sn.tag].filter(Boolean).join(" · ")}
                {sn.context && <span className="outside">outside this level</span>}
              </>
            }
            title={sn.name}
            sub={
              <>
                {sn.path && <code>{sn.path}</code>}
                {sn.tech && <span>{sn.tech}</span>}
              </>
            }
          >
            {sn.setting && (
              <div className="panel-setting">
                <code>
                  {/* Each choice kept whole, so none breaks at a hyphen. */}
                  {sn.setting.split(" · ").map((c, i) => (
                    <span key={c}>
                      {i > 0 && " · "}
                      <span className="nowrap">{c}</span>
                    </span>
                  ))}
                </code>
                <p>{settingWhere(sn.setting)}</p>
              </div>
            )}
            {sn.why && (
              <p className="why">
                <strong className="why-label">Why this way.</strong> {sn.why}
              </p>
            )}
            {(sn.description || sn.summary) && (
              <p className="lead">{sn.description ?? sn.summary}</p>
            )}
            {sn.members && sn.members.length > 0 && (
              <div className="port-members">
                {sn.members.map((m) => (
                  <code key={m}>{m}</code>
                ))}
              </div>
            )}
            {sn.drill && !sn.context && (
              <button type="button" className="open" onClick={() => onOpen(n.id)}>
                <Icon
                  name={
                    schematic.views.find((v) => v.id === sn.drill)?.type === "classes"
                      ? "code"
                      : "layers"
                  }
                  size={14}
                />
                {schematic.views.find((v) => v.id === sn.drill)?.type === "classes"
                  ? "Show its types, from the code"
                  : `Open ${schematic.views.find((v) => v.id === sn.drill)?.title ?? sn.drill}`}
                <Icon name="chevron" size={14} />
              </button>
            )}
          </Header>
          {(incoming.length > 0 || outgoing.length > 0) && (
            <section className="panel-section">
              <h3>What flows</h3>
              <div className="flows">
                {incoming.map((e) => (
                  <FlowItem
                    key={e.id}
                    edge={e}
                    peer={byId.get(e.from)}
                    dir="in"
                    onClick={() => onSelect({ type: "edge", id: e.id })}
                  />
                ))}
                {outgoing.map((e) => (
                  <FlowItem
                    key={e.id}
                    edge={e}
                    peer={byId.get(e.to)}
                    dir="out"
                    onClick={() => onSelect({ type: "edge", id: e.id })}
                  />
                ))}
              </div>
            </section>
          )}
          {(implementsList.length > 0 || adapters.length > 0) && (
            <section className="panel-section">
              <h3>{adapters.length ? "Adapters" : "Implements"}</h3>
              <div className="chips">
                {[...adapters, ...implementsList].map((id) => (
                  <button
                    key={id}
                    type="button"
                    className="tag"
                    onClick={() => onSelect({ type: "node", id })}
                  >
                    {named.get(id) ?? id}
                  </button>
                ))}
              </div>
            </section>
          )}
          <Decisions ids={sn.considerations ?? []} schematic={schematic} />
        </>
      );
    } else if (n && n.model.variant === "class") {
      const t = (n.model as ClassModel).type;
      body = (
        <>
          <Header
            kind={t.kind === "interface" ? "port" : "adapter"}
            eyebrow={
              <>
                <span className={`badge badge-${t.kind}`}>{t.kind}</span>
                {t.external ? "outside these packages" : packageLabel(t.package, view.root ?? "")}
                {!t.exported && <span className="outside">unexported</span>}
              </>
            }
            title={t.name}
            sub={<code>{t.package}</code>}
          />
          <ClassDetail t={t} classes={classes} nodes={nodes} onSelect={onSelect} />
        </>
      );
    } else if (n && n.model.variant === "group") {
      const g = n.model as GroupModel;
      const pkg = classes?.packages.find((p) => p.path === g.pkg);
      const inside = nodes.filter((x) => x.parent === n.id);
      body = (
        <>
          <Header
            eyebrow={g.eyebrow}
            title={g.title}
            sub={g.path && <code>{g.path}</code>}
            kind={g.accent}
          >
            {pkg?.doc && <p className="lead">{pkg.doc}</p>}
          </Header>
          {inside.length > 0 && (
            <section className="panel-section">
              <h3>
                Inside<span className="count">{inside.length}</span>
              </h3>
              <div className="chips">
                {inside.map((x) => (
                  <button
                    key={x.id}
                    type="button"
                    className="tag"
                    onClick={() => onSelect({ type: "node", id: x.id })}
                  >
                    {nameOf(x)}
                  </button>
                ))}
              </div>
            </section>
          )}
        </>
      );
    }
  }

  return (
    <aside className="panel">
      {selection && body && (
        <div className="panel-nav">
          <button type="button" className="back" onClick={() => onSelect(null)}>
            <Icon name="back" size={14} />
            {view.title}
          </button>
        </div>
      )}
      <div className="panel-scroll" key={selection ? `${selection.type}:${selection.id}` : view.id}>
        {body ?? (
          <Overview
            view={view}
            schematic={schematic}
            classes={classes}
            walk={walk}
            playing={playing}
            onWalk={onWalk}
          />
        )}
      </div>
    </aside>
  );
}
