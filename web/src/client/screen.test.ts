import assert from "node:assert/strict";
import { test } from "node:test";
import { ChannelTurns } from "./audio.ts";
import { Screen } from "./screen.ts";
import { httpTurns } from "./turns.ts";

const board = (content: string) => ({ name: "board", content });
const notes = (content: string) => ({ name: "notes", content });
const screenMessage = (n: number, block: { name: string; content: string }) => ({
  type: "screen",
  n,
  screen_input: [block],
});

// An audio connection's data channel opened on screen, recording what the page sends on it.
function channel(screen: Screen) {
  const sent: object[] = [];
  const turns = new ChannelTurns((m) => sent.push(JSON.parse(m)), undefined, screen);
  turns.opened();
  const from = (m: object) => turns.receive(JSON.stringify(m));
  return { sent, turns, from };
}

test("with audio up, each block that changed goes at once in a screen message of its own, and one unchanged never", () => {
  const screen = new Screen();
  const c = channel(screen);
  screen.set([board("Cache"), notes("Read-heavy")]);
  screen.set([board("Cache"), notes("Read-heavy")]);
  screen.set([board("Cache -> Database"), notes("Read-heavy")]);
  assert.deepEqual(c.sent, [
    screenMessage(1, board("Cache")),
    screenMessage(2, notes("Read-heavy")),
    screenMessage(3, board("Cache -> Database")),
  ]);
});

test("after a reconnect, every change voice didn't say went with a turn is sent again, and the old channel's word counts no more", () => {
  const screen = new Screen();
  const first = channel(screen);
  screen.set([board("Cache"), notes("Read-heavy")]);
  first.from({ type: "done", request_id: "r1", screen_taken: 1 });
  first.turns.closed();
  const second = channel(screen);
  assert.deepEqual(second.sent, [screenMessage(1, notes("Read-heavy"))]);
  first.from({ type: "done", request_id: "r2", screen_taken: 2 }); // a connection let go late
  second.turns.closed();
  assert.deepEqual(channel(screen).sent, [screenMessage(1, notes("Read-heavy"))]);
});

test("a block voice refuses is reported, not sent again unchanged, and never counted as taken", () => {
  const refusals: string[] = [];
  const screen = new Screen((m) => refusals.push(m));
  const c = channel(screen);
  screen.set([board("x".repeat(9))]);
  c.from({ type: "screen_refused", n: 1, detail: "screen input is over the limit" });
  screen.set([board("x".repeat(9)), notes("Read-heavy")]);
  c.from({ type: "done", request_id: "r1", screen_taken: 2 });
  assert.deepEqual(refusals, ["screen input is over the limit"]);
  assert.deepEqual(c.sent, [
    screenMessage(1, board("x".repeat(9))),
    screenMessage(2, notes("Read-heavy")),
  ]);
  assert.deepEqual(screen.pending(), [board("x".repeat(9))]);
  screen.set([board("Cache"), notes("Read-heavy")]);
  assert.deepEqual(c.sent.at(-1), screenMessage(3, board("Cache")));
});

// voice's answer to a typed turn: the reply streamed, or a refusal.
const answered = () =>
  new Response('event: text\ndata: {"text":"So"}\n\nevent: done\ndata: {}\n\n', {
    headers: { "Content-Type": "text/event-stream" },
  });
const refused = () =>
  Response.json(
    { type: "about:blank", title: "Payload Too Large", status: 413 },
    { status: 413, headers: { "Content-Type": "application/problem+json" } },
  );

test("over HTTP a turn, the opening too, carries the pending blocks, which stay pending until a turn carrying them is taken", async () => {
  const bodies: object[] = [];
  let answer = refused;
  const fetcher = (async (_url: string, init: RequestInit) => {
    bodies.push(JSON.parse(init.body as string));
    return answer();
  }) as typeof fetch;
  const screen = new Screen();
  const turns = httpTurns("s1", "tk1", fetcher, screen);
  screen.set([board("Cache")]);
  await assert.rejects(turns.take("", "r1", () => {}, true));
  answer = answered;
  await turns.take("Hi", "r2", () => {});
  await turns.take("And now?", "r3", () => {});
  assert.deepEqual(bodies, [
    { request_id: "r1", opening: true, screen_input: [board("Cache")] },
    { request_id: "r2", words: "Hi", screen_input: [board("Cache")] },
    { request_id: "r3", words: "And now?" },
  ]);
});
