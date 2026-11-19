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
const page = (lines: Shown["lines"], rest: Partial<Shown> = {}) =>
  renderToStaticMarkup(
    createElement(app.SessionView, {
      agents: [agent("a1", "Mock interviewer"), agent("a2", "Coach")],
      agentId: "a1",
      session: { id: "s1", agent_id: "a1", agent_version: 3, started_at: "" },
      shown: { lines, notes: [], status: "Listening", mute: "Mute", ...settled, ...rest },
      words: "",
      starting: false,
      error: "",
      on: { agent: none, start: none, end: none, mute: none, hear: none, words: none, send: none },
    }),
  );

test("the page draws the agents to pick, the transcript, beneath each reply what answered and how long the user waited, and a press to hear an agent the browser won't play", () => {
  const html = page([
    { id: "1", who: "you", text: "Hi" },
    { id: "2", who: "agent", text: "Hello", meta: { model: "m", firstTextMs: 400 } },
    { id: "3", who: "agent", text: "Go", meta: { firstSoundMs: 1234, stoppedMs: 96.4 } },
    { id: "4", who: "agent", text: "So", meta: { finishedToSoundMs: 820, lost: true } },
    { id: "5", who: "agent", text: "On", meta: { audioFailed: true } },
  ]);
  assert.match(html, /"a1" selected="">Mock interviewer<\/option><option value="a2">Coach</);
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
