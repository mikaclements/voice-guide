// The hand-written schematic holds together: every reference in it points at something that
// exists, so an edit that renames or removes a card, a view or a decision can't leave a dangling
// link the page would draw as nothing.
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { test } from "node:test";
import { pathTo } from "./nav.ts";
import type { Kind, Schematic } from "./types.ts";

const schematic: Schematic = JSON.parse(
  readFileSync(new URL("../data/schematic.json", import.meta.url), "utf8"),
);
const kinds: Kind[] = [
  "client",
  "service",
  "module",
  "core",
  "shared",
  "port",
  "adapter",
  "store",
  "external",
  "group",
];

test("view IDs are unique and every link between views names a view", () => {
  const ids = schematic.views.map((v) => v.id);
  assert.equal(new Set(ids).size, ids.length, `duplicate view IDs in ${ids}`);
  for (const v of schematic.views) {
    if (v.classes)
      assert.ok(
        ids.includes(v.classes),
        `view ${v.id} links class view ${v.classes}, which doesn't exist`,
      );
    for (const n of v.nodes ?? []) {
      if (n.drill)
        assert.ok(
          ids.includes(n.drill),
          `view ${v.id}, node ${n.id}: drill to ${n.drill}, which doesn't exist`,
        );
    }
  }
});

test("every view is reachable from the top level", () => {
  for (const v of schematic.views) {
    assert.equal(
      pathTo(schematic, v.id).at(-1),
      v.id,
      `view ${v.id} is opened by no card or class link`,
    );
  }
});

test("a class view names its generated file and its module's root package", () => {
  for (const v of schematic.views.filter((x) => x.type === "classes")) {
    assert.match(v.source ?? "", /^classes\/[a-z]+\.json$/, `view ${v.id}: source`);
    assert.match(v.root ?? "", /^internal\/[a-z]+$/, `view ${v.id}: root`);
    assert.equal(
      v.nodes,
      undefined,
      `view ${v.id} is generated, so it holds no hand-written nodes`,
    );
  }
});

test("nodes have a known kind, unique IDs, and a group as parent, listed before them", () => {
  for (const v of schematic.views) {
    const seen = new Map<string, Kind>();
    for (const n of v.nodes ?? []) {
      assert.ok(kinds.includes(n.kind), `view ${v.id}, node ${n.id}: kind ${n.kind}`);
      assert.ok(!seen.has(n.id), `view ${v.id}: node ${n.id} twice`);
      if (n.parent)
        assert.equal(
          seen.get(n.parent),
          "group",
          `view ${v.id}, node ${n.id}: parent ${n.parent} isn't a group listed before it`,
        );
      seen.set(n.id, n.kind);
    }
  }
});

test("edges join nodes of their own view, and their IDs are unique", () => {
  for (const v of schematic.views) {
    const nodes = new Set((v.nodes ?? []).map((n) => n.id));
    const ids = new Set<string>();
    for (const e of v.edges ?? []) {
      assert.ok(!ids.has(e.id), `view ${v.id}: edge ${e.id} twice`);
      ids.add(e.id);
      assert.ok(
        nodes.has(e.from) && nodes.has(e.to),
        `view ${v.id}, edge ${e.id}: ${e.from} -> ${e.to} joins a node it doesn't have`,
      );
      if (e.style !== "implements")
        assert.ok(e.send || e.protocol, `view ${v.id}, edge ${e.id}: no label saying what flows`);
    }
  }
});

test("every consideration cited exists, and every one written is cited", () => {
  const cited = new Set<string>();
  const cite = (where: string, ids: string[] | undefined) => {
    for (const id of ids ?? []) {
      assert.ok(schematic.considerations[id], `${where} cites ${id}, which doesn't exist`);
      cited.add(id);
    }
  };
  for (const v of schematic.views) {
    cite(`view ${v.id}`, v.considerations);
    for (const n of v.nodes ?? []) cite(`view ${v.id}, node ${n.id}`, n.considerations);
    for (const e of v.edges ?? []) cite(`view ${v.id}, edge ${e.id}`, e.considerations);
  }
  for (const id of Object.keys(schematic.considerations))
    assert.ok(cited.has(id), `consideration ${id} is cited nowhere`);
});

test("a consideration has its title, decision and why, and shows no design label", () => {
  for (const [id, c] of Object.entries(schematic.considerations)) {
    assert.ok(
      c.title && c.decision && c.why,
      `consideration ${id} lacks its title, decision or why`,
    );
    for (const text of [c.title, c.decision, c.why, c.tradeoff ?? "", c.rejected ?? ""]) {
      assert.doesNotMatch(
        text,
        /\b[DE]\d+\b/,
        `consideration ${id} shows a label; use the decision's title`,
      );
    }
  }
});

test("every consideration points at a decision the design still has", () => {
  const design = readFileSync(
    new URL("../../docs/reference/architecture.md", import.meta.url),
    "utf8",
  );
  for (const [id, c] of Object.entries(schematic.considerations)) {
    const anchor = c.ref?.match(/^architecture\.md#(d\d+)$/)?.[1];
    assert.ok(anchor, `consideration ${id}: ref ${c.ref} isn't a design decision`);
    assert.ok(
      design.includes(`<a id="${anchor}"></a>`),
      `consideration ${id}: the design has no ${anchor}`,
    );
  }
});

test("every hand-written level with cards has a walk-through", () => {
  for (const v of schematic.views) {
    if (!(v.nodes ?? []).some((n) => n.kind !== "group")) continue;
    assert.ok(v.flows?.length, `view ${v.id} has cards but no walk-through`);
  }
});

test("a walk-through's steps light cards and connections of their own level, both ends of each", () => {
  for (const v of schematic.views) {
    const nodes = new Set((v.nodes ?? []).filter((n) => n.kind !== "group").map((n) => n.id));
    const edges = new Map((v.edges ?? []).map((e) => [e.id, e]));
    const ids = new Set<string>();
    for (const f of v.flows ?? []) {
      assert.ok(!ids.has(f.id), `view ${v.id}: walk-through ${f.id} twice`);
      ids.add(f.id);
      assert.ok(f.name && f.steps.length, `view ${v.id}, walk-through ${f.id}: no name or steps`);
      f.steps.forEach((s, i) => {
        const where = `view ${v.id}, walk-through ${f.id}, step ${i + 1}`;
        assert.ok(s.caption, `${where}: no caption`);
        assert.doesNotMatch(s.caption, /\b[DEUW]\d+\b/, `${where}: the caption shows a label`);
        assert.ok(s.nodes.length, `${where}: lights no card`);
        for (const n of s.nodes) assert.ok(nodes.has(n), `${where}: no card ${n}`);
        for (const id of s.edges) {
          const e = edges.get(id);
          assert.ok(e, `${where}: no connection ${id}`);
          assert.ok(
            s.nodes.includes(e.from) && s.nodes.includes(e.to),
            `${where}: lights ${id} but not both its cards`,
          );
        }
      });
    }
  }
});

test("a card's why is one plain sentence with no design label", () => {
  for (const v of schematic.views) {
    for (const n of v.nodes ?? []) {
      if (!n.why) continue;
      assert.doesNotMatch(n.why, /\b[DE]\d+\b/, `view ${v.id}, node ${n.id}: why shows a label`);
      assert.ok(
        n.why.length <= 220,
        `view ${v.id}, node ${n.id}: why runs to ${n.why.length} characters; keep it one sentence`,
      );
    }
  }
});

test("a card's setting names the setting, then its choices", () => {
  for (const v of schematic.views) {
    for (const n of v.nodes ?? []) {
      if (n.setting === undefined) continue;
      assert.match(
        n.setting,
        /^[A-Za-z_]+: \S/,
        `view ${v.id}, node ${n.id}: setting "${n.setting}" isn't "NAME: choices"`,
      );
    }
  }
});

test("a card's setting is read where it says, so a renamed setting can't leave a chip behind", () => {
  const read = (path: string) => readFileSync(new URL(`../../${path}`, import.meta.url), "utf8");
  // An environment variable is read by voice, the model service, the run script or compose; an
  // agent's own setting is a key of its file.
  const env = [
    "cmd/voice/main.go",
    "services/model/src/settings.ts",
    "scripts/run.sh",
    "compose.yaml",
  ]
    .map(read)
    .join("\n");
  const agentFile = read("internal/agents/agentfile/agentfile.go");
  for (const v of schematic.views) {
    for (const n of v.nodes ?? []) {
      if (!n.setting) continue;
      const name = n.setting.split(":")[0];
      const where = `view ${v.id}, node ${n.id}: setting ${name}`;
      if (name === name.toUpperCase())
        assert.match(env, new RegExp(`\\b${name}\\b`), `${where} is read nowhere`);
      else assert.ok(agentFile.includes(`yaml:"${name}"`), `${where} isn't a key of an agent file`);
    }
  }
});

test("the design's address is a page, so a decision's anchor can be added to it", () => {
  assert.match(schematic.design, /^https:\/\/\S+\/architecture\.md$/);
});
