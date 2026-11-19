import assert from "node:assert/strict";
import { test } from "node:test";
import { ProblemError, type Turns } from "./api.ts";
import { ChannelTurns } from "./audio.ts";
import { Hearing } from "./hearing.ts";
import { type Reply, Sender } from "./sending.ts";

// A sender over a data channel's page side, with a clock, playback and page that record what
// they're told; replies are kept by message ID, in the order added.
function harness() {
  const sent: { request_id: string }[] = [];
  const turns = new ChannelTurns((m) => sent.push(JSON.parse(m)));
  const replies = new Map<string, Reply>();
  const givenBack: string[] = [];
  const mutes: boolean[] = [];
  const hearing = new Hearing();
  let now = 0;
  let ids = 0;
  const sender = new Sender(
    hearing,
    {
      add: (_you, _words, reply) => replies.set(reply, { text: "" }),
      open: (reply) => replies.set(reply, { text: "" }),
      hear: () => {},
      update: (reply, change) =>
        replies.has(reply) && replies.set(reply, change(replies.get(reply) as Reply)),
      giveBack: (_you, reply, words) => replies.delete(reply) && givenBack.push(words),
      takeBack: (_you, reply) => replies.delete(reply),
    },
    () => now,
    () => `m${++ids}`,
  );
  return {
    sent,
    givenBack,
    mutes,
    replies,
    send: (words: string) => sender.send(words, turns, { stop() {}, mute: (on) => mutes.push(on) }),
    open: () => sender.open(turns),
    say: (type: string, i: number, m: object = {}) =>
      turns.receive(JSON.stringify({ type, request_id: sent[i].request_id, ...m })),
    // The reply of the i-th send: message IDs come in pairs, the user's words then the reply.
    reply: (i: number) => replies.get(`m${2 * i + 2}`),
    at: (t: number) => {
      now = t;
    },
    // The received audio, loud or silent, read every 10 ms from `from` to `to`.
    play: (loud: boolean, from: number, to: number) => {
      for (now = from; now <= to; now += 10) hearing.sample(loud, now);
      now = to;
    },
  };
}

const flush = () => new Promise((r) => setImmediate(r));

test("an interrupting send mutes at the press, unmutes at its own first text, and times the stop", async () => {
  const h = harness();
  const first = h.send("Hi");
  h.say("text", 0, { text: "Tell me" });
  h.play(true, 0, 100);
  h.send("Wait");
  h.say("text", 0, { text: " about" });
  assert.deepEqual(h.mutes, [true, false, true], "the old reply's text doesn't unmute");
  assert.equal(h.reply(0)?.text, "Tell me about", "its text still goes to the old reply");
  h.play(true, 110, 150);
  h.play(false, 160, 700);
  h.say("done", 0);
  await flush();
  h.say("interrupted", 0, { heard: "Tell me" });
  h.say("text", 1, { text: "Sure." });
  await first;
  assert.deepEqual(h.mutes, [true, false, true, false]);
  assert.deepEqual([h.reply(0)?.heard, h.reply(0)?.stoppedMs], ["Tell me", 50], "stopped at 150");
  assert.equal(h.reply(1)?.text, "Sure.");
});

test("an opening sends no words, and shows the greeting as the agent's line alone once its text comes", async () => {
  const h = harness();
  const opening = h.open();
  assert.deepEqual(h.sent, [{ type: "turn", request_id: h.sent[0].request_id, opening: true }]);
  assert.equal(h.replies.size, 0, "nothing shows before its text");
  h.say("text", 0, { text: "Welcome." });
  h.say("done", 0);
  h.say("spoken", 0);
  await opening;
  assert.deepEqual(
    [...h.replies.values()].map((r) => [r.text, r.ended]),
    [["Welcome.", "done"]],
  );
});

test("an opening voice doesn't take, or that fails, shows nothing and gives nothing back", async () => {
  for (const [type, m] of [
    ["refused", { reason: "opening-refused" }],
    ["failed", {}],
  ] as const) {
    const h = harness();
    const opening = h.open();
    h.say(type, 0, m);
    await opening;
    assert.deepEqual([h.replies.size, h.givenBack], [0, []], type);
  }
});

test("a third send while a stop is being timed leaves the stop with the reply that was sounding", () => {
  const h = harness();
  h.send("Hi");
  h.play(true, 0, 100);
  h.send("Wait");
  h.play(true, 110, 130);
  h.send("Hold on");
  h.play(false, 140, 140); // a gap between words is not the stop
  h.play(true, 150, 150);
  h.play(false, 160, 700);
  assert.equal(h.reply(0)?.stoppedMs, 50, "timed from the first interrupting press");
  assert.equal(h.reply(1)?.stoppedMs, undefined);
  assert.equal(h.reply(2)?.firstSoundMs, undefined, "the old reply's words aren't the new one's");
});

test("a send that isn't taken leaves the reply before it to be heard and timed", async () => {
  const h = harness();
  h.send("Hi");
  h.at(300);
  h.send("Wait");
  h.say("refused", 1, { reason: "busy" });
  await flush();
  assert.deepEqual(h.givenBack, ["Wait"]);
  assert.deepEqual(h.mutes, [true, true, false], "each press mutes; the refusal unmutes");
  h.play(true, 600, 700);
  assert.equal(h.reply(0)?.firstSoundMs, 600, "the old reply's first sound is its own");
  h.send("Hold on");
  h.say("refused", 2, { reason: "busy" });
  await flush();
  h.play(true, 710, 900);
  h.play(false, 910, 1500);
  assert.equal(h.reply(0)?.stoppedMs, undefined, "voice never stopped it, so no stop is timed");
});

test("a request ID still waiting isn't sent again, and the words come back", async () => {
  const h = harness();
  const first = h.send("Wait");
  h.say("failed", 0);
  await first;
  const retry = h.send("Wait");
  assert.equal(h.sent[1].request_id, h.sent[0].request_id, "the retry repeats the request ID");
  await h.send("Wait");
  assert.deepEqual([h.sent.length, h.givenBack], [2, ["Wait", "Wait"]], "not sent again");
  h.say("done", 1);
  h.say("spoken", 1);
  await retry;
  assert.equal(h.reply(1)?.ended, "done", "the waiting retry still ends");
});

test("another send's success leaves the request ID kept for a send that may have been taken", async () => {
  const h = harness();
  const unsure = h.send("Wait");
  h.say("failed", 0);
  await unsure;
  const other = h.send("Hello");
  h.say("done", 1);
  h.say("spoken", 1);
  await other;
  h.send("Wait");
  assert.equal(h.sent[2].request_id, h.sent[0].request_id, "sent again as the same request");
});

test("a send refused as the session ended gives the words back and tells the page it ended", async () => {
  const givenBack: string[] = [];
  let ended = 0;
  const sender = new Sender(new Hearing(), {
    add: () => {},
    open: () => {},
    hear: () => {},
    update: () => {},
    giveBack: (_you, _reply, words) => givenBack.push(words),
    takeBack: () => {},
    sessionEnded: () => ended++,
  });
  const refused = (type: string): Turns => ({
    interrupts: false,
    take: () => Promise.reject(new ProblemError(409, type, "T", "")),
  });
  await sender.send("Hi", refused("/v1/problems/session-ended"), undefined);
  await sender.send("Again", refused("about:blank"), undefined);
  assert.deepEqual(givenBack, ["Hi", "Again"]);
  assert.equal(ended, 1, "only the session-ended refusal ends the session");
});

test("a turn refused as ticket-expired, or by any other 401, tells the page the session is over", async () => {
  const told: string[] = [];
  const sender = new Sender(new Hearing(), {
    add: () => {},
    open: () => {},
    hear: () => {},
    update: () => {},
    giveBack: () => {},
    takeBack: () => {},
    sessionEnded: (why) => told.push(why),
  });
  const refusedWith = (type: string): Turns => ({
    interrupts: false,
    take: () => Promise.reject(new ProblemError(401, type, "T", "")),
  });
  await sender.send("Hi", refusedWith("/v1/problems/ticket-expired"), undefined);
  await sender.send("Again", refusedWith("about:blank"), undefined);
  assert.deepEqual(told, ["expired", "unusable"]);
});
