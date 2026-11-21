import assert from "node:assert/strict";
import { resolve } from "node:path";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { runnerImport } from "vite";
import type { AgentsViewProps } from "./AgentsPanel.tsx";
import { formFrom } from "./agentForm.ts";
import type { AgentFile } from "./agents.ts";

// Node runs TypeScript but not JSX, so the panel is loaded through Vite, as its dev server does.
const { module: panel } = await runnerImport<typeof import("./AgentsPanel.tsx")>(
  resolve(import.meta.dirname, "AgentsPanel.tsx"),
  { configFile: false, logLevel: "silent" },
);

const none = () => {};
const file = (path: string, folder: AgentFile["folder"], version: AgentFile["version"]) => ({
  path,
  folder,
  version,
  id: "01900000-0000-7000-8000-000000000001",
  name: path.replace(".md", ""),
  modified: 1,
  published_at: null,
  same_id_as: [],
});
const form = formFrom({
  id: "01900000-0000-7000-8000-000000000001",
  name: "Coach",
  description: "Helps you practise.",
  reads: ["profile"],
  session_context: ["resume", "job"],
  summary: { instruction: "Note it.", model: null, schema: null, documents: ["job"] },
  opens: "agent",
  model: "my-model-1",
  voice: "bf_emma",
  speed: null,
  persona: "You coach.",
});
const list = { files: [file("coach.md", "local", 3), file("lamp.md", "shipped", 1)] };
const published = { version: 3, published_at: "2026-01-02T03:04:05.123Z" };

// The panel drawn with these, and nothing loaded, picked or pressed otherwise.
const drawn = (props: Partial<AgentsViewProps>) =>
  renderToStaticMarkup(
    createElement(panel.AgentsView, {
      voices: [],
      picked: "",
      dirty: false,
      busy: false,
      result: "",
      error: "",
      on: { pick: none, form: none, publish: none, revert: none, clone: none, publishFile: none },
      ...props,
    }),
  );
const options = (html: string) => [...html.matchAll(/<option[^>]*>([^<]*)</g)].map((m) => m[1]);
const buttons = (html: string) =>
  [...html.matchAll(/<button type="button"( disabled="")?>([^<]*)</g)].map(
    (m) => m[2] + (m[1] ? " (disabled)" : ""),
  );

test("the dropdown offers every agent file with its folder and published version, then a new agent, and says why voice couldn't tell versions", () => {
  const html = drawn({
    list: {
      files: [...list.files, file("guide.md", "local", null), file("lamp2.md", "local", "unknown")],
      problem: "the product's server couldn't reach voice: fetch failed",
    },
  });
  assert.deepEqual(options(html), [
    "Pick an agent",
    "coach · version 3",
    "lamp · shipped, version 1",
    "guide · not published",
    "lamp2 · version unknown",
    "New agent",
  ]);
  assert.match(html, /<div class="error">the product&#x27;s server couldn&#x27;t reach voice/);
});

test("a picked agent shows what voice holds, then one labelled field per setting in the agent file's order, the persona last", () => {
  const html = drawn({ list, picked: "coach.md", form, published, voices: ["af_heart"] });
  assert.match(html, /<p>Version 3 is published, since 2026-01-02 03:04:05 UTC.<\/p>/);
  assert.deepEqual(
    [...html.matchAll(/<(?:label [^>]*for=[^>]*|legend|span class="label")>([^<]+)</g)].map(
      (m) => m[1],
    ),
    [
      "Name",
      "Description",
      "Reads",
      "Session context",
      "Summary",
      "Instruction",
      "Model",
      "Documents",
      "Summary only",
      "Schema",
      "Opens",
      "Model",
      "Voice",
      "Speed",
      "Persona",
    ],
  );
  assert.match(html, /id="agent-reads"[^>]* value="profile"/);
  assert.match(html, /<textarea id="agent-persona" class="wide" rows="18">You coach.<\/textarea>/);
  assert.match(html, /<option value="agent" selected="">The agent speaks first/);
  // The agent's own voice is offered beside voice-guide's list, and empty is the server's.
  assert.match(
    html,
    /<option value="">The server&#x27;s<\/option><option value="bf_emma" selected="">bf_emma<\/option><option value="af_heart">af_heart</,
  );
  assert.deepEqual(buttons(html), ["Publish", "Revert", "Clone", "Publish the file as it is"]);
});

test("the summary's documents are checked among the session context names, a model off the list shows Other and its name, and the speed is a number in voice's range", () => {
  const html = drawn({ list, picked: "coach.md", form, published });
  assert.match(
    html,
    /<div class="choices"><label><input type="checkbox"\/>resume<\/label><label><input type="checkbox" checked=""\/>job<\/label><\/div>/,
  );
  assert.match(
    html,
    /<select id="agent-summaryModel"><option value="" selected="">The agent&#x27;s model/,
  );
  assert.match(
    html,
    /<option value="claude-haiku-5-5">claude-haiku-5-5<\/option><option value="other">Other…/,
  );
  assert.match(
    html,
    /<option value="other" selected="">Other…<\/option><\/select><input aria-label="The agent&#x27;s model name"[^>]* value="my-model-1"/,
  );
  assert.match(html, /<input id="agent-speed"[^>]* type="number" min="0.5" max="2" step="0.05"/);
  const dropped = drawn({
    list,
    picked: "coach.md",
    form: { ...form, sessionContext: "resume" },
    published,
  });
  assert.match(dropped, /Dropped, as no longer session context names: job/);
});

test("a shipped agent shows read-only, to clone into a test agent, never to publish from the form", () => {
  const html = drawn({ list, picked: "lamp.md", form, published });
  assert.match(html, /agents\/lamp.md, shipped: read-only here/);
  assert.equal(html.match(/<(input|textarea)[^>]*readOnly=""/g)?.length, 10);
  assert.equal(html.match(/<select[^>]*disabled=""/g)?.length, 4);
  assert.equal(html.match(/<input type="checkbox"[^>]*disabled=""/g)?.length, 2);
  assert.deepEqual(buttons(html), ["Clone", "Publish the file as it is"]);
});

test("a new agent says its first publish writes its file; a file never published offers to publish it as it is, having nothing to fill the form", () => {
  const made = drawn({ list, picked: panel.newAgent, form, published: null });
  assert.match(
    made,
    /Not published yet: Publish writes its file in seed\/local\/agents\/ and version 1/,
  );
  assert.deepEqual(buttons(made), ["Publish", "Revert"]);
  const never = drawn({
    list: { files: [file("guide.md", "local", null)] },
    picked: "guide.md",
    published: null,
  });
  assert.match(never, /Not published yet, so voice-guide has nothing to fill the form from/);
  assert.doesNotMatch(never, /<fieldset/);
  assert.deepEqual(buttons(never), ["Publish the file as it is"]);
});

test("a file whose id another file holds says which, as neither is published until one gets a new id", () => {
  const files = [
    {
      ...file("copy.md", "local", null),
      same_id_as: [{ path: "lamp.md", folder: "shipped" as const }],
    },
    {
      ...file("lamp.md", "shipped", 1),
      same_id_as: [{ path: "copy.md", folder: "local" as const }],
    },
  ];
  assert.match(
    drawn({ list: { files }, picked: "copy.md", published: null }),
    /<p class="meta">seed\/local\/agents\/copy.md · same id as agents\/lamp.md, so neither is published until one gets a new id<\/p>/,
  );
  assert.match(
    drawn({ list: { files }, picked: "lamp.md", form, published }),
    /agents\/lamp.md, shipped: read-only here · same id as seed\/local\/agents\/copy.md/,
  );
});

test("a local file changed after its version was published warns that Publish would overwrite what it holds", () => {
  const at = Date.parse(published.published_at);
  const warned = (modified: number, folder: AgentFile["folder"] = "local") =>
    /The file changed after version 3 was published: to keep edits made there, use Publish the file as it is, or Publish overwrites them/.test(
      drawn({
        list: { files: [{ ...file("coach.md", folder, 3), modified }] },
        picked: "coach.md",
        form,
        published,
      }),
    );
  assert.deepEqual(
    [warned(at + 60_000), warned(at - 60_000), warned(at + 60_000, "shipped")],
    [true, false, false],
  );
});

test("edits not yet published hold off picking another agent and publishing the file, which would drop them", () => {
  const html = drawn({ list, picked: "coach.md", form, published, dirty: true });
  assert.match(html, /<select aria-label="Agent" disabled="">/);
  assert.match(html, /Publish or Revert the edits to pick another agent/);
  assert.deepEqual(buttons(html), [
    "Publish",
    "Revert",
    "Clone",
    "Publish the file as it is (disabled)",
  ]);
});

test("a press's result line, or what it failed with, shows beside the form", () => {
  const shown = { list, picked: "coach.md", form, published };
  assert.match(
    drawn({ ...shown, result: "published version 4" }),
    /<\/div><p class="meta">published version 4<\/p><\/section>/,
  );
  assert.match(
    drawn({ ...shown, error: "voice-guide refused it: the agent file: name is missing" }),
    /<\/div><div class="error">voice-guide refused it: the agent file: name is missing<\/div><\/section>/,
  );
});
