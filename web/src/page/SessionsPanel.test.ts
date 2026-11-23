import assert from "node:assert/strict";
import { resolve } from "node:path";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { runnerImport } from "vite";
import type { PastSession } from "./product.ts";

// Node runs TypeScript but not JSX, so the panel is loaded through Vite, as the page's server does.
const { module: panel } = await runnerImport<typeof import("./SessionsPanel.tsx")>(
  resolve(import.meta.dirname, "SessionsPanel.tsx"),
  { configFile: false, logLevel: "silent" },
);

const none = () => {};
const form = (rest: Partial<Parameters<typeof panel.StartForm>[0]> = {}) =>
  renderToStaticMarkup(
    createElement(panel.StartForm, {
      agents: [
        { id: "a1", name: "Mock interviewer", description: "", session_context: [] },
        { id: "a2", name: "Coach", description: "", session_context: [] },
      ],
      agentId: "a1",
      started: false,
      voices: ["en_us", "ja_jp"],
      voice: "ja_jp",
      speed: "1.25",
      documents: [{ name: "resume", content: "Ten years of lamps." }],
      busy: false,
      errors: [],
      on: { agent: none, voice: none, speed: none, document: none, start: none },
      ...rest,
    }),
  );

test("the start form draws the agents to pick at its top, the voices, the speed, each document prefilled in a text area, and one Start under them", () => {
  const html = form();
  assert.match(html, /^<form><div class="row"><select aria-label="Agent to start">/);
  assert.match(html, /"a1" selected="">Mock interviewer<\/option><option value="a2">Coach</);
  assert.match(html, /<option value="">The agent&#x27;s voice<\/option>/);
  assert.match(html, /value="ja_jp" selected="">ja_jp</);
  assert.match(html, /value="1.25"/);
  assert.match(
    html,
    /<span class="meta">resume<\/span><textarea[^>]*>Ten years of lamps\.<\/textarea>/,
  );
  assert.match(html, /<button type="submit">Start<\/button><\/div><\/form>$/);
  assert.match(form({ started: true }), /<button type="submit">Start again<\/button>/);
});

test("a speed out of voice's range says so and can't be sent, and a problem shows as it came", () => {
  for (const speed of ["3", "0.1", "fast"]) {
    const html = form({ speed });
    assert.match(html, /Speed is a number from 0.5 to 2/, speed);
    assert.match(html, /<button type="submit" disabled="">/, speed);
  }
  assert.doesNotMatch(form({ speed: "" }), /Speed is a number/, "empty is the agent's own");
  assert.match(form({ errors: ["Unprocessable (422): resume is empty"] }), /resume is empty/);
  assert.match(form({ busy: true }), /<button type="submit" disabled="">/);
});

const past = (rest: Partial<PastSession>): PastSession => ({
  id: "s1",
  agent_id: "a1",
  agent_version: 1,
  started_at: "2026-11-21T10:00:00.123Z",
  ended_at: "2026-11-21T10:05:00Z",
  summary: "Covered the resume.",
  summary_status: "written",
  ...rest,
});

const list = (sessions: PastSession[], rest: { loading?: boolean; error?: string } = {}) =>
  renderToStaticMarkup(
    createElement(panel.PastSessions, {
      sessions,
      agentName: (id: string) => (id === "a1" ? "Mock interviewer" : id),
      loading: false,
      error: "",
      refresh: none,
      ...rest,
    }),
  );

test("past sessions list the agent, when each started and ended, the summary's status and text", () => {
  const html = list([
    past({}),
    past({ id: "s2", agent_id: "a2", ended_at: null, summary: null, summary_status: null }),
  ]);
  assert.match(html, /"who">Mock interviewer<\/span>/);
  assert.match(
    html,
    /started 2026-11-21 10:00:00 UTC, ended 2026-11-21 10:05:00 UTC · summary written/,
  );
  assert.match(html, /<div class="summary">Covered the resume\.<\/div>/);
  assert.match(html, /"who">a2<\/span><span class="meta">started [^<]*, still live</);
  assert.match(html, />Refresh<\/button>/);
});

test("each past session is a block of its own, its summary beneath with its paragraphs and line breaks as written", () => {
  const summary = "Where we got to.\n\nThe story:\n- the bug\n- the fix\n";
  const html = list([past({ summary }), past({ id: "s2", summary: null })]);
  assert.equal(html.match(/<article class="past">/g)?.length, 2);
  assert.match(
    html,
    /<article class="past"><div><span class="who">Mock interviewer<\/span><span class="meta">[^<]*<\/span><\/div><div class="summary">/,
  );
  assert.match(
    html,
    /<div class="summary">Where we got to\.\n\nThe story:\n- the bug\n- the fix<\/div>/,
  );
});

test("the session started shows under the start form in one column, the past sessions in the other", () => {
  const html = renderToStaticMarkup(
    createElement(
      panel.SessionsPanel,
      {
        agents: [{ id: "a1", name: "Mock interviewer", description: "", session_context: [] }],
        agentId: "a1",
        onAgent: none,
        started: true,
        busy: false,
        start: none,
        error: "",
      },
      createElement("p", null, "The session"),
    ),
  );
  assert.match(
    html,
    /^<div class="columns"><div class="column"><form>.*<\/form><p>The session<\/p><\/div><div class="column"><section><h2>Past sessions<\/h2>.*<\/section><\/div><\/div>$/,
  );
});

test("no past sessions says so, and a failed list shows why", () => {
  assert.match(list([]), /No past sessions yet/);
  const down = list([], { error: "the product's server couldn't reach voice: fetch failed" });
  assert.match(down, /couldn&#x27;t reach voice: fetch failed/);
  assert.doesNotMatch(down, /No past sessions yet/);
});
