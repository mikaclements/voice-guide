import assert from "node:assert/strict";
import { resolve } from "node:path";
import { test } from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { runnerImport } from "vite";
import type { Shown } from "../client/joined.ts";

// Node runs TypeScript but not JSX, so the page is loaded through Vite, as its dev server does.
const { module: app } = await runnerImport<typeof import("./App.tsx")>(
  resolve(import.meta.dirname, "App.tsx"),
  { configFile: false, logLevel: "silent" },
);

const agent = (id: string, name: string) => ({ id, name, description: "", session_context: [] });
const none = () => {};
// A live session, with the microphone on, waiting for the user.
const settled = { blocked: false, over: "", locked: false, busy: false };

// The page drawing a session that shows these lines, and the rest as given.
const page = (
  lines: Shown["lines"],
  rest: Partial<Shown> = {},
  props: Partial<Parameters<typeof app.SessionView>[0]> = {},
) =>
  renderToStaticMarkup(
    createElement(app.SessionView, {
      agents: [agent("a1", "Mock interviewer"), agent("a2", "Coach")],
      session: { id: "s1", agent_id: "a1", agent_version: 3, started_at: "" },
      shown: { lines, notes: [], status: "Listening", mute: "Mute", ...settled, ...rest },
      words: "",
      starting: false,
      error: "",
      on: {
        end: none,
        mute: none,
        playAfterPress: none,
        words: none,
        send: none,
      },
      ...props,
    }),
  );

test("the page draws the transcript, beneath each reply what answered and how long the user waited, and a press to hear an agent the browser won't play", () => {
  const html = page([
    { id: "1", who: "you", text: "Hi" },
    { id: "2", who: "agent", text: "Hello", meta: { model: "m", firstTextMs: 400 } },
    { id: "3", who: "agent", text: "Go", meta: { firstSoundMs: 1234, stoppedMs: 96.4 } },
    { id: "4", who: "agent", text: "So", meta: { finishedToSoundMs: 820, lost: true } },
    { id: "5", who: "agent", text: "On", meta: { audioFailed: true } },
  ]);
  assert.match(html, /"who">You<\/span>Hi<\/div>/);
  assert.match(html, /"who">Mock interviewer<\/span>Hello</);
  assert.deepEqual(
    [...html.matchAll(/<div class="meta">([^<]*)</g)].map((m) => m[1]),
    [
      "m · first text 0.40s",
      "first sound 1.23s · stopped 96 ms after sending",
      "finished speaking to first sound 0.82s · the reply was lost",
      "the audio stopped",
    ],
  );
  assert.match(html, /Listening<\/span><button type="button">Mute<\/button>/);
  assert.match(page([], { blocked: true }), />Tap to hear the agent<\/button>/);
});

test("while the browser won't keep the display on, the session says so quietly, beneath its own notes", () => {
  const html = page([], { notes: ["Microphone blocked"] }, { wakeNote: "Display may turn off" });
  assert.match(html, /Microphone blocked<\/p><p class="meta">Display may turn off<\/p>/);
  assert.doesNotMatch(page([]), /Display may turn off/);
});

test("the tab is the one the URL's hash names, Test unless it names Agents, and the tab bar marks it", () => {
  assert.deepEqual(["", "#test", "#agents", "#elsewhere"].map(app.tabFrom), [
    "test",
    "test",
    "agents",
    "test",
  ]);
  assert.match(
    renderToStaticMarkup(createElement(app.Tabs, { tab: "agents" })),
    /<a href="#test">Test<\/a><a href="#agents" aria-current="page">Agents<\/a>/,
  );
});

test("both tabs are drawn, the one the hash doesn't name hidden, so a session and the agent form outlive a switch", () => {
  const drawnAt = (hash: string) => {
    // Only the hash is read while drawing; Node has no window.
    Object.assign(globalThis, { window: { location: { hash } } });
    try {
      return renderToStaticMarkup(createElement(app.App));
    } finally {
      Reflect.deleteProperty(globalThis, "window");
    }
  };
  // The Test tab opens with its columns, the start form first, the Agents tab with its section.
  const panels = (html: string) =>
    [
      ...html.matchAll(
        /<div( hidden="")?>(<div class="columns"><div class="column"><form>)|<div( hidden="")? class="agents"><section>/g,
      ),
    ].map((m) =>
      m[2] ? ["test", m[1] ? "hidden" : "shown"] : ["agents", m[3] ? "hidden" : "shown"],
    );
  assert.deepEqual(panels(drawnAt("#agents")), [
    ["test", "hidden"],
    ["agents", "shown"],
  ]);
  assert.deepEqual(panels(drawnAt("")), [
    ["test", "shown"],
    ["agents", "hidden"],
  ]);
});
