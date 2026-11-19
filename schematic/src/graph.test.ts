// Laying out the levels: every hand-written level lays out with no card over another and every
// edge routed, and a class view shows what its filter says. Text is measured by estimate here, as
// there is no browser; scripts/schematic.sh check measures the real page.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { foldedBehind, hiddenNodes } from "./fold.ts";
import {
  type CardModel,
  type DrawnNode,
  drawView,
  type Folding,
  settingWhere,
  visibleClasses,
} from "./graph.ts";
import type { ClassData, ClassType, Schematic, View } from "./types.ts";

const schematic: Schematic = JSON.parse(
  readFileSync(new URL("../data/schematic.json", import.meta.url), "utf8"),
);

function overlap(a: DrawnNode, b: DrawnNode): boolean {
  const ox = Math.min(a.absX + a.width, b.absX + b.width) - Math.max(a.absX, b.absX);
  const oy = Math.min(a.absY + a.height, b.absY + b.height) - Math.max(a.absY, b.absY);
  return ox > 0.5 && oy > 0.5;
}

// Each way a level is drawn: every card shown; folded, as it opens; and with each walk-through's
// cards shown, as while it plays.
function drawings(view: View): [string, Folding | undefined][] {
  const out: [string, Folding | undefined][] = [["all shown", undefined]];
  if (!foldedBehind(view).size) return out;
  const folds = new Map([...foldedBehind(view).keys()].map((h) => [h, { count: 1, open: false }]));
  out.push(["folded", { hidden: hiddenNodes(view, new Set(), new Set()), folds }]);
  for (const f of view.flows ?? []) {
    const keep = new Set(f.steps.flatMap((s) => s.nodes));
    out.push([
      `walk-through ${f.id}`,
      { hidden: hiddenNodes(view, new Set(), keep), folds: new Map() },
    ]);
  }
  return out;
}

test("every hand-written level lays out with no card over another and every edge routed, folded or not", async () => {
  for (const view of schematic.views.filter((v) => v.type !== "classes"))
    for (const [how, folding] of drawings(view)) await laysOut(view, `${view.id}, ${how}`, folding);
});

async function laysOut(view: View, where: string, folding?: Folding) {
  const drawn = await drawView(view, schematic, folding);
  const cards = drawn.nodes.filter((n) => n.model.variant !== "group");
  const placed = (view.nodes ?? []).filter((n) => !folding?.hidden.has(n.id)).length;
  assert.equal(drawn.nodes.length, placed, `view ${where}: a node wasn't placed`);
  for (let i = 0; i < cards.length; i++) {
    for (let j = i + 1; j < cards.length; j++) {
      assert.ok(
        !overlap(cards[i], cards[j]),
        `view ${where}: ${cards[i].id} overlaps ${cards[j].id}`,
      );
    }
  }
  for (const e of drawn.edges)
    assert.ok(e.points.length >= 2, `view ${where}: edge ${e.id} has no route`);
}

test("a card with a setting shows it as a chip, taller by its lines, and the panel says where it's set", async () => {
  const card = { id: "tts", kind: "port" as const, name: "TextToSpeech", summary: "Speaks." };
  const level = (setting?: string): View => ({
    id: "v",
    title: "v",
    level: 3,
    lead: "",
    considerations: [],
    nodes: [{ ...card, setting }],
  });
  const [plain] = (await drawView(level(), schematic)).nodes;
  const [set] = (await drawView(level("VOICE_TTS: kokoro · fake"), schematic)).nodes;
  assert.deepEqual((plain.model as CardModel).setting, []);
  assert.deepEqual((set.model as CardModel).setting, ["VOICE_TTS: kokoro · fake"]);
  assert.ok(set.height > plain.height, "the card didn't grow to hold its setting");
  const long = "MODEL_PROVIDER: claude-api · claude-subscription · fake";
  const [wide] = (await drawView(level(long), schematic)).nodes;
  assert.equal((wide.model as CardModel).setting.join(" "), long, "a long setting lost words");
  for (const l of (wide.model as CardModel).setting)
    assert.doesNotMatch(l, /·$/, "a line breaks a choice from its separator");
  assert.match(settingWhere(long), /\.env/);
  assert.match(settingWhere("opens: agent · user"), /agent file/);
});

function type(pkg: string, name: string, extra: Partial<ClassType> = {}): ClassType {
  return {
    id: `${pkg}.${name}`,
    package: pkg,
    name,
    kind: "struct",
    exported: true,
    fields: [],
    methods: [],
    ...extra,
  };
}

// A core with a port and a service holding it; an adapter with an unexported type that implements
// the port and an exported one; an outside type the service holds.
const data: ClassData = {
  generator: "classgen",
  generated: "2026-01-01T00:00:00Z",
  module: "example.com/m",
  patterns: ["./internal/shop/..."],
  packages: [
    { path: "internal/shop", name: "shop", doc: "" },
    { path: "internal/shop/store", name: "store", doc: "" },
  ],
  types: [
    type("internal/shop", "Store", { kind: "interface" }),
    type("internal/shop", "Service"),
    type("internal/shop", "helper", { exported: false }),
    type("internal/shop/store", "rows", { exported: false }),
    type("internal/shop/store", "Options"),
    type("internal/clock", "Clock", { external: true }),
  ],
  relations: [
    { from: "internal/shop.Service", to: "internal/shop.Store", kind: "holds", via: ["store"] },
    { from: "internal/shop.Service", to: "internal/clock.Clock", kind: "holds", via: ["clock"] },
    {
      from: "internal/shop/store.rows",
      to: "internal/shop.Store",
      kind: "implements",
      pointer: true,
    },
    { from: "internal/shop.helper", to: "internal/shop.Service", kind: "holds", via: ["svc"] },
  ],
};

test("with unexported off, a class view shows exported types, an unexported implementer of a shown port and the outside types held", () => {
  const { types, relations } = visibleClasses(data, { unexported: false });
  const ids = types.map((t) => t.id).sort();
  assert.deepEqual(ids, [
    "internal/clock.Clock",
    "internal/shop.Service",
    "internal/shop.Store",
    "internal/shop/store.Options",
    "internal/shop/store.rows",
  ]);
  assert.ok(
    relations.every((r) => ids.includes(r.from) && ids.includes(r.to)),
    "a relation joins a type not shown",
  );
  assert.equal(relations.length, 3);
});

test("with unexported on, every type is shown", () => {
  const { types, relations } = visibleClasses(data, { unexported: true });
  assert.equal(types.length, data.types.length);
  assert.equal(relations.length, data.relations.length);
});
