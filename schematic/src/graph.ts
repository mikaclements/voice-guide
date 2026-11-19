// Turns a view of the schematic, or a generated class view, into sized nodes and labelled edges,
// lays them out with ELK and returns what the canvas draws.
import { KINDS } from "./kinds.ts";
import { baseOptions, layout, type Point, type SpecEdge, type SpecNode } from "./layout.ts";
import { clampLines, textWidth, truncate, wrap } from "./measure.ts";
import type {
  ClassData,
  ClassMethod,
  ClassRelation,
  ClassType,
  Kind,
  Schematic,
  SchematicEdge,
  SchematicNode,
  View,
} from "./types.ts";

// ---- the models the canvas renders -------------------------------------------------------------

export interface CardModel {
  variant: "card" | "ghost";
  node: SchematicNode;
  eyebrow: string;
  path?: string;
  tech?: string;
  // The setting that switches it, in lines that fit its chip.
  setting: string[];
  summary: string[];
  members: string[];
  footer?: string;
  // Adapters folded behind this card: how many, and whether they show.
  fold?: { count: number; open: boolean };
}

export interface GroupModel {
  variant: "group";
  title: string;
  eyebrow: string;
  path?: string;
  accent: Kind;
  external?: boolean;
  node?: SchematicNode;
  pkg?: string;
}

export interface MemberLine {
  name: string;
  rest: string;
  exported: boolean;
  related?: boolean;
  full: string;
}

export interface ClassModel {
  variant: "class";
  type: ClassType;
  fields: MemberLine[];
  methods: MemberLine[];
  hidden: number;
  width: number;
}

export type NodeModel = CardModel | GroupModel | ClassModel;

export interface EdgeModel {
  id: string;
  from: string;
  to: string;
  style: "flow" | "implements" | "holds" | "embeds";
  chip?: string;
  send: string[];
  back: string[];
  mono?: boolean;
  edge?: SchematicEdge;
  relation?: ClassRelation;
}

export interface DrawnNode {
  id: string;
  parent?: string;
  x: number;
  y: number;
  absX: number;
  absY: number;
  width: number;
  height: number;
  model: NodeModel;
  kind: Kind;
}

export interface DrawnEdge extends EdgeModel {
  points: Point[];
  label?: { x: number; y: number; width: number; height: number };
}

export interface Drawn {
  nodes: DrawnNode[];
  edges: DrawnEdge[];
  width: number;
  height: number;
}

// The canvas at 1440×900 less the side panel, the toolbar and the legend: what a level is laid out
// to fill.
const CANVAS = { width: 1010, height: 710 };

// ---- metrics, shared with the CSS ---------------------------------------------------------------

export const CARD = {
  width: 256,
  ghostWidth: 206,
  maxWidth: 320,
  pad: 14,
  head: 32,
  metaGap: 8,
  metaLine: 16,
  summaryGap: 10,
  summaryLine: 18,
  summarySize: 12.5,
  memberGap: 10,
  memberLine: 18,
  footerGap: 12,
  footer: 32,
  settingGap: 8,
  settingLine: 15,
  settingSize: 10.5,
  settingPadX: 6,
  settingPadY: 2,
};

export const LABEL = {
  max: 196,
  size: 11,
  line: 16,
  padX: 8,
  padY: 5,
  chipSize: 9.5,
  chipPad: 5,
  chipGap: 6,
  arrow: 12,
};

export const CLASS = {
  min: 196,
  max: 348,
  pad: 12,
  head: 38,
  line: 18,
  section: 6,
  size: 11.5,
};

// groupHeadWidth is the width a group's header needs for its icon, eyebrow and title; the path
// after them may be cut.
function groupHeadWidth(eyebrow: string, title: string): number {
  const eyebrowW = textWidth(eyebrow.toUpperCase(), 10, 650) + eyebrow.length * 0.6;
  return 16 + 22 + 8 + eyebrowW + 8 + textWidth(title, 13.5, 640) + 16 + 6;
}

// ---- hand-written views ------------------------------------------------------------------------

// settingLines fits a setting into its chip, breaking only between choices so none is split from
// its separator: one line if it fits; else the name, then the choices, if they fit on one line;
// else the name with the first choices, then the rest. Two lines at most, the second cut with an
// ellipsis if it still doesn't fit; the panel shows it whole.
export function settingLines(setting: string, inner: number): string[] {
  const max = inner - CARD.settingPadX * 2 - 2;
  const fits = (t: string) => textWidth(t, CARD.settingSize, 500, true) <= max;
  const cut = (t: string) => truncate(t, max, CARD.settingSize, 500, true);
  if (fits(setting)) return [setting];
  const colon = setting.indexOf(":");
  const name = setting.slice(0, colon + 1);
  const choices = setting.slice(colon + 1).trim();
  if (colon < 0 || !choices) return [cut(setting)];
  if (fits(choices)) return [cut(name), choices];
  const [first, ...rest] = choices.split(" · ");
  let line = `${name} ${first}`;
  while (rest.length && fits(`${line} · ${rest[0]}`)) line += ` · ${rest.shift()}`;
  return rest.length ? [cut(line), cut(`· ${rest.join(" · ")}`)] : [cut(line)];
}

// settingWhere says where a setting is changed: an environment variable, named in upper case, in
// .env; an agent's own setting, in lower case, in its file.
export function settingWhere(setting: string): string {
  const name = setting.split(":")[0];
  return name === name.toUpperCase()
    ? "Set in .env; restart scripts/run.sh after changing it."
    : "Set in the agent file, agents/<name>.md; scripts/run.sh publishes it as a new version.";
}

// The most lines of a port's methods a card shows: four keeps a nine-method port from filling a
// column of the level. A round guess, chosen by eye on the screenshots.
const MEMBERS = 4;

// What drawView needs to fold a level: the cards left out, and for each host card its adapters.
export interface Folding {
  hidden: Set<string>;
  folds: Map<string, { count: number; open: boolean }>;
}

function cardModel(
  n: SchematicNode,
  view: View,
  all: Schematic,
  fold?: { count: number; open: boolean },
): { model: CardModel; width: number; height: number } {
  const ghost = !!n.context;
  // A card widens, up to a limit, so its name fits beside the icon; the 2 is its border.
  const nameRoom = textWidth(n.name, 14, 620) + 32 + 10 + CARD.pad * 2 + 2 + 2;
  const width = Math.ceil(
    Math.min(CARD.maxWidth, Math.max(ghost ? CARD.ghostWidth : CARD.width, nameRoom)),
  );
  const inner = width - CARD.pad * 2 - 2;
  const kind = n.kind === "group" ? "module" : n.kind;
  const eyebrow = [KINDS[kind].label, n.tag].filter(Boolean).join(" · ");
  const summary = n.summary
    ? clampLines(n.summary, inner, ghost ? 2 : 3, ghost ? 12 : CARD.summarySize)
    : [];
  // A port with a long interface shows its first methods and how many more; the panel lists all.
  const methods = n.members ?? [];
  const shown = methods.length > MEMBERS ? methods.slice(0, MEMBERS - 1) : methods;
  const members = shown.map((m) => truncate(m, inner - 14, 11, 400, true));
  if (shown.length < methods.length) members.push(`+ ${methods.length - shown.length} more`);
  let footer: string | undefined;
  // A drill to another hand-written level gets a footer; a drill to the class view doesn't, since
  // nearly every card at level 3 has one: double-click, or the panel's button, opens it.
  const target = n.drill && !ghost ? all.views.find((v) => v.id === n.drill) : undefined;
  if (target && target.type !== "classes") footer = `Open ${target.title}`;
  // A card with adapters folded behind it gets the footer that shows or folds them; no card both
  // opens a hand-written level and has adapters.
  const foldFooter = !footer && fold ? fold : undefined;
  let h = CARD.pad + CARD.head;
  const path = n.path ? truncate(n.path, inner, 11, 400, true) : undefined;
  const tech = n.tech ? truncate(n.tech, inner, 11.5) : undefined;
  const setting = n.setting ? settingLines(n.setting, inner) : [];
  if (path) h += CARD.metaGap + CARD.metaLine;
  if (tech) h += (path ? 2 : CARD.metaGap) + CARD.metaLine;
  // The chip's lines, its padding and its border.
  if (setting.length)
    h += CARD.settingGap + setting.length * CARD.settingLine + CARD.settingPadY * 2 + 2;
  if (summary.length) h += CARD.summaryGap + summary.length * (ghost ? 17 : CARD.summaryLine);
  if (members.length) h += CARD.memberGap + members.length * CARD.memberLine;
  if (footer || foldFooter) h += CARD.footerGap + CARD.footer;
  else h += CARD.pad;
  void view;
  return {
    model: {
      variant: ghost ? "ghost" : "card",
      node: n,
      eyebrow,
      path,
      tech,
      setting,
      summary,
      members,
      footer,
      fold: foldFooter,
    },
    width,
    height: Math.ceil(h),
  };
}

// labelSize sizes an edge's pill: a protocol chip, then what is sent, then what comes back.
function labelLines(e: { protocol?: string; send?: string; back?: string }, mono = false) {
  const chipW = e.protocol
    ? textWidth(e.protocol.toUpperCase(), LABEL.chipSize, 600, true) + LABEL.chipPad * 2
    : 0;
  const indent = chipW ? chipW + LABEL.chipGap : 0;
  const textMax = LABEL.max - indent;
  // Two lines each at most, the last ending in an ellipsis when cut; the panel has the full text.
  const send = e.send
    ? mono
      ? wrap(e.send, textMax, LABEL.size, 500, true).slice(0, 2)
      : clampLines(e.send, textMax, 2, LABEL.size, 500)
    : [];
  const back = e.back
    ? mono
      ? wrap(e.back, textMax - LABEL.arrow, LABEL.size, 400, true).slice(0, 2)
      : clampLines(e.back, textMax - LABEL.arrow, 2, LABEL.size, 400)
    : [];
  let w = 0;
  for (const l of send) w = Math.max(w, textWidth(l, LABEL.size, 500, mono));
  for (const l of back) w = Math.max(w, textWidth(l, LABEL.size, 400, mono) + LABEL.arrow);
  const lines = Math.max(1, send.length + back.length);
  const width = Math.ceil(LABEL.padX * 2 + indent + w) + 2;
  const height = LABEL.padY * 2 + lines * LABEL.line;
  return { send, back, width, height, hasLabel: !!(e.protocol || send.length || back.length) };
}

export async function drawView(view: View, all: Schematic, folding?: Folding): Promise<Drawn> {
  const specNodes: SpecNode[] = [];
  const models = new Map<string, { model: NodeModel; kind: Kind }>();
  const hidden = folding?.hidden ?? new Set<string>();
  for (const n of view.nodes ?? []) {
    if (hidden.has(n.id)) continue;
    if (n.kind === "group") {
      const accent = n.accent ?? "module";
      specNodes.push({
        id: n.id,
        parent: n.parent,
        width: 0,
        height: 0,
        group: true,
        padding: { top: 52, left: 22, bottom: 22, right: 22 },
        minWidth: groupHeadWidth(
          accent === "core" ? "Core" : KINDS[accent === "group" ? "module" : accent].label,
          n.name,
        ),
      });
      const label =
        accent === "core" ? "Core" : KINDS[accent === "group" ? "module" : accent].label;
      models.set(n.id, {
        model: { variant: "group", title: n.name, eyebrow: label, path: n.path, accent, node: n },
        kind: "group",
      });
      continue;
    }
    const { model, width, height } = cardModel(n, view, all, folding?.folds.get(n.id));
    specNodes.push({ id: n.id, parent: n.parent, width, height });
    models.set(n.id, { model, kind: n.kind });
  }
  const specEdges: SpecEdge[] = [];
  const edgeModels: EdgeModel[] = [];
  for (const e of view.edges ?? []) {
    if (hidden.has(e.from) || hidden.has(e.to)) continue;
    const implementsEdge = e.style === "implements";
    const l = implementsEdge ? null : labelLines(e);
    specEdges.push({
      id: e.id,
      from: e.from,
      to: e.to,
      reverse: implementsEdge,
      label: l?.hasLabel ? { width: l.width, height: l.height } : undefined,
    });
    edgeModels.push({
      id: e.id,
      from: e.from,
      to: e.to,
      style: implementsEdge ? "implements" : "flow",
      chip: e.protocol,
      send: l?.send ?? [],
      back: l?.back ?? [],
      edge: e,
    });
  }
  return finish(specNodes, specEdges, models, edgeModels, baseOptions("roomy"));
}

async function finish(
  specNodes: SpecNode[],
  specEdges: SpecEdge[],
  models: Map<string, { model: NodeModel; kind: Kind }>,
  edgeModels: EdgeModel[],
  options: ReturnType<typeof baseOptions>,
): Promise<Drawn> {
  // Left to right reads best, but a long chain then shrinks to fit the canvas; top to bottom wins
  // when it lets the level show clearly larger.
  const [across, down] = await Promise.all([
    layout(specNodes, specEdges, { ...options, "elk.direction": "RIGHT" }),
    layout(specNodes, specEdges, { ...options, "elk.direction": "DOWN" }),
  ]);
  const scale = (l: { width: number; height: number }) =>
    Math.min(CANVAS.width / l.width, CANVAS.height / l.height);
  const result = scale(down) > scale(across) * 1.12 ? down : across;
  const nodes: DrawnNode[] = [];
  for (const s of specNodes) {
    const p = result.nodes.get(s.id);
    const m = models.get(s.id);
    if (!p || !m) continue;
    nodes.push({ ...p, model: m.model, kind: m.kind });
  }
  const edges: DrawnEdge[] = edgeModels.map((e) => {
    const r = result.edges.get(e.id);
    return { ...e, points: r?.points ?? [], label: r?.label };
  });
  return { nodes, edges, width: result.width, height: result.height };
}

// ---- generated class views ---------------------------------------------------------------------

export function packageLabel(path: string, root: string): string {
  if (path === root) return "core";
  if (path.startsWith(`${root}/`)) return path.slice(root.length + 1);
  return path.replace(/^internal\//, "");
}

function methodLine(m: ClassMethod): MemberLine {
  return { name: m.name, rest: m.short, exported: m.exported, full: `${m.name}${m.signature}` };
}

export interface ClassFilter {
  unexported: boolean;
}

// visibleClasses decides what a class view shows. With unexported off: the exported types; an
// unexported type that implements a shown interface (an adapter's connection, say) or holds a
// shown type of another package (an adapter's handler holding the core's service); and the outside
// types a shown type holds, as stubs. Relationships are kept where both ends are shown.
export function visibleClasses(data: ClassData, filter: ClassFilter) {
  const byId = new Map(data.types.map((t) => [t.id, t]));
  const rels = data.relations.filter((r) => byId.has(r.from) && byId.has(r.to));
  const shown = new Set<string>();
  for (const t of data.types) if (filter.unexported || (t.exported && !t.external)) shown.add(t.id);
  let grew = true;
  const show = (t: ClassType) => {
    if (!shown.has(t.id)) {
      shown.add(t.id);
      grew = true;
    }
  };
  while (grew) {
    grew = false;
    for (const r of rels) {
      const a = byId.get(r.from);
      const b = byId.get(r.to);
      if (!a || !b) continue;
      if (r.kind === "implements") {
        if (shown.has(b.id) && !a.external) show(a);
      } else if (r.kind === "holds") {
        if (shown.has(a.id) && b.external) show(b);
        if (shown.has(b.id) && b.exported && !a.exported && !a.external && a.package !== b.package)
          show(a);
      }
    }
  }
  return {
    types: data.types.filter((t) => shown.has(t.id)),
    relations: rels.filter((r) => shown.has(r.from) && shown.has(r.to)),
  };
}

function classModel(
  t: ClassType,
  related: Set<string>,
  filter: ClassFilter,
): { model: ClassModel; width: number; height: number } {
  const fields: MemberLine[] = [];
  let hidden = 0;
  for (const f of t.fields) {
    const isRelated = related.has(f.name);
    if (!f.exported && !filter.unexported && !isRelated) {
      hidden++;
      continue;
    }
    fields.push({
      name: f.embedded ? "" : f.name,
      rest: f.short,
      exported: f.exported,
      related: isRelated,
      full: f.embedded ? `${f.type} (embedded)` : `${f.name} ${f.type}`,
    });
  }
  const methods: MemberLine[] = [];
  for (const m of t.methods) {
    if (!m.exported && !filter.unexported) {
      hidden++;
      continue;
    }
    methods.push(methodLine(m));
  }
  // Exported first, each in source order.
  methods.sort((a, b) => Number(b.exported) - Number(a.exported));
  // An outside type's card shows its package too, as package.Name.
  const shownName = t.external ? `${t.package.split("/").pop()}.${t.name}` : t.name;
  const headW = textWidth(shownName, 13.5, 600) + 84 + CLASS.pad * 2 + 2;
  let w = headW;
  for (const l of [...fields, ...methods]) {
    const lw =
      textWidth(l.name ? `${l.name} ` : "", CLASS.size, 500, true) +
      textWidth(l.rest, CLASS.size, 400, true);
    w = Math.max(w, lw + CLASS.pad * 2 + 4);
  }
  const width = Math.ceil(Math.min(CLASS.max, Math.max(CLASS.min, w)));
  let h = CLASS.head;
  if (fields.length) h += CLASS.section * 2 + fields.length * CLASS.line + 1;
  if (methods.length) h += CLASS.section * 2 + methods.length * CLASS.line + 1;
  if (hidden) h += CLASS.line + CLASS.section;
  if (!fields.length && !methods.length && !hidden) h += 4;
  return {
    model: { variant: "class", type: t, fields, methods, hidden, width },
    width,
    height: Math.ceil(h),
  };
}

export async function drawClasses(
  data: ClassData,
  root: string,
  filter: ClassFilter,
): Promise<Drawn> {
  const { types, relations } = visibleClasses(data, filter);
  const specNodes: SpecNode[] = [];
  const models = new Map<string, { model: NodeModel; kind: Kind }>();
  // Fields that hold or embed a drawn type stay visible, de-emphasised when unexported.
  const relatedFields = new Map<string, Set<string>>();
  for (const r of relations) {
    if (r.kind === "implements") continue;
    const s = relatedFields.get(r.from) ?? new Set<string>();
    for (const v of r.via ?? []) s.add(v);
    relatedFields.set(r.from, s);
  }
  const pkgOrder: string[] = [];
  for (const p of data.packages) pkgOrder.push(p.path);
  for (const t of types) if (!pkgOrder.includes(t.package)) pkgOrder.push(t.package);
  const pkgs = pkgOrder.filter((p) => types.some((t) => t.package === p));
  for (const p of pkgs) {
    const external = !data.packages.some((d) => d.path === p);
    const id = `pkg:${p}`;
    const title = packageLabel(p, root);
    const eyebrow = external ? "Outside" : p === root ? "Package · core" : "Package";
    specNodes.push({
      id,
      width: 0,
      height: 0,
      group: true,
      padding: { top: 50, left: 18, bottom: 18, right: 18 },
      minWidth: groupHeadWidth(eyebrow, title),
    });
    models.set(id, {
      model: {
        variant: "group",
        title,
        eyebrow,
        path: p,
        accent: external ? "external" : p === root ? "core" : "adapter",
        external,
        pkg: p,
      },
      kind: "group",
    });
  }
  for (const t of types) {
    const { model, width, height } = classModel(t, relatedFields.get(t.id) ?? new Set(), filter);
    specNodes.push({ id: t.id, parent: `pkg:${t.package}`, width, height });
    models.set(t.id, { model, kind: t.kind === "interface" ? "port" : "adapter" });
  }
  const specEdges: SpecEdge[] = [];
  const edgeModels: EdgeModel[] = [];
  relations.forEach((r, i) => {
    const id = `rel${i}:${r.kind}:${r.from}->${r.to}`;
    let send: string[] = [];
    let width = 0;
    if (r.kind !== "implements") {
      const text =
        r.kind === "embeds" ? "embeds" : `${r.many ? "[]" : ""}${(r.via ?? []).join(", ")}`;
      const t = truncate(text, 150, 10.5, 500, true);
      send = [t];
      width = Math.ceil(textWidth(t, 10.5, 500, true) + 12);
    }
    specEdges.push({
      id,
      from: r.from,
      to: r.to,
      reverse: r.kind === "implements",
      label: send.length ? { width, height: 18 } : undefined,
    });
    edgeModels.push({
      id,
      from: r.from,
      to: r.to,
      style: r.kind,
      send,
      back: [],
      mono: true,
      relation: r,
    });
  });
  const opts = baseOptions("dense");
  opts["elk.layered.spacing.nodeNodeBetweenLayers"] = "52";
  opts["elk.spacing.nodeNode"] = "22";
  return finish(specNodes, specEdges, models, edgeModels, opts);
}
