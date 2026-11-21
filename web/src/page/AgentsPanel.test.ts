import assert from "node:assert/strict";
import { resolve } from "node:path";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { runnerImport } from "vite";
import type { AgentsViewProps } from "./AgentsPanel.tsx";
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
  id: "",
  name: path.replace(".md", ""),
  modified: 1,
  published_at: null,
});

// The panel drawn with these, and nothing loaded, open or pressed otherwise.
const drawn = (props: Partial<AgentsViewProps>) =>
  renderToStaticMarkup(
    createElement(panel.AgentsView, {
      name: "",
      text: "",
      busy: false,
      result: "",
      error: "",
      on: { publish: none, open: none, name: none, create: none, text: none, save: none },
      ...props,
    }),
  );

test("the panel lists each agent file with its folder, published version and a Publish button, and why voice couldn't say", () => {
  const html = drawn({
    list: {
      files: [
        file("coach.md", "local", 2),
        file("guide.md", "local", null),
        file("lamp.md", "shipped", "unknown"),
      ],
      problem: "the product's server couldn't reach voice: fetch failed",
    },
  });
  assert.deepEqual(
    [...html.matchAll(/<span class="meta">([^<]*)</g)].map((m) => m[1]),
    [
      "seed/local/agents/coach.md · version 2",
      "seed/local/agents/guide.md · not published",
      "agents/lamp.md · version unknown",
    ],
  );
  assert.equal(html.match(/<button type="button">Publish<\/button>/g)?.length, 3);
  assert.match(html, /<div class="error">the product&#x27;s server couldn&#x27;t reach voice/);
  assert.match(drawn({ list: { files: [] } }), /No agent files yet.*New agent<\/button>/);
});

test("a local file opens to edit and save, a shipped one read-only", () => {
  const list = { files: [file("coach.md", "local", 2)] };
  const open = { path: "coach.md", modified: 1, content: "saved text" };
  const local = drawn({ list, open: { ...open, folder: "local" }, text: "saved text" });
  assert.match(local, /<textarea[^>]*>saved text<\/textarea>/);
  assert.doesNotMatch(local, /readOnly|readonly/);
  assert.match(local, /disabled="">Save<\/button>/);

  const shipped = drawn({ list, open: { ...open, folder: "shipped" }, text: "saved text" });
  assert.match(shipped, /<textarea[^>]*readOnly=""/i);
  assert.doesNotMatch(shipped, />Save</);

  // Publish sends the file on disk, so it waits for the open file's text to be saved.
  const edited = drawn({ list, open: { ...open, folder: "local" }, text: "edited" });
  assert.match(edited, /<button type="button" disabled="">Publish<\/button>/);
  assert.match(
    edited,
    /<button type="button">Save<\/button><span class="meta">Save before publishing/,
  );
});

test("a press's result line, or its problem's title and detail, shows beneath the list", () => {
  const list = { files: [file("coach.md", "local", 2)] };
  assert.match(
    drawn({ list, result: "coach.md: published version 2" }),
    /"meta">coach.md: published/,
  );
  assert.match(
    drawn({ list, error: "Invalid agent file (422): name is missing" }),
    /<div class="error">Invalid agent file \(422\): name is missing<\/div><\/section>/,
  );
});
