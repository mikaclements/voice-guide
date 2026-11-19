import assert from "node:assert/strict";
import { test } from "node:test";
import { ProblemError } from "../client/api.ts";
import type { Agent, Started } from "./product.ts";
import { Starter } from "./starting.ts";

const agent: Agent = { id: "a1", name: "Keeper", description: "", session_context: ["log"] };
const session: Started = {
  id: "s1",
  agent_id: "a1",
  agent_version: 1,
  started_at: "",
  ticket: "t1",
};
const problem = (status: number) => new ProblemError(status, "about:blank", "T", "");

// A product's server that answers each start with the next outcome given, recording the request ID
// each start sent.
function harness(...outcomes: (Started | Error)[]) {
  const sent: string[] = [];
  const starter = new Starter(async (_agentId, requestId) => {
    sent.push(requestId);
    const next = outcomes.shift();
    if (next === undefined || next instanceof Error) throw next;
    return next;
  });
  return { starter, ids: () => sent };
}

test("a start voice may have taken goes again with its request ID, so one session starts", async () => {
  const h = harness(new TypeError("Failed to fetch"), problem(502), session);
  await assert.rejects(h.starter.start(agent), TypeError);
  await assert.rejects(h.starter.start(agent), ProblemError);
  assert.equal(await h.starter.start(agent), session);
  const [first, ...rest] = h.ids();
  assert.deepEqual(rest, [first, first]);
});

test("a start voice refused is a new request next time", async () => {
  for (const refusal of [problem(404), problem(422)]) {
    const h = harness(new TypeError("Failed to fetch"), refusal, session);
    await assert.rejects(h.starter.start(agent), TypeError);
    await assert.rejects(h.starter.start(agent), ProblemError);
    await h.starter.start(agent);
    const [first, refused, next] = h.ids();
    assert.equal(refused, first, "the retry repeats the kept request ID");
    assert.notEqual(next, refused, `after a ${refusal.status} the request ID is new`);
  }
});

test("a started session lets its request ID go, so starting again starts a new session", async () => {
  const h = harness(session, session);
  await h.starter.start(agent);
  await h.starter.start(agent);
  const [first, next] = h.ids();
  assert.notEqual(next, first);
});

test("a start the product's server turned away, though voice may have taken the one before, is repeated with its request ID", async () => {
  // Not voice's problem document: the dev server's own refusal, never seen by voice.
  const turnedAway = (status: number) =>
    new ProblemError(status, "about:blank", "T", "missing context", false);
  for (const refusal of [turnedAway(404), turnedAway(400)]) {
    const h = harness(new TypeError("Failed to fetch"), refusal, session);
    await assert.rejects(h.starter.start(agent), TypeError);
    await assert.rejects(h.starter.start(agent), ProblemError);
    await h.starter.start(agent);
    const [first, ...rest] = h.ids();
    assert.deepEqual(rest, [first, first], `a ${refusal.status} from the product keeps the ID`);
  }
});

test("a repeat start that comes back with no ticket says the session has expired and lets its request ID go", async () => {
  const { ticket: _, ...bare } = session;
  const h = harness(new TypeError("Failed to fetch"), bare as Started, session);
  await assert.rejects(h.starter.start(agent), TypeError);
  await assert.rejects(
    h.starter.start(agent),
    (e) => e instanceof ProblemError && e.message === "This session has expired; start again",
  );
  await h.starter.start(agent);
  const [first, expired, next] = h.ids();
  assert.equal(expired, first, "the repeat carried the kept request ID");
  assert.notEqual(next, expired, "after the expiry the request ID is new");
});
